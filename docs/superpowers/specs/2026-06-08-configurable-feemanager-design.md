# Configurable FeeManager — Design Spec

**Date:** 2026-06-08
**Status:** Revised after independent review (closes all 32 findings; two architecture decisions now locked)
**Context:** Extends the fee/TP overhaul (`feat/fee-tp-overhaul`, PR #34). The v2 distribution engine, per-feature split table, and the `FEE_MODEL_V2_ENABLED` flag already exist and are dormant. This spec adds **admin-configurable fees with a single on-chain source of truth (the FeeManager)** and a backend cache that is a strict read replica of it.

> **What changed in this revision (review verdict: needs-revision).** The two biggest review items — the source-of-truth contradiction and the two-tx drift class — are resolved by two locked decisions:
> 1. **A1 LIVE-READ (replaces the old A2).** On-chain escrows read the rate **directly** from the FeeManager via `IFeeManager.rateFor`. There is **no** escrow-side rate copy that an admin must separately set, so there is no second tx to drift. P2P snapshots the rate into the Trade at `openTrade`; Product/Service read it at settlement. One authoritative write per change = `FeeManager.setRate`.
> 2. **ALL RATES ON FeeManager + CACHE-AS-READ-REPLICA.** Every fee rate (on-chain-collected *and* backend-charged) lives on the FeeManager (single config surface + on-chain transparency). The FeeManager is the **write side / source of truth** (CQRS). The backend keeps a **read replica** (the repurposed `fee_config` table) refreshed when a `RateSet` change is *confirmed* on-chain. The charging hot-path reads the **cache**, never a live `eth_call`. Backend-charged fees have **no** on-chain enforcer (the backend *is* the enforcer, reading the cache); on-chain-collected fees ARE enforced by escrows reading the FeeManager live. Write→chain, read→replica — a single truth, not a circular one.

---

## 0. Decisions locked

| # | Decision | Choice |
|---|----------|--------|
| D1 | Source of truth | **On-chain FeeManager (per chain) is the single authoritative write side** for every fee rate (on-chain-collected AND backend-charged). The backend `fee_config` table is a **read replica** refreshed on confirmed `RateSet`. The charging hot-path reads the replica; it never blocks on a live `eth_call`. (Resolves the review's circular-truth blocker: write→chain, read→cache.) |
| D2 | Registry scope | The FeeManager holds **all fee RATES**. The distribution **split percentages stay off-chain** in the v2 engine. Backend-charged fees (withdrawal/swap/allswap/OTC/spot/instant-*) keep their authoritative *rate* on the FeeManager for transparency, but are **enforced by the backend reading the cache** — there is no on-chain collector for them. |
| D3 | Granularity | **Global default + per-chain override** per `(feature, token)`. Resolution happens off-chain at authoring time; each chain's FeeManager stores that chain's *resolved effective* rate. Backend-charged fees are per-chain only insofar as the backend threads `chainID` into `RateFor` (see §6); where it doesn't yet, they are documented as global. |
| D4 | Enforcement model | **A1 LIVE-READ.** Escrows read the rate from the FeeManager at the moment they need it: P2P reads `platformFeeBps`+`gasFeeBps` at `openTrade` and **snapshots both** into the Trade; Product/Service read at settlement. No escrow-side rate setter the admin must call; no escrow rate copy to drift. (Replaces the old A2 "mirror the rate into each escrow setter" model. Structurally eliminates the B1 drift class, the I3 snapshot-mismatch, and the I5 reversibility tangle.) |
| D5 | On-chain splits | At cutover the escrows stop splitting cashback/upline/provider **on-chain** and send 100% of the collected fee to `systemWallet`; the off-chain v2 engine becomes the sole splitter (rewards in MCGP). This is **gated by a `legacySplitEnabled` bool** in each escrow (deployed `true` = behaves exactly like today; flipped `false` at cutover). The bool makes the upgrade reversible and decoupled from rate-change timing. **ServiceContact `feeAmount` stays > 0** — `feeAmount = 0` would *brick* `payContactFee` (it reverts at `require(fee >= MIN_FEE_AMOUNT)`); the split is zeroed via the `legacySplitEnabled` branch, not by zeroing the fee. |
| D6 | Remaining collection | Fiat `CreateWithdrawal` accrues a `FeeEvent` **in the same DB tx** as the balance deduction; AllSwap via a server-verified settle endpoint; P2P platform fee accrued off-chain from the **snapshotted** `platformFeeBps`. |
| D7 | Owner / governance | The FeeManager owner **and** each escrow's `owner()` (UUPS upgrade authority) is a **Gnosis Safe multisig** (§3, §14). `MAX_BPS` is enforced *in the contract*. A short **timelock** is recommended (justification in §3). |
| D8 | Out of scope | OTC taker fee flip (desk is spread-only — deferred, rate row stays ready and admin-configurable); net-new features Spot-aggregator / Instant-pay-crypto / Instant-transfer-fiat (don't exist; engine rows already seeded, rate-only). |

---

## 1. Goals / non-goals

**Goals**
- Every fee rate is admin-configurable from a single authoritative place (the FeeManager) without a code deploy.
- The on-chain rate is the source of truth; the backend never charges or quotes a rate that diverges from the last *confirmed* on-chain value.
- Per-chain differentiation (Sonic vs BSC vs future) with a manageable default+override model.
- Preserve the signed-but-not-broadcast invariant — the backend builds unsigned txs; the multisig signs; the backend confirms. No backend key custody.
- Fully flag-gated: with `FEE_MODEL_V2_ENABLED` OFF, live charging/splitting is byte-for-byte unchanged.
- **Never charge $0 or an unbounded fee**, including on cold start / RPC outage.

**Non-goals**
- Moving the split/distribution math on-chain (stays off-chain).
- Building the net-new trading features (D8).
- Changing the reward payout (MCGP merkle distributor) — unchanged.

---

## 2. Architecture — desired-state / actual-state / read-replica

```
Admin (Gnosis Safe multisig)
   │  authors "rate intent": global default + per-chain overrides per (feature, token)
   ▼
[ fee_rate_intent ]  (off-chain, DESIRED state)
   │  backend resolves per-chain effective rate, diffs vs on-chain, builds UNSIGNED txs
   │  plan = ONLY FeeManager.setRate / setRates tx(s) per chain  (NO escrow setters — A1)
   ▼
multisig signs + broadcasts  ──►  [ FeeManager ]  (per chain — SOURCE OF TRUTH / write side)
                                       │  emits RateSet(key, feature, token, bps, flatUSD, mcgpExempt, active)
                                       │
         ┌─────────────────────────────┴───────────────────────────────┐
         │ (A) on-chain-collected fees                                   │ (B) backend-charged fees
         ▼  escrows LIVE-READ rateFor at the moment of charge            ▼  no on-chain collector
   P2PEscrowV3.openTrade ─ reads platformFeeBps+gasFeeBps,         confirm verifies receipt → refreshes
     SNAPSHOTS both into Trade (in-flight unaffected by later change)    fee_config READ REPLICA
   ProductEscrowV2 / ServiceContactV2 ─ read rateFor at settlement       │
         │ (collected fee → systemWallet when legacySplitEnabled=false)  ▼
         │                                                        FeeConfigService.RateFor(feature,token,chain)
         ▼                                                          reads CACHE only (never eth_call on hot-path)
   [ v2 engine ] splits collected fee off-chain → cashback/TP in MCGP (sole splitter after D5)
                                                                          ▼
                                              cart quote · withdrawal · allswap · swap charge
                                              (all read the SAME cached rate → quote==charge==split-base)
```

- **Desired-state** = the admin's intent (default + overrides). Authoring + diff/UX only; never charges.
- **Source of truth (write side)** = each chain's FeeManager. What is actually charged on-chain.
- **Read replica** = `fee_config`, refreshed only when a `RateSet` is *confirmed*. The hot-path reads this. It can never be "ahead" of chain because it is written from a confirmed receipt + read-back (§5, §11).
- The admin flow is **plan/apply** (Terraform-style): diff intent vs on-chain, emit the FeeManager tx(s), confirm, then refresh the replica.

---

## 3. The FeeManager contract (new, one per chain, UUPS)

```solidity
struct Rate {
    uint16 bps;        // basis points; 0 allowed for backend-charged "rate=0" cases
    uint64 flatUSD;    // flat component in 1e6 units (e.g. 100000 = $0.10)
    bool   mcgpExempt; // when the token is MCGP this feature charges 0
    bool   active;     // false = feature disabled / not configured
}

uint16 public constant MAX_BPS = 1000;     // 10% hard ceiling, fat-finger guard
mapping(bytes32 => Rate) private rates;    // key = keccak256(abi.encode(feature, tokenSymbol))

function rateFor(string calldata feature, string calldata token)
    external view returns (uint16 bps, uint64 flatUSD, bool mcgpExempt, bool active);

function setRate(string calldata feature, string calldata token, Rate calldata r) external onlyOwner;  // require(r.bps <= MAX_BPS)
function setRates(string[] calldata features, string[] calldata tokens, Rate[] calldata rs) external onlyOwner; // batched; each bps <= MAX_BPS

event RateSet(bytes32 indexed key, string feature, string token, uint16 bps, uint64 flatUSD, bool mcgpExempt, bool active);
```

- **Owner = Gnosis Safe multisig** (also the UUPS upgrade authority via `_authorizeUpgrade onlyOwner`). The same Safe owns all three escrows so `owner()` can never diverge across contracts. Provisioning + signer-recovery: see §14.
- **`MAX_BPS` is enforced IN the contract** (`require(r.bps <= MAX_BPS, "rate too high")` in both setters) — a fat-finger `10000` (100%) is impossible on-chain (closes B8). `MAX_BPS` itself is a `constant`; changing the ceiling is a deliberate UUPS upgrade.
- **`flatUSD` is `uint64` in 1e6 units** so it matches the existing `models.FeeConfig.FlatAmountUSD` semantics and `ServiceContact` token-unit math.
- **Timelock (D7):** recommend wrapping the Safe behind a short (e.g. **6h**) `TimelockController` so the backend reconciler and on-call can see a pending rate change before it lands. Justification for *short* (not 48h): fee changes are not adversarial governance — they are operational and occasionally need same-day response to network-fee spikes; 6h balances visibility against responsiveness. **Decision: ship with a 6h timelock on `setRate`/`setRates` and `_authorizeUpgrade`.** An emergency path exists only via the Safe's own threshold + the documented escalation in §14 (no separate backend key).
- **No pause needed on the FeeManager itself** — it holds no funds and only stores rates; the escrows that consume it are independently pausable.
- Events drive the off-chain replica refresh + the drift reconciler (§10).

### 3.1 `IFeeManager` and the escrow live-read wiring (A1)

Each escrow gains a minimal consumer interface, an immutable-after-set address slot, and a one-time owner setter:

```solidity
interface IFeeManager {
    function rateFor(string calldata feature, string calldata token)
        external view returns (uint16 bps, uint64 flatUSD, bool mcgpExempt, bool active);
}

// added to each escrow's storage (above its __gap):
address public feeManager;            // FeeManager proxy for this chain
bool    public legacySplitEnabled;    // deployed TRUE; flipped FALSE at cutover (D5)

function setFeeManager(address fm) external onlyOwner {     // set once at upgrade
    require(fm != address(0), "fm=0");
    address old = feeManager; feeManager = fm; emit FeeManagerUpdated(old, fm);
}
function setLegacySplitEnabled(bool on) external onlyOwner {
    legacySplitEnabled = on; emit LegacySplitEnabledUpdated(on);
}
```

- The **`token` argument is the token symbol string** (e.g. `"USDT"`), not the address — matching the FeeManager key and the off-chain `fee_config` key, so on-chain and off-chain resolve identically. Escrows hold the symbol per accepted token (a small `mapping(address => string) public tokenSymbol`, owner-set when a token is accepted) OR pass a symbol the caller cannot forge because the rate is read by the *contract*, keyed off the order's stored token; the implementation reads `tokenSymbol[order.token]`. (Storage cost noted in each escrow section below.)
- **There is no escrow rate setter the admin calls.** A1 means the only authoritative write is `FeeManager.setRate`. This is the structural fix for B1/I3.
- **"Live-read" here is an on-chain `STATICCALL`, NOT a backend `eth_call` — they are distinct despite both being "reads."** When `P2PEscrowV3.openTrade` (or Product/Service at settlement) calls `IFeeManager(feeManager).rateFor(...)`, that is a contract-to-contract call **inside the user's own transaction**: deterministic, executed by every validator, paid as a small amount of gas (a few hundred gas for the external `view` + SLOADs), with **no latency, timeout, or RPC-availability risk**. It is in the same trust/availability domain as any other SLOAD the escrow already does. This is categorically different from the *backend's* `FeeConfigService.RateFor`, which serves off-chain charging/quoting (cart, withdrawal, allswap) and **must never** do a per-request JSON-RPC `eth_call` (that would add latency + an availability dependency to the charge path) — the backend reads the cache (§4/§11). So §2/§11's "never blocks on `eth_call`" is a statement about the **backend hot-path only**; the escrow's on-chain `rateFor` read is expected and safe. Implementers: do **not** add a backend `eth_call` to satisfy the escrow live-read, and do **not** try to remove the on-chain `rateFor` call from `openTrade` — they are two different mechanisms.

---

## 4. Off-chain: rate intent, resolution, read-replica

- **`fee_rate_intent`** (new table): `(feature, token, chain_id NULLABLE) → {bps, flat_usd, mcgp_exempt, active}`. `chain_id = NULL` is the global default; a row with a `chain_id` is that chain's override. **DESIRED state only — never charges.**
- **Resolution:** effective rate for `(feature, token, chain)` = chain-specific override `??` global default. Mirrors today's token-specific → wildcard (`FeeTokenAny = "*"`) resolution, extended with the chain dimension.
- **`fee_config` table repurposed as the read replica** of the *confirmed* on-chain `rateFor` values. It gains a nullable `chain_id` column (NULL = the legacy global row, for backward compatibility and backend-charged fees that the backend doesn't yet thread chain into). `FeeConfigService.RateFor(feature, token, chainID)`:
  1. reads the **in-memory cache** (loaded from `fee_config`, keyed `feature|token|chain` with `chain`-then-global fallback). **No `eth_call` on this path.**
  2. on cache hit → return it.
  3. on cache miss / cold start → see §11 (last-known-good → seed default → loud alert; **never 0/unbounded**).
- **Replica refresh is event-driven, not admin-triggered guesswork.** It happens on a **confirmed `RateSet`** (the `confirm` endpoint reads back `rateFor` and writes the verified value into `fee_config`, §5). A backend `RateSet` log subscriber (or the confirm-on-broadcast path) is the only writer of the replica from chain truth. The old behavior of "invalidate the cache the moment the admin clicks save" is removed — it caused the quote-ahead-of-chain bug the review flagged.

> **Current-code delta (closes B2/B3 against reality).** `fee_config_service.go:185` `RateFor(feature, tokenSymbol)` today has **no `chainID` param and no `eth_call`** and returns `(0,0,false,false)` on miss (line 196). This spec: (a) adds the `chainID` param + the `fee_config.chain_id` column; (b) replaces the silent `(0,...)` miss return with the §11 fallback ladder; (c) leaves the read path as **cache-only** (the source of truth reaches the cache via confirmed `RateSet`, never via a hot-path `eth_call`). The package helpers `FeeRate` / `FeeFlatUSD` keep their built-in `defaultFeeRows` fallback (which becomes the **seed default** clamp tier of §11).

---

## 5. Admin "set-fee" flow (signed-but-not-broadcast)

Reuses `UnsignedTx{To, Value, Data, ChainID}` (the merkle-publisher pattern). Under A1 the plan contains **only FeeManager txs** — no escrow setters.

**Endpoints**

1. `POST /api/admin/fees/intent` — super-admin. Persists to `fee_rate_intent`. **409 while `FEE_MODEL_V2_ENABLED` is OFF** (matches the existing fee-config gate).
   ```json
   // request
   { "feature": "withdrawal", "token": "*",
     "default": { "bps": 50, "flatUsd": 0, "mcgpExempt": false, "active": true },
     "overrides": { "56": { "bps": 60, "flatUsd": 0, "mcgpExempt": false, "active": true } } }
   // response
   { "intentId": "uuid", "status": "pending" }
   ```
2. `POST /api/admin/fees/plan` `{ "intentId": "uuid" }` → backend resolves per-chain effective rates, reads each chain's on-chain `rateFor`, returns the **diff + unsigned txs** (one `setRate`/`setRates` per affected chain). **Idempotent: empty `txs` when intent already matches chain.**
   ```json
   {
     "intentId": "uuid",
     "diff": [
       { "feature": "withdrawal", "token": "*", "chainId": 146, "before": {"bps":50,...}, "after": {"bps":50,...}, "noop": true },
       { "feature": "withdrawal", "token": "*", "chainId": 56,  "before": {"bps":50,...}, "after": {"bps":60,...} }
     ],
     "txs": [
       { "chainId": 56,
         "to": "0xFeeManagerBSC",
         "value": "0",
         "data": "0x<setRate(\"withdrawal\",\"*\",(60,0,false,true)) abi-encoded>",
         "function": "setRate(string,string,(uint16,uint64,bool,bool))",
         "decoded": { "feature":"withdrawal","token":"*","rate":{"bps":60,"flatUsd":0,"mcgpExempt":false,"active":true} } }
     ]
   }
   ```
   (`decoded` is advisory for the signer UI; `data` is the load-bearing pre-encoded calldata. Closes M3 — concrete example with encoded calldata. Closes I6 — concrete plan schema.)
3. Multisig signs + broadcasts each tx.
4. `POST /api/admin/fees/confirm` `{ "txHash", "chainId", "intentId" }` → backend verifies the receipt (`status == 1`) **and reads back `rateFor`** to confirm the on-chain value equals the intent, THEN writes the verified value into the `fee_config` replica + advances the intent state machine. (Mirrors the hardened `ConfirmPublish` verify from PR #34.)
   - If `status != 1` **or** `rateFor != intent` → **422**, intent stays `pending` for that chain, **replica untouched**, no retry is auto-issued (admin re-signs / re-plans). (Closes I3 confirm semantics.)
   ```json
   { "intentId":"uuid", "chainId":56, "applied": true, "verifiedValue": {"bps":60,...} }
   ```

**Intent state machine (closes I6).** An intent is per-(feature,token) but fans out to N chains:

```
draft ──intent POST──► pending
pending ──plan──► pending (txs emitted; no state change)
pending ──confirm(chainId) ok──► partially_applied (this chain confirmed; others still pending)
partially_applied ──all chains confirmed──► applied
pending/partially_applied ──confirm fail (422)──► stays; that chain remains pending
pending/partially_applied ──DELETE /api/admin/fees/intent/:id──► cancelled (only while not fully applied)
```

The replica row for a chain is updated **only on that chain's successful confirm**, so a half-applied multi-chain rollout never makes one chain quote a rate the chain hasn't accepted.

---

## 6. Backend integration

- `FeeConfigService.RateFor(feature, token, chainID)` → cache-only read replica (§4), with the §11 fallback ladder. Never `eth_call` on the hot-path.
- **Cart-quote fix (closes B5/I8 + the `cart.go:274` reality).** `CalculateCartSummary` today sets `summary.GasFee = GasFeeUSD` (the `models.GasFeeUSD = 0.10` constant, `cart.go:53`, used at line 274). Replace that line with `flat, exempt := services.FeeFlatUSD(models.FeatureProduct, token); if !exempt { summary.GasFee = flat }`, threading the order's settlement token. **Remove the `models.GasFeeUSD` constant** once the call site is migrated (and `MerchantFeeBPS`/`MCGPMerchantFeeBPS` once `CalculatePlatformFee` — `checkout_handler.go:179`, already on `services.FeeRate(FeatureProductMerchant, token)` — no longer references them). The cart must surface **two line items**: the percent merchant component (`FeeRate(FeatureProductMerchant)`, MCGP-exempt → `$0`) and the flat gas component (`FeeFlatUSD(FeatureProduct)`). **Regression test required: `CalculateCartSummary` output == `FeeConfigService.RateFor` for each (feature, token), i.e. quote == charge.** Do NOT ship Phase 1 with a hardcoded fallback that can disagree with the service.
- The v2 engine's `FeeUSD` (split base) and every backend-charged fee (withdrawal, swap, allswap) read the **same** cached effective rate, so charge == split-base == quote.
- **Per-chain for backend-charged fees:** withdrawal/swap settle server-side and today read a global (`token="*"`) row with no chain awareness. This spec threads `chainID` into `RateFor` where the call site has it (withdrawal has `body.Network`; allswap has the route chain). Where a call site has no chain context, it resolves the global row — documented as global, not silently per-chain. (Closes the review's "missing multi-chain override logic" — explicit, not hand-waved.)

---

## 7. Escrow changes (UUPS upgrades — state preserved, A1 live-read, legacy-split gated)

All three are already UUPS-upgradeable; these are implementation upgrades preserving proxy address + state. Each requires re-audit before mainnet. **Common additions** (per §3.1): `address public feeManager`, `bool public legacySplitEnabled` (deployed `true`), `mapping(address => string) public tokenSymbol`, `setFeeManager`, `setLegacySplitEnabled`, `setTokenSymbol(token, sym) onlyOwner`, plus `MAX_*` caps. These consume `__gap` slots — exact slot math per contract below.

### 7.1 P2PEscrowV2 → **P2PEscrowV3** (closes B7, B6, I1-snapshot)

Current layout facts (P2PEscrowV2.sol): `feeBps` at **line 103**, `disputeFeeBps` 107, `paymentWindow` 108, `autoReleaseWindow` 109, `trustedForwarder` 112, `permit2` 113, `autoReleaseOptIn` mapping 116, then `uint256[50] private __gap` at **line 118**. The `Trade` struct (lines 65–76) embeds `uint16 feeBps` at **line 74**. `_settleRelease` (line 396) calls `_splitFee(amt, t.feeBps)` at **line 400**; `adminResolve` calls the same at **line 463**. `MAX_FEE_BPS = 500` (line 83). `_feeBpsForToken` (line 269) returns 0 for MCGP else `feeBps`.

**V3 changes:**

1. **Contract-level state — append AFTER `autoReleaseOptIn` and BEFORE `__gap`, shrinking the gap.** Do **not** repurpose the existing `feeBps`/`disputeFeeBps` slots (that would silently re-key live storage). Keep `feeBps` (it remains a live, audited slot; V3 stops *reading* it for new trades but old trades' snapshots are unaffected — see point 4). New slots, in order:
   ```solidity
   // (existing slots 0..N unchanged: feeBps@103, disputeFeeBps, paymentWindow, autoReleaseWindow,
   //  trustedForwarder, permit2, autoReleaseOptIn mapping)
   address public feeManager;            // packs with legacySplitEnabled into one slot (20B + 1B < 32B)
   bool    public legacySplitEnabled;    // packs into feeManager's slot (no extra slot)
   mapping(address => string) public tokenSymbol;  // +1 slot
   // platformFeeBps/gasFeeBps are NOT stored as contract state — they are READ from FeeManager
   // at openTrade and SNAPSHOTTED into the Trade (below). This is the A1 correction to the
   // review's "add platformFeeBps/gasFeeBps state" wording: there is no cached escrow copy to drift.
   uint256[48] private __gap;            // was [50]; minus 2 new slots = 48
   ```
   **Slot math:** `feeManager` (address, 20B) + `legacySplitEnabled` (bool, 1B) **pack into one slot**; `tokenSymbol` (mapping head) takes one → 2 new slots → `__gap` shrinks `50 → 48`. `MAX_FEE_BPS=500` stays; add `MAX_PLATFORM_FEE_BPS = 300` and `MAX_GAS_FEE_BPS = 300` as `constant`s (no slot cost) so a FeeManager misconfig can't strand a trade (closes B8 escrow-cap).
2. **Trade struct — add two snapshot fields.** The `Trade` is stored in `mapping(bytes32 => Trade) public trades` (line 99); each `Trade` occupies its own keccak-derived slots, so **extending the struct does not collide with contract-level slots or the `__gap`** — it only grows each trade's own slot footprint. Current packing: slot A = `seller`(20) ; slot B = `buyer`(20); slot C = `token`(20)+`amount`uint96(12)=32 packed; slot D = `createdAt`u40 + `autoReleaseAt`u40 + `status`u8 + `funding`u8 + `feeBps`u16 (= 12 bytes, packs into one slot with room); slot E = `voucherHash`. Add:
   ```solidity
   struct Trade {
       address seller; address buyer; address token; uint96 amount;
       uint40 createdAt; uint40 autoReleaseAt; Status status; Funding funding;
       uint16 feeBps;                 // KEPT for old-trade back-compat (set 0 by V3 for new trades)
       uint16 platformFeeBpsSnapshot; // NEW — packs into the same slot D (12B + 2B + 2B = 16B < 32B)
       uint16 gasFeeBpsSnapshot;      // NEW — also packs into slot D
       bytes32 voucherHash;
   }
   ```
   The two new `uint16`s pack into the existing slot-D free space (slot D currently uses 12 bytes of 32), so **no new per-trade slot and no shift of `voucherHash`** — a clean append. The PR must include a `hardhat-storage-layout` (or `forge inspect storage-layout`) **slot-diff** proving exactly this (closes M2/B7-tooling).
3. **`openTrade` / `openTradeWithDeposit` / `*Permit2` — snapshot BOTH from FeeManager.** Replace `feeBps: _feeBpsForToken(v.token)` (lines 318, 348, 588) with a read of `IFeeManager(feeManager).rateFor("p2p_platform", tokenSymbol[token])` and `rateFor("p2p", tokenSymbol[token])` (or a single combined feature — see note), writing `platformFeeBpsSnapshot` and `gasFeeBpsSnapshot`, and `feeBps = 0`. Apply the contract `MAX_PLATFORM_FEE_BPS`/`MAX_GAS_FEE_BPS` clamp and the MCGP-exempt flag from `rateFor`. **In-flight trades are unaffected by a later `setRate` because the rate is snapshotted at open.** If `feeManager == address(0)` (pre-cutover, not yet wired) the contract falls back to `_feeBpsForToken` into `platformFeeBpsSnapshot` and `gasFeeBpsSnapshot = 0` — so a trade opened in the window *before* the FeeManager is wired still snapshots the live legacy `feeBps` (closes I3's "release at 0%" data-loss risk).
4. **`_settleRelease` (line 396) + `adminResolve` (line 463) — use both snapshots.** Replace `_splitFee(amt, t.feeBps)` with a total computed from `t.platformFeeBpsSnapshot + t.gasFeeBpsSnapshot`. For **old trades written by V2** (which have `platformFeeBpsSnapshot == 0 && gasFeeBpsSnapshot == 0` because those bytes were zero pre-upgrade) the release path **falls back to `t.feeBps`** so a trade opened pre-upgrade releases at its original snapshot. Pseudocode:
   ```solidity
   uint16 total = t.platformFeeBpsSnapshot + t.gasFeeBpsSnapshot;
   if (total == 0 && t.feeBps != 0) total = t.feeBps; // back-compat for pre-V3 trades
   (uint256 toBuyer, uint256 fee) = _splitFee(amt, total);
   ```
   This is the migration note in lieu of an on-chain migration loop (no loop needed — the back-compat branch handles every pre-upgrade trade). **Required test:** `test_P2PV2toV3UpgradePreservesTradeSnapshot` — deploy V2, open a trade (`feeBps=50`), upgrade to V3, read the trade back and release it, assert it splits at 50 bps via the fallback (closes B7-test / M2 / I3).
5. **Split destination under D5:** P2P already sends the whole fee to `_systemRecipient()` (line 405) and does no on-chain cashback split, so P2P needs **no `legacySplitEnabled` branch for splitting** — it adds the bool only for symmetry/boot-guard parity. The off-chain v2 engine accrues the platform portion to TP from the `platformFeeBpsSnapshot` (the gas portion stays system revenue). The single `feeBps` → `platform + gas` separation is the substantive change.

> **Combined-feature note:** the two reads can be one `rateFor("p2p", sym)` returning the *gas* bps with the platform bps carried separately, OR two feature keys `"p2p"` (gas) + `"p2p_platform"` (platform) — matching the existing `FeatureP2P` (50 bps) and `FeatureP2PPlatform` (100 bps) seed rows in `DefaultFeeConfigs` (`fee_config_service.go:78-79`). **Use two keys** so each is independently configurable, consistent with the seed.

### 7.2 ProductEscrow → **ProductEscrowV2** (closes B3, I5)

Current facts: constants `PLATFORM_FEE_BPS=200`, `BUYER_CASHBACK_BPS=50`, `UPLINE_FEE_BPS=50` (lines 41–43), `GAS_FEE=100000` (line 47); `_releaseToSeller` (lines 348–389) splits `platformFee` into `buyerCashback` (line 354), `uplineAmount` (355), `systemAmount = order.platformFee - buyerCashback - uplineAmount` (356); `__gap` is `uint256[47]` (line 394). `_calculatePlatformFee` (line 337) uses `PLATFORM_FEE_BPS`.

**V2 changes:**

1. **Read the rate live at order creation / settlement, not from constants.** `_calculatePlatformFee` reads `IFeeManager(feeManager).rateFor("product_merchant", tokenSymbol[token])` (bps) and the gas flat from `rateFor("product", ...)` (`flatUSD`), honoring `mcgpExempt`. The `PLATFORM_FEE_BPS`/`GAS_FEE` constants are removed once `feeManager` is wired; if `feeManager == address(0)` the contract retains the constants as the fallback (so a pre-wire order behaves exactly like today).
2. **`legacySplitEnabled` gates the split (D5).** `_releaseToSeller`:
   ```solidity
   if (order.platformFee > 0) {
       if (legacySplitEnabled) {
           // EXISTING behavior — buyerCashback/uplineAmount/systemAmount split (lines 354-383 unchanged)
       } else {
           token.safeTransfer(owner(), order.platformFee); // 100% to system; off-chain engine splits
           emit OrderReleased(orderId, order.seller, sellerAmount, order.platformFee, 0, 0);
       }
   }
   token.safeTransfer(order.seller, sellerAmount);
   ```
   Deployed `legacySplitEnabled = true` → byte-for-byte today. Flipped `false` at cutover → 100% to system, off-chain engine becomes sole splitter. **Reversible** by flipping back (closes I5 + B6). The `BUYER_CASHBACK_BPS`/`UPLINE_FEE_BPS` constants stay (still referenced by the legacy branch); they are simply not used when the bool is false. `AUTO_REFUND_*` splits are untouched (not a platform-fee split).
3. **Storage / slot math:** add `feeManager` (address) + `legacySplitEnabled` (bool) — which **pack into one slot** (20B + 1B < 32B) — plus `tokenSymbol` mapping (+1) above the gap → 2 new slots → `__gap` shrinks `47 → 45`. Add `MAX_PLATFORM_FEE_BPS = 300` `constant` cap. PR includes the storage-layout slot-diff.

### 7.3 ServiceContact → **ServiceContactV2** (closes I2)

Current facts: `MIN_FEE_AMOUNT = 10000` (line 34); split constants `PROVIDER_BPS=2500`, `CALLER_BPS=1250`, `UPLINE_BPS=1250` (lines 38–40); `payContactFee` (line 75) does `require(fee >= MIN_FEE_AMOUNT, ...)` at **line 86** then splits (lines 94–107); `setFeeAmount` (line 114) accepts 0 (clears) — **but a 0 fee then bricks `payContactFee` at line 86**; `__gap` is `uint256[48]` (line 145).

**V2 changes — DO NOT zero `feeAmount` (the review's I2 correction):**

1. **`feeAmount` stays > 0** (read from FeeManager's `flatUSD` for `service_contact`, or kept as `feeAmounts[token]` synced — but never set to 0). Setting it to 0 makes `payContactFee` revert for every caller (line 86) — that **bricks the feature**, which is *not* the intent. The earlier draft's "zero the on-chain split via feeAmount=0" was wrong; this corrects it.
2. **`legacySplitEnabled` gates the split (D5)** — the split constants are `constant`, so the *upgrade replaces the split logic*, gated by the bool:
   ```solidity
   uint256 fee = feeAmounts[token];
   require(fee >= MIN_FEE_AMOUNT, "Fee not configured for token"); // UNCHANGED — fee stays > 0
   erc20.safeTransferFrom(msg.sender, address(this), fee);
   if (legacySplitEnabled) {
       // EXISTING 4-way split (lines 94-107 unchanged)
   } else {
       erc20.safeTransfer(owner(), fee); // 100% to system; off-chain engine does the provider/caller/upline split
   }
   emit ContactFeePaid(msg.sender, serviceProvider, token, fee, requesterUpline);
   ```
   Deployed `true` → today's behavior. Flipped `false` at cutover → whole fee to system; the off-chain engine performs the provider(25%)/caller(12.5%)/upline(12.5%) split (it already models the 10%-from-`ProviderID` provider cut). Reversible.
3. **Rate live-read:** if FeeManager governs the service-contact flat, `setFeeAmount` is driven from a confirmed `rateFor("service_contact", sym).flatUSD` — but the on-chain `feeAmounts[token]` remains the value `payContactFee` charges (the contract can't `eth_call` cross-contract for a *flat token amount* cheaply per call without decimals context, so for ServiceContact the flat is *synced* into `feeAmounts` on confirm, while the **split** is governed by the bool). This is the one feature where the rate is mirrored rather than live-read — documented because the flat is token-denominated, not bps. The mirror is still single-write (FeeManager) → confirm → `setFeeAmount` is part of the *same* plan only for service_contact; the drift risk is bounded because `feeAmount` is a flat the cache and chain both read back on confirm.
4. **Storage / slot math:** add `feeManager` (address) + `legacySplitEnabled` (bool) — which **pack into one slot** (20B + 1B < 32B) — plus `tokenSymbol` mapping (+1) above the gap → 2 new slots → `__gap` shrinks `48 → 46`. PR includes the storage-layout slot-diff.

---

## 8. Remaining collection (flag-gated)

- **Fiat `CreateWithdrawal` (closes B5/I7) — accrue in the SAME DB tx as the deduction.** Today `CreateWithdrawal` (`transaction_handler.go:303-432`) computes `platformFee` via `calculateWithdrawalFees` (line 331), creates `tx`, and deducts the balance (lines 398-410) but **never emits a FeeEvent**. Change: wrap the `tx` create + the asset-balance `Updates` + a `FeeEvent` write in **one `config.DB.Transaction(func(tx) ...)`** so the fee can never be deducted without the accrual being recorded (no treasury leak). The `FeeEvent` uses `{Feature: withdrawal, SourceType: "withdrawal", SourceID: tx.ID}` — a **distinct `SourceType` from the on-chain send path** so a withdrawal that also touches an on-chain settle path never double-settles. Emission is gated `if h.Config.IsFeeModelV2Enabled()`; with the flag OFF the legacy TP path is byte-for-byte (closes I-flag-isolation). Distribution uses the existing `EnqueueFeeSettled` (retry, `fee_v2_settle_failed_total` alert). The deduction-in-DB-tx is the leak fix; the enqueue is the (retryable) distribution.
- **AllSwap (closes I8) `POST /api/swap/lifi/settle{quoteId, from/to amounts+tokens, chainId}`** → backend **re-queries LiFi `/quote`** to recompute the fee USD, then:
  - **Rejects (422) if the client-supplied/quoted fee deviates by more than a configurable threshold (default 10%) from the re-queried fee** (anti-forgery — a client must never be able to shrink the fee routed to the v2 pool).
  - **If LiFi is unreachable**, do **not** charge 0 and do not trust the client: use a **conservative configured default (e.g. 3.5% vs the nominal 3%)** and emit a warning metric. Never block indefinitely; never accrue a client-supplied number.
  - Emits `FeeEvent{Feature: allswap, SourceType: "allswap", SourceID: quoteId}` with the **verified** fee (never the client value). Gated by the flag. Small app hook calls it post-broadcast.
- **P2P platform (closes B7/I-snapshot)** accrual reads the trade's **snapshotted** `platformFeeBpsSnapshot` (from V3, surfaced via `getTrade` / a new `platformFeeBpsSnapshot` accessor), not a global assumption, and emits `FeeEvent{Feature: p2p_platform, UserID: buyer, SourceType: "p2p_release", SourceID: tradeId}`; idempotent on `(SourceType, SourceID)`; the chain reconciler must not re-accrue. Until V3 is deployed, `accrueP2PReleaseFee` (`p2p_trade_service.go:314`) stays the documented no-op; the flag check is added now so real accrual fires the moment V3 lands (closes I-P2P-dependency).

---

## 9. Multi-chain & cutover

- FeeManager deployed per active chain (Sonic now; BSC at its launch). Escrow upgrades applied per chain.
- **Cutover order (reversibility now real — the `legacySplitEnabled` bool, not the flag alone, gates split behavior):**
  1. Deploy FeeManager per chain (owner = Safe). Deploy escrow implementation upgrades with **`legacySplitEnabled = true`** and `feeManager` **set** but split behavior unchanged → **zero behavior change even with the flag still OFF** (closes the review's "deploying V3 is irreversible" item — the splits are NOT zeroed at deploy; the bool defaults to today's behavior).
  2. Seed each FeeManager with the **current live rates** (so escrows live-reading it charge exactly today's rate). Verify via read-back.
  3. Run the v2 cutover-reset (existing) and flip `FEE_MODEL_V2_ENABLED=true`.
  4. **Boot guard (closes I1):** if `FEE_MODEL_V2_ENABLED && (escrow.legacySplitEnabled == true OR escrow.feeManager == address(0) OR escrow not upgraded)` → **fail-safe / LOUD alert at boot** to prevent double-pay (on-chain split AND off-chain split firing for the same fee). The flag must not be ON in production while any escrow still splits on-chain.
  5. In the cutover window: apply the PM target rates (via §5, FeeManager `setRate`) **and** flip each escrow `legacySplitEnabled = false` (owner tx). Order within the window: rates first (read-back verified), then the split flip.
  6. Post-cutover validation: each feature charges the PM rate; quote == charge; conservation/drift metrics clean; the boot guard passes.
- In-flight P2P trades release at their **snapshotted** rate (documented; acceptable). Document the exact block/timestamp cutover in the runbook.

---

## 10. Invariants & reconciliation

- **No-drift reconciler** (periodic job, runs **hourly**): for each chain compare on-chain `FeeManager.rateFor` vs the `fee_config` replica vs the resolved intent; emit `fee_rate_drift_total` on mismatch and **alert**. Because A1 removed the escrow rate copy, there is **no escrow-getter to reconcile** — the only pair to compare is FeeManager (truth) vs replica (cache). If they diverge, the reconciler **refreshes the replica from chain** (chain wins) and alerts; if the refresh itself fails (RPC down) it keeps the last-known-good replica and escalates. (Closes B1/I3 — there is no longer a drift *class*, only cache-staleness, which is self-healing toward chain.)
- **Money invariant** (existing) preserved: P2P active-trade `token_amount` Σ == on-chain `balanceOf` − ad-pool remaining.
- **Conservation** (existing v2): per-feature split sums to FeeUSD; with V3 the P2P split base is `platformFeeBpsSnapshot` (not the combined fee) — the gas portion is system revenue, not split. Test asserts conservation against the platform portion only.

---

## 11. Error handling & the no-$0/no-unbounded ladder (closes B2/B3, I4-fallback)

`FeeConfigService.RateFor(feature, token, chainID)` on the charging hot-path is **cache-only** and resolves in this strict order — it **never charges 0 and never charges an unbounded fee**:

1. **In-memory cache hit** (loaded from `fee_config` replica) → use it.
2. **Cache miss for the exact (feature, token, chain)** → fall back to the global (`chain = NULL`, `token = "*"`) replica row.
3. **Still missing (cold start before the replica loaded, or replica empty)** → fall back to **last-known-good** (a persisted snapshot of the last successful replica load, separate from the live cache) → if absent, the **per-feature configured seed default** (the existing `defaultFeeRows` in `fee_config_service.go:250`, which mirror production constants). Emit a **LOUD alert** (`fee_rate_cache_miss_total` + log at error).
4. **Clamp:** the resolved bps is always clamped to `[0, per-feature MAX]` (the FeeManager `MAX_BPS` mirrored as a backend constant) so a corrupt replica row can't produce an unbounded charge.

There is **no hot-path `eth_call`** (per D1) — the source of truth reaches the cache via confirmed `RateSet` (§5) or the hourly reconciler refresh (§10), never via a per-request live read that could block or fail the charge.

Other:
- Set-fee `plan` returns empty `txs` when intent already matches chain (idempotent).
- `confirm` rejects **422** if the receipt failed or on-chain `rateFor != intent` — intent stays `pending` for that chain, **replica untouched**.
- All admin endpoints super-admin gated + **409** while the flag is OFF.
- **Required test (closes B2/B3 + I4):** a **cold-start / RPC-down test** that calls `RateFor` with an empty cache and no RPC and asserts the returned fee is the seed default (non-zero, bounded) and a loud alert fired — **never 0, never unbounded**.

---

## 12. Testing

- **Contract — FeeManager:** set/get, `MAX_BPS` cap (revert on `bps > MAX_BPS`), `onlyOwner` access control, `setRates` batch, `RateSet` event payload, UUPS upgrade-safety (storage layout), timelock delay enforced.
- **Contract — escrows:**
  - `test_P2PV2toV3UpgradePreservesTradeSnapshot` (deploy V2, open trade, upgrade, release at the old snapshot via the back-compat branch).
  - P2P live-read snapshot: open after `setRate` change → snapshot reflects new rate; in-flight trade unaffected by a *subsequent* `setRate` (closes I3 / M1-style).
  - P2P open in the pre-wire window (`feeManager == 0`) → snapshots legacy `feeBps`, not 0.
  - `legacySplitEnabled` branch for Product + Service: `true` reproduces today's split exactly; `false` sends 100% to system; flipping back restores. ServiceContact: assert `feeAmount` stays > 0 and `payContactFee` does **not** revert when `legacySplitEnabled = false` (closes I2).
  - **Storage-layout slot-diff** (`hardhat-storage-layout` / `forge inspect storage-layout`) captured in the PR for all three upgrades, asserting the exact `__gap` shrink (P2P 50→48, Product 47→45, Service 48→46 — `feeManager` address + `legacySplitEnabled` bool pack into one slot, `tokenSymbol` takes one → 2 new slots each) and the Trade-struct append packing into slot D (closes M2, B7-tooling).
  - Escrow `MAX_*` caps revert a misconfigured snapshot.
- **Backend:** read-replica cache + per-chain default/override resolution; **`cart quote == charge`** regression (closes B5/I8); the set-fee tx builder produces correct calldata per chain (encode → broadcast on a fork → read-back → reconciler in sync); `confirm` verify (happy + 422 reject leaving replica untouched); drift reconciler refreshes replica from chain and alerts; **cold-start/RPC-down → seed default, non-zero, alert** (closes B2/B3/I4).
- **Collection:** withdrawal accrues in the same DB tx (no leak) + idempotent + flag-gated; allswap rejects a forged client fee (>10% deviation) and uses a conservative default when LiFi is down, accruing the verified fee; P2P-1% reads the snapshot, idempotent, reconciler doesn't re-accrue.
- **Flag isolation:** with the flag OFF, no new charge/split/event fires; legacy paths byte-for-byte; boot guard fires if the flag is ON while any escrow `legacySplitEnabled == true`.
- **OTC (closes M1):** an acceptance test that flips `FeatureOTC` 50 → 200 bps via the admin API and confirms no other feature's rate changes and the OTC charging path (when it exists) reads the new rate (rate-flip-only, no code change since the desk isn't live).

---

## 13. Delivery phases (each → its own implementation plan)

- **Phase 1 — FeeManager contract → testnet/audit FIRST + backend (parallel):**
  - The FeeManager contract itself (new, low-risk, no migration — lighter to audit than the escrow upgrades) goes to **testnet first and into audit FIRST**.
  - In parallel (against the testnet FeeManager): the read-replica plumbing (`RateFor` gets `chainID` + the §11 fallback ladder), `fee_rate_intent` + resolution + `fee_config.chain_id`, the admin set-fee plan/confirm flow + state machine, the cart-quote fix, and the remaining collection (withdrawal in-DB-tx accrual, allswap settle, P2P-1% flag-gated stub). All flag-gated.
  - **Mainnet gate:** Phase 1 ships to mainnet only after the FeeManager is audited + deployed per chain (closes the review's "Phase 1 buildability" item — testnet unblocks backend dev; mainnet waits on audit).
- **Phase 2 — Escrow live-read upgrades (re-audit):** P2PEscrowV3 + ProductEscrowV2 + ServiceContactV2 UUPS upgrades (`feeManager` + `setFeeManager`, `legacySplitEnabled` + `setLegacySplitEnabled`, `tokenSymbol`, live-read of `rateFor`, V3 dual snapshot, `MAX_*` caps). **Re-audit required.** Per-chain deploy/upgrade scripts. Depends on the FeeManager from Phase 1 being deployed (escrows reference its address).
- **Phase 3 — Cutover:** the §9 sequence + runbook + boot guard + post-cutover validation, per chain.

Phase 1 backend can proceed against testnet in parallel with Phase 2 contract work; Phase 2 cannot deploy to mainnet until Phase 1's FeeManager is audited + live; Phase 3 depends on both being live on the target chain.

---

## 14. Risks / open items

- **Re-audit cost** for three escrow live-read upgrades — the largest schedule risk; Phase 2 gated on it. The A1 design keeps each upgrade *smaller* (no per-escrow rate setter, just a live-read + a bool branch) which shrinks the audit surface vs the old A2 mirror-setter design.
- **Reversibility is precise, not absolute.** Two distinct things: (1) *deploying* the V2/V3 implementation is a normal UUPS upgrade — rolling the **code** back requires another `_authorizeUpgrade` upgrade (Safe-gated, and ideally re-audited), so the deploy step itself is not a one-click rollback; (2) the `legacySplitEnabled` **bool** reversibly gates split *behavior* for settlements that occur **after** the flip (it does not retroactively change fees already settled). The cutover is safe to stage because step 1 deploys with `legacySplitEnabled=true` (byte-for-byte today's behavior) — so the irreversible part (the code upgrade) changes nothing observable until the reversible bool is flipped in the cutover window. Past settlements are immutable either way.
- **Gnosis Safe multisig provisioning (D7, closes B8).** Owner of the FeeManager **and** all three escrows is one Safe (e.g. 3-of-5 finance/founder signers). **Provisioning:** deploy the Safe, transfer each contract's `owner()` to it (and wrap behind the 6h `TimelockController` for `setRate`/`setRates`/`_authorizeUpgrade`). **Signer recovery:** Safe owners can rotate a compromised signer via the Safe's own `swapOwner`/`changeThreshold` (itself a Safe tx) — document the runbook; keep a cold-storage recovery signer. Reusing this Safe also closes the PR #34 deferred "distributor owner → multisig" item.
- **Multisig unavailability / outage SLA (closes I9).** Document: an urgent fee change (e.g. withdrawal fee must rise during a gas spike) requires Safe threshold signatures. **SLA:** name an on-call signer set with a target signing window (e.g. 2 of the threshold reachable within 1h during incidents). **Escalation:** if the threshold can't be reached, the *backend cannot* emergency-set a rate (no backend key — by design, preserving the no-custody invariant); the documented fallback is to **pause the affected escrow** (an `onlyOwner` action, still Safe-gated) — i.e. there is **no non-Safe emergency rate path**; this is an accepted trade-off of the no-custody model and must be staffed accordingly. State this explicitly so ops plans around it.
- **In-flight P2P trades** release at the old snapshot post-bump — documented, accepted; runbook records the cutover block.
- **OTC taker fee** deferred (D8) — the rate row stays admin-configurable; flipping it is a Phase-3-or-later admin `setRate` only (no backend code change, since the desk isn't a live Go flow). M1 acceptance test guards the flip.
- **Cart-quote migration** must land before the flag flips in production (else quote≠charge) — captured in Phase 1, with the quote==charge regression test as the gate.
- **ServiceContact flat is mirrored, not live-read** (§7.3) because the fee is a token-denominated flat, not bps — the one documented exception to A1; drift is bounded by the confirm read-back.

---

## Changelog — review findings → closure

**Blocking**

- **B1 (two-tx drift, no atomic coupling):** Closed by **A1 live-read (D4)**. Escrows read the rate from the FeeManager directly; there is no escrow rate setter and thus no second tx to drift. The only authoritative write is `FeeManager.setRate`. §2/§3.1/§5/§7/§10 rewritten; the reconciler now only compares FeeManager↔replica (no escrow-getter), and that is self-healing toward chain.
- **B2 (circular source of truth):** Closed by **D1 CQRS model** — FeeManager is the write side / source of truth; `fee_config` is a read replica refreshed only on **confirmed** `RateSet`; the hot-path reads the replica, never a live `eth_call`. §2/§4/§11. Cold-start/RPC-down test required (§11/§12).
- **B3 ($0-fee fallback / backend-fee scope):** Closed by the **§11 fallback ladder** (cache → last-known-good → seed default → clamp, never 0/unbounded, loud alert) and by keeping backend-charged rates on the FeeManager *as config/transparency with no on-chain enforcer* (D2/I4). Cold-start test mandated.
- **B5 (collection not wired — withdrawal/cart):** Closed in §6/§8 — `CreateWithdrawal` emits `FeeEvent` **in the same DB tx** as the deduction (distinct `SourceType`), and `CalculateCartSummary` reads `FeeFlatUSD(FeatureProduct, token)` replacing the `cart.go:274` `GasFeeUSD` constant; quote==charge regression test.
- **B6 (escrow setters):** Closed by **D5 `legacySplitEnabled` bool** — escrows have no admin-called rate setter (they live-read); the *split* is gated by an owner-settable bool (deployed `true` = today; `false` = 100%-to-system + off-chain split), making the upgrade reversible and decoupled from timing (also closes I1/I5). Exact storage + settle-path branch specified per escrow in §7.1–§7.3.
- **B7 (P2P V3 storage):** Closed in §7.1 — exact V3 layout: `feeManager`/`legacySplitEnabled`/`tokenSymbol` appended before `__gap` (50→48; address+bool pack into one slot, mapping takes one → 2 new slots), `Trade` gains `platformFeeBpsSnapshot`+`gasFeeBpsSnapshot` packing into existing slot-D free bytes, `feeBps` kept for old-trade back-compat, `_settleRelease` (line 400) + `adminResolve` (line 463) rewritten to sum both snapshots with a `feeBps` fallback. Storage-layout slot-diff + upgrade-preserves-snapshot test mandated.
- **B8 (multisig/caps/timelock):** Closed by D7/§3/§14 — owner = Gnosis Safe (provisioning + signer-recovery), `MAX_BPS` enforced in-contract (`require(bps <= MAX_BPS)`), 6h `TimelockController` with justification, and escrow `MAX_PLATFORM_FEE_BPS`/`MAX_GAS_FEE_BPS` caps so a misconfig can't strand funds.

**Important**

- **I1 (boot guard):** §9 step 4 — boot fails-safe/alerts if `FEE_MODEL_V2_ENABLED` && any escrow `legacySplitEnabled==true` (or not upgraded / `feeManager==0`).
- **I2 (ServiceContact correction):** §7.3 — `feeAmount` stays > 0 (zeroing it bricks `payContactFee` at line 86); the split is zeroed via the `legacySplitEnabled` branch, not by zeroing the fee. All "feeAmount=0" text corrected.
- **I3 (snapshot mismatch / release-at-0%):** Closed by A1 snapshot-at-open + the pre-wire `feeManager==0` fallback to legacy `feeBps` (§7.1) so a trade opened before the FeeManager is wired never snapshots 0.
- **I4 (backend-fee rates):** §6/§11/D2 — kept on the FeeManager for config+transparency, explicitly **no enforcer**, read from the cache; §11 ladder prevents $0.
- **I5 (cutover reversibility):** §9 — escrow upgrades deploy with `legacySplitEnabled=true` (zero behavior change), splits flipped off only in the cutover window and reversible by flipping back.
- **I6 (plan/confirm schemas + state machine):** §5 — concrete plan/confirm JSON, encoded-calldata example, and the `draft→pending→partially_applied→applied` (+`cancelled`) intent state machine; confirm verifies `status==1` + read-back, else 422 + replica untouched.
- **I7 (withdrawal idempotency/leak):** §8 — accrual in the same DB tx as the deduction; distinct `SourceType`; flag-gated; `EnqueueFeeSettled` retry.
- **I8 (allswap + cart):** §6/§8 — allswap re-queries LiFi, rejects >10% deviation, conservative default when LiFi down, accrues the verified (never client) fee; cart enumerated change + quote==charge test.
- **I9 (multisig outage):** §14 — SLA + escalation documented; explicitly states there is **no** non-Safe emergency rate path (pause is the fallback), an accepted trade-off of no-custody.
- **I10 (phases):** §13 — Phase 1 FeeManager → testnet/audit FIRST, backend in parallel, mainnet gated on audit; escrow live-read upgrades are Phase 2 (re-audit).

**Minor**

- **M1 (OTC rate-flip test):** §12 — acceptance test flipping `FeatureOTC` 50→200 bps via admin API, no collateral changes.
- **M2 (storage-layout tooling):** §7/§12 — `hardhat-storage-layout`/`forge inspect storage-layout` slot-diff mandated in the PR for all three upgrades with the exact `__gap` shrink asserted.
- **M3 (example /plan response):** §5 — concrete `/plan` response with `to`/`data` encoded calldata + `decoded` advisory.
