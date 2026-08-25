#!/usr/bin/env bash
# Run by tsa-api-migrate.service. Migrates the release named in .release-target.
# Env comes from systemd's EnvironmentFile — never sourced by a shell.
set -Eeuo pipefail

TARGET_FILE=/srv/tsa-api/.release-target
# Distinguish the two failures: during an incident "missing" and "unreadable" point
# at completely different causes (a deploy that never got here, vs a permissions
# mismatch between the deploy user and this unit's User=).
if [ ! -e "$TARGET_FILE" ]; then
  echo "missing $TARGET_FILE — deploy.sh did not reach the migrate step" >&2
  exit 1
fi
if [ ! -r "$TARGET_FILE" ]; then
  echo "$TARGET_FILE exists but is not readable by $(id -un) — check its mode" >&2
  ls -la "$TARGET_FILE" >&2
  exit 1
fi
REL=$(cat "$TARGET_FILE")

case "$REL" in
  /srv/tsa-api/releases/*) ;;
  *) echo "refusing to migrate outside releases/: $REL" >&2; exit 1 ;;
esac
[ -x "$REL/tsa-api" ] || { echo "no binary at $REL/tsa-api" >&2; exit 1; }

exec "$REL/tsa-api" -migrate
