package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/db"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

func init() {
	rootCmd.AddCommand(dbBackupCmd)
	rootCmd.AddCommand(dbBackupAllCmd)
}

var dbBackupCmd = &cobra.Command{
	Use:   "db-backup <app-name>",
	Short: "Backup an app's database",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		m, err := state.LoadManifest(name)
		if err != nil {
			output.Fail("db-backup", output.NewError("NOT_FOUND", "App not found: "+name, "Check app name with: vd list"))
		}

		result := backupAppDB(name, m)
		if result == nil {
			return
		}

		if !output.IsJSON() {
			fmt.Printf("Backup: %s (%d bytes)\n", result.File, result.Size)
		}
		output.Success("db-backup", result)
	},
}

var dbBackupAllCmd = &cobra.Command{
	Use:   "db-backup-all",
	Short: "Backup all vd-managed databases (for cron)",
	Run: func(cmd *cobra.Command, args []string) {
		apps, err := state.ListApps()
		if err != nil {
			output.Fail("db-backup-all", output.NewError("LIST_FAILED", "Failed to list apps: "+err.Error(), ""))
		}

		var results []db.BackupResult
		var errors []string

		for _, name := range apps {
			m, err := state.LoadManifest(name)
			if err != nil {
				continue
			}
			result := backupAppDB(name, m)
			if result != nil {
				results = append(results, *result)
			}
		}

		if !output.IsJSON() {
			if len(results) == 0 {
				fmt.Println("No databases to backup.")
			} else {
				fmt.Printf("Backed up %d database(s)\n", len(results))
				for _, r := range results {
					fmt.Printf("  %s (%d bytes)\n", r.File, r.Size)
				}
			}
			if len(errors) > 0 {
				for _, e := range errors {
					output.Warn("%s", e)
				}
			}
		}

		output.Success("db-backup-all", map[string]any{
			"backups": results,
			"count":   len(results),
		})
	},
}

// backupAppDB backs up a single app's database. Returns nil if app has no vd-managed DB.
func backupAppDB(name string, m *state.Manifest) *db.BackupResult {
	if m.DB != "postgres" {
		// Only backup vd-managed databases (not prod-ro, not none)
		return nil
	}

	dbName := m.DBName
	if dbName == "" {
		dbName = name
	}

	destDir := state.AppDBBackupsDir(name)
	output.Info("Backing up database %s for %s...", dbName, name)

	result, err := db.DumpDB("vd-postgres", "vd_admin", dbName, destDir)
	if err != nil {
		output.Warn("Failed to backup %s: %v", name, err)
		return nil
	}

	pruned, _ := db.PruneBackups(destDir)
	if pruned > 0 {
		output.Info("Pruned %d old backup(s) for %s", pruned, name)
	}

	return result
}
