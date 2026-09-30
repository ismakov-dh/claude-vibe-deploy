package authentik

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// MCP resources: a vibe app's read-only database MCP, behind agentgateway and
// Authentik instead of Basic credentials. One OAuth2 provider per resource —
// agentgateway's mcpAuthentication pins one issuer, audience and client per
// route — mirroring stacks' mcp-db blueprint (dev-ops/stacks
// stacks/authentik-test/config/mcp.yaml, phase 0 measured 2026-09-29).

const (
	mcpSigningKey    = "authentik Self-signed Certificate"
	mcpScopeName     = "mcp"        // stacks-owned mapping: only mcp-* groups in the token
	mcpScopeMapping  = "mcp-groups" // referenced by name, never created by vd
	mcpTokenValidity = "minutes=5"  // revocation delay equals this; the owner's decision for MCP
)

var mcpGroupRe = regexp.MustCompile(`^mcp-vibe-[a-z][a-z0-9-]{1,62}$`)

// MCPName is the group, provider, client id, application slug and audience of
// an app's MCP resource — one name for all, as for mcp-db.
func MCPName(app string) (string, error) {
	n := "mcp-vibe-" + app
	if !mcpGroupRe.MatchString(n) {
		return "", fmt.Errorf("refusing MCP name %q: must match %s", n, mcpGroupRe)
	}
	return n, nil
}

// MCPSpec is what one app's MCP resource needs.
type MCPSpec struct {
	App      string
	Resource string // https://<app>.mcp.<domain>/mcp
	Owner    string // optional email; added to the group
}

// MCPResult reports what EnsureMCP did.
type MCPResult struct {
	Name   string // group = client id = audience = slug
	Issuer string
	// OwnerAdded is true when Owner was found and is now a member.
	OwnerAdded bool
	// OwnerMissing is set when Owner has no account in this Authentik.
	OwnerMissing bool
	// OwnerError is why the owner could not be added; the resource still works.
	OwnerError string
}

type oauth2Provider struct {
	PK                   int           `json:"pk,omitempty"`
	Name                 string        `json:"name"`
	ClientType           string        `json:"client_type"`
	ClientID             string        `json:"client_id"`
	AuthorizationFlow    string        `json:"authorization_flow"`
	InvalidationFlow     string        `json:"invalidation_flow"`
	SigningKey           string        `json:"signing_key"`
	PropertyMappings     []string      `json:"property_mappings"`
	RedirectURIs         []redirectURI `json:"redirect_uris"`
	SubMode              string        `json:"sub_mode"`
	IncludeClaimsInToken bool          `json:"include_claims_in_id_token"`
	AccessTokenValidity  string        `json:"access_token_validity"`
	AccessCodeValidity   string        `json:"access_code_validity"`
	RefreshTokenValidity string        `json:"refresh_token_validity"`
	IssuerMode           string        `json:"issuer_mode"`
	// GrantTypes must be sent: the API stores [] when it is omitted, and an
	// empty list makes every authorize request invalid_request.
	GrantTypes []string `json:"grant_types"`
}

type redirectURI struct {
	MatchingMode string `json:"matching_mode"`
	URL          string `json:"url"`
	Type         string `json:"redirect_uri_type,omitempty"`
}

// mcpRedirects are the callbacks of MCP clients: local loopback for Claude Code
// and other CLIs, claude.ai for the web and desktop apps. Never the upstream
// tutorial's `.*` matcher.
var mcpRedirects = []redirectURI{
	{MatchingMode: "regex", URL: `^http://(127\.0\.0\.1|localhost):[0-9]{1,5}/(callback|oauth/callback)$`, Type: "authorization"},
	{MatchingMode: "strict", URL: "https://claude.ai/api/mcp/auth_callback", Type: "authorization"},
	{MatchingMode: "strict", URL: "https://claude.com/api/mcp/auth_callback", Type: "authorization"},
}

// Issuer is the per-provider issuer URL agentgateway validates against.
func (c *Client) Issuer(slug string) string {
	return strings.TrimSuffix(c.api, "/api/v3") + "/application/o/" + slug + "/"
}

// EnsureMCP creates or reconciles an app's MCP resource: group, OAuth2 provider,
// application and group binding, and optionally puts the owner in the group.
// The gateway route is written by the caller only after this succeeds, so a
// failure here leaves the resource unpublished.
func (c *Client) EnsureMCP(s MCPSpec) (*MCPResult, error) {
	name, err := MCPName(s.App)
	if err != nil {
		return nil, err
	}
	authz, err := c.flowPK(authorizationFlow)
	if err != nil {
		return nil, err
	}
	inval, err := c.flowPK(invalidationFlow)
	if err != nil {
		return nil, err
	}
	key, err := c.keypairPK(mcpSigningKey)
	if err != nil {
		return nil, err
	}
	mappings, err := c.mcpScopeMappings()
	if err != nil {
		return nil, err
	}
	groupPK, err := c.ensureMCPGroup(name)
	if err != nil {
		return nil, err
	}

	want := oauth2Provider{
		Name: name, ClientType: "public", ClientID: name,
		AuthorizationFlow: authz, InvalidationFlow: inval, SigningKey: key,
		PropertyMappings: mappings, RedirectURIs: mcpRedirects,
		SubMode: "user_uuid", IncludeClaimsInToken: true, IssuerMode: "per_provider",
		AccessTokenValidity: mcpTokenValidity, AccessCodeValidity: "minutes=1",
		RefreshTokenValidity: "hours=12",
		GrantTypes:           []string{"authorization_code", "refresh_token"},
	}
	prov, err := c.ensureOAuth2Provider(want)
	if err != nil {
		return nil, err
	}

	// Application first without a provider, then the binding, then the
	// provider: an application with a provider and no binding admits every
	// signed-in account, and that state must not exist even between two calls.
	app, created, err := c.ensureBareApplication(name)
	if err != nil {
		return nil, err
	}
	if err := c.ensureBinding(app.PK, groupPK); err != nil {
		// Unbound means open to any signed-in account. There is no outpost here —
		// the gateway route is written only after success — so the undo is the
		// application this run created.
		notes := []string{"group binding failed: " + err.Error()}
		if created {
			if _, derr := c.do("DELETE", "/core/applications/"+name+"/", nil, nil); derr != nil {
				notes = append(notes, "could not delete the application created in this run: "+derr.Error())
			} else {
				notes = append(notes, "application created in this run was deleted")
			}
		}
		return nil, fmt.Errorf("%s", strings.Join(notes, "; "))
	}
	if _, _, err := c.ensureApplication(name, prov.PK, ""); err != nil {
		return nil, err
	}

	// The owner is a convenience, not part of the protection: a failed lookup
	// leaves a working, empty-group resource and is reported, not fatal.
	res := &MCPResult{Name: name, Issuer: c.Issuer(name)}
	if s.Owner != "" {
		switch pk, err := c.userPKByEmail(s.Owner); {
		case err != nil:
			res.OwnerError = err.Error()
		case pk == 0:
			res.OwnerMissing = true
		default:
			if _, err := c.do("POST", "/core/groups/"+groupPK+"/add_user/", map[string]any{"pk": pk}, nil); err != nil {
				res.OwnerError = fmt.Sprintf("add owner to %s: %v", name, err)
			} else {
				res.OwnerAdded = true
			}
		}
	}
	return res, nil
}

// ensureBareApplication returns the application, creating it with no provider
// if it does not exist. With no provider it grants nothing.
func (c *Client) ensureBareApplication(slug string) (*application, bool, error) {
	var cur application
	code, err := c.do("GET", "/core/applications/"+slug+"/", nil, &cur)
	if err == nil {
		return &cur, false, nil
	}
	if code != http.StatusNotFound {
		return nil, false, err
	}
	var got application
	if code, err := c.do("POST", "/core/applications/", application{Name: slug, Slug: slug}, &got); err != nil {
		if code == http.StatusBadRequest {
			return nil, false, fmt.Errorf("application slug %q exists but is not readable by vd — resolve in Authentik: %w", slug, err)
		}
		return nil, false, fmt.Errorf("create application %s: %w", slug, err)
	}
	if got.PK == "" {
		return nil, false, fmt.Errorf("create application %s: no pk in the response", slug)
	}
	return &got, true, nil
}

// RemoveMCP undoes EnsureMCP, group included. Order matters: the application
// goes first (its bindings go with it), and nothing else is touched unless that
// worked — a provider-less application or one without its group is exactly the
// open state EnsureMCP avoids. The group is deleted, not kept as --auth keeps
// its own: a kept one would hand the old members to the next app that takes
// the name. A group vd did not create is refused before anything is deleted.
func (c *Client) RemoveMCP(app string) error {
	name, err := MCPName(app)
	if err != nil {
		return err
	}
	groupPK, managed, err := c.findMCPGroup(name)
	if err != nil {
		return err
	}
	if groupPK != "" && !managed {
		return fmt.Errorf("group %s was not created by vd (no %s attribute); nothing removed — resolve in Authentik", name, vdManagedAttr)
	}
	var a application
	switch code, err := c.do("GET", "/core/applications/"+name+"/", nil, &a); {
	case err != nil && code != http.StatusNotFound:
		return err
	case code == http.StatusOK:
		if _, err := c.do("DELETE", "/core/applications/"+name+"/", nil, nil); err != nil {
			return fmt.Errorf("delete application %s (provider and group left alone): %w", name, err)
		}
	}
	var errs []error
	if p, err := c.findOAuth2Provider(name); err != nil {
		errs = append(errs, err)
	} else if p != nil {
		if _, err := c.do("DELETE", "/providers/oauth2/"+strconv.Itoa(p.PK)+"/", nil, nil); err != nil {
			errs = append(errs, fmt.Errorf("delete provider %s: %w", name, err))
		}
	}
	if groupPK != "" {
		if _, err := c.do("DELETE", "/core/groups/"+groupPK+"/", nil, nil); err != nil {
			errs = append(errs, fmt.Errorf("delete group %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// MCPHealth describes an app's MCP resource, for vd status.
type MCPHealth struct {
	Group       bool `json:"group"`
	Provider    bool `json:"provider"`
	Application bool `json:"application"`
	Binding     bool `json:"binding"`
}

func (h *MCPHealth) OK() bool { return h.Group && h.Provider && h.Application && h.Binding }

// CheckMCP reads, never writes.
func (c *Client) CheckMCP(app string) (*MCPHealth, error) {
	name, err := MCPName(app)
	if err != nil {
		return nil, err
	}
	h := &MCPHealth{}
	groupPK, _, err := c.findGroup(name)
	if err != nil {
		return nil, err
	}
	h.Group = groupPK != ""
	p, err := c.findOAuth2Provider(name)
	if err != nil {
		return nil, err
	}
	h.Provider = p != nil
	var a application
	code, err := c.do("GET", "/core/applications/"+name+"/", nil, &a)
	if err != nil && code != http.StatusNotFound {
		return nil, err
	}
	h.Application = code == http.StatusOK
	if h.Application && h.Group {
		bs, err := c.bindingsFor(a.PK)
		if err != nil {
			return nil, err
		}
		for _, b := range bs {
			if b.guards(groupPK) {
				h.Binding = true
			}
		}
	}
	return h, nil
}

// --- objects -------------------------------------------------------------

func (c *Client) findOAuth2Provider(name string) (*oauth2Provider, error) {
	var found *oauth2Provider
	// The list has no name filter; client_id does filter, and vd sets it to the
	// name. Re-matched client-side all the same.
	err := c.each("/providers/oauth2/", url.Values{"client_id": {name}}, func(raw json.RawMessage) {
		var p oauth2Provider
		if json.Unmarshal(raw, &p) == nil && p.Name == name && p.ClientID == name {
			found = &p
		}
	})
	return found, err
}

func (c *Client) ensureOAuth2Provider(want oauth2Provider) (*oauth2Provider, error) {
	cur, err := c.findOAuth2Provider(want.Name)
	if err != nil {
		return nil, err
	}
	var got oauth2Provider
	if cur == nil {
		if _, err := c.do("POST", "/providers/oauth2/", want, &got); err != nil {
			return nil, fmt.Errorf("create OAuth2 provider %s: %w", want.Name, err)
		}
	} else if _, err := c.do("PATCH", "/providers/oauth2/"+strconv.Itoa(cur.PK)+"/", want, &got); err != nil {
		return nil, fmt.Errorf("update OAuth2 provider %s: %w", want.Name, err)
	}
	if got.PK == 0 {
		return nil, fmt.Errorf("OAuth2 provider %s: response carried no pk", want.Name)
	}
	var back oauth2Provider
	if _, err := c.do("GET", "/providers/oauth2/"+strconv.Itoa(got.PK)+"/", nil, &back); err != nil {
		return nil, err
	}
	if msg := oauth2Drift(want, back); msg != "" {
		return nil, fmt.Errorf("OAuth2 provider %s did not take the requested settings: %s", want.Name, msg)
	}
	return &back, nil
}

// oauth2Drift compares the fields vd sets. Redirect URIs are compared by url and
// matching mode; the type field may be defaulted by the server.
func oauth2Drift(want, got oauth2Provider) string {
	var bad []string
	chk := func(field, w, g string) {
		if w != g {
			bad = append(bad, fmt.Sprintf("%s=%q (want %q)", field, g, w))
		}
	}
	chk("client_type", want.ClientType, got.ClientType)
	chk("client_id", want.ClientID, got.ClientID)
	chk("authorization_flow", want.AuthorizationFlow, got.AuthorizationFlow)
	chk("invalidation_flow", want.InvalidationFlow, got.InvalidationFlow)
	chk("signing_key", want.SigningKey, got.SigningKey)
	chk("sub_mode", want.SubMode, got.SubMode)
	chk("access_token_validity", want.AccessTokenValidity, got.AccessTokenValidity)
	chk("issuer_mode", want.IssuerMode, got.IssuerMode)
	if !sameSet(want.GrantTypes, got.GrantTypes) {
		bad = append(bad, fmt.Sprintf("grant_types=%v (want %v)", got.GrantTypes, want.GrantTypes))
	}
	if !sameSet(want.PropertyMappings, got.PropertyMappings) {
		bad = append(bad, fmt.Sprintf("property_mappings=%v (want %v)", got.PropertyMappings, want.PropertyMappings))
	}
	key := func(rs []redirectURI) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.MatchingMode+" "+r.URL)
		}
		return out
	}
	if !sameSet(key(want.RedirectURIs), key(got.RedirectURIs)) {
		bad = append(bad, fmt.Sprintf("redirect_uris=%v (want %v)", key(got.RedirectURIs), key(want.RedirectURIs)))
	}
	return strings.Join(bad, "; ")
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		if m[x] == 0 {
			return false
		}
		m[x]--
	}
	return true
}

func (c *Client) keypairPK(name string) (string, error) {
	var found string
	// No name filter on this list: walk it and match.
	err := c.each("/crypto/certificatekeypairs/", nil, func(raw json.RawMessage) {
		var k struct {
			PK   string `json:"pk"`
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &k) == nil && k.Name == name {
			found = k.PK
		}
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("signing key %q not found in Authentik", name)
	}
	return found, nil
}

// mcpScopeMappings returns the managed openid and offline_access mappings plus
// stacks' mcp-groups mapping, which vd references and never creates.
func (c *Client) mcpScopeMappings() ([]string, error) {
	var openid, offline, mcp string
	err := c.each("/propertymappings/provider/scope/", nil, func(raw json.RawMessage) {
		var m struct {
			PK        string `json:"pk"`
			Name      string `json:"name"`
			Managed   string `json:"managed"`
			ScopeName string `json:"scope_name"`
		}
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		switch {
		case m.Managed == "goauthentik.io/providers/oauth2/scope-openid":
			openid = m.PK
		case m.Managed == "goauthentik.io/providers/oauth2/scope-offline_access":
			offline = m.PK
		case m.Name == mcpScopeMapping && m.ScopeName == mcpScopeName:
			mcp = m.PK
		}
	})
	if err != nil {
		return nil, err
	}
	if openid == "" || offline == "" {
		return nil, fmt.Errorf("managed openid/offline_access scope mappings not found")
	}
	if mcp == "" {
		return nil, fmt.Errorf("scope mapping %q (scope %q) not found — it is created by the platform admins, not vd",
			mcpScopeMapping, mcpScopeName)
	}
	return []string{openid, offline, mcp}, nil
}

// userPKByEmail finds exactly one account by exact email and returns only its
// pk; 0 when there is none. Nothing else from the directory is kept or logged
// — the token can read the directory, this code deliberately does not.
func (c *Client) userPKByEmail(email string) (int, error) {
	email = strings.TrimSpace(email)
	var pks []int
	err := c.each("/core/users/", url.Values{"email": {email}}, func(raw json.RawMessage) {
		var u struct {
			PK    int    `json:"pk"`
			Email string `json:"email"`
		}
		if json.Unmarshal(raw, &u) == nil && strings.EqualFold(u.Email, email) {
			pks = append(pks, u.PK)
		}
	})
	if err != nil {
		return 0, fmt.Errorf("look up owner: %w", err)
	}
	switch len(pks) {
	case 0:
		return 0, nil
	case 1:
		return pks[0], nil
	default:
		return 0, fmt.Errorf("owner email matches %d accounts — refusing to guess; ask a platform admin", len(pks))
	}
}

// vdManagedAttr marks groups vd created. MCP groups are only ever adopted or
// deleted with it: a same-named group made by someone else would otherwise
// lend its members to the app, or lose them on destroy.
const vdManagedAttr = "vd_managed"

// findMCPGroup reports the group's pk and whether vd created it.
func (c *Client) findMCPGroup(name string) (pk string, managed bool, err error) {
	err = c.each("/core/groups/", url.Values{"name": {name}}, func(raw json.RawMessage) {
		var g struct {
			PK         string         `json:"pk"`
			Name       string         `json:"name"`
			Attributes map[string]any `json:"attributes"`
		}
		if json.Unmarshal(raw, &g) == nil && g.Name == name {
			pk, managed = g.PK, g.Attributes[vdManagedAttr] == true
		}
	})
	return pk, managed, err
}

// ensureMCPGroup returns the pk of vd's group name, creating it marked.
func (c *Client) ensureMCPGroup(name string) (string, error) {
	if !mcpGroupRe.MatchString(name) {
		return "", fmt.Errorf("refusing group name %q", name)
	}
	pk, managed, err := c.findMCPGroup(name)
	if err != nil {
		return "", err
	}
	if pk != "" && !managed {
		return "", fmt.Errorf("group %s exists but was not created by vd (no %s attribute); refusing to adopt its members — rename or remove it in Authentik", name, vdManagedAttr)
	}
	if pk == "" {
		body := map[string]any{"name": name, "attributes": map[string]any{vdManagedAttr: true}}
		if _, err := c.do("POST", "/core/groups/", body, nil); err != nil {
			return "", fmt.Errorf("create group %s: %w", name, err)
		}
		// Re-read: a serializer that dropped the attribute would make vd refuse
		// its own group on the next deploy, and never delete it.
		if pk, managed, err = c.findMCPGroup(name); err != nil {
			return "", err
		}
		if pk == "" || !managed {
			return "", fmt.Errorf("create group %s: not found with %s after write", name, vdManagedAttr)
		}
	}
	return pk, nil
}
