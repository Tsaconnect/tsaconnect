# P2P Optimistic Auto-Release (#26)

**Date:** 2026-05-30 · **Branch:** `feat/p2p-trading` · **Status:** spec (review-ready; not yet built).

## Goal
Opt-in optimistic auto-release: once the fiat-payer marks a trade paid, if the crypto-provider
(seller) neither releases nor disputes within a window, the escrow auto-releases to the buyer.
Removes "stuck in paid" when the seller is offline. Contract already supports it
(`autoReleaseOptIn`, `autoReleaseWindow`, `finalizeRelease`, `TradeAutoReleased`).

## How the contract arms it (the crux)
`markPaid(tradeId)` on-chain sets `autoReleaseAt = now + autoReleaseWindow` **iff**
`autoReleaseWindow > 0 && autoReleaseOptIn[t.seller]`. `finalizeRelease(tradeId)` is permissionless
after `autoReleaseAt` (Paid + armed). **Today the API's mark-paid is DB-only — the contract's
markPaid is never called — so auto-release is never armed.** Enabling it therefore requires the
buyer's mark-paid to also go **on-chain** when the counterparty has opted in.

## Design

### Contract config (owner, one-time)
- Owner sets `autoReleaseWindow` to a sane default (e.g. 24h) via `setAutoReleaseWindow`. While 0,
  the feature is fully dormant (no behavior change) — so this is the master switch.

### Backend
- **Opt-in:** `PrepareSetAutoReleaseOptIn(seller, bool)` builds the `setAutoReleaseOptIn` tx;
  confirm reads back / stores the opt-in on the merchant profile (or just reflects on-chain).
  Surface per-(seller,token) opt-in state.
- **On-chain mark-paid (conditional):** when the trade's seller has auto-release opted in AND
  `autoReleaseWindow > 0`, `MarkPaid` returns an unsigned on-chain `markPaid` tx for the fiat-payer
  to sign (arming `autoReleaseAt`), with a confirm step (parse `TradePaid`, store
  `auto_release_at` on the trade). When NOT opted in, mark-paid stays DB-only (unchanged path).
  Add `AutoReleaseAt *time.Time` to `P2PTrade`.
- **Keeper job** `internal/jobs/auto_release_keeper.go` (e.g. 1-min tick): find Paid trades with
  `auto_release_at` elapsed, call `finalizeRelease(tradeId)` via a **funded keeper EOA**, then
  reconcile to released on `TradeAutoReleased` (the escrow ABI already parses it). Permissionless
  on-chain, so the keeper just pays gas; idempotent (re-finalizing a released trade is a no-op/skip).
  Register in `cmd/server/main.go` alongside the reconciler/watchdog. Keeper key from config
  (reuse the system/relayer pattern); dormant if unset.
- **Tests:** opt-in prepare; mark-paid branches (armed vs DB-only); keeper selects only elapsed
  armed Paid trades; finalize→released reconcile. The on-chain leg is E2E-verifiable with the
  funded key (owner sets a short window, opt-in, on-chain markPaid, wait, keeper finalizes).

### App
- Seller **auto-release toggle** (prepares `setAutoReleaseOptIn`) in their merchant/ad settings,
  with the ⚡ surfaced on their ads/trust badges (the `TrustBadge autoAccept` slot already exists).
- Buyer mark-paid flow: when armed, the app signs the extra on-chain markPaid tx (the UI explains
  "the seller has auto-release on — your payment confirmation goes on-chain").

## Constraints / risks
- **Flow change:** adds an on-chain markPaid tx (gas) to the buyer's happy path **only when armed**
  (opt-in + window>0); default flow is unchanged. Document clearly.
- **Keeper EOA** must stay funded; if it stalls, sellers can still release manually (auto-release is
  a safety net, not the only path). The window should be generous (hours) so manual release wins normally.

## Out of scope (MVP)
Gasless/relayer-sponsored markPaid (Phase-1 gasless track); per-trade window overrides; auto-release
analytics. Auto-release stays owner-gated (window) + seller-opt-in so it can ship dark and be enabled deliberately.

## Rollout
1. `AutoReleaseAt` field + opt-in prepare/confirm + conditional on-chain markPaid + keeper job + tests → PR.
2. On-chain E2E with the funded key (short window) → verify.
3. App opt-in toggle + buyer armed-markPaid UI → PR open for visual sign-off.
