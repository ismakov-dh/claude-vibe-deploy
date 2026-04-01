---
name: vibe
description: Load vibe-deploy platform constraints before building an app. Use when the user wants to create/vibecode an app that will be deployed via vibe-deploy. Ensures the app is designed within platform capabilities.
---

# Vibe-Deploy Platform Constraints

**IMPORTANT: Always communicate with the user in their language. Detect the language they use and respond in the same language throughout the session.**

You are building an app that will be deployed on the **vibe-deploy** platform. You MUST design and implement the app using ONLY the capabilities listed below. If the user's idea requires something not listed here, tell them it's not supported and propose an alternative using available tools.

## First: Inspect Existing Project

Before writing any code, check if there are existing source files in the current directory. If there are, audit them against the platform requirements below and report:

1. **App type detection** — will vd auto-detect correctly? If not, what `.vd-type` file is needed?
2. **Port binding** — does the app listen on `0.0.0.0`? Is the port correct for the type?
3. **Health check** — does `GET /` return a response?
4. **Database** — if the app uses a database, does it read from `DATABASE_URL` env var? Are there hardcoded credentials?
5. **Persistence** — does the app write to local filesystem expecting data to survive? It won't.
6. **Dependencies** — are all dependencies listed in package.json / requirements.txt / go.mod?
7. **Unsupported features** — does the app use Redis, S3, WebSockets, background workers, or anything not listed in "What You Can Use"?

For each issue found, explain what needs to change and why. Then propose a plan to fix all issues and ask the user to confirm before making changes.

## What You Can Use

### HTTP App Hosting
- Any app that serves HTTP. Deployed as a Docker container.
- Auto-detected from source: Node.js, Python, Go, static HTML, or custom Dockerfile.
- App MUST listen on `0.0.0.0` (not `127.0.0.1`).
- App MUST respond to `GET /` for health checks.
- Default ports: static=80, node=3000, python=8000, go=8080.

### PostgreSQL Database
- Fresh database per app, auto-provisioned on deploy.
- `DATABASE_URL` environment variable is auto-injected — use it, never hardcode credentials.
- Full read/write access to your own database.
- All persistent data MUST go in PostgreSQL — filesystem is wiped on every deploy.

### Prod Database (Read-Only)
- Read-only access to existing production databases for dashboards.
- SELECT-only permissions. Never assume write access.
- Use `DATABASE_URL` — it points to the prod DB.

### Environment Variables
- Pass API keys, secrets, and config via `.env` file.
- `DATABASE_URL` is auto-injected when database is requested.
- Never hardcode secrets in source code.

### Cron Jobs
- Scheduled commands that run inside the app container.
- Standard cron syntax (e.g. `"0 * * * *"` = hourly, `"*/5 * * * *"` = every 5 min).
- Use for periodic tasks: cleanup, reports, data sync.

### Routing
- **Subdomain**: `myapp.apps.platform.xaidos.com` (default)
- **Path-based**: `apps.platform.xaidos.com/myapp`
- TLS/HTTPS is automatic (wildcard cert). All apps are HTTPS.

## What You CANNOT Use

Do NOT design apps that require any of these:

- **No Redis / Memcached** — use PostgreSQL or in-memory caching
- **No S3 / file storage** — store files as bytea in PostgreSQL or use external APIs
- **No background workers** (Celery, Bull, Sidekiq) — use cron for periodic tasks, or process inline, or use PostgreSQL as a job queue
- **No WebSockets** — HTTP request/response only
- **No inter-app communication** — apps are isolated. Use public URLs if needed
- **No email sending** — use external APIs (SendGrid, Resend) with API keys in env vars
- **No persistent filesystem** — containers are ephemeral. All data in PostgreSQL
- **No custom Docker volumes or mounts**
- **No root access inside containers**
- **No raw TCP/UDP** — HTTP only

## App Patterns That Work

### Static Frontend (React, Vue, Vite)
```
package.json        # with vite + build script
src/App.jsx
index.html
vite.config.js
```
Auto-detected as `static-build`. Served by nginx:alpine. SPA routing works.

### Node.js API + Frontend
```
package.json        # with express
server.js           # serves API routes + static files from public/
public/
  index.html
```
Auto-detected as `node-server`. Single container serves everything.

### Python FastAPI/Flask API
```
requirements.txt    # with fastapi or flask
app.py              # or main.py
```
Auto-detected. Gunicorn/uvicorn handles it.

### Next.js Full-Stack
```
package.json        # with next
src/
  app/
    page.tsx
    api/route.ts
```
Auto-detected as `node-next`. Single container.

### Dashboard Reading Prod Data
```
package.json        # with express + pg
server.js           # queries prod DB via DATABASE_URL
public/
  index.html        # frontend calls /api endpoints
```
Deploy with `--db prod-ro --db-name <existing-database>`.

## Rules for Writing Code

1. Read database connection from `DATABASE_URL` env var — never hardcode
2. Read port from `PORT` env var or use framework default
3. Listen on `0.0.0.0`, not `127.0.0.1`
4. Keep dependencies in `package.json` / `requirements.txt` / `go.mod`
5. Do NOT create a Dockerfile unless auto-detection doesn't work
6. Store all persistent data in PostgreSQL
7. Use `.vd-type` file to override auto-detection if needed (contains type name, e.g. `node-server`)
8. Initialize database tables on app startup (CREATE TABLE IF NOT EXISTS)

## Supported App Types

| Type | Detected by | Port |
|------|------------|------|
| `static-plain` | `index.html` only | 80 |
| `static-build` | package.json + vite/build script | 80 |
| `node-server` | package.json + express/fastify/koa/hono | 3000 |
| `node-next` | package.json + next | 3000 |
| `python-flask` | requirements.txt + flask | 8000 |
| `python-fastapi` | requirements.txt + fastapi | 8000 |
| `python-django` | manage.py | 8000 |
| `python-generic` | requirements.txt only | 8000 |
| `go` | go.mod | 8080 |
| `custom` | Dockerfile present | your choice |

Detection priority: `.vd-type` > `Dockerfile` > `manage.py` > `requirements.txt` > `package.json` > `index.html` > `go.mod`

## When You're Done Building

Tell the user to run `/deploy` to deploy the app to the server.
