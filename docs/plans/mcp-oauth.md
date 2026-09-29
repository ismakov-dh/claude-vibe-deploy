# Design: `vd deploy --mcp-oauth` — database MCP behind Authentik

**Status:** implemented on branch `feat/mcp-oauth`, 2026-09-29. Live check pending: vd's
Authentik token needs the grants below, and prod needs stacks' `mcp-groups` scope mapping.
**Goal:** an app's read-only database MCP (`<app>.mcp.<apps-domain>`) accepts a browser sign-in
through Authentik instead of shared Basic credentials — without breaking anyone on Basic.

## Shape

- **Gateway:** `vd-mcpgw`, vd's own agentgateway (same pinned image as stacks), on `vd-net`, no
  published port. Its routes file `$VD_HOME/mcpgw/config.yaml` is derived from the manifests on
  every deploy/destroy/rollback/init, validated by the image (`--validate-only`, which fetches
  each issuer's JWKS) and swapped in by rename; the gateway watches it. A Swarm config — what the
  stacks gateway uses — cannot change in place, hence a separate instance.
- **Authentik, per app** (`internal/authentik/mcp.go`): group, public OAuth2 client with PKCE,
  application and group binding, all named `mcp-vibe-<app>`; 5-minute tokens; `sub_mode=user_uuid`;
  loopback and claude.ai callbacks only; stacks' `mcp-groups` scope mapping referenced, not
  created. Same fail-closed rules as `--auth`: no filter trusted, every write re-read, an
  unbindable application deleted. Revocation delay is the token lifetime (5 min).
- **Owner:** `--mcp-owner <email>` adds one existing account to the group. Lookup by exact email,
  only the pk is kept; ambiguous matches are refused. Without it the group starts empty.
- **Routing on the MCP host:**

  | Router | Rule | Goes to |
  |---|---|---|
  | `…-mcp-basic` | `Host && HeaderRegexp(Authorization, ^Basic )` | basicauth → MCP container, as before |
  | `…-mcp-wellknown` | `Host && PathPrefix(/.well-known/)` | gateway (OAuth metadata) |
  | `…-mcp` | `Host` | gateway (`/mcp`, streamable HTTP) |

  The Basic rule is the longest, so a request with Basic credentials matches it on **every** path,
  `/.well-known/` included. A Basic client therefore sees exactly the answers it saw before and is
  never shown OAuth metadata that could make it drop its credentials.
- **Failure:** if Authentik cannot be set up, the deploy succeeds with the MCP on Basic only and a
  warning; no route is written for a resource that does not exist.

## Token grants needed on `vd-platform` (requested from stacks)

`oauth2provider` view/add/change/delete; `scopemapping` view; `certificatekeypair` view (not key
download); `user` view (global directory read — the orchestrator's decision, with the leak
exposure stated); `add_user_to_group`/`remove_user_from_group` only on groups vd creates.

## Not yet

Switching Basic off per app — only once its Basic traffic is zero.
