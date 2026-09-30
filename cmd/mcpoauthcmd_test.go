package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-deploy/vd/internal/docker"
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

// vd mcp-oauth recreates only the MCP container, but it rewrites the whole
// compose file: turning OAuth on must change MCP lines only, and keep Basic.
func TestMCPOAuthRenderTouchesOnlyTheMCP(t *testing.T) {
	m := &state.Manifest{Name: "demo", AppType: "node-server", Port: 3000, Routing: "subdomain",
		HasEnvFile: true, DB: "postgres", MCP: true, Auth: true}
	cfg := &state.Config{Domain: "apps.example.com"}
	basic := htpasswdSHA(mcpUser, "pw")
	off := mcpComposeData(m, cfg, basic, "deadbeef")
	off.MCPOAuth = false
	on := mcpComposeData(m, cfg, basic, "deadbeef")

	before, after := render(t, off), render(t, on)
	for _, l := range strings.Split(after, "\n") {
		if strings.HasPrefix(l, "# App: ") || strings.Contains(before, l+"\n") {
			continue
		}
		if !strings.Contains(l, "-mcp") && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			t.Errorf("OAuth changed a non-MCP line: %q", l)
		}
	}
	if !strings.Contains(after, "basicauth.users="+basic) || !strings.Contains(after, "vd-mcpgw@docker") {
		t.Fatal("OAuth render lost Basic or does not route to the gateway")
	}
	if !strings.Contains(after, "authentik-fa@file") || !strings.Contains(after, "X-Vibe-Ingress=deadbeef") {
		t.Fatal("the app's forward auth was not carried over")
	}
}

func TestLineDiffIgnoresOnlyTheHeader(t *testing.T) {
	a := "# App: x | Generated: 1\nfoo\nbar\n"
	if n := lineDiff(a, "# App: x | Generated: 2\nfoo\nbar\n"); n != 0 {
		t.Fatalf("header counted: %d", n)
	}
	if n := lineDiff(a, "# App: x | Generated: 1\nfoo\nbaz\n"); n != 2 {
		t.Fatalf("changed line counted %d, want 2", n)
	}
}
