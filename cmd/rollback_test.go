package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/vibe-deploy/vd/internal/backup"
	"github.com/vibe-deploy/vd/internal/state"
)

// A backup from before Basic was removed is re-rendered before it starts:
// here without platform login, so the MCP does not start at all. A restored
// file without Basic is left byte for byte.
func TestRollbackDropsBasicMCP(t *testing.T) {
	t.Setenv("VD_HOME", t.TempDir())
	oldFS := templatesFS
	templatesFS = os.DirFS("..")
	t.Cleanup(func() { templatesFS = oldFS })
	if err := state.SaveConfig(&state.Config{Domain: "apps.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state.AppDir("demo"), 0755); err != nil {
		t.Fatal(err)
	}
	m := &state.Manifest{Name: "demo", AppType: "go", Port: 8080, Routing: "subdomain", DB: "postgres", MCP: true}

	clean := []byte("services: {}\n")
	os.WriteFile(state.AppComposePath("demo"), clean, 0600)
	if err := dropBasicMCP(&backup.Metadata{Manifest: m}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(state.AppComposePath("demo")); string(b) != string(clean) {
		t.Fatal("a compose file without Basic was rewritten")
	}

	os.WriteFile(state.AppComposePath("demo"), []byte(`- "traefik.http.routers.vd-demo-mcp-basic.rule=x"`), 0600)
	if err := dropBasicMCP(&backup.Metadata{Manifest: m}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(state.AppComposePath("demo"))
	if strings.Contains(string(b), "basic") || strings.Contains(string(b), "demo-mcp") || !strings.Contains(string(b), "vd-demo") {
		t.Fatalf("re-render kept Basic or started an MCP without sign-in:\n%s", b)
	}
	if saved, _ := state.LoadManifest("demo"); saved == nil || saved.MCPOAuthLive {
		t.Fatalf("manifest after re-render: %+v", saved)
	}
}
