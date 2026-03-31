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
// adminUser is the postgres superuser (e.g. "postgres" or "reporting_user").
func ProvisionPostgresUser(container, adminUser, appName, dbName, access string) (*ProvisionResult, error) {
	user := "vd_" + strings.ReplaceAll(appName, "-", "_")
	password := generatePassword(24)

	// Create database if not exists (ignores error if already exists)
	execSQL(container, adminUser, fmt.Sprintf("CREATE DATABASE %q", dbName))

	// Create user
	execSQL(container, adminUser, fmt.Sprintf("DROP ROLE IF EXISTS %s", user))
	if err := execSQL(container, adminUser, fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", user, password)); err != nil {
		return nil, fmt.Errorf("create role: %w", err)
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

	url := fmt.Sprintf("postgresql://%s:%s@%s:5432/%s", user, password, container, dbName)

	return &ProvisionResult{
		User:     user,
		Password: password,
		Database: dbName,
		URL:      url,
	}, nil
}

func execSQL(container, adminUser, sql string) error {
	_, err := shell.Run(30*time.Second, "docker", "exec", container,
		"psql", "-U", adminUser, "-c", sql)
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
