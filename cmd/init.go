package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

var (
	initDomain        string
	initProdContainer string
	initProdUser      string
)

func init() {
	initCmd.Flags().StringVar(&initDomain, "domain", "", "base domain for apps (e.g. apps.example.com)")
	initCmd.Flags().StringVar(&initProdContainer, "prod-db", "", "existing prod postgres container (read-only access for dashboards)")
	initCmd.Flags().StringVar(&initProdUser, "prod-db-user", "postgres", "admin user on the prod postgres")
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize vibe-deploy on this server",
	Long:  "Creates directory structure, Docker networks, starts Traefik + managed PostgreSQL. Idempotent.",
	Run: func(cmd *cobra.Command, args []string) {
		runInit()
	},
}

func runInit() {
	// Create directory structure
	dirs := []string{
		state.AppsDir(),
		state.BackupsDir(),
		state.LogsDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			output.Fail("init", output.NewError("INIT_FAILED",
				"Failed to create directory: "+d, "Check permissions — run as root or with sudo"))
		}
	}
	output.Info("Created directory structure at %s", state.VDHome())

	// Create Docker networks
	for _, net := range []string{"vd-net", "vd-db"} {
		if !docker.NetworkExists(net) {
			if err := docker.NetworkCreate(net); err != nil {
				output.Fail("init", output.NewError("NETWORK_FAILED",
					"Failed to create network: "+net, "Is Docker running?"))
			}
			output.Info("Created Docker network: %s", net)
		} else {
			output.Info("Docker network already exists: %s", net)
		}
	}

	// Load or create config
	cfg, _ := state.LoadConfig()
	if cfg == nil {
		cfg = state.DefaultConfig(initDomain)
	}
	if initDomain != "" {
		cfg.Domain = initDomain
	}

	// Generate VD postgres password if not set
	if cfg.VDPostgresPassword == "" {
		cfg.VDPostgresPassword = generateRandomPassword(32)
	}

	// Connect existing prod DB container if specified
	if initProdContainer != "" {
		if err := docker.NetworkConnect("vd-db", initProdContainer); err != nil {
			output.Warn("Could not connect %s to vd-db network: %v", initProdContainer, err)
			output.Warn("Connect manually: docker network connect vd-db %s", initProdContainer)
		} else {
			output.Info("Connected prod DB %s to vd-db network", initProdContainer)
		}
		cfg.ProdDBContainer = initProdContainer
	}
	if initProdUser != "postgres" || cfg.ProdDBUser == "" {
		cfg.ProdDBUser = initProdUser
	}

	// Write infrastructure compose file
	infraContent, err := fs.ReadFile(templatesFS, "templates/compose/infrastructure.yml")
	if err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to read embedded infrastructure template", "This is a bug"))
	}
	if err := os.WriteFile(state.InfraComposePath(), infraContent, 0644); err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to write infrastructure.yml", "Check permissions"))
	}

	// Write .env for infrastructure compose (postgres password)
	envContent := fmt.Sprintf("VD_POSTGRES_PASSWORD=%s\n", cfg.VDPostgresPassword)
	if err := os.WriteFile(state.InfraEnvPath(), []byte(envContent), 0600); err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to write infrastructure .env", "Check permissions"))
	}

	// Save config
	if err := state.SaveConfig(cfg); err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to write config.json", "Check permissions"))
	}

	// Start infrastructure (Traefik + VD Postgres)
	output.Info("Starting infrastructure (Traefik + PostgreSQL)...")
	if err := docker.ComposeUp(filepath.Dir(state.InfraComposePath()), "infrastructure.yml"); err != nil {
		output.Warn("Failed to start infrastructure: %v", err)
		output.Warn("Start manually: cd %s && docker compose -f infrastructure.yml up -d", state.VDHome())
	} else {
		output.Info("Traefik and PostgreSQL are running")
	}

	output.Info("vibe-deploy initialized at %s", state.VDHome())
	if initProdContainer == "" {
		output.Info("To attach prod DB for dashboards: vd init --prod-db <container-name> --prod-db-user <user>")
	}
	if cfg.Domain == "" {
		output.Warn("No domain set. Use: vd init --domain apps.example.com")
	}

	output.Success("init", map[string]any{
		"home":           state.VDHome(),
		"domain":         cfg.Domain,
		"prod_db":        cfg.ProdDBContainer,
		"vd_postgres":    "vd-postgres",
		"networks":       []string{"vd-net", "vd-db"},
	})
}

func generateRandomPassword(length int) string {
	b := make([]byte, length/2+1)
	rand.Read(b)
	return hex.EncodeToString(b)[:length]
}
