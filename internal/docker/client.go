package docker

import (
	"encoding/json"
	"fmt"
	"net"
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

// NetworkGateway returns a network's gateway address.
//
// Traefik needs it: nginx proxies to vd-traefik's published port on 127.0.0.1,
// but a loopback-published port arrives through docker-proxy, so the container
// sees the bridge gateway (172.24.0.1 here) as the client — not 127.0.0.1.
// Trusting only loopback would leave nginx untrusted, Traefik would overwrite
// X-Forwarded-Proto, and the Authentik outpost would build http:// callbacks.
// The subnet is Docker's choice and differs per host, so it is read, not assumed.
//
// Returned as host CIDRs, one per address family: a dual-stack network has an
// IPv4 and an IPv6 gateway, and the earlier version concatenated them into one
// unparseable string.
func NetworkGateway(name string) ([]string, error) {
	r, err := shell.Run(30*time.Second, "docker", "network", "inspect", "--format",
		"{{range .IPAM.Config}}{{.Gateway}} {{end}}", name)
	if err != nil {
		return nil, err
	}
	cidrs := GatewayCIDRs(r.Stdout)
	if len(cidrs) == 0 {
		return nil, fmt.Errorf("network %s reports no gateway", name)
	}
	return cidrs, nil
}

// GatewayCIDRs turns `docker network inspect` gateway output into /32 and /128
// entries, skipping anything that is not an IP address.
func GatewayCIDRs(out string) []string {
	var cidrs []string
	for _, f := range strings.Fields(out) {
		ip := net.ParseIP(f)
		switch {
		case ip == nil:
			continue
		case ip.To4() != nil:
			cidrs = append(cidrs, ip.String()+"/32")
		default:
			cidrs = append(cidrs, ip.String()+"/128")
		}
	}
	return cidrs
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

// ComposeApply brings a compose file's services to the declared state, recreating
// only the ones whose configuration changed. For shared infrastructure, where
// ComposeUp's --force-recreate is wrong: vd init used it, so every init — even
// one that changed nothing but Traefik — recreated vd-postgres and dropped every
// app's database connections at once. Seen 2026-09-23: three apps logged
// "terminating connection due to administrator command", one answered a 500.
func ComposeApply(dir, composefile string) error {
	r, err := shell.Run(defaultTimeout, "docker", "compose", "-f", dir+"/"+composefile, "up", "-d")
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
// PullImage fetches an image ahead of time. Used by vd init so an unreachable
// private registry surfaces at initialisation rather than in the middle of
// somebody's app deploy — a compose up that cannot pull takes the app with it.
func PullImage(image string) error {
	_, err := shell.Run(5*time.Minute, "docker", "pull", image)
	return err
}

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
