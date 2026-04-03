package cmd

import (
	"time"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/backup"
	"github.com/vibe-deploy/vd/internal/db"
	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

var rollbackRestoreDB bool

func init() {
	rollbackCmd.Flags().BoolVar(&rollbackRestoreDB, "restore-db", false, "also restore database to the backed-up state")
	rootCmd.AddCommand(rollbackCmd)
	rootCmd.AddCommand(backupsCmd)
}

var rollbackCmd = &cobra.Command{
	Use:   "rollback <app-name>",
	Short: "Revert to the previous deployment",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		if _, err := state.LoadManifest(name); err != nil {
			output.Fail("rollback", output.NewError("NOT_FOUND", "App not found: "+name, ""))
		}

		output.Info("Rolling back %s...", name)
		meta, err := backup.Restore(name)
		if err != nil {
			output.Fail("rollback", output.NewError("ROLLBACK_FAILED", err.Error(), "Check backup integrity with: vd backups "+name))
		}

		// Wait for health
		containerName := "vd-" + name
		if err := docker.WaitHealthy(containerName, 60*time.Second); err != nil {
			output.Warn("Container may not be healthy: %v", err)
		}

		cs, _ := docker.InspectContainer(containerName)
		health := "unknown"
		if cs != nil {
			health = cs.Health
		}

		// Restore database if requested
		dbRestored := false
		if rollbackRestoreDB && meta.DBBackupFile != "" {
			if meta.Manifest != nil && meta.Manifest.DB == "postgres" {
				dbName := meta.Manifest.DBName
				if dbName == "" {
					dbName = name
				}
				output.Info("Restoring database %s...", dbName)
				if err := db.RestoreDB("vd-postgres", "vd_admin", dbName, meta.DBBackupFile); err != nil {
					output.Warn("Database restore failed: %v", err)
				} else {
					dbRestored = true
					output.Info("Database restored")
				}
			}
		} else if meta.DBBackupFile != "" {
			output.Info("Database backup available. To also restore the database, use: vd rollback %s --restore-db", name)
		}

		output.Info("Rolled back %s to %s", name, meta.Timestamp)
		output.Success("rollback", map[string]any{
			"name":            name,
			"rolled_back_to":  meta.Timestamp,
			"status":          "running",
			"health":          health,
			"db_restored":     dbRestored,
			"db_backup_available": meta.DBBackupFile != "",
		})
	},
}

var backupsCmd = &cobra.Command{
	Use:   "backups <app-name>",
	Short: "List available backups",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		list, err := backup.List(name)
		if err != nil || len(list) == 0 {
			output.Fail("backups", output.NewError("NO_BACKUPS", "No backups found for "+name, "Backups are created automatically on redeploy"))
		}

		if !output.IsJSON() {
			for _, b := range list {
				output.Info("%s — %s", b.Timestamp, b.Created)
			}
		}

		output.Success("backups", map[string]any{
			"app":     name,
			"backups": list,
			"count":   len(list),
		})
	},
}
