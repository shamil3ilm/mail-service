#!/usr/bin/env bash
# install.sh — one-shot server bootstrap for mail-service on Debian/Ubuntu.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/shamil3ilm/mail-service/main/deploy/install.sh | sudo bash
#   # OR:
#   sudo ./deploy/install.sh [--binary-url URL]
#
# What it does:
#   1. Creates a dedicated `mailservice` system user
#   2. Downloads (or copies from CWD) the binary → /usr/local/bin/mailservice
#   3. Creates /etc/mailservice/env from the cloud template
#   4. Creates /var/lib/mailservice with proper permissions
#   5. Installs the systemd unit
#   6. Configures UFW firewall (25, 587, 443)
#   7. Prints next steps
#
# The script is idempotent — safe to re-run.

set -euo pipefail

BINARY_URL="${BINARY_URL:-}"
LOCAL_BINARY="${LOCAL_BINARY:-./bin/mailservice}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary-url) BINARY_URL="$2"; shift 2 ;;
    --local-binary) LOCAL_BINARY="$2"; shift 2 ;;
    *) echo "unknown flag: $1"; exit 1 ;;
  esac
done

if [[ $EUID -ne 0 ]]; then
  echo "install.sh must run as root (sudo)"
  exit 1
fi

log() { echo -e "\033[1;34m[install]\033[0m $*"; }
warn() { echo -e "\033[1;33m[install]\033[0m $*"; }

# ── 1. system user ────────────────────────────────────────────────
if ! id -u mailservice >/dev/null 2>&1; then
  log "creating mailservice system user"
  useradd --system --home-dir /var/lib/mailservice --shell /usr/sbin/nologin mailservice
else
  log "user mailservice already exists"
fi

# ── 2. binary ─────────────────────────────────────────────────────
INSTALL_DIR=/usr/local/bin
INSTALL_PATH=$INSTALL_DIR/mailservice
if [[ -n "$BINARY_URL" ]]; then
  log "downloading binary from $BINARY_URL"
  tmp=$(mktemp)
  curl -fsSL "$BINARY_URL" -o "$tmp"
  install -m 755 "$tmp" "$INSTALL_PATH"
  rm -f "$tmp"
elif [[ -f "$LOCAL_BINARY" ]]; then
  log "installing binary from $LOCAL_BINARY"
  install -m 755 "$LOCAL_BINARY" "$INSTALL_PATH"
else
  warn "no binary source. Provide --binary-url or place binary at $LOCAL_BINARY"
  exit 1
fi

# ── 3. env file ───────────────────────────────────────────────────
ENV_DIR=/etc/mailservice
ENV_FILE=$ENV_DIR/env
if [[ ! -f "$ENV_FILE" ]]; then
  mkdir -p "$ENV_DIR"
  cat > "$ENV_FILE" <<'EOF'
# mail-service cloud config — edit before first start.
MAIL_MODE=cloud
MAIL_LISTEN_ADDR=0.0.0.0

MAIL_HTTP_PORT=443
MAIL_ADMIN_PORT=8036
MAIL_SMTP_PORT=25
MAIL_SUBMISSION_PORT=587

MAIL_LOG_LEVEL=info
MAIL_LOG_FORMAT=json

MAIL_DB_PATH=/var/lib/mailservice/mail.db
MAIL_RAW_STORE_PATH=/var/lib/mailservice/raw

# Auto-verify domains — leave empty in cloud mode.
MAIL_AUTO_VERIFY_DOMAINS=

MAIL_DEFAULT_MAILBOX_MODE=relay

# Outbound relay. Options: smtp | ses | resend | none
MAIL_RELAY_PROVIDER=smtp
MAIL_RELAY_SMTP_HOST=email-smtp.eu-central-1.amazonaws.com
MAIL_RELAY_SMTP_PORT=587
MAIL_RELAY_SMTP_USER=CHANGEME
MAIL_RELAY_SMTP_PASS=CHANGEME

# Startup self-checks (public MX hostname + IPv4).
MAIL_CLOUD_HOSTNAME=mail.example.com
MAIL_CLOUD_OUTBOUND_IP=203.0.113.42
MAIL_CLOUD_DNSBL_ZONES=zen.spamhaus.org,b.barracudacentral.org,bl.spamcop.net
EOF
  chown root:mailservice "$ENV_FILE"
  chmod 640 "$ENV_FILE"
  warn "created $ENV_FILE — EDIT THIS BEFORE STARTING THE SERVICE"
else
  log "$ENV_FILE already exists — leaving untouched"
fi

# ── 4. state dir ──────────────────────────────────────────────────
STATE_DIR=/var/lib/mailservice
mkdir -p "$STATE_DIR"
chown -R mailservice:mailservice "$STATE_DIR"
chmod 750 "$STATE_DIR"
log "state dir: $STATE_DIR"

# ── 5. systemd unit ───────────────────────────────────────────────
UNIT=/etc/systemd/system/mailservice.service
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [[ -f "$SCRIPT_DIR/systemd/mailservice.service" ]]; then
  cp "$SCRIPT_DIR/systemd/mailservice.service" "$UNIT"
  log "installed unit → $UNIT"
else
  warn "systemd unit not found at $SCRIPT_DIR/systemd/mailservice.service — skipping"
fi
systemctl daemon-reload

# ── 6. firewall ───────────────────────────────────────────────────
if command -v ufw >/dev/null 2>&1; then
  log "configuring UFW"
  ufw allow 25/tcp   comment 'mailservice smtp'  || true
  ufw allow 587/tcp  comment 'mailservice submission' || true
  ufw allow 443/tcp  comment 'mailservice https' || true
  ufw allow 22/tcp   comment 'ssh' || true
else
  warn "ufw not installed — configure your firewall manually for 25/587/443"
fi

# ── 7. next steps ─────────────────────────────────────────────────
cat <<EOF

$(tput setaf 2)mail-service installed.$(tput sgr0)

Next steps:
  1. Edit /etc/mailservice/env — set MAIL_CLOUD_HOSTNAME, MAIL_CLOUD_OUTBOUND_IP,
     and MAIL_RELAY_SMTP_* credentials.
  2. Ensure your DNS is published:
        A     mail.example.com          → your.ip.address
        MX    example.com               → mail.example.com  (priority 10)
        TXT   example.com               → v=spf1 ip4:your.ip.address -all
        TXT   ms1._domainkey.example.com → v=DKIM1; k=rsa; p=... (dashboard shows this)
        TXT   _dmarc.example.com        → v=DMARC1; p=quarantine; rua=mailto:dmarc@example.com
        PTR   your.ip.address           → mail.example.com  (set via VPS provider console)
  3. Start the service:
        sudo systemctl enable --now mailservice
        journalctl -u mailservice -f
  4. First-run admin password is printed to the journal at startup.

EOF
