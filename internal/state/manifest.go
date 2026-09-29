package state

import (
	"encoding/json"
	"os"
	"time"
)

// Manifest is the per-app deployment metadata.
type Manifest struct {
	Name          string `json:"name"`
	AppType       string `json:"app_type"`
	Port          int    `json:"port"`
	Routing       string `json:"routing"`
	DB            string `json:"db,omitempty"`
	DBAccess      string `json:"db_access,omitempty"`
	DBName        string `json:"db_name,omitempty"`
	DBUser        string `json:"db_user,omitempty"`
	Domain        string `json:"domain"`
	ContainerName string `json:"container_name"`
	DeployedAt    string `json:"deployed_at"`
	DeployCount   int    `json:"deploy_count"`
	HasEnvFile    bool   `json:"env_file"`

	// MCP records whether this deploy actually rendered an MCP service. Deliberately
	// not derived from DB == "postgres": apps deployed before the feature existed
	// have a database and no MCP container until their next deploy, and deriving it
	// would make vd status advertise a URL that 404s.
	MCP bool `json:"mcp,omitempty"`

	// Forward auth. Sticky: once an app is deployed with --auth, later deploys
	// keep it on even without the flag, so an agent that forgets the flag cannot
	// quietly publish a protected app. Removing protection means vd destroy.
	Auth      bool   `json:"auth,omitempty"`
	AuthTTL   string `json:"auth_ttl,omitempty"`
	AuthGroup string `json:"auth_group,omitempty"`
	// AuthBearer mirrors the provider's intercept_header_auth (--auth-bearer).
	AuthBearer bool `json:"auth_bearer,omitempty"`

	// MCP behind vd-mcpgw and Authentik. Sticky like Auth. Basic keeps working
	// alongside it until it is switched off per app.
	MCPOAuth bool   `json:"mcp_oauth,omitempty"`
	MCPOwner string `json:"mcp_owner,omitempty"`
	// MCPOAuthLive is set when this deploy's labels send non-Basic traffic to
	// vd-mcpgw, i.e. the Authentik resource exists. Routes follow it, not the
	// intent: a route for a missing issuer fails validation for every app.
	MCPOAuthLive bool `json:"mcp_oauth_live,omitempty"`
}

func LoadManifest(appName string) (*Manifest, error) {
	data, err := os.ReadFile(AppManifestPath(appName))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func SaveManifest(m *Manifest) error {
	m.DeployedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(AppManifestPath(m.Name), data, 0644)
}

// ListApps returns names of all deployed apps by scanning the apps directory.
func ListApps() ([]string, error) {
	entries, err := os.ReadDir(AppsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var apps []string
	for _, e := range entries {
		if e.IsDir() {
			// Only include dirs that have a manifest
			if _, err := os.Stat(AppManifestPath(e.Name())); err == nil {
				apps = append(apps, e.Name())
			}
		}
	}
	return apps, nil
}
