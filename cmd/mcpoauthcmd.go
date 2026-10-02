package cmd

import (
	"fmt"
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

	// Replaced in tests: the only docker call on the revert path.
	composeUpService = docker.ComposeUpService
)

func init() {
	mcpOAuthCmd.Flags().StringVar(&mcpOAuthOwner, "owner", "", "email of a person to add to the MCP's access group")
	mcpOAuthCmd.Flags().BoolVar(&mcpOAuthCheck, "check", false, "change nothing: report whether vd can re-render this app's compose file faithfully")
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
		if !nameRegex.MatchString(name) {
			output.Fail("mcp-oauth", output.NewError("INVALID_NAME", "Invalid app name: "+name, ""))
		}
		// From reading the manifest to the last compose up: a deploy in between
		// could add labels (forward auth) this run would then overwrite.
		unlock, err := state.LockApp(name)
		if err != nil {
			output.Fail("mcp-oauth", output.NewError("MCP_OAUTH_FAILED", "Could not take the app lock: "+err.Error(), ""))
		}
		defer unlock()

		m, cfg, basic, ingress := mcpOAuthPreflight(name)
		drift, err := composeDrift(m, cfg, basic, ingress)
		if err != nil {
			output.Fail("mcp-oauth", output.NewError("COMPOSE_FAILED", err.Error(), ""))
		}
		if mcpOAuthCheck {
			output.Success("mcp-oauth", map[string]any{"name": name, "check": true,
				"faithful": drift.other == 0, "mcp_lines_differ": drift.mcp, "other_lines_differ": drift.other})
			return
		}
		// Only MCP lines may change: they are all this run recreates. Anything
		// else means the file on disk is not what vd would render from the
		// manifest, and rewriting it would change the app behind its back.
		if drift.other > 0 {
			output.Fail("mcp-oauth", output.NewError("COMPOSE_DRIFT",
				fmt.Sprintf("%d non-MCP line(s) of %s's compose file differ from what vd renders from its manifest — nothing changed", drift.other, name),
				"Redeploy the app once (vd deploy), then retry"))
		}

		// Authentik first, the route second, the labels last: traffic reaches the
		// gateway only once the resource and the route exist.
		res := ensureMCPOAuth(name, cfg, mcpOAuthOwner)
		if res == nil {
			output.Fail("mcp-oauth", output.NewError("AUTH_FAILED",
				"Could not set up the MCP's sign-in in Authentik — nothing changed",
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
		fail := func(why string) {
			for _, e := range revertMCPOAuth(name, oldCompose, &oldManifest, cfg) {
				output.Warn("revert: %v", e)
			}
			output.Fail("mcp-oauth", output.NewError("MCP_OAUTH_FAILED",
				why+" — reverted to how it was (see warnings if a revert step failed); the MCP is not available to agents until this succeeds",
				"The Authentik resource stays; retrying is safe"))
		}
		if err := syncMCPGateway(cfg); err != nil {
			fail("gateway route not written: " + err.Error())
		}
		if err := docker.GenerateComposeFile(templatesFS, composeDataFor(m, cfg, basic, ingress), composePath); err != nil {
			fail("compose file: " + err.Error())
		}
		output.Info("Recreating the MCP container of %s (the app keeps running)...", name)
		if err := composeUpService(state.AppDir(name), "docker-compose.vd.yml", name+"-mcp"); err != nil {
			fail(err.Error())
		}
		if err := docker.WaitHealthy("vd-"+name+"-mcp", 60*time.Second); err != nil {
			output.Warn("MCP container not healthy yet: %v", err)
		}

		output.Success("mcp-oauth", map[string]any{
			"name":  name,
			"oauth": mcpOAuthInfo(name, cfg, mcpOAuthOwner, res),
		})
	},
}

// revertMCPOAuth puts the compose file (0600: it holds the ingress secret and
// the Basic hash) and the manifest back, recreates the MCP as it was and
// rewrites the routes. Returns every step that failed.
func revertMCPOAuth(name string, oldCompose []byte, oldManifest *state.Manifest, cfg *state.Config) []error {
	var errs []error
	path := state.AppComposePath(name)
	os.Remove(path) // a root-run vd may have left it; the directory is ours
	if err := os.WriteFile(path, oldCompose, 0600); err != nil {
		errs = append(errs, fmt.Errorf("restore compose file: %w", err))
	}
	state.ChownLikeHome(path)
	if err := state.WriteManifest(oldManifest); err != nil {
		errs = append(errs, fmt.Errorf("restore manifest: %w", err))
	}
	if err := composeUpService(state.AppDir(name), "docker-compose.vd.yml", name+"-mcp"); err != nil {
		errs = append(errs, fmt.Errorf("recreate MCP container: %w", err))
	}
	if err := syncMCPGateway(cfg); err != nil {
		errs = append(errs, fmt.Errorf("rewrite gateway routes: %w", err))
	}
	return errs
}

// mcpOAuthPreflight checks everything that can be checked before any change.
func mcpOAuthPreflight(name string) (*state.Manifest, *state.Config, string, string) {
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
		output.Fail("mcp-oauth", output.NewError("NO_MCP", "The MCP's server-side credentials file is incomplete: "+state.AppMCPEnvPath(name),
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

type composeDiff struct{ mcp, other int }

// composeDrift renders the compose file from the manifest as it stands and
// compares it with the file on disk: lines of the <app>-mcp service are
// counted apart from the rest. Counts only — the file holds the ingress secret and the Basic hash.
func composeDrift(m *state.Manifest, cfg *state.Config, basic, ingress string) (composeDiff, error) {
	tmp, err := os.CreateTemp(state.VDHome(), ".compose-check-*.yml") // not /tmp: secrets
	if err != nil {
		return composeDiff{}, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if err := docker.GenerateComposeFile(templatesFS, composeDataFor(m, cfg, basic, ingress), tmp.Name()); err != nil {
		return composeDiff{}, err
	}
	a, err := os.ReadFile(state.AppComposePath(m.Name))
	if err != nil {
		return composeDiff{}, err
	}
	b, err := os.ReadFile(tmp.Name())
	if err != nil {
		return composeDiff{}, err
	}
	return lineDiff(string(a), string(b), m.Name), nil
}

// lineDiff counts lines present in one text and not the other, split by where
// they sit: inside the "<app>-mcp:" service block (what vd mcp-oauth
// recreates) or anywhere else. Comments and blank lines are ignored.
func lineDiff(a, b, app string) composeDiff {
	type key struct {
		mcp  bool
		line string
	}
	count := func(s string) map[key]int {
		c := map[key]int{}
		inMCP := false
		for _, l := range strings.Split(s, "\n") {
			t := strings.TrimSpace(l)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			// A service header is indented two spaces; a top-level key ends the block.
			if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") && strings.HasSuffix(t, ":") {
				inMCP = t == app+"-mcp:"
			} else if !strings.HasPrefix(l, " ") {
				inMCP = false
			}
			c[key{inMCP, l}]++
		}
		return c
	}
	ca, cb := count(a), count(b)
	var d composeDiff
	tally := func(x, y map[key]int) {
		for k, n := range x {
			if n -= y[k]; n > 0 {
				if k.mcp {
					d.mcp += n
				} else {
					d.other += n
				}
			}
		}
	}
	tally(ca, cb)
	tally(cb, ca)
	return d
}
