package state

import (
	"os"
	"path/filepath"
)

// VDHome returns the base directory for vibe-deploy state.
func VDHome() string {
	if v := os.Getenv("VD_HOME"); v != "" {
		return v
	}
	return "/opt/vibe-deploy"
}

func AppsDir() string                    { return filepath.Join(VDHome(), "apps") }
func AppDir(name string) string          { return filepath.Join(AppsDir(), name) }
func AppSrcDir(name string) string       { return filepath.Join(AppDir(name), "src") }
func AppManifestPath(name string) string { return filepath.Join(AppDir(name), "manifest.json") }
func AppComposePath(name string) string  { return filepath.Join(AppDir(name), "docker-compose.vd.yml") }
func AppEnvPath(name string) string      { return filepath.Join(AppSrcDir(name), ".env") }
func BackupsDir() string                 { return filepath.Join(VDHome(), "backups") }
func AppBackupsDir(name string) string   { return filepath.Join(BackupsDir(), name) }
func DBBackupsDir() string               { return filepath.Join(VDHome(), "db-backups") }
func AppDBBackupsDir(name string) string { return filepath.Join(DBBackupsDir(), name) }
func LogsDir() string                    { return filepath.Join(VDHome(), "logs") }
func AppLogsDir(name string) string      { return filepath.Join(LogsDir(), name) }
func ConfigPath() string                 { return filepath.Join(VDHome(), "config.json") }
func InfraComposePath() string           { return filepath.Join(VDHome(), "infrastructure.yml") }
func InfraEnvPath() string               { return filepath.Join(VDHome(), ".env") }
func PushDir(name string) string         { return filepath.Join(VDHome(), "push", name) }
