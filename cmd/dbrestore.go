package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/db"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

var restoreFile string

func init() {
	dbRestoreCmd.Flags().StringVar(&restoreFile, "file", "", "specific backup file to restore (default: latest)")
	rootCmd.AddCommand(dbRestoreCmd)
	rootCmd.AddCommand(dbBackupsCmd)
}

var dbRestoreCmd = &cobra.Command{
	Use:   "db-restore <app-name>",
	Short: "Restore an app's database from backup",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		m, err := state.LoadManifest(name)
		if err != nil {
			output.Fail("db-restore", output.NewError("NOT_FOUND", "App not found: "+name, "Check app name with: vd list"))
		}

		if m.DB != "postgres" {
			output.Fail("db-restore", output.NewError("NO_DB", "App has no vd-managed database", "Only apps deployed with --db postgres can be restored"))
		}

		backupPath := restoreFile
		if backupPath == "" {
			// Find latest backup
			backups, err := db.ListBackups(state.AppDBBackupsDir(name))
			if err != nil || len(backups) == 0 {
				output.Fail("db-restore", output.NewError("NO_BACKUPS", "No backups found for "+name, "Run: vd db-backup "+name))
			}
			backupPath = backups[0].File
		}

		dbName := m.DBName
		if dbName == "" {
			dbName = name
		}

		output.Info("Restoring %s from %s...", dbName, filepath.Base(backupPath))

		if err := db.RestoreDB("vd-postgres", "vd_admin", dbName, backupPath); err != nil {
			output.Fail("db-restore", output.NewError("RESTORE_FAILED", "Restore failed: "+err.Error(), "Check backup file integrity"))
		}

		if !output.IsJSON() {
			fmt.Printf("Restored %s from %s\n", dbName, filepath.Base(backupPath))
		}
		output.Success("db-restore", map[string]any{
			"name":     name,
			"database": dbName,
			"file":     backupPath,
		})
	},
}

var dbBackupsCmd = &cobra.Command{
	Use:   "db-backups <app-name>",
	Short: "List database backups for an app",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		backups, err := db.ListBackups(state.AppDBBackupsDir(name))
		if err != nil {
			output.Fail("db-backups", output.NewError("LIST_FAILED", "Failed to list backups: "+err.Error(), ""))
		}

		if !output.IsJSON() {
			if len(backups) == 0 {
				fmt.Printf("No backups for %s\n", name)
			} else {
				for _, b := range backups {
					fmt.Printf("  %s (%d bytes)\n", filepath.Base(b.File), b.Size)
				}
			}
		}

		output.Success("db-backups", map[string]any{
			"app":     name,
			"backups": backups,
			"count":   len(backups),
		})
	},
}
