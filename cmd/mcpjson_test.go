package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vibe-deploy/vd/internal/authentik"
	"github.com/vibe-deploy/vd/internal/state"
)

// Agents are never handed the MCP's Basic path: no user, no password, no SSE
// URL, no Authorization header — in deploy's block or status's, live or not.
func assertNoBasic(t *testing.T, what string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	s := strings.ToLower(string(b))
	for _, bad := range []string{"password", `"user"`, "/sse", "basic", "authorization", "--header"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s carries %q: %s", what, bad, b)
		}
	}
}

func TestMCPBlocksCarryNoBasic(t *testing.T) {
	cfg := &state.Config{Domain: "apps.example.com"}
	res := &authentik.MCPResult{Name: "mcp-vibe-demo", OwnerAdded: true}

	dep := mcpOAuthInfo("demo", cfg, "a@example.com", res)
	assertNoBasic(t, "deploy mcp block", dep)
	if dep["add"] != "claude mcp add --transport http demo-db https://demo.mcp.apps.example.com/mcp" || dep["group"] != "mcp-vibe-demo" {
		t.Fatalf("deploy block: %v", dep)
	}

	live := mcpStatusBlock(&state.Manifest{Name: "demo", MCP: true, MCPOAuth: true, MCPOAuthLive: true}, cfg)
	assertNoBasic(t, "status block (live)", live)
	if live["available"] != true || live["add"] != dep["add"] || live["group"] != "mcp-vibe-demo" {
		t.Fatalf("status block: %v", live)
	}

	// An app still on Basic only is reported unavailable — not with its password.
	old := mcpStatusBlock(&state.Manifest{Name: "demo", MCP: true}, cfg)
	assertNoBasic(t, "status block (Basic-only app)", old)
	if old["available"] != false || !strings.Contains(old["hint"].(string), "vd mcp-oauth demo") {
		t.Fatalf("Basic-only app: %v", old)
	}
}

// --mcp-oauth is no longer needed: every app with an MCP gets it, and the old
// flag is still accepted so existing commands keep working.
func TestMCPOAuthIsTheDefault(t *testing.T) {
	if !mcpOAuthFor(true) || mcpOAuthFor(false) {
		t.Fatal("MCP OAuth must follow the MCP: on with --db postgres, off without")
	}
	f := deployCmd.Flags().Lookup("mcp-oauth")
	if f == nil || !f.Hidden {
		t.Fatal("--mcp-oauth must stay accepted (old commands) and hidden")
	}
	if r := deployCmd.Flags().Lookup("mcp-rotate-password"); r == nil || !r.Hidden {
		t.Fatal("--mcp-rotate-password is a hidden admin flag")
	}
}
