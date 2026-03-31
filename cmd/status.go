package cmd

import (
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

		if !output.IsJSON() {
			output.Info("App:       %s", m.Name)
			output.Info("Type:      %s", m.AppType)
			output.Info("URL:       %s", "https://" + m.Domain)
			output.Info("Container: %s", m.ContainerName)
			output.Info("State:     %s", cs.Status)
			output.Info("Health:    %s", cs.Health)
			output.Info("Started:   %s", cs.Started)
			output.Info("Deployed:  %s", m.DeployedAt)
			if m.DB != "" && m.DB != "none" {
				output.Info("Database:  %s (%s)", m.DB, m.DBAccess)
			}
		}

		output.Success("status", map[string]any{
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
		})
	},
}
