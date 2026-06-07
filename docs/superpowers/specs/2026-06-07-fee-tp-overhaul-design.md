# Fee Structure + Trade Points (TP) Overhaul — Design (v2)

**Status:** Draft for review (v2 — incorporates independent-review fixes + payout-rail decision) · **Date:** 2026-06-07 · **Branch:** TBD (`feat/fee-tp-overhaul`)

**Origin:** PM spec (fee table + 50/20/30 split + TP reward ladder paid in MCGP + global pool + TP screen) and the handwritten TP-levels image.

---

## 0. Decisions locked (do not re-litigate)

| Decision | Choice | Consequence |
|---|---|---|
| TP accounting | **Off-chain ledger** | The full TP engine (who is owed what) is computed off-chain, reusing today's engine. |
| MCGP payout rail | **Claim / merkle-distributor contract** | Backend computes claimable MCGP off-chain; an **admin** publishes a merkle root (admin-signed, low-frequency); **users claim** with an app-signed tx; treasury funds the distributor with MCGP. **No always-on backend private key** — preserves the "app signs, backend holds no keys" invariant. One *small, standard, auditable* contract (NOT a heavy on-chain reward engine). |
| Level rewards | **Cumulative — every level crossed, summed** | Each level pays its reward exactly once, ever; jumping L1→L4 pays L2+L3+L4. |
| Migration | **Reset balances to zero at launch** | Archive then zero `tp_balance`/`cashback_balance`; fresh start, no grandfathering. |
| Rollout | **Build the whole spec before shipping** | All sub-projects land before release (flag-gated until cutover). |

**Framing:** A TP engine **already exists** (`tsa-api-go/internal/handlers/tp_handler.go` `DistributeTPEarnings`, `GenerationPercentages` gen0 35.4% → gen10 0.4% — exactly the PM image; `TPEarning` ledger; `referred_by` graph; app screens). This is a **redefinition** of that engine, not a from-scratch build.

---

## 1. Goal

Every platform fee splits **50% system / 10% buyer / 10% direct upline / 30% Trade-Points pool**. The 30% TP pool is distributed across the transacting user + 1st–10th-generation uplines (existing `GenerationPercentages`); any generation with no live ancestor routes its share to a **global pool** shared by all users via a cumulative-index settlement. Users accrue **TP**; as cumulative TP crosses each of **15 levels** they unlock a **reward claimable in MCGP** from a distributor contract the treasury funds. A first-class **TP screen** shows balance, level progress, per-generation earnings, global-pool share, and claim history.

---

## 2. Fee model (uniform 50 / 20 / 30)

Each fee `F` (collected to the system wallet in its tx currency, as today) is *accounted off-chain* as: **50% system · 10% buyer · 10% direct-upline (gen-1) · 30% TP pool**.

**Per-feature rates** (produce `F`); MCGP-paid txns stay fee-exempt where they are today:

| Feature | Fee | Notes |
|---|---|---|
| Product (buyer) | 0.5% stable / 0% MCGP | |
| Product merchant (platform) | 2% | merchant-side |
| Service — contact reveal | $0.10 | |
| Service — other (fixed) | 0.5% | |
| OTC | 0.5% | |
| Allswap (LiFi) | 3% | active; confirm integrator-fee mechanics |
| P2P & PM (buyer) | 0.5% | on-chain fee → systemWallet; TP accounted off-chain at release |
| P2P & PM (platform/merchant) | 1% | |
| On-chain withdrawal (crypto) | 0.5% | |
| Spot · Instant-pay-crypto · Instant-transfer-fiat | 0.5% / 0.5% / 2% | ⚠️ **features don't exist — OUT OF SCOPE; fee config ready for when built** |

**SP1 — unified fee-rate config (rates ONLY).** Collapse the 5+ hardcoded rate sites (`cart.go`, `transaction_handler.go`, `service_contact_service.go`, on-chain P2P) into one `fee_config` table + service (feature × token, MCGP-exempt flag) + admin API. The **50/20/30 accounting that writes TP/cashback is SP3's job, not SP1** (avoid the hidden coupling).

---

## 3. TP accrual (off-chain) — SP3

On each fee-settled event: `tpPoolUSD = F_usd × 0.30`, distributed:
- gen0 (transacting user) … gen10 via existing `GenerationPercentages`.
- **Inactive/missing ancestor → route that generation's share to the global pool and CONTINUE climbing.** (Behavior change: today `DistributeTPEarnings` `break`s on the first non-active ancestor — `tp_handler.go:54-56` — stopping the walk. SP3 must fix the loop to skip-and-continue.)
- Each recipient: a `TPEarning` row + atomic `tp_balance` increment.

**Hook ALL fee-settled paths, not just user-confirmed ones.** Today only checkout/swap/service-contact/wallet-send call the engine, synchronously, and **P2P does not call it at all**. P2P fees also settle on the **auto-release** and **admin-resolve** paths (no user confirm) via the chain reconciler (`p2p_trade_service.go:2225+`), which currently does NO TP accounting. Centralize accrual in one `onFeeSettled(...)` called by: checkout confirm, service, swap, withdrawal, **and** every P2P release transition (`ConfirmRelease` + reconciler auto-release + `adminResolve`). Run it **async / off the request hot path** (outbox/worker) — it now fires on every fee event platform-wide.

**TP minting rate — ⚠️ CONFIRM (PM):** assume `1 TP = $1 of TP-pool value received`. Note: because both level threshold (`10^(n-1)` TP) and reward (`threshold/100`) are in TP, the reward is always ~1% of accrued TP-value **regardless of this constant** — so the constant sets **level PACING** (how much volume reaches each level), not the reward ratio. With this rate + 0.5% fee, L1≈$1.9k volume, L5≈$19M, L6+ effectively aspirational (matches the image's intent).

**Reset/migration:** archive **`TPEarning` AND `CashbackEarning`** + balances to `*_legacy` tables (audit), then zero `tp_balance`/`cashback_balance`. Cutover keyed on **settlement timestamp** so trades opened pre-cutover but settling post-cutover aren't double-counted; preserve the existing idempotency guard (`order.TPDistributedAt`, `checkout_handler.go:1220`) under the new engine.

---

## 4. Global pool — cumulative-index lazy settlement (SP4)

Literal "credit every user per transaction" is O(users)/txn — infeasible. A naive "credit everyone daily" is still O(users)/epoch. Use a **cumulative-index (MasterChef `accRewardPerShare`-style) accumulator**:
- One global scalar `globalTpIndex`, incremented per settlement epoch by `epochPoolDelta / registeredUserCount_at_epoch` — **one row write per epoch**, independent of user count.
- Each user stores `last_settled_index`. Owed global TP = `globalTpIndex − user.last_settled_index`, computed **lazily** on read/claim — **zero per-user writes per epoch**.
- A `global_tp_epoch` ledger records each epoch's delta + denominator for audit.
- **⚠️ CONFIRM (PM):** equal split among all registered users; define cadence (e.g. daily) + denominator ("registered" vs "active" users).

---

## 5. MCGP payout via claim/distributor contract (SP5 engine + SP6 contract)

**Off-chain (SP5):** compute each user's **cumulative claimable MCGP** = Σ(newly-crossed level rewards, cumulative) + cashback (10% buyer + 10% upline) + settled global-pool share. Convert USD→MCGP at the **OTC buy price locked at ACCRUAL time** (`otc_service.GetBuyPrice`), storing the MCGP amount (so payout-time MCGP volatility can't change what was earned). Idempotent: each level pays once (keyed `(user, level)`); cashback keyed `(user, txn)`.

**On-chain (SP6) — `MCGPRewardDistributor` (standard cumulative MerkleDistributor):**
- Treasury funds the contract with MCGP.
- An **admin** periodically publishes a **merkle root** of `(user, cumulativeClaimableMCGP)` — an **admin-signed, low-frequency** tx (no always-on backend key).
- Users **claim** `cumulative − alreadyClaimed` with an **app-signed** tx + merkle proof (replay-safe by construction).
- Reconciler compares published roots / on-chain claims against the off-chain ledger; treasury-MCGP-insufficiency → alert + queue, never partial-silent. Mirrors `p2p_reconciler_drift_total`.

**Cashback is now MCGP (behavior change — ⚠️ CONFIRM):** today cashback is recorded as USD and the model comment says "paid on-chain in USDC/USDT instantly" (`cashback_earning.go:10-12`). The new model pays the 10%+10% in MCGP via the same distributor. Confirm this is intended (vs keeping cashback in stable).

**Physical prizes (L6+):** record a `prize_due` row + notify admin; **manual/operational fulfilment** (not auto-paid). ⚠️ CONFIRM.

---

## 6. Anti-sybil / KYC-gate-before-payout — SP7 (NEW, required)

TP now pays **real MCGP up the referral chain**, but referrals are assigned at signup with `code = username`, **no KYC gate**, accounts default `active` immediately, and there's **no cycle / multi-account detection** (`auth_handler.go:126-168`). This is a direct fraud vector (fake downlines + self-trades minting TP up your own chain). Required:
- **KYC gate before claim/payout** — accrual can proceed, but MCGP claims require a verified account (and exclude self-referral cycles).
- Anti-sybil heuristics (multi-account/cycle detection, velocity caps) + an admin review/flag path.
- This gates the merkle-root inclusion in SP6.

---

## 7. App — TP screen (SP8)

Replace the hardcoded text explainer in `TradeAndEarnScreen.tsx`: hero (TP balance, reward earned $/MCGP, level + progress); the **15-level ladder** with Earned/In-Progress (image); **per-generation breakdown** (new endpoint); **global-pool share**; **TP earning history** + **claim history**; a **Claim** action (app-signed claim tx via the distributor). **Level-up notifications** reuse the existing event bus + `notification_service.go` (cheap).

---

## 8. Sub-projects & sequencing

| # | Sub-project | Size | Depends on |
|---|---|---|---|
| SP1 | Unified fee-rate config + admin API (rates only) | M | — |
| SP2 | Referral-graph hardening (ancestor path, downline, skip-and-continue policy) | M | — |
| SP3 | TP engine v2 — 30% base, gen0–10, route-and-continue, hook ALL release paths, async, reset/migration | L | SP1, SP2 |
| SP4 | Global-pool cumulative-index + lazy settlement | M | SP3 |
| SP5 | Payout computation — cumulative level rewards + cashback → claimable MCGP @ accrual OTC price, idempotent, reconciler | L | SP3 |
| SP6 | `MCGPRewardDistributor` merkle contract + admin root-publisher + treasury funding/ops | L | SP5 |
| SP7 | Anti-sybil + KYC-gate-before-payout | M | SP3 (gates SP6) |
| SP8 | App TP-levels/network/claim UI + level-up notifications | M | SP5, SP6 |
| SP9 | Scale validation (accrual + settlement + claim throughput) | M | SP3, SP4, SP5 |

---

## 9. Money-safety invariants

- Each level pays once `(user, level)`; cashback once `(user, txn)`; claims are cumulative-minus-claimed (contract-enforced replay safety).
- **Reconciler** compares the off-chain claimable ledger + published merkle roots against on-chain MCGP claims/transfers; drift logged/metered.
- Treasury MCGP balance monitor; insufficiency → queue + alert (never partial-silent).
- USD→MCGP rate locked at **accrual** (payout-time volatility can't change owed amounts).
- **Platform profitability invariant — ⚠️ CONFIRM (PM):** the retained 50% must, over time, cover 10%+10% cashback + cumulative level rewards + global-pool outflow. Model net-positive before launch.

---

## 10. Assumptions to confirm (PM)

1. **TP minting rate** `1 TP = $1 of TP-pool value` (sets level pacing).
2. **Global-pool** equal split among all registered users; cadence + "registered vs active" denominator.
3. **Cashback in MCGP** via the distributor (behavior change from today's USDC-on-chain cashback).
4. **Physical prizes (L6+)** admin-fulfilled flag.
5. **Out-of-scope features** (Spot, Instant-pay-crypto, Instant-transfer-fiat don't exist) — build later; fee config ready.
6. **Which fee events** carry the split — all of: P2P release (new integration), product, service, swap, withdrawal.
7. **KYC-gate** specifics (what verification level gates a claim).
8. **Profitability** net-positive after all four outflows.

---

## 11. Out of scope

- Heavy on-chain reward logic (only a standard merkle distributor for claims).
- Always-on backend signing key (admin signs root publishes; users sign claims).
- Building Spot / Instant-pay-crypto / Instant-transfer-fiat features.
- Grandfathering old balances (reset decision).
