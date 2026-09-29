package mcpgw

import (
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
	if err := Write(dir, []Route{route("alpha")}, false); err != nil {
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
