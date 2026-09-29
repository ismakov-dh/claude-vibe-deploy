package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-deploy/vd/internal/mcpgw"
	"github.com/vibe-deploy/vd/internal/state"
)

func TestSyncMCPGatewayRoutesOnlyLiveApps(t *testing.T) {
	t.Setenv("VD_HOME", t.TempDir())
	old := mcpgw.Validate
	mcpgw.Validate = func(string) error { return nil }
	t.Cleanup(func() { mcpgw.Validate = old })

	for _, m := range []*state.Manifest{
		{Name: "live", MCP: true, MCPOAuth: true, MCPOAuthLive: true},
		{Name: "intent", MCP: true, MCPOAuth: true},         // Authentik failed: no issuer yet
		{Name: "nomcp", MCPOAuth: true, MCPOAuthLive: true}, // MCP gone
		{Name: "basic", MCP: true},                          // never opted in
	} {
		if err := os.MkdirAll(state.AppDir(m.Name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := state.SaveManifest(m); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &state.Config{Domain: "apps.example.com", AuthentikURL: "https://auth.example.com"}

	// The lock covers reading the manifests, not just writing: a deploy that
	// holds it and goes live meanwhile must be in the file this sync writes.
	unlock, err := state.LockAuthentik()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- syncMCPGateway(cfg) }()
	select {
	case <-done:
		t.Fatal("sync ran while the Authentik lock was held")
	case <-time.After(200 * time.Millisecond):
	}
	if err := os.MkdirAll(state.AppDir("late"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveManifest(&state.Manifest{Name: "late", MCP: true, MCPOAuth: true, MCPOAuthLive: true}); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(state.MCPGWDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"live", "late"} {
		if !strings.Contains(string(b), app+".mcp.apps.example.com") {
			t.Fatalf("%s has no route:\n%s", app, b)
		}
	}
	for _, app := range []string{"intent", "nomcp", "basic"} {
		if strings.Contains(string(b), app+".mcp.") {
			t.Fatalf("%s must have no route:\n%s", app, b)
		}
	}
}
