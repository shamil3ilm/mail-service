#!/usr/bin/env bash
# backup.sh — atomic snapshot of a running mail-service instance.
#
# What gets backed up:
#   * SQLite DB via `sqlite3 .backup` (crash-consistent even under load —
#     don't just `cp` a WAL-mode DB, it corrupts).
#   * Raw MIME store (data/raw/) — every accepted message's .eml file.
#   * Attachments (data/attachments/) — parsed attachment payloads.
#
# The result is a single .tar.gz you can move anywhere. To restore, extract
# it over an empty $MAIL_DB_PATH/dir tree and start the service.
#
# Off-site: if RCLONE_REMOTE is set (e.g. RCLONE_REMOTE="b2:mailservice"),
# the tarball is pushed there and the local copy is kept.
#
# Cron (nightly at 02:00):
#   0 2 * * *  /usr/local/bin/mailservice-backup.sh
#
# Env vars:
#   MAIL_DATA_DIR    default: /var/lib/mailservice
#   BACKUP_DIR       default: $MAIL_DATA_DIR/backups
#   BACKUP_KEEP      how many local snapshots to keep (default: 14)
#   RCLONE_REMOTE    optional rclone destination
#   BACKUP_ENCRYPT   optional: age recipient (e.g. "age1...") — encrypt tarball
#                    before writing. Requires the `age` binary in PATH.

set -euo pipefail

MAIL_DATA_DIR="${MAIL_DATA_DIR:-/var/lib/mailservice}"
BACKUP_DIR="${BACKUP_DIR:-$MAIL_DATA_DIR/backups}"
BACKUP_KEEP="${BACKUP_KEEP:-14}"
RCLONE_REMOTE="${RCLONE_REMOTE:-}"
BACKUP_ENCRYPT="${BACKUP_ENCRYPT:-}"

DB="$MAIL_DATA_DIR/mail.db"
RAW="$MAIL_DATA_DIR/raw"
ATTS="$MAIL_DATA_DIR/attachments"

log()  { printf '[backup] %s\n'   "$*"; }
die()  { printf '[backup] ERROR: %s\n' "$*" >&2; exit 1; }

command -v sqlite3 >/dev/null || die "sqlite3 not installed"

[ -f "$DB" ] || die "no DB at $DB (is MAIL_DATA_DIR correct?)"
mkdir -p "$BACKUP_DIR"

stamp=$(date -u +%Y%m%dT%H%M%SZ)
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT

# 1. Atomic SQLite backup via .backup command (respects WAL, transactional)
log "snapshotting DB → $staging/mail.db"
sqlite3 "$DB" ".backup '$staging/mail.db'"

# 2. Raw + attachments as-is (append-only trees, safe to tar)
if [ -d "$RAW" ]; then
  log "copying raw store"
  cp -a "$RAW"  "$staging/raw"
fi
if [ -d "$ATTS" ]; then
  log "copying attachments"
  cp -a "$ATTS" "$staging/attachments"
fi

# 3. Package
outfile="$BACKUP_DIR/mailservice-$stamp.tar.gz"
log "packing → $outfile"
tar -C "$staging" -czf "$outfile" .

# 4. Optional encryption via age
if [ -n "$BACKUP_ENCRYPT" ]; then
  command -v age >/dev/null || die "BACKUP_ENCRYPT set but 'age' binary missing"
  log "encrypting for $BACKUP_ENCRYPT"
  age -r "$BACKUP_ENCRYPT" -o "$outfile.age" "$outfile"
  rm -f "$outfile"
  outfile="$outfile.age"
fi

log "wrote $(du -h "$outfile" | cut -f1) → $outfile"

# 5. Off-site push (optional)
if [ -n "$RCLONE_REMOTE" ]; then
  command -v rclone >/dev/null || die "RCLONE_REMOTE set but 'rclone' missing"
  log "pushing to $RCLONE_REMOTE"
  rclone copy "$outfile" "$RCLONE_REMOTE/" --progress
fi

# 6. Prune local copies past BACKUP_KEEP
log "pruning local snapshots (keeping $BACKUP_KEEP)"
ls -1t "$BACKUP_DIR"/mailservice-*.tar.gz "$BACKUP_DIR"/mailservice-*.tar.gz.age 2>/dev/null \
  | tail -n +$((BACKUP_KEEP + 1)) \
  | while read -r old; do
      log "  removing $(basename "$old")"
      rm -f "$old"
    done

log "done"
