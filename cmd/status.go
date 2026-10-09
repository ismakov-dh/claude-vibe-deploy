package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/authentik"
	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

func init() {
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status <app-name>",
	Short: "Show app status",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		m, err := state.LoadManifest(name)
		if err != nil {
			output.Fail("status", output.NewError("NOT_FOUND", "App not found: "+name, "Check app name with: vd list"))
		}

		cs, err := docker.InspectContainer(m.ContainerName)
		if err != nil {
			output.Fail("status", output.NewError("NOT_FOUND", "Container not found: "+m.ContainerName, "App may need redeployment"))
		}

		cfg, _ := state.LoadConfig()
		mcp, mcpHealth := mcpStatus(m, cfg)

		if !output.IsJSON() {
			output.Info("App:       %s", m.Name)
			output.Info("Type:      %s", m.AppType)
			output.Info("URL:       %s", "https://"+m.Domain)
			output.Info("Container: %s", m.ContainerName)
			output.Info("State:     %s", cs.Status)
			output.Info("Health:    %s", cs.Health)
			output.Info("Started:   %s", cs.Started)
			output.Info("Deployed:  %s", m.DeployedAt)
			if m.DB != "" && m.DB != "none" {
				output.Info("Database:  %s (%s)", m.DBSpec(), m.DBAccess)
			}
			if mcp != nil && mcp["available"] == true {
				output.Info("MCP:       %s (%s)", mcp["url"], mcpHealth)
			} else if mcp != nil {
				output.Info("MCP:       unavailable (%s)", mcp["hint"])
			}
		}
		authInfo := authStatus(m, cfg)
		if authInfo != nil && !output.IsJSON() {
			output.Info("Login:     group %s, sign-in lasts %s (%s)", m.AuthGroup, m.AuthTTL, authInfo["state"])
		}

		data := map[string]any{
			"name":        m.Name,
			"app_type":    m.AppType,
			"url":         "https://" + m.Domain,
			"container":   m.ContainerName,
			"state":       cs.Status,
			"health":      cs.Health,
			"started_at":  cs.Started,
			"deployed_at": m.DeployedAt,
			"port":        m.Port,
			"routing":     m.Routing,
			"db":          m.DBSpec(),
		}
		if mcp != nil {
			mcp["health"] = mcpHealth
			if m.MCPOAuthLive {
				mcp["oauth"] = mcpOAuthStatus(m, cfg)
			}
			data["mcp"] = mcp
		}
		if authInfo != nil {
			data["auth"] = authInfo
		}
		output.Success("status", data)
	},
}

// mcpStatus is the MCP block for agents: the sign-in entry point, never the
// credentials. An MCP not behind sign-in is not running, and is reported unavailable.
//
// Gated on m.MCP, not on m.DB: an app deployed before MCP existed has a database
// and no MCP container, and advertising a URL that 404s is worse than silence.
func mcpStatus(m *state.Manifest, cfg *state.Config) (map[string]any, string) {
	if !m.MCP || cfg == nil || cfg.Domain == "" {
		return nil, ""
	}
	return mcpStatusBlock(m, cfg), mcpContainerHealth(m.Name)
}

func mcpStatusBlock(m *state.Manifest, cfg *state.Config) map[string]any {
	if !m.MCPOAuthLive {
		// The MCP is not running: without sign-in it has no way in.
		hint := "its sign-in is not set up — redeploy to retry, or a platform admin runs: vd mcp-oauth " + m.Name
		if cfg.AuthentikURL == "" {
			hint = "platform login is not set up on this server"
		}
		return map[string]any{"available": false, "hint": hint}
	}
	url := "https://" + mcpHost(m.Name, cfg) + "/mcp"
	return map[string]any{
		"available": true,
		"url":       url,
		"group":     "mcp-vibe-" + m.Name,
		"add":       fmt.Sprintf("claude mcp add --transport http %s-db %s", m.Name, url),
	}
}

func mcpContainerHealth(appName string) string {
	cs, err := docker.InspectContainer("vd-" + appName + "-mcp")
	if err != nil {
		return "missing"
	}
	if cs.Health != "" {
		return cs.Health
	}
	return cs.Status
}

// authStatus asks Authentik what exists for the app. It reads, never writes, and
// an unreachable Authentik is reported as such rather than failing vd status.
func authStatus(m *state.Manifest, cfg *state.Config) map[string]any {
	if !m.Auth {
		return nil
	}
	info := map[string]any{"enabled": true, "group": m.AuthGroup, "ttl": m.AuthTTL, "bearer": m.AuthBearer}
	token, err := state.LoadAuthentikToken()
	if err != nil || cfg == nil || cfg.AuthentikURL == "" {
		info["state"] = "unknown"
		info["error"] = "Authentik is not configured on this server"
		return info
	}
	h, err := authentik.New(cfg.AuthentikURL, token).Check(m.Name)
	if err != nil {
		info["state"] = "unknown"
		info["error"] = err.Error()
		return info
	}
	info["authentik"] = h
	info["state"], info["hint"] = authState(h, m.AuthBearer)
	if info["hint"] == "" {
		delete(info, "hint")
	}
	return info
}

func authState(h *authentik.Health, bearer bool) (state, hint string) {
	switch {
	case !h.OK():
		return "broken", "redeploy the app to recreate its Authentik objects"
	case h.HeaderAuth != bearer:
		// Header auth on without vd asking for it lets service accounts in; off
		// when asked for breaks them. Either way the record and reality differ.
		return "drift", fmt.Sprintf("Authentik has header auth %v, vd deployed %v — redeploy with --auth-bearer=%v to confirm, or the other value to change it",
			h.HeaderAuth, bearer, bearer)
	}
	return "ok", ""
}

// mcpOAuthStatus reads what exists in Authentik for the app's MCP resource.
func mcpOAuthStatus(m *state.Manifest, cfg *state.Config) map[string]any {
	info := map[string]any{"enabled": true, "group": "mcp-vibe-" + m.Name}
	if cfg != nil {
		info["url"] = "https://" + mcpHost(m.Name, cfg) + "/mcp"
	}
	token, err := state.LoadAuthentikToken()
	if err != nil || cfg == nil || cfg.AuthentikURL == "" {
		info["state"] = "unknown"
		info["error"] = "Authentik is not configured on this server"
		return info
	}
	h, err := authentik.New(cfg.AuthentikURL, token).CheckMCP(m.Name, m.MCPAppGroup())
	if err != nil {
		info["state"] = "unknown"
		info["error"] = err.Error()
		return info
	}
	info["authentik"] = h
	if h.OK() {
		info["state"] = "ok"
	} else {
		info["state"] = "broken"
		info["hint"] = "redeploy the app to repair its MCP sign-in"
	}
	return info
}
