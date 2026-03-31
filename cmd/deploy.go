package cmd

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/app"
	"github.com/vibe-deploy/vd/internal/backup"
	"github.com/vibe-deploy/vd/internal/db"
	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

var (
	deployName    string
	deployPort    int
	deployRouting string
	deployDB      string
	deployDBAccess string
	deployDBName  string
	deployEnvFile string
)

func init() {
	deployCmd.Flags().StringVar(&deployName, "name", "", "app name (default: directory name)")
	deployCmd.Flags().IntVar(&deployPort, "port", 0, "internal app port (default: auto-detected)")
	deployCmd.Flags().StringVar(&deployRouting, "routing", "subdomain", "routing mode: subdomain or path")
	deployCmd.Flags().StringVar(&deployDB, "db", "none", "database: postgres, prod-ro, or none")
	deployCmd.Flags().StringVar(&deployDBAccess, "db-access", "rw", "database access: rw or ro")
	deployCmd.Flags().StringVar(&deployDBName, "db-name", "", "database name (default: app name)")
	deployCmd.Flags().StringVar(&deployEnvFile, "env-file", "", "path to .env file")
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

	// Set default port from app type if not specified
	if deployPort == 0 {
		deployPort = appType.DefaultPort()
	}

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

	// Provision database if requested
	if deployDB == "postgres" || deployDB == "prod-ro" {
		var container, adminUser, access, dbNameToUse string

		if deployDB == "prod-ro" {
			container = cfg.ProdDBContainer
			if container == "" {
				output.Fail("deploy", output.NewError("DB_NOT_FOUND",
					"No prod DB configured",
					"Run: vd init --prod-db <container> --prod-db-user <user>"))
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
			adminUser = "vd_admin"
			access = deployDBAccess
			dbNameToUse = deployDBName
			if dbNameToUse == "" {
				dbNameToUse = deployName
			}
		}

		output.Info("Provisioning database (%s)...", deployDB)
		result, err := db.ProvisionPostgresUser(container, adminUser, deployName, dbNameToUse, access)
		if err != nil {
			output.Warn("DB provisioning failed: %v", err)
		} else {
			output.Info("Database ready: %s (user: %s)", result.Database, result.User)
			// Append DATABASE_URL to env file
			envPath := state.AppEnvPath(deployName)
			f, _ := os.OpenFile(envPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if f != nil {
				f.WriteString("DATABASE_URL=" + result.URL + "\n")
				f.Close()
			}
			hasEnvFile = true
		}
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
		Name:          deployName,
		AppType:       string(appType),
		Port:          deployPort,
		Routing:       deployRouting,
		Domain:        cfg.Domain,
		HasEnvFile:    hasEnvFile,
		NeedsDB:       needsDB,
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
	}
	if err := state.SaveManifest(manifest); err != nil {
		output.Warn("Failed to save manifest: %v", err)
	}

	url := "https://" + domain
	output.Info("Deployed %s → %s", deployName, url)

	output.Success("deploy", map[string]any{
		"name":        deployName,
		"app_type":    string(appType),
		"url":         url,
		"status":      "running",
		"health":      "healthy",
		"port":        deployPort,
		"routing":     deployRouting,
		"db":          deployDB,
		"deployed_at": time.Now().UTC().Format(time.RFC3339),
	})
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
