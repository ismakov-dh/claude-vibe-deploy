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

	// MCP records that the app has a database MCP (its role and mcp.env exist);
	// its container runs only while MCPOAuthLive — there is no other way in. Deliberately
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
	// ProdRONetwork is set for --db prod-ro apps deployed against the replica
	// overlay. A prod-ro manifest without it predates that design (a per-app
	// user on the primary, no mandatory login) and is never restored.
	ProdRONetwork string `json:"prod_ro_network,omitempty"`
	// AuthBearer mirrors the provider's intercept_header_auth (--auth-bearer).
	AuthBearer bool `json:"auth_bearer,omitempty"`

	MCPOwner string `json:"mcp_owner,omitempty"`
	// MCPOAuthLive is set when the app's MCP resource exists in Authentik, so
	// the MCP container runs behind vd-mcpgw. Without it the MCP is not
	// rendered at all, and no gateway route is written: a route for a missing
	// issuer fails validation for every app. (Old manifests also carry
	// "mcp_oauth"; every MCP is behind sign-in now, so it is no longer read.)
	MCPOAuthLive bool `json:"mcp_oauth_live,omitempty"`
}

// ReadsProd reports whether the app reads the production replica: alone
// (DB "prod-ro") or beside its own database (DB "postgres" with the replica
// network). Every prod-ro rule keys on this, never on DB alone.
func (m *Manifest) ReadsProd() bool { return m.DB == "prod-ro" || m.ProdRONetwork != "" }

// DBSpec is the --db value that deploys this app again.
func (m *Manifest) DBSpec() string {
	if m.DB == "postgres" && m.ProdRONetwork != "" {
		return "postgres,prod-ro"
	}
	return m.DB
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
	return WriteManifest(m)
}

// WriteManifest saves m as is — for changes that are not a deploy.
func WriteManifest(m *Manifest) error {
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
