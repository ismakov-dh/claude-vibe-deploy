package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/mcpgw"
	"github.com/vibe-deploy/vd/internal/state"
)

func render(t *testing.T, d docker.ComposeData) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "c.yml")
	if err := docker.GenerateComposeFile(os.DirFS(".."), d, out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	return string(b)
}

// What deploy renders, and what mcp-oauth re-renders from the manifest deploy
// saved (through JSON, as on disk), must be the same file.
func TestRebuildFromManifestMatchesDeploy(t *testing.T) {
	cfg := &state.Config{Domain: "apps.example.com"}
	for _, deployed := range []state.Manifest{
		{Name: "demo", AppType: "node-server", Port: 3000, Routing: "subdomain", HasEnvFile: true, DB: "postgres", MCP: true, Auth: true},
		{Name: "plain", AppType: "python-fastapi", Port: 8000, Routing: "subdomain", HasEnvFile: true, DB: "postgres", MCP: true},
		{Name: "live", AppType: "go", Port: 8080, Routing: "subdomain", DB: "postgres", MCP: true, MCPOAuthLive: true},
	} {
		fromDeploy := render(t, composeDataFor(&deployed, cfg, "deadbeef"))
		raw, _ := json.Marshal(deployed)
		var saved state.Manifest
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		if d := lineDiff(fromDeploy, render(t, composeDataFor(&saved, cfg, "deadbeef")), deployed.Name); d != (composeDiff{}) {
			t.Errorf("%s: re-render differs from deploy: %+v", deployed.Name, d)
		}
	}
}

// Turning OAuth on changes MCP lines only, and keeps the app's forward auth.
func TestMCPOAuthRenderTouchesOnlyTheMCP(t *testing.T) {
	m := &state.Manifest{Name: "demo", AppType: "node-server", Port: 3000, Routing: "subdomain",
		HasEnvFile: true, DB: "postgres", MCP: true, Auth: true}
	cfg := &state.Config{Domain: "apps.example.com"}
	before := render(t, composeDataFor(m, cfg, "deadbeef"))
	on := *m
	on.MCPOAuthLive = true
	after := render(t, composeDataFor(&on, cfg, "deadbeef"))
	if d := lineDiff(before, after, "demo"); d.other != 0 || d.mcp == 0 {
		t.Fatalf("OAuth changed non-MCP lines or nothing: %+v", d)
	}
	if strings.Contains(after, "basicauth") || !strings.Contains(after, "vd-mcpgw@docker") ||
		!strings.Contains(after, "authentik-fa@file") || !strings.Contains(after, "X-Vibe-Ingress=deadbeef") {
		t.Fatal("OAuth render has Basic, or lost the gateway route or the app's forward auth")
	}
}

func TestLineDiffSeparatesMCPFromTheRest(t *testing.T) {
	// App named like its own MCP's suffix, to catch substring matching.
	a := "# App: x-mcp | Generated: 1\nservices:\n  x-mcp:\n    image: app:1\n    labels:\n      - \"r.vd-x-mcp.rule=B\"\n  x-mcp-mcp:\n    image: postgres-mcp:1\n      - \"r.vd-x-mcp-mcp.rule=A\"\nnetworks:\n  vd-net:\n"
	if d := lineDiff(a, strings.Replace(a, "Generated: 1", "Generated: 2", 1), "x-mcp"); d != (composeDiff{}) {
		t.Fatalf("header counted: %+v", d)
	}
	if d := lineDiff(a, strings.Replace(a, "postgres-mcp:1", "postgres-mcp:2", 1), "x-mcp"); d.other != 0 || d.mcp != 2 {
		t.Fatalf("MCP image change: %+v", d)
	}
	// This is what makes vd mcp-oauth refuse (COMPOSE_DRIFT).
	if d := lineDiff(a, strings.Replace(a, "image: app:1", "image: app:2", 1), "x-mcp"); d.other != 2 || d.mcp != 0 {
		t.Fatalf("app change: %+v", d)
	}
	if d := lineDiff(a, strings.Replace(a, "  vd-net:", "  other-net:", 1), "x-mcp"); d.other != 2 {
		t.Fatalf("top-level change after the MCP block counted as MCP: %+v", d)
	}
}

func TestRevertMCPOAuthRestoresEverything(t *testing.T) {
	t.Setenv("VD_HOME", t.TempDir())
	oldV := mcpgw.Validate
	mcpgw.Validate = func(string) error { return nil }
	var upped []string
	oldUp := composeUpService
	composeUpService = func(dir, file, svc string) error { upped = append(upped, svc); return nil }
	t.Cleanup(func() { mcpgw.Validate, composeUpService = oldV, oldUp })

	if err := os.MkdirAll(state.AppDir("demo"), 0755); err != nil {
		t.Fatal(err)
	}
	old := &state.Manifest{Name: "demo", DB: "postgres", MCP: true, DeployedAt: "2026-09-01T00:00:00Z"}
	if err := os.WriteFile(state.AppComposePath("demo"), []byte("new, with oauth"), 0644); err != nil {
		t.Fatal(err)
	}
	now := *old
	now.MCPOAuthLive = true
	if err := state.WriteManifest(&now); err != nil {
		t.Fatal(err)
	}

	if errs := revertMCPOAuth("demo", []byte("old compose"), old, &state.Config{Domain: "apps.example.com", AuthentikURL: "https://auth.example.com"}); len(errs) != 0 {
		t.Fatalf("revert errors: %v", errs)
	}
	b, _ := os.ReadFile(state.AppComposePath("demo"))
	fi, _ := os.Stat(state.AppComposePath("demo"))
	if string(b) != "old compose" || fi.Mode().Perm() != 0600 {
		t.Fatalf("compose after revert: %q mode %v", b, fi.Mode().Perm())
	}
	m, _ := state.LoadManifest("demo")
	if m.MCPOAuthLive || m.DeployedAt != old.DeployedAt {
		t.Fatalf("manifest after revert: %+v", m)
	}
	if len(upped) != 1 || upped[0] != "demo-mcp" {
		t.Fatalf("recreated %v, want only demo-mcp", upped)
	}
	if routes, _ := os.ReadFile(filepath.Join(state.MCPGWDir(), "config.yaml")); strings.Contains(string(routes), "demo.mcp.") {
		t.Fatal("gateway still routes the reverted app")
	}
}
