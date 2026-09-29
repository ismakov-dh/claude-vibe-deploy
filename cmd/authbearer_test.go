package cmd

import (
	"os"
	"testing"

	"github.com/vibe-deploy/vd/internal/authentik"
	"github.com/vibe-deploy/vd/internal/state"
)

func TestBearerFor(t *testing.T) {
	on := &state.Manifest{AuthBearer: true}
	off := &state.Manifest{}
	for _, c := range []struct {
		name      string
		auth      bool
		prev      *state.Manifest
		set, val  bool
		want, err bool
	}{
		{"default off", true, nil, false, false, false, false},
		{"flag turns on", true, off, true, true, true, false},
		{"sticky on redeploy", true, on, false, false, true, false},
		{"explicit false turns off", true, on, true, false, false, false},
		{"flag without auth refused", false, nil, true, true, false, true},
		{"explicit false without auth refused too", false, nil, true, false, false, true},
		{"no auth, no flag", false, on, false, false, false, false},
	} {
		got, err := bearerFor(c.auth, c.prev, c.set, c.val)
		if got != c.want || (err != nil) != c.err {
			t.Errorf("%s: got %v, %v", c.name, got, err)
		}
	}
}

func TestAuthStateReportsHeaderAuthDrift(t *testing.T) {
	ok := authentik.Health{Group: true, Provider: true, Application: true, Binding: true, InOutpost: true}
	for _, c := range []struct {
		name         string
		header, want bool
		broken       bool
		state        string
	}{
		{"both off", false, false, false, "ok"},
		{"both on", true, true, false, "ok"},
		{"on in Authentik, not asked for", true, false, false, "drift"},
		{"asked for, off in Authentik", false, true, false, "drift"},
		{"broken wins", true, false, true, "broken"},
	} {
		h := ok
		h.HeaderAuth = c.header
		if c.broken {
			h.Binding = false
		}
		if s, _ := authState(&h, c.want); s != c.state {
			t.Errorf("%s: state %s, want %s", c.name, s, c.state)
		}
	}
}

func TestRollbackKeepsAuthBearer(t *testing.T) {
	t.Setenv("VD_HOME", t.TempDir())
	if err := os.MkdirAll(state.AppDir("app"), 0755); err != nil {
		t.Fatal(err)
	}
	// The restored (older) manifest predates --auth-bearer; Authentik still has it on.
	if err := state.SaveManifest(&state.Manifest{Name: "app", Auth: true}); err != nil {
		t.Fatal(err)
	}
	if err := keepAuthBearer("app", true); err != nil {
		t.Fatal(err)
	}
	if m, _ := state.LoadManifest("app"); !m.AuthBearer {
		t.Fatal("rollback dropped auth_bearer: the next redeploy would turn the service account off")
	}
	if err := keepAuthBearer("app", false); err != nil {
		t.Fatal(err)
	}
	if m, _ := state.LoadManifest("app"); m.AuthBearer {
		t.Fatal("rollback kept auth_bearer that the current deploy had off")
	}
}
