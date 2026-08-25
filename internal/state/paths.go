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

// AppMCPEnvPath holds the MCP server's DATABASE_URI plus the basicauth pair vd
// reports back to the agent. Mode 0600: it is a credentials file, and it lives
// beside the compose file rather than under src/ so it is never copied into the
// application image or handed to the app container.
func AppMCPEnvPath(name string) string   { return filepath.Join(AppDir(name), "mcp.env") }
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
