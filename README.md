# vibe-deploy

Deploy vibecoded apps to bare metal servers. One command, zero devops knowledge needed.

## What is this?

A platform for non-programmers who build apps with AI (vibecoding) and need to deploy them somewhere. Gives you HTTP hosting, PostgreSQL, cron jobs, and HTTPS — nothing more, nothing less.

## Install (Claude Code)

```
/plugins marketplace add ismakov-dh/claude-plugins-xaid
/plugins install vibe-deploy@xaid-plugins
```

This adds two skills:
- **`/vibe`** — Load platform constraints before building an app. Claude will only use supported infrastructure.
- **`/deploy`** — Deploy the app to the server via SSH.

## What you get

| Capability | Details |
|-----------|---------|
| HTTP app hosting | Static sites, Node.js, Python, Go — auto-detected |
| PostgreSQL database | Auto-provisioned per app, `DATABASE_URL` injected |
| Prod DB read-only | Dashboards can query existing production data |
| HTTPS | Automatic via wildcard cert |
| Cron jobs | Scheduled tasks inside containers |
| Rollback | Auto-rollback on failed deploy, manual rollback to last 5 versions |

## What you don't get

No Redis, no S3, no background workers, no WebSockets, no file storage, no inter-app networking. All persistent data goes in PostgreSQL.

## Usage

1. Start a Claude Code session
2. Type `/vibe` — Claude now knows the platform constraints
3. Describe what you want: *"build me a dashboard that shows sales data from our reporting_platform database"*
4. Claude builds it within platform limits
5. Type `/deploy` — Claude deploys it via SSH
6. App is live at `https://<name>.apps.platform.xaidos.com`

## Server setup (admin only)

```bash
# Build the CLI
cd vibe-deploy && make build-linux

# Install on server (creates user, TLS, nginx, Traefik, PostgreSQL)
source .env.deploy && \
  AWS_ACCESS_KEY_ID=$AWS_ACCESS_KEY_ID \
  AWS_SECRET_ACCESS_KEY=$AWS_SECRET_ACCESS_KEY \
  ./scripts/deploy.sh root@server apps.example.com

# Connect prod DB for dashboards (optional)
ssh server "vd init --prod-db <postgres-container> --prod-db-user <admin-user>"
```

Give users the SSH key and server IP. They don't need server access — Claude handles everything.

## CLI reference

```bash
vd deploy <dir> --name <n> [--db postgres|prod-ro|none] [--routing subdomain|path]
vd status <name>
vd list
vd logs-snapshot <name> [--lines N]
vd rollback <name>
vd destroy <name> --yes [--drop-db]
vd cron-set <name> --schedule "..." --command "..."
vd cron-rm <name>
vd cron-ls
```

All commands support `--json` for structured output.
