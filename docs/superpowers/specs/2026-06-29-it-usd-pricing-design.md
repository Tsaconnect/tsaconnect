# Instant Transfer — USD pricing with live FX conversion

**Date:** 2026-06-29
**Status:** Design — approved, pending spec review
**Repos affected:** `tsa-api-go` (backend), `tsa-admin` (admin dashboard). `tsa-app` (user app) requires no functional change.

## Problem

Instant Transfer (IT) lets users buy/sell MCGP for Naira via the company Payment
Merchant. Today the admin sets the price **directly in Naira** (`ITRate.BuyRateKobo` /
`SellRateKobo`, kobo per 1 MCGP) on the admin dashboard.

This is the wrong unit. MCGP is a USD-denominated asset (Contract B prices premium
at ~$0.02). Pricing it in Naira means the admin must hand-recompute and re-enter the
Naira rate every time the NGN/USD exchange rate moves, and the IT price silently
drifts from MCGP's real value between updates. Every other asset on the platform
(P2P float ads) is priced in USD and converted to Naira using a live FX feed.

## Goal

Make IT price MCGP in **USD**, set by the admin on the dashboard, and convert to
Naira at request time using the **same NGN/USD exchange rate the rest of the
platform already uses** (the P2P FX feed). Behaviour around staleness/availability
must match how P2P assets already behave — no separate IT-specific fallback.

## Decisions (from brainstorming)

1. **Pricing structure:** admin sets a **buy price in USD** plus a **configurable
   sell discount %**. Sell price = buy × (1 − discount). Typical values the admin
   would enter: $0.02 buy, 5% discount (these are example inputs, not an auto-seeded
   default — see Migration). The buy/sell spread is the company margin and comes
   entirely from these two knobs — *not* from FX bid/ask.
2. **FX source & behaviour:** reuse the existing P2P NGN/USD feed exactly as P2P
   assets do — including its caching and staleness policy. If P2P would serve the
   rate, IT serves it; if P2P would refuse it, IT refuses. No separate IT fallback.
3. **Conversion timing:** Naira is computed on demand (display + intent creation),
   never stored on the rate row. Each intent still locks a fixed Naira amount at
   creation, so settlement/credit/reverse are unaffected.
4. **Approach A** (store USD inputs, derive Naira live) — chosen over a timed
   refresh job (B) and over dropping the admin input in favour of Contract B's
   on-chain quote (C, contradicts the requirement that the admin sets the price).

## Architecture

### Data model — `ITRate` (singleton, `it_rates` table)

Replace the two kobo columns with the admin's USD intent:

```go
type ITRate struct {
    ID                uuid.UUID
    BuyPriceUsdMicros int64       // USD per MCGP, 6dp micro-dollars; $0.02 = 20000
    SellDiscountBps   int         // discount on sell vs buy; 5% = 500; 0 <= bps < 10000
    Active            bool
    UpdatedBy         *uuid.UUID
    CreatedAt, UpdatedAt time.Time
}
```

- Units match existing codebase conventions: micro-dollars (6dp, as USDC / Contract B)
  and basis points (as fee config).
- Derived sell USD price = `BuyPriceUsdMicros × (10000 − SellDiscountBps) / 10000`.

### Pricing math

Naira is derived using the same NGN/USD value P2P float ads anchor on:

```
buyRateKobo  = buyPriceUsdMicros  × ngnPerUsd × 100 / 1e6
sellRateKobo = sellPriceUsdMicros × ngnPerUsd × 100 / 1e6
```

Rounding stays vault-favorable, matching today's "vault never over-issues / over-pays":
**buy rate rounds up, sell rate rounds down** (sub-kobo). These kobo values feed the
**unchanged** `mcgpForNgn` / `ngnForMcgp` helpers, so downstream MCGP/NGN math and
intent locking are untouched.

Worked example at FX = ₦1500/$: buy $0.02 → **₦30.00** (3000 kobo); sell at 5% →
$0.019 → **₦28.50** (2850 kobo).

### Backend — service & FX boundary (`tsa-api-go`)

Inject a small interface so `ITService` stays decoupled from P2P internals and
remains unit-testable:

```go
type FxProvider interface {
    // NgnPerUsd returns the same NGN/USD value P2P float ads anchor on, whether
    // it is being served stale, and an error if it is unavailable.
    NgnPerUsd(ctx context.Context) (rate *big.Rat, stale bool, err error)
}
```

An adapter over the existing `P2PService` NGN/USD snapshot implements `FxProvider`.
Wired where `NewITService(...)` is constructed in `cmd/server/main.go`.

New service method — the single place USD→kobo conversion happens:

```go
func (s *ITService) EffectiveKoboRates(ctx context.Context) (buyKobo, sellKobo *big.Int, fx FxMeta, err error)
```

1. Load active `ITRate` → `ErrITNoRate` if none.
2. `fx.NgnPerUsd(ctx)` → propagate unavailable as `ErrITFxUnavailable`; stale-but-served
   behaves exactly like P2P (served, flagged in `FxMeta`).
3. Compute `buyKobo` / `sellKobo` per the math above.

`FxMeta` carries `{ ngnPerUsd, stale }` for surfacing in the admin preview.

Call-site changes:
- `SetRate(ctx, adminID, buyPriceUsdMicros int64, sellDiscountBps int)` — validates
  `buyPriceUsdMicros > 0` and `0 <= sellDiscountBps < 10000`; deactivates prior rows,
  inserts the new active row (same singleton pattern as today).
- `CreateBuyIntent` / `CreateSellIntent` — call `EffectiveKoboRates` to obtain the live
  kobo rate, then lock it into the intent via the unchanged `mcgpForNgn` / `ngnForMcgp`.
  Settlement, admin credit, settle and reverse are untouched.

### API (`tsa-api-go`)

| Endpoint | Change |
|---|---|
| `GET /api/it/rate` (user) | Same JSON shape (`buyRateKobo`, `sellRateKobo`) — now computed live via `EffectiveKoboRates`. `404` when no active rate (unchanged); `503 "pricing temporarily unavailable"` when FX unavailable. |
| `POST /api/it/admin/rate` | Body becomes `{ buyPriceUsdMicros, sellDiscountBps }`. `400` on invalid input. |
| `GET /api/it/admin/rate` *(new)* | Returns raw config **and** a derived preview: `{ buyPriceUsdMicros, sellDiscountBps, derived: { buyRateKobo, sellRateKobo, ngnPerUsd, fxStale } }`. |

All admin routes keep `adminAuth` + `RequireP2PAdmin` (unchanged).

### Admin dashboard (`tsa-admin`)

The existing System → Instant Transfer page (shipped in PR #3) is reworked:

- `src/api/it-admin.ts`: `getITAdminRate()` → `GET /it/admin/rate`;
  `setITRate({ buyPriceUsdMicros, sellDiscountBps })` → `POST /it/admin/rate`.
- **Set-rate form:** two inputs — *Buy price (USD/MCGP)* (e.g. `0.02`) and
  *Sell discount (%)* (e.g. `5`). Frontend converts dollars→micros and percent→bps
  before submit (mirrors today's Naira→kobo conversion pattern).
- **Current-rate card:** shows the USD price + discount, the **derived ₦ buy/sell**
  and the **FX rate used** (from `derived`), with a subtle "FX stale" badge when
  `fxStale` is true.
- `src/pages/it-admin/it-admin-utils.ts`: add `usdToMicros` / `microsToUsd`,
  `percentToBps` / `bpsToPercent`; keep `koboToNaira` for rendering the derived
  Naira preview. Drop the now-unused `nairaToKobo` / `sellWithinBuy` (replaced by the
  USD validation).

### User app (`tsa-app`)

No functional change. `GET /it/rate` keeps returning `buyRateKobo` / `sellRateKobo`;
the buy/sell screens already render those. The only follow-up is to confirm the
existing error handling surfaces the new `503` as a "pricing temporarily unavailable"
state, and add a small guard only if it does not.

## Migration

The `it_rates` table swaps columns (`buy_rate_kobo` / `sell_rate_kobo` →
`buy_price_usd_micros` / `sell_discount_bps`). IT is a days-old dev feature with at
most one active row and no real money flow yet, so:

- Drop the old columns, add the new ones, **no data migration**.
- The admin re-sets the USD price once after deploy; until then `GET /it/rate`
  returns `404` ("no rate set") and the dashboard shows the empty state.

Follow the repo's existing migration mechanism (GORM `AutoMigrate` + any explicit
step in `internal/config/database.go`).

## Error handling

| Condition | Behaviour |
|---|---|
| No active rate | `404` on `GET /it/rate`; buy/sell abort with `ErrITNoRate` |
| FX unavailable | `503` on `GET /it/rate`; buy/sell abort with `ErrITFxUnavailable` |
| FX stale but within P2P window | Served, flagged `fxStale` (same as P2P assets) |
| Invalid SetRate input (`buy <= 0` or `discount` outside `[0,10000)`) | `400` |

## Testing

**Backend** (`it_service_test.go`):
- `EffectiveKoboRates` with a stub `FxProvider` (ngnPerUsd = 1500): buy $0.02 →
  3000 kobo; 5% discount → $0.019 → 2850 kobo.
- Edge cases: `discount = 0`; rounding direction (buy up / sell down);
  `ErrITNoRate`; `ErrITFxUnavailable` propagation.
- `SetRate` validation: reject `buy <= 0`, reject `discount >= 10000`.

**Frontend** (`it-admin-utils.test.ts`):
- `usdToMicros` / `microsToUsd`, `percentToBps` / `bpsToPercent` round-trips and
  edges (0 discount, sub-cent prices, rounding).

## Out of scope

- Intent moderation UI (credit/settle/reverse) — still deferred pending a backend
  list endpoint.
- Auto-syncing the IT USD price to Contract B's on-chain quote — the admin sets it
  by hand, by design.
- Any change to how NGN funds land or are swept (no sweep logic today; unchanged).
