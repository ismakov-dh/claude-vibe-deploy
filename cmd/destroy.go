package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/db"
	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

var (
	destroyYes    bool
	destroyDropDB bool
)

func init() {
	destroyCmd.Flags().BoolVar(&destroyYes, "yes", false, "skip confirmation")
	destroyCmd.Flags().BoolVar(&destroyDropDB, "drop-db", false, "also drop database and user")
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
			msg := fmt.Sprintf("Destroy app %q? This will stop the container and remove all files.", name)
			if destroyDropDB {
				msg += " DATABASE WILL BE DROPPED."
			}
			fmt.Printf("%s [y/N] ", msg)
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

		// Drop database and user if requested
		dbDropped := false
		if destroyDropDB && m.DB != "" && m.DB != "none" {
			cfg, _ := state.LoadConfig()
			var container, adminUser string

			if m.DB == "prod-ro" {
				// Never drop prod databases
				output.Warn("Skipping DB drop — prod-ro databases are not managed by vd")
			} else if cfg != nil {
				container = "vd-postgres"
				adminUser = "vd_admin"

				dbName := m.DBName
				if dbName == "" {
					dbName = name
				}
				user := m.DBUser
				if user == "" {
					user = "vd_" + name
				}

				output.Info("Dropping database %s and user %s...", dbName, user)
				if err := db.DropPostgresDB(container, adminUser, dbName, user); err != nil {
					output.Warn("Failed to drop database: %v", err)
				} else {
					dbDropped = true
					output.Info("Database dropped")
				}
			}
		}

		// Remove app directory (keep backups)
		output.Info("Removing app files...")
		os.RemoveAll(appDir)

		output.Info("Destroyed %s (backups retained at %s)", name, state.AppBackupsDir(name))

		output.Success("destroy", map[string]any{
			"name":             name,
			"destroyed":        true,
			"db_dropped":       dbDropped,
			"backups_retained": state.AppBackupsDir(name),
		})
	},
}
