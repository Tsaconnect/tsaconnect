# Deploying tsa-api-go

## Layout

```
/srv/tsa-api/                 owned by tsa-deploy
  repo/                       clone; fetched, never built in place
  releases/<stamp>-<sha>/     git worktree + built binary
  current -> releases/...     what is running
  bin/                        migrate.sh, backup.sh (run by systemd oneshots)
  shared/env                  0600 + ACL u:tsa:r, service config — written from Doppler
  shared/env.prev             0600, the file before the last Doppler sync
  shared/doppler-token        0600, read-only Doppler service token for tsa-app/prd
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
# no shell expansion, no CRLF. systemd strips one pair of surrounding double
# quotes, which is what Doppler writes. After this first install, the content
# comes from Doppler (see "Configuration" below); the ACL is what must survive.
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
# Real client addresses behind Cloudflare (every per-IP rate limit depends on it).
sudo install -m 644 $D/nginx-cloudflare-realip.conf /etc/nginx/conf.d/cloudflare-realip.conf
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

Rolling back past a release that enforces a setting stops enforcing it silently: the column
and the merchant's dashboard stay, the check does not. TSA Pay's live-key IP allowlist
(`allowed_ips`) is one — tell merchants who rely on it if a rollback crosses it.

## Configuration (Doppler)

Production config lives in Doppler, project `tsa-app`, config `prd`. `shared/env` is
the copy systemd reads, rewritten from Doppler by `deploy/env-sync.sh`. **Change
values in Doppler only**: a hand edit to `shared/env` is overwritten by the next sync.

```bash
tsactl env-sync --dry-run     # which keys differ from Doppler (names only, never values)
tsactl env-sync --restart     # write shared/env, restart, wait for health
tsactl env-sync               # write only; the next restart or deploy picks it up
```

Every `tsactl deploy` also syncs, before the migration (which reads `shared/env`
too). If the deploy then fails — migration, restart or health — it puts the previous
file back along with the previous release. A Doppler outage never blocks a deploy:
the sync fails, says so, and the release ships on the config already in place.

The sync refuses, leaving `shared/env` untouched, when the download is empty, lacks
`DATABASE_URL` or `JWT_SECRET`, has a carriage return or a non-`KEY=value` line, or
would remove more than 3 keys at once (`--allow-removals` for a deliberate cleanup).
With `--restart`, a service that does not come back healthy on the same commit
within 60s gets the previous file back and another restart.

**Never `doppler login` on this box.** A login stores a personal token that can write
to every project. The box gets a read-only service token scoped to `prd` alone, which
can be revoked by itself if the box is compromised again.

### One-time setup

```bash
# 1. The CLI (Doppler's apt repository — check https://docs.doppler.com/docs/install-cli)
sudo apt-get install -y apt-transport-https ca-certificates curl gnupg
curl -sLf --retry 3 --tlsv1.2 --proto "=https" \
  'https://packages.doppler.com/public/cli/gpg.DE2A7741A397C129.key' \
  | sudo gpg --dearmor -o /usr/share/keyrings/doppler-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/doppler-archive-keyring.gpg] https://packages.doppler.com/public/cli/deb/debian any-version main" \
  | sudo tee /etc/apt/sources.list.d/doppler-cli.list
sudo apt-get update && sudo apt-get install -y doppler

# 2. The token: Doppler dashboard → tsa-app → prd → Access → service token, READ only.
#    Paste it in an editor, not with echo, so it stays out of shell history.
sudo install -m 600 -o tsa-deploy -g tsa-deploy /dev/null /srv/tsa-api/shared/doppler-token
sudo -u tsa-deploy nano /srv/tsa-api/shared/doppler-token

# 3. tsactl gained env-sync; the root-owned copy on PATH must be reinstalled.
tsactl update
sudo install -m 755 -o root -g root \
    /srv/tsa-api/repo/tsa-api-go/deploy/tsactl /usr/local/bin/tsactl

# 4. First sync: dry run, and read the key list before writing anything. Quoting
#    differences are ignored; every key it names has a different value in Doppler.
tsactl env-sync --dry-run
tsactl env-sync --restart
```

Until `shared/doppler-token` exists, `env-sync.sh` is a no-op and deploys behave
exactly as before.

## Cloudflare: refuse plain HTTP to the merchant API

A TSA Pay merchant sends its secret key on every request (`Authorization: Bearer
tsapk_…`). Cloudflare's zone-wide **Always Use HTTPS** answers `http://` with a
301, but by then the key has already crossed the network in the clear, and
server-side HTTP clients ignore HSTS. So the merchant API, `/api/pay/v1/`, must
refuse plain HTTP, not redirect it. Always Use HTTPS cannot exempt a path, so it is replaced by a Single
Redirect that does the same job everywhere else. Redirect rules run before WAF
custom rules, so the block below sees the request.

Set this up in the Cloudflare dashboard for the zone. Create both rules first:
while Always Use HTTPS is on it wins, so they are inert and there is no gap.

1. **Rules → Redirect Rules → Create (Single Redirect)**, "HTTP to HTTPS except the merchant API":
   - When: `(not ssl and not starts_with(http.request.uri.path, "/api/pay/v1/"))`
   - Then: Dynamic, `concat("https://", http.host, http.request.uri.path)`, 301, **Preserve query string** on.
2. **Security → WAF → Custom rules → Create**, "Refuse plain-HTTP TSA Pay":
   - When: `(not ssl and starts_with(http.request.uri.path, "/api/pay/v1/"))` — every host, so
     another name pointing at this origin cannot keep the old redirect.
   - Then: **Block**. A custom response needs Pro or above; if the plan has it, use 403, `application/json`:
     `{"success":false,"message":"TSA Pay only accepts https. A key sent over http has leaked, so create a new one.","code":"https_required"}`
   - Put it above any rule that skips custom rules, and check no IP Access "Allow" rule matches
     merchants: either would let a plain-HTTP request through to the origin.
3. **SSL/TLS → Edge Certificates → Always Use HTTPS → Off.** Leave HSTS on.

If a request gets past the edge anyway, the API refuses it itself: the merchant API answers
`403 https_required` when Cloudflare's `CF-Visitor` says the visitor used http
(`middleware.PayMerchantTransport`), so the answer does not go back in the clear either.

Check it:

```bash
curl -s -o /dev/null -w "%{http_code}\n" -X POST http://tsa.mcgpchain.com/api/pay/v1/payments   # 403, not 301
curl -s -o /dev/null -w "%{http_code} %{redirect_url}\n" http://tsa.mcgpchain.com/api/health  # 301 → https
curl -s -o /dev/null -w "%{http_code}\n" -X POST https://tsa.mcgpchain.com/api/pay/v1/payments  # 401 (no key)
```

Any other host added to the zone later is redirected by rule 1, just as Always
Use HTTPS did.

When Cloudflare publishes new ranges, update `nginx-cloudflare-realip.conf` and
`paySharedEgress` in `internal/services/pay_merchant_service.go` together — a test fails
while they differ. A stale nginx list makes clients look like Cloudflare addresses: every
merchant with an allowlist is then refused (logged as `shared_egress=true`).

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
