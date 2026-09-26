# tsa-web — TSA Connect on the web

The TSA Connect web app: a signed-in TSA Pay merchant manages their business from a
browser — payments, payouts, API keys, webhooks and return domains — and the public
developer docs live at `/docs`. It starts with the Business (TSA Pay) area; the personal
account is a single page for now.

React 19 · Vite 7 · TypeScript · Tailwind 4 (TSA Signature tokens, mirrored from
`tsa-app/theme` in `src/styles/tokens.css`) · React Router 7 · TanStack Query 5 · Radix UI.

## How it talks to the API

- **Same origin, always.** The page and `/api/*` are served from one host — in dev by
  Vite's proxy, in production by nginx. The session is `HttpOnly; Secure; SameSite=Strict`
  host-only cookies set by the Go API (`/api/auth/web/*`); the page never sees a token.
- **One request helper** (`src/lib/api.ts`): the `{success, message, data}` envelope, the
  `X-TSA-Web: 1` CSRF header on every call, a 30 s timeout, and one refresh-and-retry on
  an uncoded 401 (a 401 with a `code`, such as `password_incorrect`, is a refusal).
- **No web storage.** Nothing goes in `localStorage`, `sessionStorage` or IndexedDB (ESLint
  refuses it, and the end-to-end run checks it). UI state lives in the URL or components.
- Sensitive changes (keys, webhooks, the signing secret, a crypto payout wallet) are
  guarded: password, then a code we email, then confirm.

## Develop

Needs Node 22 and the Go API running locally on `:5000` (its default; `API_URL` overrides
the proxy target) with, in `tsa-api-go/.env`:

```bash
WEB_APP_ORIGIN=http://localhost:5174     # turns the web sign-in routes on
TSA_PAY_ENABLED=true                     # the Business area's routes
TSA_PAY_WEBHOOK_MASTER_KEY=<64 hex chars>
MAILJET_API_KEY=… MAILJET_SECRET_KEY=…   # sign-in codes are emailed; without a mailer sign-in answers 503
```

```bash
npm install
npm run dev          # http://localhost:5174
```

The session cookies are `Secure`; Chrome and Firefox keep them on `http://localhost`.

To run the local web app against the **production** API instead (no local API needed):

```bash
TSA_PROD_API=yes npm run dev:prod-api   # http://localhost:5174 → https://tsa.mcgpchain.com/api
```

`vite.prodapi.config.mjs` presents prod's `WEB_APP_ORIGIN` through the dev proxy so the
cookie session works on localhost. It is real data: a new key, a webhook or payout change
hits the live merchant, and every page shows a red bar to say so. The prod session cookies
sit on `localhost`, where any other local server can read them — **sign out when you're
done** (that revokes the session). A stopgap until app.tsaconnectworld.com is live.

## Test

```bash
npm test             # Vitest + Testing Library (jsdom, TZ=UTC)
npm run e2e          # Playwright; first time: npx playwright install chromium
npm run lint         # ESLint
npx tsc -b           # types
```

The Playwright run (`e2e/`) uses a **mocked API** inside the browser (`page.route`,
`e2e/fixtures.ts`): sign-in and every guarded change need a code that is only ever emailed,
so a browser test cannot drive a real server. The mock uses the real routes, response
shapes and status codes; the server flows themselves (codes, cookies, CSRF, origin checks,
guarded actions) are covered by the Go tests. It starts its own Vite server on port 5180.

## Build

```bash
npm run build        # tsc -b && vite build → dist/
```

## Deploy (app.tsaconnectworld.com)

The API must be deployed and migrated first (step 2 below), whichever host serves the app.

### Netlify (recommended — admin and landing are already there)

1. **API:** step 2 of the nginx section below (deploy + migrate, `WEB_APP_ORIGIN`). Also:
   - `NETLIFY_PROXY_SECRET=<long random>` in `/srv/tsa-api/shared/env`, then restart.
   - Install `tsa-api-go/deploy/nginx-cloudflare-realip.conf` (see that deploy README) so
     nginx sees real client addresses behind Cloudflare.
2. **Netlify site:** new site from the `tsa-dev` repo, **base directory `tsa-web`**; build and
   routing come from `netlify.toml`. Add the env var **`API_SIGNATURE_TOKEN`** with the SAME
   value as `NETLIFY_PROXY_SECRET`, **scope: Runtime** (signed proxies need it at runtime).
   `/api/*` is proxied to `https://tsa.mcgpchain.com/api/*` so the browser sees one origin —
   the SameSite=Strict session cookies and the API's Origin check both depend on that. Never
   point the app at the API's own domain instead. The proxy is signed (`x-nf-sign`): the API
   verifies it and only then counts the request as coming from the visitor Netlify names
   (`X-Nf-Client-Connection-Ip`) instead of from Netlify — otherwise every web visitor would
   share Netlify's address and one rate-limit budget.
3. **Domain:** add `app.tsaconnectworld.com` to the site. In Cloudflare make the record
   **DNS only (grey cloud)**: proxied through Cloudflare, Netlify would see Cloudflare's
   address instead of the visitor's (and Netlify advises against it).
4. **Checks, after DNS resolves:**
   - Origin accepted: `curl -s -o /dev/null -w '%{http_code}' -X POST https://app.tsaconnectworld.com/api/auth/web/login
     -H 'X-TSA-Web: 1' -H 'Origin: https://app.tsaconnectworld.com'` → `400` (a `403` with
     `"code":"origin"` means `WEB_APP_ORIGIN` does not match).
   - Sign in fully once in a browser: DevTools → Application → Cookies shows `tsa_at` / `tsa_rt`
     host-only for `app.tsaconnectworld.com` (Set-Cookie passes through the proxy).
   - Visitor address: while signing in, `sudo journalctl -u tsa-api -f` on the server shows your
     own public IP on the `/api/auth/web/*` lines, not a Netlify one. Repeat with a forged
     `-H 'X-Nf-Client-Connection-Ip: 1.2.3.4'` on the curl above: the log must still show your
     real IP (Netlify overwrites the header; if it shows `1.2.3.4`, stop and report).
   - Cloudflare → `tsa.mcgpchain.com` → Security: Bot Fight Mode / WAF must not challenge
     Netlify's traffic (a challenge shows up as an HTML 403/503 from `/api`).

### nginx on the API server

Order matters: the API first, then the files, then nginx.

1. **DNS and TLS first:** an A record for `app.tsaconnectworld.com` → the API server, then
   the certificate on its own: `certbot certonly --nginx -d app.tsaconnectworld.com`. The site
   file (step 4) names the certificate paths, so it cannot pass `nginx -t` until they exist.
2. **API:** run `tsa-api -migrate` with the new binary **before** it starts serving. The new
   schema: tables `web_signin_challenges`, `web_trusted_browsers`, `pay_owner_actions`;
   column `refresh_tokens.web`; index on `pay_payments`. Critical: every app login writes
   `refresh_tokens.web` and every app refresh filters on it. If the binary serves before
   the migration, app refreshes fail and users are signed out. Password reset and account
   deletion also write `web_trusted_browsers`. `tsa-api-go/deploy/deploy.sh` already
   migrates first; a hand deploy must do it too. Deploy the build
   with `WEB_APP_ORIGIN=https://app.tsaconnectworld.com` in `/srv/tsa-api/shared/env` (the
   only env file production reads). Check the web routes are on:
   `curl -s -X POST https://tsa.mcgpchain.com/api/auth/web/login` answers `403` with
   `"code":"csrf"` (a `404` means `WEB_APP_ORIGIN` did not load).
3. **Web:** `npm ci && npm run build`, copy `dist/` to `/srv/tsa-web/releases/<sha>/` and
   repoint the symlink: `ln -sfn /srv/tsa-web/releases/<sha> /srv/tsa-web/current`. Rolling
   back is repointing `current` at the previous release.
4. **nginx:** install `deploy/nginx.conf.example` (see its header), then `nginx -t`, then
   `systemctl reload nginx`. It proxies `/api/` to the existing `tsa_api` upstream, so a
   blue/green API cutover needs no change here. The site sets its own `gzip_types`:
   `grep gzip_types /etc/nginx/nginx.conf` shows the line commented out on stock Ubuntu,
   which compresses only HTML. Confirm the scripts are compressed after the reload:
   `curl -sI -H 'Accept-Encoding: gzip' https://app.tsaconnectworld.com/assets/<index-hash>.js`
   answers `Content-Encoding: gzip`.
