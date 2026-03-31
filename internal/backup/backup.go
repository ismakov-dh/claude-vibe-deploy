package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/state"
)

const maxBackups = 5

// Metadata describes a single backup.
type Metadata struct {
	App       string          `json:"app"`
	Timestamp string          `json:"timestamp"`
	ImageID   string          `json:"image_id"`
	Created   string          `json:"created"`
	Manifest  *state.Manifest `json:"manifest"`
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

	// Copy manifest
	manifest, _ := state.LoadManifest(appName)
	meta := Metadata{
		App:       appName,
		Timestamp: ts,
		ImageID:   imageID,
		Created:   time.Now().UTC().Format(time.RFC3339),
		Manifest:  manifest,
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

	// Restore env file
	copyFile(filepath.Join(backupDir, ".env"), state.AppEnvPath(appName))

	// Restore manifest
	if meta.Manifest != nil {
		state.SaveManifest(meta.Manifest)
	}

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

func copyFile(src, dst string) {
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	os.WriteFile(dst, data, 0644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
