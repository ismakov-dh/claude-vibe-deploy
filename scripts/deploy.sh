#!/bin/bash
# deploy.sh — Install vd on a remote server
#
# Usage:
#   ./scripts/deploy.sh server [domain] [prod-db-container] [prod-db-user]
#
# Environment variables (for wildcard TLS via Route53):
#   AWS_ACCESS_KEY_ID       — IAM credentials for certbot DNS challenge
#   AWS_SECRET_ACCESS_KEY
#
# Runs commands via sudo on the remote server (prompts for password).

set -euo pipefail

SERVER="${1:?Usage: $0 <server> [domain] [prod-db-container] [prod-db-user]}"
DOMAIN="${2:-}"
PROD_DB="${3:-}"
PROD_DB_USER="${4:-postgres}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

if [[ -n "$DOMAIN" && ( -z "${AWS_ACCESS_KEY_ID:-}" || -z "${AWS_SECRET_ACCESS_KEY:-}" ) ]]; then
    echo "WARNING: AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY not set."
    echo "         Wildcard TLS cert will be skipped."
    echo ""
fi

echo "==> Building Linux binary..."
cd "$PROJECT_DIR"
make build-linux

echo "==> Copying files to $SERVER..."
scp vd-linux-amd64 "$SERVER":/tmp/vd
scp scripts/vd-ssh-wrapper "$SERVER":/tmp/vd-ssh-wrapper

echo "==> Preparing remote setup script..."
# Write the remote script to a temp file, then scp + execute with TTY
REMOTE_SETUP=$(mktemp)
cat > "$REMOTE_SETUP" <<'REMOTE_SCRIPT'
set -eo pipefail

DOMAIN="${1:-}"
PROD_DB="${2:-}"
PROD_DB_USER="${3:-postgres}"
AWS_KEY="${4:-}"
AWS_SECRET="${5:-}"
VD_USER="vd-user"
VD_HOME="/opt/vibe-deploy"
CERTBOT="/opt/certbot/bin/certbot"
CERTBOT_VERSION="5.7.0"
AWS_CREDS="/etc/letsencrypt/aws/credentials.ini"

# ---------------------------------------------------------------
# 1. Install binary
# ---------------------------------------------------------------
echo "[vd] Installing binary..."
sudo install -m 755 /tmp/vd /usr/local/bin/vd
sudo install -m 755 /tmp/vd-ssh-wrapper /usr/local/bin/vd-ssh-wrapper
rm -f /tmp/vd /tmp/vd-ssh-wrapper

# ---------------------------------------------------------------
# 2. Create restricted user
# ---------------------------------------------------------------
echo "[vd] Creating user $VD_USER..."
if ! id "$VD_USER" &>/dev/null; then
    sudo useradd -m -s /bin/bash "$VD_USER"
fi

if getent group docker &>/dev/null; then
    sudo usermod -aG docker "$VD_USER"
    echo "[vd] Added $VD_USER to docker group"
else
    echo "[vd] WARNING: docker group not found. Install Docker first."
fi

# ---------------------------------------------------------------
# 3. SSH keypair + forced command
# ---------------------------------------------------------------
SSH_DIR="/home/$VD_USER/.ssh"
KEY_PATH="$SSH_DIR/vd_agent_key"
sudo mkdir -p "$SSH_DIR"

if sudo test -f "$KEY_PATH"; then
    echo "[vd] SSH keypair already exists"
else
    sudo ssh-keygen -t ed25519 -f "$KEY_PATH" -N "" -C "vd-agent-key"
    echo "[vd] Generated SSH keypair"
fi

AUTH_KEYS="$SSH_DIR/authorized_keys"
PUB_KEY=$(sudo cat "${KEY_PATH}.pub")
FORCED_ENTRY="command=\"/usr/local/bin/vd-ssh-wrapper\",no-port-forwarding,no-x11-forwarding,no-agent-forwarding $PUB_KEY"

if ! sudo grep -qF "vd-agent-key" "$AUTH_KEYS" 2>/dev/null; then
    echo "$FORCED_ENTRY" | sudo tee -a "$AUTH_KEYS" > /dev/null
    echo "[vd] Configured authorized_keys with forced command"
fi

sudo chown -R "$VD_USER:$VD_USER" "$SSH_DIR"
sudo chmod 700 "$SSH_DIR"
sudo chmod 600 "$AUTH_KEYS" "$KEY_PATH"
sudo chmod 644 "${KEY_PATH}.pub"

# ---------------------------------------------------------------
# 4. Wildcard TLS cert via Route53
# ---------------------------------------------------------------
if [[ -n "$DOMAIN" && -n "$AWS_KEY" ]]; then
    echo "[vd] Setting up wildcard TLS for *.$DOMAIN..."

    # certbot in its own venv, per upstream's recommended install. Pinned so
    # hosts provisioned months apart get the same certbot.
    if ! sudo test -x "$CERTBOT"; then
        sudo apt-get update -qq
        sudo apt-get install -y -qq python3-venv
        sudo python3 -m venv /opt/certbot
        sudo /opt/certbot/bin/pip install -q --upgrade pip
        sudo /opt/certbot/bin/pip install -q \
            "certbot==$CERTBOT_VERSION" "certbot-dns-route53==$CERTBOT_VERSION"
    fi

    # An apt or snap certbot would also own a renewal timer, which would keep
    # failing in the background against the same lineage. Every call below uses
    # the absolute venv path, so PATH order cannot decide which one runs.
    if dpkg-query -W -f='${Status}' certbot 2>/dev/null | grep -q '^install ok installed'; then
        echo "[vd] WARNING: an apt certbot is also installed. Remove it, or its"
        echo "[vd]          timer will keep failing renewals: sudo apt-get remove"
        echo "[vd]          -y certbot python3-certbot-dns-route53"
    fi

    # Write AWS credentials for certbot. certbot-dns-route53 has no
    # --credentials flag — it only reads boto3's credential chain, which
    # ignores this path unless AWS_SHARED_CREDENTIALS_FILE points at it.
    sudo mkdir -p "$(dirname "$AWS_CREDS")"
    echo "[default]
aws_access_key_id = $AWS_KEY
aws_secret_access_key = $AWS_SECRET" | sudo tee "$AWS_CREDS" > /dev/null
    sudo chmod 600 "$AWS_CREDS"

    # Renewal. A venv certbot ships no systemd units, so this cron entry is the
    # only thing that will ever renew the cert. The env line is what makes the
    # credentials above reachable; without it renewal fails with "Unable to
    # locate credentials" and the cert silently expires after 90 days.
    sudo tee /etc/cron.d/vd-certbot > /dev/null <<CRONEOF
# Managed by vd — do not edit manually
AWS_SHARED_CREDENTIALS_FILE=$AWS_CREDS
0 0,12 * * * root /opt/certbot/bin/python -c 'import random,time; time.sleep(random.random() * 3600)' && $CERTBOT renew -q
CRONEOF
    sudo chmod 644 /etc/cron.d/vd-certbot

    # Reload nginx after renewal, or it keeps serving the old cert until
    # something else restarts it. reload-or-restart because a plain reload
    # fails outright if nginx happens to be stopped. The hooks directory
    # applies to every lineage, including certs issued before this existed.
    sudo mkdir -p /etc/letsencrypt/renewal-hooks/deploy
    printf '#!/bin/sh\nsystemctl reload-or-restart nginx\n' \
        | sudo tee /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh > /dev/null
    sudo chmod +x /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh

    # Request wildcard cert. Deliberately via the credentials file rather than
    # inline env vars, so issuance and renewal resolve credentials identically —
    # issuance succeeding is then evidence renewal will too.
    if ! sudo test -d "/etc/letsencrypt/live/$DOMAIN"; then
        sudo AWS_SHARED_CREDENTIALS_FILE="$AWS_CREDS" \
            "$CERTBOT" certonly \
            --dns-route53 \
            -d "*.$DOMAIN" \
            -d "$DOMAIN" \
            --non-interactive \
            --agree-tos \
            --register-unsafely-without-email \
            && echo "[vd] Wildcard cert obtained for *.$DOMAIN" \
            || echo "[vd] WARNING: certbot failed. Set up cert manually."
    else
        echo "[vd] Wildcard cert already exists for $DOMAIN"
    fi

    # Prove renewal works now — otherwise the first symptom is a browser TLS
    # warning in 90 days. Runs in the same environment cron will have: the
    # credentials file and nothing else.
    if sudo test -d "/etc/letsencrypt/live/$DOMAIN"; then
        echo "[vd] Verifying renewal (staging dry-run, may take a minute)..."
        sudo AWS_SHARED_CREDENTIALS_FILE="$AWS_CREDS" \
            "$CERTBOT" renew --cert-name "$DOMAIN" --dry-run \
            && echo "[vd] Renewal verified" \
            || echo "[vd] WARNING: renewal dry-run failed — cert will expire in 90 days"
    fi
elif [[ -n "$DOMAIN" ]]; then
    echo "[vd] Skipping TLS setup (no AWS credentials)"
fi

# ---------------------------------------------------------------
# 5. Nginx config — proxy wildcard domain to Traefik
# ---------------------------------------------------------------
if [[ -n "$DOMAIN" ]]; then
    echo "[vd] Configuring nginx..."

    NGINX_CONF="/etc/nginx/sites-enabled/vd-proxy.conf"

    SSL_BLOCK=""
    if sudo test -f "/etc/letsencrypt/live/$DOMAIN/fullchain.pem"; then
        SSL_BLOCK="
    listen 443 ssl;
    ssl_certificate /etc/letsencrypt/live/$DOMAIN/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/$DOMAIN/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;"
    fi

    sudo tee "$NGINX_CONF" > /dev/null <<NGINXEOF
# vibe-deploy: proxy wildcard to Traefik
# Managed by vd — do not edit manually
server {
    listen 80;${SSL_BLOCK}
    server_name *.$DOMAIN $DOMAIN;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 300s;
    }
}
NGINXEOF

    if sudo nginx -t 2>/dev/null; then
        sudo systemctl reload nginx
        echo "[vd] Nginx configured and reloaded"
    else
        echo "[vd] WARNING: nginx config test failed:"
        sudo nginx -t
    fi
else
    echo "[vd] Skipping nginx setup (no domain)"
fi

# ---------------------------------------------------------------
# 6. vd init
# ---------------------------------------------------------------
sudo mkdir -p "$VD_HOME"
sudo chown "$VD_USER:$VD_USER" "$VD_HOME"

echo "[vd] Running vd init..."
INIT_FLAGS=""
if [[ -n "$DOMAIN" ]]; then
    INIT_FLAGS="$INIT_FLAGS --domain $DOMAIN"
fi
if [[ -n "$PROD_DB" ]]; then
    INIT_FLAGS="$INIT_FLAGS --prod-db $PROD_DB --prod-db-user $PROD_DB_USER"
fi
sudo su -s /bin/bash "$VD_USER" -c "sg docker -c 'VD_HOME=$VD_HOME /usr/local/bin/vd init $INIT_FLAGS'" || {
    echo "[vd] WARNING: vd init failed. Trying with docker directly..."
    sudo VD_HOME=$VD_HOME /usr/local/bin/vd init $INIT_FLAGS || true
    sudo chown -R "$VD_USER:$VD_USER" "$VD_HOME"
}

# ---------------------------------------------------------------
# 7. Daily database backup cron
# ---------------------------------------------------------------
echo "[vd] Setting up daily database backup cron..."
CRON_CMD="0 3 * * * /usr/local/bin/vd db-backup-all 2>&1 | logger -t vd-db-backup"
(sudo crontab -u "$VD_USER" -l 2>/dev/null | grep -v 'vd db-backup-all'; echo "$CRON_CMD") | sudo crontab -u "$VD_USER" -
echo "[vd] Daily backup cron installed (3:00 AM)"

# ---------------------------------------------------------------
# 8. Summary
# ---------------------------------------------------------------
echo ""
echo "==========================================="
echo "  vd installed successfully"
echo "==========================================="
echo ""
echo "Private key (copy to agent machines):"
echo "---"
sudo cat "$KEY_PATH"
echo "---"
echo ""
echo "Agent SSH config:"
SERVER_IP=$(curl -s ifconfig.me 2>/dev/null || echo '<server-ip>')
echo "  Host vd-server"
echo "    HostName $SERVER_IP"
echo "    User $VD_USER"
echo "    IdentityFile ~/.ssh/vd_agent_key"
echo ""
echo "Test: ssh vd-server \"vd version --json\""
if [[ -n "$DOMAIN" ]]; then
    echo ""
    echo "Apps will be available at: https://<app-name>.$DOMAIN"
fi
echo ""
echo "NEXT STEPS:"
if [[ -n "$DOMAIN" ]]; then
    echo "  1. Add wildcard DNS record: *.$DOMAIN -> $SERVER_IP"
fi
if [[ -z "$PROD_DB" ]]; then
    echo "  2. Connect prod DB later: sudo su -s /bin/bash $VD_USER -c 'vd init --prod-db <container> --prod-db-user <user>'"
fi
echo ""
REMOTE_SCRIPT

# Nothing else parses the heredoc above, so a typo in it would only surface
# part-way through provisioning a live server.
bash -n "$REMOTE_SETUP" || { echo "ERROR: remote setup script has a syntax error"; exit 1; }

scp "$REMOTE_SETUP" "$SERVER":/tmp/vd-setup.sh
rm -f "$REMOTE_SETUP"

echo "==> Running setup on $SERVER (will prompt for sudo password)..."
ssh -t "$SERVER" "bash /tmp/vd-setup.sh '$DOMAIN' '$PROD_DB' '$PROD_DB_USER' '${AWS_ACCESS_KEY_ID:-}' '${AWS_SECRET_ACCESS_KEY:-}'; rm -f /tmp/vd-setup.sh"

echo ""
echo "==> Done. Copy the private key above to your agent machines."
echo "    Save it as ~/.ssh/vd_agent_key and chmod 600 it."
