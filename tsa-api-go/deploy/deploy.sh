#!/usr/bin/env bash
# Deploy tsa-api-go. Build and migrate BEFORE touching the running service, so any
# failure before the restart leaves production untouched.
#
# Linux only: mv -T, flock, readlink, df --output are GNU.
#
# Usage: deploy.sh [<git-ref>] [--force]
set -Eeuo pipefail
# 022, NOT 077. The release tree must be readable and executable by the SERVICE
# user (tsa), which is a different account from the deploy user. Under umask 077
# `go build` produces a 0700 binary that tsa-api.service cannot exec, and the
# service never starts — verified on a rehearsal host.
#
# Nothing secret is written by this script: shared/env is provisioned once at 0600
# with an ACL for tsa, and the database dumps are written by tsa-api-backup.service,
# which carries its own UMask=0077.
umask 022

ROOT=/srv/tsa-api
REPO=$ROOT/repo
RELEASES=$ROOT/releases
SHARED=$ROOT/shared
KEEP=5
PORT=8081
GO=${GO:-/usr/local/go/bin/go}
SYSTEMCTL=/usr/bin/systemctl

REF=origin/main
FORCE=0
for a in "$@"; do
  case "$a" in
    --force) FORCE=1 ;;
    *) REF=$a ;;
  esac
done

# Every step also goes to journald tagged `tsa-deploy`, so who deployed what, when,
# and whether it failed survives the terminal:  journalctl -t tsa-deploy
#
# SUDO_USER, not id -un: this runs as tsa-deploy via `sudo -u tsa-deploy`, so id -un
# is always "tsa-deploy" and would record nothing. SUDO_USER is the real person.
#
# `|| true` and no preflight check for logger: an audit-log failure must never be
# the thing that aborts a deploy.
audit() { logger -t tsa-deploy -- "${SUDO_USER:-$(id -un)}: $*" 2>/dev/null || true; }
log()  { printf '\n=== %s\n' "$*"; audit "$*"; }
fail() { printf '\nFAILED: %s\n' "$*" >&2; audit "FAILED: $*"; exit 1; }

audit "deploy requested: ref=$REF force=$FORCE"

# ── serialise ────────────────────────────────────────────────────────────
# Two concurrent deploys would run `-migrate` simultaneously — the exact DDL race
# this pipeline exists to remove — and clobber each other's temp symlink.
exec 9>"$ROOT/.deploy.lock"
flock -n 9 || fail "another deploy is already running"

# ── preflight ────────────────────────────────────────────────────────────
# Ubuntu's sudo secure_path excludes /usr/local/go/bin, so `go` is typically absent
# from PATH under sudo. Fail with a clear message rather than "command not found".
for c in git flock curl jq df; do
  command -v "$c" >/dev/null || fail "missing required command: $c"
done
[ -x "$GO" ] || fail "go not found at $GO (override with GO=/path/to/go)"
[ -r "$SHARED/env" ] || fail "missing $SHARED/env"
sudo -n "$SYSTEMCTL" is-enabled tsa-api.service >/dev/null 2>&1 \
  || fail "no passwordless systemctl grant for tsa-api.service — see deploy/README.md"

# ── resolve the target ───────────────────────────────────────────────────
log "fetching $REF"
git -C "$REPO" fetch --prune origin
SHA=$(git -C "$REPO" rev-parse "$REF^{commit}") || fail "cannot resolve $REF"
SHORT=${SHA:0:7}

# Only ship what is on main, unless explicitly overridden.
if [ "$FORCE" != "1" ]; then
  git -C "$REPO" merge-base --is-ancestor "$SHA" origin/main \
    || fail "$SHORT is not an ancestor of origin/main (use --force to override)"
fi

# Read the live SHA from the running service, not from a path. An earlier revision
# compared against a path containing a fresh timestamp, so the guard never fired.
LIVE_SHA=$(curl -fsS --max-time 2 "http://127.0.0.1:$PORT/health" 2>/dev/null | jq -r '.commit // empty' || true)
if [ "$FORCE" != "1" ] && [ "$LIVE_SHA" = "$SHORT" ]; then
  fail "$SHORT is already running (use --force to redeploy)"
fi

STAMP=$(date -u +%Y%m%d-%H%M%S)
REL=$RELEASES/$STAMP-$SHORT

# ── build ────────────────────────────────────────────────────────────────
# EXIT, not ERR: bash does not run an ERR trap for the left side of `||`, and the
# `exit` inside fail() is not a failing command. An ERR trap here never fires.
DEPLOYED=0
cleanup() {
  [ "$DEPLOYED" = "1" ] && return 0
  log "cleaning up failed release $REL"
  git -C "$REPO" worktree remove --force "$REL" 2>/dev/null || rm -rf "$REL"
  git -C "$REPO" worktree prune 2>/dev/null || true
}
trap cleanup EXIT

log "creating worktree $REL"
git -C "$REPO" worktree add --detach "$REL" "$SHA" || fail "worktree add failed"

AVAIL_MB=$(df --output=avail -m "$RELEASES" | tail -1)
[ "$AVAIL_MB" -gt 3072 ] || fail "only ${AVAIL_MB}MB free — refusing to build"

log "building $SHORT"
# GOTOOLCHAIN=local: with the default `auto`, a go.mod newer than the host toolchain
# makes Go download and execute a toolchain from the network at deploy time. That is
# not something to do on a payments host.
( cd "$REL/tsa-api-go" \
  && nice -n 10 env GOTOOLCHAIN=local GOMAXPROCS=2 GOFLAGS=-trimpath \
       GOCACHE="$ROOT/.cache/go-build" GOMODCACHE="$ROOT/.cache/go-mod" \
       "$GO" build -ldflags "-X main.buildCommit=$SHORT" -o "$REL/tsa-api" ./cmd/server ) \
  || fail "build failed — live service untouched"

[ -x "$REL/tsa-api" ] || fail "build produced no binary"
# Explicit, so a future umask change cannot silently make the binary unexecutable
# by the service user again.
chmod 755 "$REL/tsa-api"

# ── back up, then migrate ────────────────────────────────────────────────
log "backing up the database"
sudo -n "$SYSTEMCTL" start tsa-api-backup.service \
  || fail "backup failed — refusing to migrate without one"

log "migrating"
echo "$REL" > "$ROOT/.release-target"
# Readable by the migrate oneshot, which runs as `tsa`, not `tsa-deploy`. The
# umask 077 above (which protects the backups) would otherwise make this 0600 and
# the migration would fail with "cannot read .release-target". It holds a path,
# not a secret.
chmod 644 "$ROOT/.release-target"
sudo -n "$SYSTEMCTL" start tsa-api-migrate.service \
  || fail "migration failed — live service untouched; see: journalctl -u tsa-api-migrate"

# ── activate ─────────────────────────────────────────────────────────────
# Read the CURRENT link without -f. GNU readlink -f prints the path and exits 0 even
# when the link does not exist, so on a fresh host PREV would be non-empty and the
# rollback path would point `current` at itself — ELOOP, service unable to ever start.
PREV=""
if [ -L "$ROOT/current" ]; then
  PREV=$(readlink "$ROOT/current")
fi

log "activating $REL"
TMPLINK=$ROOT/.current.$$
rm -f "$TMPLINK"
ln -sfn "$REL" "$TMPLINK"
mv -T "$TMPLINK" "$ROOT/current"
DEPLOYED=1     # the worktree is live now; cleanup must not remove it

sudo -n "$SYSTEMCTL" restart tsa-api.service || {
  printf '\nrestart failed — reverting symlink\n' >&2
  if [ -n "$PREV" ] && [ -x "$PREV/tsa-api" ]; then
    ln -sfn "$PREV" "$TMPLINK" && mv -T "$TMPLINK" "$ROOT/current"
    sudo -n "$SYSTEMCTL" restart tsa-api.service || true
  fi
  fail "could not restart the service"
}

# ── health gate ──────────────────────────────────────────────────────────
# Asserts the reported commit, not just a 200: any process on the port would answer,
# including the previous release if the restart silently did nothing.
log "waiting for health"
SECONDS=0
HEALTHY=0
GOT=""
while [ "$SECONDS" -lt 60 ]; do
  GOT=$(curl -fsS --max-time 2 "http://127.0.0.1:$PORT/health" 2>/dev/null | jq -r '.commit // empty' || true)
  if [ "$GOT" = "$SHORT" ]; then HEALTHY=1; break; fi
  sleep 1
done

if [ "$HEALTHY" != "1" ]; then
  printf '\nunhealthy after %ds (reported commit: %s, wanted %s)\n' "$SECONDS" "${GOT:-none}" "$SHORT" >&2
  if [ -n "$PREV" ] && [ -x "$PREV/tsa-api" ]; then
    log "rolling back to $PREV"
    ln -sfn "$PREV" "$TMPLINK" && mv -T "$TMPLINK" "$ROOT/current"
    sudo -n "$SYSTEMCTL" restart tsa-api.service || true
    fail "rolled back to $PREV. NOTE: the schema is migrated to $SHORT — if the previous release cannot run against it, this is a manual recovery. See deploy/README.md."
  fi
  fail "no previous release to roll back to — THE SERVICE IS DOWN"
fi
log "healthy after ${SECONDS}s"

# ── prune ────────────────────────────────────────────────────────────────
log "pruning to the last $KEEP releases"
CURRENT_REAL=$(readlink "$ROOT/current")
# shellcheck disable=SC2012  # ls -t sorts by mtime; find has no portable equivalent,
# and these paths are generated by this script (timestamp-sha), never user input.
while IFS= read -r old; do
  old=${old%/}
  case "$old" in "$RELEASES"/*) ;; *) continue ;; esac   # never delete outside releases/
  [ "$old" = "$CURRENT_REAL" ] && continue
  [ "$old" = "$PREV" ] && continue                        # keep the rollback target
  git -C "$REPO" worktree remove --force "$old" 2>/dev/null || rm -rf "$old"
done < <(ls -1dt "$RELEASES"/*/ 2>/dev/null | tail -n +$((KEEP + 1)))
git -C "$REPO" worktree prune

log "deployed $SHORT — $(git -C "$REPO" log -1 --format=%s "$SHA")"
