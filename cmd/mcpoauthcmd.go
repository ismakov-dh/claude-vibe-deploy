package cmd

import (
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/docker"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

var (
	mcpOAuthOwner string
	mcpOAuthCheck bool
)

func init() {
	mcpOAuthCmd.Flags().StringVar(&mcpOAuthOwner, "owner", "", "email of a person to add to the MCP's access group")
	mcpOAuthCmd.Flags().BoolVar(&mcpOAuthCheck, "check", false, "change nothing: verify vd can rebuild this app's compose file faithfully")
	rootCmd.AddCommand(mcpOAuthCmd)
}

// vd mcp-oauth turns on --mcp-oauth for a running app without redeploying it:
// a deploy rebuilds and force-recreates the app container, and all this needs
// is Authentik, the gateway route and new labels on the MCP container. Basic
// keeps working with the same password.
var mcpOAuthCmd = &cobra.Command{
	Use:   "mcp-oauth <app-name>",
	Short: "Put an app's database MCP behind platform login too, without touching the app",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		m, cfg, basic, ingress := mcpOAuthPreflight(name)
		if mcpOAuthCheck {
			differ, err := composeDrift(m, cfg, basic, ingress)
			if err != nil {
				output.Fail("mcp-oauth", output.NewError("COMPOSE_FAILED", err.Error(), ""))
			}
			output.Success("mcp-oauth", map[string]any{"name": name, "check": true,
				"faithful": differ == 0, "differing_lines": differ})
			return
		}
		if m.MCPOAuth && m.MCPOAuthLive {
			output.Info("%s already has MCP OAuth — reconciling Authentik and the route", name)
		}

		// Authentik first, the route second, the labels last: traffic reaches the
		// gateway only once the resource and the route exist.
		res := ensureMCPOAuth(name, cfg, mcpOAuthOwner)
		if res == nil {
			output.Fail("mcp-oauth", output.NewError("AUTH_FAILED",
				"Could not set up the MCP's resource in Authentik — nothing changed, the MCP stays on Basic",
				"See warnings; retry once the cause is fixed"))
		}

		composePath := state.AppComposePath(name)
		oldCompose, err := os.ReadFile(composePath)
		if err != nil {
			output.Fail("mcp-oauth", output.NewError("COMPOSE_FAILED", "Cannot read "+composePath+": "+err.Error(), ""))
		}
		oldManifest := *m

		m.MCPOAuth, m.MCPOAuthLive = true, true
		if mcpOAuthOwner != "" {
			m.MCPOwner = mcpOAuthOwner
		}
		if err := state.WriteManifest(m); err != nil {
			output.Fail("mcp-oauth", output.NewError("MANIFEST_WRITE_FAILED", err.Error(), "Nothing on the MCP changed"))
		}

		undo := func(why string) {
			os.WriteFile(composePath, oldCompose, 0644)
			state.WriteManifest(&oldManifest)
			docker.ComposeUpService(state.AppDir(name), "docker-compose.vd.yml", name+"-mcp")
			syncMCPGateway(cfg)
			output.Fail("mcp-oauth", output.NewError("MCP_OAUTH_FAILED",
				why+" — reverted, the MCP is on Basic as before",
				"The Authentik resource stays; retrying is safe"))
		}
		if err := syncMCPGateway(cfg); err != nil {
			undo("gateway route not written: " + err.Error())
		}
		data := mcpComposeData(m, cfg, basic, ingress)
		if err := docker.GenerateComposeFile(templatesFS, data, composePath); err != nil {
			undo("compose file: " + err.Error())
		}
		output.Info("Recreating the MCP container of %s (the app keeps running)...", name)
		if err := docker.ComposeUpService(state.AppDir(name), "docker-compose.vd.yml", name+"-mcp"); err != nil {
			undo(err.Error())
		}
		if err := docker.WaitHealthy("vd-"+name+"-mcp", 60*time.Second); err != nil {
			output.Warn("MCP container not healthy yet: %v", err)
		}

		output.Success("mcp-oauth", map[string]any{
			"name":  name,
			"oauth": mcpOAuthInfo(name, cfg, mcpOAuthOwner, res),
			"basic": "unchanged — the same password keeps working",
		})
	},
}

// mcpOAuthPreflight checks everything that can be checked before any change.
func mcpOAuthPreflight(name string) (*state.Manifest, *state.Config, string, string) {
	if !nameRegex.MatchString(name) {
		output.Fail("mcp-oauth", output.NewError("INVALID_NAME", "Invalid app name: "+name, ""))
	}
	m, err := state.LoadManifest(name)
	if err != nil {
		output.Fail("mcp-oauth", output.NewError("NOT_FOUND", "App not found: "+name, "Check app name with: vd list"))
	}
	if !m.MCP || m.DB != "postgres" {
		output.Fail("mcp-oauth", output.NewError("NO_MCP", name+" has no database MCP", "Only --db postgres apps have one"))
	}
	cfg, err := state.LoadConfig()
	if err != nil || cfg.Domain == "" {
		output.Fail("mcp-oauth", output.NewError("NOT_INITIALIZED", "Run vd init first", ""))
	}
	pw := envValue(state.AppMCPEnvPath(name), "VD_MCP_PASSWORD")
	if pw == "" {
		output.Fail("mcp-oauth", output.NewError("NO_MCP", "No Basic password in "+state.AppMCPEnvPath(name),
			"Redeploy the app once so its MCP has one"))
	}
	ingress := ""
	if m.Auth {
		if ingress = envValue(state.AppEnvPath(name), ingressEnvKey); ingress == "" {
			output.Fail("mcp-oauth", output.NewError("AUTH_FAILED",
				"App is behind --auth but its ingress secret is missing", "Redeploy the app"))
		}
	}
	return m, cfg, htpasswdSHA(mcpUser, pw), ingress
}

// mcpComposeData rebuilds what vd deploy rendered, from the manifest and the
// app's own files, with the MCP behind the gateway. Only the MCP service is
// recreated from it.
func mcpComposeData(m *state.Manifest, cfg *state.Config, basic, ingress string) docker.ComposeData {
	return docker.ComposeData{
		Name:          m.Name,
		AppType:       m.AppType,
		Port:          m.Port,
		Routing:       m.Routing,
		Domain:        cfg.Domain,
		HasEnvFile:    m.HasEnvFile,
		NeedsDB:       true,
		NeedsMCP:      true,
		MCPImage:      docker.MCPImage,
		MCPBasicAuth:  basic,
		MCPOAuth:      true,
		Auth:          m.Auth,
		IngressSecret: ingress,
	}
}

// composeDrift renders the app's compose file as vd deploy last did (OAuth
// off) and counts lines that differ from the file on disk, the generated-at
// header aside. Zero means the rebuild from the manifest is faithful. Counts
// only: the file holds the ingress secret and the Basic hash.
func composeDrift(m *state.Manifest, cfg *state.Config, basic, ingress string) (int, error) {
	data := mcpComposeData(m, cfg, basic, ingress)
	data.MCPOAuth = m.MCPOAuth && m.MCPOAuthLive
	tmp, err := os.CreateTemp("", "vd-compose-check-*.yml")
	if err != nil {
		return 0, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if err := docker.GenerateComposeFile(templatesFS, data, tmp.Name()); err != nil {
		return 0, err
	}
	a, err := os.ReadFile(state.AppComposePath(m.Name))
	if err != nil {
		return 0, err
	}
	b, err := os.ReadFile(tmp.Name())
	if err != nil {
		return 0, err
	}
	return lineDiff(string(a), string(b)), nil
}

// lineDiff counts lines present in one text and not the other, ignoring the
// "# App: … Generated: …" header.
func lineDiff(a, b string) int {
	count := func(s string) map[string]int {
		c := map[string]int{}
		for _, l := range strings.Split(s, "\n") {
			if !strings.HasPrefix(l, "# App: ") {
				c[l]++
			}
		}
		return c
	}
	ca, cb := count(a), count(b)
	n := 0
	for l, k := range ca {
		if d := k - cb[l]; d > 0 {
			n += d
		}
	}
	for l, k := range cb {
		if d := k - ca[l]; d > 0 {
			n += d
		}
	}
	return n
}
