# Corporate PM Onboarding — Design

**Date:** 2026-06-17
**Status:** Design (for review)
**Scope:** Backend (`tsa-api-go`) + minimal app/admin surface. Builds on the PM automation already on `main` (PRs #37/#38/#39 + hardening). No smart-contract changes.

## 1. Summary

Open Payment-Merchant (PM) status to corporate bodies. A corporate applies, an admin approves, and the backend **auto-provisions a Nomba sub-account + virtual account (VA) under TSA's single Nomba parent**, captures a settlement bank, and activates the PM. From then on, **any ad the corporate publishes automatically becomes a PM ad** (automated settlement), and the existing PM engine handles it unchanged.

This is an **extension**, not a rebuild. The settlement engine (webhook → match by `walletId` → `markPaid` → `adminRelease`; manual queue; recovery job) already supports many PMs because each PM is a distinct `walletId`. We add: onboarding, Nomba provisioning, balance-gated sell liquidity, sell-payout via sub-account, and ad-tagging.

## 2. Decisions (locked during brainstorming)

| Decision | Choice | Why |
|---|---|---|
| Fiat custody | **TSA's single Nomba parent; each PM = a Nomba sub-account + VA** | Fits the current single-client/single-webhook-secret code; PM keyed by `walletId`. Nomba sub-accounts are ledger partitions under TSA's KYB (not legally segregated — `BVN requirement: N`), so **TSA is the rail/holder of record**; accepted. |
| Sell-side liquidity | **Balance-gated: PM's NGN held in its VA; sell exposure ≤ available balance** | Mirrors the crypto-pool model (pool gates buys, NGN balance gates sells). No "auto-sweep starves sells" problem. |
| Funds settlement | **Funds stay in the VA as working balance; PM withdraws excess (above open exposure) to its settlement bank** | Keeps liquidity for sells; PM still gets paid out. |
| Onboarding | **Self-serve apply via existing `MerchantRequest`; admin approves → backend auto-provisions + activates** | Reuses existing surface; money-touching steps (provisioning, caps, activation) stay admin-controlled. |
| Ad → PM ad | **Auto-tag: an `active` PM user's new ads get `pm_id` set** | No new ad UI; reuses the entire ad/voucher/pool flow. |
| Release authority | **Unchanged — single platform admin EOA via `adminRelease`** | The admin EOA can release any trade regardless of PM; scales to many PMs with no per-PM key. |

**Non-goals (this phase):** legally-segregated per-merchant fiat (would require Model A — each corporate's own Nomba account); a full corporate self-serve portal (admin-driven setup for now); multi-currency beyond NGN; auto-scheduled sweeps (manual/triggered withdrawal first).

## 3. Architecture

**Reused unchanged:** the Nomba webhook receiver + verifier (one parent account → one secret), `PMAutomationService` (`HandleInbound` keyed by `walletId`, `HandlePayoutStatus` by `merchantTxRef`), `SystemMarkPaid`/`AutoRelease` via the admin EOA, the manual-release queue, the PM recovery job, per-trade + daily caps.

**New components:**
- **PM onboarding service** — consumes an approved `MerchantRequest`, provisions Nomba, writes the `payment_merchants` row, activates.
- **Nomba provisioning (in `nomba_client.go`)** — `CreateSubAccount`, `CreateVirtualAccountForSubAccount`, set sub-account settlement bank. (GATED on the Nomba API shapes — built against documented shapes + fakes; live after sandbox verification.)
- **NGN balance gate** — at sell-trade-open, ensure the PM's *available* NGN (sub-account balance − reserved-by-open-sells) covers the payout; reserve/consume/release across the trade lifecycle.
- **Sell-payout via sub-account** — route `InitiateSellPayout` through Nomba's *transfer-from-sub-account* (debits the PM's sub-account), not the parent.
- **Ad-tagging hook** — `CreateAd`/`createBuyAd` stamp `pm_id` when the creator is an active PM.
- **PM withdrawal** — an admin/PM action to settle excess sub-account balance to the PM's bank.

## 4. Data model (additive)

`payment_merchants` gains:
- `nomba_sub_account_id string` — the provisioned Nomba sub-account.
- `settlement_bank_code` / `settlement_account_number` / `settlement_account_name string` — where excess NGN is withdrawn (name-verified via the existing `/p2p/banks` resolve).

Existing fields map: `inbound_account_number` = the VA number (buyers pay here); `nomba_wallet_id` = the sub-account/VA wallet (the inbound match key); `nomba_account_id` = TSA parent (shared). `user_id` links the corporate's user (drives ad-tagging). `status`/caps unchanged.

`MerchantRequest` (existing): reuse `business_type='p2p_merchant'`. Capture the applicant's **settlement bank**, **maker wallet** (the corporate's EVM address that funds its crypto pool and is the `SellerAddress` on its ads — stored in the existing `operator_wallet_address` field), and **requested caps** in the request payload (extend its fields if absent; admin may adjust caps at approval).

No new column is needed for "reserved NGN" — it is **derived** as the sum of `fiat_amount` over the PM's open sell trades (status ∈ pending_payment/paid/releasing, funding = deposited-by-taker).

## 5. Onboarding & provisioning flow

1. Corporate applies → `MerchantRequest{business_type:'p2p_merchant', settlementBank, requestedCaps}`.
2. Admin approves (existing admin flow). On approval, the onboarding service:
   a. Calls Nomba **create sub-account** (under TSA parent) → `nomba_sub_account_id`.
   b. Calls Nomba **create VA for the sub-account** → VA account number (`inbound_account_number`) + `walletId` (`nomba_wallet_id`).
   c. Sets the sub-account's settlement destination = the PM's settlement bank (for excess withdrawal).
   d. Inserts/activates the `payment_merchants` row; upgrades the user's role.
3. Failure at any step → the request stays un-activated with a clear error; admin can retry. Provisioning is idempotent on `user_id` (don't double-create a sub-account).

## 6. Buy flow (unchanged + balance growth)

User buys crypto from a PM (PM sell-ad, pre-funded crypto pool). User pays NGN into the PM's **VA** → credits the PM's sub-account (balance grows) → inbound webhook (keyed by `walletId`) → match → `markPaid` → `adminRelease` releases crypto. Exactly today's flow; the only addition is the NGN lands in the per-PM sub-account.

## 7. Sell flow (balance-gated)

User sells crypto to a PM (PM buy-ad, user deposits crypto JIT). New gate at **open**:
- Compute `available = nombaSubAccountBalance(pm) − reservedNGN(pm)` where `reservedNGN` = Σ `fiat_amount` of the PM's open sell trades.
- If `available < this trade's fiat payout` → **reject the open** ("merchant NGN liquidity unavailable") — symmetric to a crypto-pool shortfall.
- Else proceed (the deposit + trade open as today). The payout amount is implicitly reserved by the trade being open.
- `InitiateSellPayout` pays the user from the **PM's sub-account** (transfer-from-sub-account) → on success, `adminRelease` sends the deposited crypto to the PM; the NGN has left the sub-account (balance shrinks). On failure → the existing alert/manual/watchdog path; the reservation releases when the trade cancels.

`nombaSubAccountBalance(pm)` uses Nomba's **fetch-sub-account-balance** endpoint (GATED shape; fakeable). To avoid a network call per open under load, this may be cached briefly or backed by a local NGN ledger later — v1 fetches live (sell-opens are low-frequency).

## 8. Ad-tagging

In `CreateAd`/`createBuyAd`: look up `payment_merchants WHERE user_id = <creator> AND status='active'`. If found, set `ad.PMID = pm.ID`. The ad's seller wallet must be the PM's funded maker wallet (validate: the seller address is the PM's registered maker wallet, else reject — the pool the admin EOA releases from must be the PM's). Non-PM users: unchanged.

## 9. PM withdrawal (excess settlement)

An admin/PM endpoint settles available NGN (sub-account balance − reserved) to the PM's settlement bank via Nomba. Guard: never withdraw below open sell exposure. (Auto-scheduled sweeps are a later enhancement.)

## 10. Failure / safety

- Provisioning failure → request not activated; idempotent retry; no orphan sub-accounts (check before create).
- Sell over-liquidity → open rejected (never a payout the PM can't cover).
- All existing PM safety carries over: settled-only auto-release, conservative single-hit matching, money-invariant fail-closed, manual queue, recovery job, daily/per-trade caps (now per corporate PM).
- Multi-PM webhook: one secret (TSA parent) still verifies all; `walletId` routes to the right PM; an unknown `walletId` → no-op.

## 11. Gated on Nomba sandbox confirmation

The provisioning + sub-account endpoints' exact request/response shapes: **create sub-account**, **create VA for sub-account**, **set sub-account settlement bank**, **fetch sub-account balance**, **transfer from sub-account**. Build against the documented shapes + fakes; verify against the Nomba sandbox before flipping live (same pattern as the transfer gateway). The `nomba-facts` memory + the API reference cover these.

## 12. Testing

- Onboarding service: approve → provisions (fake Nomba) → row active + fields stored; idempotent; failure leaves un-activated.
- Ad-tagging: active-PM user's ad gets `pm_id`; non-PM user's doesn't; wrong seller wallet rejected.
- Sell gate: open allowed when available ≥ payout; rejected when below; reservation = Σ open sell fiat; release on cancel.
- Sell payout via sub-account: debits the right sub-account; success → adminRelease; failure → manual.
- Provisioning + sub-account Nomba calls: httptest against documented shapes.
- Withdrawal: settles excess; blocked below open exposure.

## 13. Phasing (one plan, ordered tasks)

1. Data model + Nomba provisioning client methods (+ fakes).
2. PM onboarding service (MerchantRequest approval → provision → activate).
3. Ad-tagging hook.
4. Sell-side NGN balance gate + sub-account payout routing.
5. PM withdrawal endpoint.
6. Admin/app surface: approval action + (minimal) PM status view.
