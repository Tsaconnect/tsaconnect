# P2P Backend — Two-Sided (v2) Integration Plan

> **For agentic workers:** implement task-by-task with TDD. After each task run the relevant Go tests. The backend evolves the existing v1 P2P stack to the v2 two-sided escrow — **preserve the existing pooled sell-ad flow unchanged** and **add the JIT buy-ad / sell-side flow**.

**Goal:** Make the Go API support the two-sided escrow: merchants post **buy-ads**, users **sell crypto** to them by depositing just-in-time on-chain (`openTradeWithDeposit`), with a role-aware lifecycle and funding-branched accounting/reconciliation.

**Branch:** `feat/p2p-two-sided` (already checked out). Build dir: `tsa-api-go/`.

**Tech stack:** Go 1.24, Gin, GORM, go-ethereum ABI. Tests: pure-Go SQLite rig (raw DDL, `SetMaxOpenConns(1)`) + synthetic receipts + `mockEscrow`. gcc IS installed, so cgo handler tests run.

**Verify commands:**
- Build: `go build ./...`
- Targeted: `go test ./internal/services -run 'P2P|Reputation|MerchantStats|PaymentProvider' -count=1`
- Jobs: `go test ./internal/jobs -count=1`
- Handlers (cgo): `go test ./internal/handlers -run 'P2P|Seller|Rate|Dispute|SignAd|OpenTrade|BuyAd' -count=1`

## Core design decisions (locked)
1. **Role model is unchanged at the lifecycle layer.** `SellerID` = crypto-provider/releaser, `BuyerID` = crypto-recipient/marks-paid. Two-sidedness is captured *only* at trade-open by **who is assigned each role** + a new `Funding` discriminator. So `MarkPaid`/release/dispute role checks need NO change — they're already correct once roles are assigned right.
   - **Sell-ad** (today): `SellerID`=merchant (ad poster), `BuyerID`=user; `Funding=Pooled`.
   - **Buy-ad** (new): `SellerID`=user (depositor), `BuyerID`=merchant (ad poster); `Funding=DepositedByTaker`.
2. **Branch on `ad.Side` inside the existing service/handlers** — no new routes. `POST /p2p/ads` creates a sell- or buy-ad by `side`; `POST /p2p/ads/:id/trades` opens a pooled or JIT trade by the ad's side.
3. **Buy-ads have no voucher, no on-chain pool, no `/sign`, no on-chain cancel.** A buy-ad is created `active` directly. Its `Remaining`/`Reserved` are **off-chain capacity** counters (decrement on open, restore on cancel/refund) — never tied to an on-chain pool balance.
4. **Funding-branch every pool-mutation site.** For `Funding=DepositedByTaker`, the crypto moves to/from the **user's wallet** on cancel/refund (not a pool), so all `ad.reserved`/`ad.remaining`-as-on-chain-pool semantics are replaced by the off-chain-capacity restore, and no on-chain pool credit happens. Read `Funding` from **on-chain truth** (`ReadTrade`) in the reconciler.
5. **Build against the v2 ABI.** v2 isn't deployed yet; live wiring happens at cutover. All tests use synthetic receipts/mocks (no chain). The configured address stays v1 until cutover — that's fine, this code is exercised by unit tests now.
6. **`autoReleaseAt`/`TradeAutoReleased` are decode-but-ignore** (auto-release is a later sub-project, default-disabled). But `VerifyTradeReleased` must not be the only release recognizer — see Task 6.

---

## Task 1: `P2PTrade.Funding` model + constants + test rig column

**Files:** `internal/models/p2p_trade.go`, `internal/config/database.go`, test DDL files.

- [ ] Add to `P2PTrade` (after `ChainID`): `Funding uint8 \`gorm:"not null;default:0" json:"funding"\``. Add constants:
  ```go
  const (
      P2PTradeFundingPooled        uint8 = 0 // sell-ad: crypto from merchant's pool
      P2PTradeFundingDepositedByTaker uint8 = 1 // buy-ad: crypto deposited JIT by the user
  )
  ```
- [ ] In `internal/config/database.go` where P2P CHECK constraints are added via `DB.Exec`, add (Postgres only — guard like the existing checks): `ALTER TABLE p2p_trades ADD CONSTRAINT chk_p2p_trades_funding CHECK (funding IN (0,1))` (idempotent pattern matching the existing ones). AutoMigrate adds the column automatically.
- [ ] Update the SQLite test DDL for `p2p_trades` (in `internal/services/p2p_trade_service_test.go` `setupTradeDB` and `internal/handlers/p2p_handler_test.go` `setupP2PTestDB`): add `funding INTEGER NOT NULL DEFAULT 0`.
- [ ] **Test:** add `TestP2PTradeFunding_Constants` asserting the values match the contract enum (0/1), mirroring the existing status-enum parity test.
- [ ] `go build ./...` clean; commit.

## Task 2: Escrow service — v2 ABI, struct, decode, new bindings/parsers

**File:** `internal/services/p2p_escrow_service.go` (+ its test file).

- [ ] **ABI string:** (a) change `getTrade` outputs to the v2 10-field order: `seller, buyer, token, amount, createdAt, status(uint8), funding(uint8), feeBps(uint16), autoReleaseAt(uint256), voucherHash(bytes32)`. (b) Change `TradeCancelled` event to `[{indexed tradeId bytes32},{amount uint256},{returnedToPool bool}]`. (c) Add function `openTradeWithDeposit(bytes32 tradeId, address token, uint256 amount, address merchant)`. (d) Add event `TradeOpenedWithDeposit` with indexed `tradeId, seller, buyer` and non-indexed `token, amount`. (e) Add event `TradeAutoReleased(bytes32 indexed tradeId, address indexed buyer, uint256 amountToBuyer, uint256 feeToSystem)`.
- [ ] **`OnChainP2PTrade` struct:** add `Funding uint8`, `FeeBps uint16`, `AutoReleaseAt *big.Int`. **`ReadTrade`**: change `len(vals) != 7` → `!= 10` and decode in the new order — `Funding: uint8(vals[6].(uint8))`, `FeeBps: vals[7].(uint16)`, `AutoReleaseAt: vals[8].(*big.Int)`, `VoucherHash: vals[9].([32]byte)`. (Carefully: `feeBps` unpacks as `uint16`.)
- [ ] **`P2PTradeCancelledEvent`:** replace `ReturnedToAvailable *big.Int` with `Amount *big.Int` and `ReturnedToPool bool`. Update `parseTradeCancelledFromReceipt`: 2 non-indexed fields now (`amount`, `returnedToPool`).
- [ ] **New:** `P2PTradeOpenedWithDepositEvent{ TradeID [32]byte; Seller, Buyer, Token common.Address; Amount *big.Int }`; `parseTradeOpenedWithDepositFromReceipt` (3 indexed topics: tradeId, seller, buyer; 2 non-indexed: token, amount); `VerifyTradeOpenedWithDeposit(txHash string, expectedTradeId [32]byte)`; and the builder:
  ```go
  func (s *P2PEscrowService) PrepareOpenTradeWithDeposit(tradeId [32]byte, token string, amount *big.Int, merchant, from string) ([]byte, error) {
      data, err := parsedP2PEscrowABI.Pack("openTradeWithDeposit", tradeId, common.HexToAddress(token), amount, common.HexToAddress(merchant))
      if err != nil { return nil, fmt.Errorf("pack openTradeWithDeposit: %w", err) }
      return s.buildUnsignedTx(from, data)
  }
  ```
- [ ] **Release recognizer:** `VerifyTradeReleased` parses `TradeReleased`. Add a sibling that also accepts `TradeAutoReleased` (same field layout) so a release that happened via auto-release still verifies. Simplest: make `parseTradeReleasedFromReceipt` try `TradeReleased` then fall back to `TradeAutoReleased`.
- [ ] **Tests** (`p2p_escrow_service_test.go`): update the status-enum parity test if needed; add a `Funding` enum parity test; add synthetic-receipt tests for `parseTradeOpenedWithDepositFromReceipt` and the new `parseTradeCancelledFromReceipt` (amount + bool); a `ReadTrade` decode test for the 10-field layout (construct ABI-packed output). Follow the existing synthetic-receipt test pattern in this file.
- [ ] `go build ./...` then `go test ./internal/services -run 'P2PEscrow' -count=1`. Commit. (Note: this task changes the adapter surface — Task 3 updates the interface + mocks; build may not be fully green until Task 3. That's acceptable for this one task; get the escrow package compiling + its own tests passing.)

## Task 3: Adapter interface + mocks + ReadTrade callers

**Files:** `internal/services/p2p_trade_service.go` (the `P2PEscrowAdapter` interface ~L57-91), all mocks (`p2p_trade_service_test.go`, `p2p_handler_test.go`), reconciler caller.

- [ ] Add to the `P2PEscrowAdapter` interface: `PrepareOpenTradeWithDeposit(tradeId [32]byte, token string, amount *big.Int, merchant, from string) ([]byte, error)` and `VerifyTradeOpenedWithDeposit(txHash string, expectedTradeId [32]byte) (*P2PTradeOpenedWithDepositEvent, error)`.
- [ ] Update **every** mock implementing the adapter (`mockEscrow` in both test files) to implement the 2 new methods (return canned values controllable per-test).
- [ ] Fix every reference to the renamed `P2PTradeCancelledEvent.ReturnedToAvailable` (now `.Amount`/`.ReturnedToPool`) across services + tests + audit-log call sites.
- [ ] `go build ./...` clean (the whole module compiles now). Commit.

## Task 4: Trade service — buy-ad creation + JIT open + funding branches

**File:** `internal/services/p2p_trade_service.go`.

- [ ] **`CreateAd`** (or a new `CreateBuyAd`): accept a `side`. For `side == "buy"`: skip `ReadBalance`/pool precheck, skip voucher fields, set `Status = active` directly (no `pending`→sign), require token/fiat/rate/limits/payment-methods (the merchant's PMs — where they'll send fiat). Set `Side = "buy"`. `Remaining = Amount` (off-chain cap), `Reserved = 0`. Keep sell-ad path identical.
- [ ] **`OpenTrade`**: branch on `ad.Side`. Keep the pooled path unchanged for sell-ads. For buy-ads, route to a new `openTradeWithDeposit` path:
  - `SellerID = openerUserID` (the user selling crypto), `BuyerID = ad.SellerID` (the merchant). `Funding = DepositedByTaker`. Self-trade check: `ad.SellerID == openerUserID` → reject (a merchant can't take their own buy-ad).
  - Compute `tokenAmount` from `fiatAmount`/rate (same helper). Validate against ad limits + remaining off-chain capacity (`ad.Remaining`).
  - No voucher; call `escrow.PrepareOpenTradeWithDeposit(onChainTradeID, ad.Token, tokenAmount, merchantAddress, userAddress)` where `userAddress` is the opener's wallet (the depositor/`from`) and `merchantAddress` is the merchant's wallet (the `merchant` arg / cryptoRecipient). The merchant wallet comes from the merchant user's wallet address (look up like sell-ad uses `ad.SellerAddress`; for a buy-ad store/lookup the merchant's wallet — add a `MerchantAddress`/reuse `SellerAddress` on the ad as the merchant's recipient wallet, set at buy-ad creation).
  - In the DB txn: decrement `ad.Remaining` / increment `ad.Reserved` (off-chain capacity), create the trade `pending_payment`, `Funding=DepositedByTaker`, `ExpiresAt = now+30m`. Emit events with `"side":"buy"` in Data.
- [ ] **`ConfirmOpenTrade`**: branch — for buy-ads verify via `VerifyTradeOpenedWithDeposit` and cross-check `seller==user`,`buyer==merchant`.
- [ ] **`ConfirmRelease`**: the on-chain release moves crypto to `BuyerID` (merchant on buy-ad) minus fee — identical on-chain. Skip the `ad.reserved`/`remaining` pool-exhaust math when `Funding=DepositedByTaker` (a buy-ad's reserved capacity is consumed permanently on release — decrement `ad.reserved` only, do NOT re-credit `remaining`). Keep sell-ad behavior unchanged.
- [ ] **`ConfirmCancel`**: use `ev.Amount`. Funding-branch: sell-ad → `ad.reserved -=`, `ad.remaining +=` (as today). Buy-ad → restore off-chain capacity (`ad.reserved -=`, `ad.remaining +=`) AND the crypto already went to the user's wallet on-chain (nothing else to do); fix the system message (don't say "returned to seller's ad" for buy-ads — say "crypto returned to your wallet").
- [ ] **`ConfirmAdminResolveDispute`** (`refundBuyer=false` path): funding-branch identically (buy-ad refund went to the user's wallet on-chain; restore off-chain capacity only).
- [ ] **Tests:** add service tests (pure-Go SQLite rig + mockEscrow): create buy-ad; open a JIT trade (assert SellerID=user, BuyerID=merchant, Funding=1, no voucher, Remaining decremented); confirm-open; markPaid by merchant; release by user; cancel (capacity restored, no double-credit); admin-resolve refund-to-seller (buy-ad) restores capacity. Keep all existing sell-ad tests green.
- [ ] `go build ./...` + `go test ./internal/services -run 'P2P' -count=1`. Commit.

## Task 5: Handlers + DTOs

**File:** `internal/handlers/p2p_handler.go`.

- [ ] `createAdRequest`: add `Side string \`json:"side"\`` (default "sell"). Validate `side ∈ {sell,buy}`. Route to the buy-ad branch. For buy-ads the merchant role gate (`require verified` + merchant) still applies (only merchants post ads).
- [ ] `openTradeRequest`: add optional `SellerAddress string` and `TokenAmount string` (used on the buy-ad/JIT path; for sell-ads `BuyerAddress` is still used). The handler passes both through; the service decides based on `ad.Side`.
- [ ] `OpenTrade` handler: no role logic needed — pass `user.ID` + addresses to the service which branches on `ad.Side`. Map the new error types to clean HTTP codes (self-take, insufficient capacity).
- [ ] Buy-ad: ensure `/sign`, `/cancel` (on-chain) handlers return a clear "not applicable for buy-ads" error (or the service no-ops the DB-only cancel). A buy-ad cancel is a DB status flip (merchant pauses/cancels their buy-ad) — add that to `PrepareCancelAd`/`ConfirmCancelAd` or a dedicated branch.
- [ ] **Tests** (cgo handler rig): `TestCreateBuyAd_OK`, `TestOpenBuyAdTrade_DepositPath` (asserts the service is called with the deposit path + correct roles), `TestOpenBuyAdTrade_RejectsMerchantSelfTake`. Follow `p2p_handler_test.go` patterns.
- [ ] `go test ./internal/handlers -run 'P2P|BuyAd|OpenTrade' -count=1`. Commit.

## Task 6: Reconciler + watchdog funding-awareness

**Files:** `internal/services/p2p_trade_service.go` (reconcile helpers), `internal/jobs/*`.

- [ ] In `ReconcileTrade`, read `Funding` from the on-chain `ReadTrade` result (now available) — prefer it over the DB column to avoid a wrong-branch refund.
- [ ] `reconcileToCancelled`, `reconcileToReleased`, `reconcileToResolved`: funding-branch the `ad.reserved`/`remaining` mutations exactly as Tasks 4's confirm-paths do. For `DepositedByTaker`, restore off-chain capacity on cancel/refund; on release, decrement reserved only (no remaining credit). Never assume an on-chain pool for buy-ads.
- [ ] `timeout_watchdog.go`: message text is sell-ad flavored ("Reclaim your USDT") — make it side-aware (cosmetic). No accounting in the watchdog.
- [ ] **Tests:** extend the reconciler tests (`p2p_trade_service_test.go` / `chain_reconciler_test.go`) with a buy-ad/JIT trade reconcile case for each terminal state, asserting funding-branched capacity handling. 
- [ ] `go test ./internal/services ./internal/jobs -run 'Reconcile|Recon|P2P|Watchdog|Timeout' -count=1`. Commit.

## Task 7: Full build + suite + self-review

- [ ] `go build ./...` clean.
- [ ] `go vet ./internal/services ./internal/handlers ./internal/jobs`.
- [ ] `CGO_ENABLED=1 go test ./internal/services ./internal/handlers ./internal/jobs -count=1` — P2P suites green (note: pre-existing unrelated wallet/cart/order failures may persist; the P2P-specific tests must pass).
- [ ] Self-review against this plan: every funding-branch site covered? `ReturnedToAvailable` fully renamed? new ABI fields decoded in the right order? Commit any fixes.

## Out of scope (later sub-projects)
- Relayer/Permit2-client, auto-release keeper + ⚡ UX, dispute-v2 off-chain, the cutover (repoint `sonicPresets[*].p2pEscrow` to the deployed v2 + bump the app voucher EIP-712 domain version to "2").
- The app changes (#6) are a separate plan.

## Risks to watch
- **`getTrade` field reorder** (voucherHash 6→9): silent mis-decode if the index isn't updated — the 10-field decode test guards it.
- **EIP-712 domain version** bumped to "2" in v2 — only affects sell-ad voucher signing (app + the contract `hashVoucher` view handles it server-side). Flagged for cutover; not in scope here.
- **Buy-ad merchant wallet** (cryptoRecipient): must be captured at buy-ad creation (reuse `SellerAddress` as "the ad poster's wallet" — for a buy-ad that's the merchant who receives crypto).
