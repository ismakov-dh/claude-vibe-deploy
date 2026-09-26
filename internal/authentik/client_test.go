package authentik

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fake reproduces the Authentik behaviours measured on test on 2026-09-22 —
// the ones that fail silently. It is deliberately hostile: it ignores every
// list filter except on groups and flows, hides bound applications from lists,
// and paginates bindings one per page.
type fake struct {
	mu        sync.Mutex
	groups    map[string]string // name -> pk
	providers map[int]map[string]any
	apps      map[string]map[string]any // slug -> app
	bindings  []map[string]any
	outpost   []int
	nextPK    int
	calls     []string

	// dropField, if set, is silently not stored on provider writes — the way
	// sub_mode is dropped by the real proxy serializer.
	dropField string

	// vibeFlow makes vibe-provider-invalidation-flow exist.
	vibeFlow bool

	// groupUsers are member pks reported for a group, by name.
	groupUsers map[string][]int

	// onOutpostGet runs on every read of the outpost list, to simulate a writer
	// that is not vd changing it between vd's reads.
	onOutpostGet func(n int)
	outpostGets  int

	// failBindings makes creating or patching a policy binding fail.
	failBindings bool
}

func newFake() *fake {
	return &fake{
		groups: map[string]string{"reporting-platform": "g-rp"},
		providers: map[int]map[string]any{
			// Someone else's provider. Any lookup that trusts a filter finds it.
			2: {"pk": 2, "name": "reporting-test-forward-auth", "mode": "forward_single",
				"external_host": "https://api.test.example.com", "access_token_validity": "hours=1",
				"authorization_flow": "f-authz", "invalidation_flow": "f-inval", "intercept_header_auth": false},
		},
		apps:    map[string]map[string]any{},
		outpost: []int{2},
		nextPK:  10,
	}
}

func page(results []any, next int) map[string]any {
	return map[string]any{"pagination": map[string]any{"next": next, "count": len(results)}, "results": results}
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/api/v3")
	f.calls = append(f.calls, r.Method+" "+p)
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	out := func(code int, v any) {
		w.WriteHeader(code)
		if v != nil {
			json.NewEncoder(w).Encode(v)
		}
	}

	switch {
	case p == "/flows/instances/":
		slug := r.URL.Query().Get("slug")
		flows := map[string]string{authorizationFlow: "f-authz", invalidationFlow: "f-inval"}
		if f.vibeFlow {
			flows[VibeInvalidationFlow] = "f-vibe"
		}
		pk := flows[slug]
		if pk == "" {
			out(200, page(nil, 0))
			return
		}
		out(200, page([]any{map[string]any{"pk": pk, "slug": slug}}, 0))

	case p == "/core/groups/" && r.Method == "GET":
		name := r.URL.Query().Get("name")
		var res []any
		if pk, ok := f.groups[name]; ok {
			users := f.groupUsers[name]
			if users == nil {
				users = []int{}
			}
			res = append(res, map[string]any{"pk": pk, "name": name, "users": users})
		}
		out(200, page(res, 0))
	case p == "/core/groups/" && r.Method == "POST":
		name := body["name"].(string)
		f.nextPK++
		pk := "g-" + strconv.Itoa(f.nextPK)
		f.groups[name] = pk
		out(201, map[string]any{"pk": pk, "name": name})

	case p == "/providers/proxy/" && r.Method == "GET":
		// Every filter ignored: the whole list, stranger first.
		var res []any
		for _, pk := range []int{2, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20} {
			if v, ok := f.providers[pk]; ok {
				res = append(res, v)
			}
		}
		out(200, page(res, 0))
	case p == "/providers/proxy/" && r.Method == "POST":
		f.nextPK++
		body["pk"] = f.nextPK
		f.store(body)
		out(201, f.providers[f.nextPK])
	case strings.HasPrefix(p, "/providers/proxy/"):
		pk, _ := strconv.Atoi(strings.Trim(strings.TrimPrefix(p, "/providers/proxy/"), "/"))
		cur, ok := f.providers[pk]
		if !ok {
			out(404, map[string]any{"detail": "Not found."})
			return
		}
		switch r.Method {
		case "GET":
			out(200, cur)
		case "PATCH":
			for k, v := range body {
				cur[k] = v
			}
			cur["pk"] = pk
			f.store(cur)
			out(200, f.providers[pk])
		case "DELETE":
			delete(f.providers, pk)
			out(204, nil)
		}

	case p == "/core/applications/" && r.Method == "GET":
		// Policy-filtered: bound applications are invisible, counts are not.
		out(200, map[string]any{"pagination": map[string]any{"next": 0, "count": len(f.apps)}, "results": []any{}})
	case p == "/core/applications/" && r.Method == "POST":
		slug := body["slug"].(string)
		if _, ok := f.apps[slug]; ok {
			out(400, map[string]any{"slug": []string{"Application with this slug already exists."}})
			return
		}
		f.nextPK++
		body["pk"] = "a-" + strconv.Itoa(f.nextPK)
		f.apps[slug] = body
		out(201, body)
	case strings.HasPrefix(p, "/core/applications/"):
		slug := strings.Trim(strings.TrimPrefix(p, "/core/applications/"), "/")
		cur, ok := f.apps[slug]
		if !ok {
			out(404, map[string]any{"detail": "Not found."})
			return
		}
		switch r.Method {
		case "GET":
			out(200, cur)
		case "PATCH":
			for k, v := range body {
				cur[k] = v
			}
			out(200, cur)
		case "DELETE":
			delete(f.apps, slug)
			out(204, nil)
		}

	case p == "/policies/bindings/" && r.Method == "GET":
		// target filter ignored, one binding per page: the client must walk pages
		// and match the target itself.
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if n < 1 {
			n = 1
		}
		if n > len(f.bindings) {
			out(200, page(nil, 0))
			return
		}
		next := n + 1
		if next > len(f.bindings) {
			next = 0
		}
		out(200, page([]any{f.bindings[n-1]}, next))
	case p == "/policies/bindings/" && r.Method == "POST" && f.failBindings:
		out(500, map[string]any{"detail": "fake: binding refused"})
	case p == "/policies/bindings/" && r.Method == "POST":
		f.nextPK++
		body["pk"] = "b-" + strconv.Itoa(f.nextPK)
		f.bindings = append(f.bindings, body)
		out(201, body)
	case strings.HasPrefix(p, "/policies/bindings/") && r.Method == "PATCH":
		if f.failBindings {
			out(500, map[string]any{"detail": "fake: binding refused"})
			return
		}
		pk := strings.Trim(strings.TrimPrefix(p, "/policies/bindings/"), "/")
		for _, b := range f.bindings {
			if b["pk"] == pk {
				for k, v := range body {
					b[k] = v
				}
				out(200, b)
				return
			}
		}
		out(404, map[string]any{"detail": "Not found."})
	case strings.HasPrefix(p, "/policies/bindings/") && r.Method == "DELETE":
		pk := strings.Trim(strings.TrimPrefix(p, "/policies/bindings/"), "/")
		for i, b := range f.bindings {
			if b["pk"] == pk {
				f.bindings = append(f.bindings[:i], f.bindings[i+1:]...)
				break
			}
		}
		out(204, nil)

	case p == "/outposts/instances/" && r.Method == "GET":
		f.outpostGets++
		if f.onOutpostGet != nil {
			f.onOutpostGet(f.outpostGets)
		}
		out(200, page([]any{
			map[string]any{"pk": "o-ldap", "managed": nil, "providers": []int{99}},
			map[string]any{"pk": "o-emb", "managed": embeddedOutpost, "providers": f.outpost},
		}, 0))
	case p == "/outposts/instances/o-emb/" && r.Method == "PATCH":
		var next []int
		for _, v := range body["providers"].([]any) {
			next = append(next, int(v.(float64)))
		}
		f.outpost = next
		out(200, map[string]any{"pk": "o-emb", "providers": next})

	default:
		out(500, map[string]any{"detail": "fake: unhandled " + r.Method + " " + p})
	}
}

func (f *fake) store(v map[string]any) {
	pk := int(toFloat(v["pk"]))
	if f.dropField != "" {
		delete(v, f.dropField)
		// Dropped fields read back as the serializer default.
		if f.dropField == "intercept_header_auth" {
			v["intercept_header_auth"] = true
		}
	}
	f.providers[pk] = v
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}

func (f *fake) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func setup(t *testing.T) (*fake, *Client) {
	t.Helper()
	f := newFake()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, New(srv.URL, "t0ken")
}

func spec() Spec {
	return Spec{App: "demo", ExternalHost: "https://demo.apps.example.com", TTL: DefaultTTL}
}

func TestEnsureCreatesEverythingAndKeepsOtherOutpostMembers(t *testing.T) {
	f, c := setup(t)
	res, err := c.Ensure(spec())
	if err != nil {
		t.Fatal(err)
	}
	if res.Group != "vibe-demo" || res.AppSlug != "vibe-demo" {
		t.Fatalf("names: %+v", res)
	}
	if _, ok := f.groups["vibe-demo"]; !ok {
		t.Fatal("group not created")
	}
	// The stranger's provider must be untouched, not matched by the ignored filter.
	if got := f.providers[2]["access_token_validity"]; got != "hours=1" {
		t.Fatalf("stranger provider modified: %v", got)
	}
	if f.count("PATCH /providers/proxy/2/") != 0 {
		t.Fatal("patched a provider vd does not own")
	}
	// Outpost: previous member kept, ours added.
	if len(f.outpost) != 2 || f.outpost[0] != 2 || f.outpost[1] != res.ProviderPK {
		t.Fatalf("outpost providers = %v, want [2 %d]", f.outpost, res.ProviderPK)
	}
	if len(f.bindings) != 1 || f.bindings[0]["group"] != f.groups["vibe-demo"] {
		t.Fatalf("bindings = %v", f.bindings)
	}
	// The portal tile, and the post-logout redirect, open the app itself.
	if got := f.apps["vibe-demo"]["meta_launch_url"]; got != "https://demo.apps.example.com/" {
		t.Fatalf("meta_launch_url = %v", got)
	}
}

// An application created before launch URLs were set gets one on the next deploy.
func TestEnsureBackfillsLaunchURL(t *testing.T) {
	f, c := setup(t)
	if _, err := c.Ensure(spec()); err != nil {
		t.Fatal(err)
	}
	f.apps["vibe-demo"]["meta_launch_url"] = ""
	if _, err := c.Ensure(spec()); err != nil {
		t.Fatal(err)
	}
	if got := f.apps["vibe-demo"]["meta_launch_url"]; got != "https://demo.apps.example.com/" {
		t.Fatalf("launch URL not backfilled: %v", got)
	}
}

func TestEnsureIsIdempotentDespiteHiddenApplications(t *testing.T) {
	f, c := setup(t)
	if _, err := c.Ensure(spec()); err != nil {
		t.Fatal(err)
	}
	posts := func() int {
		return f.count("POST ")
	}
	before := posts()
	outpostPatches := f.count("PATCH /outposts/")

	// Second run: the application is invisible in the list (policy-filtered) but
	// exists. Nothing may be created again, and the outpost must not be rewritten.
	if _, err := c.Ensure(spec()); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if posts() != before {
		t.Fatalf("second Ensure created objects: %v", f.calls)
	}
	if f.count("PATCH /outposts/") != outpostPatches {
		t.Fatal("second Ensure rewrote the outpost list")
	}
	if len(f.bindings) != 1 {
		t.Fatalf("binding duplicated: %v", f.bindings)
	}
}

func TestEnsureFindsBindingOnLaterPage(t *testing.T) {
	f, c := setup(t)
	// Two unrelated bindings first, so ours lands on page 3.
	f.bindings = []map[string]any{
		{"pk": "b-x", "target": "a-other", "group": "g-rp", "order": 0},
		{"pk": "b-y", "target": "a-other2", "group": "g-rp", "order": 0},
	}
	if _, err := c.Ensure(spec()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ensure(spec()); err != nil {
		t.Fatal(err)
	}
	if len(f.bindings) != 3 {
		t.Fatalf("expected 3 bindings (ours once), got %d", len(f.bindings))
	}
}

func TestEnsureRejectsDroppedProviderField(t *testing.T) {
	f, c := setup(t)
	f.dropField = "intercept_header_auth"
	_, err := c.Ensure(spec())
	if err == nil || !strings.Contains(err.Error(), "did not take") {
		t.Fatalf("want verification failure, got %v", err)
	}
	// Failing before the outpost step keeps the app unpublished.
	if len(f.outpost) != 1 {
		t.Fatalf("outpost touched after failed verification: %v", f.outpost)
	}
}

func TestTTLChangeIsReported(t *testing.T) {
	_, c := setup(t)
	if _, err := c.Ensure(spec()); err != nil {
		t.Fatal(err)
	}
	s := spec()
	s.TTL = "hours=4"
	res, err := c.Ensure(s)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TTLChanged {
		t.Fatal("TTL change not reported")
	}
}

func TestRemoveKeepsGroupAndOtherOutpostMembers(t *testing.T) {
	f, c := setup(t)
	res, err := c.Ensure(spec())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Remove("demo"); err != nil {
		t.Fatal(err)
	}
	if len(f.outpost) != 1 || f.outpost[0] != 2 {
		t.Fatalf("outpost after remove = %v, want [2]", f.outpost)
	}
	if _, ok := f.providers[res.ProviderPK]; ok {
		t.Fatal("provider not deleted")
	}
	if _, ok := f.providers[2]; !ok {
		t.Fatal("stranger provider deleted")
	}
	if _, ok := f.apps["vibe-demo"]; ok {
		t.Fatal("application not deleted")
	}
	if len(f.bindings) != 0 {
		t.Fatalf("bindings left: %v", f.bindings)
	}
	if _, ok := f.groups["vibe-demo"]; !ok {
		t.Fatal("group deleted — vd must never delete groups")
	}
	if f.count("DELETE /core/groups/") != 0 {
		t.Fatal("attempted to delete a group")
	}
	// Removing twice is harmless.
	if err := c.Remove("demo"); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

func TestCheck(t *testing.T) {
	_, c := setup(t)
	h, err := c.Check("demo")
	if err != nil {
		t.Fatal(err)
	}
	if h.OK() {
		t.Fatal("nothing exists yet, but Check says OK")
	}
	if _, err := c.Ensure(spec()); err != nil {
		t.Fatal(err)
	}
	if h, _ = c.Check("demo"); !h.OK() || h.ProviderTTL != DefaultTTL {
		t.Fatalf("after Ensure: %+v", h)
	}
}

func TestGroupNameValidation(t *testing.T) {
	for _, bad := range []string{"", "a", "Demo", "1app", "app_x", "app/x", "../x", "x y",
		strings.Repeat("a", 64)} {
		if g, err := GroupName(bad); err == nil {
			t.Errorf("GroupName(%q) = %q, want error", bad, g)
		}
	}
	for _, good := range []string{"ab", "demo", "my-app-2", strings.Repeat("a", 63)} {
		if _, err := GroupName(good); err != nil {
			t.Errorf("GroupName(%q): %v", good, err)
		}
	}
}

func TestInvalidNameNeverReachesTheAPI(t *testing.T) {
	f, c := setup(t)
	s := spec()
	s.App = "Bad_Name"
	if _, err := c.Ensure(s); err == nil {
		t.Fatal("Ensure accepted an invalid name")
	}
	if len(f.calls) != 0 {
		t.Fatalf("API called for an invalid name: %v", f.calls)
	}
}

func TestValidTTL(t *testing.T) {
	for _, ok := range []string{"days=7", "hours=1", "days=1;hours=12", "minutes=30"} {
		if !ValidTTL(ok) {
			t.Errorf("ValidTTL(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "7d", "days=", "days=7;", "years=1", "days=7 hours=1"} {
		if ValidTTL(bad) {
			t.Errorf("ValidTTL(%q) = true", bad)
		}
	}
}

// Without the vibe flow, the default one is used — a host where stacks has not
// created it yet keeps working exactly as before, and says so.
func TestInvalidationFlowFallsBackToDefault(t *testing.T) {
	f, c := setup(t)
	res, err := c.Ensure(spec())
	if err != nil {
		t.Fatal(err)
	}
	if res.InvalidationFlow != invalidationFlow || f.providers[res.ProviderPK]["invalidation_flow"] != "f-inval" {
		t.Fatalf("flow = %s / %v", res.InvalidationFlow, f.providers[res.ProviderPK]["invalidation_flow"])
	}
}

// Once the vibe flow appears, the next deploy moves the existing provider to it.
func TestInvalidationFlowPrefersVibeFlow(t *testing.T) {
	f, c := setup(t)
	if _, err := c.Ensure(spec()); err != nil {
		t.Fatal(err)
	}
	f.vibeFlow = true
	res, err := c.Ensure(spec())
	if err != nil {
		t.Fatal(err)
	}
	if res.InvalidationFlow != VibeInvalidationFlow || f.providers[res.ProviderPK]["invalidation_flow"] != "f-vibe" {
		t.Fatalf("flow = %s / %v", res.InvalidationFlow, f.providers[res.ProviderPK]["invalidation_flow"])
	}
}

// Someone other than vd (stacks, reporting) adds a provider between vd's read
// and its write. vd must not overwrite the list with its stale copy.
func TestOutpostChangedConcurrentlyAborts(t *testing.T) {
	f, c := setup(t)
	f.onOutpostGet = func(n int) {
		if n == 2 { // the re-read just before PATCH
			f.outpost = append(f.outpost, 77)
		}
	}
	_, err := c.Ensure(spec())
	if err == nil || !strings.Contains(err.Error(), "changed while vd was updating") {
		t.Fatalf("want concurrent-change abort, got %v", err)
	}
	if f.count("PATCH /outposts/") != 0 {
		t.Fatal("outpost was written despite the concurrent change")
	}
	if len(f.outpost) != 2 || f.outpost[1] != 77 {
		t.Fatalf("other writer's provider lost: %v", f.outpost)
	}
}

// A first deploy under a reused name inherits the group's existing members.
func TestEnsureReportsExistingGroupMembers(t *testing.T) {
	f, c := setup(t)
	f.groups["vibe-demo"] = "g-old"
	f.groupUsers = map[string][]int{"vibe-demo": {5, 6}}
	res, err := c.Ensure(spec())
	if err != nil {
		t.Fatal(err)
	}
	if res.GroupMembers != 2 {
		t.Fatalf("GroupMembers = %d, want 2", res.GroupMembers)
	}
}

func TestTTLSeconds(t *testing.T) {
	for in, want := range map[string]int{
		"hours=1": 3600, "days=7": 604800, "days=1;hours=12": 129600,
		"minutes=2": 120, "weeks=1": 604800, "bogus": 0, "": 0,
	} {
		if got := TTLSeconds(in); got != want {
			t.Errorf("TTLSeconds(%q) = %d, want %d", in, got, want)
		}
	}
	if TTLSeconds(DefaultTTL) != 3600 {
		t.Errorf("default sign-in lifetime is %s, the owner's decision is one hour", DefaultTTL)
	}
}

// First deploy, binding refused: the app must never be served, and the
// application object created in this run must not be left behind unbound.
func TestBindingFailureOnFirstDeployLeavesNothingOpen(t *testing.T) {
	f, c := setup(t)
	f.failBindings = true
	_, err := c.Ensure(spec())
	if err == nil || !strings.Contains(err.Error(), "group binding failed") {
		t.Fatalf("want binding failure, got %v", err)
	}
	if len(f.outpost) != 1 || f.outpost[0] != 2 {
		t.Fatalf("outpost = %v, want the stranger only", f.outpost)
	}
	if _, ok := f.apps["vibe-demo"]; ok {
		t.Fatal("unbound application left behind")
	}
}

// A published app whose binding was removed by hand, redeployed while the API
// refuses bindings: it must be taken off the outpost, not left open to every
// signed-in account. The application pre-existed, so it is kept.
func TestBindingFailureOnPublishedAppUnpublishes(t *testing.T) {
	f, c := setup(t)
	res, err := c.Ensure(spec())
	if err != nil {
		t.Fatal(err)
	}
	f.bindings = nil // someone deleted it
	f.failBindings = true
	_, err = c.Ensure(spec())
	if err == nil || !strings.Contains(err.Error(), "not served") {
		t.Fatalf("want fail-closed error, got %v", err)
	}
	if slices.Contains(f.outpost, res.ProviderPK) {
		t.Fatalf("provider still on the outpost: %v", f.outpost)
	}
	if !slices.Contains(f.outpost, 2) {
		t.Fatal("stranger's provider dropped while unpublishing ours")
	}
	if _, ok := f.apps["vibe-demo"]; !ok {
		t.Fatal("pre-existing application deleted")
	}
}

// A disabled or negated group binding does not protect anything. Ensure repairs
// it in place, and Check does not count it.
func TestWeakBindingIsRepairedAndNotCounted(t *testing.T) {
	for _, field := range []string{"enabled", "negate"} {
		f, c := setup(t)
		if _, err := c.Ensure(spec()); err != nil {
			t.Fatal(err)
		}
		f.bindings[0][field] = field == "negate" // enabled=false, or negate=true
		if h, _ := c.Check("demo"); h.Binding || h.OK() {
			t.Fatalf("%s: Check counts a weak binding: %+v", field, h)
		}
		if _, err := c.Ensure(spec()); err != nil {
			t.Fatal(err)
		}
		if len(f.bindings) != 1 {
			t.Fatalf("%s: duplicate binding instead of repair: %v", field, f.bindings)
		}
		if h, _ := c.Check("demo"); !h.Binding || !h.OK() {
			t.Fatalf("%s: not repaired: %+v", field, h)
		}
	}
}

func TestCheckReportsMissingBinding(t *testing.T) {
	f, c := setup(t)
	if _, err := c.Ensure(spec()); err != nil {
		t.Fatal(err)
	}
	f.bindings = nil
	h, err := c.Check("demo")
	if err != nil {
		t.Fatal(err)
	}
	if h.Binding || h.OK() {
		t.Fatalf("unbound app reported healthy: %+v", h)
	}
}
