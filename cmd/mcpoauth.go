package cmd

import (
	"fmt"

	"github.com/vibe-deploy/vd/internal/authentik"
	"github.com/vibe-deploy/vd/internal/mcpgw"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

// mcpHost is where an app's database MCP is published.
func mcpHost(app string, cfg *state.Config) string { return app + ".mcp." + cfg.Domain }

// syncMCPGateway rewrites vd-mcpgw's routes from the manifests on disk: one
// route per app with MCPOAuthLive and an MCP. Called after every change that can
// add or remove one — deploy, destroy, rollback, init — so the file is always
// derived, never edited.
func syncMCPGateway(cfg *state.Config) error {
	// Held across read and write: two runs must not each read the manifests and
	// then write back a file missing the other's change.
	unlock, err := state.LockAuthentik()
	if err != nil {
		return err
	}
	defer unlock()
	apps, err := state.ListApps()
	if err != nil {
		return err
	}
	var routes []mcpgw.Route
	for _, app := range apps {
		m, err := state.LoadManifest(app)
		if err != nil || !m.MCPOAuthLive || !m.MCP {
			continue
		}
		if cfg.AuthentikURL == "" {
			return fmt.Errorf("app %s has MCP OAuth but Authentik is not configured", app)
		}
		name, err := authentik.MCPName(app)
		if err != nil {
			return err
		}
		host := mcpHost(app, cfg)
		routes = append(routes, mcpgw.Route{
			Name:     name,
			Host:     host,
			Resource: "https://" + host + "/mcp",
			Issuer:   authentik.New(cfg.AuthentikURL, "").Issuer(name),
			Backend:  "http://vd-" + app + "-mcp:8089/sse",
		})
	}
	dropped, err := mcpgw.Write(state.MCPGWDir(), routes)
	if err != nil {
		return err
	}
	for _, name := range dropped {
		output.Warn("vd-mcpgw rejected the route for %s (issuer unreachable?) — left out, Basic still works; any vd deploy or vd init retries", name)
	}
	state.ChownLikeHome(state.MCPGWDir())
	state.ChownLikeHome(state.MCPGWDir() + "/config.yaml")
	return nil
}

// mcpOAuthInfo is the block agents read to register the MCP over OAuth.
func mcpOAuthInfo(app string, cfg *state.Config, owner string, res *authentik.MCPResult) map[string]any {
	url := "https://" + mcpHost(app, cfg) + "/mcp"
	info := map[string]any{
		"url":   url,
		"group": res.Name,
		"add":   fmt.Sprintf("claude mcp add --transport http %s-db %s", app, url),
		"note":  "Sign in through the browser when the client asks; access is membership in " + res.Name + ".",
	}
	switch {
	case owner == "":
		info["owner"] = nil
		info["grant"] = "No --mcp-owner this time: nobody was added. Access is membership in " + res.Name + ", managed by platform admins."
	case res.OwnerAdded:
		info["owner"] = owner // the email the caller gave, never directory data
	case res.OwnerMissing:
		info["owner"] = owner
		info["grant"] = owner + " has no platform account yet; a platform admin adds people to " + res.Name + "."
	case res.OwnerError != "":
		info["owner"] = owner
		info["grant"] = owner + " was not added (see warnings); a platform admin adds people to " + res.Name + "."
	}
	return info
}

// ensureMCPOAuth provisions the app's MCP resource in Authentik. Failure leaves
// the MCP on Basic only — still protected — and says so; it never publishes a
// gateway route for a resource that does not exist.
func ensureMCPOAuth(app string, cfg *state.Config, owner string) *authentik.MCPResult {
	if err := cfg.AuthentikReady(); err != nil {
		output.Warn("MCP OAuth not set up (%v) — the MCP stays on Basic credentials only", err)
		return nil
	}
	token, _ := state.LoadAuthentikToken()
	unlock, err := state.LockAuthentik()
	if err != nil {
		output.Warn("MCP OAuth not set up (lock: %v) — the MCP stays on Basic credentials only", err)
		return nil
	}
	res, err := authentik.New(cfg.AuthentikURL, token).EnsureMCP(authentik.MCPSpec{
		App: app, Resource: "https://" + mcpHost(app, cfg) + "/mcp", Owner: owner,
	})
	unlock()
	if err == nil && res.OwnerError != "" {
		output.Warn("--mcp-owner not added: %s — the MCP OAuth is set up, the group just has no new member", res.OwnerError)
	}
	if err != nil {
		output.Warn("MCP OAuth not set up in Authentik (%v) — the MCP stays on Basic credentials only; redeploy to retry", err)
		return nil
	}
	return res
}
