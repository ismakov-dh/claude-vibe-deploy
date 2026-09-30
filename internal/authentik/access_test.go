package authentik

import (
	"errors"
	"strings"
	"testing"
)

func accessSetup(t *testing.T) (*fake, *Client) {
	f, c := setup(t)
	f.groups["vibe-demo"] = "g-demo"
	f.groups["mcp-vibe-demo"] = "g-mcp"
	f.groupAttrs = map[string]map[string]any{"mcp-vibe-demo": {vdManagedAttr: true}}
	return f, c
}

func TestAccessAddListRemove(t *testing.T) {
	f, c := accessSetup(t)
	for _, g := range []string{"vibe-demo", "mcp-vibe-demo"} {
		if ms, _, err := c.Members(g); err != nil || len(ms) != 0 {
			t.Fatalf("%s empty: %v %v", g, ms, err)
		}
		// Mixed case: Authentik's email= filter is case-sensitive.
		if changed, err := c.AddMember(g, " Owner@Example.COM "); err != nil || !changed {
			t.Fatalf("%s add: %v %v", g, changed, err)
		}
		if changed, err := c.AddMember(g, "owner@example.com"); err != nil || changed {
			t.Fatalf("%s second add must be a no-op: %v %v", g, changed, err)
		}
		ms, _, err := c.Members(g)
		if err != nil || len(ms) != 1 || ms[0].Email != "owner@example.com" || !ms[0].Active || ms[0].Name == "" {
			t.Fatalf("%s list: %+v %v", g, ms, err)
		}
		if changed, err := c.RemoveMember(g, "owner@example.com"); err != nil || !changed {
			t.Fatalf("%s remove: %v %v", g, changed, err)
		}
		if changed, err := c.RemoveMember(g, "owner@example.com"); err != nil || changed {
			t.Fatalf("%s second remove must be a no-op: %v %v", g, changed, err)
		}
	}
	_ = f
}

// Authentik ignores filters it does not know: the group list may come back
// whole. The client must still act on the exact group only.
func TestAccessMatchesTheGroupNameItself(t *testing.T) {
	f, c := accessSetup(t)
	f.ignoreGroupFilter = true
	f.groups["vibe-demo2"] = "g-decoy"
	if _, err := c.AddMember("vibe-demo", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := f.groupUsers["vibe-demo"]; len(got) != 1 || len(f.groupUsers["vibe-demo2"]) != 0 {
		t.Fatalf("wrote to the wrong group: %v", f.groupUsers)
	}
}

func TestAccessRefusals(t *testing.T) {
	f, c := accessSetup(t)

	if _, err := c.AddMember("mcp-vibe-demo", "nobody@example.com"); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("unknown email: %v", err)
	}
	// A longer address containing the given one is not a match.
	if _, err := c.AddMember("mcp-vibe-demo", "owner@example.co"); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("substring email matched: %v", err)
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
	f.forbidGroups = nil

	// Names outside the two families never reach Authentik.
	for _, g := range []string{"reporting-platform", "authentik Admins", "vibe-", "vibe-Demo", "xvibe-demo", "mcp-reporting"} {
		calls := len(f.calls)
		if _, err := c.AddMember(g, "owner@example.com"); err == nil || !strings.Contains(err.Error(), "refusing group name") {
			t.Errorf("%q: %v", g, err)
		}
		if len(f.calls) != calls {
			t.Errorf("%q reached Authentik", g)
		}
	}

	// An MCP group vd did not create is not touched.
	f.groupAttrs["mcp-vibe-demo"] = nil
	if _, err := c.AddMember("mcp-vibe-demo", "owner@example.com"); err == nil || !strings.Contains(err.Error(), "not created by vd") {
		t.Fatalf("foreign MCP group: %v", err)
	}
}

func TestAccessFailsClosedOnIncompleteAnswers(t *testing.T) {
	f, c := accessSetup(t)
	f.groupUsers = map[string][]int{"vibe-demo": {7}}

	// No member list: remove must not report "was not in" while 7 stays in.
	f.omitGroupUsers = true
	if _, err := c.RemoveMember("vibe-demo", "owner@example.com"); err == nil {
		t.Fatal("remove succeeded without a member list")
	}
	f.omitGroupUsers = false

	// A write Authentik acknowledges but does not apply.
	f.dropMembershipWrites = true
	if _, err := c.RemoveMember("vibe-demo", "owner@example.com"); err == nil || !strings.Contains(err.Error(), "does not show it") {
		t.Fatalf("unapplied write: %v", err)
	}
	f.dropMembershipWrites = false

	// A member whose account is gone is counted, not fatal.
	f.groupUsers["vibe-demo"] = []int{7, 99}
	ms, unreadable, err := c.Members("vibe-demo")
	if err != nil || len(ms) != 1 || unreadable != 1 {
		t.Fatalf("members with a missing account: %+v %d %v", ms, unreadable, err)
	}
}
