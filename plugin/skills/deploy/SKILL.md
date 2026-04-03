---
name: deploy
description: Deploy a vibecoded app to the server via vibe-deploy. Use when the user wants to deploy, update, check status, or manage an app on the vibe-deploy platform.
---

# Deploy with vibe-deploy

**IMPORTANT: Always communicate with the user in their language. Detect the language they use and respond in the same language throughout the session.**

Deploy apps to the server via SSH. Always use `--json` flag for parsing, but present results to the user in a clear, human-readable way. After a successful deploy, always tell the user:
- The app URL
- App type detected
- Whether a database was provisioned
- Health status

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

### Verify server is ready

Before first deploy, check that vd is initialized:

```bash
$SSH_CMD "vd list --json"
```

If this returns `"ok": true`, the server is ready. If it fails with a connection or permission error, the user needs to ask their admin to set up vd on the server.

## Deploy Workflow

### 1. Push app files to server

Use `vd push` to send files via tar stream through SSH — no scp needed. **Always exclude build artifacts:**

```bash
tar cf - --exclude='node_modules' --exclude='.git' --exclude='__pycache__' --exclude='.venv' --exclude='venv' --exclude='.next' ./my-app | $SSH_CMD "vd push <app-name> --json"
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

# With extra environment variables (.env is pushed with app files, NEVER commit .env to git)
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

# Push app files to server (always exclude build artifacts)
tar cf - --exclude='node_modules' --exclude='.git' --exclude='__pycache__' --exclude='.venv' --exclude='venv' --exclude='.next' ./my-app | $SSH_CMD "vd push my-app --json"

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
Receive app files via tar stream on stdin. Use before deploy. **Always exclude build artifacts:**
```bash
tar cf - --exclude='node_modules' --exclude='.git' --exclude='__pycache__' --exclude='.venv' --exclude='venv' --exclude='.next' ./my-app | $SSH_CMD "vd push <app-name> --json"
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

### `vd rollback <app-name> [--restore-db]`
Revert to previous deployment. Last 5 backups kept. If the app has a vd-managed database, **ask the user if they want to also restore the database** — if yes, add `--restore-db`. This restores the database to the state at the time of the previous deploy. Without this flag, only the container is rolled back.

### `vd destroy <app-name> --yes [--drop-db]`
Stop and remove app. `--drop-db` also drops the database and user. Database is automatically backed up before dropping.

### `vd cron-set <app-name> --schedule "..." --command "..."`
Add a scheduled task. Runs inside the container.

### `vd cron-rm <app-name>`
Remove all cron jobs for an app.

### `vd cron-ls [--app <name>]`
List cron jobs.

### `vd db-backup <app-name>`
Backup an app's database. Only works for apps with `--db postgres`. Keeps last 7 backups per app.

### `vd db-backups <app-name>`
List available database backups for an app.

### `vd db-restore <app-name> [--file <backup-path>]`
Restore an app's database from backup. Restores the latest backup by default. Only works for apps with `--db postgres`. Databases are also backed up daily (automatic) and before `vd destroy --drop-db`.

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
| `NO_DB` | App has no vd-managed database |
| `RESTORE_FAILED` | Check backup file integrity |

## App Naming Rules

- Lowercase, starts with letter, 2-63 chars, a-z/0-9/hyphens only
