# Plan: Rewrite vibe-deploy CLI in Go

## Context

Non-programmers vibecode apps and need to deploy them to bare metal servers. The current `bin/vd` is a 1029-line bash script that works but is fragile past this size. We're rewriting it as a single Go binary with embedded templates, structured JSON output, and proper error handling. The AI agent calls `vd` via SSH.

**Locked decisions:**
- Go single binary (one external dep: cobra)
- Single server only
- Wildcard domain (subdomains) or single domain with subpaths
- Basic security: container isolation + per-app DB users
- SSH + CLI interface with JSON output for agents
- Existing infra: PostgreSQL (`db`), Redis, Celery, SuperTokens running in Docker Compose

## What We're Building

A Go CLI `vd` that:
1. Auto-detects app type from source files
2. Generates Dockerfiles from embedded templates
3. Builds and runs containers via `docker compose`
4. Generates nginx configs and reloads nginx (running in Docker)
5. Provisions per-app database users
6. Supports backup/rollback, cron, SSL
7. Outputs structured JSON for AI agents, colored text for humans

## Project Structure

```
go.mod
main.go                              # Entrypoint, version var
embed.go                             # //go:embed directives

cmd/
  root.go                            # Cobra root, --json flag, VD_HOME/VD_DOMAIN
  init.go                            # Create dirs, networks, start nginx
  deploy.go                          # Full deploy orchestration
  status.go                          # Container state + health
  list.go                            # All deployed apps
  logs.go                            # Stream or snapshot logs
  rollback.go                        # Revert to previous backup
  destroy.go                         # Tear down app
  ssl.go                             # Retry certbot
  exec.go                            # docker exec passthrough
  cron.go                            # cron-set, cron-rm, cron-ls
  dbcreate.go                        # Provision DB user
  version.go

internal/
  app/
    detect.go                        # App type auto-detection from files
    types.go                         # AppType enum + Dockerfile mapping
  docker/
    compose.go                       # Generate docker-compose.vd.yml from template
    client.go                        # Shell-out wrappers for docker/compose commands
  nginx/
    config.go                        # Generate subdomain/path nginx configs
    reload.go                        # nginx -t && nginx -s reload
  state/
    config.go                        # Global config.json (domain, db container names)
    manifest.go                      # Per-app manifest.json
    paths.go                         # /opt/vibe-deploy/... path constants
  backup/
    backup.go                        # Create, restore, prune backups
  db/
    provision.go                     # Create per-app postgres/mysql users
  ssl/
    certbot.go                       # Certbot invocation
  cron/
    cron.go                          # Host crontab manipulation
  output/
    output.go                        # JSON/human output, structured errors

templates/                           # All embedded via //go:embed
  dockerfiles/                       # Existing 7 templates (unchanged)
  compose/
    app.yml.tmpl                     # Per-app compose template
    infrastructure.yml               # Nginx container compose
  nginx/
    nginx.conf                       # Master nginx config
    subdomain.conf.tmpl
    path-snippet.conf.tmpl
    paths-server.conf.tmpl
    ssl-block.conf.tmpl
```

## Integration With Existing Compose Stack

The user's compose creates a default network. We do NOT modify their compose file.

1. `vd init` creates external networks: `vd-net`, `vd-db`
2. User runs once: `docker network connect vd-db <postgres-container-name>`
3. `vd init --db-container <name>` stores the container name in `config.json`
4. Deployed apps join `vd-net` (always) + `vd-db` (if `--db postgres`)
5. Nginx container is on `vd-net` so it resolves app containers by name
6. App gets `DATABASE_URL` pointing to the DB container by name on `vd-db`

## Key Commands

```
vd init [--db-container <name>] [--domain <domain>]
vd deploy <dir> --name <n> [--port 3000] [--routing subdomain|path] [--db postgres|none] [--env-file .env] [--no-ssl]
vd status <name>
vd list
vd logs <name> [--lines 100] [--follow]
vd rollback <name>
vd destroy <name> [--yes]
vd ssl <name>
vd exec <name> -- <cmd...>
vd cron-set <name> --schedule "..." --command "..."
vd cron-rm <name>
vd cron-ls [--app <name>]
vd db-create <name> --type postgres [--access rw|ro]
```

All commands support `--json` for structured output.

## Output Format

Success: `{"ok": true, "command": "deploy", "data": {...}}`
Failure: `{"ok": false, "command": "deploy", "error": {"code": "BUILD_FAILED", "message": "...", "hint": "..."}}`

Error codes: `NOT_FOUND`, `BUILD_FAILED`, `UNHEALTHY`, `NGINX_CONFIG_INVALID`, `DB_NOT_FOUND`, etc.

## Implementation Phases

### Phase 1: Skeleton (~30 min)
- `go mod init`, cobra, `main.go`, `cmd/root.go`, `cmd/version.go`
- `internal/output/output.go` — JSON/human mode
- `internal/state/paths.go` — path constants
- `embed.go` — embed all templates
- Move Dockerfile templates into `templates/dockerfiles/`
- Create nginx/compose Go templates in `templates/`

### Phase 2: Init
- `cmd/init.go` — create dirs, networks, write embedded files, start nginx
- `internal/state/config.go` — read/write config.json

### Phase 3: Deploy (critical path)
- `internal/app/detect.go` + `types.go` — port bash detection logic to Go
- `internal/docker/compose.go` — render compose template
- `internal/docker/client.go` — build, up, down, inspect, logs
- `internal/nginx/config.go` + `reload.go` — render and reload
- `internal/state/manifest.go` — per-app state
- `internal/backup/backup.go` — pre-deploy backup
- `cmd/deploy.go` — orchestrate everything

### Phase 4: Operations
- `cmd/status.go`, `cmd/list.go`, `cmd/logs.go`
- `cmd/destroy.go`, `cmd/rollback.go`

### Phase 5: Secondary
- `cmd/ssl.go`, `cmd/exec.go`, `cmd/cron.go`, `cmd/dbcreate.go`
- `internal/db/provision.go`, `internal/ssl/certbot.go`, `internal/cron/cron.go`

### Phase 6: Agent docs
- Update `CLAUDE.md` with full command reference and usage patterns
- Add example deploy workflows

## Build & Install

```bash
# Cross-compile for Linux
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o vd .

# Install on server
scp vd server:/usr/local/bin/vd
ssh server "vd init --domain example.com --db-container project-db-1"
```

Single binary, no templates to copy — everything embedded.

## What We Keep From Bash Scaffolding
- 7 Dockerfile templates (as-is, embedded in Go binary)
- `templates/nginx/nginx.conf` master config (embedded)
- `templates/compose/infrastructure.yml` (embedded)
- `docs/plans/security-model.md` (reference doc)
- `docs/plans/architecture.md` (reference doc)

## What We Delete
- `bin/vd` (replaced by Go binary)
- `install.sh` (simplified — just copy binary + run `vd init`)

## Verification

1. `go build` compiles with no errors
2. `./vd version` outputs version JSON
3. `./vd init` creates directory structure and starts nginx container on a test server
4. `./vd deploy ./test-app --name test --no-ssl` builds, starts, configures nginx
5. `curl http://test.<domain>` returns app response
6. `./vd status test --json` returns structured JSON
7. `./vd destroy test --yes` cleans up everything
