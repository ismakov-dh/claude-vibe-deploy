package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/authentik"
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

		// Take the app's objects out of Authentik. The container is already down,
		// so a failure here leaves no exposed app — only litter to clean up — and
		// must not stop the destroy the caller asked for. It is reported, loudly.
		authRemoved := ""
		if m.Auth {
			authRemoved = "removed"
			cfg, _ := state.LoadConfig()
			token, terr := state.LoadAuthentikToken()
			switch {
			case cfg == nil || cfg.AuthentikURL == "":
				authRemoved = "failed: Authentik is not configured on this server"
			case terr != nil:
				authRemoved = "failed: " + terr.Error()
			default:
				output.Info("Removing platform login from Authentik (the group is kept)...")
				if unlock, lerr := state.LockAuthentik(); lerr != nil {
					authRemoved = "failed: " + lerr.Error()
				} else {
					if err := authentik.New(cfg.AuthentikURL, token).Remove(name); err != nil {
						authRemoved = "failed: " + err.Error()
					}
					unlock()
				}
			}
			if authRemoved != "removed" {
				output.Warn("Authentik cleanup %s — a platform admin should remove the provider and application vibe-%s by hand", authRemoved, name)
			}
		}

		// Backup database before dropping
		if destroyDropDB && m.DB == "postgres" {
			dbName := m.DBName
			if dbName == "" {
				dbName = name
			}
			output.Info("Backing up database %s before destroy...", dbName)
			result := backupAppDB(name, m)
			if result != nil {
				output.Info("Backup saved: %s", result.File)
			} else {
				output.Warn("Database backup failed — proceeding with destroy")
			}
		}

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

				// The MCP's companion role holds no objects, so DROP DATABASE
				// succeeds with it still present — but redeploying the same app name
				// would then inherit a stale role and its old password.
				db.DropRole(container, adminUser, db.ReadOnlyRoleName(name))

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

		data := map[string]any{
			"name":             name,
			"destroyed":        true,
			"db_dropped":       dbDropped,
			"backups_retained": state.AppBackupsDir(name),
		}
		if m.Auth {
			data["auth"] = map[string]any{"cleanup": authRemoved, "group_kept": m.AuthGroup}
		}
		output.Success("destroy", data)
	},
}
