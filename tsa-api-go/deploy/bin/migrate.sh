#!/usr/bin/env bash
# Run by tsa-api-migrate.service. Migrates the release named in .release-target.
# Env comes from systemd's EnvironmentFile — never sourced by a shell.
set -Eeuo pipefail

TARGET_FILE=/srv/tsa-api/.release-target
[ -r "$TARGET_FILE" ] || { echo "missing $TARGET_FILE" >&2; exit 1; }
REL=$(cat "$TARGET_FILE")

case "$REL" in
  /srv/tsa-api/releases/*) ;;
  *) echo "refusing to migrate outside releases/: $REL" >&2; exit 1 ;;
esac
[ -x "$REL/tsa-api" ] || { echo "no binary at $REL/tsa-api" >&2; exit 1; }

exec "$REL/tsa-api" -migrate
