package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/cron"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

var (
	cronSchedule string
	cronCommand  string
	cronApp      string
)

func init() {
	cronSetCmd.Flags().StringVar(&cronSchedule, "schedule", "", "cron schedule expression (e.g. '0 * * * *')")
	cronSetCmd.Flags().StringVar(&cronCommand, "command", "", "command to run inside container")
	cronSetCmd.MarkFlagRequired("schedule")
	cronSetCmd.MarkFlagRequired("command")

	cronLsCmd.Flags().StringVar(&cronApp, "app", "", "filter by app name")

	rootCmd.AddCommand(cronSetCmd)
	rootCmd.AddCommand(cronRmCmd)
	rootCmd.AddCommand(cronLsCmd)
}

var cronSetCmd = &cobra.Command{
	Use:   "cron-set <app-name>",
	Short: "Add or update a cron job for an app",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		if _, err := state.LoadManifest(name); err != nil {
			output.Fail("cron-set", output.NewError("NOT_FOUND", "App not found: "+name, ""))
		}

		if err := cron.Set(name, cronSchedule, cronCommand); err != nil {
			output.Fail("cron-set", output.NewError("CRON_FAILED", err.Error(), ""))
		}

		output.Info("Cron job set for %s: %s — %s", name, cronSchedule, cronCommand)
		output.Success("cron-set", map[string]any{
			"app":      name,
			"schedule": cronSchedule,
			"command":  cronCommand,
		})
	},
}

var cronRmCmd = &cobra.Command{
	Use:   "cron-rm <app-name>",
	Short: "Remove all cron jobs for an app",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		if err := cron.Remove(name); err != nil {
			output.Fail("cron-rm", output.NewError("CRON_FAILED", err.Error(), ""))
		}
		output.Info("Cron jobs removed for %s", name)
		output.Success("cron-rm", map[string]any{"app": name, "removed": true})
	},
}

var cronLsCmd = &cobra.Command{
	Use:   "cron-ls",
	Short: "List cron jobs",
	Run: func(cmd *cobra.Command, args []string) {
		jobs, err := cron.List(cronApp)
		if err != nil {
			output.Fail("cron-ls", output.NewError("CRON_FAILED", err.Error(), ""))
		}
		if !output.IsJSON() {
			if len(jobs) == 0 {
				fmt.Println("No cron jobs.")
				return
			}
			for _, j := range jobs {
				fmt.Printf("%-15s %s — %s\n", j.App, j.Schedule, j.Command)
			}
		}
		output.Success("cron-ls", map[string]any{"jobs": jobs, "count": len(jobs)})
	},
}
