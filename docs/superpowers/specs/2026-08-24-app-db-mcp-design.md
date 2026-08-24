# Read-only MCP for app databases

Date: 2026-08-24
Status: approved, ready for implementation plan

## Problem

Agents building apps on vibe-deploy are blind to their own app's data. `vd exec`
is disabled over SSH (`scripts/vd-ssh-wrapper:17`), there is no psql path, and
the platform doctrine explicitly forbids pointing a Postgres MCP at a database
(`plugin/skills/vibe/SKILL.md:72`, `docs/SECURITY-FOR-VIBECODERS.md:56`). The
result is that an agent debugging a schema or a bad insert has to guess, or the
vibecoder works around the rule with an unsanctioned connection.

Give them a sanctioned path: for every app that has its own vd-managed
PostgreSQL database, vd runs a read-only MCP server scoped to that one database.
Production stays closed — that rule does not change.

## Non-goals

- **No MCP for `--db prod-ro`.** Production remains reachable only by deploying a
  dashboard app. The reporting platform already has its own MCP at
  `mcp-db.platform.acuradai.com`, owned by the dev-ops stack repo, not by vd.
- **No read-only replica of `vd-postgres`.** The reporting-platform pattern
  (primary → streaming replica → MCP) exists so analytics queries never touch a
  hot production primary. Here the use case is debugging the app you just
  deployed, and replication lag actively harms it: the agent inserts a row
  through the app, does not see it in the MCP, and debugs a bug that does not
  exist. Read-only is enforced by role grants plus the MCP server's own
  restricted mode instead.
- **No `vd mcp-enable` / `vd mcp-disable` commands.** MCP presence is derived
  from `--db postgres`.

## Architecture

### The container

For every app deployed with `--db postgres`, `docker-compose.vd.yml` gains a
second service, `vd-<app>-mcp`:

| Aspect | Value |
|--------|-------|
| Image | `registry.xaid.ai/radiology/devops/stacks/postgres-mcp:0.3.0-init-tolerant` |
| Command | `--transport=sse --sse-port=8089 --sse-host=0.0.0.0 --access-mode=restricted` |
| Networks | `vd-net` (Traefik reaches it), `vd-db` (it reaches vd-postgres) |
| Env | `env_file: ./mcp.env` (mode 0600) |
| Host | `<app>.mcp.<apps-domain>` |
| Auth | Traefik basicauth, per-app generated password |

The image is the patched fork from the dev-ops repo, not upstream. postgres-mcp
0.3.0 is the latest release on PyPI and the published `crystaldba/postgres-mcp:0.3.0`
image resolves `mcp` to 1.6.0, which raises `RuntimeError` on any message that
arrives before `notifications/initialized` completes. The Node/undici MCP SDK —
which is what Claude Code uses — triggers this on reconnect, and the exception
tears down the SSE stream for the whole session. `images/postgres-mcp.Dockerfile`
in the dev-ops repo neutralises the two guards; see that file for the full
reasoning. The same host (nashville) already pulls this image for the production
stack, so no new registry access is required.

### The `mcp.` namespace, and why not `mcp.<app>.<domain>`

MCP endpoints live one label below a dedicated `mcp.` label: `<app>.mcp.<domain>`,
not `mcp.<app>.<domain>`. The dotted-prefix form cannot be served: a TLS wildcard
matches exactly one label (RFC 6125), so `*.apps.platform.acuradai.com` does not
cover `mcp.myapp.apps.platform.acuradai.com`, and multi-level wildcards
(`*.*.`) are neither issued by Let's Encrypt nor valid in TLS. Serving that shape
would mean issuing a certificate per app at deploy time — a Route53 challenge on
every deploy and N certificates in rotation. Flipping the order keeps every host
one label under a wildcard and costs one SAN.

Three layers, and only one of them needs changing:

- **TLS — changes.** Add `*.mcp.$DOMAIN` as a third SAN on the existing
  certificate. `scripts/deploy.sh:195` currently requests `-d "*.$DOMAIN" -d
  "$DOMAIN"` guarded by `if ! sudo test -d /etc/letsencrypt/live/$DOMAIN`. Drop
  the guard and pass `--cert-name "$DOMAIN" --expand` with all three `-d` args:
  certbot is then idempotent when the SAN set is unchanged ("not yet due for
  renewal", exit 0) and self-upgrading on hosts whose certificate predates this
  change. Keeping it as one certificate keeps it as one renewal.
- **nginx — no change.** An nginx `server_name` wildcard, unlike TLS and DNS,
  matches more than one label: `*.apps.platform.acuradai.com` already matches
  `myapp.mcp.apps.platform.acuradai.com`.
- **DNS — no change expected, but verify.** Under RFC 4592 the existing
  `*.apps.platform.acuradai.com` record synthesizes an answer for
  `myapp.mcp.apps.platform.acuradai.com` too, *provided the zone contains no
  `mcp.apps.platform.acuradai.com` node*. That is a real dependency on an
  absence: if someone later adds that record, every MCP endpoint stops resolving
  at once. Verify with `dig` during implementation, and note in the runbook that
  an explicit `*.mcp.apps.platform.acuradai.com` record in Route53 removes the
  fragility. Creating it is a zone change outside vd's remit, so it is a
  recommendation rather than a step.

A pleasant consequence: no hostname-collision guard is needed. Under
`mcp-<app>.<domain>` an app named `mcp-foo` would have claimed app `foo`'s MCP
host; under `<app>.mcp.<domain>` app names and MCP hosts cannot overlap. It also
makes the whole MCP surface addressable as a group — an IP allowlist over every
MCP endpoint is later one nginx `server` block rather than a per-app edit.

### Traefik routing

Two routers, deliberately:

```
traefik.http.routers.vd-<app>-mcp.rule=Host(`<app>.mcp.<domain>`)
traefik.http.routers.vd-<app>-mcp.middlewares=vd-<app>-mcp-auth
traefik.http.middlewares.vd-<app>-mcp-auth.basicauth.users=mcp:<hash>

traefik.http.routers.vd-<app>-mcp-wellknown.rule=Host(`<app>.mcp.<domain>`) && PathPrefix(`/.well-known/`)
traefik.http.routers.vd-<app>-mcp-wellknown.priority=100
```

The second router has **no** auth middleware, by design. MCP clients probe
`/.well-known/*` for OAuth metadata before using credentials they already hold.
A 401 there reads as "start the OAuth flow" and the client abandons its Basic
credentials. The app answers 404, which is the answer the client needs. This is
the same shape as `stacks/new-reporting-platform-backend/stack.yml:105` and the
reason it is there was learned the hard way.

Both routers use `entrypoints=web`, matching the app router — vd's Traefik has
only the one entrypoint (`templates/compose/infrastructure.yml`).

### Credentials

Two secrets per app, both generated at deploy time.

**Database.** A second role, `vd_<app>_ro`, alongside the existing read-write
`vd_<app>` (hyphens in the app name become underscores in both, per
`internal/db/provision.go:26`). Grants are the already-written `access == "ro"` branch in
`internal/db/provision.go:41`: `CONNECT`, `USAGE ON SCHEMA public`, `SELECT ON
ALL TABLES`, and `ALTER DEFAULT PRIVILEGES ... GRANT SELECT` so future tables are
covered. `ProvisionPostgresUser` currently hardcodes the role name
(`internal/db/provision.go:26`); the role name becomes a parameter, and the two
existing call sites keep today's behaviour by passing the same derived name.

Read-only is enforced twice over: the role physically cannot write, and
`--access-mode=restricted` keeps postgres-mcp itself to read-only tools.

**HTTP.** User `mcp`, password 32 hex chars. The htpasswd entry uses the `{SHA}`
scheme — `{SHA}` + base64(sha1(password)) — which Traefik's basicauth accepts and
which `crypto/sha1` + `encoding/base64` produce in three lines. bcrypt would mean
adding `golang.org/x/crypto`, and apr1 would mean ~40 lines of Apache MD5-crypt;
neither is worth it against a 128-bit random password, where the absence of a
salt is not a practical weakness. `{SHA}` also contains no `$`, so there is no
compose interpolation escaping to get wrong.

`mcp.env` (0600, at `AppDir/mcp.env`) holds:

```
DATABASE_URI=postgresql://vd_<app>_ro:<pw>@vd-postgres:5432/<db>
VD_MCP_USER=mcp
VD_MCP_PASSWORD=<pw>
```

The last two are vd's bookkeeping so `vd status` can report them; they are also
visible inside the MCP container, which is harmless — anything that can read that
container's environment already holds its database URI.

### Why one compose file

The MCP service goes into the app's existing `docker-compose.vd.yml`, not a
second file. Every lifecycle path already operates on that filename —
`cmd/destroy.go:53`, `internal/backup/backup.go:48`, `:96`, `:107`, `:118` — so
destroy, backup, rollback and redeploy pick the MCP container up for free. A
second compose file would mean touching all four.

The failure mode this trades away is that a broken MCP image could fail
`docker compose up` and take an app deploy down with it. Mitigated by pulling the
image once during `vd init`, so an unreachable registry surfaces at
initialisation rather than mid-deploy.

## Agent-facing output

`vd deploy --json` and `vd status --json` gain an `mcp` block:

```json
"mcp": {
  "url": "https://myapp.mcp.apps.platform.acuradai.com/sse",
  "user": "mcp",
  "password": "<32 hex>",
  "add": "claude mcp add --transport sse myapp-db https://myapp.mcp.apps.platform.acuradai.com/sse --header \"Authorization: Basic <base64 of mcp:pw>\""
}
```

The prebuilt `add` command matters: the audience is non-programmers and agents
working from a JSON response, and neither should have to assemble a base64 Basic
header by hand.

`Manifest` gains `MCP bool`. Presence cannot be derived from `DB == "postgres"`,
because apps deployed before this change have a postgres database and no MCP
container until their next deploy — deriving it would make `vd status` advertise
a URL that 404s.

## Collateral fixes

These are defects in code this feature depends on, not scope creep.

1. **nginx cuts the SSE stream.** `scripts/deploy.sh:257` sets
   `proxy_read_timeout 300s` and leaves `proxy_buffering` on. An MCP SSE stream
   dies after five idle minutes and is buffered rather than streamed. Fix:
   `proxy_buffering off` and `proxy_read_timeout 3600s` in the wildcard location.
   Applied to the existing location rather than a dedicated `server` block for
   `*.mcp.$DOMAIN`: unbuffered proxying and a long read timeout are harmless for
   small vibecoded apps, and a second block would duplicate the whole
   `proxy_set_header` list for no benefit today. When the MCP namespace needs its
   own `server` block for an IP allowlist, these two directives move with it.
   **Operational note:** an already-provisioned host needs `deploy.sh` re-run (or
   the config edited by hand) before its MCP endpoints behave. Without it the
   symptom is an MCP that works and then mysteriously stops.

2. **`copyFile` widens secret permissions.** `internal/backup/backup.go:186`
   writes every copy 0644, so backing up and restoring `src/.env` downgrades it
   from 0600. Preserve the source file's mode. Pre-existing, but this change adds
   a second 0600 secret next to it.

3. **`mcp.env` is not backed up.** Add it to `backup.Create` and
   `backup.Restore` alongside the compose file and `.env`, so a rolled-back
   compose and its env file stay a matched pair.

4. **`--drop-db` leaves the read-only role behind.** `cmd/destroy.go:93` drops
   only `vd_<app>`. Drop `vd_<app>_ro` too, so redeploying a destroyed app name
   does not inherit a stale role.

## Cost and ceiling

One Python container per app with a database: roughly 80 MB RSS each, sharing a
single image layer. Noise below about twenty such apps. If it stops being noise,
the upgrade is to put it behind a `vd deploy --mcp` flag — the compose template
already renders conditionally, so that is a flag and a manifest field. A
`ponytail:` comment records the ceiling and this upgrade path at the template
call site.

## Testing

One test: `docker.GenerateComposeFile` with the MCP flag set, asserting that

- the primary MCP router carries the basicauth middleware, and
- the `/.well-known/` router does **not**.

That inversion is the one thing here that fails silently and expensively — the
endpoint either sits wide open or refuses the credentials it was given, and
neither shows up as a broken deploy. Role provisioning shells out to `docker
exec psql` and is not meaningfully unit-testable; it is exercised by deploying.

## Documentation, same commit

| File | Change |
|------|--------|
| `plugin/skills/vibe/SKILL.md:72` | "never connect a Postgres MCP" → prod never; your own app's database via the platform-provided MCP only |
| `docs/SECURITY-FOR-VIBECODERS.md:56` | same rewording |
| `plugin/skills/deploy/SKILL.md` | the `mcp` response block and how to register it |
| `CLAUDE.md` | capability table, JSON output format, `--drop-db` note |
| `README.md` | capability mention, plus the upgrade note for already-provisioned hosts: re-run `deploy.sh` to pick up the `*.mcp.$DOMAIN` SAN and the nginx SSE directives, and `dig` one MCP host to confirm the wildcard resolves |
| `plugin/.claude-plugin/plugin.json` | 1.6.4 → 1.7.0 (MINOR: new capability) |
