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

	// What vd deploy --json puts under "mcp", both ways.
	dep := deployMCPBlock("demo", cfg, "a@example.com", res)
	assertNoBasic(t, "deploy mcp block", dep)
	if dep["available"] != true || dep["group"] != "mcp-vibe-demo" ||
		dep["add"] != "claude mcp add --transport http demo-db https://demo.mcp.apps.example.com/mcp" {
		t.Fatalf("deploy block: %v", dep)
	}
	down := deployMCPBlock("demo", cfg, "", nil)
	assertNoBasic(t, "deploy mcp block (sign-in failed)", down)
	if down["available"] != false || down["add"] != nil {
		t.Fatalf("deploy block without sign-in: %v", down)
	}

	live := mcpStatusBlock(&state.Manifest{Name: "demo", MCP: true, MCPOAuthLive: true}, cfg)
	assertNoBasic(t, "status block (live)", live)
	if live["available"] != true || live["add"] != dep["add"] || live["group"] != "mcp-vibe-demo" {
		t.Fatalf("status block: %v", live)
	}

	// Not live: unavailable with the reason that applies, never the password.
	withAK := &state.Config{Domain: "apps.example.com", AuthentikURL: "https://auth.example.com"}
	for _, c := range []struct {
		name string
		cfg  *state.Config
		m    *state.Manifest
		hint string
	}{
		{"sign-in not set up", withAK, &state.Manifest{Name: "demo", MCP: true}, "vd mcp-oauth demo"},
		{"sign-in not set up", withAK, &state.Manifest{Name: "demo", MCP: true}, "redeploy"},
		{"no platform login here", cfg, &state.Manifest{Name: "demo", MCP: true}, "not set up on this server"},
	} {
		b := mcpStatusBlock(c.m, c.cfg)
		assertNoBasic(t, "status block ("+c.name+")", b)
		if b["available"] != false || !strings.Contains(b["hint"].(string), c.hint) {
			t.Errorf("%s: %v", c.name, b)
		}
	}
}

// --mcp-oauth is no longer needed and is still accepted so existing commands
// keep working; the Basic password flag is gone with Basic.
func TestMCPOAuthIsTheDefault(t *testing.T) {
	f := deployCmd.Flags().Lookup("mcp-oauth")
	if f == nil || !f.Hidden {
		t.Fatal("--mcp-oauth must stay accepted (old commands) and hidden")
	}
	if deployCmd.Flags().Lookup("mcp-rotate-password") != nil {
		t.Fatal("--mcp-rotate-password must be gone with Basic")
	}
}

// An MCP without sign-in is not rendered at all: fail closed.
func TestMCPWithoutSignInIsNotRendered(t *testing.T) {
	cfg := &state.Config{Domain: "apps.example.com"}
	m := &state.Manifest{Name: "demo", AppType: "go", Port: 8080, Routing: "subdomain", DB: "postgres", MCP: true}
	if body := render(t, composeDataFor(m, cfg, "")); strings.Contains(body, "demo-mcp") {
		t.Fatal("MCP rendered without a live sign-in route")
	}
	m.MCPOAuthLive = true
	if body := render(t, composeDataFor(m, cfg, "")); !strings.Contains(body, "routers.vd-demo-mcp.service=vd-mcpgw@docker") {
		t.Fatal("live MCP not rendered behind the gateway")
	}
}
