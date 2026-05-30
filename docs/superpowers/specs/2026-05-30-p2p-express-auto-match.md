# P2P Express Auto-Match (#22)

**Date:** 2026-05-30 · **Branch:** `feat/p2p-trading` · **Status:** spec (review-ready; not yet built).

## Goal
A one-tap "Express" buyer flow: the user enters asset + fiat + amount (+ optional method),
and the app instantly matches them to the **best** eligible ad and opens a trade — turning
browse-and-pick into an automatic best-price match. Browse remains the power-user fallback.

## MVP scope decision (important)
The roadmap's full vision is "broadcast to top N merchants → fastest-accept-wins (<1 min)
accept-lock". That needs a merchant-side accept UI + on-server locking + timeouts — a large,
stateful sub-system. **MVP cuts that:** Express = **rank eligible ads → auto-select the best →
open a trade against it** (reusing the existing `OpenTrade`/`openTradeWithDeposit` flow). It's
instant, deterministic, and reuses the now-hardened open path. The broadcast/accept-lock variant
is a Phase-2 enhancement, explicitly out of MVP.

## Design

### Backend (pure, testable — ship first)
- **Matching service** `ExpressMatch(side, token, fiat, amount, paymentMethodId?) → ranked []AdMatch`:
  - Eligible = active ads of the right `side` + `token` + `fiatCurrency`, not rate-stale,
    `remaining >= amount` (token side) and the implied fiat within the ad's min/max limits,
    and (if a method is given) the ad's payment-method set includes it.
  - **Rank score** = price (primary) × reputation (tiebreak/weight). For a BUY (user buys crypto)
    the best price is the **lowest** effective rate; for a SELL the **highest**. Reputation pulls
    from `p2p_merchant_stats` (completion rate, avg release time, online) — advisory only, never
    gates money. Concrete: sort by effective rate, then by a reputation score; expose the score so
    the UI can explain "best price + trusted".
  - Reuse `ComputeEffectiveRate` (float ads) + the existing fiat/token math; **no new money math**.
- **Endpoint** `POST /api/p2p/express/quote {side, token, fiatCurrency, amount, paymentMethodId?}`
  → `{ best: AdMatch, alternatives: AdMatch[] }` (top ~3). Pure read; auth + KYC gated like all `/p2p`.
  Returns 404/empty when nothing matches (UI shows "no merchants available, try browse").
- Opening is the EXISTING `POST /p2p/ads/:id/trades` against `best.adId` — Express does not add a
  new open path, so all the hardening (dedup, validation, capacity) applies unchanged.
- **Tests:** ranking correctness (price ordering per side, reputation tiebreak, eligibility
  filters: stale/limit/method/remaining); empty-match. Pure unit tests (no chain).

### App
- An **Express** entry (tab or primary CTA on `/p2p`): one form (`SegmentedControl` Buy/Sell,
  token `Chip`s, `AmountInput`, optional method via `PickerSheet`) → on submit call `express/quote`
  → show the **best match** card (seller `TrustBadge`, `AmountText` rate, fee `SummaryCard`,
  `Spinner` "finding best price…") + "see N alternatives" → Confirm opens the trade (existing flow)
  → routes to the trade cockpit. Empty → CTA to browse.
- All on the TSA Signature design system; visual sign-off like PR #13.

## Out of scope (MVP)
Broadcast-to-top-N + fastest-accept-wins accept-lock; merchant accept UI; auto-retry on decline.
Those are Phase-2 once the simple best-match Express proves the funnel.

## Rollout
1. Matching service + `express/quote` endpoint + unit tests → PR → merge (verifiable, additive).
2. Express app screen → PR open for visual sign-off.
