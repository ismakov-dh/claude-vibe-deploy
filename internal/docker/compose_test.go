package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The MCP service is two Traefik routers over one container, and the whole
// arrangement fails silently in both directions: get it wrong one way and the
// endpoint is wide open, wrong the other way and MCP clients see a 401 on
// /.well-known/, read it as "start the OAuth flow", and throw away the Basic
// credentials they were handed. Neither shows up as a failed deploy.
func renderMCP(t *testing.T, data ComposeData) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "docker-compose.vd.yml")
	// The real template, not a fixture — a fixture would drift.
	if err := GenerateComposeFile(os.DirFS("../.."), data, out); err != nil {
		t.Fatalf("render: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(body)
}

func mcpData() ComposeData {
	return ComposeData{
		Name:         "myapp",
		AppType:      "node-server",
		Port:         3000,
		Routing:      "subdomain",
		Domain:       "apps.example.com",
		NeedsDB:      true,
		NeedsMCP:     true,
		MCPImage:     "example/postgres-mcp:test",
		MCPBasicAuth: "mcp:{SHA}abc123=",
	}
}

func line(body, needle string) string {
	for _, l := range strings.Split(body, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}

func TestComposeMCPAuthOnlyOnTheMainRouter(t *testing.T) {
	body := renderMCP(t, mcpData())

	main := line(body, "routers.vd-myapp-mcp.rule=")
	wellknown := line(body, "routers.vd-myapp-mcp-wellknown.rule=")
	if main == "" || wellknown == "" {
		t.Fatalf("expected both routers, got:\n%s", body)
	}

	if !strings.Contains(body, "routers.vd-myapp-mcp.middlewares=vd-myapp-mcp-auth") {
		t.Error("main router is missing the basicauth middleware — endpoint would be unauthenticated")
	}
	if strings.Contains(body, "routers.vd-myapp-mcp-wellknown.middlewares") {
		t.Error("/.well-known/ router must not carry auth — a 401 there makes clients drop their Basic credentials")
	}
}

// Traefik derives router priority from rule length. The wellknown rule is a
// strict superset of the main rule, so it wins by default and needs no explicit
// priority — a literal priority would have to beat a default that grows with the
// app name, and would silently stop winning past ~62 characters.
func TestComposeMCPWellKnownOutranksByRuleLength(t *testing.T) {
	body := renderMCP(t, mcpData())

	main := line(body, "routers.vd-myapp-mcp.rule=")
	wellknown := line(body, "routers.vd-myapp-mcp-wellknown.rule=")
	if len(wellknown) <= len(main) {
		t.Errorf("wellknown rule must be longer than the main rule\n main: %s\n well: %s", main, wellknown)
	}
	if strings.Contains(body, "-mcp-wellknown.priority") {
		t.Error("explicit priority on the wellknown router: it competes with a length-derived default that grows with the app name")
	}
}

// Traefik runs with exposedbydefault=false, and the command line sets 8089 while
// the image exposes something else. Without either label the router exists and
// routes nowhere.
func TestComposeMCPCarriesEnableAndPort(t *testing.T) {
	body := renderMCP(t, mcpData())

	for _, want := range []string{
		"traefik.enable=true",
		"traefik.http.services.vd-myapp-mcp.loadbalancer.server.port=8089",
		"container_name: vd-myapp-mcp",
		"Host(`myapp.mcp.apps.example.com`)",
		"./mcp.env",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestComposeWithoutMCPRendersNoMCPService(t *testing.T) {
	data := mcpData()
	data.NeedsMCP = false
	data.MCPBasicAuth = ""
	body := renderMCP(t, data)

	if strings.Contains(body, "-mcp") {
		t.Errorf("MCP service leaked into a deploy that did not ask for one:\n%s", body)
	}
	// vd-db is still needed for the app's own DATABASE_URL.
	if !strings.Contains(body, "vd-db") {
		t.Error("expected vd-db network for an app with a database")
	}
}
