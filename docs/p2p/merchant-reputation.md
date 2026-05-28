# P2P Merchant Reputation & Trust

A per-merchant reputation system surfaced on ad rows, ad detail, and a seller profile, plus a live payment-window countdown and a post-trade rating prompt. **Advisory only** — reputation never gates money movement; all stat writes are best-effort and the nightly reconcile is the correctness backstop.

## Data model
- **`p2p_merchant_stats`** (one row per user; `TableName() = "p2p_merchant_stats"`): `released_count`, `cancelled_count` (lifetime, self-attributable), `avg_release_secs_30d`, `avg_pay_secs_30d` (nullable, job-computed), `thumbs_up`, `thumbs_down`, `last_seen_at`, `verified`, `trusted`, `trusted_manual`.
- **`p2p_ratings`** (`TableName() = "p2p_ratings"`): one 👍/👎 per `(trade_id, rater_id)` (UNIQUE), `value ∈ {-1, 1}`, allowed only after a trade is `released`, immutable.

> The explicit `TableName()` pins are required: this app opens GORM with a plain config whose default naming would produce `p2_p_*`; every P2P model pins `p2p_*` and the raw constraint/reconcile SQL depends on it.

## Service: `MerchantStatsService` (`internal/services/p2p_merchant_stats_service.go`)
- Subscribes to the event bus for `P2PTradeReleased`, `P2PTradeCancelled`, `P2PTradeResolved`. These events are emitted to **both** trade parties, so `handleEvent` **dedups** by processing only the seller-addressed copy (`ev.UserID == trade.SellerID`) — otherwise counts would double.
- `ApplyReleased` credits **both** parties a completed order. `ApplyCancelled` attributes a payment-timeout cancel to the **buyer** (no-show), not the merchant. `ApplyResolved` penalizes the dispute **loser** (seller if released-to-buyer, else buyer).
- `RecordRating` bumps the ratee's thumbs. `Touch` updates `last_seen_at` (presence). `GetStats(ids)` is a batched read for ad-list decoration; `GetProfile(id)` returns stats + recent ratings.
- **Trusted** is derived: `verified && released_count ≥ N && completionRate ≥ X && accountAge ≥ D`, or a manual admin grant. Thresholds are config (env `P2P_TRUSTED_MIN_RELEASED`=50, `P2P_TRUSTED_MIN_COMPLETION`=0.95, `P2P_TRUSTED_MIN_AGE_DAYS`=30). **Verified** mirrors `VerificationStatus == "verified" && role == merchant`.
- **Computed on read** (pure helpers in `p2p_reputation_compute.go`): `completionRate = released/(released+cancelled)` (null when none), `positivePct = up/(up+down)` (null when none), `online = last_seen within 5m`, `totalOrders = released_count`, `isNew = (released+cancelled)==0`.

## Nightly reconcile (`internal/jobs/merchant_reputation_reconciler.go`, 24h)
`ReconcileAllMerchants` recomputes `released_count` (both roles) and the 30-day avg release/pay times from the source-of-truth `p2p_trades`, refreshes `verified`/`trusted`, and self-heals counter drift. Date-diff SQL is dialect-aware (SQLite `julianday` in tests, PostgreSQL `EXTRACT(EPOCH …)` in prod). Idempotent.

## API
- **Ad list / detail** decoration adds `sellerStats` (`{completionRate, avgReleaseSecs, totalOrders, positivePct, online, verified, trusted, isNew}`) via a batched `GetStats` — non-fatal (the list never 5xxs if stats fail), and contains **no account data**.
- **`GET /api/p2p/sellers/:id`** → `{ userId, stats, recentRatings, activeAds }`.
- **`POST /api/p2p/trades/:id/rate`** `{ value: 1 | -1 }` → 201; `400` invalid value, `403` not a trade party, `404` trade not found, `422` trade not released, `409` already rated.
- **Presence**: `require()` (the P2P auth helper) fires a best-effort `Touch` goroutine on every authenticated P2P request, so `online` reflects recent activity.

## Frontend (`tsa-app`)
- `services/p2p.ts`: `SellerStats`, `P2PRating`, `MerchantProfile` types; `sellersApi.profile(id)`; `ratingsApi.rate(tradeId, value)`; `P2PAd.sellerStats`.
- `components/p2p/SellerTrustBadges.tsx`: Verified/Trusted badges, completion %, avg release time, order count, 👍 % (MaterialIcons), online dot, or a "New" chip. Rendered on the ad row (`app/p2p/index.tsx`) and ad detail (`app/p2p/ad/[adId].tsx`), each linking to the seller profile.
- `app/p2p/seller/[sellerId].tsx`: profile screen (badges, recent feedback, active ads).
- `app/p2p/trade/[tradeId].tsx`: a live payment-window **countdown** (`hooks/useCountdown.ts`) and a post-`released` 👍/👎 rating prompt (409 treated as already-rated). `app/p2p/my-trades.tsx` shows a "rate this trade" nudge on released trades.

## Known limitations / future
- `cancelled_count` is maintained by the (deduped, correct) incremental path; the nightly reconcile currently backstops `released_count` + avgs but not `cancelled_count`. A dropped event (rare, only on bus-buffer overflow) could under-count cancels until a future reconcile extension.
- The my-trades rating nudge is not persisted per-trade, so it can reappear across sessions for an already-rated trade (the detail screen's `rated` state is ephemeral). Phase-2: persist or derive a `userRating` on the trade.
- Mobile **push** for trade events is a separate (app-wide) spec; today events flow to in-app/WebSocket + email.
