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
  unbindable application deleted. Order closes the open window: application created without a
  provider → group binding → provider attached; destroy deletes the application first (bindings
  cascade) and touches provider and group only after that worked. The group is deleted too, so
  it cannot hand its members to the next app of that name. vd marks the groups it creates with
  the attribute `vd_managed: true`; a same-named group without it is neither adopted by deploy
  nor deleted by destroy — both refuse before changing anything. Revocation delay is the token lifetime (5 min).
- **Owner:** `--mcp-owner <email>` adds one existing account to the group. Lookup by whole email (case-insensitive),
  only the pk is kept; ambiguous matches are refused. Without it the group starts empty. Safe
  because users cannot change their own email or username (`default_user_change_email=false`,
  `change_username=false` on prod, checked by the orchestrator). A failed lookup is a warning.
- **Routing on the MCP host:**

  | Router | Rule | Goes to |
  |---|---|---|
  | `…-mcp-basic` | `Host && HeaderRegexp(Authorization, (?i)^Basic )` | basicauth → MCP container, as before |
  | `…-mcp-wellknown` | `Host && PathPrefix(/.well-known/)` | gateway (OAuth metadata) |
  | `…-mcp` | `Host` | gateway (`/mcp`, streamable HTTP) |

  The Basic rule is the longest, so a request with Basic credentials matches it on **every** path,
  `/.well-known/` included. A Basic client therefore sees exactly the answers it saw before and is
  never shown OAuth metadata that could make it drop its credentials.
- **Failure:** if Authentik cannot be set up, the deploy succeeds with the MCP on Basic only and a
  warning; no route is written for a resource that does not exist (routes follow `mcp_oauth_live`).
- **Routes file:** rebuilt from manifests under the Authentik lock (read and write), validated by
  the image on `vd-net`. A route the gateway rejects alone (unreachable JWKS) is dropped with a
  warning, so one app cannot freeze the others and a destroy always removes its route. If every
  route is rejected the cause is not the routes; the old file stays.

## Token grants needed on `vd-platform` (requested from stacks)

`oauth2provider` view/add/change/delete; `scopemapping` view; `certificatekeypair` view (not key
download); `user` view (global directory read — the orchestrator's decision, with the leak
exposure stated); `add_user_to_group`/`remove_user_from_group`/`delete_group` only on groups vd creates (InitialPermissions, on the role `vd-platform`); `view_role` and `unassign_role_permissions` — never `assign_role_permissions` — on that role, so destroy can remove the rows on a group it deleted.

## Not yet

Switching Basic off per app — only once its Basic traffic is zero.
