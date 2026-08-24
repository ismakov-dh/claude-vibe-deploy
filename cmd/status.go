package cmd

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
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
	user, password := "", ""
	data, err := os.ReadFile(state.AppMCPEnvPath(m.Name))
	if err != nil {
		return nil, ""
	}
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

	health := "unknown"
	if cs, err := docker.InspectContainer("vd-" + m.Name + "-mcp"); err == nil {
		health = cs.Health
		if health == "" {
			health = cs.Status
		}
	}
	return mcpInfo(m.Name, m.Name+".mcp."+cfg.Domain, user, password), health
}
