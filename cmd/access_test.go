package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
