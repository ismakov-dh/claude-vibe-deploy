package db

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/vibe-deploy/vd/internal/shell"
)

// ProvisionResult holds the result of database user provisioning.
type ProvisionResult struct {
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
	URL      string `json:"url"`
}

// RoleName is the database role for an app. Hyphens are legal in app names and
// illegal in unquoted SQL identifiers, so they become underscores.
func RoleName(appName string) string {
	return "vd_" + strings.ReplaceAll(appName, "-", "_")
}

// ReadOnlyRoleName is the companion role the MCP server connects as.
func ReadOnlyRoleName(appName string) string {
	return RoleName(appName) + "_ro"
}

// ProvisionPostgresUser creates a per-app database user.
// adminContainer is where roles are created (primary).
// connectHost is what goes into DATABASE_URL (replica or primary).
func ProvisionPostgresUser(adminContainer, adminUser, connectHost, user, dbName, access string) (*ProvisionResult, error) {
	container := adminContainer
	password := generatePassword(24)

	// Create database if not exists
	execSQL(container, adminUser, fmt.Sprintf("CREATE DATABASE %q", dbName))

	// Create or reset user (idempotent — handles redeploys)
	// If role exists, reset password instead of drop/recreate (avoids grant dependency issues)
	err := execSQL(container, adminUser, fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", user, password))
	if err != nil {
		// Role likely exists — update password
		execSQL(container, adminUser, fmt.Sprintf("ALTER ROLE %s WITH LOGIN PASSWORD '%s'", user, password))
	}

	// Grant permissions
	if access == "ro" {
		// NOTE: the ALTER DEFAULT PRIVILEGES below covers only objects created by
		// adminUser, because no FOR ROLE clause is given. That is the whole of what
		// Postgres offers without naming the creating role, and for prod-ro the
		// creating roles belong to the production application, not to us. So a
		// read-only app sees the tables that existed when it was deployed, and picks
		// up newer ones on its next deploy via the GRANT ... ON ALL TABLES above.
		// ProvisionReadOnlyCompanion does have an owner to name, and does name it.
		execSQL(container, adminUser, fmt.Sprintf("GRANT CONNECT ON DATABASE %q TO %s", dbName, user))
		execSQLDB(container, adminUser, dbName, fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s", user))
		execSQLDB(container, adminUser, dbName, fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA public TO %s", user))
		execSQLDB(container, adminUser, dbName, fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO %s", user))
	} else {
		execSQL(container, adminUser, fmt.Sprintf("GRANT ALL PRIVILEGES ON DATABASE %q TO %s", dbName, user))
		execSQLDB(container, adminUser, dbName, fmt.Sprintf("GRANT ALL ON SCHEMA public TO %s", user))
		execSQLDB(container, adminUser, dbName, fmt.Sprintf("GRANT ALL ON ALL TABLES IN SCHEMA public TO %s", user))
		execSQLDB(container, adminUser, dbName, fmt.Sprintf("GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO %s", user))
		execSQLDB(container, adminUser, dbName, fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO %s", user))
		execSQLDB(container, adminUser, dbName, fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO %s", user))
	}

	url := fmt.Sprintf("postgresql://%s:%s@%s:5432/%s", user, password, connectHost, dbName)

	return &ProvisionResult{
		User:     user,
		Password: password,
		Database: dbName,
		URL:      url,
	}, nil
}

// ProvisionReadOnlyCompanion creates a SELECT-only role alongside an existing
// read-write owner, for the per-app MCP server to connect as.
//
// The FOR ROLE clause is the load-bearing part. Without it, ALTER DEFAULT
// PRIVILEGES applies only to objects created by whoever ran the statement —
// adminUser — and the app creates its tables as ownerRole, so the companion
// would end up with SELECT on nothing at all. On a first deploy that is
// literally nothing: provisioning runs before the container has ever started,
// so there is no table to GRANT on yet and the default privileges are the only
// thing standing between the MCP and "permission denied" for every query.
func ProvisionReadOnlyCompanion(adminContainer, adminUser, connectHost, ownerRole, roRole, dbName string) (*ProvisionResult, error) {
	password := generatePassword(24)

	if err := execSQL(adminContainer, adminUser,
		fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", roRole, password)); err != nil {
		execSQL(adminContainer, adminUser,
			fmt.Sprintf("ALTER ROLE %s WITH LOGIN PASSWORD '%s'", roRole, password))
	}

	execSQL(adminContainer, adminUser, fmt.Sprintf("GRANT CONNECT ON DATABASE %q TO %s", dbName, roRole))
	execSQLDB(adminContainer, adminUser, dbName, fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s", roRole))
	execSQLDB(adminContainer, adminUser, dbName, fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA public TO %s", roRole))
	execSQLDB(adminContainer, adminUser, dbName,
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT ON TABLES TO %s", ownerRole, roRole))

	// PUBLIC keeps TEMPORARY on every database unless revoked, so a "read-only"
	// role can still create and write temp tables. Postgres 15 already revoked
	// PUBLIC's CREATE on schema public, so this is the remaining hole. Revoking
	// from PUBLIC also revokes it from ownerRole, hence the re-grant — apps do
	// use temp tables, the MCP has no business doing so.
	execSQL(adminContainer, adminUser, fmt.Sprintf("REVOKE TEMPORARY ON DATABASE %q FROM PUBLIC", dbName))
	execSQL(adminContainer, adminUser, fmt.Sprintf("GRANT TEMPORARY ON DATABASE %q TO %s", dbName, ownerRole))

	return &ProvisionResult{
		User:     roRole,
		Password: password,
		Database: dbName,
		URL:      fmt.Sprintf("postgresql://%s:%s@%s:5432/%s", roRole, password, connectHost, dbName),
	}, nil
}

// SetRolePassword forces a role's password to a known value.
//
// Used by rollback: provisioning mints a fresh password on every deploy, so a
// restored .env carries credentials the database no longer accepts. Restoring
// the files without restoring the password leaves an app that starts, passes its
// health check and cannot reach its database.
func SetRolePassword(container, adminUser, role, password string) error {
	return execSQL(container, adminUser,
		fmt.Sprintf("ALTER ROLE %s WITH LOGIN PASSWORD '%s'", role, password))
}

// DropRole removes a role, ignoring absence.
func DropRole(container, adminUser, role string) error {
	return execSQL(container, adminUser, fmt.Sprintf("DROP ROLE IF EXISTS %s", role))
}

// DropPostgresDB drops a database and its user.
func DropPostgresDB(container, adminUser, dbName, user string) error {
	// Terminate active connections
	execSQL(container, adminUser, fmt.Sprintf(
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()", dbName))
	// Revoke and drop
	execSQL(container, adminUser, fmt.Sprintf("REVOKE ALL ON DATABASE %q FROM %s", dbName, user))
	execSQL(container, adminUser, fmt.Sprintf("DROP DATABASE IF EXISTS %q", dbName))
	execSQL(container, adminUser, fmt.Sprintf("DROP ROLE IF EXISTS %s", user))
	return nil
}

func execSQL(container, adminUser, sql string) error {
	_, err := shell.Run(30*time.Second, "docker", "exec", container,
		"psql", "-U", adminUser, "-d", "postgres", "-c", sql)
	return err
}

func execSQLDB(container, adminUser, dbName, sql string) error {
	_, err := shell.Run(30*time.Second, "docker", "exec", container,
		"psql", "-U", adminUser, "-d", dbName, "-c", sql)
	return err
}

func generatePassword(length int) string {
	b := make([]byte, length/2+1)
	rand.Read(b)
	return hex.EncodeToString(b)[:length]
}
