# vibe-deploy Security & Isolation Model

**Date**: 2026-03-31
**Status**: Design proposal
**Risk Level**: CRITICAL -- untrusted code from non-programmers on shared infrastructure

---

## Threat Model Summary

### What we are defending

| Asset | Classification | Impact if compromised |
|-------|---------------|----------------------|
| Other tenants' apps and data | Critical | Full breach of unrelated customer |
| Shared database(s) | Critical | Data exfiltration, corruption, ransomware |
| Host operating system | Critical | Total platform compromise |
| API keys and secrets | High | Financial loss, upstream service abuse |
| Platform control plane | Critical | Attacker deploys/modifies any app |
| Network infrastructure | High | Lateral movement, MITM |

### Who/what are the adversaries

1. **Malicious user** -- deliberately deploys a cryptominer, port scanner, or data exfiltration tool
2. **Incompetent vibecoded app** -- SQL injection, path traversal, open redirects, leaked secrets in client bundles (this is the *most likely* threat)
3. **Compromised dependency** -- supply chain attack via npm/pip package in a user's app
4. **Noisy neighbor** -- app that accidentally consumes all CPU/RAM/disk and DoS-es the server
5. **Attacker exploiting a user's app** -- XSS/SQLi in a deployed app used as a pivot to attack infrastructure

### Fundamental security principle

**The platform must assume every deployed app is hostile.** Security cannot depend on user behavior, code quality, or good intentions. Every control must be enforced by infrastructure that the user cannot modify or bypass.

---

## 1. Container Isolation

### 1.1 Docker Runtime Security Profile

Every app container MUST run with the following `docker run` flags (or equivalent in Compose):

```yaml
# docker-compose per-app service template
services:
  app-{user}-{name}:
    # --- ISOLATION ---
    read_only: true                    # Read-only root filesystem
    tmpfs:
      - /tmp:size=64M,noexec,nosuid   # Writable tmp, capped, no executables
      - /var/tmp:size=32M,noexec,nosuid
    security_opt:
      - no-new-privileges:true         # Prevent privilege escalation via setuid
      - seccomp=seccomp-vibe.json      # Restrictive seccomp profile (see below)
      - apparmor=vibe-deploy-app       # AppArmor confinement
    cap_drop:
      - ALL                            # Drop ALL Linux capabilities
    cap_add: []                        # Add back NOTHING -- apps need zero capabilities
    privileged: false                  # NEVER. Redundant with cap_drop but explicit.
    user: "65534:65534"                # Run as nobody:nogroup -- never root

    # --- RESOURCE LIMITS ---
    deploy:
      resources:
        limits:
          cpus: "1.0"                  # Max 1 CPU core (adjust per tier)
          memory: 512M                 # Hard memory limit -- OOM killed above this
          pids: 256                    # Prevent fork bombs
        reservations:
          cpus: "0.1"                  # Guaranteed minimum
          memory: 64M

    # --- STORAGE ---
    storage_opt:
      size: 1G                         # Container layer disk limit (requires overlay2 + xfs)

    # --- NETWORKING ---
    networks:
      - app-{user}-{name}-net          # Isolated per-app network (see Section 3)
    dns:
      - 10.0.0.2                       # Internal DNS only -- no arbitrary DNS resolution

    # --- NO DANGEROUS MOUNTS ---
    # NEVER mount: /var/run/docker.sock, /proc, /sys, /dev, host paths
    volumes: []                         # No host volume mounts -- ever

    # --- LOGGING ---
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
        tag: "app-{{.Name}}"

    # --- HEALTH CHECK ---
    healthcheck:
      test: ["CMD", "wget", "--spider", "--quiet", "http://localhost:${APP_PORT}/"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 30s

    # --- RESTART POLICY ---
    restart: unless-stopped

    # --- LABELS FOR MANAGEMENT ---
    labels:
      vibe.user: "${USER_ID}"
      vibe.app: "${APP_NAME}"
      vibe.deployed: "${DEPLOY_TIMESTAMP}"
      vibe.tier: "${TIER}"             # free, pro, etc. -- determines resource limits
```

### 1.2 Custom Seccomp Profile

The default Docker seccomp profile allows ~300 syscalls. We need a restrictive one. Save as `/etc/vibe-deploy/seccomp-vibe.json`:

```json
{
  "defaultAction": "SCMP_ACT_ERRNO",
  "defaultErrnoRet": 1,
  "architectures": ["SCMP_ARCH_X86_64", "SCMP_ARCH_AARCH64"],
  "syscalls": [
    {
      "names": [
        "accept", "accept4", "access", "arch_prctl", "bind", "brk",
        "clock_getres", "clock_gettime", "clock_nanosleep", "clone",
        "close", "connect", "dup", "dup2", "dup3", "epoll_create",
        "epoll_create1", "epoll_ctl", "epoll_pwait", "epoll_wait",
        "eventfd", "eventfd2", "exit", "exit_group", "faccessat",
        "faccessat2", "fadvise64", "fallocate", "fchmod", "fchmodat",
        "fchown", "fchownat", "fcntl", "fdatasync", "flock",
        "fstat", "fstatfs", "fsync", "ftruncate", "futex",
        "getcwd", "getdents", "getdents64", "getegid", "geteuid",
        "getgid", "getgroups", "getpeername", "getpgrp", "getpid",
        "getppid", "getrandom", "getresgid", "getresuid",
        "getrlimit", "getsockname", "getsockopt", "gettid",
        "gettimeofday", "getuid", "ioctl", "kill", "listen",
        "lseek", "lstat", "madvise", "memfd_create", "mincore",
        "mkdir", "mkdirat", "mmap", "mprotect", "mremap",
        "munmap", "nanosleep", "newfstatat", "open", "openat",
        "pipe", "pipe2", "poll", "ppoll", "pread64", "preadv",
        "prlimit64", "pselect6", "pwrite64", "pwritev", "read",
        "readlink", "readlinkat", "readv", "recvfrom", "recvmsg",
        "rename", "renameat", "renameat2", "rmdir", "rt_sigaction",
        "rt_sigprocmask", "rt_sigreturn", "sched_getaffinity",
        "sched_yield", "select", "sendfile", "sendmsg", "sendto",
        "set_robust_list", "set_tid_address", "setitimer",
        "setsockopt", "shutdown", "sigaltstack", "socket",
        "socketpair", "stat", "statfs", "statx", "symlink",
        "symlinkat", "tgkill", "umask", "uname", "unlink",
        "unlinkat", "utimensat", "wait4", "waitid", "write",
        "writev"
      ],
      "action": "SCMP_ACT_ALLOW"
    }
  ]
}
```

This explicitly allows only the syscalls a typical web app needs. Notably blocked: `ptrace` (no debugging/tracing other processes), `mount`/`umount`, `reboot`, `swapon`, `kexec_load`, `init_module`, `bpf`, `userfaultfd`.

### 1.3 AppArmor Profile

Install at `/etc/apparmor.d/vibe-deploy-app`:

```
#include <tunables/global>

profile vibe-deploy-app flags=(attach_disconnected,mediate_deleted) {
  #include <abstractions/base>
  #include <abstractions/nameservice>

  # Deny all network raw/packet (no ping, no port scanning)
  deny network raw,
  deny network packet,

  # Allow TCP/UDP only (needed for HTTP serving and DB connections)
  network inet stream,
  network inet dgram,
  network inet6 stream,
  network inet6 dgram,

  # Deny access to sensitive host paths
  deny /proc/*/mem rw,
  deny /proc/sysrq-trigger rw,
  deny /proc/kcore r,
  deny /sys/** w,
  deny /dev/** rw,

  # Allow /tmp and /var/tmp (tmpfs-mounted with noexec)
  /tmp/** rw,
  /var/tmp/** rw,

  # Allow read access to app files
  /app/** r,
  /app/node_modules/.cache/** rw,

  # Deny writes to sensitive locations
  deny /etc/shadow rw,
  deny /etc/passwd w,
  deny /root/** rw,
}
```

### 1.4 What this prevents

| Attack | Blocked by |
|--------|-----------|
| Fork bomb | `pids: 256` limit |
| Cryptominer / CPU abuse | `cpus: 1.0` limit |
| Memory exhaustion DoS | `memory: 512M` hard limit, OOM killer |
| Disk filling | `storage_opt: size: 1G`, tmpfs size caps |
| Container escape via Docker socket | No volume mounts allowed |
| Privilege escalation via setuid | `no-new-privileges`, `cap_drop: ALL`, `user: 65534` |
| Host filesystem access | `read_only: true`, no volume mounts |
| Kernel exploitation | seccomp blocks dangerous syscalls, AppArmor confines filesystem |
| Running a port scanner | AppArmor denies raw/packet network |
| Process debugging/injection | seccomp blocks `ptrace` |

---

## 2. Database Access

This is the hardest problem. Vibecoded apps routinely generate SQL injection vulnerabilities. The database architecture must limit blast radius to the individual app even when SQLi is present.

### 2.1 Architecture: Per-App Database User + Per-App Schema

```
                    +-----------------+
                    |  PostgreSQL     |
                    |  (shared host)  |
                    +-----------------+
                    |                 |
          +---------+    +---------+ |
          | Schema:  |   | Schema:  | |
          | app_abc  |   | app_xyz  | |
          | Owner:   |   | Owner:   | |
          | u_abc    |   | u_xyz    | |
          +----------+   +----------+ |
                                      |
                    No cross-schema   |
                    access possible   |
                    +-----------------+
```

### 2.2 Per-App Database Provisioning

When an app is deployed that requires a database, the platform runs (as superuser, not exposed to the app):

```sql
-- 1. Create a dedicated role for this app
CREATE ROLE app_u_abc123 WITH
  LOGIN
  PASSWORD 'generated-random-64-char'
  NOSUPERUSER
  NOCREATEDB
  NOCREATEROLE
  NOREPLICATION
  CONNECTION LIMIT 10              -- Max 10 simultaneous connections
  VALID UNTIL '2026-04-30';        -- Credential expiry -- forces rotation

-- 2. Create a dedicated schema
CREATE SCHEMA app_abc123 AUTHORIZATION app_u_abc123;

-- 3. Lock the role into its own schema -- cannot see other schemas
REVOKE ALL ON SCHEMA public FROM app_u_abc123;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM app_u_abc123;
ALTER ROLE app_u_abc123 SET search_path = 'app_abc123';

-- 4. Restrict to DML only -- no DDL in production
-- (DDL is run by the platform during migrations, not by the app)
GRANT USAGE ON SCHEMA app_abc123 TO app_u_abc123;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA app_abc123 TO app_u_abc123;
ALTER DEFAULT PRIVILEGES IN SCHEMA app_abc123
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_u_abc123;

-- 5. Grant sequence usage (needed for auto-increment PKs)
GRANT USAGE ON ALL SEQUENCES IN SCHEMA app_abc123 TO app_u_abc123;
ALTER DEFAULT PRIVILEGES IN SCHEMA app_abc123
  GRANT USAGE ON SEQUENCES TO app_u_abc123;

-- 6. Statement timeout -- kill runaway queries
ALTER ROLE app_u_abc123 SET statement_timeout = '30s';

-- 7. Row limit on results (prevent accidental full table dumps)
ALTER ROLE app_u_abc123 SET work_mem = '16MB';

-- 8. Restrict to connecting only from the app's container network
-- (Handled via pg_hba.conf -- see below)
```

### 2.3 pg_hba.conf: Network-Level Access Control

```
# TYPE   DATABASE        USER             ADDRESS            METHOD
# Platform admin -- local socket only
local    all             vibe_admin                          scram-sha-256

# Per-app users -- only from their specific Docker network subnet
host     vibedb          app_u_abc123     172.20.1.0/24      scram-sha-256
host     vibedb          app_u_xyz789     172.20.2.0/24      scram-sha-256

# Deny everything else
host     all             all              0.0.0.0/0          reject
```

### 2.4 Connection Pooling via PgBouncer

Apps should NOT connect directly to PostgreSQL. A PgBouncer instance sits between apps and the database:

```ini
; /etc/pgbouncer/pgbouncer.ini
[databases]
; Each app gets its own pool entry pointing to its schema
app_abc123 = host=127.0.0.1 port=5432 dbname=vibedb user=app_u_abc123 pool_size=5
app_xyz789 = host=127.0.0.1 port=5432 dbname=vibedb user=app_u_xyz789 pool_size=5

[pgbouncer]
listen_addr = 172.20.0.2      ; Only on internal Docker network
listen_port = 6432
auth_type = scram-sha-256
auth_file = /etc/pgbouncer/userlist.txt

; Connection limits
default_pool_size = 5           ; Per-app pool
max_client_conn = 200           ; Total across all apps
max_db_connections = 20         ; Per-database limit
reserve_pool_size = 2
reserve_pool_timeout = 3

; Timeouts
query_timeout = 30              ; Kill queries over 30s
client_idle_timeout = 300       ; Disconnect idle clients after 5min
server_idle_timeout = 60

; Security
disable_pqexec = 1             ; Disable multi-statement queries (blocks some SQLi)
```

### 2.5 What the app receives

The app's `DATABASE_URL` environment variable points to PgBouncer, not directly to PostgreSQL:

```
DATABASE_URL=postgresql://app_u_abc123:PASSWORD@db-proxy:6432/app_abc123
```

The app has zero knowledge of other tenants, other schemas, or the real database topology.

### 2.6 Read-Only Mode by Default

For apps that only need to read data (dashboards, reports), provision with:

```sql
GRANT SELECT ON ALL TABLES IN SCHEMA app_abc123 TO app_u_abc123;
-- No INSERT, UPDATE, DELETE
```

The deployment agent should default to read-only and only grant write access when the app clearly needs it (e.g., user registration, form submissions). This limits SQLi impact significantly: an attacker who finds injection in a read-only app cannot `DROP TABLE` or `INSERT` malicious data.

### 2.7 What this prevents

| Attack | Blocked by |
|--------|-----------|
| SQL injection reading other tenants' data | Per-app schema, `REVOKE` on public schema |
| SQL injection dropping tables | App role has no DDL privileges |
| Runaway queries DoS-ing the database | `statement_timeout = 30s`, PgBouncer `query_timeout` |
| Connection exhaustion | `CONNECTION LIMIT 10` per role, PgBouncer pool limits |
| Multi-statement injection (`; DROP TABLE`) | PgBouncer `disable_pqexec = 1` |
| Direct database access from the internet | `pg_hba.conf` restricts to Docker subnets only |
| Credential reuse / stale credentials | `VALID UNTIL` forces rotation |

---

## 3. Network Isolation

### 3.1 Network Architecture

```
                        INTERNET
                           |
                     [nginx reverse proxy]
                      172.18.0.0/16 (proxy-net)
                           |
              +------------+------------+
              |            |            |
         [app-abc]    [app-xyz]    [app-def]
         172.20.1/24  172.20.2/24  172.20.3/24
         (isolated)   (isolated)   (isolated)
              |            |            |
              +------------+------------+
                           |
                     [db-proxy / PgBouncer]
                      172.19.0.0/16 (db-net)
                           |
                     [PostgreSQL]
```

### 3.2 Docker Network Setup

```bash
#!/bin/bash
# Platform infrastructure networks -- created once at server setup

# Proxy network: nginx <-> app containers
docker network create \
  --driver bridge \
  --subnet 172.18.0.0/16 \
  --opt com.docker.network.bridge.name=br-proxy \
  --opt com.docker.network.bridge.enable_icc=false \
  proxy-net

# Database network: PgBouncer <-> PostgreSQL
docker network create \
  --driver bridge \
  --subnet 172.19.0.0/16 \
  --internal \
  --opt com.docker.network.bridge.enable_icc=false \
  db-net
```

For each deployed app:

```bash
# Per-app isolated network -- app can reach proxy and db-proxy, nothing else
docker network create \
  --driver bridge \
  --subnet "172.20.${APP_SUBNET}.0/24" \
  --internal \
  --opt com.docker.network.bridge.enable_icc=false \
  "app-${APP_ID}-net"
```

Key settings:
- **`enable_icc=false`**: Disables inter-container communication on the same bridge. App A cannot reach App B even if they share a network.
- **`--internal`**: No outbound internet access from app networks. Apps cannot call home, exfiltrate data, or download payloads at runtime.

### 3.3 Container Network Attachment

```bash
# App container connects to:
# 1. Its own isolated network (where it listens for HTTP)
# 2. proxy-net (so nginx can reach it)
# 3. db-net (only if the app needs database access)

docker run ... \
  --network "app-${APP_ID}-net" \
  "app-${APP_ID}"

# Attach to proxy-net so nginx can route to it
docker network connect proxy-net "app-${APP_ID}"

# Attach to db-net ONLY if the app has database access approved
if [ "$APP_NEEDS_DB" = "true" ]; then
  docker network connect db-net "app-${APP_ID}"
fi
```

### 3.4 iptables Hardening (Host Level)

Even with Docker networks, add host-level iptables rules as defense-in-depth:

```bash
# Block containers from reaching the host's metadata service (cloud providers)
iptables -I DOCKER-USER -d 169.254.169.254 -j DROP

# Block containers from reaching the Docker daemon API
iptables -I DOCKER-USER -d 127.0.0.1 -p tcp --dport 2375 -j DROP
iptables -I DOCKER-USER -d 127.0.0.1 -p tcp --dport 2376 -j DROP

# Block containers from reaching SSH on the host
iptables -I DOCKER-USER -d 172.17.0.1 -p tcp --dport 22 -j DROP
```

### 3.5 Outbound Internet Access: Default Deny with Explicit Allow

Most vibecoded apps do NOT need outbound internet access at runtime. The default is **no outbound access** (via `--internal` networks).

For apps that legitimately need to call external APIs (e.g., Stripe, OpenAI):

```bash
# Create a controlled egress proxy (Squid or similar)
# Apps that need outbound access go through this proxy
# The proxy has an allowlist of permitted domains

# squid.conf excerpt:
acl allowed_domains dstdomain .stripe.com
acl allowed_domains dstdomain .openai.com
acl allowed_domains dstdomain .googleapis.com

http_access allow allowed_domains
http_access deny all
```

The app receives `HTTP_PROXY=http://egress-proxy:3128` and the proxy enforces the domain allowlist. This prevents:
- Data exfiltration to attacker-controlled servers
- Reverse shell connections
- Cryptominer pool connections
- SSRF attacks against internal infrastructure

### 3.6 What this prevents

| Attack | Blocked by |
|--------|-----------|
| App A attacking App B | `enable_icc=false`, separate bridge networks |
| Reverse shell / C2 callback | `--internal` network, no outbound internet |
| SSRF against cloud metadata | iptables drop rule for 169.254.169.254 |
| Port scanning internal network | AppArmor denies raw sockets, network isolation |
| Container reaching Docker daemon | iptables block, no socket mount |
| Data exfiltration | No outbound by default, egress proxy with allowlist |

---

## 4. Secrets Management

### 4.1 What Goes Where

| Secret type | Storage | NOT here |
|-------------|---------|----------|
| Database credentials | Docker secrets (swarm) or mounted tmpfs files | Environment variables, Dockerfiles, image layers |
| API keys (user-provided) | Encrypted at rest in platform DB, injected as Docker secrets | .env files in repos, build args, image labels |
| TLS certificates | Host filesystem, nginx reads directly | Container volume mounts |
| Platform admin credentials | HashiCorp Vault or SOPS-encrypted | Any file in any repo, env vars |

### 4.2 Docker Secrets Implementation

If using Docker Swarm mode (recommended even for single-node):

```bash
# Create a secret for an app's database credentials
echo "postgresql://app_u_abc123:${RANDOM_PW}@db-proxy:6432/app_abc123" | \
  docker secret create "app-abc123-db-url" -

# In the service definition:
# The secret appears as a file at /run/secrets/db-url inside the container
services:
  app-abc123:
    secrets:
      - source: app-abc123-db-url
        target: db-url
        uid: "65534"
        gid: "65534"
        mode: 0400            # Read-only, only by the app user
```

If NOT using Swarm (plain Docker Compose):

```bash
# Mount secrets via tmpfs -- never as bind mounts from host filesystem
# The deployment tool writes the secret to a tmpfs and bind-mounts it read-only
mkdir -p /run/vibe-secrets/app-abc123
echo "postgresql://..." > /run/vibe-secrets/app-abc123/db-url
chmod 400 /run/vibe-secrets/app-abc123/db-url

# In docker-compose:
volumes:
  - type: tmpfs
    target: /run/secrets
    tmpfs:
      size: 1M
      mode: 0700
```

### 4.3 Secret Injection at Runtime

The deployment agent performs this process:

1. User declares "I need a database" or "I have a Stripe API key"
2. Platform generates database credentials (user never sees the raw password)
3. User-provided API keys are encrypted with a platform KMS key and stored
4. At deploy time, secrets are decrypted and injected as Docker secrets
5. The app reads secrets from `/run/secrets/` files, NOT from environment variables
6. On redeployment, old secrets are rotated and new ones issued

### 4.4 Why NOT environment variables

Environment variables are the most common and most dangerous way to handle secrets:

- **Leaked in crash dumps**: Most runtimes dump env vars on crash
- **Visible in `/proc`**: `cat /proc/1/environ` inside the container exposes all env vars
- **Logged by frameworks**: Many frameworks log all env vars at startup (Express, Rails, Django debug mode)
- **Inherited by child processes**: Every subprocess inherits all env vars
- **Visible in `docker inspect`**: Anyone with Docker API access can read them

Docker secrets (files) are none of these things. They exist only in memory (tmpfs), are readable only by the designated user, and are not inherited by child processes.

### 4.5 Secret Rotation

```bash
# Automated monthly rotation for database credentials
# 1. Generate new password
NEW_PW=$(openssl rand -base64 48)

# 2. Update PostgreSQL role
psql -c "ALTER ROLE app_u_abc123 WITH PASSWORD '${NEW_PW}';"

# 3. Update PgBouncer auth file
# 4. Update Docker secret
# 5. Rolling restart of the app container (zero downtime)
docker service update --secret-rm app-abc123-db-url \
  --secret-add source=app-abc123-db-url-v2,target=db-url \
  app-abc123
```

### 4.6 Scanning for Leaked Secrets

The deployment pipeline scans every app before building:

```bash
# Run gitleaks on the app source before building
gitleaks detect --source /path/to/app --no-git --report-format json

# Also scan the built image layers
docker save app-abc123:latest | gitleaks detect --pipe

# Common patterns to flag:
# - Hardcoded AWS keys (AKIA...)
# - Hardcoded database URLs with passwords
# - Private keys (-----BEGIN RSA PRIVATE KEY-----)
# - .env files with real credentials baked into the image
```

If secrets are detected, the deployment is **blocked** and the user is notified with specific remediation instructions.

---

## 5. Resource Abuse Prevention

### 5.1 Multi-Layer Resource Limits

```
Layer 1: Docker cgroup limits      (hard ceiling per container)
Layer 2: PgBouncer connection pool  (database connection limits)
Layer 3: Nginx rate limiting        (request rate per app)
Layer 4: Disk quotas               (filesystem usage limits)
Layer 5: Process monitoring         (anomaly detection and auto-kill)
```

### 5.2 Docker Resource Limits (cgroups v2)

```yaml
# Per-tier limits
tiers:
  free:
    cpus: "0.5"
    memory: "256M"
    pids: 128
    disk: "512M"
    db_connections: 3
    requests_per_minute: 60

  pro:
    cpus: "1.0"
    memory: "512M"
    pids: 256
    disk: "1G"
    db_connections: 10
    requests_per_minute: 300

  business:
    cpus: "2.0"
    memory: "1G"
    pids: 512
    disk: "5G"
    db_connections: 20
    requests_per_minute: 1000
```

### 5.3 What Happens When an App Goes Rogue

| Condition | Detection | Response | Recovery |
|-----------|-----------|----------|----------|
| Memory exceeds limit | cgroup OOM killer | Container killed immediately | Auto-restart with same limits |
| CPU maxed for >10 min | Monitoring alert | Throttled by cgroup (not killed) | Alert sent to platform admin |
| Disk full | `storage_opt` enforcement | Writes fail with ENOSPC | User notified, must reduce usage |
| Fork bomb | `pids: 256` limit | New process creation fails | Container continues, no new forks |
| DB connection flood | PgBouncer pool limit | Connections queued then rejected | Automatic -- pool manages it |
| Request flood (self or attack) | Nginx rate limit | 429 Too Many Requests | Automatic recovery |
| Container restart loop | Docker restart policy + monitoring | Stopped after 5 restarts in 5 min | User notified, manual redeploy |
| Outbound network abuse | Egress proxy / `--internal` | Connections blocked | Automatic -- no outbound by default |

### 5.4 Anti-Restart-Loop

```yaml
# In Docker daemon config /etc/docker/daemon.json
{
  "default-runtime": "runc",
  "live-restore": true,
  "max-concurrent-downloads": 3,
  "max-concurrent-uploads": 2,
  "log-driver": "json-file",
  "log-opts": {
    "max-size": "10m",
    "max-file": "3"
  }
}
```

The deployment agent tracks restart counts:

```bash
# If a container restarts more than 5 times in 5 minutes, stop it
RESTARTS=$(docker inspect --format='{{.RestartCount}}' "app-${APP_ID}")
if [ "$RESTARTS" -gt 5 ]; then
  docker stop "app-${APP_ID}"
  notify_user "${USER_ID}" "Your app ${APP_NAME} has been stopped due to repeated crashes. Check your logs."
fi
```

---

## 6. Nginx Security

### 6.1 Main Nginx Configuration

```nginx
# /etc/nginx/nginx.conf

user nginx;
worker_processes auto;
pid /run/nginx.pid;

events {
    worker_connections 2048;
    multi_accept on;
}

http {
    # --- BASIC SECURITY ---
    server_tokens off;                    # Hide nginx version
    more_clear_headers Server;            # Remove Server header entirely

    # --- TIMEOUTS ---
    client_body_timeout 10s;              # Slow loris protection
    client_header_timeout 10s;
    send_timeout 10s;
    keepalive_timeout 30s;

    # --- SIZE LIMITS ---
    client_max_body_size 10m;             # Max upload size
    client_body_buffer_size 128k;
    client_header_buffer_size 1k;
    large_client_header_buffers 4 4k;

    # --- RATE LIMITING ZONES ---
    # Per-IP global rate limit
    limit_req_zone $binary_remote_addr zone=global_per_ip:10m rate=30r/s;

    # Per-app rate limit (keyed on server_name + remote_addr)
    limit_req_zone $binary_remote_addr$server_name zone=per_app:10m rate=10r/s;

    # Connection limiting
    limit_conn_zone $binary_remote_addr zone=conn_per_ip:10m;

    # Rate limit response
    limit_req_status 429;
    limit_conn_status 429;

    # --- LOGGING ---
    log_format security '$remote_addr - $remote_user [$time_local] '
                        '"$request" $status $body_bytes_sent '
                        '"$http_referer" "$http_user_agent" '
                        '$request_time $upstream_response_time '
                        '$server_name';

    access_log /var/log/nginx/access.log security;
    error_log /var/log/nginx/error.log warn;

    # --- INCLUDES ---
    include /etc/nginx/conf.d/*.conf;
    include /etc/nginx/sites-enabled/*;
}
```

### 6.2 Per-App Server Block (Auto-Generated by Deployment Agent)

```nginx
# /etc/nginx/sites-enabled/app-abc123.conf
# Auto-generated by vibe-deploy -- do not edit manually

upstream app_abc123_backend {
    server 172.20.1.2:3000;     # App container's internal IP and port
    keepalive 8;
}

server {
    listen 443 ssl http2;
    server_name abc123.vibeapps.dev;

    # --- TLS ---
    ssl_certificate     /etc/letsencrypt/live/abc123.vibeapps.dev/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/abc123.vibeapps.dev/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers on;
    ssl_session_timeout 1d;
    ssl_session_cache shared:SSL:10m;
    ssl_session_tickets off;
    ssl_stapling on;
    ssl_stapling_verify on;

    # --- SECURITY HEADERS ---
    add_header Strict-Transport-Security "max-age=63072000; includeSubDomains; preload" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "DENY" always;
    add_header X-XSS-Protection "0" always;             # Deprecated -- CSP replaces this
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;
    add_header Permissions-Policy "camera=(), microphone=(), geolocation=(), payment=()" always;

    # Content-Security-Policy -- restrictive default, apps can request relaxation
    add_header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; font-src 'self' https://fonts.gstatic.com; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self';" always;

    # --- RATE LIMITING ---
    limit_req zone=per_app burst=20 nodelay;
    limit_conn conn_per_ip 20;

    # --- REQUEST FILTERING ---
    # Block common attack patterns
    if ($request_uri ~* "(\.\.\/|\.\.\\|%2e%2e|%252e%252e)") {
        return 403;
    }

    # Block access to hidden files
    location ~ /\. {
        deny all;
        return 404;
    }

    # Block common sensitive paths
    location ~* ^/(\.env|\.git|wp-admin|wp-login|phpinfo|phpmyadmin|adminer) {
        deny all;
        return 404;
    }

    # --- PROXY TO APP ---
    location / {
        proxy_pass http://app_abc123_backend;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";

        # Timeouts
        proxy_connect_timeout 5s;
        proxy_send_timeout 30s;
        proxy_read_timeout 30s;

        # Buffer limits
        proxy_buffer_size 4k;
        proxy_buffers 8 4k;
        proxy_busy_buffers_size 8k;

        # Hide upstream headers that leak info
        proxy_hide_header X-Powered-By;
        proxy_hide_header Server;
    }

    # --- ERROR PAGES ---
    error_page 502 503 504 /50x.html;
    location = /50x.html {
        root /usr/share/nginx/html;
        internal;
    }
}

# Redirect HTTP to HTTPS
server {
    listen 80;
    server_name abc123.vibeapps.dev;
    return 301 https://$host$request_uri;
}
```

### 6.3 What this prevents

| Attack | Blocked by |
|--------|-----------|
| Slow loris DoS | `client_body_timeout`, `client_header_timeout` |
| Large payload DoS | `client_max_body_size 10m` |
| Rate-based DoS | `limit_req` and `limit_conn` zones |
| Path traversal | Request URI regex filter |
| Version fingerprinting | `server_tokens off`, header removal |
| Clickjacking | `X-Frame-Options: DENY` |
| MIME sniffing | `X-Content-Type-Options: nosniff` |
| Missing HTTPS | HSTS header, HTTP->HTTPS redirect |
| XSS via script injection | Content-Security-Policy |
| Sensitive file exposure | Deny rules for `.env`, `.git`, etc. |
| Protocol downgrade | TLS 1.2+ only, strong ciphers |

---

## 7. Deployment Guardrails

The deployment agent is the primary security gate. It must validate every deployment before building.

### 7.1 Dockerfile Validation (Pre-Build)

The deployment agent MUST reject Dockerfiles containing:

```python
# deployment_validator.py -- run before every build

import re
from pathlib import Path
from dataclasses import dataclass

@dataclass
class ValidationResult:
    passed: bool
    errors: list[str]
    warnings: list[str]

BLOCKED_INSTRUCTIONS = {
    # Pattern: (regex, reason, severity)
    r"^\s*FROM\s+.*:latest\b": (
        "Using :latest tag -- pin to a specific version for reproducibility",
        "warning",
    ),
    r"^\s*USER\s+root\b": (
        "Running as root is forbidden -- the platform enforces user 65534",
        "error",
    ),
    r"^\s*VOLUME\s": (
        "VOLUME instructions are forbidden -- the platform manages storage",
        "error",
    ),
    r"^\s*EXPOSE\s+(?!(?:3000|8080|8000|5000)\b)\d+": (
        "Only ports 3000, 8080, 8000, 5000 are allowed -- standard web app ports",
        "error",
    ),
    r"--privileged": (
        "Privileged mode is forbidden",
        "error",
    ),
    r"--cap-add": (
        "Adding Linux capabilities is forbidden",
        "error",
    ),
    r"--security-opt\s+seccomp[=:]unconfined": (
        "Disabling seccomp is forbidden",
        "error",
    ),
    r"--pid\s*=\s*host": (
        "Host PID namespace is forbidden",
        "error",
    ),
    r"--network\s*=\s*host": (
        "Host network mode is forbidden",
        "error",
    ),
    r"docker\.sock": (
        "Mounting Docker socket is forbidden -- this allows full host compromise",
        "error",
    ),
    r"(curl|wget).*\|\s*(sh|bash)": (
        "Piping remote scripts to shell is forbidden -- supply chain attack risk",
        "error",
    ),
    r"(ssh-keygen|ssh-agent|sshd)": (
        "SSH utilities are forbidden -- no remote access into containers",
        "error",
    ),
    r"(nc\s|ncat|netcat|nmap|masscan)": (
        "Network scanning/tunneling tools are forbidden",
        "error",
    ),
    r"(passwd|shadow|sudoers)": (
        "Modifying authentication files is forbidden",
        "error",
    ),
    r"ADD\s+https?://": (
        "ADD with remote URLs is forbidden -- use COPY with pre-fetched files",
        "error",
    ),
}

def validate_dockerfile(dockerfile_path: str) -> ValidationResult:
    errors = []
    warnings = []
    content = Path(dockerfile_path).read_text()

    for pattern, (reason, severity) in BLOCKED_INSTRUCTIONS.items():
        if re.search(pattern, content, re.MULTILINE | re.IGNORECASE):
            if severity == "error":
                errors.append(reason)
            else:
                warnings.append(reason)

    # Check for multi-stage builds leaking secrets
    stages = re.findall(r"FROM\s+\S+\s+AS\s+(\w+)", content, re.IGNORECASE)
    if not stages and "ARG" in content and "TOKEN" in content.upper():
        warnings.append(
            "Build args containing tokens may be cached in image layers -- "
            "use multi-stage builds to avoid leaking secrets"
        )

    # Ensure there is a non-root USER instruction
    if not re.search(r"^\s*USER\s+(?!root)\S+", content, re.MULTILINE):
        warnings.append(
            "No non-root USER instruction found -- the platform will force user 65534 "
            "but it's better to set this in the Dockerfile"
        )

    return ValidationResult(
        passed=len(errors) == 0,
        errors=errors,
        warnings=warnings,
    )
```

### 7.2 Source Code Scanning (Pre-Build)

```python
# source_scanner.py -- scan app source for obvious security issues

PATTERNS_TO_FLAG = {
    # Hardcoded secrets
    r"(password|secret|api[_-]?key|token)\s*[=:]\s*['\"][^'\"]{8,}['\"]": (
        "Potential hardcoded secret detected",
        "error",
    ),
    r"AKIA[0-9A-Z]{16}": (
        "AWS access key detected -- remove immediately",
        "error",
    ),
    r"-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----": (
        "Private key detected in source code",
        "error",
    ),

    # Dangerous patterns in Node.js
    r"eval\s*\(": (
        "eval() is dangerous -- potential code injection",
        "warning",
    ),
    r"child_process": (
        "child_process module detected -- potential command injection risk",
        "warning",
    ),
    r"\.exec\s*\([^)]*\$": (
        "Shell command with variable interpolation -- command injection risk",
        "warning",
    ),

    # Dangerous patterns in Python
    r"os\.system\s*\(": (
        "os.system() is dangerous -- use subprocess with shell=False",
        "warning",
    ),
    r"__import__\s*\(": (
        "Dynamic imports detected -- potential code injection",
        "warning",
    ),
    r"pickle\.loads?\s*\(": (
        "pickle.load with untrusted data is a remote code execution vulnerability",
        "warning",
    ),

    # SQL injection indicators
    r"f['\"].*SELECT.*\{": (
        "SQL query with f-string interpolation -- SQL injection vulnerability",
        "warning",
    ),
    r"\".*SELECT.*\"\s*%\s*": (
        "SQL query with string formatting -- SQL injection vulnerability",
        "warning",
    ),
    r"\+\s*['\"].*(?:SELECT|INSERT|UPDATE|DELETE|DROP)": (
        "SQL query built with string concatenation -- SQL injection vulnerability",
        "warning",
    ),
}
```

### 7.3 Image Scanning (Post-Build)

After the Docker image is built, scan it before deploying:

```bash
# Scan with Trivy for known CVEs
trivy image --severity CRITICAL,HIGH --exit-code 1 \
  --ignore-unfixed \
  "app-${APP_ID}:${VERSION}"

# Scan for secrets in image layers
trivy image --scanners secret --exit-code 1 \
  "app-${APP_ID}:${VERSION}"

# Check image size -- abnormally large images may contain unwanted payloads
IMAGE_SIZE=$(docker image inspect "app-${APP_ID}:${VERSION}" --format='{{.Size}}')
MAX_SIZE=$((500 * 1024 * 1024))  # 500MB max
if [ "$IMAGE_SIZE" -gt "$MAX_SIZE" ]; then
  echo "ERROR: Image size ${IMAGE_SIZE} exceeds maximum ${MAX_SIZE}"
  exit 1
fi
```

### 7.4 Runtime Validation

```bash
# After container starts, verify it is running with expected security settings
INSPECT=$(docker inspect "app-${APP_ID}")

# Verify not running as root
USER=$(echo "$INSPECT" | jq -r '.[0].Config.User')
if [ "$USER" = "root" ] || [ "$USER" = "0" ] || [ -z "$USER" ]; then
  docker stop "app-${APP_ID}"
  echo "SECURITY: Container running as root -- stopped immediately"
  exit 1
fi

# Verify no dangerous capabilities
CAPS=$(echo "$INSPECT" | jq -r '.[0].HostConfig.CapAdd // [] | .[]')
if [ -n "$CAPS" ]; then
  docker stop "app-${APP_ID}"
  echo "SECURITY: Container has added capabilities: ${CAPS} -- stopped immediately"
  exit 1
fi

# Verify read-only filesystem
READONLY=$(echo "$INSPECT" | jq -r '.[0].HostConfig.ReadonlyRootfs')
if [ "$READONLY" != "true" ]; then
  docker stop "app-${APP_ID}"
  echo "SECURITY: Container filesystem is not read-only -- stopped immediately"
  exit 1
fi

# Verify privileged mode is off
PRIVILEGED=$(echo "$INSPECT" | jq -r '.[0].HostConfig.Privileged')
if [ "$PRIVILEGED" = "true" ]; then
  docker stop "app-${APP_ID}"
  echo "SECURITY: Container is privileged -- stopped immediately"
  exit 1
fi
```

### 7.5 Deployment Decision Matrix

```
Source scan → HARD BLOCK on errors (secrets, dangerous patterns)
           → WARN on warnings (proceed with logging)

Dockerfile → HARD BLOCK on errors (root, volumes, docker.sock)
           → WARN on warnings (:latest tag, missing USER)

Image scan → HARD BLOCK on CRITICAL CVEs or leaked secrets
           → WARN on HIGH CVEs (deploy but flag for review)

Image size → HARD BLOCK above 500MB
           → WARN above 200MB

Runtime    → HARD KILL if security profile is violated
```

---

## 8. Monitoring & Alerting

### 8.1 What to Monitor

```yaml
# Prometheus metrics to collect per container
metrics:
  resource_usage:
    - container_cpu_usage_seconds_total       # CPU usage trending toward limit
    - container_memory_usage_bytes            # Memory usage trending toward limit
    - container_fs_usage_bytes                # Disk usage
    - container_network_receive_bytes_total   # Unusual inbound traffic
    - container_network_transmit_bytes_total  # Unusual outbound traffic (data exfiltration)
    - container_processes                     # Process count approaching pid limit

  database:
    - pg_stat_activity_count                  # Active connections per app user
    - pg_stat_statements_total_time           # Slow queries per app
    - pg_stat_statements_calls               # Query volume per app
    - pgbouncer_pool_active_connections      # Pool utilization

  nginx:
    - nginx_http_requests_total              # Request rate per app
    - nginx_http_request_duration_seconds    # Latency per app
    - nginx_http_requests_status_4xx         # Error rates (attack indicator)
    - nginx_http_requests_status_5xx         # App health

  security:
    - rate_limit_rejections_total            # Apps being rate limited
    - blocked_requests_total                 # Requests blocked by WAF rules
    - container_restart_count                # Restart loops
    - failed_health_checks                   # Unhealthy containers
```

### 8.2 Alert Rules

```yaml
# Prometheus alerting rules
groups:
  - name: vibe_security_alerts
    rules:
      # Container consuming >90% of memory limit for >5 minutes
      - alert: ContainerMemoryPressure
        expr: |
          (container_memory_usage_bytes / container_spec_memory_limit_bytes) > 0.9
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Container {{ $labels.name }} is near memory limit"

      # Container restarted more than 3 times in 10 minutes
      - alert: ContainerRestartLoop
        expr: |
          increase(container_restart_count[10m]) > 3
        labels:
          severity: critical
        annotations:
          summary: "Container {{ $labels.name }} is in a restart loop"
          action: "Auto-stop container and notify user"

      # Unusual outbound network traffic (potential data exfiltration)
      - alert: UnusualOutboundTraffic
        expr: |
          rate(container_network_transmit_bytes_total[5m]) > 1048576
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "Container {{ $labels.name }} transmitting >1MB/s outbound"
          action: "Investigate -- possible data exfiltration"

      # High rate of 4xx errors (potential attack)
      - alert: HighErrorRate
        expr: |
          rate(nginx_http_requests_status_4xx{app!=""}[5m]) > 10
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "App {{ $labels.app }} receiving high 4xx rate -- possible attack"

      # Database connection exhaustion
      - alert: DatabaseConnectionExhaustion
        expr: |
          pg_stat_activity_count > 80
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Database connections near limit"

      # Slow queries (potential SQLi probing)
      - alert: SlowDatabaseQueries
        expr: |
          pg_stat_statements_mean_time_seconds > 10
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "App {{ $labels.user }} running slow queries -- possible SQLi probing"

      # Disk usage above 80%
      - alert: ContainerDiskPressure
        expr: |
          (container_fs_usage_bytes / container_fs_limit_bytes) > 0.8
        for: 5m
        labels:
          severity: warning
```

### 8.3 Audit Logging

Every security-relevant action must be logged immutably:

```python
# audit_events.py -- structured security audit logging

AUDIT_EVENTS = {
    # Deployment lifecycle
    "deploy.started":       "App deployment initiated",
    "deploy.scan.failed":   "Source/image scan blocked deployment",
    "deploy.scan.warning":  "Source/image scan found warnings",
    "deploy.completed":     "App deployed successfully",
    "deploy.rolled_back":   "Deployment rolled back",

    # Security enforcement
    "security.container_killed":    "Container killed for security violation",
    "security.rate_limited":        "App rate limited",
    "security.secret_rotated":      "Database credentials rotated",
    "security.blocked_request":     "Request blocked by WAF/nginx rule",

    # Resource events
    "resource.oom_killed":          "Container OOM killed",
    "resource.restart_loop":        "Container entered restart loop",
    "resource.disk_full":           "Container hit disk limit",

    # Access events
    "access.db_created":            "Database user/schema created for app",
    "access.db_revoked":            "Database access revoked for app",
    "access.secret_accessed":       "Secret injected into container",
}

# Log format (JSON, append-only, shipped to central log store)
# {
#   "timestamp": "2026-03-31T14:22:00Z",
#   "event": "deploy.scan.failed",
#   "user_id": "usr_abc",
#   "app_id": "app_abc123",
#   "details": {"reason": "AWS access key detected in source code"},
#   "severity": "critical",
#   "source_ip": "..."
# }
```

---

## 9. Least Privilege Matrix

### 9.1 Component Permissions

| Component | Runs as | Can access | Cannot access |
|-----------|---------|------------|---------------|
| **App container** | `nobody:nogroup` (65534) | Own port, own DB schema via proxy, /tmp (noexec), /run/secrets (read-only) | Host filesystem, Docker socket, other containers, other schemas, internet (by default) |
| **Nginx** | `nginx` user | TLS certs (read), upstream sockets, log directory (write) | Docker socket, database, app filesystems, secrets |
| **PgBouncer** | `pgbouncer` user | PostgreSQL socket, auth file (read), config (read) | Docker socket, app containers, host filesystem |
| **PostgreSQL** | `postgres` user | Data directory, WAL directory | Docker socket, app containers, nginx |
| **Deployment agent** | `vibe-deploy` user | Docker API (via socket group), nginx config dir (write), secrets store (read/write) | PostgreSQL superuser (uses dedicated admin role), SSH keys, host root |
| **Monitoring** (Prometheus) | `prometheus` user | Docker API (read-only), nginx stub_status, PgBouncer stats | Write access to anything, secrets, database data |

### 9.2 Database Role Hierarchy

```
pg_superuser (platform DBA -- emergency only, MFA-protected, audit logged)
  └── vibe_admin (deployment agent -- can CREATE ROLE, CREATE SCHEMA, but not SUPERUSER)
        └── app_u_abc123 (app -- DML only on own schema, connection-limited)
        └── app_u_xyz789 (app -- DML only on own schema, connection-limited)
        └── vibe_readonly (monitoring -- SELECT on pg_stat views only)
```

### 9.3 Docker API Access

```bash
# The deployment agent needs Docker API access, but it should be restricted.
# Option 1: Docker socket proxy (recommended)
# Use a socket proxy like tecnativa/docker-socket-proxy that whitelists API endpoints

docker run -d \
  --name docker-proxy \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -e CONTAINERS=1 \
  -e IMAGES=1 \
  -e NETWORKS=1 \
  -e SERVICES=1 \
  -e SECRETS=1 \
  -e POST=1 \
  -e BUILD=1 \
  -e COMMIT=0 \
  -e EXEC=0 \        # CRITICAL: No exec into containers
  -e SWARM=0 \
  -e NODES=0 \
  -e PLUGINS=0 \
  -e SYSTEM=0 \       # No system-level operations
  -e VOLUMES=0 \      # No volume management
  tecnativa/docker-socket-proxy

# The deployment agent connects to the proxy, not the real socket
# DOCKER_HOST=tcp://docker-proxy:2375
```

### 9.4 Filesystem Permissions

```bash
# Platform directories
/etc/vibe-deploy/                # 750 root:vibe-deploy
  ├── seccomp-vibe.json          # 644 root:root
  ├── nginx/                     # 750 root:nginx
  │   └── sites-enabled/         # 750 vibe-deploy:nginx (agent writes, nginx reads)
  └── secrets/                   # 700 vibe-deploy:vibe-deploy

/var/log/vibe-deploy/            # 750 root:vibe-deploy
  ├── audit.log                  # 640 vibe-deploy:vibe-deploy (append-only via chattr +a)
  ├── nginx/                     # 750 nginx:nginx
  └── apps/                      # 750 vibe-deploy:vibe-deploy

/run/vibe-secrets/               # 700 vibe-deploy:vibe-deploy (tmpfs)
  └── app-abc123/                # 700 vibe-deploy:vibe-deploy
      └── db-url                 # 400 65534:65534 (readable by app user only)
```

---

## 10. Implementation Priority

This is ordered by risk reduction per effort, not by section number. Implement in this order:

### Phase 1: Foundation (Week 1-2) -- Blocks the most dangerous attacks

1. **Container isolation profile** -- `cap_drop: ALL`, `read_only`, `no-new-privileges`, resource limits, `user: 65534`. This is the single most impactful change. Without it, a container escape compromises the host.

2. **Network isolation** -- Per-app Docker networks with `enable_icc=false`, `--internal`. Without this, any compromised app can attack every other app.

3. **iptables hardening** -- Block metadata service, Docker socket, host SSH from containers. Three commands that prevent common privilege escalation paths.

4. **Nginx security headers and rate limiting** -- The security headers config above, applied to every app. This is copy-paste configuration.

### Phase 2: Database Hardening (Week 2-3) -- Limits blast radius of SQLi

5. **Per-app database users and schemas** -- The provisioning SQL above, automated by the deployment agent. This is what prevents one app's SQL injection from reading another app's data.

6. **PgBouncer deployment** -- Connection pooling with limits. Prevents connection exhaustion and blocks multi-statement injection.

7. **pg_hba.conf network restrictions** -- Database only accessible from app Docker networks, not from the internet.

### Phase 3: Deployment Pipeline (Week 3-4) -- Catches problems before they deploy

8. **Dockerfile validation** -- The validation script above, run before every build. Blocks the most dangerous Dockerfile patterns.

9. **Secret scanning** -- Gitleaks on source code and built images. Prevents credential leaks.

10. **Image scanning with Trivy** -- Catches known CVEs in base images and dependencies.

### Phase 4: Secrets & Monitoring (Week 4-5) -- Operational security

11. **Docker secrets (file-based)** -- Replace environment variable secrets with file-based injection. Requires app-level changes to read from `/run/secrets/`.

12. **Monitoring and alerting** -- Prometheus + alerting rules for resource abuse, restart loops, and anomalous traffic.

13. **Audit logging** -- Structured, append-only logging of all security-relevant events.

### Phase 5: Advanced Hardening (Week 5+) -- Defense in depth

14. **Custom seccomp profile** -- Restricts syscalls beyond the default Docker profile.

15. **AppArmor profile** -- Filesystem and network confinement beyond what Docker provides.

16. **Egress proxy** -- Controlled outbound internet access with domain allowlisting.

17. **Docker socket proxy** -- Restrict the deployment agent's access to the Docker API.

18. **Automated credential rotation** -- Monthly rotation of database passwords.

---

## 11. What This Does NOT Cover (Future Work)

| Gap | Risk | Mitigation path |
|-----|------|-----------------|
| DDoS from outside the platform | High | Cloudflare or similar CDN/DDoS protection in front of nginx |
| Kernel exploits from inside container | Medium | Consider gVisor (runsc) or Kata Containers for stronger isolation |
| Compromised platform admin account | Critical | MFA on all admin access, audit logging, break-glass procedures |
| Backup encryption and access | High | Encrypt backups at rest, restrict access to DBA role only |
| Compliance (SOC2, GDPR, etc.) | Varies | Data retention policies, right-to-delete, processing records |
| Multi-server orchestration | Medium | Move to Kubernetes with network policies and pod security standards when scaling |
| WebSocket security | Medium | Add WebSocket-specific rate limiting and origin validation in nginx |
| File upload handling | High | Add virus scanning (ClamAV), file type validation, size limits, sandboxed storage |

---

## 12. Quick Reference: Security Invariants

These must ALWAYS be true. If any of these invariants is violated, it is a security incident.

1. **No container runs as root.** Ever. No exceptions.
2. **No container has Linux capabilities.** `cap_drop: ALL`, `cap_add: []`.
3. **No container mounts the Docker socket.** Or any host path.
4. **No container has outbound internet access** unless explicitly approved with an egress proxy.
5. **No app can query another app's database schema.** Per-app roles with schema-level isolation.
6. **No secrets exist in environment variables, image layers, or source code.** File-based injection only.
7. **No deployment proceeds with CRITICAL scan findings.** Hard block, no override.
8. **Every container has memory, CPU, PID, and disk limits.** No unbounded resource consumption.
9. **Every app is rate-limited at the nginx layer.** No unbounded request processing.
10. **Every security event is audit logged.** Deployments, kills, blocks, rotations, access grants.
