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
  refuses before any read or build. **A nonce only ever carries one call**, across processes and
  restarts — the lane is an OUTBOX:
  - **A.** Chain reads (pending, mined + balance at one block, gas price) happen first. Then, under a
    per-(chain, address) `pg_advisory_xact_lock` (a process mutex on SQLite) in ONE transaction with no
    network I/O: choose the nonce (max(pending, mined, every allocation + 1); never a hole), run the fee
    gate, sign, write the allocation (`key_lane_nonces`: unique `(chain_id, address, nonce)`, the call's
    fingerprint keccak(to|data|value), the caller's operation key) and its signing (`key_lane_txs`,
    raw bytes, status `signed`), run the caller's **pre-broadcast hook**, commit. The hook
    (`services.WithPreBroadcast(ctx, LaneHook)`, or the `*Recorded` helpers) is the caller's own
    business write — DB only, through `services.LaneDB(ctx, db)`, which is that same transaction — so
    the caller's state and the signed call commit together or not at all; a hook error rolls
    everything back and nothing signed survives. Callers: treasury sale legs (`sendSale`: leg nonce +
    hash, only while pending), conversion bridge/delivery/refund legs (`legSend`: hash + tool, only
    while pending — a refund that won the leg means nothing is signed), pool fills
    (`pool_orders.fill_tx_hash`, only while unset and the intent — row-locked in the hook — is still `delivering`), P2P releases (`release_tx_hash`, only while releasing),
    slice resolves (hash replaces the claim), expiry cancels (the cancel DECISION — `dropped_at`
    under the row lock — is taken in the hook), gas top-ups and authorization relays (the
    sponsorship / intent row's hash), the name-change fee refund (`fee_tx_hash`). An approve never
    inherits its route's hook: a bridge leg's approve records `approve_tx_hash` through its own
    (`WithApproveHook`); a leg whose approve committed but whose route did not is freed after 2 min
    only through a PERMANENT `KeyLane.RetireOp` of its `:route` operation, and then only while the
    leg records no route hash; the route goes on a new attempt.
    **The give-up rule:** a caller may abandon an operation (fail or free its record, clear a claim,
    roll back, retry on a new operation) only after `KeyLane.RetireOp(op, hash)` — under the lane
    lock — proved it holds nothing live or executed (by its key and by the allocation of its
    recorded hash; a resolved-cancelled one does not count, nor a mined one that REVERTED) and
    marked its tombstone (`key_lane_retired_ops`): **permanent** when the retry is a NEW key (legs and
    routes, `gas_topup:<row>`, `auth_relay:<row>`, `p2p_drip`, `name_change_refund:<id>:<n>`, IT ops),
    **sequenced** when the retry reuses the key (intent/destination top-ups, slice release/resolve,
    AutoRelease) — an attempt begun before it is refused at its commit (`ErrOperationRetired`); a
    permanent tombstone is never downgraded. The lane PROBES (no
    tombstone) on every not-attempted answer for an operation — a hold, a fee/floor read, a build
    error, the fee gate, a hook refusal, and the treasury's own pre-lane refusals (kill switch,
    checks, gas gate, allowance read — every key of the operation under ONE lock): an allocation
    found there is answered in flight (a different call under the key is logged and answered with
    the committed one; a RELATED call — a route's approve — is in flight with no hash, never its
    hash as the route's). A conversion leg left pending with no hash is re-probed after 5 min
    (`recoverHashless`: a permanent `RetireOp` of its keys) and adopts the committed hash or is
    freed. Every pre-broadcast hook
    updates exactly one row under its expected-state guard, else the allocation rolls back.
    A gasless relay in flight with no hash yet answers 202 `ErrRelayInProgress` naming its operation;
    a retry of a committed relay is answered from its allocation before the rate limit, budget or
    simulation.
    Every operation therefore carries a key: legs `ramp_leg:<leg>`, opens `pool_open:<slice>`,
    releases/resolves/cancels `p2p_release|p2p_resolve|p2p_cancel:<trade>`, IT ops
    `it_op:<call hash>`, top-ups `gas_topup:<row>` / `gas_topup_intent|gas_topup_dest:<intent>` /
    `p2p_drip:<uuid>`, relays `auth_relay:<row>` and `relay:<forward-request digest>` (a client retry
    of the same request reuses its allocation; an in-flight relay answers its hash), name-change
    refunds `name_change_refund:<change>:<attempt>`. Caller writes on give-up are conditional on the
    recorded hash (`FailExpected`, `ClearCancelledTopUp` / `ClearCancelledDestTopUp`).
    IT operator ops write their row before the call and resolve by the contract ref; a
    not-attempted IT op still leaves a sequenced `it_op:` tombstone. A system release that mined and
    REVERTED is never rolled back to `paid`: at settle depth it is parked `releasing` with
    `release_reverted_at` set (`ALERT p2p_release_reverted_total`, deduped), out of the sweep and
    refused by AutoRelease, for an admin to resolve. In-memory bookkeeping (the P2P drip memory) runs after the commit, never in a hook.
    An error from the allocation transaction that is not the body's own refusal (fresh or reused
    allocation) is re-checked under the RE-TAKEN lane lock by operation key: committed answers in
    flight, provably absent answers not sent, and a re-check that fails (or an unreadable operation
    key) answers `ErrSendOutcomeUnknown` — attempted, never "not sent".
  - **B.** After the commit, outside every lock: the bytes are broadcast and a short transaction
    records the outcome (`accepted` / `uncertain` / `refused`). Once committed the call is IN FLIGHT:
    a broadcast the node refused or that timed out returns its hash with `ErrPendingBroadcast`
    (attempted = true; never matches `ErrTxNotBroadcast`) — callers treat it as uncertain, never as
    "not sent". `attempted = false` means nothing was committed.
  - **C.** A committed signing IS its nonce's transaction until the chain mines that nonce (statuses
    `signed` → `sent` → `mined`). Refused or lost, the upkeep re-broadcasts the same bytes or speeds up
    the SAME call until it mines. A call that can NEVER be sent — with NO signing of it accepted, a
    call-intrinsic refusal (intrinsic gas too low, over the block gas limit) or an RPC tx-fee-cap
    refusal of its lowest-priced signing (`unsendableKind`); once any signing was accepted the lane
    never cancels it and holds instead (that signing may still mine) — or that a super-admin
    cancels (`POST .../cancel`) is replaced at its nonce by a 0-value self-transfer priced above every
    earlier signing (same caps). A fee-cap refusal of a SPEED-UP while a cheaper signing is accepted
    only stops the bumping (hold), never cancels. Once the cancel's receipt is max(`keyLaneCancelDepth`
    (5), the deepest configured confirmations — `SetFinalityDepth` at boot) blocks deep against the
    latest block the allocation is `cancelled`, its operation key freed, and verifiers
    report `ErrCallCancelled` (shallower it stays in flight, re-read every tick: a reorg that puts the
    original in resolves it as mined and the caller sees success). Every caller reads ONE verdict,
    `KeyLane.CallOutcome` (one store read; a recorded `cancelled` is re-proven on chain each time — a
    chain that disagrees is in flight): sale legs and the sale gate, conversion legs, gas top-ups
    (order row forgotten / intent claim given back / P2P drip memory dropped), authorization relays
    (relayed again), the name-change refund (back to `refund_failed`), IT sells (failed at once), slice
    releases/resolves (`SliceHashReverted`) and AutoRelease — each then retries on a new operation
    and nonce. The cancel is never followed as the
    call's receipt. Only a hole NO process allocated — strictly below the highest allocated nonce —
    is ever filled, and only by a super-admin (`fill-gap`).
  - **D.** A signing the node lacks 15 s on is re-broadcast byte for byte once, then sped up with the
    SAME call — decided, signed and committed under the lock (the bump counted in the committed row),
    broadcast after: one +12.5% step per bump since the last ACCEPTED signing, above every earlier
    signing, at least today's price, never above min(3 × today's price, `keyLaneFeeCeilingGwei`) or
    the allocation's admin-raised `price_cap`, at most `BumpLimit` (3) bumps, through the full fee gate
    INCLUDING the lane's reservations (the watch job's too) — except an owed call's (`ReservedSend`,
    recorded as `reserved`), gated as its send was, never against its own reservation. Past a cap the
    lane holds; a speed-up the balance cannot pay never holds — every tick retries it, so a top-up
    re-arms the lane by itself. A hold is shared state (`key_lane_states`): every process refuses in
    its allocation transaction (after the operation-key lookup: a committed operation is answered
    even on a held lane) and adopts it on its watch; a hold whose persist failed is re-persisted,
    never dropped; an ADMIN clear on any process clears it for all and (`clear_version`, bumped only
    by admin clears — retry, raise-price-cap, cancel, fill-gap, clear-hold — not by the watch
    clearing a passed hold or a cancel's unhold) resets every
    process's gap timers and drops unpersisted local holds. Retry, raise-price-cap and clear-hold refuse a cancelled nonce (409, no
    audit row); raise-price-cap stays the exit for a cancel held at the fee cap.
  - **E.** `services.WithOperationRef(ctx, ref)` is the allocation's idempotency key: the same
    operation retried (release `p2p_release:<trade>`, cancel, resolve, IT op `it_op:<call hash>`,
    treasury legs `ramp_leg:<leg>` with `:approve`/`:route`) gets its existing allocation and its
    honest status — the current hash, with `ErrPendingBroadcast` until a node accepted a signing —
    before any fee read or hold can call it "not sent"; never a new nonce. A different call under the
    same key is refused. A mined allocation frees its key.
  - Every allocation update (outcomes, speed-ups, admin actions, marking mined) runs under the lane
    lock and names the row's `version`; a stale copy or a mined row is never written back
    (`errStaleAllocation`). `FinalHash` / receipt waits follow only signings of the SAME call
    fingerprint. The fee gate's committed spend is two statements, never one per allocation.
  - First use of a lane on an empty table reads the pending nonce twice a second apart (an old
    process may still be sending) and the mined nonce, and starts above all of them.
  - Receipt waits (`waitFollowingReplacements`: IT operator receipts, gas-drip confirms, the
    treasury approve) poll EVERY signing of the call, so the original mining after a speed-up still
    answers. Post-send verifiers keyed by a recorded hash go through ONE helper first
    (`Treasury.followCall`: non-waiting receipt reads over every signing of the same call, refused ones
    included; a read error defers): sale-leg finalize and its dropped-tx evidence, conversion
    bridge/top-up/refund legs and pool fills verify whichever signing mined and record that hash
    on the row. AutoRelease verifies the release on each signing of the call; an ordinary trade whose
    release is committed but not yet accepted stays `releasing`, and a retried slice release writes
    its allocation's hash back. Pool slice releases are proven from the escrow's `TradeReleased` log.
  - A hole at a nonce NO process allocated (a send from outside the server) holds the lane after
    15 s (`ALERT key_lane_nonce_gap_held_total`); transient read errors never hold. The 30 s
    `key-lane-watch` job marks allocations below the mined nonce `mined` (raw bytes dropped), clears
    holds the chain passed, reviews the allocation at the mined nonce (so an idle lane still moves),
    and fails (failure-streak ALERT) while a lane is held.
  - **The fee gate:** the balance and mined nonce read at ONE block (`NativeStateAt`), less the spend
    of this lane's live allocations the balance has not paid (nonce ≥ that mined nonce: value + gas
    limit × price of each current signing), must cover this tx's gas limit × gas price ×
    `gasFeeBuffer` (2) + value + the lane's reservations; fails closed (`ALERT
    key_lane_cannot_pay_total`, `ErrRelayerUnderfunded`). `CanPay` (quotes, opens) is the same.
  - Reservations (`AddReservation`, `services.LaneCommitments`: DB-counted gas units, cached 5 s,
    fresh on opens) keep back what in-flight work is owed (pool fills on the
    release key; pool deliveries, converting routes, company-wallet sales on treasury lanes). Only
    owed call sites run as `services.ReservedSend(ctx)` (pool fills, `sendSale`,
    `startBridge`, `deliverShortfall`, `refund`, `RefundFromTreasury`).
  - A signer that cannot sign without broadcasting is refused on the database store. Lanes are built
    only in `release_signer_lanes*.go` (lint `TestKeyLanes_OnlyFromTheRegistry`; `SetOperatorSigner`
    is tests-only); `SignAndBroadcast` on any signer outside `release_signer*.go` fails
    `TestReleaseKey_NoSignAndBroadcastOnAChainSender`.
  - **Runbook — never send from the OPERATOR (or any hot key) by hand.** A manual tx takes a nonce no
    process allocated and holds that lane. Super admin: `GET /api/admin/signer/lanes`;
    `POST /api/admin/signer/lanes/fill-gap {chainId, address, nonce}` (a 0-value self-transfer at a
    hole NO process allocated; refused for any nonce in the record or already mined);
    `POST .../retry {chainId, address, nonce}` (raises a held nonce's bump limit by 3; the fee cap still
    applies); `POST .../raise-price-cap {chainId, address, nonce, maxGasPriceWei}` (the exit from a
    fee-cap hold: that allocation's own speed-up ceiling — above every signing of it, at most 10× the
    chain's default ceiling — plus a fresh bump count; audited with the old and new cap);
    `POST .../clear-hold {chainId, address}` (after checking the chain by hand). Each writes a
    `p2p_audit_logs` row; no key material is ever read or logged. `POST .../cancel {chainId, address,
    nonce}` replaces a stuck committed call with the lane's 0-value cancel (its operation then fails
    with `ErrCallCancelled` and retries as a new one); a call the node refuses for good is cancelled
    automatically. `fill-gap` is refused for any nonce at or above the highest allocated one.
  `MCGPSwapService.signMu` is only the operator SEQUENCE lock (held across receipt waits).
- **IT operator role is verified, not assumed.** The IT key is wired but dormant (`HasOperator` false,
  every operator op refused) until `operators(<key>)` reads true on the swap — at boot or on the 60 s
  `it-operator-role-probe` job (failure-streak alerted; `ALERT signer_role_missing=1 role=it_operator`).
  A release key that is not a Sonic escrow admin logs `WARNING signer_role_missing=1 role=pm_release`.
  Never use the OPERATOR address by hand: a manual tx collides with server nonces.
- **Floors are alert-only** (owner 2026-10-13): `SONIC_GAS_FLOOR_WEI`, `RAMP_SPONSOR_BALANCE_FLOOR_WEI_*`,
  `RELAYER_BALANCE_FLOOR_WEI`, the pool `releaseGasFloorWei` and `RAMP_TREASURY_NATIVE_FLOOR_WEI_*`
  only raise `*_underfunded_total` / float-monitor ALERTs; the lane fee gate decides. A pool open
  asks whether the release key can pay every fill already owed plus this order's one
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
- **Payer declarations** (`services/inbound_payer.go`, owner 2026-10-16): a buyer whose naira
  came from an account not in their name calls `POST /api/inbound/payer {orderId, bankCode,
  accountNumber}` (KYC-verified, 10/15 min per user). The account name is the bank's (name
  enquiry) — the buyer never types it — and a credit is attributed only when the statement
  sender name matches it, the sender bank is the declared one (`payerSameBank`; an
  uncomparable bank goes to review, never a bind), the amount is the claim's and it arrived after
  the claim opened. Funding rows carry no sender account number, so the number is never compared.
  Declare settles only credits already held `sender_matches_no_candidate_name` (never unmatched
  ones — the matcher decides those, a candidate's own name first — nor any other hold), and only
  when the matcher recorded the declarer's claim as the SOLE candidate when it held the credit
  (`held_claim_ids`, written on every no-name hold — candidates as of arrival, not of now; empty
  means review). A declared bind first CLAIMS the row (`status = binding`, claim named) and only
  then runs the product settle, so an admin refund/assign racing it gets 409; an interrupted bind
  is finished by the matcher once it has sat 30 min (`resumeDeclaredBinds`, under a conditional
  lease — Accept is idempotent after a call finishes, not alongside a live one), never handed back
  to `held`. Handler logs pass errors through `payerLogSafe` (account digits masked).
  Declarations close when the order matches on the buyer's own name, or after 48 h waiting.
  ≤ ₦500,000 binds through the normal `bindCredit` path (strength `declared`, audit
  `inbound.payer_declared`, product review gates still apply — never `withManualBind`); larger,
  ambiguous or product-refused ones go to `review` for a super admin. A declaration made before
  the money lands is consulted by the matcher only when no candidate's own name matches.
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
- `BSC_NETWORK` — `"mainnet"` | `"testnet"` (**default `mainnet`**). It selects BSC's rows: the ordinary P2P row (`internal/config/p2p_presets.go`, empty = dormant) and the shared liquidity pool row (`internal/config/liquidity_pool.go`, no vault = dormant).
- `P2P_ESCROW_ADDRESS_BSC` / `P2P_ESCROW_EIP712_VERSION_BSC` — **retired**: boot refuses to start if either is set.
- **The slice pool (spec 2026-10-08) is retired.** No LP ads, slices, per-LP naira forwards or `liquidity_provider` applications remain (a pending one is refused at approval: 409 `use_liquidity_activate`). `hideSlice` / `notPoolSlice` / `isPoolAd` still keep any row it left behind (a `p2p_trades` row with `it_intent_id`, an ad owned by an LP row — public ad lists hide those via a `NOT EXISTS` on `payment_merchants` (`excludePoolAds`), never an id list) off every user path, and the reconciler never moves one — an operator settles leftovers by hand.
- **Shared liquidity pool (spec 2026-10-14):** the pool row in `liquidity_pool.go` names the `TsaLiquidityPool` vault (+ `vaultDeployBlock`); `tsa_pool` orders fill from it (`smart-contracts/contracts/TsaLiquidityPool.sol`, UUPS, owned directly by the owner's wallet while in development — no timelock (owner 2026-10-14); the market-ready contracts will not be upgradeable): LPs deposit principal and every fill credits its spread to all LPs pro-rata. **No collateral** (owner 2026-10-14, spec `2026-10-14-liquidity-no-collateral.md`): LP deposits are what the pool sells — a fill sends USDT from the pool to the treasury, `outstanding += amount + spread`, and the company refills after it has the naira (`refill`, signed by the refill wallet — the contract's `collateralProvider`, name kept). What a fill or a claim may use is `liquid()` = balance − `totalPendingWithdrawals`. A withdraw the pool cannot pay in full pays what is liquid now and records the rest as the LP's pending withdrawal (no longer principal, earns nothing), paid later only to that LP by `payPending`. Invariant: `balance + outstanding = totalPrincipal + Σunclaimed + totalPendingWithdrawals` (dust in the pool's favour). `postCollateral`/`withdrawCollateral`/`settleFromCollateral`/`freeCapacity` are gone. Operators are a set: the refill wallet (owner wallet) adds and removes them directly, the guardian can remove one. **Deploy the contract upgrade before this API**: `VaultState` reads `totalPendingWithdrawals` and `Position` reads `pendingWithdrawal`, which the old contract lacks. Code: `services/pool_vault.go` (open/claim/fill), `liquidity_vault*.go` (adapter, LP side), `liquidity_indexer.go` (ledger), `pool_vault_admin.go` (console); boot wiring `cmd/server/pool_orders.go`, `liquidity.go`.
  - **Open** writes no on-chain state: under the pool lock, `liquid − Σ(fill of every reserving order not yet proven filled) ≥ this fill` (`pool_orders.fill_units`; the spread needs no backing — it is credited to LPs and owed by the company until it refills). A reserving order is every paid one (`payment_received`, `delivering`, `buy_awaiting_rate`) whatever its age, and an unpaid `pending_payment` one only while `created_at` is inside `stableBuyClaimWindow` (24 h — after that it can never be claimed); the release-key and treasury lane commitments count the same set. The open also refuses what the contract's own fill limits would refuse at the fill, read fresh under the lock (`VaultChain.FillCaps`; unreadable → refused): fill + spread over `maxFillPerTx`, spread over `maxSpreadBps`, and — while the contract's fixed window is still open (`fillWindowStart + fillWindow > now`) — `filledInWindow + Σreserved + this` over `fillWindowCap`. The gates read `maxFillPerTx`/`maxSpreadBps` every check and ALERT (alert-only) `liquidity_pool_order_max_over_vault_cap` / `liquidity_pool_spread_over_vault_max` when `pool_order_max × (1 + pool_spread)` or `pool_spread` exceeds them.
  - **Claim** re-checks liquid with every OTHER order's reserve and the contract's fill limits (`fillCapRefusal`, the open's check with this order's own reserve left out of the window); covered → the snapshot becomes what she is owed. Short or over a limit → `buy_awaiting_rate` `pool_fill_refused: <reason>` (per-fill: "over the vault's per-fill limit — refund or split") with the snapshot untouched (a park keeps the intent's open-time quote, so the two never disagree); a park of money priced live ends with `(priced live at claim: a retry re-prices)`.
  - **Settle** (`liquidity-pool-settle`): the OPERATOR sends `fill(treasury, units, spread, HashID(intent))`, op `pool_fill:<intent>`, hash written by the pre-broadcast hook (`recordVaultFill`), which takes the intent's row lock and refuses unless it is still `delivering` — the pool lock is released between the decision and the lane, so a park + refund in that gap rolls the allocation back and nothing is signed; spread 0 when no LP holds principal. Before sending, the fill must equal the intent's `asset_amount` in chain units (what the sale desk delivers) — otherwise nothing is sent: `ALERT liquidity_pool_fill_amount_mismatch` and a `pool_fill_refused` park. Proven at sale depth on its own `Filled` event (ref, treasury, amount) → `filled_at`, then the pool sale desk delivers or hands off (custody proven from the fill transfer). A refusal at estimate (contract revert reason) parks `pool_fill_refused`; a fill that MINED and REVERTED parks once the lane proves it (`KeyLane.CallReverted` at settle depth — never a give-up: a mined allocation is executed, and the watch frees its key so the retry takes a new nonce), hash cleared conditionally. RPC errors are transients; a fill unsent or unproven past 10 min (`poolFillStallAfter`) returns `ErrPoolReleaseStalled`, which feeds the settle job's sustained `liquidity_pool_release_stalled` alert.
  - **Refund** only while no fill is recorded. Operator exits: `POST /api/admin/liquidity/orders/:id/retry-fill` and the refund, both super admin. Retry re-reserves from the intent as a claim would: a re-priced claim park is priced again live on `received_amount` (intent quote columns + `repriced`, rate-updated notice), any other park re-derives fill/spread from `asset_amount`; refused (`ErrPoolCannotFill` with the reason, stays parked) unless liquid covers its fill with every other reserve kept back and it is within the contract's fill limits — never estimated into another park. **Auto-retry** (`liquidity-pool-fill-auto-retry`, 2 min, failure-streak alerted, registered next to the settle job; review 2026-10-15): parks whose reason is a pure LIQUIDITY refusal (`poolFillParkedOnLiquidity`: `pool_fill_refused: ` + the claim's free-liquidity refusal, its pre-rename text `the pool's free collateral does not cover the amount owed` (parks from before the no-collateral deploy), or `the vault refuses the fill: Insufficient liquidity` — never a fill-limit, amount-mismatch, reverted-fill or other revert park, and **never a re-priced park** (reason ending `(priced live at claim: a retry re-prices)`): late or different money sold at a new rate is a super admin's decision via retry-fill, not the system's) are retried oldest first (≤ 50 per tick) through `RetryPoolFill` itself with no actor (`it_intent_audit_logs.actor_id` NULL) and note "auto-retry after liquidity returned". The vault is read ONCE per tick (skipped while paused or 0 liquid): slack = liquid − Σ fill of every reserving order (signed; parks keep their reservation), and a park whose fill (from `asset_amount`) less its own reservation exceeds the slack is skipped without calling `RetryPoolFill` (no lock, no RPC); each resume spends its share. One still short stays parked silently (`ErrPoolCannotFill`/`ErrITBadState`). Only reading the vault, the parks or the reserves fails the tick; any other per-order retry error (rate, quote, missing row…) is logged once per order per hour (`alertf`, `[liquidity] auto-retry intent=… — left parked`) and the tick goes on. Success logs `METRIC liquidity_pool_fill_auto_retried`. Keep the park reason strings stable — the classifier matches them exactly.
  - **Ledger:** `liquidity-vault-indexer` (30 s, failure-streak alerted) indexes final blocks (head − 5) into `liquidity_ledger` (incl. `withdrawal_pending` / `withdrawal_paid`; the retired `SettledFromCollateral` event stays decodable so a ledger rebuilt over pre-upgrade history still subtracts those settles) and reconciles against `StateAt` the same block: drift in outstanding, principal (deposits − withdrawn − withdrawal_pending) or pending withdrawals (withdrawal_pending − withdrawal_paid), or the invariant short (`balance + outstanding < principal + (Σspread − Σclaimed) + pending`) (`liquidity_vault_ledger_drift`), a fill whose ref matches no pool order (`liquidity_vault_unknown_fill`), a fill under one of our refs that is not that order's — amount ≠ `fill_units`, spread ≠ `fill_spread_units`/`spread_units`, destination ≠ the treasury (set from the desk in `SetVaultLedger`; skipped while no desk is wired), or an order with no fill recorded (`liquidity_vault_fill_mismatch`, report `mismatchedFills`) — for either: the operator key spending outside the order path, pause the vault via the guardian and rotate — outstanding ≥ 80% of principal + pending (`liquidity_vault_high_use`: the company refills), and `liquidity_pending_withdrawals` (hourly-deduped ALERT) while any withdrawal is pending and the vault's token balance > 0 (`payPending` pays min(pending, balance), so even a part can go out while liquid is still 0; an admin should pay them). All alerts dedupe hourly (`alertf`).
  - **LPs** (`/api/liquidity/*` in vault mode): `POST /activate` is instant for an active TSA Pay merchant or any KYC-verified user (never the company account) on the proven wallet, one LP account per wallet (`uq_payment_merchants_lp_wallet`); the OPERATOR allow-lists it (`liquidity-depositor-sync`, 2 min, also revokes paused LPs — deposits only; a wallet that should stay ALLOWED and whose latest indexed `DepositorSet` already says allowed costs no RPC; every revoke, and every disagreement, reads its position (a stale index never keeps a wallet able to deposit); it returns the number of setDepositor calls sent; a sent-but-unmined write is answered by its `pool_depositor:` op key, never re-sent). A withdraw over principal ("Bad amount") or a claim with nothing earned ("Nothing to claim") refused at estimate is a 400 (`ErrLPAmount` / `ErrLPNothingToClaim`); a claim (or `claim-for`) the pool has no liquid USDT for ("Insufficient liquidity") is a 409 `claim_unavailable`, "Claims are not available right now. Try again later." (`ErrLPClaimUnavailable`) — user copy never gives a reason. `/liquidity/me` returns `pendingWithdrawal` (omitted when 0, shown neutrally as "Pending withdrawal"); activity kinds add `withdrawal_pending` and `withdrawal_paid` (`withdrawal` is the part paid at once). Re-activating from a new wallet is refused (`liquidity_old_wallet_has_funds`) while the old one has principal, earnings or a pending withdrawal. The pool card (`pool`) is `{totalLiquidity, apr30d}` only — no collateral/coverage fields and no `outstanding` (what the company owes the pool is the console's, review 2026-10-15). Deposit/withdraw/claim are the LP's own unsigned txs; `POST /claim-for` is a gas-free claim to the LP's own wallet (≥ $0.50, once per 8 h). Withdraw and claim are never gated by pause or allow-list — that is the custody promise; never add a guard to them. The contract's one bound (owner 2026-10-14): principal deposited in the last `withdrawLock` (24 h, owner-set, max 7 days) cannot be withdrawn yet — it earns from the deposit on; `/liquidity/me` returns `withdrawable` and `lockedUntil`, and a withdraw over the unlocked part is a 400 `deposit_locked`. Earnings are never locked.
  - **Console:** `GET /api/admin/liquidity/pool` → `outstanding`, `liquid`, `reserved` (Σ fill of reserving orders), `free` (liquid − reserved), `totalPrincipal`, `totalPendingWithdrawals`, `pendingWithdrawals: [{lp?, wallet, amount}]` (wallets from the ledger — withdrawal_pending − withdrawal_paid — each amount re-read from the contract's `pendingWithdrawal`, which wins; `lp` = the LP row id when one names the wallet), `utilisationPercent` = outstanding ÷ (principal + pending), `activeLps`, `spread30d`, roles/operators, `ledger`. `POST /api/admin/liquidity/pool/pay-pending` `{operationId, wallet?, note}` (super admin, `Idempotency` middleware) → message "Pending withdrawals sent" (sent, not proven paid), `{paid: [{wallet, txHash, amount}], skipped: [{wallet, pending, reason}]}`: the OPERATOR sends `payPending(wallet)` for one wallet, or every ledger-pending wallet in the order they went pending. Sends are not awaited, so the vault's token balance is read ONCE (`Vault.State`, head, uncached) and budgeted (review 2026-10-15): each wallet is sent expecting `min(pending, remaining)`, remaining = balance − Σ expected of this operation's sends (earlier sends of the same operation count too); once it is 0 the rest are `skipped` with reason "not enough liquid yet" and nothing is sent (no gas on a payPending that would pay 0). Single-wallet mode budgets the same way. `amount` and the audit row's `expected` (with `pending`), and the `liquidity_pay_pending … pending= expected=` METRIC, are what the send expects to pay. Op `pool_pay_pending:<operationId>:<lowercase wallet>`, whose pre-broadcast hook writes the `liquidity.pay_pending` `p2p_audit_logs` row in the lane transaction, so a retry answers the same hash and amount (lane allocation, or the audit row once mined), never a second send (a wallet the first call skipped has no op yet: a retry after a refill may send it); a wallet with 0 pending on-chain is neither paid nor skipped. `operationId` missing/invalid or a bad wallet is a 400. A pay-all that fails part-way (after some sends) answers 500 code `pay_pending_partial` with `data {paid, skipped}` — what was already sent; the console toasts those (tx links) with the error and keeps the dialog's `operationId`, so a retry replays them; a failure before any send uses the normal error mapping. Refilling is signed by the refill wallet in the console, never a server key; `payPending` only ever pays the LP's own wallet.
  - **Rehearse** the adapter against the real contract: `npx hardhat node` + `scripts/rehearse-liquidity-pool-local.js`, then `VAULT_NODE='<json>' go test -tags vaultnode ./internal/services/ -run '^TestVaultNode_'`.
- Pool settings are the seven `pool_*` `fee_configs` rows (admin console, `poolSettingDefaults`): `pool_spread`, `pool_take`, `pool_band`, `pool_min_slice` (the minimum order; key kept), `pool_order_max`, `pool_user_daily_max`, `pool_network_fee` (flat USD per order, the buy price's "Network fee" line). `pool_order_max = 0` is the incident stop (no new pool orders). `pool_order_max` can never exceed `RAMP_TREASURY_MAX_INFLIGHT_USD_PER_ORDER`.
  - **LP deposit limits are the contract's, never a setting** (cleanup 2026-10-15): `minDeposit` / `maxPerLp` / `totalCap`, changed only by the pool owner's `setLimits`. `/liquidity/me` `limits {min, max}` reads them through a 60 s cache (`limitsReadable: false` and empty strings when the read fails — never a guess); `PrepareDeposit` reads them fresh with the vault's `totalPrincipal` and refuses below min / over maxPerLp (400) / over totalCap (409 `pool_full`, naming the room left), or 503 `deposits_unavailable` when either read fails. The console pool view returns them as `minDeposit`, `maxPerLp`, `totalCap` (omitted on a failed read).
  - **Retired rows** (`pool_lp_min`, `pool_lp_max`, `pool_slice_cap`, `pool_gas_per_slice`, `pool_gas_delivery`) are deleted at boot by `ensurePoolSettingRows` (idempotent, one transaction); when `pool_network_fee` does not exist yet it is first seeded from what the two gas rows priced at (an active row's amount, else its old default 0.03 / 0.01). An order already open keeps its network fee: it is the fee line stored on its intent, never re-read from settings.
- The expiry canceller (`jobs/p2p_expiry_canceller.go`, every minute, failure-streak alerted) is always on whenever the PM release key is set: it cancels expired ordinary P2P trades 30 min after the expiry notice. Every tick defers while the central-account statement (the inbound poller, when Flutterwave is configured) is stale, so a paid order is never cancelled on a blind spot.
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
- **Gas top-up** `POST /p2p/gas/top-up` `{action, tradeId?, adId?, chainId?}` → `{txHash?, amountWei?, skipped, chainId}`. The chain is the ad's or trade's (a `chainId` that differs is `gas_not_needed`), else `chainId` (absent = Sonic, never BSC); each chain drips from its own lane (S on Sonic, BNB on BSC — `services.P2PGasLane`), a chain with an escrow but no lane is `gas_unavailable`, the per-user daily cap is one across chains and the unused-drip rule is per chain. Actions: `approve`, `deposit`, `withdraw`, `cancel_ad` (adId), `open_trade` (adId), `mark_paid`, `release`, `cancel_trade`, `dispute` (tradeId). S is sent to the caller's proven wallet only when the action is really her turn (`RequireP2PGasNeed` mirrors each `Prepare*` gate; Sonic only). Codes: 400 bad action/id; 403 `WALLET_PROOF_REQUIRED` / `kyc_verification_required` / `kyc_legal_name_missing` (open_trade when OpenTrade's KYC tier would refuse her: a sell-ad needs a legal name) / not her own linked wallet (`ErrSponsorWallet`); 409 `gas_not_needed`, `gas_topup_unused` (last drip < 1 h old and not yet spent), `sponsorship_in_progress`; 429 daily cap (`P2P_GAS_TOPUPS_PER_DAY`, skips don't count); 503 `gas_unavailable` (no lane, sponsor cannot pay amount + fee, over ceiling, budget, RPC, revert; an unconfirmed drip includes `data{txHash,amountWei}`). Per-user cap, in-flight lock and unused-drip memory are in-process.
- **Direct P2P on BSC (decisions 2026-10-12, plan p2p-bsc).** Direct user-to-user P2P moves to a BSC P2PEscrowV3 with its own FeeManager whose admins are dispute arbiters only; Sonic keeps PM trades. Each P2P preset row has an `audience` (`services/p2p_audience.go`): `pm` (Sonic after the switch) lets only an active Payment Merchant create/sign/resume/fund ads there and opens only PM ads (browse hides the rest from all but their owner); `direct` (the BSC rows) refuses PM sellers and AutoRelease; `all` gates nothing. Refusals are 409 `p2p_direct_moved` / `p2p_pm_not_on_direct`; exits (release, cancel, dispute, resolve, withdraw, confirms, reconcile, cancel-ad) are never gated; `GET /p2p/chains` entries carry a per-caller `adsOpen`. Switched 2026-10-17 (plan API-11): Sonic is `pm`, flipped in the same change that filled the BSC mainnet row (escrow 0xD16c…EB03, FeeManager 0xe760…74ac, owner = the deployer 0xB3D5…2834, arbiter 0x2a88…0A0D, 1.9% `p2p_platform`), so there was no window without direct P2P. A `direct` row is registered only when its boot gates pass (`services/p2p_direct_gates.go`: bytecode, `eth_chainId`, `paymentWindow()` = 30 min, `feeManager()` = the preset's, assets accepted with 18 dp read on-chain, no hot key — every configured signer, `PM_ADMIN_EOA_ADDRESS`, the production operator 0x38cE and treasury 0xA412 — as admin or owner, non-zero `systemWallet`, zero `trustedForwarder`, `owner()` = the row's `expectedOwner`, a receipt probe, and the FeeManager's `owner()` = the same `expectedOwner`). Each failure logs `ALERT p2p_direct_escrow_gate_failed kind=wrong|unread`. A **wrong** value leaves the escrow dormant (unregistered) and unregisters its FeeManager (`p2p_direct_fee_manager_unregistered`). A gate that only could not be **read** (RPC error, timeout, 403 receipts) keeps the escrow registered so live trades keep every exit, but refuses new activity on it with 503 (`ErrChainUnavailable`, `ALERT p2p_direct_escrow_unverified`) and re-checks every minute until all gates pass. The admin trade view (`GET /admin/p2p/trades/:id`) returns `escrowAddress`, which the console checks a resolve's `to` against. The row's FeeManager is BSC's; a different `FEE_MANAGER_ADDRESS_BSC` is fatal. Boot refuses (`p2p_direct_unconfigure_refused`) to empty a direct row while non-slice trades are non-terminal or ads pending/active/paused on that chain. No new keys or env vars: BNB drips use the gas_sponsor key on BSC (`buildP2PChainGasSponsorships`, limits `RAMP_SPONSOR_*_BSC` + `RAMP_SPONSOR_DAILY_GAS_BUDGET`, guard and float monitor `p2p_bsc_gas`; nonce shared with any ramp `bsc` lane through the one KeyLane), and the expiry canceller cancels expired BSC trades with permissionless `cancelByTimeout` on the operator's BSC lane (`releaseBroadcasters` covers every P2P chain). `ExpressMatch` reads its amount on one chain (`chainId`, default Sonic). After the switch, boot sends each non-PM user with a pending/active/paused ad or a confirmed deposit on a `pm` escrow one `p2p.direct_moved` notice (push + email + in-app, `NotifyDirectMoved`; skipped for anyone who already has one, so every boot is safe; the app routes it to My ads).
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
