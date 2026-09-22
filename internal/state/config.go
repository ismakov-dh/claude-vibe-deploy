package state

import (
	"encoding/json"
	"os"
	"time"
)

// Config is the global vibe-deploy configuration.
type Config struct {
	Version string `json:"version"`
	Domain  string `json:"domain"`

	// VD-managed postgres (for apps that need their own database)
	VDPostgresPassword string `json:"vd_postgres_password,omitempty"`

	// External prod DB (read-only attach for dashboards)
	ProdDBPrimary string `json:"prod_db_primary,omitempty"` // primary — where users are created
	ProdDBReplica string `json:"prod_db_replica,omitempty"` // replica — where apps connect to read
	ProdDBUser    string `json:"prod_db_user,omitempty"`    // admin user for creating roles

	// Authentik — the identity provider behind `vd deploy --auth`.
	// AuthentikInternal is an address on the overlay vd-traefik joins (see
	// AuthentikNetwork), not the public host: the outpost resolves an app's
	// provider from X-Forwarded-Host, and going out through nginx and the swarm
	// Traefik would overwrite that header. docs/plans/forward-auth.md §2.
	AuthentikURL      string `json:"authentik_url,omitempty"`
	AuthentikInternal string `json:"authentik_internal,omitempty"`
	AuthentikNetwork  string `json:"authentik_network,omitempty"`

	CreatedAt string `json:"created_at"`
}

func DefaultConfig(domain string) *Config {
	return &Config{
		Version:   "1.0.0",
		Domain:    domain,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func LoadConfig() (*Config, error) {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func SaveConfig(cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), data, 0644)
}
