package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/authentik"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

var accessMCP bool

func init() {
	accessCmd.Flags().BoolVar(&accessMCP, "mcp", false, "the app's database MCP group (mcp-vibe-<app>) instead of its login group (vibe-<app>)")
	rootCmd.AddCommand(accessCmd)
}

// vd access lets the people who deploy an app grant and revoke access to it
// themselves. The group is never typed: it is derived from the app name, so
// the command reaches only groups vd made for its own apps.
var accessCmd = &cobra.Command{
	Use:   "access <app-name> list|add|remove [email]",
	Short: "List, grant or revoke access to an app (or, with --mcp, to its database MCP)",
	Args:  cobra.RangeArgs(2, 3),
	Run: func(cmd *cobra.Command, args []string) {
		name, action := args[0], args[1]
		email := ""
		switch {
		case action == "list" && len(args) == 2:
		case (action == "add" || action == "remove") && len(args) == 3:
			email = args[2]
		default:
			output.Fail("access", output.NewError("INVALID_ARGS",
				"Usage: vd access <app> list | add <email> | remove <email> [--mcp]", ""))
		}

		group, ttl, ttlSeconds := accessGroup(name, accessMCP)
		cfg, cerr := state.LoadConfig()
		if cerr != nil {
			output.Fail("access", output.NewError("NOT_INITIALIZED", "Run vd init first", ""))
		}
		if err := cfg.AuthentikReady(); err != nil {
			output.Fail("access", output.NewError("AUTH_NOT_CONFIGURED", "Platform login is not set up on this server: "+err.Error(), ""))
		}
		token, terr := state.LoadAuthentikToken()
		if terr != nil {
			output.Fail("access", output.NewError("AUTH_NOT_CONFIGURED", "vd's Authentik token is not readable: "+terr.Error(),
				"A platform admin installs it: vd init --authentik-token-stdin"))
		}
		c := authentik.New(cfg.AuthentikURL, token)

		data := map[string]any{"app": name, "group": group}
		switch action {
		case "list":
			ms, unreadable, err := c.Members(group)
			if err != nil {
				failAccess(group, err)
			}
			if unreadable > 0 {
				output.Warn("%d member(s) of %s could not be read (deleted or hidden from vd)", unreadable, group)
			}
			for _, m := range ms {
				st := "active"
				if !m.Active {
					st = "inactive"
				}
				output.Info("%s  %s  (%s)", m.Email, m.Name, st)
			}
			if len(ms) == 0 {
				output.Info("%s has no members", group)
			}
			data["members"] = ms
		case "add", "remove":
			unlock, err := state.LockAuthentik()
			if err != nil {
				output.Fail("access", output.NewError("ACCESS_FAILED", "Could not take the Authentik lock: "+err.Error(), ""))
			}
			var changed bool
			if action == "add" {
				changed, err = c.AddMember(group, email)
			} else {
				changed, err = c.RemoveMember(group, email)
			}
			// A strict MCP needs the app's group at sign-in only — Authentik
			// runs no policy on refresh — so leaving the app leaves its MCP too:
			// the gateway then refuses the next 5-minute token.
			mcpRemoved := false
			if m, _ := state.LoadManifest(name); err == nil && action == "remove" && !accessMCP && m != nil && m.MCPStrict && m.MCPOAuthLive {
				mg, _ := authentik.MCPName(name)
				if mcpRemoved, err = c.RemoveMember(mg, email); err == nil {
					data["mcp_removed"] = mcpRemoved
					changed = changed || mcpRemoved
				} else {
					group = mg
				}
			}
			unlock()
			if err != nil {
				failAccess(group, err)
			}
			data["email"] = email
			data["changed"] = changed
			switch {
			case action == "add" && changed:
				output.Info("Added %s to %s", email, group)
			case action == "add":
				output.Info("%s already had access (%s)", email, group)
			case changed:
				output.Info("Removed %s from %s", email, group)
			default:
				output.Info("%s was not in %s", email, group)
			}
			if action == "remove" && changed {
				data["takes_effect"] = ttl
				data["takes_effect_max_seconds"] = ttlSeconds
				output.Info("Access ends %s", ttl)
			}
		}
		output.Success("access", data)
	},
}

// accessGroup checks the app and returns its group and when a removal bites.
func accessGroup(name string, mcp bool) (group, takesEffect string, maxSeconds int) {
	if !nameRegex.MatchString(name) {
		output.Fail("access", output.NewError("INVALID_NAME", "Invalid app name: "+name, ""))
	}
	m, err := state.LoadManifest(name)
	if err != nil {
		output.Fail("access", output.NewError("NOT_FOUND", "App not found: "+name, "Check app name with: vd list"))
	}
	if mcp {
		if !m.MCPOAuthLive {
			output.Fail("access", output.NewError("ACCESS_NOT_ENABLED",
				name+"'s database MCP is not behind platform sign-in (yet)", "Redeploy the app; if it persists, a platform admin checks vd status "+name))
		}
		g, _ := authentik.MCPName(name)
		// The gateway admits a token only if the group is in its groups claim.
		// Access tokens live 5 minutes, and a refreshed one is issued with the
		// claims as they are then — so the 12-hour refresh token cannot carry
		// a removed membership past the next 5-minute token.
		return g, "within 5 minutes, when their MCP token expires", 300
	}
	if !m.Auth {
		output.Fail("access", output.NewError("ACCESS_NOT_ENABLED",
			name+" is not behind platform login", "Deploy it with --auth (see /auth)"))
	}
	g, _ := authentik.GroupName(name)
	ttl := m.AuthTTL
	if ttl == "" {
		ttl = authentik.DefaultTTL
	}
	return g, "when their current sign-in expires (at most " + ttl + ")", authentik.TTLSeconds(ttl)
}

func failAccess(group string, err error) {
	switch {
	case errors.Is(err, authentik.ErrNoAccount):
		output.Fail("access", output.NewError("NO_ACCOUNT",
			"No platform account with that email", "The person must accept a platform invitation first; a platform admin sends it"))
	case errors.Is(err, authentik.ErrNoGroup):
		output.Fail("access", output.NewError("NOT_FOUND",
			"Group "+group+" not found in Authentik (missing, or not visible to vd)",
			"Redeploy the app (or run vd mcp-oauth) to recreate it; if it exists, a platform admin grants vd view on it"))
	case errors.Is(err, authentik.ErrForbidden):
		output.Fail("access", output.NewError("ACCESS_FORBIDDEN",
			fmt.Sprintf("Authentik does not let vd change %s", group), "A platform admin grants vd rights on this group"))
	default:
		e := output.NewError("ACCESS_FAILED", "Authentik request failed", "Retry; if it persists, check Authentik")
		e.Details = err.Error()
		output.Fail("access", e)
	}
}
