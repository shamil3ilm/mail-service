#!/usr/bin/env bash
# restore.sh — restore a mail-service snapshot produced by backup.sh.
#
# Usage:
#   restore.sh <snapshot.tar.gz>
#   restore.sh <snapshot.tar.gz.age>  (requires BACKUP_DECRYPT_KEY=/path/to/age-key)
#
# Refuses to run if the target MAIL_DATA_DIR is non-empty — you must
# --force to overwrite, so no accidental clobber of a live install.

set -euo pipefail

MAIL_DATA_DIR="${MAIL_DATA_DIR:-/var/lib/mailservice}"
FORCE=0

log() { printf '[restore] %s\n' "$*"; }
die() { printf '[restore] ERROR: %s\n' "$*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --force) FORCE=1; shift ;;
    -*)      die "unknown flag: $1" ;;
    *)       SNAPSHOT="$1"; shift ;;
  esac
done

[ -n "${SNAPSHOT:-}" ] || die "usage: restore.sh <snapshot.tar.gz[.age]> [--force]"
[ -f "$SNAPSHOT" ]     || die "no such file: $SNAPSHOT"

if [ -e "$MAIL_DATA_DIR/mail.db" ] && [ "$FORCE" -eq 0 ]; then
  die "$MAIL_DATA_DIR/mail.db exists — pass --force to overwrite"
fi

# Stop the service first so we're not restoring under a live writer.
if systemctl is-active --quiet mailservice; then
  log "stopping mailservice.service"
  systemctl stop mailservice
  restart=1
fi

staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT

# Decrypt if .age
work="$SNAPSHOT"
if [[ "$SNAPSHOT" == *.age ]]; then
  [ -n "${BACKUP_DECRYPT_KEY:-}" ] || die "BACKUP_DECRYPT_KEY required for .age input"
  command -v age >/dev/null       || die "'age' binary missing"
  log "decrypting"
  age -d -i "$BACKUP_DECRYPT_KEY" -o "$staging/decrypted.tar.gz" "$SNAPSHOT"
  work="$staging/decrypted.tar.gz"
fi

log "extracting into $staging"
tar -C "$staging" -xzf "$work"

mkdir -p "$MAIL_DATA_DIR"
log "installing DB"
install -m 0644 "$staging/mail.db" "$MAIL_DATA_DIR/mail.db"
chown mailservice:mailservice "$MAIL_DATA_DIR/mail.db" 2>/dev/null || true

for sub in raw attachments; do
  if [ -d "$staging/$sub" ]; then
    log "restoring $sub"
    rm -rf "${MAIL_DATA_DIR:?}/$sub"
    cp -a "$staging/$sub" "$MAIL_DATA_DIR/$sub"
    chown -R mailservice:mailservice "$MAIL_DATA_DIR/$sub" 2>/dev/null || true
  fi
done

if [ "${restart:-0}" -eq 1 ]; then
  log "restarting mailservice.service"
  systemctl start mailservice
fi

log "done"
