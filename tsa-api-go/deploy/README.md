# Deploying tsa-api-go

## Layout

```
/srv/tsa-api/                 owned by tsa-deploy
  repo/                       clone; fetched, never built in place
  releases/<stamp>-<sha>/     git worktree + built binary
  current -> releases/...     what is running
  bin/                        migrate.sh, backup.sh (run by systemd oneshots)
  shared/env                  0600, service config
  shared/pgenv                0600, PG* vars for pg_dump only
  shared/backups/             0700, pre-migration dumps
  .release-target             release the migrate oneshot should act on

/usr/local/bin/tsactl         operator shortcut; wraps the scripts below
```

**Two users.** `tsa-deploy` owns everything above and runs deploys. `tsa` runs the
service and owns nothing — if the API is compromised it must not be able to write a
release, repoint `current`, or read a database dump.

## First-time provisioning

```bash
sudo useradd --system --home /srv/tsa-api --shell /usr/sbin/nologin tsa-deploy
sudo useradd --system --no-create-home --shell /usr/sbin/nologin tsa

sudo mkdir -p /srv/tsa-api/{releases,bin,shared/backups}
sudo chown -R tsa-deploy:tsa-deploy /srv/tsa-api
sudo chmod 755 /srv/tsa-api
sudo chmod 700 /srv/tsa-api/shared /srv/tsa-api/shared/backups

# SSH for the clone. Without a pre-seeded known_hosts the clone fails on host-key
# verification with no prompt available.
sudo -u tsa-deploy mkdir -p /srv/tsa-api/.ssh && sudo chmod 700 /srv/tsa-api/.ssh
sudo -u tsa-deploy ssh-keyscan github.com | sudo -u tsa-deploy tee /srv/tsa-api/.ssh/known_hosts
# Install a READ-ONLY deploy key scoped to ojimcy/tsa-dev alone. Not a personal token.
sudo install -m 600 -o tsa-deploy -g tsa-deploy /path/to/deploy_key /srv/tsa-api/.ssh/id_ed25519

sudo -u tsa-deploy -H git clone git@github.com:ojimcy/tsa-dev.git /srv/tsa-api/repo

# Config. shared/env is systemd EnvironmentFile syntax: KEY=value, no `export`,
# no shell expansion, no CRLF, no quotes unless you mean them literally.
sudo install -m 600 -o tsa-deploy -g tsa-deploy /path/to/env /srv/tsa-api/shared/env
sudo setfacl -m u:tsa:r /srv/tsa-api/shared/env    # service reads it; cannot write

# pgenv: PG* vars only, so pg_dump needs no argument and no credential hits argv.
sudo tee /srv/tsa-api/shared/pgenv >/dev/null <<'EOF'
PGHOST=127.0.0.1
PGPORT=5432
PGUSER=tsa
PGPASSWORD=...
PGDATABASE=tsa
EOF
sudo chown tsa-deploy:tsa-deploy /srv/tsa-api/shared/pgenv
sudo chmod 600 /srv/tsa-api/shared/pgenv

sudo install -m 755 -o tsa-deploy -g tsa-deploy \
    /srv/tsa-api/repo/tsa-api-go/deploy/bin/*.sh /srv/tsa-api/bin/

# The operator shortcut. Root-owned and 755: it is on everyone's PATH, and it
# escalates via each operator's own sudo, so it must not be writable by tsa-deploy.
sudo install -m 755 -o root -g root \
    /srv/tsa-api/repo/tsa-api-go/deploy/tsactl /usr/local/bin/tsactl

D=/srv/tsa-api/repo/tsa-api-go/deploy
sudo install -m 644 $D/tsa-api.service         /etc/systemd/system/
sudo install -m 644 $D/tsa-api.slice           /etc/systemd/system/
sudo install -m 644 $D/tsa-api-migrate.service /etc/systemd/system/
sudo install -m 644 $D/tsa-api-backup.service  /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable tsa-api

sudo visudo -cf $D/sudoers-tsa-deploy
sudo install -m 440 -o root -g root $D/sudoers-tsa-deploy /etc/sudoers.d/tsa-deploy

sudo install -m 644 $D/nginx-tsa-api-upstream.conf /etc/nginx/conf.d/
# Merge nginx-tsa-api-site.conf's location block into the existing server block.
sudo nginx -t && sudo systemctl reload nginx

sudo apt-get install -y jq shellcheck   # jq is required by deploy.sh
```

Verify the grant works before the first deploy:

```bash
sudo -u tsa-deploy sudo -n /usr/bin/systemctl is-enabled tsa-api.service && echo "grant OK"
```

Record the hardening score:

```bash
systemd-analyze security tsa-api.service
```

## Deploying

```bash
tsactl status                 # always start here: what is actually running
tsactl deploy                 # latest origin/main
tsactl deploy <sha>           # a specific commit
tsactl deploy <ref> --force   # off-main ref, or redeploy the running sha
tsactl logs -f                # tail the service
tsactl audit                  # who deployed what, and when
```

`tsactl` is a thin wrapper, not a privilege grant: it is not setuid and not in
sudoers, so `deploy` and `rollback` still run through your own `sudo` and the audit
trail names a real person. The long form still works and is what the wrapper runs:

```bash
sudo -u tsa-deploy -H /srv/tsa-api/repo/tsa-api-go/deploy/deploy.sh <sha>
```

`-H` matters: without it `HOME` may stay `/root`, and both the Go build cache and the
SSH key lookup fail with permission errors.

### Keeping the tooling itself current

`deploy.sh` only ever **fetches** `repo/` — it builds in per-release worktrees and
never checks the clone out again. So the scripts in `repo/` stay frozen at whatever
was cloned until someone pulls:

```bash
tsactl update      # sudo -u tsa-deploy -H git -C /srv/tsa-api/repo pull --ff-only
```

`tsactl status` always prints a `tooling` line, and exits non-zero when the checkout
is behind `origin/main`. This bites after any change to the deploy scripts themselves —
including the change that added this section.

## Rolling back

```bash
tsactl rollback --list        # releases with a usable binary
tsactl rollback               # the newest one that is not current
tsactl rollback <release-dir> # a specific one
```

Rollback re-points a symlink and restarts. It does not rebuild, so it works when the
current build is broken.

**Rollback reverts code, not schema.** The database is already migrated by the time a
health check can fail. This is safe only because of the rule below; if release N-1
cannot run against release N's schema, recovery is a manual, data-losing restore — stop
and escalate rather than improvising.

## The migration rule

**Every migration must be backward-compatible with the immediately preceding release.**

`AutoMigrate` is additive, which makes it non-destructive — it does not make it safe.
Adding a `NOT NULL` column, a unique index, or a `CHECK` constraint still breaks the
older release still serving traffic during a deploy. Expand/contract: add nullable,
backfill, switch reads, tighten in a *later* deploy.

Nothing enforces this mechanically. It is a review rule.

The per-deploy dump is insurance against a migration corrupting data — not a rollback
mechanism. Restoring it discards every write since it was taken. Continuous archiving
(WAL-G, pgBackRest) is the real recovery story and is not yet set up.

## What is running right now

```bash
tsactl status
```

Needs no privilege and takes no lock, so it is safe to run mid-deploy and from any
account. Exits non-zero when the service is down, unhealthy, or serving a commit
other than the one `current` points at — so it also works as a monitoring probe.

The raw form, if the tooling is not installed:

```bash
readlink /srv/tsa-api/current
curl -s http://127.0.0.1:8081/health | jq .commit
```

Trust the second one. The symlink says what *should* be running; the health endpoint
says what *is*. `tsactl status` compares them for you and shouts when they disagree —
that mismatch means a restart silently did nothing and the old release is still
serving.

## The audit trail

`deploy.sh` and `rollback.sh` mirror every step to journald under the `tsa-deploy`
tag, attributed to `SUDO_USER` rather than the service account:

```bash
tsactl audit                            # last 100 entries
journalctl -t tsa-deploy --since -7d    # the same thing, unwrapped
```

Failures are recorded too — a refused deploy is as interesting afterwards as a
successful one. Logging is strictly best-effort (`|| true`, and `logger` is not a
preflight dependency): an audit-log failure must never be the thing that aborts a
deploy.

## One-off migrations

`deploy/one-off/` holds scripts that boot migration deliberately refuses to run. Read
the header before running any of them; they are not idempotent by assumption and
several destroy data. Apply with `psql` before the deploy that needs them.
