package authentik

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

// Access groups vd manages membership of: an app's login group or its MCP's.
// Anything else is refused here, before Authentik is asked, and Authentik
// refuses it again (vd-platform holds membership rights only on groups vd
// created).
var accessGroupRe = regexp.MustCompile(`^(mcp-)?vibe-[a-z][a-z0-9-]{1,62}$`)

var (
	ErrNoGroup   = errors.New("group not found")
	ErrNoAccount = errors.New("no platform account with that email")
	ErrForbidden = errors.New("Authentik refused: vd may not change this group")
)

// Member is what vd access list shows: enough to recognise a person.
type Member struct {
	Email  string `json:"email"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// groupUsers finds the group by exact name and returns its pk and member pks.
func (c *Client) groupUsers(name string) (string, []int, error) {
	if !accessGroupRe.MatchString(name) {
		return "", nil, fmt.Errorf("refusing group name %q", name)
	}
	var pk string
	var users []int
	err := c.each("/core/groups/", url.Values{"name": {name}}, func(raw json.RawMessage) {
		var g struct {
			PK    string `json:"pk"`
			Name  string `json:"name"`
			Users []int  `json:"users"`
		}
		if json.Unmarshal(raw, &g) == nil && g.Name == name {
			pk, users = g.PK, g.Users
		}
	})
	if err != nil {
		return "", nil, err
	}
	if pk == "" {
		return "", nil, ErrNoGroup
	}
	return pk, users, nil
}

// Members lists the group's accounts. Read one by one from the group's own
// member list: a user-list filter Authentik ignored would list the directory.
func (c *Client) Members(group string) ([]Member, error) {
	_, pks, err := c.groupUsers(group)
	if err != nil {
		return nil, err
	}
	out := []Member{}
	for _, pk := range pks {
		var u struct {
			Email    string `json:"email"`
			Name     string `json:"name"`
			IsActive bool   `json:"is_active"`
		}
		if _, err := c.do("GET", "/core/users/"+strconv.Itoa(pk)+"/", nil, &u); err != nil {
			return nil, err
		}
		out = append(out, Member{Email: u.Email, Name: u.Name, Active: u.IsActive})
	}
	return out, nil
}

// AddMember puts the account with this email into the group. changed is false
// when it already was a member.
func (c *Client) AddMember(group, email string) (changed bool, err error) {
	return c.setMember(group, email, true)
}

// RemoveMember takes the account out. changed is false when it was not in.
func (c *Client) RemoveMember(group, email string) (changed bool, err error) {
	return c.setMember(group, email, false)
}

func (c *Client) setMember(group, email string, in bool) (bool, error) {
	gpk, members, err := c.groupUsers(group)
	if err != nil {
		return false, err
	}
	upk, err := c.userPKByEmail(email)
	if err != nil {
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
