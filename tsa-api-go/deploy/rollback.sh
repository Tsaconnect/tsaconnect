#!/usr/bin/env bash
# Roll back to a previous release. Re-points a symlink and restarts — never builds,
# so it works when the current build is broken.
#
# Linux only (bash 4+ for mapfile; mv -T is GNU).
#
# Usage: rollback.sh [--list] [<release-dir>]
set -Eeuo pipefail
umask 077

ROOT=/srv/tsa-api
RELEASES=$ROOT/releases
PORT=8081
SYSTEMCTL=/usr/bin/systemctl

CURRENT=""
[ -L "$ROOT/current" ] && CURRENT=$(readlink "$ROOT/current")

# Only releases that actually have a binary are candidates. A failed deploy leaves a
# worktree with no binary; treating it as a candidate and giving up on it would
# disable rollback entirely after a single failed deploy.
mapfile -t CANDIDATES < <(
  ls -1dt "$RELEASES"/*/ 2>/dev/null | sed 's:/*$::' | while IFS= read -r d; do
    [ -x "$d/tsa-api" ] && printf '%s\n' "$d"
  done
)

if [ "${1:-}" = "--list" ]; then
  printf 'releases with a usable binary, newest first:\n'
  for r in "${CANDIDATES[@]:-}"; do
    [ -z "$r" ] && continue
    if [ "$r" = "$CURRENT" ]; then printf '  %s  <- current\n' "$r"; else printf '  %s\n' "$r"; fi
  done
  exit 0
fi

if [ -n "${1:-}" ]; then
  TARGET=$(readlink -f "$1")
  # Containment: without this, rollback.sh /tmp/evil would point current at it.
  case "$TARGET" in
    "$RELEASES"/*) ;;
    *) echo "refusing target outside $RELEASES: $TARGET" >&2; exit 1 ;;
  esac
  [ -x "$TARGET/tsa-api" ] || { echo "$TARGET has no binary" >&2; exit 1; }
else
  TARGET=""
  for r in "${CANDIDATES[@]:-}"; do
    [ -z "$r" ] && continue
    if [ "$r" != "$CURRENT" ]; then TARGET=$r; break; fi
  done
fi

[ -n "$TARGET" ] || { echo "no previous release with a usable binary" >&2; exit 1; }
[ "$TARGET" != "$CURRENT" ] || { echo "$TARGET is already current" >&2; exit 1; }

cat >&2 <<'WARN'

WARNING: this reverts CODE only. The database schema stays where the last deploy
left it. This is safe only if every migration was backward-compatible with the
previous release. If it was not, stop and escalate — recovery is a manual,
data-losing restore.

WARN

printf 'rolling back\n  from %s\n  to   %s\n' "${CURRENT:-none}" "$TARGET"
TMPLINK=$ROOT/.current.$$
rm -f "$TMPLINK"
ln -sfn "$TARGET" "$TMPLINK"
mv -T "$TMPLINK" "$ROOT/current"
sudo -n "$SYSTEMCTL" restart tsa-api.service || { echo "restart failed" >&2; exit 1; }

SECONDS=0
while [ "$SECONDS" -lt 60 ]; do
  if curl -fsS --max-time 2 "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
    echo "healthy after ${SECONDS}s — rolled back to $TARGET"
    exit 0
  fi
  sleep 1
done

# Deliberately does NOT roll itself forward again. If the previous release is also
# unhealthy the problem is not the release, and further flipping hides that.
echo "WARNING: still unhealthy ${SECONDS}s after rollback to $TARGET — investigate now" >&2
exit 1
