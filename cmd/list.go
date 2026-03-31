package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

func init() {
	rootCmd.AddCommand(listCmd)
}

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all deployed apps",
	Run: func(cmd *cobra.Command, args []string) {
		apps, err := state.ListApps()
		if err != nil {
			output.Fail("list", output.NewError("LIST_FAILED", "Failed to list apps: "+err.Error(), ""))
		}

		type appInfo struct {
			Name       string `json:"name"`
			AppType    string `json:"app_type"`
			URL        string `json:"url"`
			State      string `json:"state"`
			Health     string `json:"health"`
			DeployedAt string `json:"deployed_at"`
		}
		var results []appInfo

		for _, name := range apps {
			m, err := state.LoadManifest(name)
			if err != nil {
				continue
			}
			info := appInfo{
				Name:       m.Name,
				AppType:    m.AppType,
				URL:        "https://" + m.Domain,
				State:      "unknown",
				Health:     "unknown",
				DeployedAt: m.DeployedAt,
			}
			if cs, err := docker.InspectContainer(m.ContainerName); err == nil {
				info.State = cs.Status
				info.Health = cs.Health
			}
			results = append(results, info)
		}

		if !output.IsJSON() {
			if len(results) == 0 {
				fmt.Println("No apps deployed.")
				return
			}
			fmt.Printf("%-20s %-15s %-10s %-10s %s\n", "NAME", "TYPE", "STATE", "HEALTH", "URL")
			for _, a := range results {
				fmt.Printf("%-20s %-15s %-10s %-10s %s\n", a.Name, a.AppType, a.State, a.Health, a.URL)
			}
		}

		output.Success("list", map[string]any{
			"apps":  results,
			"count": len(results),
		})
	},
}
