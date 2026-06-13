# P2P Region-Scoped Payment-Method Picker (#24)

**Date:** 2026-05-30 · **Branch:** `feat/p2p-trading` · **Status:** spec (review-ready; not yet built).

## Goal
Drive the payment-method picker off the selected fiat/country so users only see relevant
providers, each with a **speed label** (Instant / ~10 min / 1–2 days). NG→OPay/PalmPay/bank,
CA→Interac, EU→SEPA/Revolut/Wise, etc. Today the provider catalog
(`internal/services/p2p_payment_providers.go`) is a flat 9-provider list with no region scoping
or speed metadata.

## Design

### Backend (catalog extension — pure, testable; ship first)
- Extend the provider catalog entries with `Currencies []string` (or `Countries`) it applies to
  and a `Speed` enum (`instant | fast | standard | slow`) + a human label. Keep the existing
  slugs (`bank_transfer/opay/palmpay/kuda/interac/paypal/revolut/wise/skrill`); add the
  region + speed metadata. Add the missing common ones if needed (sepa) without breaking the
  existing `IsValidProvider`/`ProviderType`.
- A mapping `providersForCurrency(currencyOrCountry) → []Provider` (case-insensitive). Generic
  providers (e.g. bank_transfer) are universal; mobile-money/region ones are scoped.
- **Endpoint:** extend `GET /api/p2p/payment-providers` to accept `?currency=` (and/or
  `?country=`) and return the scoped list **with `speed` + `speedLabel`**. Unscoped call returns
  all (back-compat). Pure read.
- **Tests:** scoping correctness (NG returns OPay/PalmPay/bank but not Interac; CA returns
  Interac; unknown currency → generic providers only); speed labels present; back-compat unscoped.

### App
- The **add-payment-method** screen (`app/p2p/payment-methods/new.tsx`) and the marketplace
  **payment filter** drive their provider list off the active currency (from `useP2pCurrency`),
  rendering providers via `PickerSheet` grouped by speed, each row showing the speed label. The
  NG-bank NUBAN/Nomba path is preserved. Pre-saved instruments unchanged.
- TSA Signature design system; visual sign-off like PR #13.

## Out of scope (MVP)
Per-method N-cap per ad; localized provider names beyond English; auto-detecting country from
device locale (use the existing currency selector). These are easy follow-ons.

## Rollout
1. Catalog metadata + scoped `payment-providers` endpoint + unit tests → PR → merge (additive, verifiable).
2. App picker wiring (add-method + filter) → PR open for visual sign-off.
