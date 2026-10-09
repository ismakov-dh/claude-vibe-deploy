package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/mcpgw"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

var (
	initDomain string

	initAuthentikURL      string
	initAuthentikInternal string
	initAuthentikNetwork  string
	initAuthentikTokenIn  bool

	initProdROURLIn   bool
	initProdRONetwork string
)

func init() {
	initCmd.Flags().StringVar(&initDomain, "domain", "", "base domain for apps (e.g. apps.example.com)")
	initCmd.Flags().StringVar(&initAuthentikURL, "authentik-url", "", "public Authentik URL (e.g. https://auth.example.com)")
	initCmd.Flags().StringVar(&initAuthentikInternal, "authentik-internal", "", "Authentik address on the overlay (e.g. http://authentik_server:9000)")
	initCmd.Flags().StringVar(&initAuthentikNetwork, "authentik-network", "authentik-forward", "overlay vd-traefik joins to reach Authentik")
	initCmd.Flags().BoolVar(&initAuthentikTokenIn, "authentik-token-stdin", false, "read the Authentik API token from stdin")
	initCmd.Flags().BoolVar(&initProdROURLIn, "prod-ro-url-stdin", false, "read the DSN of the prod read-only replica role (for --db prod-ro) from stdin")
	initCmd.Flags().StringVar(&initProdRONetwork, "prod-ro-network", "", "attachable overlay on which only the prod read-only replica resolves")
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

	// Connect existing prod DB containers if specified
	if initAuthentikURL != "" {
		cfg.AuthentikURL = strings.TrimRight(initAuthentikURL, "/")
	}
	if initAuthentikInternal != "" {
		cfg.AuthentikInternal = strings.TrimRight(initAuthentikInternal, "/")
	}
	if initAuthentikNetwork != "" {
		cfg.AuthentikNetwork = initAuthentikNetwork
	}

	// The API token arrives on stdin, never as a flag value: a flag is visible in
	// `ps` and in the ssh wrapper's log line. stdin also works through that
	// wrapper — `... | ssh vd-server "vd init --authentik-token-stdin"` — so
	// installing or rotating the token needs no root and no file shuffling, the
	// same way vd push takes its tar. Persisted 0600, then read from disk.
	if initAuthentikTokenIn && initProdROURLIn {
		output.Fail("init", output.NewError("INIT_FAILED",
			"--authentik-token-stdin and --prod-ro-url-stdin both read stdin", "Run vd init once for each"))
	}
	if initProdRONetwork != "" {
		if !networkNameRe.MatchString(initProdRONetwork) {
			output.Fail("init", output.NewError("INIT_FAILED", "Invalid --prod-ro-network name", ""))
		}
		cfg.ProdRONetwork = initProdRONetwork
	}
	if initProdROURLIn {
		// Same route as the Authentik token: stdin, never a flag (ps, the ssh
		// wrapper's log line). The value is never echoed, not even in errors.
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		dsn := strings.TrimSpace(string(raw))
		if err != nil || !validProdROURL(dsn) {
			output.Fail("init", output.NewError("INIT_FAILED",
				"--prod-ro-url-stdin: stdin is not a postgres:// URL with user, password, host and database",
				"Pipe the DSN in: ... | vd init --prod-ro-url-stdin"))
		}
		if a := appShadowing(dsn); a != "" {
			output.Fail("init", output.NewError("INIT_FAILED",
				"--prod-ro-url-stdin: the app "+a+" answers on vd's networks for the replica's host",
				"Destroy "+a+" first, or use the replica's full host name in the DSN"))
		}
		if err := os.WriteFile(state.ProdROURLPath(), []byte(dsn+"\n"), 0600); err != nil {
			output.Fail("init", output.NewError("INIT_FAILED", "Failed to write prod-ro.url", "Check permissions"))
		}
		os.Chmod(state.ProdROURLPath(), 0600) // WriteFile's mode applies only on create
		state.ChownLikeHome(state.ProdROURLPath())
		output.Info("Stored the prod read-only DSN at %s — redeploy prod-ro apps to pick up a rotated one", state.ProdROURLPath())
	}

	if initAuthentikTokenIn {
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		tok := strings.TrimSpace(string(raw))
		if err != nil || tok == "" {
			output.Fail("init", output.NewError("INIT_FAILED",
				"--authentik-token-stdin given but no token on stdin", "Pipe the token in: ... | vd init --authentik-token-stdin"))
		}
		if err := os.WriteFile(state.AuthentikTokenPath(), []byte(tok+"\n"), 0600); err != nil {
			output.Fail("init", output.NewError("INIT_FAILED",
				"Failed to write authentik.token", "Check permissions"))
		}
		// 0600 and root-owned after a root-run init would lock every agent
		// (vd-user) out of --auth deploys with AUTH_NOT_CONFIGURED.
		state.ChownLikeHome(state.AuthentikTokenPath())
		output.Info("Stored Authentik API token at %s", state.AuthentikTokenPath())
	}

	// Write infrastructure compose file.
	//
	// Templated for one value: Traefik's trusted proxy addresses. See the
	// template's own comment — the gateway is read from the live network because
	// Docker picks the subnet per host, and guessing it breaks forward auth
	// silently.
	infraContent, err := fs.ReadFile(templatesFS, "templates/compose/infrastructure.yml")
	if err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to read embedded infrastructure template", "This is a bug"))
	}
	trusted := "127.0.0.1/32"
	if gws, err := docker.NetworkGateway("vd-net"); err == nil {
		trusted += "," + strings.Join(gws, ",")
	} else {
		output.Warn("Could not read vd-net gateway (%v) — trusting loopback only. "+
			"Apps deployed with --auth may see the wrong scheme.", err)
	}
	infraTmpl, err := template.New("infrastructure").Parse(string(infraContent))
	if err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to parse infrastructure template", "This is a bug"))
	}
	var infraOut strings.Builder
	if err := infraTmpl.Execute(&infraOut, map[string]string{"TrustedIPs": trusted, "MCPGWImage": mcpgw.Image}); err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to render infrastructure template", "This is a bug"))
	}
	if err := os.WriteFile(state.InfraComposePath(), []byte(infraOut.String()), 0644); err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to write infrastructure.yml", "Check permissions"))
	}

	writeTraefikDynamic(cfg)

	// vd-mcpgw needs its routes file before it can start. Derived from the
	// manifests, so on a host with no --mcp-oauth apps it is the 404 fallback.
	output.Info("Pulling %s...", mcpgw.Image)
	if err := docker.PullImage(mcpgw.Image); err != nil {
		output.Warn("Could not pull %s: %v", mcpgw.Image, err)
	}
	if err := syncMCPGateway(cfg); err != nil {
		output.Warn("Could not write vd-mcpgw routes: %v", err)
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
	if err := docker.ComposeApply(filepath.Dir(state.InfraComposePath()), "infrastructure.yml"); err != nil {
		output.Warn("Failed to start infrastructure: %v", err)
		output.Warn("Start manually: cd %s && docker compose -f infrastructure.yml up -d", state.VDHome())
	} else {
		output.Info("Traefik and PostgreSQL are running")
	}

	// Attach vd-traefik to the Authentik overlay — after compose up, always.
	// Compose recreates the container with only the networks it declares, so a
	// recreate drops this attachment and it has to be made again.
	//
	// Deliberately not declared in infrastructure.yml as an external network: if
	// the overlay were missing, `docker compose up` would refuse to start the
	// whole file and take PostgreSQL down with it. A missing overlay should cost
	// forward auth, nothing else.
	if cfg.AuthentikNetwork != "" && cfg.AuthentikInternal != "" {
		if err := docker.NetworkConnect(cfg.AuthentikNetwork, "vd-traefik"); err != nil {
			output.Warn("Could not connect vd-traefik to %s: %v", cfg.AuthentikNetwork, err)
			output.Warn("Apps deployed with --auth will not be able to reach Authentik")
		} else {
			output.Info("Connected vd-traefik to %s", cfg.AuthentikNetwork)
		}
	}

	// Pull the per-app MCP image now. It lives in a private registry, and a
	// compose up that cannot pull takes the whole app deploy down with it — far
	// better that an unreachable registry fails here.
	output.Info("Pulling MCP image %s...", docker.MCPImage)
	if err := docker.PullImage(docker.MCPImage); err != nil {
		output.Warn("Could not pull %s: %v", docker.MCPImage, err)
		output.Warn("Apps deployed with --db postgres will fail until this image is available")
	}

	output.Info("vibe-deploy initialized at %s", state.VDHome())
	if cfg.Domain == "" {
		output.Warn("No domain set. Use: vd init --domain apps.example.com")
	}

	output.Success("init", map[string]any{
		"home":            state.VDHome(),
		"domain":          cfg.Domain,
		"prod_ro_network": cfg.ProdRONetwork,
		"vd_postgres":     "vd-postgres",
		"networks":        []string{"vd-net", "vd-db"},
	})
}

func generateRandomPassword(length int) string {
	b := make([]byte, length/2+1)
	rand.Read(b)
	return hex.EncodeToString(b)[:length]
}

// writeTraefikDynamic renders the file-provider config that points Traefik at
// Authentik. Nothing is written when Authentik is not configured: an empty
// dynamic directory is fine, a half-written service definition is not.
func writeTraefikDynamic(cfg *state.Config) {
	if err := os.MkdirAll(state.TraefikDynamicDir(), 0755); err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to create "+state.TraefikDynamicDir(), "Check permissions"))
	}
	if cfg.AuthentikInternal == "" {
		return
	}
	raw, err := fs.ReadFile(templatesFS, "templates/traefik/authentik.yml.tmpl")
	if err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to read embedded Authentik template", "This is a bug"))
	}
	t, err := template.New("authentik").Parse(string(raw))
	if err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to parse Authentik template", "This is a bug"))
	}
	var out strings.Builder
	if err := t.Execute(&out, map[string]string{
		"Internal": cfg.AuthentikInternal,
		"Network":  cfg.AuthentikNetwork,
	}); err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to render Authentik template", "This is a bug"))
	}
	if err := os.WriteFile(state.AuthentikDynamicPath(), []byte(out.String()), 0644); err != nil {
		output.Fail("init", output.NewError("INIT_FAILED",
			"Failed to write "+state.AuthentikDynamicPath(), "Check permissions"))
	}
	state.ChownLikeHome(state.TraefikDynamicDir())
	state.ChownLikeHome(state.AuthentikDynamicPath())
	output.Info("Wrote Traefik dynamic config for %s", cfg.AuthentikInternal)
}

var networkNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// validProdROURL accepts a complete postgres DSN and nothing that could smuggle
// YAML or shell into the .env it is written to.
func validProdROURL(s string) bool {
	if strings.ContainsAny(s, " \t\r\n\"'`$") {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || u.User == nil {
		return false
	}
	// One host, in the URL: libpq reads "a,b" as several and ?host= overrides
	// it, and vd checks the host against app names (shadowsReplica).
	if strings.Contains(u.Host, ",") || u.Query().Has("host") || u.Query().Has("hostaddr") {
		return false
	}
	pw, ok := u.User.Password()
	return u.User.Username() != "" && ok && pw != "" && strings.Trim(u.Path, "/") != ""
}

// appShadowing names a deployed app whose DNS names on vd's networks include
// the DSN's host, or "".
func appShadowing(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	apps, _ := state.ListApps()
	for _, a := range apps {
		if shadowsReplica(a, strings.ToLower(u.Hostname())) {
			return a
		}
	}
	return ""
}
