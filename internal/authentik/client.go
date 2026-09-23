// Package authentik provisions the Authentik objects behind `vd deploy --auth`.
//
// Every lookup in here is written against what the API was measured to do on
// 2026-09-22, not against what its documentation suggests — the two differ, and
// every difference fails silently. See docs/plans/forward-auth.md §1:
//
//   - Unknown query parameters are ignored, not rejected. providers/proxy/?name=
//     is one of them and returns the whole list, reporting's provider included.
//     So no filter is ever trusted: results are always re-matched client-side.
//   - Application lists are filtered by the policy engine. Once an application is
//     bound to vibe-<app> — a group the service account is not in — it vanishes
//     from core/applications/ even for its creator, while the count still says 1.
//     Applications are therefore looked up by detail (slug), never by list.
//   - Fields the serializer does not know are accepted and dropped. Every object
//     is re-read after it is written and compared field by field.
//   - The embedded outpost's providers list is one shared object. It is only ever
//     patched with the full list, previous members included, and verified after.
package authentik

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	authorizationFlow = "default-provider-authorization-implicit-consent"
	invalidationFlow  = "default-provider-invalidation-flow"

	// VibeInvalidationFlow, when it exists, logs the person out globally like the
	// default and then sends them back to the app's own URL, which starts a fresh
	// sign-in that returns to the app. The default flow ends on Authentik's login
	// page and, after the password, its portal: the outpost's sign_out never
	// passes a post_logout_redirect_uri. The flow is stacks-owned; vd only picks
	// it up, so a host without it keeps working exactly as before.
	VibeInvalidationFlow = "vibe-provider-invalidation-flow"
	embeddedOutpost      = "goauthentik.io/outposts/embedded"

	// DefaultTTL is the owner's decision for vibe apps: non-PHI, no browser tokens,
	// so a week-long assertion buys an SPA that never meets a 302 mid-session.
	// Group removal therefore takes up to this long to bite; deactivating the
	// account is the immediate lever.
	DefaultTTL = "days=7"
)

// groupRe is the only shape of group vd may create. The token holds add but not
// delete on groups — deliberately, or it could remove reporting-platform-prod —
// so a malformed name would be permanent litter in the production directory.
var groupRe = regexp.MustCompile(`^vibe-[a-z][a-z0-9-]{1,62}$`)

var ttlRe = regexp.MustCompile(`^(weeks|days|hours|minutes|seconds)=[0-9]+(;(weeks|days|hours|minutes|seconds)=[0-9]+)*$`)

// GroupName derives the access group for an app. It is the only source of group
// names in vd; anything that does not fit is a bug here, not a configuration.
func GroupName(app string) (string, error) {
	g := "vibe-" + app
	if !groupRe.MatchString(g) {
		return "", fmt.Errorf("refusing group name %q: must match %s", g, groupRe)
	}
	return g, nil
}

// ValidTTL reports whether s is an Authentik timedelta string such as "days=7".
func ValidTTL(s string) bool { return ttlRe.MatchString(s) }

// Client talks to one Authentik installation.
type Client struct {
	api   string // https://host/api/v3
	token string
	http  *http.Client
}

// New takes the public Authentik URL (https://auth.example.com). The admin API is
// reached there rather than over the overlay: it is ordinary HTTPS, it carries no
// forwarded-host semantics, and vd runs as a host process with no overlay access.
func New(publicURL, token string) *Client {
	return &Client{
		api:   strings.TrimRight(publicURL, "/") + "/api/v3",
		token: strings.TrimSpace(token),
		http:  &http.Client{Timeout: 20 * time.Second},
	}
}

// Spec is what one app needs.
type Spec struct {
	App          string // already validated app name
	ExternalHost string // https://<app>.<domain>
	TTL          string // e.g. days=7
}

// Result reports what Ensure did.
type Result struct {
	Group      string
	ProviderPK int
	AppSlug    string
	// InvalidationFlow is the slug the provider ended up with.
	InvalidationFlow string
	// TTLChanged is set when an existing provider's validity was changed. The
	// embedded outpost caches it (seen during reporting's rollout) and may keep
	// the old value until the Authentik server is restarted.
	TTLChanged bool
}

// provider is the subset of a proxy provider vd writes and verifies.
type provider struct {
	PK                  int    `json:"pk,omitempty"`
	Name                string `json:"name"`
	Mode                string `json:"mode"`
	ExternalHost        string `json:"external_host"`
	AccessTokenValidity string `json:"access_token_validity"`
	AuthorizationFlow   string `json:"authorization_flow"`
	InvalidationFlow    string `json:"invalidation_flow"`
	InterceptHeaderAuth bool   `json:"intercept_header_auth"`
}

type application struct {
	PK       string `json:"pk,omitempty"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Provider *int   `json:"provider"`
	// LaunchURL is what the Authentik portal's tile opens, and what
	// application.get_launch_url() returns — the post-logout redirect reads it.
	LaunchURL string `json:"meta_launch_url"`
}

type binding struct {
	PK     string `json:"pk,omitempty"`
	Target string `json:"target"`
	Group  string `json:"group"`
	Order  int    `json:"order"`
}

type outpost struct {
	PK        string `json:"pk"`
	Managed   string `json:"managed"`
	Providers []int  `json:"providers"`
}

// Ensure creates or reconciles everything the app needs, in an order that keeps
// it unreachable until the end: the outpost starts serving the provider last.
func (c *Client) Ensure(s Spec) (*Result, error) {
	group, err := GroupName(s.App)
	if err != nil {
		return nil, err
	}
	if !ValidTTL(s.TTL) {
		return nil, fmt.Errorf("invalid token validity %q (want e.g. days=7 or hours=1)", s.TTL)
	}
	authz, err := c.flowPK(authorizationFlow)
	if err != nil {
		return nil, err
	}
	invalSlug := VibeInvalidationFlow
	inval, err := c.findFlow(invalSlug)
	if err != nil {
		return nil, err
	}
	if inval == "" {
		invalSlug = invalidationFlow
		if inval, err = c.flowPK(invalSlug); err != nil {
			return nil, err
		}
	}

	groupPK, err := c.ensureGroup(group)
	if err != nil {
		return nil, err
	}

	want := provider{
		Name:                group,
		Mode:                "forward_single",
		ExternalHost:        s.ExternalHost,
		AccessTokenValidity: s.TTL,
		AuthorizationFlow:   authz,
		InvalidationFlow:    inval,
		InterceptHeaderAuth: false,
	}
	prov, ttlChanged, err := c.ensureProvider(want)
	if err != nil {
		return nil, err
	}

	app, err := c.ensureApplication(group, prov.PK, s.ExternalHost+"/")
	if err != nil {
		return nil, err
	}
	if err := c.ensureBinding(app.PK, groupPK); err != nil {
		return nil, err
	}
	if err := c.addToOutpost(prov.PK); err != nil {
		return nil, err
	}
	return &Result{Group: group, ProviderPK: prov.PK, AppSlug: group,
		InvalidationFlow: invalSlug, TTLChanged: ttlChanged}, nil
}

// Remove undoes Ensure except for the group: vd holds no delete right on groups,
// and a redeploy must not quietly restore access somebody revoked by hand.
func (c *Client) Remove(app string) error {
	group, err := GroupName(app)
	if err != nil {
		return err
	}
	prov, err := c.findProvider(group)
	if err != nil {
		return err
	}
	if prov != nil {
		if err := c.removeFromOutpost(prov.PK); err != nil {
			return err
		}
	}

	var a application
	switch code, err := c.do("GET", "/core/applications/"+group+"/", nil, &a); {
	case err != nil && code != http.StatusNotFound:
		return err
	case code == http.StatusOK:
		bs, err := c.bindingsFor(a.PK)
		if err != nil {
			return err
		}
		for _, b := range bs {
			if _, err := c.do("DELETE", "/policies/bindings/"+b.PK+"/", nil, nil); err != nil {
				return err
			}
		}
		if _, err := c.do("DELETE", "/core/applications/"+group+"/", nil, nil); err != nil {
			return err
		}
	}

	if prov != nil {
		if _, err := c.do("DELETE", "/providers/proxy/"+strconv.Itoa(prov.PK)+"/", nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// Health describes what exists for an app, for vd status.
type Health struct {
	Group       bool   `json:"group"`
	Provider    bool   `json:"provider"`
	Application bool   `json:"application"`
	InOutpost   bool   `json:"in_outpost"`
	ProviderTTL string `json:"provider_ttl,omitempty"`
}

// OK is true when the app is actually protected and servable.
func (h *Health) OK() bool { return h.Group && h.Provider && h.Application && h.InOutpost }

// Check reads, never writes.
func (c *Client) Check(app string) (*Health, error) {
	group, err := GroupName(app)
	if err != nil {
		return nil, err
	}
	h := &Health{}
	if pk, err := c.findGroup(group); err != nil {
		return nil, err
	} else {
		h.Group = pk != ""
	}
	prov, err := c.findProvider(group)
	if err != nil {
		return nil, err
	}
	if prov != nil {
		h.Provider = true
		h.ProviderTTL = prov.AccessTokenValidity
		o, err := c.embedded()
		if err != nil {
			return nil, err
		}
		h.InOutpost = slices.Contains(o.Providers, prov.PK)
	}
	code, err := c.do("GET", "/core/applications/"+group+"/", nil, nil)
	if err != nil && code != http.StatusNotFound {
		return nil, err
	}
	h.Application = code == http.StatusOK
	return h, nil
}

// --- objects -------------------------------------------------------------

func (c *Client) flowPK(slug string) (string, error) {
	pk, err := c.findFlow(slug)
	if err != nil {
		return "", err
	}
	if pk == "" {
		return "", fmt.Errorf("flow %q not found in Authentik", slug)
	}
	return pk, nil
}

// findFlow returns "" with no error when the flow simply does not exist, so a
// missing optional flow can be told apart from an API failure.
func (c *Client) findFlow(slug string) (string, error) {
	var found string
	err := c.each("/flows/instances/", url.Values{"slug": {slug}}, func(raw json.RawMessage) {
		var f struct {
			PK   string `json:"pk"`
			Slug string `json:"slug"`
		}
		if json.Unmarshal(raw, &f) == nil && f.Slug == slug {
			found = f.PK
		}
	})
	return found, err
}

func (c *Client) findGroup(name string) (string, error) {
	var found string
	err := c.each("/core/groups/", url.Values{"name": {name}}, func(raw json.RawMessage) {
		var g struct {
			PK   string `json:"pk"`
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &g) == nil && g.Name == name {
			found = g.PK
		}
	})
	return found, err
}

func (c *Client) ensureGroup(name string) (string, error) {
	if pk, err := c.findGroup(name); err != nil || pk != "" {
		return pk, err
	}
	// Belt and braces: GroupName already checked, but this call is the one that
	// cannot be undone with the token vd holds.
	if !groupRe.MatchString(name) {
		return "", fmt.Errorf("refusing group name %q", name)
	}
	var g struct {
		PK   string `json:"pk"`
		Name string `json:"name"`
	}
	if _, err := c.do("POST", "/core/groups/", map[string]any{"name": name}, &g); err != nil {
		return "", fmt.Errorf("create group %s: %w", name, err)
	}
	if g.PK == "" || g.Name != name {
		return "", fmt.Errorf("create group %s: unexpected response", name)
	}
	return g.PK, nil
}

func (c *Client) findProvider(name string) (*provider, error) {
	var found *provider
	// name__iexact filters; ?name= on this endpoint is silently ignored. Neither is
	// trusted — the name is compared again below.
	err := c.each("/providers/proxy/", url.Values{"name__iexact": {name}}, func(raw json.RawMessage) {
		var p provider
		if json.Unmarshal(raw, &p) == nil && p.Name == name {
			found = &p
		}
	})
	return found, err
}

func (c *Client) ensureProvider(want provider) (*provider, bool, error) {
	cur, err := c.findProvider(want.Name)
	if err != nil {
		return nil, false, err
	}
	ttlChanged := false
	var got provider
	if cur == nil {
		if _, err := c.do("POST", "/providers/proxy/", want, &got); err != nil {
			return nil, false, fmt.Errorf("create provider %s: %w", want.Name, err)
		}
	} else {
		ttlChanged = cur.AccessTokenValidity != want.AccessTokenValidity
		if _, err := c.do("PATCH", "/providers/proxy/"+strconv.Itoa(cur.PK)+"/", want, &got); err != nil {
			return nil, false, fmt.Errorf("update provider %s: %w", want.Name, err)
		}
	}
	if got.PK == 0 {
		return nil, false, fmt.Errorf("provider %s: response carried no pk", want.Name)
	}

	// Re-read, not trust the write response: that is where dropped fields show.
	var back provider
	if _, err := c.do("GET", "/providers/proxy/"+strconv.Itoa(got.PK)+"/", nil, &back); err != nil {
		return nil, false, err
	}
	want.PK = back.PK
	if back != want {
		return nil, false, fmt.Errorf("provider %s did not take the requested settings:\n  want %+v\n  got  %+v",
			want.Name, want, back)
	}
	return &back, ttlChanged, nil
}

func (c *Client) ensureApplication(slug string, providerPK int, launchURL string) (*application, error) {
	var cur application
	code, err := c.do("GET", "/core/applications/"+slug+"/", nil, &cur)
	if err != nil && code != http.StatusNotFound {
		return nil, err
	}
	want := application{Name: slug, Slug: slug, Provider: &providerPK, LaunchURL: launchURL}
	var got application
	if code == http.StatusNotFound {
		if code, err := c.do("POST", "/core/applications/", want, &got); err != nil {
			if code == http.StatusBadRequest {
				return nil, fmt.Errorf("application slug %q exists but is not readable by vd "+
					"(hidden from its list, or owned by someone else) — resolve in Authentik: %w", slug, err)
			}
			return nil, fmt.Errorf("create application %s: %w", slug, err)
		}
	} else if cur.Provider == nil || *cur.Provider != providerPK || cur.LaunchURL != launchURL {
		patch := map[string]any{"provider": providerPK, "meta_launch_url": launchURL}
		if _, err := c.do("PATCH", "/core/applications/"+slug+"/", patch, &got); err != nil {
			return nil, fmt.Errorf("update application %s: %w", slug, err)
		}
	}

	var back application
	if _, err := c.do("GET", "/core/applications/"+slug+"/", nil, &back); err != nil {
		return nil, err
	}
	if back.Provider == nil || *back.Provider != providerPK {
		return nil, fmt.Errorf("application %s is not bound to provider %d after write", slug, providerPK)
	}
	if back.LaunchURL != launchURL {
		return nil, fmt.Errorf("application %s launch URL is %q after write, want %q", slug, back.LaunchURL, launchURL)
	}
	return &back, nil
}

func (c *Client) bindingsFor(target string) ([]binding, error) {
	var out []binding
	err := c.each("/policies/bindings/", url.Values{"target": {target}}, func(raw json.RawMessage) {
		var b binding
		if json.Unmarshal(raw, &b) == nil && b.Target == target {
			out = append(out, b)
		}
	})
	return out, err
}

func (c *Client) ensureBinding(appPK, groupPK string) error {
	bs, err := c.bindingsFor(appPK)
	if err != nil {
		return err
	}
	for _, b := range bs {
		if b.Group == groupPK {
			return nil
		}
	}
	var got binding
	if _, err := c.do("POST", "/policies/bindings/", binding{Target: appPK, Group: groupPK, Order: 0}, &got); err != nil {
		return fmt.Errorf("bind application to group: %w", err)
	}
	if got.Target != appPK || got.Group != groupPK {
		return fmt.Errorf("group binding did not take: got %+v", got)
	}
	return nil
}

func (c *Client) embedded() (*outpost, error) {
	var found *outpost
	err := c.each("/outposts/instances/", nil, func(raw json.RawMessage) {
		var o outpost
		if json.Unmarshal(raw, &o) == nil && o.Managed == embeddedOutpost {
			found = &o
		}
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("embedded outpost not found (managed=%s)", embeddedOutpost)
	}
	return found, nil
}

// setOutpostProviders writes the full list and proves nobody else was dropped.
func (c *Client) setOutpostProviders(o *outpost, next []int) error {
	if _, err := c.do("PATCH", "/outposts/instances/"+o.PK+"/", map[string]any{"providers": next}, nil); err != nil {
		return fmt.Errorf("update embedded outpost: %w", err)
	}
	back, err := c.embedded()
	if err != nil {
		return err
	}
	for _, pk := range next {
		if !slices.Contains(back.Providers, pk) {
			return fmt.Errorf("embedded outpost lost provider %d after update (now %v)", pk, back.Providers)
		}
	}
	return nil
}

func (c *Client) addToOutpost(pk int) error {
	o, err := c.embedded()
	if err != nil {
		return err
	}
	if slices.Contains(o.Providers, pk) {
		return nil
	}
	return c.setOutpostProviders(o, append(slices.Clone(o.Providers), pk))
}

func (c *Client) removeFromOutpost(pk int) error {
	o, err := c.embedded()
	if err != nil {
		return err
	}
	if !slices.Contains(o.Providers, pk) {
		return nil
	}
	next := slices.DeleteFunc(slices.Clone(o.Providers), func(p int) bool { return p == pk })
	return c.setOutpostProviders(o, next)
}

// --- transport -----------------------------------------------------------

// each walks every page. Authentik paginates by page number and reports the next
// one in pagination.next, 0 when there is none.
func (c *Client) each(path string, q url.Values, fn func(json.RawMessage)) error {
	if q == nil {
		q = url.Values{}
	}
	for page := 1; page != 0; {
		q.Set("page", strconv.Itoa(page))
		var resp struct {
			Pagination struct {
				Next int `json:"next"`
			} `json:"pagination"`
			Results []json.RawMessage `json:"results"`
		}
		if _, err := c.do("GET", path+"?"+q.Encode(), nil, &resp); err != nil {
			return err
		}
		for _, r := range resp.Results {
			fn(r)
		}
		if resp.Pagination.Next <= page {
			break
		}
		page = resp.Pagination.Next
	}
	return nil
}

// do returns the status code alongside any error, so callers can tell 404 apart.
func (c *Client) do(method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.api+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet := strings.TrimSpace(string(data))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, snippet)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: decode: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}
