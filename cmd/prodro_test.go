package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-deploy/vd/internal/state"
)

func TestProdROGate(t *testing.T) {
	for _, c := range []struct {
		name, db string
		auth     bool
		ttl      string
		code     string
	}{
		{"postgres untouched", "postgres", false, "", ""},
		{"none untouched", "none", false, "", ""},
		{"prod-ro without auth refused", "prod-ro", false, "", "PROD_RO_REQUIRES_AUTH"},
		{"prod-ro hours=1 ok", "prod-ro", true, "hours=1", ""},
		{"prod-ro minutes=30 ok", "prod-ro", true, "minutes=30", ""},
		{"prod-ro hours=2 refused", "prod-ro", true, "hours=2", "INVALID_AUTH_TTL"},
		{"prod-ro hours=1;minutes=1 refused", "prod-ro", true, "hours=1;minutes=1", "INVALID_AUTH_TTL"},
		{"long ttl fine without prod-ro", "postgres", true, "days=7", ""},
	} {
		e := prodROGate(c.db, c.auth, c.ttl)
		got := ""
		if e != nil {
			got = e.Code
		}
		if got != c.code {
			t.Errorf("%s: got %q, want %q", c.name, got, c.code)
		}
	}
}

func TestValidProdROURL(t *testing.T) {
	for s, want := range map[string]bool{
		"postgres://vibe_ro:pw@db-replica:5432/reporting": true,
		"postgresql://vibe_ro:pw@db-replica/reporting":    true,
		"postgres://vibe_ro@db-replica/reporting":         false, // no password
		"postgres://vibe_ro:pw@db-replica":                false, // no database
		"mysql://vibe_ro:pw@db-replica/reporting":         false,
		"postgres://vibe_ro:pw@db-replica/reporting\nX=1": false, // .env injection
		"postgres://vibe_ro:p$w@db-replica/reporting":     false, // compose interpolation
		"": false,
	} {
		if validProdROURL(s) != want {
			t.Errorf("validProdROURL(%q) != %v", s, want)
		}
	}
}

func TestRollbackProdROGate(t *testing.T) {
	good := &state.Manifest{Name: "a", DB: "prod-ro", Auth: true, AuthTTL: "hours=1", ProdRONetwork: "net"}
	cur := &state.Manifest{Name: "a", DB: "prod-ro", Auth: true, AuthTTL: "hours=1", ProdRONetwork: "net"}
	for _, c := range []struct {
		name   string
		cur, b *state.Manifest
		refuse bool
	}{
		{"not prod-ro backup", cur, &state.Manifest{DB: "postgres"}, false},
		{"replica backup onto replica app", cur, good, false},
		{"app left prod-ro since", &state.Manifest{DB: "none"}, good, true},
		{"pre-replica backup (per-app prod user)", cur, &state.Manifest{DB: "prod-ro", Auth: true, AuthTTL: "hours=1"}, true},
		{"backup without login", cur, &state.Manifest{DB: "prod-ro", ProdRONetwork: "net"}, true},
		{"backup with long sign-in", cur, &state.Manifest{DB: "prod-ro", Auth: true, AuthTTL: "days=7", ProdRONetwork: "net"}, true},
		{"no current manifest", nil, good, true},
	} {
		if got := rollbackProdROGate(c.cur, c.b) != nil; got != c.refuse {
			t.Errorf("%s: refused=%v", c.name, got)
		}
	}
}

func TestDropProdRODSN(t *testing.T) {
	t.Setenv("VD_HOME", t.TempDir())
	const dsn = "postgres://vibe_ro:pw@db-replica/reporting"
	if err := os.WriteFile(state.ProdROURLPath(), []byte(dsn+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), ".env")
	write := func(s string) {
		if err := os.WriteFile(env, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}

	write("A=1\nDATABASE_URL=" + dsn + "\n")
	if !dropProdRODSN(env, false) || envValue(env, "DATABASE_URL") != "" || envValue(env, "A") != "1" {
		t.Fatal("stored DSN left in .env, or other keys lost")
	}

	write("DATABASE_URL=postgres://own:pw@vd-postgres/own\n")
	if dropProdRODSN(env, false) || envValue(env, "DATABASE_URL") == "" {
		t.Fatal("an app's own DATABASE_URL was removed")
	}

	// Previous deploy was prod-ro, DSN rotated since: removed anyway.
	write("DATABASE_URL=postgres://vibe_ro:old@db-replica/reporting\n")
	if !dropProdRODSN(env, true) || envValue(env, "DATABASE_URL") != "" {
		t.Fatal("rotated-out prod DSN left behind")
	}
	if fi, _ := os.Stat(env); fi.Mode().Perm() != 0600 {
		t.Fatalf(".env mode %v", fi.Mode().Perm())
	}
}

func TestEnsureDockerignoreKeepsEnvOutOfTheImage(t *testing.T) {
	dir := t.TempDir()
	if err := ensureDockerignore(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".dockerignore")); string(b) != ".env\n.env.*\n" {
		t.Fatalf("new .dockerignore: %q", b)
	}
	if err := os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("node_modules\n.env"), 0644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := ensureDockerignore(dir); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".dockerignore")); string(b) != "node_modules\n.env\n.env.*\n" {
		t.Fatalf("existing .dockerignore: %q", b)
	}
}

// The gate must stop a deploy before anything is written: no app directory, no
// .env with the DSN. Run as a real `vd deploy` in a subprocess, since Fail exits.
func TestProdROGateRunsBeforeAnythingIsWritten(t *testing.T) {
	if os.Getenv("VD_DEPLOY_HELPER") == "1" {
		rootCmd.SetArgs(strings.Fields(os.Getenv("VD_DEPLOY_ARGS")))
		rootCmd.Execute()
		os.Exit(0)
	}
	for _, c := range []struct{ args, code string }{
		{"--db prod-ro", "PROD_RO_REQUIRES_AUTH"},
		{"--db prod-ro --auth --auth-ttl hours=2", "INVALID_AUTH_TTL"},
	} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"domain":"apps.example.com","prod_ro_network":"net"}`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "prod-ro.url"), []byte("postgres://vibe_ro:pw@db-replica/reporting\n"), 0600); err != nil {
			t.Fatal(err)
		}
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "index.html"), []byte("ok"), 0644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestProdROGateRunsBeforeAnythingIsWritten$")
		cmd.Env = append(os.Environ(), "VD_DEPLOY_HELPER=1", "VD_HOME="+home,
			"VD_DEPLOY_ARGS=deploy "+src+" --name probe --json "+c.args)
		out, _ := cmd.CombinedOutput()
		if !strings.Contains(string(out), `"code": "`+c.code+`"`) {
			t.Fatalf("%s: want %s, got:\n%s", c.args, c.code, out)
		}
		if strings.Contains(string(out), "pw@db-replica") {
			t.Fatalf("%s: DSN in output", c.args)
		}
		if _, err := os.Stat(filepath.Join(home, "apps", "probe")); !os.IsNotExist(err) {
			t.Fatalf("%s: app directory created before the gate", c.args)
		}
	}
}
