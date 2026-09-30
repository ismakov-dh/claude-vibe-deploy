package authentik

import (
	"errors"
	"strings"
	"testing"
)

func TestAccessAddListRemove(t *testing.T) {
	f, c := setup(t)
	f.groups["vibe-demo"] = "g-demo"

	if ms, err := c.Members("vibe-demo"); err != nil || len(ms) != 0 {
		t.Fatalf("empty group: %v %v", ms, err)
	}
	changed, err := c.AddMember("vibe-demo", " Owner@Example.com ")
	if err != nil || !changed {
		t.Fatalf("add: %v %v", changed, err)
	}
	if changed, err := c.AddMember("vibe-demo", "owner@example.com"); err != nil || changed {
		t.Fatalf("second add must be a no-op: %v %v", changed, err)
	}
	ms, err := c.Members("vibe-demo")
	if err != nil || len(ms) != 1 || ms[0].Email != "owner@example.com" || !ms[0].Active || ms[0].Name == "" {
		t.Fatalf("list: %+v %v", ms, err)
	}
	if changed, err := c.RemoveMember("vibe-demo", "owner@example.com"); err != nil || !changed {
		t.Fatalf("remove: %v %v", changed, err)
	}
	if changed, err := c.RemoveMember("vibe-demo", "owner@example.com"); err != nil || changed {
		t.Fatalf("second remove must be a no-op: %v %v", changed, err)
	}
	if ms, _ := c.Members("vibe-demo"); len(ms) != 0 {
		t.Fatalf("still listed after remove: %+v", ms)
	}
}

func TestAccessRefusals(t *testing.T) {
	f, c := setup(t)
	f.groups["mcp-vibe-demo"] = "g-mcp"

	if _, err := c.AddMember("mcp-vibe-demo", "nobody@example.com"); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("unknown email: %v", err)
	}
	if _, err := c.AddMember("vibe-missing", "owner@example.com"); !errors.Is(err, ErrNoGroup) {
		t.Fatalf("missing group: %v", err)
	}
	f.forbidGroups = map[string]bool{"mcp-vibe-demo": true}
	posts := f.count("POST /core/groups/")
	if _, err := c.AddMember("mcp-vibe-demo", "owner@example.com"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("403: %v", err)
	}
	if f.count("POST /core/groups/") != posts+1 {
		t.Fatal("403 must not be retried")
	}
	// Names outside the two families never reach Authentik.
	for _, g := range []string{"reporting-platform", "authentik Admins", "vibe-", "vibe-Demo", "xvibe-demo"} {
		calls := len(f.calls)
		if _, err := c.AddMember(g, "owner@example.com"); err == nil || !strings.Contains(err.Error(), "refusing group name") {
			t.Errorf("%q: %v", g, err)
		}
		if len(f.calls) != calls {
			t.Errorf("%q reached Authentik", g)
		}
	}
}
