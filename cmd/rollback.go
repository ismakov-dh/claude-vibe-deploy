package cmd

import (
	"bytes"
	"errors"
	"os"
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

func keepAuthBearer(name string, bearer bool) error {
	restored, err := state.LoadManifest(name)
	if err != nil || restored.AuthBearer == bearer {
		return err
	}
	restored.AuthBearer = bearer
	return state.SaveManifest(restored)
}

// rollbackProdROGate refuses a restore that would bring production access back
// in a form deploy no longer allows. A backup's compose and .env are restored
// verbatim, so prodROGate never sees them: a prod-ro backup comes back only
// onto an app that is prod-ro now (so detaching or leaving prod-ro stays
// done), only from the replica design, and only if it passes the same gate.
func rollbackProdROGate(cur, b *state.Manifest) *output.VDError {
	if b == nil {
		// Nothing says what the backup restores — prod access included.
		// ponytail: refuses all such rollbacks; none of the 52 backups on prod
		// had a nil manifest when this was written.
		return output.NewError("ROLLBACK_WOULD_UNPROTECT",
			"The backup carries no manifest, so vd cannot tell what access it would restore",
			"Redeploy a fixed version instead")
	}
	if b.DB != "prod-ro" {
		return nil
	}
	refuse := func(why string) *output.VDError {
		return output.NewError("ROLLBACK_WOULD_UNPROTECT",
			"The previous version of "+b.Name+" read production data "+why+"; rolling back would restore that access",
			"Redeploy a fixed version instead")
	}
	if cur == nil || cur.DB != "prod-ro" {
		return refuse("and the current version does not")
	}
	if b.ProdRONetwork == "" {
		return refuse("through the retired per-app prod user")
	}
	if e := prodROGate("prod-ro", b.Auth, b.AuthTTL); e != nil {
		return refuse("without the login rules prod-ro now requires (" + e.Message + ")")
	}
	return nil
}

var rollbackCmd = &cobra.Command{
	Use:   "rollback <app-name>",
	Short: "Revert to the previous deployment",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		if !nameRegex.MatchString(name) {
			output.Fail("rollback", output.NewError("INVALID_NAME", "Invalid app name: "+name, ""))
		}
		unlock, lerr := state.LockApp(name)
		if lerr != nil {
			output.Fail("rollback", output.NewError("ROLLBACK_FAILED", "Could not take the app lock: "+lerr.Error(), ""))
		}
		defer unlock()
		cur, err := state.LoadManifest(name)
		if err != nil {
			output.Fail("rollback", output.NewError("NOT_FOUND", "App not found: "+name, ""))
		}

		// A backup from before --auth would restore a compose file without the
		// forward-auth chain: the app would come back public. Refuse rather than
		// quietly drop protection.
		if cur.Auth {
			if _, meta, err := backup.Latest(name); err == nil && (meta == nil || meta.Manifest == nil || !meta.Manifest.Auth) {
				output.Fail("rollback", output.NewError("ROLLBACK_WOULD_UNPROTECT",
					"The previous version of "+name+" was deployed without platform login; rolling back would make it public",
					"Redeploy a fixed version with --auth instead"))
			}
		}

		if _, meta, err := backup.Latest(name); err == nil && meta != nil {
			if e := rollbackProdROGate(cur, meta.Manifest); e != nil {
				output.Fail("rollback", e)
			}
		}

		output.Info("Rolling back %s...", name)
		meta, err := backup.Restore(name, dropBasicMCP)
		if err != nil {
			output.Fail("rollback", output.NewError("ROLLBACK_FAILED", err.Error(), "Check backup integrity with: vd backups "+name))
		}

		// Rollback restores code, not Authentik: the provider keeps its header
		// auth, so the manifest must keep saying so, or the next plain redeploy
		// would silently turn a service account's access off (or on).
		if err := keepAuthBearer(name, cur.AuthBearer); err != nil {
			output.Warn("Could not keep auth_bearer=%v in the restored manifest: %v", cur.AuthBearer, err)
		}

		// The restored manifest decides whether the MCP has an OAuth route.
		if cfg, _ := state.LoadConfig(); cfg != nil {
			if err := syncMCPGateway(cfg); err != nil {
				output.Warn("Could not rewrite vd-mcpgw routes: %v", err)
			}
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
			"name":                name,
			"rolled_back_to":      meta.Timestamp,
			"status":              "running",
			"health":              health,
			"db_restored":         dbRestored,
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

// dropBasicMCP re-renders a restored compose file that still carries the MCP's
// Basic route — a backup from before vd removed it — so the restored app starts
// with its MCP behind sign-in, or without the MCP when sign-in cannot be set up.
// Any other restored compose file is left exactly as it was.
func dropBasicMCP(meta *backup.Metadata) error {
	if meta.Manifest == nil {
		// Unreachable today (rollback refuses these, deploy backs up only with
		// one) — and nothing to re-render from, so refuse to start.
		return errors.New("backup has no manifest")
	}
	name := meta.Manifest.Name
	compose, err := os.ReadFile(state.AppComposePath(name))
	if err != nil {
		return err
	}
	if !hasBasicMCP(compose) {
		return nil
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		return err
	}
	m := meta.Manifest
	// No owner: the backup's may since have been removed with vd access.
	m.MCPOAuthLive = m.MCP && ensureMCPOAuth(name, cfg, "") != nil
	ingress := ""
	if m.Auth {
		if ingress = envValue(state.AppEnvPath(name), ingressEnvKey); ingress == "" {
			return errors.New("the restored app is behind --auth but its ingress secret is missing")
		}
	}
	if err := state.SaveManifest(m); err != nil {
		return err
	}
	output.Info("Restored version had the MCP's Basic route; re-rendered it without (MCP running: %v)", m.MCPOAuthLive)
	return docker.GenerateComposeFile(templatesFS, composeDataFor(m, cfg, ingress), state.AppComposePath(name))
}

// hasBasicMCP reports whether a compose file from an older vd still has the
// MCP's Basic route.
func hasBasicMCP(compose []byte) bool {
	return bytes.Contains(compose, []byte("mcp-basic")) || bytes.Contains(compose, []byte("basicauth.users"))
}
