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
5. **Migrations** — if the app uses a database, does it use a migration tool? Are migrations reversible? Is `migrate`/`upgrade` wired into startup? Raw `CREATE TABLE` is not acceptable.
6. **Persistence** — does the app write to local filesystem expecting data to survive? It won't.
7. **Dependencies** — are all dependencies listed in package.json / requirements.txt / go.mod?
8. **Unsupported features** — does the app use Redis, S3, WebSockets, background workers, or anything not listed in "What You Can Use"?

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
8. **Always use database migrations** — never raw `CREATE TABLE IF NOT EXISTS` (see below)
9. **NEVER commit secrets or .env files to git.** No API keys, passwords, tokens, or DATABASE_URL values in source code or tracked files. Add `.env` to `.gitignore`. Pass secrets via `--env-file` on deploy

## Database Migrations

If the app uses a database, you MUST use a migration tool — never create tables with raw SQL or `CREATE TABLE IF NOT EXISTS`. Migrations allow safe schema changes on redeploy and rollback.

**All migrations MUST be reversible** (have a "down" / "rollback" step). This is critical — on rollback, the agent may need to unapply migrations to match the previous code version.

### Migration tool by framework

| Framework | Tool | Setup |
|-----------|------|-------|
| Django | Built-in (`manage.py migrate`) | Runs automatically on container start. Create migrations with `manage.py makemigrations` |
| FastAPI / Flask / plain Python | Alembic | Add `alembic` to requirements.txt. Init with `alembic init migrations`. Add `alembic upgrade head` to app startup or as an entrypoint script |
| Node.js (Express, Fastify, Hono) | Prisma | Add `prisma` to package.json. Define schema in `prisma/schema.prisma`. Add `npx prisma migrate deploy` to start script |
| Node.js (alternative) | Knex | Add `knex` to package.json. Create migrations in `migrations/`. Add `npx knex migrate:latest` to start script |
| Next.js | Prisma | Same as Node.js — add to `package.json` scripts |
| Go | goose or golang-migrate | Add migration files in `migrations/`. Run on app startup |

### How migrations interact with deploy/rollback

- **Deploy**: migrations run forward on container start (`upgrade head` / `migrate:latest`)
- **Rollback**: use `vd rollback --restore-db` to restore both container and database to the previous deploy state. Migration downgrade is not supported — full DB restore is the rollback mechanism
- **Destroy**: database is backed up automatically before `--drop-db`

### Entrypoint pattern for non-Django apps

For frameworks without auto-migration on startup, create an `entrypoint.sh`:

```bash
#!/bin/sh
# Run migrations, then start the app
alembic upgrade head    # or: npx prisma migrate deploy
exec "$@"
```

And in the Dockerfile (or override via `.vd-type` = `custom`):
```dockerfile
ENTRYPOINT ["./entrypoint.sh"]
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000"]
```

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
