package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

var (
	logsLines  int
	logsFollow bool
)

func init() {
	logsCmd.Flags().IntVar(&logsLines, "lines", 100, "number of log lines")
	logsCmd.Flags().BoolVar(&logsFollow, "follow", false, "stream logs continuously")
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(logsSnapshotCmd)
}

var logsCmd = &cobra.Command{
	Use:   "logs <app-name>",
	Short: "View app logs",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		m, err := state.LoadManifest(name)
		if err != nil {
			output.Fail("logs", output.NewError("NOT_FOUND", "App not found: "+name, "Check app name with: vd list"))
		}

		if logsFollow {
			// Stream logs — pass through to terminal
			c := docker.ContainerLogsFollow(m.ContainerName, logsLines)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			c.Run()
			return
		}

		logs, err := docker.ContainerLogs(m.ContainerName, logsLines)
		if err != nil {
			output.Fail("logs", output.NewError("LOGS_FAILED", "Failed to get logs: "+err.Error(), ""))
		}
		fmt.Print(logs)
	},
}

var logsSnapshotCmd = &cobra.Command{
	Use:   "logs-snapshot <app-name>",
	Short: "Get app logs (one-shot, for automated workflows)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		m, err := state.LoadManifest(name)
		if err != nil {
			output.Fail("logs-snapshot", output.NewError("NOT_FOUND", "App not found: "+name, "Check app name with: vd list"))
		}

		logs, err := docker.ContainerLogs(m.ContainerName, logsLines)
		if err != nil {
			output.Fail("logs-snapshot", output.NewError("LOGS_FAILED", "Failed to get logs: "+err.Error(), ""))
		}

		if output.IsJSON() {
			output.Success("logs-snapshot", map[string]any{"logs": logs})
			return
		}
		fmt.Print(logs)
	},
}
