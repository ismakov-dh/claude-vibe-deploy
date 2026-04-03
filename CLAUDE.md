# vibe-deploy

A deployment CLI for vibecoded apps on bare metal Linux servers. Single Go binary, called by AI agents via SSH.

## Architecture

- Single bare metal server running Linux
- Docker containers for app isolation
- Traefik reverse proxy (auto-discovers containers via Docker labels)
- Host nginx terminates TLS (wildcard cert) and proxies to Traefik
- Two PostgreSQL instances:
  - **vd-postgres**: managed by vd, for apps that need their own database
  - **Prod DB**: existing production database, read-only access for dashboards
- CLI tool: `vd` accessed via `ssh vd-server "vd <command> --json"`

---

## Guide for AI Agents

**You MUST design and implement apps using ONLY the capabilities described below.** Do not assume any infrastructure, service, or feature that is not listed here. If the user's idea requires something not available (e.g. Redis, S3, WebSockets, background workers outside cron, email sending), you must either find a way to implement it with the available tools or tell the user it's not supported yet.

### What You Have

| Capability | How to use | Details |
|-----------|-----------|---------|
| **HTTP app hosting** | `vd deploy` | Any app that listens on an HTTP port. Containers are isolated. |
| **Subdomain routing** | `--routing subdomain` (default) | App at `<name>.apps.platform.xaidos.com` |
| **Path routing** | `--routing path` | App at `apps.platform.xaidos.com/<name>` |
| **TLS/HTTPS** | Automatic | Host nginx has wildcard cert. All apps are HTTPS. No config needed. |
| **Own PostgreSQL database** | `--db postgres` | Auto-provisioned on deploy. `DATABASE_URL` injected into `.env`. Fresh DB per app. |
| **Prod DB read-only access** | `--db prod-ro --db-name <db>` | Read-only (SELECT only) access to existing production databases for dashboards. |
| **Environment variables** | `--env-file` or auto-injected | Pass secrets, API keys, config. `DATABASE_URL` is auto-injected when using `--db`. |
| **Cron jobs** | `vd cron-set` | Scheduled commands that run inside the app container. |
| **Health checks** | Automatic | Traefik + Docker check `GET http://127.0.0.1:<port>/` every 30s. |
| **Auto-rollback** | Automatic | If health check fails after deploy, previous version is restored. |
| **Manual rollback** | `vd rollback` | Revert to any of the last 5 deployments. |
| **Logs** | `vd logs-snapshot` | Get container logs for debugging. |
| **File upload** | `vd push` | Send files via tar stream through SSH. No scp needed. |

### What You DON'T Have

Do not design apps that require any of these:

- **No Redis/Memcached** — use PostgreSQL for caching if needed (or in-memory within the app)
- **No S3/file storage** — no persistent filesystem. Store files as bytea in PostgreSQL or use external APIs
- **No background workers** (beyond cron) — no Celery, Bull, Sidekiq. Use cron for periodic tasks. For async work, process inline or use PostgreSQL as a job queue
- **No WebSocket support** — HTTP only through Traefik (WebSocket upgrade headers are forwarded, but not tested)
- **No inter-app communication** — apps cannot call each other by container name. Use public URLs if needed
- **No email sending** — use external APIs (SendGrid, Resend, etc.) with API keys in env vars
- **No custom Docker volumes** — containers are ephemeral. All persistent data goes in PostgreSQL
- **No root access inside containers** — apps run as non-root
- **No custom ports exposed** — only the app's HTTP port is routed via Traefik

### How to Design Apps

**Every app is a Docker container that serves HTTP.** That's it. Design within this constraint.

**Rules:**
1. App MUST listen on an HTTP port on `0.0.0.0` (not `127.0.0.1`)
2. App MUST respond to `GET /` with any HTTP response (health check)
3. Use `DATABASE_URL` env var for database connections — never hardcode credentials
4. Use `PORT` env var if the framework supports it, or use the default port
5. All persistent data MUST go in PostgreSQL — filesystem is wiped on redeploy
6. Keep dependencies in `package.json` / `requirements.txt` / `go.mod` — no global installs
7. Do NOT create a Dockerfile unless the built-in templates don't work — auto-detection handles most cases

**Default ports by app type:**
| Type | Port |
|------|------|
| Static (HTML/JS/CSS) | 80 |
| Node.js (express, fastify, next) | 3000 |
| Python (flask, fastapi, django) | 8000 |
| Go | 8080 |

**App patterns that work well:**
- Static frontend (React/Vue/Vite) — auto-detected, served by nginx:alpine, SPA routing works
- Node.js API + static frontend in one container (express serves both `/api` and static files)
- Next.js full-stack — auto-detected, single container
- Python FastAPI/Flask API — auto-detected, gunicorn/uvicorn handles it
- Dashboard that reads prod data — backend queries prod DB via `DATABASE_URL`, frontend calls backend API

### App Type Detection

Auto-detected from files. Override with a `.vd-type` file containing the type name.

| Type | Detected by | Port | Runtime |
|------|------------|------|---------|
| `static-plain` | `index.html` only | 80 | nginx:alpine |
| `static-build` | package.json + vite/build script | 80 | npm build → nginx:alpine |
| `node-server` | package.json + express/fastify/koa/hono | 3000 | node:20-alpine |
| `node-next` | package.json + next | 3000 | multi-stage next build |
| `python-flask` | requirements.txt + flask | 8000 | gunicorn |
| `python-fastapi` | requirements.txt + fastapi | 8000 | uvicorn |
| `python-django` | manage.py | 8000 | gunicorn + auto-migrate |
| `python-generic` | requirements.txt only | 8000 | runs app.py or main.py |
| `go` | go.mod | 8080 | multi-stage static build |
| `custom` | Dockerfile present | (your choice) | your Dockerfile |

Priority: `.vd-type` > `Dockerfile` > `manage.py` > `requirements.txt` > `package.json` > `index.html` > `go.mod`

### Deploy Workflow

**Step 1: Push app files to server**

```bash
tar cf - --exclude='node_modules' --exclude='.git' --exclude='__pycache__' --exclude='.venv' --exclude='venv' --exclude='.next' ./my-app | ssh vd-server "vd push my-app --json"
```

**Always exclude build artifacts** — `node_modules`, `.git`, `__pycache__`, `.venv`, `venv`, `.next`. These are rebuilt inside Docker.

**Step 2: Deploy**

```bash
# Static frontend — no database
vd deploy /opt/vibe-deploy/push/my-app --name my-dashboard --json

# Backend with its own database — DB auto-provisioned, DATABASE_URL auto-injected
vd deploy /opt/vibe-deploy/push/my-app --name my-api --db postgres --json

# Dashboard reading production data
vd deploy /opt/vibe-deploy/push/my-app --name my-dash --db prod-ro --db-name reporting_platform --json

# With extra env vars (API keys, secrets)
vd deploy /opt/vibe-deploy/push/my-app --name my-app --db postgres --env-file /opt/vibe-deploy/push/my-app/.env --json

# Path-based routing
vd deploy /opt/vibe-deploy/push/my-app --name my-app --routing path --json
```

**Step 3: Verify**

```bash
vd status my-app --json
```

**Step 4: If broken**

```bash
vd logs-snapshot my-app --lines 50 --json   # check logs
vd rollback my-app --json                    # revert to previous version
```

### Command Reference

#### `vd push <app-name>`

Receive app files via tar stream on stdin. Use before deploy.

```bash
tar cf - --exclude='node_modules' --exclude='.git' --exclude='__pycache__' --exclude='.venv' --exclude='venv' --exclude='.next' ./my-app | ssh vd-server "vd push myapp --json"
```

Files stored at `/opt/vibe-deploy/push/<app-name>`. Always exclude build artifacts — they are rebuilt inside Docker.

#### `vd deploy <source-dir>`

Deploy or redeploy an app. Auto-provisions database if `--db` is set. Backs up before redeploy. Auto-rollbacks if health check fails.

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | directory name | App name (lowercase, a-z/0-9/hyphens, 2-63 chars) |
| `--port` | auto-detected | Internal port the app listens on |
| `--routing` | `subdomain` | `subdomain` or `path` |
| `--db` | `none` | `postgres` (own DB), `prod-ro` (read-only prod), or `none` |
| `--db-access` | `rw` | `rw` or `ro` (prod-ro always forces `ro`) |
| `--db-name` | app name | Database name (required for `prod-ro`) |
| `--env-file` | none | Path to .env file to inject (merged with auto-generated DATABASE_URL) |

#### `vd status <app-name>`

Returns: container state, health, URL, app type, deploy time, database info.

#### `vd list`

All deployed apps with state, health, and URLs.

#### `vd logs-snapshot <app-name>`

One-shot log dump. **Always use this in automation**, not `vd logs` (which streams forever).

| Flag | Default | Description |
|------|---------|-------------|
| `--lines` | 100 | Number of log lines |

#### `vd rollback <app-name>`

Revert to previous deployment. Restores container image, compose file, env, and manifest. Last 5 backups kept.

#### `vd destroy <app-name>`

Stop container, remove app files.

| Flag | Default | Description |
|------|---------|-------------|
| `--yes` | false | Skip confirmation (always use in automation) |
| `--drop-db` | false | Also drop the database and user (vd-managed only, never drops prod) |

#### `vd db-create <app-name>`

Provision a database user independently (usually not needed — `vd deploy --db` does this automatically).

| Flag | Default | Description |
|------|---------|-------------|
| `--type` | `postgres` | `postgres` (vd-managed) or `prod-ro` (existing prod DB) |
| `--access` | `rw` | `rw` or `ro` (prod-ro always forces `ro`) |
| `--db-name` | app name | Database name (required for `prod-ro`) |

#### `vd cron-set <app-name>`

Schedule a recurring command inside the app container.

| Flag | Required | Description |
|------|----------|-------------|
| `--schedule` | yes | Cron expression (e.g. `"0 * * * *"` = hourly, `"*/5 * * * *"` = every 5 min) |
| `--command` | yes | Command to run inside container (e.g. `"node jobs/cleanup.js"`) |

#### `vd cron-rm <app-name>`

Remove all cron jobs for an app.

#### `vd cron-ls`

List all cron jobs. Filter with `--app <name>`.

#### `vd exec <app-name> -- <command>`

Run a one-off command inside the container. **Not available via SSH agent access** (blocked by wrapper for security).

### JSON Output Format

All commands support `--json`. **Always use it in automated workflows.**

Success:
```json
{"ok": true, "command": "deploy", "data": {"name": "my-app", "url": "https://my-app.apps.platform.xaidos.com", "status": "running", "health": "healthy"}}
```

Error:
```json
{"ok": false, "command": "deploy", "error": {"code": "BUILD_FAILED", "message": "Docker build failed", "hint": "Check Dockerfile and source code", "details": "..."}}
```

Error codes: `NOT_FOUND`, `INVALID_NAME`, `INVALID_SOURCE`, `DETECTION_FAILED`, `BUILD_FAILED`, `START_FAILED`, `UNHEALTHY`, `HEALTH_TIMEOUT`, `DB_NOT_FOUND`, `DB_PROVISION_FAILED`, `MISSING_DB_NAME`, `NO_BACKUPS`, `ROLLBACK_FAILED`

### Troubleshooting

| Problem | Fix |
|---------|-----|
| `DETECTION_FAILED` | Add a `.vd-type` file with the type name |
| `BUILD_FAILED` | Check `vd logs-snapshot`. Usually missing dependency or bad import |
| `UNHEALTHY` | App must listen on `0.0.0.0` (not `127.0.0.1`). Check port matches `--port` |
| App not reachable | Check `vd status`. Verify DNS `*.apps.platform.xaidos.com` resolves |
| DB connection refused | Ensure `--db postgres` or `--db prod-ro` was passed to deploy |
| Prod DB access denied | Use `--db prod-ro --db-name <existing-db-name>` |
| Stale data after destroy | Use `--drop-db` to also drop the database |

### Constraints Summary

- **Naming**: lowercase, starts with letter, 2-63 chars, a-z/0-9/hyphens only
- **Persistence**: PostgreSQL only. No filesystem persistence.
- **Networking**: HTTP only. No raw TCP, no UDP, no inter-container networking.
- **Resources**: No CPU/memory limits yet. Don't deploy crypto miners.
- **Secrets**: Pass via `--env-file`. Never hardcode in source.
- **Deploys**: Automatic backup before redeploy. Last 5 kept. Auto-rollback on health failure.

---

## Development

For developers working on the `vd` tool itself.

### Build

```bash
make build          # macOS binary
make build-linux    # Linux amd64 binary for deployment
```

### Deploy to Server

```bash
# Full setup (binary, user, TLS, nginx, vd init)
source .env.deploy && AWS_ACCESS_KEY_ID=$AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY=$AWS_SECRET_ACCESS_KEY \
  ./scripts/deploy.sh root@server apps.example.com

# Connect prod DB (primary for user creation, replica for app connections)
ssh vd-server "vd init --prod-db <primary> --prod-db-replica <replica> --prod-db-user <admin-user>"
```

### Project Structure

```
main.go, embed.go           # Entry point + template embedding
cmd/                         # Cobra CLI commands
internal/
  app/                       # App type detection
  docker/                    # Docker/compose operations (shell-out, not SDK)
  backup/                    # Backup/restore
  db/                        # Database user provisioning
  cron/                      # Host crontab manipulation
  state/                     # Config, manifest, path constants
  output/                    # JSON/human output formatting
  shell/                     # exec.Command wrapper
templates/
  dockerfiles/               # 7 Dockerfile templates (embedded in binary)
  compose/                   # App + infrastructure compose templates
scripts/
  deploy.sh                  # Server installation script
  vd-ssh-wrapper             # SSH forced command wrapper
```

### Tech Stack

- Go 1.26+, single external dependency (cobra)
- Templates embedded in binary via `//go:embed`
- Docker operations via CLI shell-out, not SDK
- State: JSON files at `/opt/vibe-deploy/`
- TLS: host nginx wildcard cert, not Traefik
