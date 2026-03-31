package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

var destroyYes bool

func init() {
	destroyCmd.Flags().BoolVar(&destroyYes, "yes", false, "skip confirmation")
	rootCmd.AddCommand(destroyCmd)
}

var destroyCmd = &cobra.Command{
	Use:   "destroy <app-name>",
	Short: "Remove an app completely",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		m, err := state.LoadManifest(name)
		if err != nil {
			output.Fail("destroy", output.NewError("NOT_FOUND", "App not found: "+name, "Check app name with: vd list"))
		}

		if !destroyYes && !output.IsJSON() {
			fmt.Printf("Destroy app %q? This will stop the container and remove all files. [y/N] ", name)
			var answer string
			fmt.Scanln(&answer)
			if answer != "y" && answer != "Y" {
				fmt.Println("Cancelled.")
				return
			}
		}

		// Stop and remove container
		output.Info("Stopping container...")
		appDir := state.AppDir(name)
		docker.ComposeDown(appDir, "docker-compose.vd.yml")

		// Remove app directory (keep backups)
		output.Info("Removing app files...")
		os.RemoveAll(appDir)

		output.Info("Destroyed %s (backups retained at %s)", name, state.AppBackupsDir(name))

		output.Success("destroy", map[string]any{
			"name":              name,
			"destroyed":         true,
			"backups_retained":  state.AppBackupsDir(name),
		})

		_ = m // used for future cleanup (cron, db user, etc.)
	},
}
