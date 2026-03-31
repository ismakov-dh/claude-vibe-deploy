---
name: deploy
description: Deploy a vibecoded app to the server via vibe-deploy. Use when the user wants to deploy, update, check status, or manage an app on the vibe-deploy platform.
---

# Deploy with vibe-deploy

Deploy apps to the server via SSH. All commands go through `ssh vd-server "vd <command> --json"`.

## Prerequisites

- SSH access configured: `vd-server` host in `~/.ssh/config`
- App code in a directory on the server (scp it first)

## Deploy Workflow

### 1. Copy app to server

```bash
scp -r /path/to/app vd-server:/tmp/<app-name>
```

Note: `scp` to `vd-server` won't work (forced command blocks it). Copy via the regular SSH user first:
```bash
scp -r /path/to/app nashville:/tmp/<app-name>
```

### 2. Deploy

```bash
# Static frontend — no database
ssh vd-server "vd deploy /tmp/<app-name> --name <app-name> --json"

# With its own PostgreSQL database (auto-provisioned, DATABASE_URL auto-injected)
ssh vd-server "vd deploy /tmp/<app-name> --name <app-name> --db postgres --json"

# Dashboard reading production data (read-only access)
ssh vd-server "vd deploy /tmp/<app-name> --name <app-name> --db prod-ro --db-name <existing-db> --json"

# With extra environment variables
ssh vd-server "vd deploy /tmp/<app-name> --name <app-name> --db postgres --env-file /tmp/<app-name>/.env --json"

# Path-based routing instead of subdomain
ssh vd-server "vd deploy /tmp/<app-name> --name <app-name> --routing path --json"
```

### 3. Verify

```bash
ssh vd-server "vd status <app-name> --json"
```

### 4. If something is wrong

```bash
# Check logs
ssh vd-server "vd logs-snapshot <app-name> --lines 50 --json"

# Rollback to previous version
ssh vd-server "vd rollback <app-name> --json"
```

## Command Reference

### `vd deploy <source-dir>`

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | dir name | App name (lowercase, a-z/0-9/hyphens, 2-63 chars) |
| `--port` | auto | Internal port |
| `--routing` | `subdomain` | `subdomain` or `path` |
| `--db` | `none` | `postgres` (own DB), `prod-ro` (read-only prod), `none` |
| `--db-access` | `rw` | `rw` or `ro` |
| `--db-name` | app name | Database name (required for `prod-ro`) |
| `--env-file` | none | Path to .env file on server |

### `vd status <app-name>`
Returns state, health, URL, deploy time.

### `vd list`
All deployed apps.

### `vd logs-snapshot <app-name> [--lines N]`
One-shot log dump. Default 100 lines. **Always use this, not `vd logs`** (which streams forever).

### `vd rollback <app-name>`
Revert to previous deployment. Last 5 backups kept.

### `vd destroy <app-name> --yes [--drop-db]`
Stop and remove app. `--drop-db` also drops the database and user.

### `vd cron-set <app-name> --schedule "..." --command "..."`
Add a scheduled task. Runs inside the container.

### `vd cron-rm <app-name>`
Remove all cron jobs for an app.

### `vd cron-ls [--app <name>]`
List cron jobs.

## JSON Output

Success:
```json
{"ok": true, "command": "deploy", "data": {"name": "my-app", "url": "https://my-app.apps.platform.xaidos.com", ...}}
```

Error:
```json
{"ok": false, "command": "deploy", "error": {"code": "BUILD_FAILED", "message": "...", "hint": "..."}}
```

Always check `ok` field. On error, read `hint` for the fix.

## Error Codes

| Code | Meaning | Fix |
|------|---------|-----|
| `DETECTION_FAILED` | Can't detect app type | Add `.vd-type` file |
| `BUILD_FAILED` | Docker build failed | Check logs: `vd logs-snapshot <name>` |
| `UNHEALTHY` | App doesn't respond on port | Listen on `0.0.0.0`, check `--port` |
| `DB_NOT_FOUND` | No DB container configured | Run `vd init --prod-db <container>` on server |
| `DB_PROVISION_FAILED` | Can't create DB user | Check postgres container is running |
| `NOT_FOUND` | App doesn't exist | Check name with `vd list` |
| `NO_BACKUPS` | No backups for rollback | Only exists after first redeploy |

## App Naming Rules

- Lowercase only
- Starts with a letter
- 2-63 characters
- Only a-z, 0-9, hyphens
- Examples: `my-dashboard`, `sales-api`, `report-viewer`
