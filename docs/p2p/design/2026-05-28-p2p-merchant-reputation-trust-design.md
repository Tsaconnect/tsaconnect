# P2P Merchant Reputation & Trust — Design

**Date:** 2026-05-28
**Phase:** P2P Phase 0 (UX/trust layer; no escrow-contract change). See `2026-05-28-p2p-master-roadmap.md`.
**Scope:** A per-merchant reputation system — derived stats (completion rate, avg release time, order count) + 👍/👎 ratings, Verified + Trusted badges, online/last-seen presence — surfaced on ad rows, ad detail, and a seller profile screen, plus a live countdown timer in the trade UI.

**Out of scope (sibling/later specs):** Expo mobile push (its own next spec — app-wide infra wired to the same trade events); Express auto-match; merchant collateral/security-deposit (escrow-v2); escrow contract changes. This spec changes **no smart contract**.

---

## Overview

The trade state machine and event bus already exist: `P2PTrade` ([models/p2p_trade.go](tsa-dev/tsa-api-go/internal/models/p2p_trade.go)) has explicit transition validation (`P2PTradeValidNextStatuses`) across `pending_payment → paid → releasing → released | cancelled | disputed → resolved`, with timestamps `CreatedAt`, `MarkedPaidAt`, `ReleasedAt`, `ExpiresAt`. Trade transitions already publish events consumed by the notification service. **There is no reputation data today** — ad rows surface only `sellerUsername` + `paymentMethodSummary`.

This spec adds a **materialized read model** (`p2p_merchant_stats`, one row per user) that every ad row / ad detail / seller profile reads from cheaply (batched, like the existing `LookupUsernames`). It is kept fresh by:
- **Event-driven incremental updates** (subscribe to the existing bus on trade-terminal events + on each rating) for cheap, always-fresh metrics (counts, thumbs, last-seen).
- A **nightly reconcile job** that recomputes the 30-day windowed metrics (avg release/pay time) from the source-of-truth `p2p_trades`/`p2p_ratings`, and self-heals the lifetime counters against drift.

Reputation is **advisory** — it never gates money movement, so eventual consistency is fine.

### Locked decisions
1. **Cancel attribution:** a cancel increments `cancelled_count` only for the **at-fault** party. A payment-window-timeout cancel (`pending_payment → cancelled`, buyer never paid) is attributed to the **buyer**, never the merchant. A dispute `resolved` against a party attributes to that party.
2. **Trusted threshold:** `verified && released_count ≥ 50 && completionRate ≥ 0.95 && accountAge ≥ 30d`, **or** a manual admin grant. Thresholds are config (env), defaults as stated.
3. **Stat windows:** `totalOrders` and `completionRate` are **lifetime**; `avgReleaseSecs` is **30-day** (recency matters for responsiveness).

---

## Data model (GORM `AutoMigrate`, 2 new tables)

### `p2p_merchant_stats` — one row per user (PK = user_id)
| Column | Type | Notes |
|---|---|---|
| `user_id` | uuid PK | the merchant/user |
| `released_count` | bigint, default 0 | lifetime completed (as either party) |
| `cancelled_count` | bigint, default 0 | lifetime **self-attributable** cancels |
| `avg_release_secs_30d` | integer, nullable | seller responsiveness, job-computed (`released_at − marked_paid_at`) over last 30d as seller |
| `avg_pay_secs_30d` | integer, nullable | buyer pay latency, job-computed (`marked_paid_at − created_at`) over last 30d as buyer |
| `thumbs_up` | bigint, default 0 | |
| `thumbs_down` | bigint, default 0 | |
| `last_seen_at` | timestamptz, nullable | updated on WS connect + P2P activity |
| `verified` | bool, default false | mirrors KYC + merchant role |
| `trusted` | bool, default false | threshold-derived |
| `trusted_manual` | bool, default false | admin override; forces trusted=true |
| `updated_at` | timestamptz | |

**Computed on read (not stored):** `completionRate = released_count / (released_count + cancelled_count)` (null when denominator 0 → "New"); `positivePct = thumbs_up / (thumbs_up + thumbs_down)` (null when 0); `totalOrders = released_count`; `online = now − last_seen_at < 5m`.

### `p2p_ratings` — thumbs, one per (trade, rater)
| Column | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `trade_id` | uuid, index | |
| `rater_id` | uuid, index | the party leaving feedback |
| `ratee_id` | uuid, index | the counterparty being rated |
| `value` | smallint | +1 or −1 |
| `created_at` | timestamptz | |

CHECK `value IN (-1, 1)`; UNIQUE `(trade_id, rater_id)` (one rating per side per trade; immutable in v1). Added post-AutoMigrate via `DB.Exec` (constraint pattern already used in `config/database.go`).

---

## `MerchantStatsService` (owns all stats logic)
New `internal/services/p2p_merchant_stats_service.go`. Single responsibility: maintain + serve the stats read model.

- `RecordTradeTerminal(ctx, trade)` — on `released`: **both parties** `released_count++` (completion rate spans roles), plus record the seller's release time (`released_at − marked_paid_at`) and buyer's pay time (`marked_paid_at − created_at`) for the nightly avg job; on `cancelled`: at-fault party's `cancelled_count++` (timeout ⇒ buyer); on `resolved`: attribute per admin outcome. Then `EvaluateTrusted` for affected users. Idempotent per trade-terminal-event (guard against double-count using the trade's terminal state).
- `RecordRating(ctx, rating)` — `ratee.thumbs_up/down++`.
- `Touch(ctx, userID)` — upsert `last_seen_at = now`.
- `GetStats(ctx, userIDs[]) → map[userID]SellerStats` — batched read for ad-list decoration.
- `GetProfile(ctx, userID) → MerchantProfile` — stats + recent ratings + active ads.
- `EvaluateTrusted(ctx, userID)` — set `trusted` from thresholds OR `trusted_manual`.
- `ReconcileWindowed(ctx)` — nightly: recompute `avg_release_secs_30d` / `avg_pay_secs_30d` from `p2p_trades`; recompute lifetime `released_count`/`cancelled_count` from trades (self-heal); recompute thumbs from `p2p_ratings`. Idempotent.

**Wiring:** subscribe to the existing event bus for `P2PTradeReleased` and `P2PTradeCancelled` (same bus the notification service uses). For `resolved`, the admin-resolve confirm handler calls `RecordTradeTerminal` directly (resolution direction is known there). All updates are best-effort + logged; failure never blocks the trade flow.

**Presence:** reuse the existing notifications WebSocket hub — call `Touch` on WS connect and on authenticated P2P requests; `online` derived on read from `last_seen_at` (optionally short-circuited by the hub's live connection set).

**Trusted thresholds (config/env):** `P2P_TRUSTED_MIN_RELEASED=50`, `P2P_TRUSTED_MIN_COMPLETION=0.95`, `P2P_TRUSTED_MIN_AGE_DAYS=30`.

---

## API
- **Ad-list decoration:** extend `adListItem` ([p2p_handler.go](tsa-dev/tsa-api-go/internal/handlers/p2p_handler.go)) with `sellerStats: { completionRate, avgReleaseSecs, totalOrders, positivePct, online, verified, trusted, isNew }`, populated by a batched `MerchantStatsService.GetStats(sellerIDs)` inside `decorateAdsWithEffectiveRate` (alongside `LookupUsernames`). Missing row ⇒ `isNew:true`, other fields null — **non-fatal**, never 5xx. The ad-detail (`GetAd`) gets the same block.
- `GET /api/p2p/sellers/:id` → `{ stats, recentRatings[], activeAds[] }` for the seller profile screen.
- `POST /api/p2p/trades/:id/rate` `{ value: 1 | -1 }` → 201; validates caller is a trade party, trade is `released`, and no existing rating from this rater (409 on dup). Idempotent via the unique constraint. Registered in the `p2pGroup` block.

---

## Frontend (tsa-app)
- **`services/p2p.ts`:** add `SellerStats` + `MerchantProfile` + `P2PRating` types; `sellersApi.profile(id)`; `ratingsApi.rate(tradeId, value)`; extend `P2PAd` with `sellerStats?: SellerStats`.
- **Trust block component** `components/p2p/SellerTrustBadges.tsx` — renders Verified ✓ / Trusted ⭐ badges, completion %, avg release time ("releases in ~3m"), order count, positive %, online dot, or a "New" chip when `isNew`. Used on the ad row (`app/p2p/index.tsx` `AdCard`) and ad detail (`app/p2p/ad/[adId].tsx`).
- **Seller profile screen** `app/p2p/seller/[sellerId].tsx` — avatar + badges + stats + recent thumbs + the seller's active ads (tap an ad → ad detail). Linked from ad rows / ad detail seller name.
- **Rating prompt** in `app/p2p/trade/[tradeId].tsx` once status is `released` (👍/👎 → `ratingsApi.rate`), plus a subtle "rate this trade" nudge on done-but-unrated trades in `app/p2p/my-trades.tsx`.
- **Live countdown** — `hooks/useCountdown.ts` (pure, testable): given `expiresAt`, returns ticking `mm:ss` + expired flag; replaces the static "Xm remaining" text in `trade/[tradeId].tsx` for the `pending_payment` window.

---

## Data flow
1. Trade → `released` → event bus → `RecordTradeTerminal` increments **both parties'** `released_count` (records seller release time + buyer pay time) → next ad-list read shows fresh completion rate / order count.
2. User taps 👍/👎 on a released trade → `POST /trades/:id/rate` → `RecordRating` bumps ratee thumbs → positive %.
3. Nightly `ReconcileWindowed` recomputes 30-day avg times + self-heals counters from `p2p_trades`/`p2p_ratings`.
4. WS connect / P2P activity → `Touch` → `last_seen_at` → `online` on read.

## Error handling
- Stats decoration failure: log + render the ad without stats (graceful degrade), never 5xx the list.
- Missing stats row: treat as new merchant (`isNew`, zeros).
- Rating: 409 on duplicate; 403 if not a trade party; 422 if trade not `released`.
- `RecordTradeTerminal` guards against double-counting (keyed on the trade reaching a terminal state once).
- Nightly job idempotent + safe to re-run; it is the correctness backstop for the incremental path.

## Testing
- **Unit (Go):** `completionRate` incl. buyer-timeout exclusion; `avgReleaseSecs`/`avgPaySecs`; trusted-threshold evaluation (+ manual override); `positivePct`; online window. Cancel-attribution table-test (timeout ⇒ buyer).
- **Service (Go, SQLite rig):** `RecordTradeTerminal` updates correct user/role per outcome; idempotent on re-fire; `RecordRating`; dup-rating rejected; batched `GetStats`; `ReconcileWindowed` output matches incremental within tolerance (reconciliation test).
- **Handler (Go):** `POST /trades/:id/rate` happy + 409 dup + 403 non-party + 422 not-released; ad-list response includes `sellerStats` and never fails when stats are absent.
- **Frontend (Jest):** `useCountdown` pure ticking + expiry; `SellerTrustBadges` rendering incl. `isNew`; rating prompt gating (only after `released`, once).
- **Manual:** complete a trade → stats appear on the ad row + seller profile; leave a thumb → positive % updates; open a pending trade → countdown ticks.

## Build sequence
1. Models + migration (`p2p_merchant_stats`, `p2p_ratings`) + constraints.
2. `MerchantStatsService` (records, getstats, evaluate-trusted) + unit tests.
3. Event-bus subscription + resolve-path hook (incremental updates).
4. Nightly `ReconcileWindowed` job.
5. Ad-list/detail `sellerStats` decoration + `GET /sellers/:id` + `POST /trades/:id/rate`.
6. Frontend: `services/p2p.ts` types/api → `useCountdown` → `SellerTrustBadges` → ad row/detail wiring → seller profile screen → rating prompt.
7. Tests + manual pass.
