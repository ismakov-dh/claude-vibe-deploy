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
