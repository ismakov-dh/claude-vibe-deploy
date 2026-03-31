# vibe-deploy

A deployment CLI for vibecoded apps on bare metal Linux servers. Single Go binary, designed to be called by AI agents via SSH.

## Architecture

- Single bare metal server running Linux
- Docker containers for app isolation
- Traefik reverse proxy (auto-discovers containers via Docker labels)
- Host nginx terminates TLS (wildcard cert) and proxies to Traefik
- Host nginx proxies wildcard domain to Traefik
- Two PostgreSQL instances:
  - **vd-postgres**: managed by vd, for apps that need their own database
  - **Prod DB**: existing production database, read-only access for dashboards
- One CLI tool: `vd` (Go binary with embedded Dockerfile templates)

## Guide for AI Agents

This section describes how to design, build, and deploy apps using `vd`. Always use `--json` flag in automated workflows.

### Available Infrastructure

| Resource | How to access | Notes |
|----------|--------------|-------|
| **Web routing** | Automatic via Traefik | Subdomain (`app.domain.com`) or path (`domain.com/app`) |
| **TLS/HTTPS** | Handled by host nginx (wildcard cert) | No per-app TLS setup needed |
| **Own PostgreSQL** | `vd deploy --db postgres` + `vd db-create` | Fresh database per app on vd-managed postgres |
| **Prod DB (read-only)** | `vd deploy --db prod-ro` + `vd db-create --type prod-ro --db-name <db>` | Read-only access to existing production data |
| **Cron jobs** | `vd cron-set` | Scheduled tasks run inside the app container |
| **Persistent storage** | PostgreSQL only | No local filesystem persistence — containers are ephemeral |

### How to Design Apps

**General rules:**
- App must listen on an HTTP port (auto-detected per type, override with `--port`)
- App must respond to `GET /` for health checks (or any HTTP response on the port)
- Use `DATABASE_URL` environment variable for database connections
- No local file writes that need to persist — use the database
- No hardcoded ports — read from `PORT` env var or use the framework default
- Keep dependencies in `package.json` / `requirements.txt` / `go.mod`

**Frontend apps (static HTML/JS/CSS):**
- Put built files in project root or use a build step (vite, CRA, etc.)
- SPA routing works — nginx serves with `try_files $uri /index.html`
- Default port: 80 (served by nginx:alpine inside the container)

**Backend APIs (Node/Python/Go):**
- Use express, fastify, flask, fastapi, django, or any HTTP framework
- Listen on `0.0.0.0`, not `127.0.0.1`
- Default ports: node=3000, python=8000, go=8080

**Full-stack apps (Next.js, Nuxt):**
- Deploy as a single container — the framework serves both frontend and API
- Default port: 3000

**Dashboard reading prod data:**
- Build a frontend + backend app
- Use `--db prod-ro` to get read-only access to production database
- The `DATABASE_URL` will point to the prod DB with SELECT-only permissions
- Never assume write access to prod

### App Type Detection

The CLI auto-detects from files present. Override with a `.vd-type` file containing the type name.

| Type | Detected by | Default port | Dockerfile template |
|------|------------|-------------|-------------------|
| `static-plain` | `index.html` (no package.json) | 80 | nginx:alpine serves files |
| `static-build` | package.json + vite/build script | 80 | npm build → nginx:alpine |
| `node-server` | package.json + express/fastify/koa/hono | 3000 | node:20-alpine |
| `node-next` | package.json + next dependency | 3000 | multi-stage next build |
| `python-flask` | requirements.txt + flask imports | 8000 | gunicorn + flask |
| `python-fastapi` | requirements.txt + fastapi imports | 8000 | uvicorn + fastapi |
| `python-django` | manage.py | 8000 | gunicorn + auto-migrate |
| `python-generic` | requirements.txt (no framework detected) | 8000 | runs app.py or main.py |
| `go` | go.mod | 8080 | multi-stage static build |
| `custom` | Dockerfile present | 3000 | uses your Dockerfile as-is |

Detection priority: `.vd-type` > `Dockerfile` > `manage.py` > `requirements.txt` > `package.json` > `index.html` > `go.mod`

### Deploy Workflow

**Step 1: Generate app code into a directory**

```
/tmp/my-dashboard/
  package.json
  src/
    App.jsx
    ...
  vite.config.js
```

**Step 2: Deploy**

```bash
# Simple static frontend
vd deploy /tmp/my-dashboard --name my-dashboard --json

# Backend API with its own database
vd deploy /tmp/my-api --name my-api --db postgres --json
vd db-create my-api --type postgres --json

# Dashboard reading production data
vd deploy /tmp/dash --name dash --db prod-ro --json
vd db-create dash --type prod-ro --db-name reporting_platform --json

# Pass environment variables
vd deploy /tmp/my-app --name my-app --env-file /tmp/my-app/.env --json

# Path-based routing (domain.com/my-app instead of my-app.domain.com)
vd deploy /tmp/my-app --name my-app --routing path --json
```

**Step 3: Verify**

```bash
vd status my-dashboard --json
```

**Step 4: If something is wrong**

```bash
# Check logs
vd logs-snapshot my-dashboard --lines 50 --json

# Rollback to previous version
vd rollback my-dashboard --json
```

### Command Reference

#### `vd deploy <source-dir>`
Deploy or redeploy an app. Backs up automatically before redeploy.

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | directory name | App name (lowercase, a-z/0-9/hyphens, 2-63 chars) |
| `--port` | auto-detected | Internal port the app listens on |
| `--routing` | `subdomain` | `subdomain` or `path` |
| `--db` | `none` | `postgres` (own DB), `prod-ro` (read-only prod), or `none` |
| `--env-file` | none | Path to .env file to inject |

#### `vd db-create <app-name>`
Provision a database user. Returns `DATABASE_URL` in JSON response.

| Flag | Default | Description |
|------|---------|-------------|
| `--type` | `postgres` | `postgres` (vd-managed) or `prod-ro` (existing prod DB) |
| `--access` | `rw` | `rw` or `ro` (prod-ro always forces `ro`) |
| `--db-name` | app name | Database name (required for `prod-ro`) |

#### `vd status <app-name>`
Container state, health, URL, deploy time.

#### `vd list`
All deployed apps with state and health.

#### `vd logs-snapshot <app-name>`
One-shot log dump. Use this in automation, not `vd logs` (which streams forever).

| Flag | Default | Description |
|------|---------|-------------|
| `--lines` | 100 | Number of log lines |

#### `vd rollback <app-name>`
Revert to previous deployment. Restores container image, compose file, env, and manifest.

#### `vd destroy <app-name>`
Stop container, remove app files. Backups are retained.

| Flag | Default | Description |
|------|---------|-------------|
| `--yes` | false | Skip confirmation (always use in automation) |

#### `vd cron-set <app-name>`
Schedule a recurring command inside the app container.

| Flag | Required | Description |
|------|----------|-------------|
| `--schedule` | yes | Cron expression (e.g. `"0 * * * *"`) |
| `--command` | yes | Command to run inside container |

#### `vd cron-rm <app-name>`
Remove all cron jobs for an app.

#### `vd cron-ls`
List all cron jobs. Filter with `--app <name>`.

#### `vd exec <app-name> -- <command>`
Run a one-off command inside the container.

### JSON Output Format

All commands support `--json`. Always use it in automated workflows.

Success:
```json
{
  "ok": true,
  "command": "deploy",
  "data": {
    "name": "my-app",
    "url": "https://my-app.apps.example.com",
    "status": "running",
    "health": "healthy"
  }
}
```

Error:
```json
{
  "ok": false,
  "command": "deploy",
  "error": {
    "code": "BUILD_FAILED",
    "message": "Docker build failed",
    "hint": "Check Dockerfile and source code",
    "details": "npm ERR! missing script: build"
  }
}
```

Error codes: `NOT_FOUND`, `INVALID_NAME`, `INVALID_SOURCE`, `DETECTION_FAILED`, `BUILD_FAILED`, `START_FAILED`, `UNHEALTHY`, `HEALTH_TIMEOUT`, `DB_NOT_FOUND`, `DB_PROVISION_FAILED`, `NO_BACKUPS`, `ROLLBACK_FAILED`

### Troubleshooting Checklist

| Problem | What to check |
|---------|--------------|
| `DETECTION_FAILED` | Missing package.json/requirements.txt/go.mod. Add a `.vd-type` file. |
| `BUILD_FAILED` | Check `vd logs-snapshot <name>`. Usually missing dependencies. |
| `UNHEALTHY` | App doesn't respond on its port. Check it listens on `0.0.0.0`, not `127.0.0.1`. Check the port matches `--port`. |
| App deploys but not reachable | Check `vd status`. Check DNS points to server. Check host nginx proxies to Traefik port 8080. |
| Database connection refused | Check `--db` flag was used on deploy. Check `vd db-create` was run. The container must be on `vd-db` network. |
| Prod DB access denied | Use `--type prod-ro`. Check `vd init --prod-db` was run. The prod container must be on `vd-db` network. |

### Constraints

- App names: lowercase, starts with letter, 2-63 chars, only a-z/0-9/hyphens
- No local file persistence — containers are ephemeral, use the database
- No volume mounts — apps cannot access the host filesystem
- No inter-app communication — apps are isolated on separate networks (except via public URLs)
- Backups are automatic before redeploy, last 5 kept per app
- Traefik dashboard: http://127.0.0.1:8099 (localhost only, for debugging)

---

## Development

For developers working on the `vd` tool itself.

### Build

```bash
make build          # macOS binary
make build-linux    # Linux amd64 binary for deployment
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
```

### Deploy to Server

```bash
# Full setup (builds binary, creates restricted user, wildcard TLS, nginx, vd init)
source .env.deploy && AWS_ACCESS_KEY_ID=$AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY=$AWS_SECRET_ACCESS_KEY \
  ./scripts/deploy.sh nashville apps.platform.xaidos.com [prod-db-container] [prod-db-user]

# Or via make
make deploy SERVER=nashville DOMAIN=apps.platform.xaidos.com
```

### Scripts

- `scripts/deploy.sh` — Full server installation (binary, user, TLS, nginx, vd init)
- `scripts/vd-ssh-wrapper` — SSH forced command wrapper (restricts to vd commands only)

### Tech Stack

- Go 1.26+, single external dependency (cobra)
- Templates embedded in binary via `//go:embed` — no files to copy to server
- All Docker operations use CLI shell-out, not SDK
- State: JSON files at `/opt/vibe-deploy/` (configurable via `VD_HOME` env)
- TLS handled by host nginx (wildcard cert), not Traefik
