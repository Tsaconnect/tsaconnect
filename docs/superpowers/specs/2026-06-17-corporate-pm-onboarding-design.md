# Corporate PM Onboarding — Design

**Date:** 2026-06-17
**Status:** Design (revised after independent review)
**Scope:** Backend (`tsa-api-go`) + minimal app/admin surface. Builds on the PM automation on `main` (PRs #37/#38/#39 + hardening). No smart-contract changes.

## 1. Summary

Open Payment-Merchant (PM) status to corporate bodies. A corporate applies, an admin approves, and the backend **provisions a Nomba sub-account + virtual account (VA) under TSA's single Nomba parent**, captures a settlement bank + maker wallet, and activates the PM. Thereafter **any ad the corporate publishes is automatically a PM ad**.

**Honest reuse boundary** (validated against the code in review): the **buy flow, webhook receiver/verifier, matching-by-`walletId`, `adminRelease` via the single admin EOA, manual queue, recovery job, and per-PM caps are reused unchanged** — they already support many PMs. What is **genuinely new**: (a) **per-PM sell-payout routing** (debit the PM's sub-account, not the parent), (b) an **atomic per-PM NGN balance ledger** that gates sells, (c) **provisioning** (resume-able state machine), (d) **onboarding** (MerchantRequest extension + a provisioning branch in approval), (e) **ad-tagging**.

## 2. Decisions (locked during brainstorming + revised after review)

| Decision | Choice | Why |
|---|---|---|
| Fiat custody | TSA's single Nomba parent; each PM = a Nomba sub-account + VA | Fits the single-client/single-secret code; PM keyed by `walletId`. **Sub-accounts are ledger partitions under TSA's KYB (not legally segregated; `BVN requirement: N`) → TSA holds legal title; the PM has a contractual claim, not ownership.** Accepted. |
| Sell-side liquidity | Balance-gated via an **atomic per-PM NGN ledger** (not derive-on-read) | The crypto pool gates buys with an atomic conditional UPDATE; the NGN side must match it or it over-commits (review B2). |
| Per-PM payout routing | Thread the PM's **sub-account id** through `PayoutRequest → NombaGateway → NombaClient`; sell payouts debit the PM's sub-account | Today's single-gateway/single-`accountId` wiring debits the parent (review B1). One client, sub-account as a per-call parameter — no per-PM client/creds. |
| NGN ledger source of truth | **Local ledger** for the gate (atomic); Nomba sub-account balance for **reconciliation** only | Same shape as crypto: DB `remaining/reserved` reconciled against on-chain. A live fetch can't be the atomic anchor (latency, unsettled/mid-withdrawal funds). |
| Funding the NGN balance | Every **settled inbound credit** to the PM's VA increases its `available_ngn` (whether a buyer payment or a PM self-top-up); payouts decrease it | A sell-first PM funds by transferring NGN into its VA; no special top-up endpoint needed (review I6). |
| Onboarding | Self-serve apply via **extended** `MerchantRequest`; admin approves → provisioning branch auto-provisions + activates | Reuses the application surface; money-touching steps stay admin-controlled. (Net-new fields + approval branch — review I3.) |
| Ad → PM ad | Auto-tag: an `active` PM user's new ads get `pm_id`; **all** their ads are PM ads (no manual-ad opt-out this phase) | Matches the stated intent ("corporate users' ads become PM ads"). |
| Release authority | Unchanged — single platform admin EOA via `adminRelease` | Releases any trade regardless of PM; no per-PM key. |

**Non-goals (this phase):** legally-segregated per-merchant fiat (would need Model A — each corporate's own Nomba account); a full self-serve corporate portal; multi-currency beyond NGN; auto-scheduled sweeps; changing fee/TP accrual per PM (corporate-PM trades accrue via the existing engine exactly like TSA's — unchanged).

## 3. Architecture

**Reused unchanged** (review-confirmed sound): Nomba webhook receiver + `NombaWebhookVerifier` (one parent → one secret verifies all); `HandleInbound` keyed by `data.merchant.walletId` → `GetByWalletID` (rejects empty walletId — no cross-PM leakage); `HandlePayoutStatus` by `merchantTxRef`; `SystemMarkPaid`/`AutoRelease`; manual queue; recovery job; per-PM `WithinPerTradeCap`/daily-cap (queries scope `WHERE p2p_ads.pm_id = ?`).

**New components:**
- **PM onboarding service** — consumes an approved `MerchantRequest`, runs provisioning, writes/activates the `payment_merchants` row.
- **Nomba provisioning (in `nomba_client.go`)** — `CreateSubAccount`, `CreateVirtualAccountForSubAccount`, set sub-account settlement bank, `FetchSubAccountBalance`, `TransferFromSubAccount`. (GATED on Nomba shapes — built against documented shapes + fakes; live after sandbox verification.)
- **Per-PM NGN ledger** — `available_ngn` + `reserved_ngn` on `payment_merchants`; atomic reserve on sell-open, consume on payout success, release on cancel/fail; credited by settled inbound webhooks.
- **NGN reconciler** — periodic job comparing each PM's ledger `available_ngn` to its Nomba sub-account balance; logs/alerts drift (mirrors the chain reconciler).
- **Per-PM payout routing** — `PayoutRequest.SubAccountID`; `InitiateSellPayout` resolves it from the trade's ad→PM.
- **Ad-tagging hook** — `CreateAd`/`createBuyAd` stamp `pm_id` for active PMs.
- **PM withdrawal** — admin/PM action to settle excess (`available_ngn − reserved_ngn`) to the settlement bank, atomically.

## 4. Data model (additive)

`payment_merchants` gains:
- `nomba_sub_account_id string` — the provisioned sub-account (idempotency anchor for provisioning).
- `settlement_bank_code` / `settlement_account_number` / `settlement_account_name string`.
- `available_ngn numeric(38,2)` + `reserved_ngn numeric(38,2)` — the NGN ledger (default 0). `available_ngn` tracks the sub-account balance (credited by every settled inbound, debited by every payout); `reserved_ngn` = NGN committed to open sell trades.
- `provisioning_status string` — `pending` → `provisioned` → `active` (+ `failed`); supports resume-able provisioning.

Existing-field mapping: `inbound_account_number` = VA number; `nomba_wallet_id` = sub-account/VA wallet (inbound match key); `operator_wallet_address` = the PM's **maker wallet** (crypto pool funder + the `SellerAddress`/recipient on its ads); `nomba_account_id` = TSA parent (shared); `user_id` = corporate user (drives ad-tagging); `status`/caps unchanged.

`MerchantRequest` gains (review I3): `settlement_bank_code`/`settlement_account_number`, `maker_wallet_address`, `requested_per_trade_cap_usd`/`requested_daily_cap_usd`. `ApproveMerchantRequest` gains a `business_type='p2p_merchant'` provisioning branch (distinct from the generic merchant-role upgrade).

`reserved_ngn` is an explicit column (NOT derived) so it can be decremented atomically — see §7.

## 5. Onboarding & provisioning (resume-able state machine — review I4)

1. Corporate applies → extended `MerchantRequest` (settlement bank, maker wallet, requested caps). Settlement bank + maker wallet validated (bank via `/p2p/banks` resolve; wallet is a valid EVM address).
2. Admin approves → onboarding service runs, writing state as it goes so a retry resumes rather than restarts:
   a. Upsert `payment_merchants` row `provisioning_status='pending'` (keyed on `user_id`).
   b. If `nomba_sub_account_id` unset → Nomba create sub-account → store it.
   c. If VA fields unset → Nomba create VA for that sub-account → store `inbound_account_number` + `nomba_wallet_id`.
   d. Set the sub-account settlement destination = the PM's settlement bank.
   e. All set → `provisioning_status='provisioned'`, `status='active'`; upgrade role.
3. A retry re-enters at the first unset field (idempotency anchor = the stored `nomba_sub_account_id`, never a blind re-create → no orphans). Activation gates on **all** steps. Rejection/abandonment → an admin "discard" expires the VA + flags the row; document orphan cleanup as best-effort.

## 6. Buy flow (unchanged + ledger credit)

User buys crypto from a PM (PM sell-ad, pre-funded crypto pool). User pays NGN into the PM's VA → settled inbound webhook (keyed by `walletId`) → **credit `available_ngn += amount`** → match → `markPaid` → `adminRelease` releases crypto from the PM's pool. The only addition to today's flow is the ledger credit; every settled inbound credits `available_ngn` whether or not it matches a trade (so a PM self-top-up is just an unmatched credit that grows the balance).

## 7. Sell flow (atomic balance-gated — review B2)

User sells crypto to a PM (PM buy-ad, user deposits crypto JIT). At **open**, inside the same DB transaction as the existing crypto-side capacity update, perform an **atomic NGN reservation** mirroring the pool's conditional UPDATE:
```
UPDATE payment_merchants
   SET reserved_ngn = reserved_ngn + :payout
 WHERE id = :pm AND (available_ngn - reserved_ngn) >= :payout
```
`RowsAffected == 0` → reject the open ("merchant NGN liquidity unavailable"), roll back. This is compare-and-decrement, not derive-on-read — concurrent opens cannot over-commit.

- `InitiateSellPayout` debits the **PM's sub-account** via `TransferFromSubAccount` (resolving `nomba_sub_account_id` from the trade's ad→PM), with `merchantTxRef` = trade id.
- **Payout success** webhook → `consume`: `available_ngn -= payout; reserved_ngn -= payout` (atomic), then `adminRelease` sends the deposited crypto to the PM. NGN has left the sub-account.
- **Payout failure / cancel / expiry** → `release`: `reserved_ngn -= payout` (atomic). This must fire on the existing failed-payout path (today it leaves the trade `pending_payment` for the watchdog to cancel + refund the deposit) — the spec **requires** that when such a trade reaches a terminal cancel, the reservation is released. Confirm the watchdog cancels these (not only refunds).

Nomba's live sub-account balance is used only by the §3 reconciler to detect ledger drift, never as the per-open gate.

## 8. Ad-tagging (review I5)

In `CreateAd`/`createBuyAd`: look up `payment_merchants WHERE user_id = <authenticated creator> AND status='active'`. If found, set `ad.PMID = pm.ID`. (`user_id` from the JWT → a non-PM user can't self-tag.)

- **Seller/maker wallet check:** the ad's seller address (`SellerAddress` for a sell-ad = pool funder; the merchant's crypto-recipient wallet for a buy-ad) **must equal the PM's registered maker wallet** (`operator_wallet_address`), else reject. Both ad types use the maker wallet — verify the buy-ad recipient is the same registered wallet so the admin EOA releases from/the PM receives at the right address.
- **No opt-out:** every ad an active PM creates is a PM ad (intended). A corporate that needs a manual ad is out of scope this phase.
- **Pause/offboard:** ads tagged while `active` that remain open when the PM is `paused` keep `pm_id`; the buy flow skips paused PMs → they drain to the manual queue (intended). Offboarding uses `status` (`paused` to halt new trades; a future `disabled` for permanent) — in-flight trades drain; no new ads tag once non-active.

## 9. PM withdrawal (atomic — review I7)

Admin/PM endpoint settles excess to the PM's settlement bank. Atomic guard against the same counter as §7:
```
UPDATE payment_merchants
   SET available_ngn = available_ngn - :amt
 WHERE id = :pm AND (available_ngn - reserved_ngn) >= :amt
```
`RowsAffected == 0` → reject (would dip below open sell exposure). Then a Nomba transfer from the sub-account to the settlement bank. Auto-scheduled sweeps are a later enhancement.

## 10. Failure / safety

- Provisioning partial failure → resume-able (§5); no orphan sub-accounts (anchor on stored id); activation gates on all steps.
- Sell over-liquidity → impossible (atomic reservation); a payout the PM can't cover is never opened.
- Per-PM payout routing → sells debit the correct sub-account, not the parent (B1 fix).
- Ledger drift → the reconciler alerts; the local ledger gates, Nomba is truth-of-record for money.
- All existing PM safety carries over: settled-only auto-release, conservative single-hit match, money-invariant fail-closed, manual queue, recovery job, per-PM caps.
- Multi-PM webhook: one secret verifies all; `walletId` routes; unknown/empty `walletId` → no-op (review-confirmed).
- Custody clarity: ledger balances are TSA-owned partitions; the PM holds a contractual claim, not legal title (avoids inversion confusion).

## 11. Gated on Nomba sandbox confirmation

Exact request/response shapes for: **create sub-account**, **create VA for sub-account**, **set sub-account settlement bank**, **fetch sub-account balance**, **transfer from sub-account**. Build against documented shapes + fakes; verify in the Nomba sandbox before live (same pattern as the transfer gateway). Acknowledged code-shape impact (review B1): `PayoutRequest`/`NombaGateway`/`NombaClient` gain a sub-account parameter; the single-`accountId` assumption changes to a per-call sub-account target.

## 12. Testing

- Onboarding/provisioning: approve → provisions (fake Nomba) → row active + fields stored; **partial-failure resume** (sub-account created, VA fails → retry resumes, no second sub-account); activation gates on all steps.
- NGN reservation: concurrent sell-opens cannot exceed balance (atomic UPDATE; RowsAffected==0 path); reserve→consume on payout success; reserve→release on cancel/fail/expiry; reconciler flags an injected drift.
- Per-PM payout routing: payout debits the trade's PM sub-account (assert the sub-account id reaches the gateway), not the parent.
- Ad-tagging: active-PM user's ad tagged; non-PM not; wrong seller wallet rejected; buy-ad recipient == maker wallet; paused PM's open tagged ads drain to manual.
- Funding: settled inbound (unmatched = top-up) credits `available_ngn`; a zero-balance PM's sell-open is rejected until funded.
- Withdrawal: settles excess; blocked below open exposure (atomic).
- Nomba provisioning/sub-account calls: httptest against documented shapes.

## 13. Phasing (one plan, ordered tasks)

1. Data model: `payment_merchants` NGN-ledger + provisioning + settlement fields; `MerchantRequest` fields. Migrations + atomic-update helpers.
2. Nomba provisioning + sub-account client methods (CreateSubAccount, CreateVA, settlement, FetchSubAccountBalance, TransferFromSubAccount) + fakes.
3. PM onboarding service (resume-able provisioning) + approval-branch wiring.
4. Per-PM NGN ledger: credit-on-inbound, atomic reserve/consume/release across the sell lifecycle, + the reconciler job.
5. Per-PM payout routing (sub-account) through PayoutRequest/gateway/client + `InitiateSellPayout`.
6. Ad-tagging hook (+ maker-wallet validation).
7. PM withdrawal endpoint (atomic).
8. Admin/app surface: approval action + minimal PM status/balance view.
