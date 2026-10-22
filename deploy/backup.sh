#!/usr/bin/env bash
# Nightly logical backup of the Prospector database.
#
#   bash deploy/backup.sh
# Cron (03:15 daily):
#   15 3 * * * bash /opt/apps/prospector/deploy/backup.sh >> /opt/apps/prospector/backups/backup.log 2>&1
#
# Writes backups/nightly/prospector-<timestamp>.dump (pg_dump custom format) and deletes nightly
# dumps older than RETENTION_DAYS (default 14). Restore with pg_restore; see docs/DEPLOYMENT.md.
# Copy these files off the VPS periodically: a backup on the same disk is not a backup of the disk.
set -euo pipefail

cd "$(dirname "$0")/.."
umask 077
mkdir -p backups/nightly

ts=$(date +%Y%m%d-%H%M%S)
out="backups/nightly/prospector-$ts.dump"

docker compose exec -T db sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom' > "$out.partial"
# An empty or truncated dump must never replace a good one under the final name.
if [ ! -s "$out.partial" ]; then
  rm -f "$out.partial"
  echo "$(date -Is) backup failed: empty dump" >&2
  exit 1
fi
mv "$out.partial" "$out"

find backups/nightly -name 'prospector-*.dump' -mtime +"${RETENTION_DAYS:-14}" -delete
echo "$(date -Is) backup ok: $out ($(du -h "$out" | cut -f1))"
