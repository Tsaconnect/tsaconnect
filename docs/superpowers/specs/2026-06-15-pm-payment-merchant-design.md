# PM (Payment Merchant) — Automated P2P via Nomba

**Date:** 2026-06-15
**Status:** Design (approved for spec review)
**Scope:** Backend (`tsa-api-go`) + app surface for filtering/ordering. Smart contracts: **unchanged** in this phase.

## 1. Summary

A **Payment Merchant (PM)** is a corporate body that provides **automated** P2P liquidity. PM orders are ordinary P2P ads/trades that the platform settles automatically by talking to the merchant's **Nomba** account: it auto-`markPaid` on a matched inbound naira payment and auto-`release`s the escrowed token; on a sell it pays the user's bank via Nomba payout and releases on success.

PM is **not** a new subsystem. It reuses the existing P2P escrow, trade state machine, timeout watchdog, chain reconciler, dispute/admin-resolve flow, and fee/TP accrual. The only genuinely new logic is an **automation layer** that bridges Nomba events ↔ the existing two manual touchpoints (`markPaid`, `release`).

## 2. Decisions (locked during brainstorming)

| Decision | Choice | Why |
|---|---|---|
| Fiat custody | **Each PM has its own Nomba account** | Per-PM liquidity + settlement isolation; platform orchestrates. |
| On-chain release authorization | **Per-PM operator hot key, backend-signed; TSA-first** | No escrow contract change / audit / redeploy. Blast radius bounded per-PM and by escrow rules (release only pays the trade's counterparty). Prove the loop with TSA, then onboard corporates. |
| Buy-flow inbound attribution | **Sender bank-account name ≈ buyer KYC name + amount**, with **PM manual release** fallback | Users need no Nomba account and no bank registration; the KYC name is the anchor. Auto when names match; manual when they don't. |
| Architecture | **Reuse P2P; mark ads with `pm_id`; add a marketplace filter** | The escrow + lifecycle are already exactly what PM needs. |

**Non-goals (this phase):** public corporate PM onboarding (TSA is seeded manually), escrow contract changes, user-side Nomba accounts, multi-currency beyond NGN, dynamic virtual accounts.

## 3. Architecture

PM = automated P2P. Reused unchanged: `P2PEscrowV2` adapter (`p2p_escrow_service.go`), the trade state machine and transitions (`p2p_trade_service.go`: `OpenTrade`/`openTradeWithDeposit`, `MarkPaid`, `PrepareRelease`/`ConfirmRelease`, `ConfirmCancel`, dispute + `adminResolve`), the timeout watchdog + chain reconciler (`internal/jobs/`), and `accrueP2PReleaseFee` (fee/TP).

New components:
- **`PaymentMerchant`** entity + service — a registered PM and its Nomba creds / operator key / inbound account / caps / status.
- **Per-PM Nomba client factory** — builds a `NombaClient` from a PM's stored creds; adds payout + inbound-webhook + signature-verify capabilities the current client lacks.
- **Nomba webhook receiver** (`POST /api/webhooks/nomba`) + **async processor** — verifies, dedupes (`WebhookEvent`), maps an event to a trade action.
- **PM automation service** — the bridge: inbound-match → `markPaid` + operator-signed `release`; sell deposit → payout → release on payout success; failure/retry/manual-queue handling.
- **App:** a "PM"/automated filter over P2P ads, and the buy/sell order screens that show the PM's pay-to account + a manual-release queue for PM operators.

## 4. Data model (additive)

**New `payment_merchants`**
- `id`, `user_id` (the corporate's merchant account), `name`, `status` (`pending`|`active`|`paused`).
- Nomba: `nomba_account_id` + a **secret-store reference** for client id/secret (encrypted creds never stored plaintext in the DB).
- `inbound_account_number` / `inbound_bank` — the static Nomba account users pay into for buys.
- `operator_wallet_address` — the EVM address whose key (in the secret manager) the backend uses to sign `release`/deposit; **never** stored in the DB.
- Caps: `per_trade_cap_usd`, `daily_cap_usd` (+ a running daily counter or derived).
- timestamps.

**`p2p_ads` +** `pm_id *uuid` — null = normal P2P, set = automated PM ad (FK to `payment_merchants`). This column *is* the marketplace "PM filter." Side/token/rate/fiat/limits/payment-methods reused as-is.

**`p2p_trades` +**
- `payment_ref string` — optional human reference (secondary signal only).
- `nomba_inbound_event_id string` — the matched inbound `WebhookEvent` (buy).
- `nomba_payout_id string` — the Nomba payout reference (sell).
- `payout_account_number` / `payout_bank` / `payout_account_name` — the user's KYC-name-verified destination (sell).

Status machine **unchanged**. Unmatched inbound payments use the existing `WebhookEvent` model + a **PM manual-release queue** (a query over `pending_payment` PM trades with an unresolved inbound, surfaced to the PM operator).

**Constraint:** at most **one open PM buy trade per user** at a time (tighten the existing one-active-per-ad+opener index for PM ads) so `sender-name + amount` is an unambiguous auto-match key.

## 5. Buy flow — user buys token from a PM (= a sell-ad; PM is seller, token pre-funded in escrow)

1. PM publishes an automated **sell-ad** (`pm_id` set; token, rate, NGN, limits), funded into the escrow pool by the PM operator key.
2. User (KYC-verified) opens a buy trade → existing `OpenTrade`. Trade is `pending_payment`. The app shows: **pay ₦`fiatAmount` to the PM's Nomba account from an account in your own name** (the name on file = their KYC name). A `payment_ref` is shown as an optional aid.
3. User pays by normal bank transfer. Nomba **inbound webhook** fires → receiver verifies signature, dedupes, persists `WebhookEvent`, enqueues processing.
4. **PM automation service** matches: `normalize(sender_account_name) ≈ normalize(buyer KYC name)` (threshold) **AND** `amount == trade.fiatAmount` **AND** exactly one open PM buy trade for that user:
   - **Match** → `MarkPaid` (system-driven; no proof image) → operator-signed `release` → token to user. `accrueP2PReleaseFee` fires.
   - **No/low-confidence/ambiguous match** → leave in the **PM manual-release queue**: the PM operator reviews the payment and either releases (manual `MarkPaid`+`release`) or declines. If never resolved, the **existing timeout watchdog** cancels the trade (token returns to the PM pool); declined/overpaid funds are refunded out-of-band by the PM.
5. **Amount mismatch** (under/over) never auto-releases → manual queue.

## 6. Sell flow — user sells token to a PM (= a buy-ad; PM is buyer, user deposits token JIT)

1. PM publishes an automated **buy-ad** (`pm_id` set).
2. User opens a sell trade → existing `openTradeWithDeposit` (user's token escrowed JIT). User supplies a **destination bank account**; the system **Nomba name-lookup verifies it is in the user's own KYC name** before proceeding (mismatch → blocked/manual). Stored on the trade.
3. PM automation service initiates a **Nomba payout** from the PM's account → the user's bank (₦ `fiatAmount`); records `nomba_payout_id`.
4. Nomba **payout webhook**:
   - **success** → operator-signed `release` → token to PM. `accrueP2PReleaseFee` fires.
   - **failed** (after bounded retries) → cancel the trade via the existing cancel path → deposited token returns to the user; alert PM/admin.

**Ordering (deliberate):** sell flow pays the user **fiat first, then releases token to the PM**. A release that fails after a successful payout leaves the PM merely *owed* an escrowed token (deterministic, idempotent on-chain retry) — the **user is always made whole first**. The reverse ordering would risk the user losing their token with no fiat.

## 7. Nomba layer (per-PM)

Current `nomba_client.go` has only `ListBanks` + `Lookup` (OAuth2 client-credentials, token cache). Add:
- **`PerPMClient(pm)`** factory — a `NombaClient` built from the PM's stored creds.
- **Payout / transfer-to-bank** — initiate a payout to `(bankCode, accountNumber, amount)`; return a payout id; expose status.
- **Inbound-payment webhook** + **HMAC signature verification** (per-PM secret).
- Route: `POST /api/webhooks/nomba` → verify → `WebhookEvent` (dedup on `provider="nomba", event_id`) → async processor. The **PM is identified from the destination account** in the payload.
- The processor maps: inbound → buy match (§5); payout status → sell settle/refund (§6). Idempotent per `event_id`.

**Dependency to confirm against Nomba's API:** (a) inbound webhook payload includes the **payer account name/number**; (b) payout endpoint + status webhook exist; (c) webhook signature scheme. If (a) is unavailable, fall back to the optional `payment_ref`.

## 8. PM onboarding (reuse `MerchantRequest`)

- Corporate applies via the existing `MerchantRequest` (`business_type = p2p_merchant`, already defined) → admin approves → role upgraded → a `payment_merchants` row is created (`pending`).
- Setup: link Nomba creds (encrypted to secret store), register the operator wallet + **fund the escrow pool**, set the inbound account + payout source, rate/limits/caps → admin activates (`active`).
- **Phase 1: TSA is seeded manually** (one `payment_merchants` row; creds via secure config). Public corporate onboarding is Phase 2.
- KYC: PM operations require a verified merchant (existing P2P `require(verified)` gate). Users require KYC verification (their name is the buy-flow match anchor and the sell-flow payout-name check).

## 9. Failure / reconciliation / dispute safety

- **Inbound no-match / amount mismatch** → PM manual-release queue; never auto-release. Timeout watchdog cancels if unresolved.
- **Payout fails (sell)** → bounded retry → permanent fail cancels + refunds the user's deposited token + alerts.
- **Release fails after fiat settled** → idempotent retry via the existing confirm/reconciler until released; alert if stuck.
- **Inbound reversal / chargeback after release** → crypto cannot be clawed back. Mitigation: release **only on a `settled` webhook** (never `pending`) + an optional hold on large amounts. Residual loss is the PM's risk — explicitly accepted.
- **Disputes** → automated trades still flow through the existing dispute → `adminResolve` path.
- **Kill switch** → set a PM `paused`: no new trades; in-flight trades drain.
- **Operator-key safety** → keys in a secret manager (never DB/plaintext); sign only on a verified, settled, idempotent webhook; per-PM caps + monitoring; start TSA-only.

## 10. Fee / TP

PM trades **are** P2P trades, so the existing **1% `p2p_platform`** fee and `accrueP2PReleaseFee` fire on release exactly as today → TP/cashback via the fee engine. No change.

## 11. Phasing

1. **Phase 1 (this spec): TSA-only automated PM.** Both buy and sell flows; manual `payment_merchants` seed; per-PM operator key; reference-optional name+amount matching with PM manual-release fallback; Nomba payout + inbound webhook + signature.
2. **Phase 2:** public corporate PM onboarding (the `MerchantRequest` → setup → activate path), multiple live PMs, per-PM caps/monitoring dashboards.
3. **Later (custody hardening):** escrow `operator` role (contract upgrade + audit) to remove per-PM hot keys, if volume justifies it.

## 12. Open items to verify before/within implementation

- Nomba: inbound payload fields (payer name/number), payout API + status webhook, signature scheme, sandbox availability.
- Fuzzy name-match thresholds and normalization rules (case, ordering, middle names, business vs personal).
- Secret-store mechanism for per-PM Nomba creds + operator keys (env/KMS).
