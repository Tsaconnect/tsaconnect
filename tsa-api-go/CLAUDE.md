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
- **Three hot keys, one lane per key and chain** (signer consolidation 2026-10-13 + owner
  decisions the same day; `docs/plans/2026-10-13-signer-key-consolidation.md` outside the repo).
  OPERATOR = `PM_RELEASE_PRIVATE_KEY`: PM/pool releases and cancels, and the IT operator and gas
  sponsor whenever their own var is unset (`roleKey`, resolved where each signer is built — the operator secret is never copied into the role's config field; raw-key ALERTs name the var actually used, e.g. `key=PM_RELEASE_PRIVATE_KEY role=gas_sponsor`; boot lines `[signer] role=… signs
  with the operator` / `[signer] DEPRECATED role=…`). RELAYER = `RELAYER_PRIVATE_KEY`, its own key:
  never falls back (unset = gasless dormant), and boot refuses it equal to any other key; it relays
  only while its key reads clean of swap owner/operator/guardian — at boot and on the 60 s
  `relayer-role-probe` job (failure-streak alerted, `ALERT relayer_role_check_failed=1`); a failed
  check takes only gasless down, never the API. TREASURY = `RAMP_TREASURY_PRIVATE_KEY`.
  `assertDistinctKey` is class-based: only operator roles may share a key; the company wallet must
  differ from all of them.
- **KeyLane** (`services/release_signer_lanes.go`, store `services/key_lane_store.go`): every
  signing path takes its lane from the one `SignerRegistry` (`BlockchainService.Signers()`), keyed by
  (address, chain id); boot prints `[signer] operator=… roles=… lanes=[…]` and gives the registry the
  database store. The in-process lane lock is ctx-aware (a dead ctx never acquires it); a held lane
  refuses before any read or build.
  - **Nonces are DB-authoritative and carry one call each.** Every send allocates its nonce in
    `key_lane_nonces` (unique `(chain_id, address, nonce)`, with the call's fingerprint
    keccak(to|data|value)) inside a per-(chain, address) `pg_advisory_xact_lock` (a process mutex on
    SQLite), re-reading the allocations each time — two processes (a rolling deploy) never share a
    nonce. The nonce is the smallest free one ≥ the chain's pending view: free = released (its only
    signing was definitely refused) or above every allocation; a lower nonce with no allocation is
    never taken. Each signing is written to `key_lane_txs` (raw bytes kept until mined) in the same
    transaction, BEFORE the caller's `onPrepared` hook and the broadcast. A signer that cannot hand
    over its bytes is refused on the database store (nothing is broadcast unrecorded).
  - When the chain's pending nonce sits at one of our allocations whose signing the node lacks
    after 15 s, the lane re-broadcasts the stored bytes once (`ALERT key_lane_nonce_gap_rebroadcast_total`),
    then re-signs the SAME call (`ALERT key_lane_speed_up_total`): one +12.5% step per signing since
    the last ACCEPTED one (a refused replacement is recorded `rejected`, never the base), at least
    today's price, never above min(3 × today's price, the chain's `keyLaneFeeCeilingGwei`), at most 3
    bumps, and through the fee gate. Past a cap the lane holds. `waitFollowingReplacements` /
    `KeyLane.FinalHash` follow an original hash to the accepted speed-up: IT operator receipts
    (`MCGPSwapService.receipt`), gas-drip confirms and the treasury's approve wait use it; pool slice
    releases are proven from the escrow's `TradeReleased` log, which a replacement does not change.
  - A hole at a nonce NO process allocated (a send from outside the server) holds the lane after
    15 s (`ALERT key_lane_nonce_gap_held_total`); a mined or released allocation of ours is never a
    hole. Transient lookup or mined-nonce read errors never hold or reuse. The 30 s `key-lane-watch`
    job marks allocations the mined nonce has passed `mined` (raw bytes dropped), clears holds the
    chain has passed, and fails (failure-streak ALERT) while a lane is held.
  - **The fee gate** reads the balance and the mined nonce at ONE block (`NativeStateAt`) on the
    caller's ctx before any lock, then, under the store lock, subtracts the spend of this lane's
    allocations the balance has not paid (nonce ≥ that mined nonce: value + gas limit × price), and
    requires this tx's gas limit × gas price × `gasFeeBuffer` (2) + value + the lane's reservations;
    fails closed (`ALERT key_lane_cannot_pay_total`, `ErrRelayerUnderfunded`). `CanPay` (quotes,
    opens) makes the same computation.
  - Reservations (`AddReservation`, `services.LaneCommitments`: DB-counted gas units, cached 5 s,
    fresh on opens, a newer count never overwritten by an older read) keep back what in-flight work
    is owed: the pool release key its live slices' release/cancel/resolve calls; the pool treasury
    lane its pool orders' deliveries; each conversion lane its converting routes; each company-wallet
    lane its paid, undelivered sales. Only owed call sites run as `services.ReservedSend(ctx)` (slice
    release/cancel/resolve, `sendSale`, `startBridge`, `deliverShortfall`, `refund`,
    `RefundFromTreasury`); any other treasury transfer respects the reservation.
  - Lanes are built only in `release_signer_lanes*.go` (lint `TestKeyLanes_OnlyFromTheRegistry` over
    the whole AST; `SetOperatorSigner` is tests-only); `SignAndBroadcast` on any signer outside
    `release_signer*.go` fails `TestReleaseKey_NoSignAndBroadcastOnAChainSender`.
  - **Runbook — never send from the OPERATOR (or any hot key) by hand.** A manual tx takes a nonce no
    process allocated and holds that lane. Super admin: `GET /api/admin/signer/lanes`;
    `POST /api/admin/signer/lanes/fill-gap {chainId, address, nonce}` (a 0-value self-transfer at a
    hole NO process allocated, itself allocated and recorded; refused for any nonce in the record or
    already mined); `POST .../retry {chainId, address, nonce}` (a nonce held at the speed-up limit gets
    a fresh count; the fee cap still applies); `POST .../clear-hold {chainId, address}` (after checking
    the chain by hand). Each writes a `p2p_audit_logs` row (`signer.lane_gap_filled`,
    `signer.lane_speed_up_retry`, `signer.lane_hold_cleared`); no key material is ever read or logged.
  `MCGPSwapService.signMu` is only the operator SEQUENCE lock (held across receipt waits).
- **IT operator role is verified, not assumed.** The IT key is wired but dormant (`HasOperator` false,
  every operator op refused) until `operators(<key>)` reads true on the swap — at boot or on the 60 s
  `it-operator-role-probe` job (failure-streak alerted; `ALERT signer_role_missing=1 role=it_operator`).
  A release key that is not a Sonic escrow admin logs `WARNING signer_role_missing=1 role=pm_release`.
  Never use the OPERATOR address by hand: a manual tx collides with server nonces.
- **Floors are alert-only** (owner 2026-10-13): `SONIC_GAS_FLOOR_WEI`, `RAMP_SPONSOR_BALANCE_FLOOR_WEI_*`,
  `RELAYER_BALANCE_FLOOR_WEI`, the pool `releaseGasFloorWei` and `RAMP_TREASURY_NATIVE_FLOOR_WEI_*`
  only raise `*_underfunded_total` / float-monitor ALERTs; the lane fee gate decides. A pool open
  asks whether the release key can pay every slice call already owed plus this order's `SliceCap`
  (`ITService.poolReleaseGasOK`). Opens read the count, balance and gas price fresh (also
  `Treasury.CanPayForGasFresh`); quotes reuse a 5 s count and a 5 s balance/price reading, falling
  back to one up to 30 s old on a failed read.
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

TSA Pay crypto refunds (`services/pay_refund_*.go`, `pay-refund-confirmer` job):
- Always on (no flag, owner 2026-10-09). Without a chain registry the service is unwired and
  every owner and admin refund route answers 503 `refunds_unavailable`; the routes stay mounted
  so a client never reads a 404 as "no such refund". Boot logs
  `[tsa-pay] refunds ready over N mainnet chains`.
- **RPCs must serve receipts.** Refunds are proven by reading the receipt on every mainnet chain
  with a client. A chain whose RPC refuses receipt reads (publicnode 403s them on Base, BSC,
  Arbitrum, Optimism) pays the customer on-chain but leaves the refund `submitted` with only
  the `pay_refund_unconfirmed` ALERT. Failover only skips a URL on 429/5xx/transport errors, so
  a 403 endpoint must not be first in a `*_MAINNET_RPC_URL` list. All chains verified
  2026-10-09.

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
  lane (`MARKETPLACE_SONIC_GAS`, default off, ramp sponsor key; floor/ceiling `SONIC_GAS_FLOOR_WEI` / `SONIC_GAS_TOPUP_MAX_WEI`, guard `sonic_gas`) — never
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
- **LPs (TSA Liquidity plan B):** an LP is a `payment_merchants` row with `kind = 'lp'`. One user may hold both roles (owner, 2026-10-13): one row per `(user_id, kind)` (`uq_payment_merchants_user_kind`), and every by-user read names its kind (`PMService.GetByUserIDKind`). The old one-row-per-user index `uq_payment_merchants_user` is dropped by `deploy/one-off/2026-10-13-payment-merchants-drop-one-role-index.sql`; until it runs, a second-role approval is a 409 `second_role_not_enabled`. IT_PM_ID's row is never an LP and its user can never become one (`services.LoadCompanyPM`; 409/403 `company_account_not_eligible`), so the pool is never both sides of a slice. An LP applies with a `liquidity_provider` merchant request, a P2P admin approves it, and it activates with a server-resolved settlement-name match. Only an active LP reaches the pool chain (`openableFor` in `services/p2p_pool_lp.go`); its stock ad never lists (`p2p_pool_hidden.go`). Limits are `fee_configs` rows `pool_lp_min` / `pool_lp_max` (ensured at boot). LP reads: `GET /api/liquidity/me`, `/me/activity`. Boot wiring is `cmd/server/liquidity.go`: with no pool row (any network whose BSC preset row is empty; the mainnet row is live since 2026-10-13, escrow `0xeBF3…22cD`) the `/liquidity` routes are not registered, the app's LP CTA is hidden (`/p2p/pm/me` `liquidityAvailable: false`), applications answer 503 and the `liquidity-voucher-renewal` job (6 h, failure-streak alerted) is not registered.
- **Pool orders (TSA Liquidity plan C1):** a `tsa_pool` buy (registered first in the ramp router) opens every LP slice from the conversion treasury before the buyer pays in (`services/pool_open.go`). Boot wiring is `cmd/server/pool_orders.go` (`wirePoolOrders`): no pool row → nothing; a pool row → the desk, once the release key, the P2P settlement forwarder and the pool sale wallet all exist; the desk also needs the escrow's `paymentWindow()` to be 60 min. Boot refuses to start (`liquidity_pool_unconfigure_refused`) while a live slice sits on a chain that is no longer the pool row.
- A failed open that sent slices still locks LP stock for the slice window and spends treasury gas. It counts against the user's `pool_user_daily_max`, and `poolOpenFailLimit` (3) of them in a row within `poolOpenFailWindow` (10 min) close the pool's gates (`ALERT liquidity_pool_open_failing`) until a restart re-runs every gate. `PoolOpenCap` (8 s) is sized from `scripts/pool-open-timing-probe.sh` run on the prod host (C2 rollout); a change is a code constant, never an env value.
- **A tripped pool-open breaker reopens on any restart, auto-deploys included.** While it is tripped (`ALERT liquidity_pool_open_failing`), set `pool_order_max = 0` (the incident stop) before anything restarts the API, and lift it only once the cause is fixed.
- **Pool settle (plan C2):** the `liquidity-pool-settle` job (30 s) drives a paid `tsa_pool` order: `payment_received → delivering` under the pool lock (S37); every slice `adminRelease`d to the conversion treasury through `ChainSender.Send`; a broadcast release stays `releasing` (at the treasury) until it is proven gone; each release proven on its own receipt 5 deep, never by a balance; each LP paid per proven slice through the P2P settlement forwarder (`provider_ref = pool:<slice>`), with a back-fill for any slice a tick missed; then her USDT sent from the treasury's BSC lane (the company-wallet delivery module over a `saleDesk`) or, converted, handed to the orchestrator in one transaction. `pool_orders` books are written at settle and completed when every LP payout's actual fee is read. Operator actions: `/api/admin/liquidity/orders/:id` (refund only with no slice at the treasury; re-deliver a `pool_slices_short` park; the S37 super-admin refund override; F22 pay-LP **or** USDT-returned, never both — offered for a slice that no longer backs a paid order; `clear-release` for a slice release stuck past the releasing window, allowed only on the chain's evidence). A mainnet pool row now opens real orders: fill it only through the rollout runbook.
- **Runbook:** never `pause()` the pool escrow while paid slices exist (`adminRelease` is `whenNotPaused`); the incident stop is `pool_order_max = 0`. Never lower the escrow's `paymentWindow` while slices are open. A settlement forward that is `pending` with `started_at` set may have paid: never pay it by hand — the re-drive asks the provider by reference.
- Pool settings are the `pool_*` `fee_configs` rows (admin console); `pool_order_max = 0` is the incident stop (no new pool orders). `pool_order_max` can never exceed `RAMP_TREASURY_MAX_INFLIGHT_USD_PER_ORDER`.
- The expiry canceller (`jobs/p2p_expiry_canceller.go`, every minute, failure-streak alerted) is always on whenever the PM release key is set: it cancels unpaid pool slices and also cancels expired ordinary P2P trades 30 min after the expiry notice. Every tick defers while the central-account statement (the inbound poller, when Flutterwave is configured) is stale, so a paid order is never cancelled on a blind spot.
- **Never pay a failed PM sell payout by hand.** A failed payout leaves the trade `pending_payment` with `cancel_reason` "pm payout failed (…)", and the expiry canceller refunds the seller's deposit on-chain; a hand payout on top of that pays the user twice.
- Release-key calls go only through `ChainSender.Send` (the key's KeyLane per chain); `.SignAndBroadcast` on any signer outside `release_signer*.go` fails `TestReleaseKey_NoSignAndBroadcastOnAChainSender`.

**Direct-P2P launch-readiness behaviour (Sonic, manual ads; PM/LP behaviour unchanged):**
- **Mark-paid is on-chain.** `POST /p2p/trades/:id/mark-paid/prepare` `{proofImageUrl, callerAddress?}` → `{unsignedTx, trade}` (fiat payer only; proof saved after the tx builds), then the app signs/broadcasts and `POST .../mark-paid/confirm` `{txHash}` verifies the `TradePaid` receipt and flips `paid`. `markPaidCore` is shared with the reconciler (chain Paid + DB pending now also notifies both parties). The old `POST .../mark-paid` answers 409 `{code:"app_update_required"}` and changes nothing.
- **Lost confirm-open is recovered.** The reconciler (and the mark-paid/dispute gate `requireOpenOnChain`) backfills a trade that exists on-chain but has no `open_tx_hash`, writing the sentinel `open_tx_hash = "reconciled"` (never build an explorer link from it). One matcher, `onChainOpenMatches` (`p2p_open_backfill.go`), guards both this and `ConfirmOpenTrade`: trade exists, amount, funding side, **token**, **counterparty** (`onchain.Seller` for pooled, `onchain.Buyer` for JIT must equal `ad.SellerAddress`) and **opener** (`onchain.Buyer` for pooled, `onchain.Seller` for JIT must equal `p2p_trades.opener_address`, the wallet stored at prepare — so an open front-run under our trade id by another wallet is never recorded; a row with no `opener_address` is refused). `ConfirmOpenTrade` ignores the client's `callerAddress`. A refusal is a 409 plus an `ALERT p2p_open_backfill_mismatch_total=1` log; the reconciler then ignores the foreign trade's progress (only chain Cancelled is applied).
- **Disputes.** A row that was prepared but never confirmed no longer blocks a retry: the same raiser may replace it anytime, the other party only after 10 min (`disputePrepareStaleAfter`), else 409. Confirm requires the `TradeDisputed` `by` to be the on-chain buyer/seller matching the raiser's role (read from chain; a read failure is a 503, never a skip).
- **Payee visibility.** `GET /p2p/trades/:id` returns `payee {provider, bankName, accountNumber, accountName}` to both parties while the open is recorded (pending_payment with an open hash, paid, releasing, disputed); never for PM trades. The public `GET /p2p/ads/:id` masks payment methods to `{id, type, provider, bankName, accountNumber:"******7890", accountNumberLast4}` (no account name). An ad's voucher signature, nonce and deadline are never serialised.
- **Admin review.** `GET /admin/p2p/trades/:id` → `{trade, side, buyer, seller, payee, dispute}` (party = id, name, username, verificationStatus, legalName?) and `GET /admin/p2p/trades/:id/messages?limit&since`; both behind `RequireP2PAdmin`.
- **Operator alerts** (`P2PAlerter`/`NotifyP2P`, emailed to `INBOUND_ALERT_EMAILS`, reason text redacted and capped): `p2p_paid_trade_cancelled` (reconciler cancelled a trade the DB had as paid/releasing), `p2p_dispute_filed` (on confirm or reconciler backfill, only on the real flip), `p2p_paid_unreleased` (job: manual-ad trade paid/releasing longer than `P2P_PAID_UNRELEASED_ALERT_AFTER`; deduped by a `trade.paid_stale_alerted` audit row; CTA `consoleBaseURL/p2p-trades/<id>`).
- **Ad rules.** Fixed-rate ads outside ±`P2P_FIXED_RATE_BAND_BPS` (default 2000 = ±20%) of the market reference are refused 400 `rate_out_of_band` (`data{referenceRate,minRate,maxRate,bandBps}`); skipped for float, non-stable, PM and pool ads, and allowed (logged) when no fresh reference exists. Suspended sellers' ads are hidden from browse/seller profile/express and cannot be opened (409 ad not active). Any KYC-verified user may post ads off the pool chain (merchant/admin/LP role is only for the pool chain); buy-ad openers must be KYC-verified. KYC refusals carry `code: kyc_verification_required`.
- **Live fees.** Fee bps are read from the escrow (60 s cache), never hard-coded: `feeBps` on ads and `GET /p2p/chains` (`{SYMBOL: bps}`), and per trade `feeBps` (snapshot stored in nullable `p2p_trades.fee_bps` at open, from the same one `trades()` read that confirm/backfill already make; NULL for older trades) with derived `feeAmount` / `buyerNetAmount`. The fee comes out of the crypto receiver.
- **Gas top-up** `POST /p2p/gas/top-up` `{action, tradeId?, adId?}` → `{txHash?, amountWei?, skipped}`. Actions: `approve`, `deposit`, `withdraw`, `cancel_ad` (adId), `open_trade` (adId), `mark_paid`, `release`, `cancel_trade`, `dispute` (tradeId). S is sent to the caller's proven wallet only when the action is really her turn (`RequireP2PGasNeed` mirrors each `Prepare*` gate; Sonic only). Codes: 400 bad action/id; 403 `WALLET_PROOF_REQUIRED` / `kyc_verification_required` / `kyc_legal_name_missing` (open_trade when OpenTrade's KYC tier would refuse her: a sell-ad needs a legal name) / not her own linked wallet (`ErrSponsorWallet`); 409 `gas_not_needed`, `gas_topup_unused` (last drip < 1 h old and not yet spent), `sponsorship_in_progress`; 429 daily cap (`P2P_GAS_TOPUPS_PER_DAY`, skips don't count); 503 `gas_unavailable` (no lane, sponsor cannot pay amount + fee, over ceiling, budget, RPC, revert; an unconfirmed drip includes `data{txHash,amountWei}`). Per-user cap, in-flight lock and unused-drip memory are in-process.
- **One Sonic sponsor lane.** P2P top-ups (always, when the lane exists) and checkout funding (only while `MARKETPLACE_SONIC_GAS` is on) share the one Sonic lane built by `buildSonicGasSponsorship` (key `RAMP_SPONSOR_PRIVATE_KEY`, or the OPERATOR key when unset; alert floor/ceiling `SONIC_GAS_FLOOR_WEI` / `SONIC_GAS_TOPUP_MAX_WEI` — the old `MARKETPLACE_SONIC_GAS_*` names are read as deprecated fallbacks with a boot line — guard `sonic_gas`; every role on that key and chain shares its one KeyLane). Ramp sponsorship uses its own base/arbitrum/polygon/bsc lanes on the same key. The Sonic lane is live whenever that key is set and allowed; `RAMP_GAS_SPONSORSHIP` gates only the separate ramp lanes and `MARKETPLACE_SONIC_GAS` only checkout's use of the Sonic lane. Boot refuses a sponsor key equal to the TREASURY or company-wallet key (`assertDistinctKey`, class rule) and, in production, a raw key unless `rawSignerKeyAllowed`. **Fund the sponsor address with S** (float monitor: below 10 S (2x the 5 S floor) logs `sonic_gas_float_low_total`, below 5 S `sonic_gas_float_empty_total`; renamed from `marketplace_sonic_gas_*`, so move any log alert keyed on the old names).
- **Notifications.** Every P2P event carries `data.actorId`; push and email are skipped for the actor; their DB row is kept but stored read (`is_read`, `read_at` set) so it never counts in `/notifications/unread-count`, and the WS frame still goes out. System/reconciler events carry no actorId.
- **Expiry canceller on direct trades.** The always-on expiry canceller (`services/pool_expiry.go` legacy scope) also cancels expired direct trades on-chain with the PM release key, 30 min after the expiry notice and only while the chain still says Pending. It is dormant without that key and pauses every tick while the central-account statement is stale. A direct trade whose buyer saved payment proof (mark-paid prepare) but whose on-chain markPaid never landed is **not** cancelled: it gets one `ALERT p2p_expiry_proof_saved` (and a `p2p_expired_with_proof` operator email, deduped by a `trade.expired_proof_alerted` audit row) and is left to the parties (mark paid, dispute, or cancel by timeout). Once the canceller has decided a trade (`dropped_at`), the mark-paid prepare and the `mark_paid` / `cancel_trade` gas needs refuse it; a confirm or reconcile of a markPaid that already landed still flips it to paid. A user's cancel confirm that lost the race to another cancel returns the cancelled trade. Before deploy, check the release key holds S on Sonic.
- **Image URLs.** Payment proofs (mark-paid prepare), P2P dispute evidence, order-dispute evidence and chat images must be `https://res.cloudinary.com/...` (`isUploadedImageURL`, exact hostname); anything else is a 400.
- **Deploy notes:**
  - Schema changes are additive and nullable (AutoMigrate): `p2p_trades.fee_bps` (+ its legs `fee_platform_bps`, `fee_gas_bps`; release accrual reads the platform leg before the chain), `p2p_trades.opener_address` and `p2p_disputes.prepare_nonce` (the replace guard for an unconfirmed dispute row).
  - **Run before deploy.** (a) Live trades past pending with no open recorded (the open backfill would stall on them): `status IN ('paid','releasing','disputed') AND COALESCE(open_tx_hash,'') = ''`. (b) Direct trades the old DB-only mark-paid left `paid`: `SELECT t.id, t.on_chain_trade_id, t.created_at FROM p2p_trades t JOIN p2p_ads a ON a.id = t.ad_id WHERE t.status = 'paid' AND a.pm_id IS NULL` — check each one's chain status; on chain they are still Pending, so they stay cancellable by timeout, and the new mark-paid prepare (which needs `pending_payment`) can't re-mark them. (c) Trades still waiting for their open (`status = 'pending_payment' AND COALESCE(open_tx_hash,'') = ''`): they have no `opener_address`, so neither a late confirm-open nor the reconciler backfill will record them (refused with the mismatch ALERT) — let them settle or expire before deploying, or reconcile them by hand.
  - Keep the window between the API deploy and the EAS update short: an old app gets 409 `app_update_required` on mark-paid.
  - **Sonic gas lane renames** (move Doppler vars and any log alert keyed on the old names together):
    - env: `MARKETPLACE_SONIC_GAS_FLOOR_WEI` → `SONIC_GAS_FLOOR_WEI`, `MARKETPLACE_SONIC_GAS_TOPUP_MAX_WEI` → `SONIC_GAS_TOPUP_MAX_WEI` (old names still read, with a `[config] DEPRECATED env` boot line). `MARKETPLACE_SONIC_GAS` (checkout's switch) is unchanged.
    - log prefix `[marketplace_sonic_gas]` → `[sonic_gas]` on the guard and float-monitor lines, and these metrics:
      - `marketplace_sonic_gas_underfunded_total` → `sonic_gas_underfunded_total`
      - `marketplace_sonic_gas_rate_limited_total` → `sonic_gas_rate_limited_total`
      - `marketplace_sonic_gas_daily_cap_exhausted_total` → `sonic_gas_daily_cap_exhausted_total`
      - `marketplace_sonic_gas_skipped_total` → `sonic_gas_skipped_total` (lane not built: bad floor/ceiling/budget)
      - `marketplace_sonic_gas_float_low_total` → `sonic_gas_float_low_total`
      - `marketplace_sonic_gas_float_empty_total` → `sonic_gas_float_empty_total`
    - unchanged: `marketplace_sonic_gas_sent` (checkout funding's own send metric).
  - **Check the escrows answer the V3 `trades()` layout.** `cast call <escrow> "trades(bytes32)" 0x00…00 --rpc-url <rpc>` on the Sonic and BSC escrows must return 12 words. A shorter answer means a pre-V3 escrow: every `ReadTrade` (reconciler, confirm, backfill) then costs 2 RPCs and records no fee snapshot.
  - The Sonic sponsor lane is built whenever a sponsor key exists (`RAMP_SPONSOR_PRIVATE_KEY`, else the OPERATOR), so `assertDistinctKey` can stop boot even with `MARKETPLACE_SONIC_GAS` off. Fund the sponsor address with **at least 10 S** first, or the float monitor ALERTs.
  - **Funding after the signer consolidation:** OPERATOR on Sonic 15–20 S (releases, IT ops, gas top-ups); RELAYER keeps its own S; OPERATOR on BSC ≥ 0.025 BNB (pool releases/cancels; ≥ 0.03 if `bsc` is in `RAMP_SPONSOR_CHAINS`); OPERATOR on each `RAMP_SPONSOR_CHAINS` chain ≥ 2× its alert floor; TREASURY on BSC ≥ 0.02–0.025 BNB, and on every `RAMP_TREASURY_CHAINS` lane it serves.
- **Settings:** `P2P_FIXED_RATE_BAND_BPS=2000`, `P2P_PAID_UNRELEASED_ALERT_AFTER=2h`, `P2P_GAS_TOPUPS_PER_DAY=6` (see `.env.example`). The defaults live only in `config` (`DefaultP2P*`); a non-positive override falls back to them.

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
