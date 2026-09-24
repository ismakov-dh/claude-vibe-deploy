package cmd

import (
	"os"
	"strings"

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
				output.Info("Database:  %s (%s)", m.DB, m.DBAccess)
			}
			if mcp != nil {
				output.Info("MCP:       %s (%s)", mcp["url"], mcpHealth)
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
			"db":          m.DB,
		}
		if mcp != nil {
			mcp["health"] = mcpHealth
			data["mcp"] = mcp
		}
		if authInfo != nil {
			data["auth"] = authInfo
		}
		output.Success("status", data)
	},
}

// mcpStatus reads back what deploy wrote. The password lives in mcp.env rather
// than the manifest because the manifest is world-readable and this is not.
//
// Gated on m.MCP, not on m.DB: an app deployed before MCP existed has a database
// and no MCP container, and advertising a URL that 404s is worse than silence.
func mcpStatus(m *state.Manifest, cfg *state.Config) (map[string]any, string) {
	if !m.MCP || cfg == nil || cfg.Domain == "" {
		return nil, ""
	}
	// An MCP the manifest knows about but whose credentials cannot be read is a
	// different thing from no MCP, and it must not look the same. Report the
	// endpoint and say why the credentials are missing — silence here previously
	// hid a working MCP whose mcp.env a root-run deploy had left unreadable.
	data, err := os.ReadFile(state.AppMCPEnvPath(m.Name))
	if err != nil {
		return map[string]any{
			"url":   "https://" + m.Name + ".mcp." + cfg.Domain + "/sse",
			"error": "cannot read " + state.AppMCPEnvPath(m.Name) + ": " + err.Error(),
			"hint":  "redeploy to regenerate the credentials file with the right owner",
		}, mcpContainerHealth(m.Name)
	}

	user, password := "", ""
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "VD_MCP_USER="):
			user = strings.TrimPrefix(line, "VD_MCP_USER=")
		case strings.HasPrefix(line, "VD_MCP_PASSWORD="):
			password = strings.TrimPrefix(line, "VD_MCP_PASSWORD=")
		}
	}
	if user == "" || password == "" {
		return nil, ""
	}
	return mcpInfo(m.Name, m.Name+".mcp."+cfg.Domain, user, password), mcpContainerHealth(m.Name)
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
	info := map[string]any{"enabled": true, "group": m.AuthGroup, "ttl": m.AuthTTL}
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
	if h.OK() {
		info["state"] = "ok"
	} else {
		info["state"] = "broken"
		info["hint"] = "redeploy the app to recreate its Authentik objects"
	}
	return info
}
