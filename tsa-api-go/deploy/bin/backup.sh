#!/usr/bin/env bash
# Run by tsa-api-backup.service. Connection parameters arrive as PG* env vars, so
# no credential appears in the process list.
set -Eeuo pipefail
umask 077

DEST=/srv/tsa-api/shared/backups
STAMP=$(date -u +%Y%m%d-%H%M%S)
KEEP_DAYS=14

mkdir -p "$DEST"

# Refuse to start a dump that could fill the disk. Postgres stops accepting writes
# when its volume fills, so an unguarded dump can take the database down.
AVAIL_KB=$(df --output=avail "$DEST" | tail -1)
if [ "$AVAIL_KB" -lt 2097152 ]; then   # 2 GiB
  echo "only ${AVAIL_KB}KB free at $DEST — refusing to dump" >&2
  exit 1
fi

# -Fc: compressed custom format, selectively restorable, no separate gzip.
# -w:  never prompt. Without it a missing password hangs the deploy indefinitely.
pg_dump -Fc -Z3 -w -f "$DEST/$STAMP.dump"

# The deploy prunes releases; without this, backups grow without bound.
find "$DEST" -name '*.dump' -mtime +$KEEP_DAYS -delete

echo "backup written: $DEST/$STAMP.dump"
