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

// AppMCPEnvPath holds the MCP server's DATABASE_URI (files from before Basic was
// removed also carry VD_MCP_USER/VD_MCP_PASSWORD, which nothing reads). Mode 0600: it is a credentials file, and it lives
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

// TraefikDynamicDir holds Traefik's file-provider configuration, mounted read-only
// into vd-traefik. The docker provider cannot express an upstream that is not a
// container, and Authentik is not one — see docs/plans/forward-auth.md.
func TraefikDynamicDir() string    { return filepath.Join(VDHome(), "traefik-dynamic") }
func AuthentikDynamicPath() string { return filepath.Join(TraefikDynamicDir(), "authentik.yml") }

// AuthentikTokenPath holds the Authentik admin API token. Mode 0600, and kept out
// of config.json: config.json is read and printed in ordinary operation, a token
// is not.
// MCPGWDir holds vd-mcpgw's routes file, mounted into the gateway.
func MCPGWDir() string { return filepath.Join(VDHome(), "mcpgw") }

func AuthentikTokenPath() string { return filepath.Join(VDHome(), "authentik.token") }

// ProdROURLPath holds the --db prod-ro DSN (the shared vibe_ro role). Mode 0600.
func ProdROURLPath() string      { return filepath.Join(VDHome(), "prod-ro.url") }
func InfraEnvPath() string       { return filepath.Join(VDHome(), ".env") }
func PushDir(name string) string { return filepath.Join(VDHome(), "push", name) }
