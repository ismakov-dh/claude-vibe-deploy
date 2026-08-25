package backup

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vibe-deploy/vd/internal/db"
	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/state"
)

const maxBackups = 5

// Metadata describes a single backup.
type Metadata struct {
	App          string          `json:"app"`
	Timestamp    string          `json:"timestamp"`
	ImageID      string          `json:"image_id"`
	Created      string          `json:"created"`
	Manifest     *state.Manifest `json:"manifest"`
	DBBackupFile string          `json:"db_backup_file,omitempty"`
}

// Create backs up the current deployment before a new deploy.
func Create(appName string) error {
	containerName := "vd-" + appName
	ts := time.Now().UTC().Format("20060102_150405")
	backupDir := filepath.Join(state.AppBackupsDir(appName), ts)

	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}

	// Save Docker image
	imageID, err := docker.GetImageID(containerName)
	if err == nil && imageID != "" {
		imgPath := filepath.Join(backupDir, "image.tar.gz")
		if err := docker.SaveImage(containerName, imgPath); err != nil {
			return fmt.Errorf("save image: %w", err)
		}
	}

	// Copy compose file
	copyFile(state.AppComposePath(appName), filepath.Join(backupDir, "docker-compose.vd.yml"))

	// Copy env file if exists
	copyFile(state.AppEnvPath(appName), filepath.Join(backupDir, ".env"))

	// The MCP server's credentials file. Backed up alongside the compose file so a
	// restored pair is coherent — the compose references ./mcp.env by name.
	copyFile(state.AppMCPEnvPath(appName), filepath.Join(backupDir, "mcp.env"))

	// Backup database if vd-managed
	manifest, _ := state.LoadManifest(appName)
	var dbBackupFile string
	if manifest != nil && manifest.DB == "postgres" {
		dbName := manifest.DBName
		if dbName == "" {
			dbName = appName
		}
		result, err := db.DumpDB("vd-postgres", "vd_admin", dbName, backupDir)
		if err == nil {
			dbBackupFile = result.File
		}
		// non-fatal — continue even if DB backup fails
	}

	// Copy manifest
	meta := Metadata{
		App:          appName,
		Timestamp:    ts,
		ImageID:      imageID,
		Created:      time.Now().UTC().Format(time.RFC3339),
		Manifest:     manifest,
		DBBackupFile: dbBackupFile,
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(filepath.Join(backupDir, "metadata.json"), metaJSON, 0644)

	// Prune old backups
	prune(appName)

	return nil
}

// Restore rolls back to the most recent backup.
func Restore(appName string) (*Metadata, error) {
	backupDir, meta, err := Latest(appName)
	if err != nil {
		return nil, err
	}

	appDir := state.AppDir(appName)

	// Stop current container
	docker.ComposeDown(appDir, "docker-compose.vd.yml")

	// Load image
	imgPath := filepath.Join(backupDir, "image.tar.gz")
	if fileExists(imgPath) {
		if err := docker.LoadImage(imgPath); err != nil {
			return nil, fmt.Errorf("load image: %w", err)
		}
	}

	// Restore compose file
	copyFile(filepath.Join(backupDir, "docker-compose.vd.yml"), state.AppComposePath(appName))

	// Restore env files
	copyFile(filepath.Join(backupDir, ".env"), state.AppEnvPath(appName))
	copyFile(filepath.Join(backupDir, "mcp.env"), state.AppMCPEnvPath(appName))

	// Restore manifest
	if meta.Manifest != nil {
		state.SaveManifest(meta.Manifest)
	}

	// Put the database passwords back to what the restored files say.
	//
	// Provisioning mints a fresh password on every deploy and ALTERs the role, so
	// by the time a failed deploy rolls back, the credentials in these files are
	// the ones the database has already forgotten. Without this the app starts,
	// passes its health check and cannot reach its database — and the MCP endpoint
	// answers 500 to every query.
	restorePasswords(appName, meta.Manifest)

	// Start restored container
	if err := docker.ComposeUp(appDir, "docker-compose.vd.yml"); err != nil {
		return nil, fmt.Errorf("start restored container: %w", err)
	}

	return meta, nil
}

// Latest returns the most recent backup directory and its metadata.
func Latest(appName string) (string, *Metadata, error) {
	dirs, err := listBackupDirs(appName)
	if err != nil || len(dirs) == 0 {
		return "", nil, fmt.Errorf("no backups found for %s", appName)
	}
	dir := dirs[len(dirs)-1]
	meta, _ := loadMetadata(dir)
	return dir, meta, nil
}

// List returns all backup metadata for an app.
func List(appName string) ([]Metadata, error) {
	dirs, err := listBackupDirs(appName)
	if err != nil {
		return nil, err
	}
	var result []Metadata
	for _, d := range dirs {
		if m, err := loadMetadata(d); err == nil {
			result = append(result, *m)
		}
	}
	return result, nil
}

func listBackupDirs(appName string) ([]string, error) {
	base := state.AppBackupsDir(appName)
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(base, e.Name()))
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

func prune(appName string) {
	dirs, err := listBackupDirs(appName)
	if err != nil || len(dirs) <= maxBackups {
		return
	}
	for _, d := range dirs[:len(dirs)-maxBackups] {
		os.RemoveAll(d)
	}
}

func loadMetadata(dir string) (*Metadata, error) {
	data, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		return nil, err
	}
	var m Metadata
	return &m, json.Unmarshal(data, &m)
}

// restorePasswords re-ALTERs every role named in the restored env files.
func restorePasswords(appName string, m *state.Manifest) {
	if m == nil || m.DB == "" || m.DB == "none" {
		return
	}

	container, adminUser := "vd-postgres", "vd_admin"
	if m.DB == "prod-ro" {
		cfg, err := state.LoadConfig()
		if err != nil || cfg.ProdDBPrimary == "" {
			return
		}
		container = cfg.ProdDBPrimary
		adminUser = cfg.ProdDBUser
		if adminUser == "" {
			adminUser = "postgres"
		}
	}

	for path, key := range map[string]string{
		state.AppEnvPath(appName):    "DATABASE_URL",
		state.AppMCPEnvPath(appName): "DATABASE_URI",
	} {
		role, password := credsFromEnvFile(path, key)
		if role != "" && password != "" {
			db.SetRolePassword(container, adminUser, role, password)
		}
	}
}

// credsFromEnvFile pulls the role and password out of a postgres URL in a .env
// file. Returns empty strings for anything it cannot parse — a missing or
// malformed file means there is nothing to restore, not an error worth failing
// a rollback over.
func credsFromEnvFile(path, key string) (string, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, key+"=") {
			continue
		}
		u, err := url.Parse(strings.TrimPrefix(line, key+"="))
		if err != nil || u.User == nil {
			return "", ""
		}
		password, _ := u.User.Password()
		return u.User.Username(), password
	}
	return "", ""
}

// copyFile preserves the source mode. It used to write 0644 unconditionally,
// which quietly downgraded .env and mcp.env from 0600 every time a backup was
// taken or restored.
func copyFile(src, dst string) {
	info, err := os.Stat(src)
	if err != nil {
		return
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	os.WriteFile(dst, data, info.Mode().Perm())
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
