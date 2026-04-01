package cmd

import (
	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/db"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

var (
	dbType   string
	dbAccess string
	dbName   string
)

func init() {
	dbCreateCmd.Flags().StringVar(&dbType, "type", "postgres", "database type: postgres or prod-ro")
	dbCreateCmd.Flags().StringVar(&dbAccess, "access", "rw", "access level: rw or ro (prod-ro always uses ro)")
	dbCreateCmd.Flags().StringVar(&dbName, "db-name", "", "database name (default: app name for postgres, required for prod-ro)")
	rootCmd.AddCommand(dbCreateCmd)
}

var dbCreateCmd = &cobra.Command{
	Use:   "db-create <app-name>",
	Short: "Provision a database user for an app",
	Long: `Create a database user for an app.

  --type postgres   New database on vd-managed PostgreSQL (default)
  --type prod-ro    Read-only access to existing prod database`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		cfg, err := state.LoadConfig()
		if err != nil {
			output.Fail("db-create", output.NewError("NOT_INITIALIZED", "Run vd init first", ""))
		}

		var container, adminUser, connectHost, access string

		switch dbType {
		case "prod-ro":
			container = cfg.ProdDBPrimary
			if container == "" {
				output.Fail("db-create", output.NewError("DB_NOT_FOUND",
					"No prod DB configured",
					"Run: vd init --prod-db <primary> --prod-db-user <user>"))
			}
			connectHost = cfg.ProdDBReplica
			if connectHost == "" {
				connectHost = container
			}
			adminUser = cfg.ProdDBUser
			if adminUser == "" {
				adminUser = "postgres"
			}
			access = "ro"
			if dbName == "" {
				output.Fail("db-create", output.NewError("MISSING_DB_NAME",
					"--db-name is required for prod-ro",
					"Example: vd db-create my-app --type prod-ro --db-name reporting_platform"))
			}

		case "postgres":
			container = "vd-postgres"
			connectHost = "vd-postgres"
			adminUser = "vd_admin"
			access = dbAccess
			if dbName == "" {
				dbName = name
			}

		default:
			output.Fail("db-create", output.NewError("INVALID_TYPE",
				"Unknown db type: "+dbType, "Use: postgres or prod-ro"))
		}

		result, err := db.ProvisionPostgresUser(container, adminUser, connectHost, name, dbName, access)
		if err != nil {
			output.Fail("db-create", output.NewError("DB_PROVISION_FAILED", err.Error(),
				"Check that the postgres container is running and accessible"))
		}

		output.Info("Database user created: %s", result.User)
		output.Info("DATABASE_URL=%s", result.URL)

		// Update manifest if app exists
		if m, err := state.LoadManifest(name); err == nil {
			m.DB = dbType
			m.DBAccess = access
			m.DBName = dbName
			m.DBUser = result.User
			state.SaveManifest(m)
		}

		output.Success("db-create", map[string]any{
			"app":          name,
			"type":         dbType,
			"user":         result.User,
			"database":     result.Database,
			"access":       access,
			"database_url": result.URL,
		})
	},
}
