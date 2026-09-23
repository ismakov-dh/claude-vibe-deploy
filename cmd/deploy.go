package cmd

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/app"
	"github.com/vibe-deploy/vd/internal/authentik"
	"github.com/vibe-deploy/vd/internal/backup"
	"github.com/vibe-deploy/vd/internal/db"
	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/policy"
	"github.com/vibe-deploy/vd/internal/state"
)

var (
	deployName          string
	deployPort          int
	deployRouting       string
	deployDB            string
	deployDBAccess      string
	deployDBName        string
	deployEnvFile       string
	deployAllowExternal bool
	deployAuth          bool
	deployAuthTTL       string
)

func init() {
	deployCmd.Flags().StringVar(&deployName, "name", "", "app name (default: directory name)")
	deployCmd.Flags().IntVar(&deployPort, "port", 0, "internal app port (default: auto-detected)")
	deployCmd.Flags().StringVar(&deployRouting, "routing", "subdomain", "routing mode: subdomain or path")
	deployCmd.Flags().StringVar(&deployDB, "db", "none", "database: postgres, prod-ro, or none")
	deployCmd.Flags().StringVar(&deployDBAccess, "db-access", "rw", "database access: rw or ro")
	deployCmd.Flags().StringVar(&deployDBName, "db-name", "", "database name (default: app name)")
	deployCmd.Flags().StringVar(&deployEnvFile, "env-file", "", "path to .env file")
	deployCmd.Flags().BoolVar(&deployAllowExternal, "allow-external", false, "silence warnings about unsupported external services (Supabase, Firebase, etc.)")
	deployCmd.Flags().BoolVar(&deployAuth, "auth", false, "put the app behind platform login (Authentik forward auth); sticky once set")
	deployCmd.Flags().StringVar(&deployAuthTTL, "auth-ttl", "", "how long a sign-in lasts before re-checking with Authentik, e.g. days=7 or hours=1 (default days=7)")
	rootCmd.AddCommand(deployCmd)
}

var nameRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)

var deployCmd = &cobra.Command{
	Use:   "deploy <source-dir>",
	Short: "Deploy an app",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runDeploy(args[0])
	},
}

func runDeploy(srcPath string) {
	// Resolve source directory
	srcPath, err := filepath.Abs(srcPath)
	if err != nil {
		output.Fail("deploy", output.NewError("INVALID_SOURCE", "Invalid source path", "Provide an absolute or relative path to the app directory"))
	}
	if info, err := os.Stat(srcPath); err != nil || !info.IsDir() {
		output.Fail("deploy", output.NewError("INVALID_SOURCE", "Source directory does not exist: "+srcPath, "Check the path"))
	}

	// Derive app name
	if deployName == "" {
		deployName = filepath.Base(srcPath)
		deployName = strings.ToLower(deployName)
		deployName = regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(deployName, "-")
		deployName = strings.Trim(deployName, "-")
	}
	if !nameRegex.MatchString(deployName) {
		output.Fail("deploy", output.NewError("INVALID_NAME",
			"Invalid app name: "+deployName,
			"Must be lowercase, start with a letter, 2-63 chars, only a-z/0-9/hyphens"))
	}

	// Load global config
	cfg, err := state.LoadConfig()
	if err != nil {
		output.Fail("deploy", output.NewError("NOT_INITIALIZED",
			"vibe-deploy not initialized", "Run: vd init --domain <domain>"))
	}
	if cfg.Domain == "" && deployRouting == "subdomain" {
		output.Fail("deploy", output.NewError("NO_DOMAIN",
			"No domain configured", "Run: vd init --domain <domain>"))
	}

	// Detect app type
	appType, err := app.Detect(srcPath)
	if err != nil || appType == "" {
		output.Fail("deploy", output.NewError("DETECTION_FAILED",
			"Cannot detect app type in "+srcPath,
			"Create a .vd-type file with one of: static-plain, static-build, node-server, node-next, python-flask, python-fastapi, python-django, go, custom"))
	}
	output.Info("Detected app type: %s", appType)

	if appType != app.Custom {
		if _, err := os.Stat(filepath.Join(srcPath, "Dockerfile")); err == nil {
			output.Warn("Source contains a Dockerfile but it will be ignored. To use a custom Dockerfile, create a .vd-type file containing 'custom'.")
		}
	}

	// Policy scan: block hardcoded secrets, warn on unsupported external services.
	var policyWarnings []string
	if scan, _ := policy.Scan(srcPath); scan != nil {
		if len(scan.Secrets) > 0 {
			var lines []string
			for _, f := range scan.Secrets {
				lines = append(lines, fmt.Sprintf("%s:%d %s (%s)", f.File, f.Line, f.Kind, f.Excerpt))
			}
			e := output.NewError("POLICY_VIOLATION",
				"Hardcoded secret(s) detected in source",
				"Remove the secret from source. Put secrets in a .env file and pass it with --env-file — never hardcode credentials.")
			e.Details = strings.Join(lines, "\n")
			output.Fail("deploy", e)
		}
		if len(scan.External) > 0 && !deployAllowExternal {
			for _, f := range scan.External {
				loc := f.File
				if f.Line > 0 {
					loc = fmt.Sprintf("%s:%d", f.File, f.Line)
				}
				w := fmt.Sprintf("%s (%s) — the platform provides PostgreSQL (--db postgres) and read-only prod access (--db prod-ro). If intentional, redeploy with --allow-external.", loc, f.Kind)
				policyWarnings = append(policyWarnings, w)
				output.Warn("%s", w)
			}
		}
	}

	// Set default port from app type if not specified
	if deployPort == 0 {
		deployPort = appType.DefaultPort()
	}

	// Forward auth runs before anything on this host changes. If Authentik cannot
	// be brought into shape, the running app — protected or not — stays exactly
	// as it was, and a first deploy publishes nothing.
	auth := resolveAuth(cfg)

	// Check for existing deployment and backup
	isRedeploy := false
	if _, err := state.LoadManifest(deployName); err == nil {
		isRedeploy = true
		output.Info("Existing deployment found — backing up...")
		if err := backup.Create(deployName); err != nil {
			output.Warn("Backup failed: %v (continuing anyway)", err)
		} else {
			output.Info("Backup created")
		}
	}

	// Keep the ingress secret stable across redeploys. Read before anything can
	// replace src/.env — both the source copy (a pushed tree may carry its own
	// .env) and --env-file do — and written back once they are done.
	ingressSecret := ""
	if auth != nil {
		ingressSecret = envValue(state.AppEnvPath(deployName), ingressEnvKey)
		if ingressSecret == "" {
			ingressSecret = generateRandomPassword(48)
		}
	}

	// Create app directory structure
	appDir := state.AppDir(deployName)
	appSrcDir := state.AppSrcDir(deployName)
	os.MkdirAll(appSrcDir, 0755)

	// Copy source files
	output.Info("Copying source files...")
	if err := copyDir(srcPath, appSrcDir); err != nil {
		output.Fail("deploy", output.NewError("COPY_FAILED",
			"Failed to copy source: "+err.Error(), "Check permissions and disk space"))
	}

	// Copy env file if provided
	hasEnvFile := false
	if deployEnvFile != "" {
		envData, err := os.ReadFile(deployEnvFile)
		if err != nil {
			output.Fail("deploy", output.NewError("ENV_FILE_MISSING",
				"Cannot read env file: "+deployEnvFile, "Check the file path"))
		}
		os.WriteFile(state.AppEnvPath(deployName), envData, 0600)
		hasEnvFile = true
	} else if _, err := os.Stat(state.AppEnvPath(deployName)); err == nil {
		hasEnvFile = true
	}

	// MCP wiring, filled in during provisioning below.
	needsMCP := false
	mcpAuth := ""
	mcpHost := ""
	mcpPassword := ""

	// Provision database if requested
	if deployDB == "postgres" || deployDB == "prod-ro" {
		var container, adminUser, access, dbNameToUse string

		var connectHost string

		if deployDB == "prod-ro" {
			container = cfg.ProdDBPrimary
			if container == "" {
				output.Fail("deploy", output.NewError("DB_NOT_FOUND",
					"No prod DB configured",
					"Run: vd init --prod-db <primary> --prod-db-user <user>"))
			}
			connectHost = cfg.ProdDBReplica
			if connectHost == "" {
				connectHost = container // fall back to primary
			}
			adminUser = cfg.ProdDBUser
			if adminUser == "" {
				adminUser = "postgres"
			}
			access = "ro"
			dbNameToUse = deployDBName
			if dbNameToUse == "" {
				output.Fail("deploy", output.NewError("MISSING_DB_NAME",
					"--db-name is required for prod-ro",
					"Example: vd deploy ./app --name myapp --db prod-ro --db-name reporting_platform"))
			}
		} else {
			container = "vd-postgres"
			connectHost = "vd-postgres"
			adminUser = "vd_admin"
			access = deployDBAccess
			dbNameToUse = deployDBName
			if dbNameToUse == "" {
				dbNameToUse = deployName
			}
		}

		output.Info("Provisioning database (%s)...", deployDB)
		ownerRole := db.RoleName(deployName)
		result, err := db.ProvisionPostgresUser(container, adminUser, connectHost, ownerRole, dbNameToUse, access)
		if err != nil {
			output.Warn("DB provisioning failed: %v", err)
		} else {
			output.Info("Database ready: %s (user: %s)", result.Database, result.User)
			if err := setEnvVar(state.AppEnvPath(deployName), "DATABASE_URL", result.URL); err != nil {
				output.Warn("Could not write DATABASE_URL: %v", err)
			}
			hasEnvFile = true

			// Read-only MCP, for vd-managed databases only. Production stays
			// reachable solely by deploying a dashboard with --db prod-ro.
			if deployDB == "postgres" && cfg.Domain != "" {
				roRole := db.ReadOnlyRoleName(deployName)
				ro, err := db.ProvisionReadOnlyCompanion(container, adminUser, connectHost, ownerRole, roRole, dbNameToUse)
				if err != nil {
					output.Warn("MCP database role failed: %v — deploying without MCP", err)
				} else {
					mcpPassword = generateRandomPassword(32)
					mcpAuth = htpasswdSHA(mcpUser, mcpPassword)
					if err := writeMCPEnv(deployName, ro.URL, mcpUser, mcpPassword); err != nil {
						output.Warn("Could not write mcp.env: %v — deploying without MCP", err)
						mcpAuth = ""
					} else {
						needsMCP = true
						mcpHost = deployName + ".mcp." + cfg.Domain
						output.Info("Read-only MCP will be available at https://%s/sse", mcpHost)
					}
				}
			}
		}
	}

	if auth != nil {
		if err := setEnvVar(state.AppEnvPath(deployName), ingressEnvKey, ingressSecret); err != nil {
			output.Fail("deploy", output.NewError("AUTH_FAILED",
				"Could not write the ingress secret: "+err.Error(), "Check permissions"))
		}
		hasEnvFile = true
	}

	// Generate Dockerfile from template
	if appType != app.Custom {
		tmplPath := appType.DockerfileTemplate()
		tmplContent, err := fs.ReadFile(templatesFS, tmplPath)
		if err != nil {
			output.Fail("deploy", output.NewError("TEMPLATE_ERROR",
				"Failed to read Dockerfile template for "+string(appType), "This is a bug"))
		}
		if err := os.WriteFile(filepath.Join(appSrcDir, "Dockerfile.vd"), tmplContent, 0644); err != nil {
			output.Fail("deploy", output.NewError("WRITE_FAILED",
				"Failed to write Dockerfile.vd", "Check permissions"))
		}
		output.Info("Generated Dockerfile for %s", appType)
	} else {
		// Copy existing Dockerfile as Dockerfile.vd
		data, _ := os.ReadFile(filepath.Join(appSrcDir, "Dockerfile"))
		os.WriteFile(filepath.Join(appSrcDir, "Dockerfile.vd"), data, 0644)
	}

	// Generate docker-compose.vd.yml
	needsDB := deployDB == "postgres" || deployDB == "prod-ro"
	domain := buildDomain(deployName, cfg.Domain, deployRouting)

	composeData := docker.ComposeData{
		Name:         deployName,
		AppType:      string(appType),
		Port:         deployPort,
		Routing:      deployRouting,
		Domain:       cfg.Domain,
		HasEnvFile:   hasEnvFile,
		NeedsDB:      needsDB,
		NeedsMCP:     needsMCP,
		MCPImage:     docker.MCPImage,
		MCPBasicAuth: mcpAuth,
	}
	if auth != nil {
		composeData.Auth = true
		composeData.IngressSecret = ingressSecret
	}
	if err := docker.GenerateComposeFile(templatesFS, composeData, state.AppComposePath(deployName)); err != nil {
		output.Fail("deploy", output.NewError("COMPOSE_FAILED",
			"Failed to generate compose file: "+err.Error(), "This is a bug"))
	}

	// If redeploying, stop old container first
	if isRedeploy {
		output.Info("Stopping previous deployment...")
		docker.ComposeDown(appDir, "docker-compose.vd.yml")
	}

	// Build and start
	output.Info("Building and starting container...")
	if err := docker.ComposeUp(appDir, "docker-compose.vd.yml"); err != nil {
		e := output.NewError("BUILD_FAILED",
			"Docker build/start failed", "Check Dockerfile and source code")
		e.Details = err.Error()
		output.Fail("deploy", e)
	}

	// Wait for health check
	containerName := "vd-" + deployName
	output.Info("Waiting for container to become healthy...")
	if err := docker.WaitHealthy(containerName, 120*time.Second); err != nil {
		// Get logs for debugging
		logs, _ := docker.ContainerLogs(containerName, 30)
		e := output.NewError("UNHEALTHY",
			"Container did not become healthy within 60s",
			"Check logs with: vd logs "+deployName)
		e.Details = logs
		// Rollback if this was a redeploy
		if isRedeploy {
			output.Warn("Rolling back to previous version...")
			docker.ComposeDown(appDir, "docker-compose.vd.yml")
			backup.Restore(deployName)
		}
		output.Fail("deploy", e)
	}
	output.Info("Container is healthy")

	// Save manifest
	deployCount := 1
	if m, _ := state.LoadManifest(deployName); m != nil {
		deployCount = m.DeployCount + 1
	}
	manifest := &state.Manifest{
		Name:          deployName,
		AppType:       string(appType),
		Port:          deployPort,
		Routing:       deployRouting,
		DB:            deployDB,
		DBAccess:      deployDBAccess,
		DBName:        deployDBName,
		Domain:        domain,
		ContainerName: containerName,
		DeployCount:   deployCount,
		HasEnvFile:    hasEnvFile,
		MCP:           needsMCP,
	}
	if auth != nil {
		manifest.Auth = true
		manifest.AuthTTL = auth.ttl
		manifest.AuthGroup = auth.group
	}
	if err := state.SaveManifest(manifest); err != nil {
		output.Warn("Failed to save manifest: %v", err)
	}

	url := "https://" + domain
	output.Info("Deployed %s → %s", deployName, url)

	data := map[string]any{
		"name":        deployName,
		"app_type":    string(appType),
		"url":         url,
		"status":      "running",
		"health":      "healthy",
		"port":        deployPort,
		"routing":     deployRouting,
		"db":          deployDB,
		"deployed_at": time.Now().UTC().Format(time.RFC3339),
	}
	if needsMCP {
		data["mcp"] = mcpInfo(deployName, mcpHost, mcpUser, mcpPassword)
	}
	if auth != nil {
		data["auth"] = map[string]any{
			"enabled":   true,
			"group":     auth.group,
			"ttl":       auth.ttl,
			"authentik": cfg.AuthentikURL,
			"grant":     "Add people to the group " + auth.group + " in Authentik — nothing else is needed.",
		}
	}
	if len(policyWarnings) > 0 {
		output.SuccessWithWarnings("deploy", data, policyWarnings)
	} else {
		output.Success("deploy", data)
	}
}

// mcpUser is fixed; the password is per-app and regenerated on every deploy.
const mcpUser = "mcp"

// htpasswdSHA builds a Traefik basicauth entry using the {SHA} scheme.
//
// Traefik accepts MD5 (apr1), SHA1 and bcrypt. bcrypt would mean adding
// golang.org/x/crypto to a tool whose only dependency is cobra, and apr1 is
// ~40 lines of Apache MD5-crypt. Against a 128-bit random password the missing
// salt is not a practical weakness — an unsalted SHA1 of 32 hex characters is
// not brute-forceable, and the password is never reused anywhere. {SHA} also
// contains no '$', so there is no compose interpolation to escape.
func htpasswdSHA(user, password string) string {
	sum := sha1.Sum([]byte(password))
	return user + ":{SHA}" + base64.StdEncoding.EncodeToString(sum[:])
}

// mcpInfo returns the block an agent needs to register the server. The ready-made
// command matters: the audience is non-programmers and agents reading JSON, and
// neither should have to assemble a base64 Basic header by hand.
func mcpInfo(appName, host, user, password string) map[string]any {
	url := "https://" + host + "/sse"
	cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	return map[string]any{
		"url":      url,
		"user":     user,
		"password": password,
		"add": fmt.Sprintf(`claude mcp add --transport sse %s-db %s --header "Authorization: Basic %s"`,
			appName, url, cred),
	}
}

// writeMCPEnv writes the MCP container's environment. The basicauth pair is
// stored alongside DATABASE_URI so vd status can report it; it is also visible
// inside the MCP container, which costs nothing — anything that can read that
// container's environment already holds its database URI.
//
// The ownership dance is not decoration. vd runs both as root (admin, over a
// login shell) and as vd-user (every agent, through the forced-command wrapper).
// Every other file in the app directory already exists by the time a deploy
// rewrites it, so it keeps its original owner; this one is created fresh, so a
// root deploy leaves it root-owned and 0600 — unreadable to vd-user. The
// symptoms are two, both silent: `vd status` omits the mcp block for an MCP that
// is up and serving, and the next deploy as vd-user cannot overwrite the file, so
// it drops the MCP entirely with a warning nobody reads.
func writeMCPEnv(appName, dbURI, user, password string) error {
	path := state.AppMCPEnvPath(appName)

	// Unlink first, so a vd-user deploy can replace a file root left behind.
	// Removing is governed by write permission on the directory, which vd-user
	// owns, not by ownership of the file itself.
	os.Remove(path)

	body := fmt.Sprintf("DATABASE_URI=%s\nVD_MCP_USER=%s\nVD_MCP_PASSWORD=%s\n", dbURI, user, password)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		return err
	}

	// Match the app directory's owner. Fails with EPERM when a non-root user runs
	// this, which is exactly the case where ownership is already correct.
	if fi, err := os.Stat(state.AppDir(appName)); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			os.Chown(path, int(st.Uid), int(st.Gid))
		}
	}
	return nil
}

// setEnvVar replaces a key in a .env file, or appends it. Appending blindly —
// which is what this used to do — left one DATABASE_URL line per deploy, and
// rollback has to be able to read the password back out of this file.
func setEnvVar(path, key, value string) error {
	var kept []string
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" || strings.HasPrefix(line, key+"=") {
				continue
			}
			kept = append(kept, line)
		}
	}
	kept = append(kept, key+"="+value)
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0600); err != nil {
		return err
	}
	// Explicit, because the 0600 above only applies when the file is created.
	// This file holds DATABASE_URL with a live password, and it can arrive at
	// 0644 by routes that predate any of this — a .env inside a pushed tar keeps
	// the tar's mode, and a backup taken before copyFile preserved modes was
	// restored 0644. Chmod on every write makes it self-healing instead of
	// something a human has to notice and fix.
	return os.Chmod(path, 0600)
}

func buildDomain(name, baseDomain, routing string) string {
	if routing == "path" {
		return baseDomain + "/" + name
	}
	return name + "." + baseDomain
}

// copyDir copies the contents of src into dst using rsync-like behavior.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip node_modules, .git, __pycache__, .venv
		base := filepath.Base(path)
		if info.IsDir() && (base == "node_modules" || base == ".git" || base == "__pycache__" || base == ".venv" || base == "venv") {
			return filepath.SkipDir
		}

		relPath, _ := filepath.Rel(src, path)
		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dstPath, data, info.Mode())
	})
}

// ingressEnvKey is where the app finds the secret Traefik stamps on every request
// that passed forward auth. Kept in the app's .env so backup and rollback carry
// it together with the compose file that holds the other half.
const ingressEnvKey = "VIBE_INGRESS_SECRET"

type authPlan struct {
	group string
	ttl   string
}

// resolveAuth decides whether this deploy is protected and, if so, provisions
// Authentik. It returns nil for a public app and exits on any failure.
func resolveAuth(cfg *state.Config) *authPlan {
	prev, _ := state.LoadManifest(deployName)
	want := deployAuth || (prev != nil && prev.Auth)
	if !want {
		if deployAuthTTL != "" {
			output.Fail("deploy", output.NewError("INVALID_AUTH_TTL",
				"--auth-ttl given without --auth", "Add --auth, or drop --auth-ttl"))
		}
		return nil
	}
	if !deployAuth {
		output.Info("App is deployed with platform login — keeping it on (to make it public: vd destroy, then deploy)")
	}

	ttl := deployAuthTTL
	if ttl == "" && prev != nil && prev.AuthTTL != "" {
		ttl = prev.AuthTTL
	}
	if ttl == "" {
		ttl = authentik.DefaultTTL
	}
	if !authentik.ValidTTL(ttl) {
		output.Fail("deploy", output.NewError("INVALID_AUTH_TTL",
			"Invalid --auth-ttl: "+ttl, "Use Authentik's format, e.g. days=7, hours=1, days=1;hours=12"))
	}
	if deployRouting != "subdomain" {
		output.Fail("deploy", output.NewError("AUTH_REQUIRES_SUBDOMAIN",
			"--auth needs subdomain routing",
			"Drop --routing path. The login flow and its cookie are bound to the app's own host."))
	}
	if err := cfg.AuthentikReady(); err != nil {
		output.Fail("deploy", output.NewError("AUTH_NOT_CONFIGURED",
			"Platform login is not set up on this server: "+err.Error(),
			"A platform admin runs: vd init --authentik-url <url> --authentik-internal <addr> (see docs/plans/forward-auth.md)"))
	}
	token, _ := state.LoadAuthentikToken()

	output.Info("Setting up platform login in Authentik...")
	res, err := authentik.New(cfg.AuthentikURL, token).Ensure(authentik.Spec{
		App:          deployName,
		ExternalHost: "https://" + deployName + "." + cfg.Domain,
		TTL:          ttl,
	})
	if err != nil {
		e := output.NewError("AUTH_FAILED",
			"Could not set up platform login in Authentik — nothing was deployed or changed on this server",
			"Retry; if it persists, a platform admin should check the Authentik side")
		e.Details = err.Error()
		output.Fail("deploy", e)
	}
	if res.TTLChanged {
		output.Warn("Sign-in lifetime changed to %s. The Authentik outpost may keep the old value "+
			"for existing sessions until the Authentik server is restarted.", ttl)
	}
	output.Info("Platform login ready — access is membership in the group %s", res.Group)
	return &authPlan{group: res.Group, ttl: ttl}
}

// envValue reads one key from a .env file, or "" when absent.
func envValue(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}
