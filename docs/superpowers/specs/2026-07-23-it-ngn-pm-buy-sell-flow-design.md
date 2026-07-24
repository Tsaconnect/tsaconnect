# IT Buy/Sell — PM flow shell, NGN-only — Design

**Date:** 2026-07-23  
**Status:** Implemented on branch `feat/it-ngn-pm-buy-sell-flow`  
**Repos:** `tsa-api-go`, `tsa-app`

## Goal

Reshape Instant Transfer Buy/Sell to the PM’s sketched flow (full-page form →
Continue / Review → action), with **Naira only** for both directions until
multi-currency collection/payout is product-ready.

## Decisions

- **Buy:** NGN only (bank transfer → assigned VA). No card/wallet. No PIN.
- **Sell:** NGN bank only. Form → Review (full fee quote) → AuthorizationModal
  PIN → Transfer.
- **Locked shell fields:** show Payment/Payout currency = NGN, Method = bank,
  Country = Nigeria (sell) as visible single-choice / disabled rows so the UI
  matches the PM sketch and can expand later.
- **Backend product lock:** `WithdrawalDestinationCatalog` = NGN bank only;
  Sell rejects other currency/rail combos. Phase 2 cross-currency gateway code
  stays dormant behind the catalog.
- **Out of scope:** USD/GBP/EUR/AED, card/wallet, GHS/XOF live payouts, SWIFT.

## Architecture

Money paths unchanged (NGN VA buy; NGN bank sell). Work is app flow/UI +
catalog lock. Fees via existing `GET /api/it/quote`. PIN via existing
`AuthorizationModal`.

## Buy flow

1. Form: locked currency NGN, locked method bank transfer, amount ₦.
2. Continue → `itBuy` → VA / pay-to instructions (existing).

## Sell flow

1. Form: MCGP amount; locked NGN / bank / Nigeria; bank code, account, name.
2. Review: destination + itemized fees from `/api/it/quote?flow=withdraw&asset=MCGP&amount=<ngn>&currency=NGN`.
3. Transfer → AuthorizationModal → `itSell` (NGN bank fields).
4. Success screen.

## Testing

- Catalog / destination validation: NGN bank only; GHS/XOF rejected.
- App: fee-quote client; existing IT jest suites; typecheck on touched files.
