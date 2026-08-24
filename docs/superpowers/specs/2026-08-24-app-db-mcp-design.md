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
cover `mcp.myapp.apps.platform.acuradai.com`, and multi-level wildcards are
neither issued by Let's Encrypt nor valid in TLS. Serving that shape would mean a
certificate per app, issued at deploy time. Flipping the order keeps every host
one label under a wildcard.

Three layers. Two need a change, and the DNS one is not the change it looks like.

- **nginx — no change.** An nginx `server_name` wildcard, unlike TLS and DNS,
  matches more than one label, so `*.apps.platform.acuradai.com` already matches
  `myapp.mcp.apps.platform.acuradai.com`.
- **DNS — one record, and not for the reason first assumed.** The design called
  for an explicit `*.mcp.apps.platform` A record on the theory that the first ACME
  challenge TXT would make `mcp.apps.platform` an empty non-terminal and, per RFC
  4592, stop `*.apps.platform` synthesizing anything below it. Tested against the
  live zone with the challenge record in place: Route 53 does **not** behave that
  way. It cascades a wildcard to every level beneath it and ignores the ENT, so
  `a.b.c.mcp.apps.platform` answered from `*.apps.platform` alone. The prediction
  was wrong for this provider. The record is kept for a different and weaker
  reason: `platform.acuradai.com` already CNAMEs to a Yandex GSLB, so this domain
  spans providers, and depending on Route 53's non-standard cascade to keep every
  MCP endpoint reachable is a bet. One record makes it explicit.
- **TLS — one SAN, plus an IAM grant.** `*.mcp.$DOMAIN` joins the existing
  certificate. `scripts/deploy.sh` drops its `if ! test -d` guard for
  `--cert-name "$DOMAIN" --expand`, which is idempotent when the SAN set is
  unchanged and, unlike the guard, actually adds a SAN to a host provisioned
  before it existed.

The DNS-01 challenge for the new SAN lands at `_acme-challenge.mcp.$DOMAIN`.
`certbot-dns-route53` picks its target zone by matching the longest hosted-zone
*name* against the challenge name; it performs no DNS resolution and does not
follow CNAMEs, so the only matching zone is `acuradai.com`, which `vd-certbot`
could not write to. Two ways to fix that were considered: a second delegated
hosted zone, mirroring the existing `*.apps` one, or IAM condition keys narrowing
a grant on `acuradai.com` to specific record names. The second was chosen, and it
retired the existing delegation too — one mechanism instead of two, no zone.

That trade is recorded in `dev-ops/infra/iam/vd-certbot`, which also had to bring
the policy into Terraform: the delegation was a structural guarantee that certbot
could not name a record in the zone carrying Google Workspace MX, SPF, DKIM and
DMARC, and its replacement is a `condition` block. A guarantee that is a policy
string belongs somewhere it gets reviewed.

A pleasant consequence of the namespace shape: no hostname-collision guard is
needed. Under `mcp-<app>.<domain>` an app named `mcp-foo` would have claimed app
`foo`'s MCP host; under `<app>.mcp.<domain>` app names and MCP hosts cannot
overlap.

### Traefik routing

Two routers, deliberately:

```
traefik.enable=true
traefik.http.services.vd-<app>-mcp.loadbalancer.server.port=8089
traefik.http.routers.vd-<app>-mcp.rule=Host(`<app>.mcp.<domain>`)
traefik.http.routers.vd-<app>-mcp.service=vd-<app>-mcp
traefik.http.routers.vd-<app>-mcp.middlewares=vd-<app>-mcp-auth
traefik.http.middlewares.vd-<app>-mcp-auth.basicauth.users=mcp:{SHA}<base64>

traefik.http.routers.vd-<app>-mcp-wellknown.rule=Host(`<app>.mcp.<domain>`) && PathPrefix(`/.well-known/`)
traefik.http.routers.vd-<app>-mcp-wellknown.service=vd-<app>-mcp
```

`traefik.enable=true` is not inherited from the app container — vd's Traefik runs
`exposedbydefault=false`. Without the explicit `loadbalancer.server.port=8089`,
Traefik falls back to the image's exposed port, which is not the port the command
line sets.

**No `priority` on the wellknown router.** Traefik derives priority from rule
length and compares explicit values against those defaults in the same space. The
main rule is 39 characters plus the app name, so a literal `priority=100` stops
winning once an app name reaches 62 characters — and app names are allowed 63.
The wellknown rule is a strict superset of the main one, so the default ordering
is always correct; the explicit priority was the bug.

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
`vd_<app>` (hyphens become underscores in both). `CONNECT`, `USAGE ON SCHEMA
public`, `SELECT ON ALL TABLES`, and — the part that is easy to get wrong —
`ALTER DEFAULT PRIVILEGES **FOR ROLE** vd_<app> ... GRANT SELECT ON TABLES`.

Without `FOR ROLE`, `ALTER DEFAULT PRIVILEGES` covers only objects created by the
role that ran the statement, which is `vd_admin`. The app creates its tables as
`vd_<app>`, so the companion would get SELECT on nothing — and on a first deploy
that is literally nothing, because provisioning runs before the container has
ever started and there is no table for `GRANT ... ON ALL TABLES` to cover yet.
The MCP would answer `permission denied` to every query in the exact scenario the
feature exists for. `ProvisionPostgresUser` also stops hardcoding the role name,
so both roles come from one place (`db.RoleName` / `db.ReadOnlyRoleName`).

Read-only is enforced in two places, and honestly the first layer is not airtight
on its own: the grants stop DML, but PUBLIC keeps `TEMPORARY` on the database
(so a "read-only" role can create and write temp tables) and `EXECUTE` on
functions, so a `SECURITY DEFINER` function owned by the app writes with the
owner's privileges. `TEMPORARY` is revoked from PUBLIC and re-granted to the app
role; the function path is closed only by `--access-mode=restricted` in
postgres-mcp.

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

## Route53 prerequisites

Blocking, before the first deploy that renders an MCP service. Full runbook in
`dev-ops/infra/iam/vd-certbot/README.md`; the sequence matters because a wrong
step fails silently at certificate renewal, around 2026-09-26.

1. `dns/acuradai-com`: add `*.mcp.apps.platform` A. Apply first.
2. `iam/vd-certbot`: import the live policy, free a version slot (IAM keeps 5,
   v1–v4 exist, Terraform does not prune), apply the widened document.
3. `dns/acuradai-com`: remove the `_acme-challenge.apps.platform` NS record, then
   delete hosted zone `Z022093612FB84OA1WQAM`. Together — a zone left behind
   keeps capturing certbot's writes into a zone no resolver consults.
4. `certbot ... --expand --dry-run` on nashville, then for real.

## Collateral fixes

Defects in code this feature depends on, not scope creep.

1. **nginx cuts the SSE stream.** `scripts/deploy.sh` set `proxy_read_timeout
   300s`, left `proxy_buffering` on, and never set `proxy_http_version 1.1` —
   nginx proxies HTTP/1.0 by default, which cannot stream a chunked response at
   all. All three fixed in the existing `location`, not a separate `server` block
   for `*.mcp.$DOMAIN`: unbuffered proxying and a long read timeout are harmless
   for small apps, and a second block would duplicate the whole
   `proxy_set_header` list. When the MCP namespace needs its own block for an IP
   allowlist, these move with it.

   While there: `Connection: "upgrade"` was sent unconditionally, on every
   ordinary request. Now driven by a `map` on `$http_upgrade`.

   **Operational note:** an already-provisioned host needs `deploy.sh` re-run
   before its MCP endpoints behave. Without it the symptom is an MCP that works
   and then mysteriously stops.

2. **Rollback left dead database credentials.** Pre-existing, and the MCP would
   have doubled it. `ProvisionPostgresUser` mints a fresh password and `ALTER
   ROLE`s on *every* deploy, while `backup.Restore` puts back the previous
   `.env` — so after an auto-rollback the app starts, passes its health check,
   and cannot reach its database. Fixed on the restore side rather than by
   redesigning provisioning: `backup.Restore` now parses the role and password
   back out of the restored env files and re-`ALTER`s them, which also makes the
   invariant hold for a manual `vd rollback`.

3. **`copyFile` widened secret permissions.** It wrote every copy 0644, so
   backing up and restoring `src/.env` downgraded it from 0600. Now preserves the
   source mode — needed twice over, with `mcp.env` beside it.

4. **`mcp.env` is backed up and restored** alongside the compose file that
   references it by name.

5. **`--drop-db` left the read-only role behind.** `DROP DATABASE` succeeds with
   it present, but redeploying the same app name would inherit a stale role.

6. **`DATABASE_URL` was appended, not replaced** — one line per deploy. Rollback
   has to read the password back out of that file, so it now has to be
   unambiguous.

7. **The MCP container has a healthcheck.** Nothing gates the deploy on it — vd
   waits only on the app container — but without one a crash-looping MCP presents
   as a working endpoint that times out. `vd status` reports it.

## Cost and ceiling

One Python container per app with a database: roughly 80 MB RSS each, sharing a
single image layer. Noise below about twenty such apps. If it stops being noise,
the upgrade is to put it behind a `vd deploy --mcp` flag — the compose template
already renders conditionally, so that is a flag and a manifest field. A
`ponytail:` comment records the ceiling and this upgrade path at the template
call site.

## Testing

`internal/docker/compose_test.go`, rendering the real template rather than a
fixture. The two-router arrangement fails silently in both directions — wrong one
way and the endpoint is unauthenticated, wrong the other way and clients drop
their credentials on a 401 from `/.well-known/` — and neither shows up as a
failed deploy. So: auth on the main router and absent from the wellknown one, the
wellknown rule longer than the main rule with no explicit priority,
`traefik.enable` and the explicit port present, and no MCP service at all when
`NeedsMCP` is false.

Role provisioning shells out to `docker exec psql` and is not meaningfully
unit-testable; it is exercised by deploying. The generated compose file was also
checked against `docker compose config`.

## Documentation, same commit

| File | Change |
|------|--------|
| `plugin/skills/vibe/SKILL.md:72` | "never connect a Postgres MCP" → prod never; your own app's database via the platform-provided MCP only |
| `docs/SECURITY-FOR-VIBECODERS.md:56` | same rewording |
| `plugin/skills/deploy/SKILL.md` | the `mcp` response block and how to register it |
| `CLAUDE.md` | capability table, JSON output format, `--drop-db` note |
| `README.md` | capability mention, plus the upgrade note for already-provisioned hosts: re-run `deploy.sh` to pick up the `*.mcp.$DOMAIN` SAN and the nginx SSE directives, and `dig` one MCP host to confirm the wildcard resolves |
| `plugin/.claude-plugin/plugin.json` | 1.6.4 → 1.7.0 (MINOR: new capability) |
