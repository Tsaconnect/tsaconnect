# Configurable FeeManager — Design Spec

**Date:** 2026-06-08
**Status:** Draft for review
**Context:** Extends the fee/TP overhaul (`feat/fee-tp-overhaul`, PR #34). The v2 distribution engine, per-feature split table, and the `FEE_MODEL_V2_ENABLED` flag already exist and are dormant. This spec adds **admin-configurable fees with an on-chain source of truth**.

---

## 0. Decisions locked (from brainstorm, 2026-06-08)

| # | Decision | Choice |
|---|----------|--------|
| D1 | Source of truth | **On-chain FeeManager** (per chain) is authoritative for what's charged. Changes are infrequent, signed by a finance/founder multisig. |
| D2 | Registry scope | The FeeManager holds **all fee RATES** (on-chain-collected *and* backend-charged). The distribution **split percentages stay off-chain** in the v2 engine. |
| D3 | Granularity | **Global default + per-chain override** per `(feature, token)`. The default/override resolution happens off-chain at authoring time; each chain's FeeManager stores that chain's *resolved effective* rate. |
| D4 | Enforcement model | **A2** — FeeManager is the canonical store; the admin "set-fee" flow also writes each affected escrow's setter so the on-chain enforcer mirrors the FeeManager. Backend reads all rates from the FeeManager (cached), including the product cart quote. No live cross-contract reads. |
| D5 | On-chain splits | **Zeroed at cutover.** Product/Service escrows stop splitting cashback/upline/provider on-chain; the off-chain v2 engine becomes the *sole* splitter (rewards in MCGP). |
| D6 | Remaining collection | Fiat `CreateWithdrawal` earns TP (70/30); AllSwap via a server-verified settle endpoint; P2P 1% platform fee accrued off-chain. |
| D7 | Out of scope | OTC fee (desk is spread-only — deferred); net-new features Spot-aggregator / Instant-pay-crypto / Instant-transfer-fiat (don't exist; engine rows already seeded). |

---

## 1. Goals / non-goals

**Goals**
- Every fee rate (P2P platform+gas, Product merchant+gas, Service, swap, withdrawal, allswap, OTC, the net-new features' rates) is admin-configurable from a single authoritative place, without a code deploy.
- The on-chain rate is the source of truth for what is charged; the backend never charges or quotes a rate that diverges from chain.
- Per-chain differentiation (Sonic vs BSC vs future) with a manageable default+override model.
- Preserve the signed-but-not-broadcast invariant — the backend builds unsigned txs; admins sign; the backend confirms. No backend key custody.
- Fully flag-gated: with `FEE_MODEL_V2_ENABLED` OFF, live charging/splitting is byte-for-byte unchanged.

**Non-goals**
- Moving the split/distribution math on-chain (stays off-chain).
- Building the net-new trading features (D7).
- Changing the reward payout (MCGP merkle distributor) — unchanged.

---

## 2. Architecture — desired-state / actual-state

```
Admin (finance/founder multisig)
   │  authors "rate intent": default rate + per-chain overrides per (feature, token)
   ▼
[ fee_rate_intent ]  (off-chain, DESIRED state) ── backend resolves per-chain effective rate
   │                                                  │ diffs vs on-chain; builds UNSIGNED txs
   │  read-through cache (fee_config table)           ▼
   ▼                                       [ FeeManager ] (per chain, ACTUAL state = SOURCE OF TRUTH)
backend reads effective rate ◄── eth_call ── + escrow setters synced in the same flow
   │                                              (P2P.setFeeBps / Service.setFeeAmount / Product.setRate)
   ▼
cart quote · backend-fee charge · off-chain split base   (all read the same effective rate)
   ▼
[ v2 engine ] splits the collected fee off-chain → cashback/TP in MCGP (sole splitter after D5)
```

- **Desired-state** = the admin's intent (default + overrides). Authoring + diff/UX only.
- **Actual-state** = each chain's FeeManager. What is actually charged. The backend reads this for every fee decision.
- The admin flow is **plan/apply** (Terraform-style): compute the diff between intent and on-chain, emit the txs to align, confirm.

---

## 3. The FeeManager contract (new, one per chain, UUPS)

```solidity
struct Rate { uint16 bps; uint64 flatUSD; bool mcgpExempt; bool active; }   // flatUSD in 1e6 units
mapping(bytes32 => Rate) private rates;   // key = keccak256(abi.encode(feature, tokenSymbol))

function rateFor(string feature, string token) external view returns (Rate memory);
function setRate(string feature, string token, Rate r) external onlyOwner;     // MAX_BPS cap
function setRates(Feature[] calldata batch) external onlyOwner;                // gas-batched
event RateSet(bytes32 indexed key, string feature, string token, uint16 bps, uint64 flatUSD, bool mcgpExempt, bool active);
```

- `onlyOwner` = the finance multisig (also the upgrade authority). `MAX_BPS` cap (e.g. 1000 = 10%) guards fat-finger.
- Holds **this chain's resolved effective rates** — per-chain differentiation is realized by each chain's contract holding different values.
- MCGP-exemption becomes a configurable flag here (today baked per-contract).
- Events drive the off-chain reconciler.

**Escrows do not read it live** (A2). They keep their own settable param, written from the FeeManager by the admin flow (§5). The FeeManager is consulted on-chain only if a future escrow opts into live reads.

---

## 4. Off-chain: rate intent, resolution, cache

- **`fee_rate_intent`** (new table): `(feature, token, chainId NULLABLE) → {bps, flatUSD, mcgpExempt, active}`. `chainId = NULL` is the global default; a row with a `chainId` is that chain's override.
- **Resolution:** effective rate for `(feature, token, chain)` = chain-specific override ?? global default. Mirrors today's token-specific → wildcard resolution, extended with the chain dimension.
- **`fee_config` table repurposed** as the **read-through cache** of on-chain `rateFor` values (per chain), invalidated on set-fee confirmation. `FeeConfigService.RateFor(feature, token, chain)` reads cache → falls back to an `eth_call` against that chain's FeeManager → caches.
- The off-chain intent is **not** authoritative for charging; it is the desired-state used to generate txs and to detect drift.

---

## 5. Admin "set-fee" flow (signed-but-not-broadcast)

Reuses `UnsignedTx{To, Value, Data, ChainID}` (the merkle-publisher pattern).

1. `POST /api/admin/fees/intent` `{feature, token, default:{...}, overrides:{chainId→{...}}}` — super-admin. Persists to `fee_rate_intent`. Rejected with 409 while `FEE_MODEL_V2_ENABLED` is OFF (matches the existing fee-config gate).
2. `POST /api/admin/fees/plan` → backend resolves per-chain effective rates, reads each chain's on-chain `rateFor`, and returns the **diff + the set of unsigned txs** to align: `FeeManager.setRate(...)` per affected chain, **plus** the affected escrow's setter (`P2P.setFeeBps`, `Service.setFeeAmount`, `Product.setRate`) so the enforcer matches.
3. Admin signs + broadcasts each tx.
4. `POST /api/admin/fees/confirm` `{txHash, chainId}` → backend verifies the receipt (status==1) **and** reads back `rateFor`/the escrow getter to confirm the on-chain value equals the intent before marking applied + invalidating the cache. (Mirrors the hardened `ConfirmPublish` verify from PR #34.)

---

## 6. Backend integration

- `FeeConfigService` → read-through from the on-chain FeeManager (§4), per chain, cached.
- **Cart quote fix:** `CalculateCartSummary` (today reads `cart.go` constants) reads the effective rate from `FeeConfigService`, closing the quote≠charge divergence. The hardcoded constants are removed once this lands.
- The v2 engine's `FeeUSD` (split base) and all backend-charged fees (swap, withdrawal) read the same effective rate, so charge == split-base == quote.

---

## 7. Escrow changes (UUPS upgrades — state preserved)

- **P2PEscrowV2 → V3:** replace the single `feeBps` with `platformFeeBps` + `gasFeeBps`, each settable (synced from FeeManager). Release sends both to `systemWallet`; the off-chain engine accrues the 1% platform → TP. Per-trade snapshot of both retained (rate locks at open; in-flight trades unaffected by later changes).
- **ProductEscrow:** add setters (the hardcoded `PLATFORM_FEE_BPS`/`BUYER_CASHBACK_BPS`/`UPLINE_FEE_BPS`/`GAS_FEE` constants become settable storage, synced from FeeManager) **and zero the on-chain cashback/upline split** — the fee goes to `systemWallet`; the off-chain engine is sole splitter.
- **ServiceContact:** keep `setFeeAmount`; **zero the hardcoded provider/caller/upline split bps** — fee to `systemWallet`; the off-chain engine splits (incl. the 10% provider cut it already models from `ProviderID`).
- All three are already UUPS-upgradeable; these are implementation upgrades preserving proxy address + state. Each requires re-audit before mainnet.

---

## 8. Remaining collection (flag-gated)

- **Fiat `CreateWithdrawal`** emits `FeeEvent{Feature: withdrawal, SourceType:"withdrawal", SourceID: walletTx.ID}` after the 0.5% deduction — distinct idempotency key from the on-chain send path so a withdrawal flowing through both never double-settles.
- **AllSwap** `POST /api/swap/lifi/settle{quoteId, from/to amounts+tokens}` → backend re-queries LiFi `/quote` to verify the 3% fee USD (anti-forgery; never trust a client-supplied fee), then emits `FeeEvent{Feature: allswap, SourceType:"allswap", SourceID: quoteId}`. Small app hook to call it post-broadcast.
- **P2P 1% platform** accrual reads the trade's snapshotted `platformFeeBps` (not a global assumption) and emits `FeeEvent{Feature: p2p_platform, UserID: buyer, SourceType:"p2p_release", SourceID: tradeId}`; idempotent; the chain reconciler must not re-accrue.

---

## 9. Multi-chain & cutover

- FeeManager deployed per active chain (Sonic now; BSC at its launch). Escrow upgrades applied per chain.
- **Cutover order (each step reversible until the flag flips):**
  1. Deploy FeeManager per chain; deploy the escrow implementation upgrades (flag still OFF, splits still on-chain → zero behavior change).
  2. Seed the FeeManager + escrow setters with the *current* live rates (so nothing changes yet).
  3. Run the v2 cutover-reset (existing) and flip `FEE_MODEL_V2_ENABLED=true`.
  4. In one window: apply the PM target rates (via §5) **and** the escrow upgrades that zero on-chain splits + separate P2P platform/gas.
  5. Post-cutover validation: each feature charges the PM rate; quote==charge; conservation/drift metrics clean.
- In-flight P2P trades release at their snapshotted rate (documented; acceptable).

---

## 10. Invariants & reconciliation

- **No-drift reconciler** (periodic job): for each chain, compare on-chain `rateFor` / escrow getters vs the cache and the resolved intent; emit `fee_rate_drift_total` on mismatch and alert. Prevents the cache or an un-synced escrow from charging a stale rate.
- **Money invariant** (existing) preserved: P2P active-trade `token_amount` Σ == on-chain `balanceOf` − ad-pool remaining.
- **Conservation** (existing v2): per-feature split sums to FeeUSD; unchanged by this work.

---

## 11. Error handling

- Set-fee `plan` returns an empty tx set when intent already matches chain (idempotent).
- `confirm` rejects (422) if the receipt failed or the on-chain value ≠ intent — intent stays "pending", cache untouched.
- Cache miss / `eth_call` failure on read → fall back to last-known-good cache + log; never charge $0 or an unbounded fee (clamp to a sane configured default + alert).
- All admin endpoints super-admin gated + 409 while the flag is OFF.

---

## 12. Testing

- **Contract:** FeeManager set/get/cap/access-control + UUPS upgrade-safety (storage layout); each escrow upgrade preserves state and exhibits zeroed-split + setter behavior; P2P V3 platform/gas separation + snapshot.
- **Backend:** read-through cache + per-chain default/override resolution; `cart quote == charge`; the set-fee tx builder produces correct calldata per chain; `confirm` verify (happy + reject); drift reconciler fires on mismatch.
- **Collection:** withdrawal/allswap/P2P-1% emit the right FeeEvent once, idempotent, flag-gated; allswap rejects a forged client fee.
- **Flag isolation:** with the flag OFF, no new charge/split/event fires; legacy paths byte-for-byte.

---

## 13. Delivery phases (each → its own implementation plan)

- **Phase 1 — FeeManager contract + backend + collection:** the FeeManager contract itself (new, low-risk, no migration — lighter to audit than the escrow upgrades), its read-through plumbing, `fee_rate_intent` + resolution + cache, the admin set-fee plan/confirm flow, cart-quote fix, and the remaining collection (withdrawal TP, allswap settle, P2P 1% accrual). All flag-gated.
- **Phase 2 — Escrow upgrades:** P2P V3 + Product + Service UUPS upgrades (zero on-chain splits, settable rates synced from the FeeManager); re-audit; per-chain deploy/upgrade scripts.
- **Phase 3 — Cutover:** the §9 sequence + runbook + post-cutover validation.

(Phase 1 can start immediately; Phase 2 depends on the FeeManager contract from Phase 1 being designed but can otherwise proceed in parallel; Phase 3 depends on both.)

---

## 14. Risks / open items

- **Re-audit cost** for three escrow upgrades — the largest schedule risk; Phase 2 gated on it.
- **Multisig owner** for FeeManager + upgrade authority must be provisioned (also closes the PR #34 deferred "distributor owner → multisig" item if reused).
- **In-flight P2P trades** release at the old rate post-bump — documented, accepted.
- **OTC 2%** deferred (D7) — revisit if the PM confirms a real taker fee; the engine row stays ready.
- **Cart-quote migration** must land before the flag flips in production (else quote≠charge) — captured in Phase 1.
