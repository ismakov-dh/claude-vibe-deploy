package mcpgw

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func route(app string) Route {
	return Route{
		Name:     "mcp-vibe-" + app,
		Host:     app + ".mcp.apps.example.com",
		Resource: "https://" + app + ".mcp.apps.example.com/mcp",
		Issuer:   "https://auth.example.com/application/o/mcp-vibe-" + app + "/",
		Backend:  "http://vd-" + app + "-mcp:8089/sse",
	}
}

func TestRenderOneRoutePerAppPlusFallback(t *testing.T) {
	out, err := Render([]Route{route("zed"), route("alpha")})
	if err != nil {
		t.Fatal(err)
	}
	a, z := strings.Index(out, `name: "mcp-vibe-alpha"`), strings.Index(out, `name: "mcp-vibe-zed"`)
	f := strings.Index(out, `name: "fallback"`)
	if a < 0 || z < 0 || !(a < z && z < f) {
		t.Fatalf("routes missing or out of order (alpha %d, zed %d, fallback %d)", a, z, f)
	}
	for _, want := range []string{
		`hostnames: ["alpha.mcp.apps.example.com"]`,
		`audiences: ["mcp-vibe-alpha"]`,
		`clientId: "mcp-vibe-alpha"`,
		`'has(jwt.groups) && "mcp-vibe-alpha" in jwt.groups'`,
		`sse: {host: "http://vd-alpha-mcp:8089/sse"}`,
		`resource: "https://alpha.mcp.apps.example.com/mcp"`,
		`lookupFamily: V4Only`,
		`mode: strict`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
}

// A value carrying a quote would break out of its YAML string or the CEL rule.
func TestRenderRefusesUnsafeValues(t *testing.T) {
	r := route("alpha")
	r.Name = `mcp-vibe-alpha" || true`
	if _, err := Render([]Route{r}); err == nil {
		t.Fatal("rendered a quote into the authorization rule")
	}
	r = route("alpha")
	r.Backend = ""
	if _, err := Render([]Route{r}); err == nil {
		t.Fatal("rendered an empty backend")
	}
}

func TestEmptyRoutesStillServeFallback(t *testing.T) {
	out, err := Render(nil)
	if err != nil || !strings.Contains(out, `name: "fallback"`) || strings.Contains(out, "mcpAuthentication") {
		t.Fatalf("empty render: %v\n%s", err, out)
	}
}

func TestWriteIsAtomicAndLeavesNoStage(t *testing.T) {
	dir := t.TempDir()
	noValidate(t)
	if _, err := Write(dir, []Route{route("alpha")}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil || !strings.Contains(string(b), "mcp-vibe-alpha") {
		t.Fatalf("config not written: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("stage directory left behind: %v", entries)
	}
}

func noValidate(t *testing.T) {
	old := Validate
	Validate = func(string) error { return nil }
	t.Cleanup(func() { Validate = old })
}

// rejecting makes the fake gateway refuse any file that mentions one of names.
func rejecting(t *testing.T, names ...string) {
	old := Validate
	Validate = func(stage string) error {
		b, _ := os.ReadFile(filepath.Join(stage, "config.yaml"))
		for _, n := range names {
			if strings.Contains(string(b), n) {
				return errors.New("jwks fetch failed for " + n)
			}
		}
		return nil
	}
	t.Cleanup(func() { Validate = old })
}

func TestWriteDropsOnlyTheRouteTheGatewayRejects(t *testing.T) {
	dir := t.TempDir()
	rejecting(t, "mcp-vibe-beta")
	dropped, err := Write(dir, []Route{route("alpha"), route("beta")})
	if err != nil || len(dropped) != 1 || dropped[0] != "mcp-vibe-beta" {
		t.Fatalf("dropped %v, err %v", dropped, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(b), "mcp-vibe-alpha") || strings.Contains(string(b), "mcp-vibe-beta") {
		t.Fatalf("written:\n%s", b)
	}
}

func TestWriteKeepsOldFileWhenEveryRouteIsRejected(t *testing.T) {
	dir := t.TempDir()
	noValidate(t)
	if _, err := Write(dir, []Route{route("alpha")}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	rejecting(t, "mcp-vibe-")
	if _, err := Write(dir, []Route{route("alpha"), route("beta")}); err == nil {
		t.Fatal("total rejection accepted")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if string(after) != string(before) {
		t.Fatal("old file replaced after rejection")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("stage left behind: %v", entries)
	}
}

// One OAuth app and Authentik down: the old file stays, not a fallback-only one.
func TestWriteKeepsOldFileWhenItsOnlyRouteIsRejected(t *testing.T) {
	dir := t.TempDir()
	noValidate(t)
	if _, err := Write(dir, []Route{route("alpha")}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	rejecting(t, "mcp-vibe-alpha")
	if _, err := Write(dir, []Route{route("alpha")}); err == nil {
		t.Fatal("rejected single route accepted")
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "config.yaml")); string(after) != string(before) {
		t.Fatal("old file replaced")
	}
}
