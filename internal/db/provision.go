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

// ProvisionPostgresUser creates a per-app database user.
// adminContainer is where roles are created (primary).
// connectHost is what goes into DATABASE_URL (replica or primary).
func ProvisionPostgresUser(adminContainer, adminUser, connectHost, appName, dbName, access string) (*ProvisionResult, error) {
	container := adminContainer
	user := "vd_" + strings.ReplaceAll(appName, "-", "_")
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
