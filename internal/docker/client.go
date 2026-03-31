package docker

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/vibe-deploy/vd/internal/shell"
)

const defaultTimeout = 10 * time.Minute

// NetworkExists checks if a Docker network exists.
func NetworkExists(name string) bool {
	_, err := shell.Run(30*time.Second, "docker", "network", "inspect", name)
	return err == nil
}

// NetworkCreate creates a Docker network.
func NetworkCreate(name string) error {
	_, err := shell.Run(30*time.Second, "docker", "network", "create", name)
	return err
}

// NetworkConnect connects a container to a network.
func NetworkConnect(network, container string) error {
	// Check if already connected
	r, err := shell.Run(30*time.Second, "docker", "inspect", "--format", "{{json .NetworkSettings.Networks}}", container)
	if err == nil && strings.Contains(r.Stdout, network) {
		return nil // already connected
	}
	_, err = shell.Run(30*time.Second, "docker", "network", "connect", network, container)
	return err
}

// ComposeBuild runs docker compose build in the given directory.
func ComposeBuild(dir, composefile string) error {
	_, err := shell.Run(defaultTimeout, "docker", "compose", "-f", composefile, "build", "--no-cache")
	if err != nil {
		// Try from the directory
		r, err2 := shell.Run(defaultTimeout, "docker", "compose", "-f", dir+"/"+composefile, "build", "--no-cache")
		if err2 != nil {
			return fmt.Errorf("docker compose build failed: %s", r.Stderr)
		}
	}
	return nil
}

// ComposeUp runs docker compose up -d in the given directory.
func ComposeUp(dir, composefile string) error {
	r, err := shell.Run(defaultTimeout, "docker", "compose", "-f", dir+"/"+composefile, "up", "-d", "--build", "--force-recreate")
	if err != nil {
		return fmt.Errorf("docker compose up failed: %s", r.Stderr)
	}
	return nil
}

// ComposeDown runs docker compose down.
func ComposeDown(dir, composefile string) error {
	r, err := shell.Run(2*time.Minute, "docker", "compose", "-f", dir+"/"+composefile, "down", "--remove-orphans")
	if err != nil {
		return fmt.Errorf("docker compose down failed: %s", r.Stderr)
	}
	return nil
}

// ContainerState returns the state of a container.
type ContainerState struct {
	Running bool   `json:"running"`
	Status  string `json:"status"`
	Health  string `json:"health"`
	Started string `json:"started_at"`
}

func InspectContainer(name string) (*ContainerState, error) {
	r, err := shell.Run(30*time.Second, "docker", "inspect", "--format",
		`{"running":{{.State.Running}},"status":"{{.State.Status}}","health":"{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}","started_at":"{{.State.StartedAt}}"}`,
		name)
	if err != nil {
		return nil, fmt.Errorf("container %s not found", name)
	}
	var s ContainerState
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ContainerLogs returns the last N lines of container logs.
func ContainerLogs(name string, lines int) (string, error) {
	r, err := shell.Run(30*time.Second, "docker", "logs", "--tail", fmt.Sprintf("%d", lines), name)
	if err != nil {
		return "", err
	}
	// Docker logs writes to both stdout and stderr
	return r.Stdout + r.Stderr, nil
}

// ContainerLogsFollow starts streaming logs (returns the exec.Cmd for the caller to manage).
func ContainerLogsFollow(name string, lines int) *exec.Cmd {
	return exec.Command("docker", "logs", "--tail", fmt.Sprintf("%d", lines), "-f", name)
}

// WaitHealthy polls container health for up to timeout.
func WaitHealthy(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s, err := InspectContainer(name)
		if err == nil && s.Running {
			if s.Health == "healthy" || s.Health == "none" {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("container %s did not become healthy within %s", name, timeout)
}

// SaveImage saves a Docker image to a tar.gz file.
func SaveImage(image, destPath string) error {
	_, err := shell.Run(5*time.Minute, "sh", "-c",
		fmt.Sprintf("docker save %s | gzip > %s", image, destPath))
	return err
}

// LoadImage loads a Docker image from a tar.gz file.
func LoadImage(srcPath string) error {
	_, err := shell.Run(5*time.Minute, "sh", "-c",
		fmt.Sprintf("gunzip -c %s | docker load", srcPath))
	return err
}

// GetImageID returns the image ID for a container.
func GetImageID(containerName string) (string, error) {
	r, err := shell.Run(30*time.Second, "docker", "inspect", "--format", "{{.Image}}", containerName)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(r.Stdout), nil
}
