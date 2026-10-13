package authentik

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Access groups vd manages membership of: an app's login group or its MCP's.
// Anything else is refused here, before Authentik is asked, and Authentik
// refuses it again (vd-platform holds membership rights only on groups vd
// created).
var accessGroupRe = regexp.MustCompile(`^(mcp-)?vibe-[a-z][a-z0-9-]{1,62}$`)

var (
	ErrNoGroup   = errors.New("group not found")
	ErrNoAccount = errors.New("no platform account with that email, nor service account with that username")
	ErrForbidden = errors.New("Authentik refused: vd may not change this group")
)

// Member is what vd access list shows: enough to recognise a person.
type Member struct {
	Email    string `json:"email"`
	Username string `json:"username"` // how a service account (no email) is named in vd access
	Name     string `json:"name"`
	Active   bool   `json:"active"`
}

// groupUsers finds the group by exact name and returns its pk and member pks.
// MCP groups must carry vd's marker, as everywhere else vd touches them.
func (c *Client) groupUsers(name string) (string, []int, error) {
	if !accessGroupRe.MatchString(name) {
		return "", nil, fmt.Errorf("refusing group name %q", name)
	}
	var pk string
	var users []int
	found, hasUsers, managed := false, false, false
	// include_users=false drops the full user objects, not the pk list.
	code, err := c.eachCode("/core/groups/", url.Values{"name": {name}, "include_users": {"false"}}, func(raw json.RawMessage) {
		var g struct {
			PK         string         `json:"pk"`
			Name       string         `json:"name"`
			Users      *[]int         `json:"users"`
			Attributes map[string]any `json:"attributes"`
		}
		if json.Unmarshal(raw, &g) == nil && g.Name == name {
			found, pk = true, g.PK
			managed = g.Attributes[vdManagedAttr] == true
			if g.Users != nil {
				hasUsers, users = true, *g.Users
			}
		}
	})
	if code == http.StatusForbidden {
		return "", nil, ErrForbidden
	}
	if err != nil {
		return "", nil, err
	}
	if !found {
		return "", nil, ErrNoGroup
	}
	// Without the member list a remove would see "not a member" and report
	// success while the person keeps access.
	if !hasUsers {
		return "", nil, fmt.Errorf("Authentik returned group %s without its member list", name)
	}
	if strings.HasPrefix(name, "mcp-") && !managed {
		return "", nil, fmt.Errorf("group %s was not created by vd (no %s attribute); refusing", name, vdManagedAttr)
	}
	return pk, users, nil
}

// Members lists the group's accounts. Read one by one from the group's own
// member list: a user-list filter Authentik ignored would list the directory.
// unreadable counts members vd could not read (deleted meanwhile, hidden).
func (c *Client) Members(group string) (members []Member, unreadable int, err error) {
	_, pks, err := c.groupUsers(group)
	if err != nil {
		return nil, 0, err
	}
	members = []Member{}
	for _, pk := range pks {
		var u struct {
			Email    string `json:"email"`
			Username string `json:"username"`
			Name     string `json:"name"`
			IsActive bool   `json:"is_active"`
		}
		code, err := c.do("GET", "/core/users/"+strconv.Itoa(pk)+"/", nil, &u)
		if code == http.StatusNotFound || code == http.StatusForbidden {
			unreadable++
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		members = append(members, Member{Email: u.Email, Username: u.Username, Name: u.Name, Active: u.IsActive})
	}
	return members, unreadable, nil
}

// AddMember puts the account with this email (or the service account with this
// username) into the group. changed is false
// when it already was a member.
func (c *Client) AddMember(group, email string) (changed bool, err error) {
	return c.setMember(group, email, true)
}

// RemoveMember takes the account out. changed is false when it was not in.
func (c *Client) RemoveMember(group, email string) (changed bool, err error) {
	return c.setMember(group, email, false)
}

func (c *Client) setMember(group, who string, in bool) (bool, error) {
	gpk, members, err := c.groupUsers(group)
	if err != nil {
		return false, err
	}
	var upk int
	if strings.Contains(who, "@") {
		upk, err = c.userPKByEmail(who)
	} else {
		upk, err = c.serviceAccountPK(who)
	}
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 403") {
			return false, ErrForbidden
		}
		return false, err
	}
	if upk == 0 {
		return false, ErrNoAccount
	}
	isMember := false
	for _, m := range members {
		if m == upk {
			isMember = true
		}
	}
	if isMember == in {
		return false, nil
	}
	action := "remove_user"
	if in {
		action = "add_user"
	}
	code, err := c.do("POST", "/core/groups/"+gpk+"/"+action+"/", map[string]any{"pk": upk}, nil)
	if code == http.StatusForbidden {
		return false, ErrForbidden
	}
	if err != nil {
		return false, err
	}
	// Re-read: the write is only done when the group says so.
	_, after, err := c.groupUsers(group)
	if err != nil {
		return false, err
	}
	now := false
	for _, m := range after {
		if m == upk {
			now = true
		}
	}
	if now != in {
		return false, fmt.Errorf("%s: Authentik accepted the change but the group does not show it", group)
	}
	return true, nil
}

// serviceAccountPK finds a service account by its exact username. People are
// found by email only: a username is not something their owner can verify,
// and a service account has no email.
func (c *Client) serviceAccountPK(username string) (int, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return 0, nil
	}
	var pks []int
	// search matches substrings; the exact comparison below decides.
	err := c.each("/core/users/", url.Values{"search": {username}}, func(raw json.RawMessage) {
		var u struct {
			PK       int    `json:"pk"`
			Username string `json:"username"`
			Type     string `json:"type"`
		}
		// Not internal_service_account: those are Authentik's own (outposts).
		if json.Unmarshal(raw, &u) == nil && u.Username == username && u.Type == "service_account" {
			pks = append(pks, u.PK)
		}
	})
	if err != nil {
		return 0, fmt.Errorf("look up service account: %w", err)
	}
	if len(pks) > 1 {
		return 0, fmt.Errorf("username matches %d service accounts — refusing to guess; ask a platform admin", len(pks))
	}
	if len(pks) == 0 {
		return 0, nil
	}
	return pks[0], nil
}
