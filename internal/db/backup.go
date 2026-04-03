package db

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vibe-deploy/vd/internal/shell"
)

const maxBackups = 7

// BackupResult holds info about a completed backup.
type BackupResult struct {
	File     string `json:"file"`
	Database string `json:"database"`
	Size     int64  `json:"size_bytes"`
}

// DumpDB runs pg_dump inside the container and writes a gzipped SQL file.
func DumpDB(container, adminUser, dbName, destDir string) (*BackupResult, error) {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("create backup dir: %w", err)
	}

	ts := time.Now().UTC().Format("20060102-150405")
	filename := fmt.Sprintf("%s-%s.sql.gz", dbName, ts)
	destPath := filepath.Join(destDir, filename)

	// pg_dump inside container, gzip on host via pipe
	_, err := shell.Run(5*time.Minute, "sh", "-c",
		fmt.Sprintf("docker exec %s pg_dump -U %s %s | gzip > %s",
			container, adminUser, dbName, destPath))
	if err != nil {
		os.Remove(destPath)
		return nil, fmt.Errorf("pg_dump failed: %w", err)
	}

	info, err := os.Stat(destPath)
	if err != nil {
		return nil, err
	}

	return &BackupResult{
		File:     destPath,
		Database: dbName,
		Size:     info.Size(),
	}, nil
}

// RestoreDB restores a gzipped SQL dump into the database.
func RestoreDB(container, adminUser, dbName, backupPath string) error {
	_, err := shell.Run(10*time.Minute, "sh", "-c",
		fmt.Sprintf("gunzip -c %s | docker exec -i %s psql -U %s -d %s",
			backupPath, container, adminUser, dbName))
	return err
}

// ListBackups returns backup files for a given directory, newest first.
func ListBackups(dir string) ([]BackupResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var backups []BackupResult
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		backups = append(backups, BackupResult{
			File: filepath.Join(dir, e.Name()),
			Size: info.Size(),
		})
	}

	// Newest first (filenames contain timestamps, so lexicographic reverse works)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].File > backups[j].File
	})
	return backups, nil
}

// PruneBackups keeps only the newest maxBackups files in the directory.
func PruneBackups(dir string) (int, error) {
	backups, err := ListBackups(dir)
	if err != nil || len(backups) <= maxBackups {
		return 0, err
	}

	removed := 0
	for _, b := range backups[maxBackups:] {
		if os.Remove(b.File) == nil {
			removed++
		}
	}
	return removed, nil
}
