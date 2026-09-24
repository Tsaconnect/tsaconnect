#!/usr/bin/env bash
# Write shared/env from Doppler. Doppler is the source of truth for production
# config; shared/env is the copy systemd reads, and nothing else should edit it.
#
# Usage: env-sync.sh [--dry-run] [--restart] [--allow-removals]
#   --dry-run         name the keys that would change; write nothing
#   --restart         after a change, restart tsa-api and wait for health;
#                     restores the previous file if the service does not come back
#   --allow-removals  permit a sync that deletes more than MAX_REMOVED keys
#
# Runs as tsa-deploy, which owns shared/. The read-only service token for the
# Doppler config lives in shared/doppler-token; with no token file, this is a
# no-op, so a box that has not been cut over still deploys exactly as before.
#
# Exit 0: shared/env matches Doppler (changed or not), or Doppler is not set up.
# Exit 1: shared/env is untouched (or restored), and the reason is printed.
#
# Linux only: sha256sum, getfacl and the GNU userland are assumed.
set -Eeuo pipefail
# Everything written here is secret: the download, the backup, the temp files.
umask 077

# TSA_ROOT exists for the rehearsal harness only. sudo's env_reset strips it on
# the real box, so production always resolves to /srv/tsa-api.
ROOT=${TSA_ROOT:-/srv/tsa-api}
SHARED=$ROOT/shared
ENV_FILE=$SHARED/env
PREV_FILE=$SHARED/env.prev
TOKEN_FILE=$SHARED/doppler-token
DOPPLER=${DOPPLER:-/usr/bin/doppler}
SYSTEMCTL=/usr/bin/systemctl
PORT=8081

# A download without these is a broken download, not a config change: syncing
# it would take the API down on the next restart.
REQUIRED_KEYS=(DATABASE_URL JWT_SECRET)
# Doppler removals are usually deliberate one-offs. Many at once is far more
# likely a wrong token, a wrong config, or a bulk delete in the dashboard.
MAX_REMOVED=3

DRY_RUN=0
RESTART=0
ALLOW_REMOVALS=0
for a in "$@"; do
  case "$a" in
    --dry-run) DRY_RUN=1 ;;
    --restart) RESTART=1 ;;
    --allow-removals) ALLOW_REMOVALS=1 ;;
    *) printf 'env-sync: unknown option %s\n' "$a" >&2; exit 2 ;;
  esac
done

# Same journald tag as deploy.sh, so `tsactl audit` shows config changes next to
# the deploys they happened in. Key NAMES only — never a value.
audit() { logger -t tsa-deploy -- "${SUDO_USER:-$(id -un)}: env-sync: $*" 2>/dev/null || true; }
say()  { printf 'env-sync: %s\n' "$*"; }
fail() { printf 'env-sync: FAILED: %s\n' "$*" >&2; audit "FAILED: $*"; exit 1; }

if [ ! -e "$TOKEN_FILE" ]; then
  say "no $TOKEN_FILE — Doppler not set up, leaving shared/env alone"
  exit 0
fi
[ -r "$TOKEN_FILE" ] || fail "cannot read $TOKEN_FILE"
[ -x "$DOPPLER" ]    || fail "doppler CLI not found at $DOPPLER"
[ -f "$ENV_FILE" ]   || fail "missing $ENV_FILE — provision it first (deploy/README.md)"

WORK=$(mktemp -d "$SHARED/.env-sync.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

# ── download ─────────────────────────────────────────────────────────────
# The token goes through the environment, never argv: argv is readable by every
# user on the box through /proc.
#
# --no-fallback: by default the CLI caches every secret in ~/.doppler/fallback,
# encrypted with a passphrase derived from the token that sits next to it, and
# serves that copy when the API is down. Here that would be a second copy of all
# of production's secrets, and an outage reported as "already matches".
# shared/env is the fallback.
TOKEN=$(tr -d '[:space:]' < "$TOKEN_FILE")
[ -n "$TOKEN" ] || fail "$TOKEN_FILE is empty"
if ! DOPPLER_TOKEN=$TOKEN "$DOPPLER" secrets download --no-file --format env \
       --no-fallback --no-check-version > "$WORK/raw" 2> "$WORK/err"; then
  fail "doppler download failed: $(head -c 300 "$WORK/err" | tr '\n' ' ')"
fi
unset TOKEN

# Doppler adds its own bookkeeping keys; the service has no use for them, and
# leaving them in would make the file differ from what anyone typed.
grep -vE '^DOPPLER_(PROJECT|CONFIG|ENVIRONMENT)=' "$WORK/raw" > "$WORK/new" || true

# ── validate ─────────────────────────────────────────────────────────────
[ -s "$WORK/new" ] || fail "Doppler returned no secrets — wrong token or empty config?"
if grep -q $'\r' "$WORK/new"; then
  fail "download contains carriage returns — systemd would keep them in the values"
fi
if grep -vqE '^[A-Za-z_][A-Za-z0-9_]*=' "$WORK/new"; then
  fail "download has lines that are not KEY=value (line $(grep -vnE '^[A-Za-z_][A-Za-z0-9_]*=' "$WORK/new" | head -1 | cut -d: -f1))"
fi
for k in "${REQUIRED_KEYS[@]}"; do
  grep -qE "^$k=" "$WORK/new" || fail "download has no $k — refusing to write a config the API cannot boot with"
done

# ── diff, by key name ────────────────────────────────────────────────────
# Each key maps to a hash of its value, so the report names what changed without
# printing a value. For a key repeated in a hand-edited file, the LAST one wins,
# because that is the one systemd applies.
#
# One pair of surrounding double quotes is stripped before hashing, as systemd
# does: Doppler quotes every value and a hand-written file usually quotes none,
# and a report that calls every key "changed" hides the one that really did.
# This is for the report only — what gets written is Doppler's text, verbatim.
keyhash() {
  local line val
  grep -E '^[A-Za-z_][A-Za-z0-9_]*=' "$1" | tac | awk -F= '!seen[$1]++' |
    while IFS= read -r line; do
      val=${line#*=}
      if [ "${#val}" -ge 2 ] && [ "${val:0:1}" = '"' ] && [ "${val: -1}" = '"' ]; then
        val=${val:1:${#val}-2}
      fi
      printf '%s %s\n' "${line%%=*}" "$(printf '%s' "$val" | sha256sum | cut -c1-16)"
    done | sort
}
keyhash "$ENV_FILE"  > "$WORK/old.kh"
keyhash "$WORK/new"  > "$WORK/new.kh"
ADDED=$(join -v2 "$WORK/old.kh" "$WORK/new.kh" | cut -d' ' -f1 | paste -sd' ' -)
REMOVED=$(join -v1 "$WORK/old.kh" "$WORK/new.kh" | cut -d' ' -f1 | paste -sd' ' -)
CHANGED=$(join "$WORK/old.kh" "$WORK/new.kh" | awk '$2 != $3 {print $1}' | paste -sd' ' -)

if [ -z "$ADDED$REMOVED$CHANGED" ] && cmp -s "$ENV_FILE" "$WORK/new"; then
  say "shared/env already matches Doppler"
  exit 0
fi
say "added:   ${ADDED:-none}"
say "removed: ${REMOVED:-none}"
say "changed: ${CHANGED:-none}"
[ -n "$ADDED$REMOVED$CHANGED" ] || say "values identical — only quoting or ordering differs"

N_REMOVED=$(join -v1 "$WORK/old.kh" "$WORK/new.kh" | wc -l)
if [ "$N_REMOVED" -gt "$MAX_REMOVED" ] && [ "$ALLOW_REMOVALS" != "1" ]; then
  fail "$N_REMOVED keys would be removed (limit $MAX_REMOVED) — check the token's config, or rerun with --allow-removals"
fi

if [ "$DRY_RUN" = "1" ]; then
  say "dry run — nothing written"
  exit 0
fi

# ── write ────────────────────────────────────────────────────────────────
# The backup is a fresh 0600 file with no ACL: only tsa-deploy may read it.
cp "$ENV_FILE" "$PREV_FILE"
# Overwrite IN PLACE. A rename (mv, sed -i, install) creates a new inode without
# the u:tsa:r ACL, and tsa-api.service then fails to start because it can no
# longer read its own config.
cat "$WORK/new" > "$ENV_FILE"
audit "wrote shared/env — added: ${ADDED:-none}; removed: ${REMOVED:-none}; changed: ${CHANGED:-none}"
say "wrote $ENV_FILE (previous copy: $PREV_FILE)"

if command -v getfacl >/dev/null && ! getfacl -p "$ENV_FILE" 2>/dev/null | grep -q '^user:tsa:r'; then
  say "WARNING: $ENV_FILE has no user:tsa:r ACL — the service will not be able to read it"
fi

[ "$RESTART" = "1" ] || { say "not restarted — the service picks this up on its next restart"; exit 0; }

# ── restart and health ───────────────────────────────────────────────────
restore() {
  cat "$PREV_FILE" > "$ENV_FILE"
  sudo -n "$SYSTEMCTL" restart tsa-api.service || true
}
health_commit() {
  curl -fsS --max-time 2 "http://127.0.0.1:$PORT/health" 2>/dev/null | jq -r '.commit // empty' || true
}

# The same binary must come back: a config change is not a deploy, and "healthy"
# on a different commit would mean something else answered on the port.
BEFORE=$(health_commit)
say "restarting tsa-api (running ${BEFORE:-unknown})"
if ! sudo -n "$SYSTEMCTL" restart tsa-api.service; then
  restore
  fail "restart failed — restored the previous shared/env"
fi
SECONDS=0
while [ "$SECONDS" -lt 60 ]; do
  NOW=$(health_commit)
  if [ -n "$NOW" ] && { [ -z "$BEFORE" ] || [ "$NOW" = "$BEFORE" ]; }; then
    say "healthy after ${SECONDS}s on $NOW"
    audit "restarted on new config, healthy on $NOW"
    exit 0
  fi
  sleep 1
done
restore
fail "unhealthy 60s after restart — restored the previous shared/env and restarted. See: tsactl logs"
