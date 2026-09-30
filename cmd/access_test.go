package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-deploy/vd/internal/state"
)

// vd access refuses before Authentik is contacted (no Authentik is configured
// in these runs): bad names, unknown apps, apps without the matching login.
func TestAccessRefusesBeforeAuthentik(t *testing.T) {
	if os.Getenv("VD_ACCESS_HELPER") == "1" {
		rootCmd.SetArgs(strings.Fields(os.Getenv("VD_ACCESS_ARGS")))
		rootCmd.Execute()
		os.Exit(0)
	}
	home := t.TempDir()
	t.Setenv("VD_HOME", home)
	for _, m := range []*state.Manifest{
		{Name: "public", DB: "postgres", MCP: true},
		{Name: "loggedin", Auth: true, DB: "postgres", MCP: true},
	} {
		if err := os.MkdirAll(state.AppDir(m.Name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := state.SaveManifest(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"domain":"apps.example.com"}`), 0644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ args, code string }{
		{"access ../etc list", "INVALID_NAME"},
		{"access Reporting-Platform-Prod list", "INVALID_NAME"},
		{"access ghost list", "NOT_FOUND"},
		{"access public add a@example.com", "ACCESS_NOT_ENABLED"},
		{"access loggedin list --mcp", "ACCESS_NOT_ENABLED"},
		{"access loggedin add", "INVALID_ARGS"},
		{"access loggedin grant a@example.com", "INVALID_ARGS"},
		{"access loggedin list", "AUTH_NOT_CONFIGURED"}, // passed every app check
	} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAccessRefusesBeforeAuthentik$")
		cmd.Env = append(os.Environ(), "VD_ACCESS_HELPER=1", "VD_HOME="+home, "VD_ACCESS_ARGS="+c.args+" --json")
		out, _ := cmd.CombinedOutput()
		if !strings.Contains(string(out), `"code": "`+c.code+`"`) {
			t.Errorf("%s: want %s, got:\n%s", c.args, c.code, out)
		}
	}
}

// The Authentik-backed paths of the real command, against a small fake:
// takes_effect on remove, NO_ACCOUNT and ACCESS_FORBIDDEN mapped for agents.
func TestAccessCommandAgainstAuthentik(t *testing.T) {
	var mu sync.Mutex
	members := []int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		p := strings.TrimPrefix(r.URL.Path, "/api/v3")
		switch {
		case p == "/core/groups/":
			var res []any
			if n := r.URL.Query().Get("name"); n == "vibe-loggedin" || n == "vibe-forbidden" {
				res = append(res, map[string]any{"pk": "g-" + n, "name": n, "users": members})
			}
			json.NewEncoder(w).Encode(map[string]any{"pagination": map[string]any{"next": 0}, "results": res})
		case p == "/core/users/":
			var res []any
			if strings.Contains("owner@example.com", strings.ToLower(r.URL.Query().Get("search"))) {
				res = append(res, map[string]any{"pk": 7, "email": "owner@example.com"})
			}
			json.NewEncoder(w).Encode(map[string]any{"pagination": map[string]any{"next": 0}, "results": res})
		case p == "/core/users/7/":
			json.NewEncoder(w).Encode(map[string]any{"pk": 7, "email": "owner@example.com", "name": "Owner", "is_active": true})
		case strings.HasPrefix(p, "/core/groups/g-vibe-forbidden/"):
			w.WriteHeader(403)
			w.Write([]byte(`{"detail":"You do not have permission to perform this action."}`))
		case strings.HasSuffix(p, "/add_user/"):
			members = []int{7}
			w.WriteHeader(204)
		case strings.HasSuffix(p, "/remove_user/"):
			members = []int{}
			w.WriteHeader(204)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("VD_HOME", home)
	for _, m := range []*state.Manifest{
		{Name: "loggedin", Auth: true, AuthTTL: "minutes=30"},
		{Name: "forbidden", Auth: true},
	} {
		os.MkdirAll(state.AppDir(m.Name), 0755)
		if err := state.SaveManifest(m); err != nil {
			t.Fatal(err)
		}
	}
	cfg := `{"domain":"apps.example.com","authentik_url":"` + srv.URL + `","authentik_internal":"http://authentik:9000"}`
	os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0644)
	os.MkdirAll(filepath.Dir(state.AuthentikDynamicPath()), 0755)
	os.WriteFile(state.AuthentikDynamicPath(), []byte("x"), 0644)
	os.WriteFile(state.AuthentikTokenPath(), []byte("sekrit-token\n"), 0600)

	run := func(args string) string {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAccessRefusesBeforeAuthentik$")
		cmd.Env = append(os.Environ(), "VD_ACCESS_HELPER=1", "VD_HOME="+home, "VD_ACCESS_ARGS="+args+" --json")
		out, _ := cmd.CombinedOutput()
		if strings.Contains(string(out), "sekrit-token") {
			t.Fatalf("%s: token in output", args)
		}
		return string(out)
	}
	if out := run("access loggedin add owner@example.com"); !strings.Contains(out, `"changed": true`) {
		t.Fatalf("add:\n%s", out)
	}
	if out := run("access loggedin list"); !strings.Contains(out, `"email": "owner@example.com"`) {
		t.Fatalf("list:\n%s", out)
	}
	out := run("access loggedin remove owner@example.com")
	if !strings.Contains(out, `"takes_effect_max_seconds": 1800`) || !strings.Contains(out, "minutes=30") {
		t.Fatalf("remove must say when it bites (auth_ttl):\n%s", out)
	}
	if out := run("access loggedin remove owner@example.com"); strings.Contains(out, "takes_effect") || !strings.Contains(out, `"changed": false`) {
		t.Fatalf("repeat remove:\n%s", out)
	}
	if out := run("access loggedin add ghost@example.com"); !strings.Contains(out, `"code": "NO_ACCOUNT"`) {
		t.Fatalf("unknown email:\n%s", out)
	}
	if out := run("access forbidden add owner@example.com"); !strings.Contains(out, `"code": "ACCESS_FORBIDDEN"`) {
		t.Fatalf("403:\n%s", out)
	}
}
