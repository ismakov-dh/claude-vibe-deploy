package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The MCP has one Traefik router and it goes to vd-mcpgw; get that wrong and
// the endpoint is wide open, which does not show up as a failed deploy.
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
	body := renderMCP(t, data)

	if strings.Contains(body, "-mcp") {
		t.Errorf("MCP service leaked into a deploy that did not ask for one:\n%s", body)
	}
	// vd-db is still needed for the app's own DATABASE_URL.
	if !strings.Contains(body, "vd-db") {
		t.Error("expected vd-db network for an app with a database")
	}
}

func authData() ComposeData {
	return ComposeData{
		Name:          "myapp",
		AppType:       "python-fastapi",
		Port:          8000,
		Routing:       "subdomain",
		Domain:        "apps.example.com",
		HasEnvFile:    true,
		Auth:          true,
		IngressSecret: "deadbeef",
	}
}

// Forward auth fails open in two quiet ways: a chain in the wrong order, or a
// strip list that misses a header forwardAuth passes through. Both render a
// perfectly healthy app that trusts whatever identity a client sends.
func TestComposeAuthChain(t *testing.T) {
	body := renderMCP(t, authData())

	chain := line(body, "routers.vd-myapp.middlewares=")
	want := "vd-myapp-strip-identity,authentik-fa@file,vd-myapp-ingress"
	if !strings.Contains(chain, want) {
		t.Fatalf("middleware chain = %q, want %q", chain, want)
	}
	if !strings.Contains(body, "vd-myapp-ingress.headers.customrequestheaders.X-Vibe-Ingress=deadbeef") {
		t.Fatal("ingress secret not stamped")
	}

	outpost := line(body, "routers.vd-myapp-outpost.rule=")
	if !strings.Contains(outpost, "Host(`myapp.apps.example.com`) && PathPrefix(`/outpost.goauthentik.io/`)") {
		t.Fatalf("outpost router rule = %q", outpost)
	}
	outMW := line(body, "routers.vd-myapp-outpost.middlewares=")
	if strings.Contains(outMW, "authentik-fa") {
		t.Fatal("outpost router must not sit behind forward auth — it serves the login itself")
	}
	if !strings.Contains(outMW, "vd-myapp-strip-identity") {
		t.Fatalf("outpost router must pin X-Forwarded-Host too, got %q", outMW)
	}
	if !strings.Contains(line(body, "routers.vd-myapp-outpost.service="), "authentik@file") {
		t.Fatal("outpost router must point at the file-provider authentik service")
	}
}

// The strip list and authResponseHeaders live in two files; this keeps them in
// step. An entry missing from the strip list is a header a client can forge.
func TestStripListCoversEveryAuthResponseHeader(t *testing.T) {
	body := renderMCP(t, authData())
	dyn, err := os.ReadFile("../../templates/traefik/authentik.yml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	var passed []string
	in := false
	for _, l := range strings.Split(string(dyn), "\n") {
		tl := strings.TrimSpace(l)
		if tl == "authResponseHeaders:" {
			in = true
			continue
		}
		if in {
			if !strings.HasPrefix(tl, "- ") {
				break
			}
			passed = append(passed, strings.TrimPrefix(tl, "- "))
		}
	}
	if len(passed) == 0 {
		t.Fatal("found no authResponseHeaders in the dynamic template")
	}
	for _, h := range append(passed, "X-Vibe-Ingress") {
		if !strings.Contains(body, "vd-myapp-strip-identity.headers.customrequestheaders."+h+"=\"") {
			t.Errorf("strip-identity does not blank %s", h)
		}
	}
}

func TestComposeWithoutAuthHasNoAuthLabels(t *testing.T) {
	d := authData()
	d.Auth = false
	body := renderMCP(t, d)
	for _, s := range []string{"authentik", "X-Vibe-Ingress", "outpost"} {
		if strings.Contains(body, s) {
			t.Errorf("non-auth app renders %q", s)
		}
	}
}

func TestComposeRefusesAuthWithPathRouting(t *testing.T) {
	d := authData()
	d.Routing = "path"
	out := filepath.Join(t.TempDir(), "c.yml")
	if err := GenerateComposeFile(os.DirFS("../.."), d, out); err == nil {
		t.Fatal("rendered forward auth with path routing")
	}
	d = authData()
	d.IngressSecret = ""
	if err := GenerateComposeFile(os.DirFS("../.."), d, out); err == nil {
		t.Fatal("rendered forward auth without an ingress secret")
	}
}

// A client-supplied X-Forwarded-Host picks the outpost's application; if it
// survives to forwardAuth, a member of one app passes as another. The router
// matched the real host, so strip-identity must force exactly that and blank
// the headers forwardAuth would otherwise relay from the client.
func TestComposePinsForwardedHost(t *testing.T) {
	body := renderMCP(t, authData())
	want := map[string]string{
		"X-Forwarded-Host":   "myapp.apps.example.com",
		"X-Forwarded-Uri":    "",
		"X-Forwarded-Method": "",
		"X-Forwarded-Port":   "",
	}
	for h, v := range want {
		l := "vd-myapp-strip-identity.headers.customrequestheaders." + h + "=" + v + "\""
		if !strings.Contains(body, l) {
			t.Errorf("strip-identity does not set %s=%q", h, v)
		}
	}
}

// The compose file of an --auth app carries the ingress secret.
func TestComposeFileIsPrivate(t *testing.T) {
	out := filepath.Join(t.TempDir(), "docker-compose.vd.yml")
	os.WriteFile(out, []byte("old"), 0644) // a pre-existing, world-readable file
	if err := GenerateComposeFile(os.DirFS("../.."), authData(), out); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("compose file mode = %o, want 600", fi.Mode().Perm())
	}
}

// The Traefik API must not be reachable from vd-net: it lists every
// middleware, ingress secrets included.
func TestInfraKeepsTraefikAPIOnLoopback(t *testing.T) {
	b, err := os.ReadFile("../../templates/compose/infrastructure.yml")
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if !strings.Contains(body, `"--entrypoints.traefik.address=127.0.0.1:8080"`) {
		t.Error("traefik entrypoint (API) is not bound to the container's loopback")
	}
	if strings.Contains(body, "8099") {
		t.Error("API port 8099 is still published")
	}
}

func TestGatewayCIDRs(t *testing.T) {
	cases := map[string][]string{
		"172.24.0.1 ":           {"172.24.0.1/32"},
		"172.24.0.1 fd00:1::1 ": {"172.24.0.1/32", "fd00:1::1/128"},
		"":                      nil,
		"<no value> 10.0.0.1":   {"10.0.0.1/32"},
	}
	for in, want := range cases {
		got := GatewayCIDRs(in)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("GatewayCIDRs(%q) = %v, want %v", in, got, want)
		}
	}
}

// The only way into the MCP is the gateway: one router, no middleware of its
// own, and no Basic route under any input.
func TestComposeMCPOnlyThroughTheGateway(t *testing.T) {
	body := renderMCP(t, mcpData())
	if !strings.Contains(line(body, "routers.vd-myapp-mcp.service="), "vd-mcpgw@docker") {
		t.Fatal("the MCP router must go to the gateway")
	}
	for _, l := range strings.Split(body, "\n") {
		if strings.Contains(l, "routers.vd-myapp-mcp") && !strings.Contains(l, "routers.vd-myapp-mcp.") {
			t.Errorf("second MCP router: %s", l)
		}
	}
	if line(body, "routers.vd-myapp-mcp.middlewares=") != "" {
		t.Fatal("the gateway router must carry no middleware")
	}
	for _, auth := range []bool{false, true} {
		for _, mcp := range []bool{false, true} {
			d := mcpData()
			d.NeedsMCP, d.Auth = mcp, auth
			if auth {
				d.IngressSecret = "deadbeef"
			}
			b := strings.ToLower(renderMCP(t, d))
			if strings.Contains(b, "basicauth") || strings.Contains(b, "mcp-basic") {
				t.Errorf("auth=%v mcp=%v renders Basic", auth, mcp)
			}
		}
	}
}

func TestComposeProdROJoinsOnlyTheReplicaNetwork(t *testing.T) {
	d := authData()
	d.ProdRONetwork = "stack_vd-prod-ro"
	body := renderMCP(t, d)
	if !strings.Contains(body, "      - vd-prod-ro\n") || !strings.Contains(body, "    name: stack_vd-prod-ro\n    external: true") {
		t.Fatalf("app not on the replica network:\n%s", body)
	}
	if strings.Contains(body, "vd-db") || strings.Contains(body, "-mcp:") {
		t.Fatalf("prod-ro app must get neither vd-db nor an MCP:\n%s", body)
	}
}

func TestComposeRefusesUnsafeProdRO(t *testing.T) {
	out := filepath.Join(t.TempDir(), "c.yml")
	for name, mut := range map[string]func(*ComposeData){
		"no auth":  func(d *ComposeData) { d.Auth = false; d.IngressSecret = "" },
		"with MCP": func(d *ComposeData) { d.NeedsMCP = true; d.MCPImage = "x" },
		"on vd-db": func(d *ComposeData) { d.NeedsDB = true },
	} {
		d := authData()
		d.ProdRONetwork = "stack_vd-prod-ro"
		mut(&d)
		if err := GenerateComposeFile(os.DirFS("../.."), d, out); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
}

// Leaving prod-ro: the regenerated compose no longer names the replica network,
// so the recreated container is detached from it.
func TestComposeWithoutProdROHasNoReplicaNetwork(t *testing.T) {
	if body := renderMCP(t, authData()); strings.Contains(body, "vd-prod-ro") {
		t.Fatalf("replica network without prod-ro:\n%s", body)
	}
}
