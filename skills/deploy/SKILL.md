---
name: deploy
description: Deploy a vibecoded app to the server via vibe-deploy. Use when the user wants to deploy, update, check status, or manage an app on the vibe-deploy platform.
---

# Deploy with vibe-deploy

Deploy apps to the server via SSH.

## Connection Setup

Before deploying, ask the user for ONE of these:

1. **SSH alias** (e.g. `vd-server`) — if they have it in `~/.ssh/config`
2. **Server IP + key path** (e.g. `141.105.67.159` + `~/.ssh/vd_agent_key`) — if no alias

Build the SSH command accordingly:

```bash
# Option 1: SSH alias
SSH_CMD="ssh vd-server"

# Option 2: IP + key
SSH_CMD="ssh -i <key-path> -o StrictHostKeyChecking=accept-new vd-user@<server-ip>"
```

All vd commands follow this pattern:

```bash
$SSH_CMD "vd <command> --json"
```

## Deploy Workflow

### 1. Push app files to server

Use `vd push` to send files via tar stream through SSH — no scp needed:

```bash
tar cf - ./my-app | $SSH_CMD "vd push <app-name> --json"
```

Files are stored at `/opt/vibe-deploy/push/<app-name>` on the server.

### 2. Deploy

```bash
# Static frontend — no database
$SSH_CMD "vd deploy /opt/vibe-deploy/push/<app-name> --name <app-name> --json"

# With its own PostgreSQL database (auto-provisioned, DATABASE_URL auto-injected)
$SSH_CMD "vd deploy /opt/vibe-deploy/push/<app-name> --name <app-name> --db postgres --json"

# Dashboard reading production data (read-only access)
$SSH_CMD "vd deploy /opt/vibe-deploy/push/<app-name> --name <app-name> --db prod-ro --db-name <existing-db> --json"

# With extra environment variables (write .env to server first)
$SSH_CMD "vd deploy /opt/vibe-deploy/push/<app-name> --name <app-name> --db postgres --env-file /opt/vibe-deploy/push/<app-name>/.env --json"

# Path-based routing instead of subdomain
$SSH_CMD "vd deploy /opt/vibe-deploy/push/<app-name> --name <app-name> --routing path --json"
```

### 3. Verify

```bash
$SSH_CMD "vd status <app-name> --json"
```

The URL will be `https://<app-name>.apps.platform.xaidos.com`.

### 4. If something is wrong

```bash
# Check logs
$SSH_CMD "vd logs-snapshot <app-name> --lines 50 --json"

# Rollback to previous version
$SSH_CMD "vd rollback <app-name> --json"
```

## Full Example

```bash
# Set up connection (ask user for these values)
KEY=~/.ssh/vd_agent_key
SERVER=141.105.67.159
SSH_CMD="ssh -i $KEY -o StrictHostKeyChecking=accept-new vd-user@$SERVER"

# Push app files to server
tar cf - ./my-app | $SSH_CMD "vd push my-app --json"

# Deploy with database
$SSH_CMD "vd deploy /opt/vibe-deploy/push/my-app --name my-app --db postgres --json"

# Check status
$SSH_CMD "vd status my-app --json"

# Set up a cron job
$SSH_CMD "vd cron-set my-app --schedule '0 * * * *' --command 'node jobs/cleanup.js' --json"

# Later: destroy with database cleanup
$SSH_CMD "vd destroy my-app --yes --drop-db --json"
```

## Command Reference

### `vd push <app-name>`
Receive app files via tar stream on stdin. Use before deploy.
```bash
tar cf - ./my-app | $SSH_CMD "vd push <app-name> --json"
```
Files stored at `/opt/vibe-deploy/push/<app-name>`.

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

| Code | Fix |
|------|-----|
| `DETECTION_FAILED` | Add `.vd-type` file |
| `BUILD_FAILED` | Check logs: `vd logs-snapshot <name>` |
| `UNHEALTHY` | Listen on `0.0.0.0`, check `--port` |
| `DB_NOT_FOUND` | Admin needs to run `vd init --prod-db` |
| `DB_PROVISION_FAILED` | Check postgres container is running |
| `NOT_FOUND` | Check name with `vd list` |
| `NO_BACKUPS` | Only exists after first redeploy |

## App Naming Rules

- Lowercase, starts with letter, 2-63 chars, a-z/0-9/hyphens only
