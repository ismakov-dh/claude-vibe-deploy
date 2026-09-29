package authentik

import (
	"strings"
	"testing"
)

func mcpSpec() MCPSpec {
	return MCPSpec{App: "demo", Resource: "https://demo.mcp.apps.example.com/mcp"}
}

func TestEnsureMCPCreatesAPublicPKCEClientBoundToItsGroup(t *testing.T) {
	f, c := setup(t)
	res, err := c.EnsureMCP(mcpSpec())
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "mcp-vibe-demo" || !strings.HasSuffix(res.Issuer, "/application/o/mcp-vibe-demo/") {
		t.Fatalf("result %+v", res)
	}
	if len(f.oauth2) != 1 {
		t.Fatalf("providers: %v", f.oauth2)
	}
	for _, p := range f.oauth2 {
		if p["client_type"] != "public" || p["client_id"] != "mcp-vibe-demo" || p["sub_mode"] != "user_uuid" ||
			p["access_token_validity"] != "minutes=5" || p["signing_key"] != "k-self" {
			t.Fatalf("provider settings: %v", p)
		}
		maps := p["property_mappings"].([]any)
		if len(maps) != 3 || maps[2] != "m-mcp" {
			t.Fatalf("scope mappings %v — must be openid, offline_access and stacks' mcp-groups", maps)
		}
		for _, r := range p["redirect_uris"].([]any) {
			if u := r.(map[string]any)["url"].(string); strings.Contains(u, ".*") {
				t.Fatalf("catch-all redirect %q", u)
			}
		}
	}
	gpk := f.groups["mcp-vibe-demo"]
	if len(f.bindings) != 1 || f.bindings[0]["group"] != gpk {
		t.Fatalf("bindings %v", f.bindings)
	}
	if len(f.outpost) != 1 {
		t.Fatalf("MCP must not touch the proxy outpost: %v", f.outpost)
	}

	// Idempotent: nothing created the second time.
	posts := f.count("POST ")
	if _, err := c.EnsureMCP(mcpSpec()); err != nil {
		t.Fatal(err)
	}
	if f.count("POST ") != posts {
		t.Fatalf("second EnsureMCP created objects: %v", f.calls)
	}
}

func TestEnsureMCPNeedsStacksScopeMappingAndCreatesNothingWithout(t *testing.T) {
	f, c := setup(t)
	f.noMCPScope = true
	_, err := c.EnsureMCP(mcpSpec())
	if err == nil || !strings.Contains(err.Error(), "mcp-groups") {
		t.Fatalf("want missing-mapping error, got %v", err)
	}
	if n := f.count("POST "); n != 0 {
		t.Fatalf("created %d objects before failing: %v", n, f.calls)
	}
}

func TestEnsureMCPOwner(t *testing.T) {
	f, c := setup(t)
	s := mcpSpec()
	s.Owner = " Owner@Example.com "
	res, err := c.EnsureMCP(s)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OwnerAdded || res.OwnerMissing {
		t.Fatalf("owner: %+v", res)
	}
	if got := f.addedUsers[f.groups["mcp-vibe-demo"]]; len(got) != 1 || got[0] != 7 {
		t.Fatalf("added %v, want [7]", got)
	}

	s.Owner = "nobody@example.com"
	res, err = c.EnsureMCP(s)
	if err != nil || !res.OwnerMissing || res.OwnerAdded {
		t.Fatalf("unknown owner: %+v %v", res, err)
	}

	f.users[9] = "owner@example.com" // two accounts, one email
	s.Owner = "owner@example.com"
	if _, err := c.EnsureMCP(s); err == nil || !strings.Contains(err.Error(), "refusing to guess") {
		t.Fatalf("ambiguous owner accepted: %v", err)
	}
}

func TestEnsureMCPRejectsDroppedProviderField(t *testing.T) {
	f, c := setup(t)
	f.dropOAuth2 = "sub_mode"
	if _, err := c.EnsureMCP(mcpSpec()); err == nil || !strings.Contains(err.Error(), "sub_mode") {
		t.Fatalf("want drift error on sub_mode, got %v", err)
	}
}

func TestEnsureMCPBindingFailureDeletesNewApplication(t *testing.T) {
	f, c := setup(t)
	f.failBindings = true
	if _, err := c.EnsureMCP(mcpSpec()); err == nil || !strings.Contains(err.Error(), "group binding failed") {
		t.Fatalf("want binding failure, got %v", err)
	}
	if _, ok := f.apps["mcp-vibe-demo"]; ok {
		t.Fatal("unbound MCP application left behind — open to every signed-in account")
	}
}

func TestRemoveAndCheckMCP(t *testing.T) {
	f, c := setup(t)
	if _, err := c.EnsureMCP(mcpSpec()); err != nil {
		t.Fatal(err)
	}
	if h, err := c.CheckMCP("demo"); err != nil || !h.OK() {
		t.Fatalf("check after ensure: %+v %v", h, err)
	}
	f.bindings = nil
	if h, _ := c.CheckMCP("demo"); h.Binding || h.OK() {
		t.Fatalf("unbound MCP reported OK: %+v", h)
	}
	if err := c.RemoveMCP("demo"); err != nil {
		t.Fatal(err)
	}
	if len(f.oauth2) != 0 {
		t.Fatalf("provider left: %v", f.oauth2)
	}
	if _, ok := f.apps["mcp-vibe-demo"]; ok {
		t.Fatal("application left")
	}
	if _, ok := f.groups["mcp-vibe-demo"]; !ok {
		t.Fatal("group deleted — vd never deletes groups")
	}
}

func TestMCPName(t *testing.T) {
	if n, err := MCPName("demo"); err != nil || n != "mcp-vibe-demo" {
		t.Fatalf("%q %v", n, err)
	}
	if _, err := MCPName("Bad_Name"); err == nil {
		t.Fatal("accepted a bad name")
	}
}
