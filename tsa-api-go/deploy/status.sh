#!/usr/bin/env bash
# Read-only view of what is actually deployed.
#
# Deliberately needs NO privilege and takes NO lock: this is the command you run
# during an incident, when you may not be the deploy user and a deploy may be in
# flight. It never writes, never restarts, never touches the release tree.
#
# Exit 0 healthy, 1 if the service is down, unhealthy, or serving a commit other
# than the one `current` points at — so it works as a monitoring probe.
#
# Linux only: bash 4+ for mapfile, and systemctl/journalctl.
#
# Usage: status.sh
set -Eeuo pipefail

ROOT=/srv/tsa-api
REPO=$ROOT/repo
RELEASES=$ROOT/releases
PORT=8081

if [ -t 1 ]; then R=$'\033[31m'; G=$'\033[32m'; Y=$'\033[33m'; Z=$'\033[0m'
else R=; G=; Y=; Z=; fi
row() { printf '  %-9s %s\n' "$1" "$2"; }

RC=0

# ── service ──────────────────────────────────────────────────────────────
# is-active exits non-zero when inactive; that is data here, not a failure.
SVC=$(systemctl is-active tsa-api.service 2>/dev/null || true)
SINCE=$(systemctl show tsa-api.service -p ActiveEnterTimestamp --value 2>/dev/null || true)
[ "$SVC" = active ] || RC=1

# ── health ───────────────────────────────────────────────────────────────
# The health endpoint is the only source that says what is RUNNING. The symlink
# below says what is SUPPOSED to be running; when they disagree, believe this one.
HEALTH=$(curl -fsS --max-time 2 "http://127.0.0.1:$PORT/health" 2>/dev/null || true)
LIVE_SHA=""
if [ -n "$HEALTH" ] && command -v jq >/dev/null 2>&1; then
  LIVE_SHA=$(printf '%s' "$HEALTH" | jq -r '.commit // empty' 2>/dev/null || true)
fi

# ── current ──────────────────────────────────────────────────────────────
CURRENT=""
[ -L "$ROOT/current" ] && CURRENT=$(readlink "$ROOT/current")
CURRENT_SHA=""
if [ -n "$CURRENT" ]; then
  b=$(basename "$CURRENT")
  CURRENT_SHA=${b##*-}          # dirs are <stamp>-<short sha>
fi

printf '\ntsa-api\n\n'
if [ "$SVC" = active ]; then row service "${G}active${Z}${SINCE:+  since $SINCE}"
else                          row service "${R}${SVC:-unknown}${Z}"; fi

if [ -z "$HEALTH" ]; then
  row health "${R}no response on 127.0.0.1:$PORT${Z}"
  RC=1
elif [ -z "$LIVE_SHA" ]; then
  row health "${Y}responding, but reported no commit${Z}"
else
  row health "${G}ok${Z}  commit $LIVE_SHA"
fi

row current "${CURRENT:-${R}no current symlink${Z}}"

# The mismatch this exists to catch: a restart that silently did nothing, leaving
# the previous release serving while `current` claims the new one is live.
if [ -n "$LIVE_SHA" ] && [ -n "$CURRENT_SHA" ] && [ "$LIVE_SHA" != "$CURRENT_SHA" ]; then
  row '' "${R}MISMATCH: serving $LIVE_SHA, current points at $CURRENT_SHA${Z}"
  RC=1
fi

# ── tooling drift ────────────────────────────────────────────────────────
# deploy.sh only ever FETCHES $REPO; it builds in worktrees and never checks the
# clone out again. So the scripts on this box stay frozen at clone time until
# someone pulls. Surfacing it here is the only thing that makes that visible.
#
# Read the refs off disk rather than shelling out to git. git refuses a repo owned
# by another user (safe.directory), which is precisely this repo: the clone belongs
# to tsa-deploy while status is run by root or by a human. That refusal cannot be
# waived per-invocation — safe.directory has been protected-config-only since git
# 2.38 and is ignored from `-c`. An earlier revision called git and sent stderr to
# /dev/null, which made this entire row disappear on the first real run.
GITDIR=$REPO/.git

# Loose ref first, then packed-refs. A freshly cloned origin/main is packed; one
# updated by a later fetch is loose. Both occur on a live box.
resolve_ref() {
  if [ -r "$GITDIR/$1" ]; then
    cut -c1-7 <"$GITDIR/$1"
  elif [ -r "$GITDIR/packed-refs" ]; then
    awk -v r="$1" '$2 == r { print substr($1, 1, 7); exit }' "$GITDIR/packed-refs"
  fi
}

head_sha() {
  [ -r "$GITDIR/HEAD" ] || return 0
  local h; h=$(cut -c1-64 <"$GITDIR/HEAD")
  case "$h" in
    'ref: '*) resolve_ref "${h#ref: }" ;;
    *)        printf '%s' "${h:0:7}" ;;   # detached HEAD
  esac
}

HEAD_SHA=$(head_sha || true)
MAIN_SHA=$(resolve_ref refs/remotes/origin/main || true)

# Always prints a row. "No tooling line" must never again be a possible output —
# a silently absent warning is worse than no warning at all.
if [ -z "$HEAD_SHA" ]; then
  row tooling "${Y}cannot read $GITDIR — deploy checkout missing or unreadable${Z}"
elif [ -z "$MAIN_SHA" ]; then
  row tooling "$HEAD_SHA ${Y}(no origin/main ref — cannot check for drift)${Z}"
elif [ "$HEAD_SHA" != "$MAIN_SHA" ]; then
  row tooling "${Y}checkout $HEAD_SHA, origin/main $MAIN_SHA — scripts are stale${Z}"
  row '' "run: tsactl update"
  RC=1
else
  row tooling "$HEAD_SHA"
fi

# ── releases ─────────────────────────────────────────────────────────────
# Same rule as rollback.sh: a release without a binary is a failed deploy, not a
# rollback candidate. Listing them identically here means the target this prints
# is the target `rollback` will actually pick.
# shellcheck disable=SC2012  # ls -t sorts by mtime; these paths are generated by
# deploy.sh (timestamp-sha), never user input.
mapfile -t CANDIDATES < <(
  ls -1dt "$RELEASES"/*/ 2>/dev/null | sed 's:/*$::' | while IFS= read -r d; do
    [ -x "$d/tsa-api" ] && printf '%s\n' "$d"
  done
)

ROLLBACK=""
for r in "${CANDIDATES[@]:-}"; do
  [ -z "$r" ] && continue
  if [ "$r" != "$CURRENT" ]; then ROLLBACK=$r; break; fi
done
row rollback "${ROLLBACK:-${Y}none — no other release has a binary${Z}}"

printf '\nreleases with a usable binary, newest first:\n'
if [ -z "${CANDIDATES[0]:-}" ]; then
  printf '  (none)\n'
else
  for r in "${CANDIDATES[@]}"; do
    case "$r" in
      "$CURRENT")  printf '  %s  <- current\n'  "$(basename "$r")" ;;
      "$ROLLBACK") printf '  %s  <- rollback\n' "$(basename "$r")" ;;
      *)           printf '  %s\n'              "$(basename "$r")" ;;
    esac
  done
fi
printf '\n'

exit $RC
