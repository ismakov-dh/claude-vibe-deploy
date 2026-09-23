package docker

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"text/template"
	"time"
)

// MCPImage is the per-app read-only database MCP server.
//
// Pinned to the dev-ops fork rather than upstream crystaldba/postgres-mcp:0.3.0.
// That image resolves mcp to 1.6.0, which raises RuntimeError on any message
// arriving before notifications/initialized completes — and the Node/undici MCP
// SDK, which is what Claude Code uses, triggers exactly that on reconnect. The
// exception tears down the SSE stream for the whole session. 0.3.0 is still the
// latest postgres-mcp release, so there is no upstream version to move to; see
// images/postgres-mcp.Dockerfile in the dev-ops repo for the patch.
const MCPImage = "registry.xaid.ai/radiology/devops/stacks/postgres-mcp:0.3.0-init-tolerant"

// ComposeData holds the data for rendering the app compose template.
type ComposeData struct {
	Name       string
	AppType    string
	Port       int
	Routing    string
	Domain     string
	HasEnvFile bool
	NeedsDB    bool
	Timestamp  string

	// ponytail: one MCP container per app with a database — ~80MB RSS each,
	// sharing one image layer. Noise below ~20 such apps. If it stops being
	// noise, put it behind a `vd deploy --mcp` flag: the template already renders
	// conditionally, so that is a flag plus a manifest field.
	NeedsMCP     bool
	MCPImage     string
	MCPBasicAuth string // htpasswd entry, user:{SHA}base64(sha1(pw))

	// Forward auth via the Authentik embedded outpost. IngressSecret is hex, so
	// it carries no '$' for compose to interpolate.
	Auth          bool
	IngressSecret string
}

// GenerateComposeFile renders the app compose template and writes it to disk.
func GenerateComposeFile(tmplFS fs.FS, data ComposeData, destPath string) error {
	// Refused here as well as in deploy: the outpost router and the provider's
	// external_host are host-based, and path routing would render an app whose
	// middleware label collides with the strip-prefix one. Failing at the lowest
	// layer keeps a future caller from publishing an app it believes is protected.
	if data.Auth && (data.Routing != "subdomain" || data.IngressSecret == "") {
		return fmt.Errorf("forward auth needs subdomain routing and an ingress secret")
	}
	tmplContent, err := fs.ReadFile(tmplFS, "templates/compose/app.yml.tmpl")
	if err != nil {
		return err
	}

	t, err := template.New("compose").Parse(string(tmplContent))
	if err != nil {
		return err
	}

	data.Timestamp = time.Now().UTC().Format(time.RFC3339)

	// 0600: for --auth apps this file holds the ingress secret. Unlinked first so
	// a vd-user deploy can replace one a root deploy left behind (removal needs
	// write permission on the directory, not ownership of the file), and chowned
	// to the app directory's owner for the same two-user reason as mcp.env.
	os.Remove(destPath)
	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if fi, err := os.Stat(filepath.Dir(destPath)); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			os.Chown(destPath, int(st.Uid), int(st.Gid))
		}
	}

	return t.Execute(f, data)
}
