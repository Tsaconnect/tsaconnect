# TSA API (Go)

Go (Gin) REST API with PostgreSQL/GORM backend for a trading/asset management platform. Includes multi-chain wallet functionality (Sonic, BSC).

## Commands

```bash
go build ./cmd/server          # Build server binary
go run ./cmd/server            # Run server, default port 5000
go build ./...                 # Build all packages
go mod tidy                    # Sync dependencies
go test ./... -v -count=1      # Run all tests
go test ./internal/handlers -v # Run handler tests only
go test ./internal/blockchain -v # Run blockchain client tests only
```

## Architecture

```
cmd/server/main.go             # Entry point - Gin setup, graceful shutdown
internal/
  config/
    config.go                  # Config struct from env vars, multi-chain config
    database.go                # PostgreSQL connection (GORM), AutoMigrate
    cloudinary.go              # Cloudinary client init
  models/                      # GORM models (User, Product, Cart, Category, Transaction, Wallet, WalletTransaction, SupportedToken, VerificationLog)
                               # NOTE: no model stores a crypto balance. Wallet balances are read from
                               # the chain per request; the Asset/Portfolio mirrors were removed 2026-09-01.
  blockchain/
    client.go                  # EVMClient - chain-agnostic Ethereum RPC wrapper
    token.go                   # ERC-20 token operations, native transfer prep
    client_test.go             # EVMClient unit tests
    token_test.go              # Token operation tests
  middleware/
    auth.go                    # JWT Bearer token auth
    admin_auth.go              # Admin/merchant role check
    cloudinary.go              # Cloudinary upload helper
    rate_limiter.go            # IP-based rate limiting (100 req/15 min)
    cors.go                    # CORS configuration
    logger.go                  # Request logging
    middleware.go              # Middleware aggregator
  handlers/
    handlers.go                # Handlers struct + stub methods (central aggregator)
    auth_handler.go            # Signup, Login, Identity, Facial
    user_handler.go            # Profile, password, referrals, admin user mgmt
    verification_handler.go    # BVN/document/facial verification, admin approval
    product_handler.go         # Product CRUD, marketplace, category filtering
    category_handler.go        # Category CRUD with tree structure
    cart_handler.go            # Cart operations, checkout, coupon, shipping
    transaction_handler.go     # Transaction history (read-only)
    market_handler.go          # Market overview, price history, search
    upload_handler.go          # File upload handlers
    wallet_handler.go          # Multi-chain wallet: register, balances, send, history
    wallet_handler_test.go     # Wallet handler unit tests (SQLite in-memory)
    common.go                  # getUserFromContext + callerWallet/requireOwnWallet (device/account wallet reconciliation)
    wallet_proof.go            # wallet-ownership challenge, proof check, requireProvenWallet
  services/
    price_service.go           # CoinGecko API with 60s cache
    blockchain_service.go      # Multi-chain BlockchainService registry
  utils/
    validators.go              # BVN, phone, password, email validators
    response.go                # SuccessResponse, ErrorResponse helpers
  routes/
    routes.go                  # All route definitions with middleware
```

## Multi-Chain Wallet Architecture

- **EVMClient** (`blockchain/client.go`): Chain-agnostic Ethereum RPC wrapper. Constructor: `NewEVMClient(rpcURL, chainID)`.
- **BlockchainService** (`services/blockchain_service.go`): Registry of `map[string]*EVMClient` keyed by chain name. Methods: `ClientForChain(name)`, `ClientForChainID(id)`, `TokenAddress(chain, symbol)`.
- **ChainConfig** (`config/config.go`): `Name`, `RPCURL`, `ChainID`, `NativeCurrency`. Stored in `Config.Chains` map.
- **Token addresses**: Keyed as `"chain:symbol"` (e.g., `"sonic:MCGP"`). Each chain's own native coin (`ChainConfig.NativeCurrency`: S, BNB, POL, ETH, AVAX — see `BlockchainService.IsNativeCurrency`) uses `PrepareNativeTransfer`; ERC-20 tokens use `PrepareERC20Transfer`.
- **Supported tokens**: DB-driven via `SupportedToken` model. Auto-seeded from hardcoded defaults if table is empty.

## API Routes

All routes prefixed with `/api/`:
- `/auth` - signup (3-step), login
- `/users` - user profile management
- `/verification` - document/identity verification
- `/upload` - image uploads via Cloudinary
- `/transactions` (read-only), `/market` - transaction history, market data
- `/products`, `/cart` - marketplace/e-commerce features
- `/wallet` - multi-chain wallet (register address, balances, send, history, supported tokens)
- `/health` - health check (root level)

## Key Patterns

- **Response format**: `{"success": bool, "message": string, "data": object}` or `{"success": false, "errors": array}`
- **Auth**: JWT tokens with 7-day expiry. `Authorization: Bearer <token>` header
- **Rate limiting**: 500 requests per 15 minutes per IP on `/api/` routes (`routes.go`), with tighter per-route limits on some groups. "IP" must be the real client: nginx restores it behind Cloudflare (`deploy/nginx-cloudflare-realip.conf`), and `middleware.NetlifyClientIP` restores it for the web app's signed Netlify proxy (`NETLIFY_PROXY_SECRET`; unset with `WEB_APP_ORIGIN` set is a boot `ALERT netlify_proxy_secret_unset=1`, and a visitor address that fails the signature logs `netlify_client_ip_untrusted=1` at most once a minute)
- **Handler structure**: `WalletHandler` has its own struct with `DB`, `Config`, `BlockchainService` fields. Other handlers use `*Handlers` struct.
- **ORM**: GORM with PostgreSQL driver
- **Wallet tests**: Use SQLite in-memory DB (raw DDL, not AutoMigrate) to avoid PostgreSQL-specific defaults
- **Balances are never stored**: crypto balances come from the chain on every request and
  entitlement balances from the swap contract. Endpoints that display one take the caller's
  device wallet address and reconcile it against `users.wallet_address` — never silently
  substitute the registered address, which makes a device with no key look as though the
  platform is custodying the user's assets.
- **A wallet is linked only with proof of ownership** (`handlers/wallet_proof.go`). The operator
  spends Spendable MCGP from `users.wallet_address` with no user signature, so re-pointing it
  needs a signed `POST /api/wallet/challenge` (EIP-191) sent to `/wallet/register` or in the
  `X-Wallet-Proof-*` headers; a wallet the account proved before needs none. Reads use
  `requireOwnWallet` (never links; 403 on mismatch), writes use `callerWallet` (links only with
  proof; 403 `WALLET_PROOF_REQUIRED`), and every operator debit (IT sell, bills,
  pay-spendable) and the name-change fee go through `requireProvenWallet`. A valid proof
  evicts holders of the same wallet that never proved it (pre-fix links; `AUDIT wallet_link_evicted=1` log line); a
  proven holder is never evicted (409). Addresses are stored checksummed and unique
  case-insensitively (`config/wallet_address_index.go`, built CONCURRENTLY on Postgres; case
  duplicates it cannot resolve are logged `ALERT wallet_address_case_duplicates`). The signed
  message is a fixed template the app parses line for line — change both sides together.
  `deploy/one-off/2026-09-28-wallet-link-inflight-debits.sql` lists operator debits in flight
  from unproven or shared wallets (read-only).
- **PostgreSQL-backed tests** use `testutil.TempPostgres` (throwaway schema; skip without
  `DATABASE_URL`).
- **Destructive DDL**: never in `AutoMigrate` (enforced by `TestAutoMigrateHasNoDestructiveDDL`).
  It goes in `deploy/one-off/`, run deliberately by an operator after the release is live.

## Environment Variables

Required (set in `.env`):
- `DATABASE_URL` - PostgreSQL connection string
- `JWT_SECRET` - JWT signing secret
- `FRONTEND_URL` - CORS allowed origin (defaults to `http://localhost:3000`)
- `PORT` - Server port (defaults to `5000`)
- `ENV` - `development` or `production`. Normalised at load (trim, lower-case, "prod" ->
  "production") — call `cfg.IsProduction()`, never compare `cfg.Env` directly, so a stray
  space or differing case can't desync a guard.
- `CLOUDINARY_CLOUD_NAME`, `CLOUDINARY_API_KEY`, `CLOUDINARY_API_SECRET`
- `MIGRATE_ON_BOOT` - run schema migration during boot. Leave unset wherever a deploy
  script exists: the deploy runs `tsa-api -migrate` once, before starting the service,
  so processes never race on DDL. Set `true` for local development.

Raw signer keys (IT operator, gasless relayer, ramp gas sponsor, PM release local signer —
`IT_OPERATOR_PRIVATE_KEY`, `RELAYER_PRIVATE_KEY`, `RAMP_SPONSOR_PRIVATE_KEY`,
`PM_RELEASE_PRIVATE_KEY`):
- Each is a dev/staging convenience: the key lives in process env, not a key store. In
  production every one of them is refused by default — `cmd/server/signer_guard.go`'s
  `rawSignerKeyAllowed(cfg)` is the single guard all four (and the PM local signer branch)
  go through, and the corresponding feature (IT + utility bills, gasless relaying, ramp gas
  sponsorship, PM auto-settle) stays dormant rather than signing with a key that shouldn't
  be there.
- `ALLOW_RAW_SIGNER_KEYS` (bool, default `false`) is the interim opt-in until a KMS-backed
  `ReleaseSigner` exists (`internal/services/release_signer.go` — `PM_RELEASE_SIGNER=kms` is
  not yet implemented). Ignored outside production. When it lets a raw key through in
  production, boot logs `ALERT raw_signer_key_in_production=1 key=<NAME>` once per key —
  never the key material — so the exposure is visible in every log sink.
- **Rollout order matters.** As of 2026-09, production runs with `ENV` not exactly
  `"production"` — that is what currently keeps IT/bills/gasless/ramp-sponsorship alive with
  their raw keys. Flipping `ENV=production` alone would silently take all of them dormant.
  Set `ENV=production` and `ALLOW_RAW_SIGNER_KEYS=true` together in the same deploy, and
  before doing so review the other things gated on `cfg.IsProduction()`: gin switches to
  `ReleaseMode`, and the nil-chain-client fallbacks in `escrow_service.go`,
  `mcgp_swap_service.go`, `otc_service.go` and `service_contact_service.go` start refusing to
  build a partial unsigned tx instead of returning one for local tooling.

IT sell recovery (`internal/services/it_sell_recovery.go`, `it-sell-reconciler` job):
- `IT_SELL_FW_ABSENCE_TRUSTED` (bool, default `false`) - whether "Flutterwave's
  `GET /transfers` listing has no transfer for this payout reference" may be acted on:
  the reconciler re-sending an already-attempted payout or reversing a sell whose retry
  was refused, and an admin reverse / retry-payout on that evidence. Leave `false` until
  the listing's response shape is confirmed against the live API — a wrong "absent" pays
  the user twice. While `false` those sells ALERT and wait in `GET /api/it/admin/sell/stuck`;
  provider-confirmed SUCCESSFUL/FAILED transfers and first sends still resolve normally.
- Money rule for every sell path: a payout send and a debit reverse claim the same row
  (`payout_rejected`), so exactly one can win; a payout is only sent while the on-chain
  debit is Pending; a settle/reverse that did not verify but whose debit is already
  resolved on-chain parks the sell as `needs_review`.
- **A sell is parked only on facts that cannot un-happen** — a debit already RESOLVED, or
  one for another wallet/amount. "No debit at this ref" is a lagging RPC node as often as
  it is a missing debit, so it (and an unreadable chain) holds the payout for a later tick
  and alerts past the 30-minute abandon window. `POST /api/it/admin/sell/:id/reevaluate`
  (super_admin) is the exit from `needs_review`: it re-reads the chain and the payout
  transfer and closes the row the way the evidence points, refusing when they disagree.

Central-account inbound attribution (default `va` — a deploy that omits all of these changes nothing):
- `INBOUND_MODE` - `va`: per-user Flutterwave virtual account, attributed by the charge
  webhook. `central`: every payer is sent to one company account, attributed by polling the
  wallet statement and matching the sender's name. Anything unrecognised reads as `va`.
- These are the only variables here that can REFUSE TO BOOT. `central` requires
  `INBOUND_CENTRAL_ACCOUNT_NUMBER` (validated as a 10-digit NUBAN), `INBOUND_CENTRAL_BANK_NAME`,
  `INBOUND_CENTRAL_ACCOUNT_NAME` and Flutterwave credentials. The usual dormant-if-unset
  pattern is wrong for them: a pay-into screen with a blank or unwatched account number takes
  real customer money that nothing can attribute afterwards.
- `INBOUND_POLL_INTERVAL`, `INBOUND_STATEMENT_LOOKBACK` - wallet funding fires no webhook, so
  the poll is the only thing that ever notices a credit and the interval is the payer's wait.
- `INBOUND_ALERT_EMAILS` - who is paged when the matcher holds a credit. Nothing releases a
  held credit automatically; unset is legitimate but means the admin queue is the only place
  it is ever seen. The same list also receives background-job failure alerts (below).
- Per-variable rationale is in `.env.example`; read it before changing any of them.
- Payment-merchant P2P under central routing (`p2pCentralRouting` in `cmd/server/main.go`):
  - A payment matched to a trade still has to pass `PMService.CheckAutoSettle`: the merchant
    is active, and the trade is within the per-trade and daily caps. The VA webhook uses the
    same check.
  - A refusal holds the credit (`merchant_is_not_active` / `trade_not_within_merchant_cap`).
    A super-admin assigning that credit is the override.
  - The PM manual queue then lists only `paid` orphans. `ManualRelease` refuses unattributed
    trades (`ErrSettleViaInboundAssign`), because releasing one pays out the crypto while the
    credit stays held and the merchant unpaid.
  - Activation mints no virtual account and takes no BVN/NIN. It requires the merchant's own
    Didit verification instead.
  - A trade under Flutterwave's ₦100 transfer floor is held (`payout_below_transfer_minimum`)
    rather than released and then failed at the bank.
  - Merchants are forwarded the full trade fiat. The Flutterwave costs (1.40% wallet funding plus
    the transfer fee) are operations costs, which the admin-set platform fee is meant to cover
    (owner, 2026-10-05).

Background-job failure alerts (`internal/jobs/scheduler_alert.go`):
- A worker registered with `jobs.AlertOnFailureStreak()` alerts when it has failed every tick
  for ~5 minutes (`ceil(5m / interval)` consecutive failures, never fewer than 3; override with
  `jobs.AlertAfterFailures(n)`). Register every worker whose silent death leaves money
  unattributed, unpaid or unreconciled with it — the inbound poller and reconciler failed every
  tick for six days (2026-09-11..17) with only a `tick error` log line to show for it.
- Log lines: `[jobs] ALERT worker_failing_total=1 worker=… consecutive_failures=… failing_for=…
  since=… last_error="…"`, repeated at most hourly while the streak lasts, then
  `[jobs] RECOVERED worker_recovered_total=1 …` on the first success. Boot logs which workers are
  watched and at what threshold.
- Email goes to `INBOUND_ALERT_EMAILS` through `InboundHoldNotifier` (no new variable; blank = log
  only). Error text passes through `services.RedactAlertText` (URL paths/queries, userinfo,
  bearer/JWT, credentials as `key=value` AND JSON `"key": "value"`, Flutterwave `FLWSECK-` keys,
  64-hex, DB constraint values, emails; 500-byte cap) first.

Marketplace checkout "pay with another token" (`services/marketplace_funding.go`, routes
`/api/orders/funding/*`, `marketplace-funding-reconciler` job):
- One LiFi swap lands the order token (Sonic USDC/USDT) in the BUYER's own wallet on Sonic,
  then the unchanged escrow pays each order from it — ProductEscrowV2 records `msg.sender` as
  the buyer, so the funds cannot go anywhere else. It reuses the TSA Pay delivery machinery
  (`RampSellQuoter` in Delivery + `PassThrough` mode, `SellIntentService` intents with
  `purpose='marketplace'` + `reference_id` = the orders' `checkout_group_id`, the Sonic
  `ConfirmDelivery` proof) and builds delivery-only copies when Paycrest is off.
- Pass-through pricing: no TSA fee; the only integrator fee is the gas we sponsor (source-chain
  top-up, S on Sonic for the escrow txs). No share-of-the-order cap: she sees the cost before
  signing and is refused only when her wallet can't cover what the route takes (2026-10-01). `MARKETPLACE_FUNDING` (default OFF) turns it on;
  `MARKETPLACE_ESCROW_GAS_PER_ORDER` sizes the S top-up. The Sonic top-up is the funding's OWN
  lane (`MARKETPLACE_SONIC_GAS`, default off, ramp sponsor key, own floor/ceiling/guard) — never
  a `RAMP_SPONSOR_CHAINS` lane, so enabling it changes nothing for sells or TSA Pay. Every
  refusal it could meet (KYC, ceiling, float, budget, daily cap) is asked at quote time, before
  she signs; without the lane a buyer with no S is refused up front.
- Stock held with no funds is earned: `fundWithSwap` orders need the proven wallet and at most
  one such checkout per buyer; the orders are kept alive only once LiFi sees her swap (10 min
  grace after she reports a hash), never past the intent's creation + 2h; an unseen hash is
  closed after 1h.
- Re-check LiFi's Sonic destinations with `scripts/lifi-sonic-checkout-probe.sh` (read-only)
  when a source starts failing: a toToken other than the configured address is refused.

Didit KYC (dormant if unset — `/api/kyc/session` returns 503):
- `DIDIT_API_KEY` - Application API key from https://business.didit.me
- `DIDIT_WORKFLOW_ID` - Published KYC workflow UUID
- `DIDIT_WEBHOOK_SECRET` - Destination `secret_shared_key` from webhook setup
- `DIDIT_CALLBACK_URL` - Hosted-flow redirect after completion (default `tsaconnect://kyc/callback`)
- `DIDIT_BASE_URL` - Defaults to `https://verification.didit.me/v3`

Blockchain (wallet feature):
- `SONIC_RPC_URL` - Sonic testnet RPC (default: `https://rpc.testnet.soniclabs.com`)
- `SONIC_CHAIN_ID` - Sonic chain ID (default: `14601`)
- `BSC_RPC_URL` - BSC testnet RPC (default: `https://data-seed-prebsc-1-s1.binance.org:8545`)
- `BSC_CHAIN_ID` - BSC chain ID (default: `97`)
- `MCGP_TOKEN_ADDRESS` - MCGP token contract (Sonic)
- `USDT_TOKEN_ADDRESS` - USDT token contract (Sonic)
- `USDC_TOKEN_ADDRESS` - USDC token contract (Sonic)
- `BSC_USDT_TOKEN_ADDRESS` - USDT token contract (BSC)
- `BSC_USDC_TOKEN_ADDRESS` - USDC token contract (BSC)

P2P trading (Sonic) and the TSA Liquidity pool chain (BSC):
- `SONIC_NETWORK` - `"mainnet"` | `"testnet"` (default `mainnet`) — selects the Sonic P2P preset
- `P2P_ESCROW_ADDRESS` - Sonic P2PEscrowV2 proxy; overrides the preset (hot-fix escape hatch)
- `P2P_ESCROW_EIP712_VERSION` - EIP-712 domain version matching the deployed Sonic contract (default from preset)
- `BSC_NETWORK` — `"mainnet"` | `"testnet"` (**default `mainnet`**). It selects the BSC preset row, which is the **TSA Liquidity pool** (`internal/config/p2p_presets.go`). An empty row means the pool is dormant.
- `P2P_ESCROW_ADDRESS_BSC` / `P2P_ESCROW_EIP712_VERSION_BSC` — **retired**: boot refuses to start if either is set.
- **LPs (TSA Liquidity plan B):** an LP is a `payment_merchants` row with `kind = 'lp'` (never also a merchant). It applies with a `liquidity_provider` merchant request, a P2P admin approves it, and it activates with a server-resolved settlement-name match. Only an active LP reaches the pool chain (`openableFor` in `services/p2p_pool_lp.go`); its stock ad never lists (`p2p_pool_hidden.go`). Limits are `fee_configs` rows `pool_lp_min` / `pool_lp_max` (ensured at boot). LP reads: `GET /api/liquidity/me`, `/me/activity`. Boot wiring is `cmd/server/liquidity.go`: with no pool row (production until the BSC mainnet row is filled) the `/liquidity` routes are not registered, the app's LP CTA is hidden (`/p2p/pm/me` `liquidityAvailable: false`), applications answer 503 and the `liquidity-voucher-renewal` job (6 h, failure-streak alerted) is not registered.

P2P trade-milestone emails (opened/paid/released/cancelled/disputed/resolved/expired) are sent to both parties via `notification_service.go` (role-aware, respect `mute_email`); per-message chat does not email. No CTA link — the app is mobile-only with no universal-link setup, so the email tells the user to open the app.

## Dependencies

Key packages:
- `github.com/gin-gonic/gin` - HTTP framework
- `gorm.io/gorm` + `gorm.io/driver/postgres` - ORM + PostgreSQL driver
- `gorm.io/driver/sqlite` - SQLite driver (test only)
- `github.com/ethereum/go-ethereum` - Ethereum client library
- `github.com/golang-jwt/jwt/v5` - JWT auth
- `golang.org/x/crypto/bcrypt` - Password hashing
- `github.com/cloudinary/cloudinary-go/v2` - Image uploads
- `github.com/joho/godotenv` - Env file loading
- `github.com/google/uuid` - UUID generation
