# vibe-deploy Architecture

## Design Decisions

### Why a single bash script?

AI agents execute shell commands. A single `vd` script means:
- No runtime dependencies (no Python, no Node required on the host for the tool itself)
- Every command is one line an agent can run and parse
- Errors go to stderr with `[vd error]` prefix, easy to detect programmatically
- Success output is structured and parseable

### Why not Kubernetes?

This targets single bare metal servers used by non-programmers. Kubernetes adds:
- Massive operational complexity
- Resource overhead (control plane alone uses ~2GB RAM)
- A learning curve that is irrelevant when you have 1 server

Docker Compose gives us container isolation, networking, and restart policies. That is sufficient.

### Network Architecture

```
                    Internet
                       |
                    Nginx (:80/:443)
                       |
            +----------+----------+
            |          |          |
    vd-todo:3000  vd-api:8000  vd-blog:80
            |          |
        [vd-network]   |
            |     [vd-postgres]
            |          |
            +---PostgreSQL:5432
```

Three Docker networks:
- `vd-network`: All app containers join this. Nginx connects to apps via container name.
- `vd-postgres`: Only apps that need PostgreSQL join this.
- `vd-mysql`: Only apps that need MySQL join this.

This means:
- Apps can talk to each other via `<app-name>.internal` on vd-network
- Apps can ONLY reach databases they were explicitly granted access to
- Database containers are invisible to apps that don't need them

### Nginx is on the host, not in Docker

Nginx runs directly on the host (installed via apt). Reasons:
- Simpler SSL cert management with certbot
- Direct access to Docker container network via container names (nginx can resolve them because Docker's embedded DNS works with the bridge network from the host when nginx is in the same network)
- One less container to manage

**Important**: Nginx must be connected to `vd-network` to resolve container names. The `vd init` command handles this. Alternatively, we use `docker inspect` to get container IPs, but DNS resolution via the Docker network is cleaner.

**Correction**: Since nginx runs on the host (not in Docker), it cannot use Docker DNS directly. Instead, we use `proxy_pass` with the container's published port, OR we run a small nginx container inside vd-network that does the proxying. Let me resolve this.

**Resolution**: We do NOT publish ports from app containers to the host. Instead, `vd` configures nginx `proxy_pass` to use `127.0.0.1:<allocated-port>` and publishes that port from the compose file. Each app gets a unique host port. This is the simplest approach.

**Wait, that leaks ports on the host.** Better approach: Run nginx inside Docker on vd-network, OR use Docker's internal IP. Let me think about what's actually simplest.

**Final decision**: Run a single nginx container on vd-network that handles all routing. App containers do NOT publish ports. The nginx container publishes 80 and 443. Config files are mounted from the host. This gives us:
- No port allocation headaches
- No host port leakage
- Container DNS resolution works
- Certbot can still work (run on host, certs mounted into nginx container)

See the `infrastructure compose` section below.

### Revised Network Architecture

```
                    Internet
                       |
              Nginx container (:80/:443)
              [on vd-network]
                       |
            +----------+----------+
            |          |          |
    vd-todo:3000  vd-api:8000  vd-blog:80
    [vd-network]  [vd-network]  [vd-network]
                  [vd-postgres]
                       |
                  PostgreSQL:5432
                  [vd-postgres]
```

## Component Details

### 1. App Containerization

Auto-detection priority:
1. `.vd-type` file (explicit override)
2. `Dockerfile` (user provided, used as-is)
3. File-based heuristics (package.json, requirements.txt, go.mod, index.html)

Each Dockerfile template:
- Uses multi-stage builds where appropriate (Go, static sites with build steps)
- Includes a HEALTHCHECK instruction
- Has a clear CMD that auto-detects entrypoints
- Prints helpful error messages on failure

### 2. Database Access Control

Per-app database users with least-privilege:
- Username: `vd_<app-name>`
- Password: random 24-char, stored in app's .env
- Grants: either full (rw) or SELECT-only (ro) on a specific database
- Database name defaults to app name but is configurable

The app receives `DATABASE_URL` in its environment. It never sees the root password.

### 3. Cron Jobs

Implemented via the host's crontab. Each cron entry:
- Runs `docker exec` into the app's container
- Has a unique comment tag (`# vd-cron-<app-name>`) for identification
- Logs output to `/opt/vibe-deploy/logs/<app-name>/cron.log`

This is simpler than running cron inside containers (which requires init systems).

### 4. Rollback

Before every deploy, the system:
1. Saves the current Docker image as a tarball
2. Copies compose and env files
3. Copies nginx config
4. Stores all in a timestamped backup directory

Rollback restores from the most recent backup. Last 5 backups are kept.

### 5. Logging

- Container logs: Docker's json-file driver with 10MB rotation (3 files)
- Cron logs: written to disk at `/opt/vibe-deploy/logs/<app-name>/cron.log`
- Access via `vd logs <name>` (streaming) or `vd logs-snapshot <name>` (one-shot)

## Open Questions

1. **Inter-app communication**: Apps can reach each other via `<name>.internal` on vd-network. Should we restrict this? For now, all apps on vd-network can talk to each other. True isolation would require per-app networks, but that complicates DB access.

2. **Resource limits**: Should we enforce CPU/memory limits per container? Probably yes for production. Not implemented yet -- add `deploy_resources` in compose template.

3. **Multi-server**: This design is single-server. Scaling to multiple servers would require a load balancer in front and shared storage for state. Out of scope for v0.1.

4. **Secrets rotation**: DATABASE_URL passwords are generated once and stored in .env. There is no automatic rotation. For vibecoded apps this is probably fine.
