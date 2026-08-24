package docker

import (
	"io/fs"
	"os"
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
}

// GenerateComposeFile renders the app compose template and writes it to disk.
func GenerateComposeFile(tmplFS fs.FS, data ComposeData, destPath string) error {
	tmplContent, err := fs.ReadFile(tmplFS, "templates/compose/app.yml.tmpl")
	if err != nil {
		return err
	}

	t, err := template.New("compose").Parse(string(tmplContent))
	if err != nil {
		return err
	}

	data.Timestamp = time.Now().UTC().Format(time.RFC3339)

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, data)
}
