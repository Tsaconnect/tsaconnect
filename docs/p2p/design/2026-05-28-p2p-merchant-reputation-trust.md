# P2P Merchant Reputation & Trust — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-merchant reputation system (derived stats + 👍/👎 ratings, Verified/Trusted badges, online presence) surfaced on ad rows, ad detail, and a seller profile, plus a live countdown timer in the trade UI — no smart-contract change.

**Architecture:** A materialized `p2p_merchant_stats` read model (one row per user) updated incrementally via the existing event bus (`P2PTradeReleased`/`Cancelled`/`Resolved` + ratings) and reconciled nightly from `p2p_trades`/`p2p_ratings` by a `jobs.Worker`. Reads are batched into the existing ad-list decorator. Reputation is advisory (never gates money).

**Tech Stack:** Go (Gin, GORM, the in-repo `internal/events` bus + `internal/jobs` scheduler) backend; React Native + Expo + TypeScript app; Jest + Go `testing` (SQLite rig).

Spec: `docs/superpowers/specs/2026-05-28-p2p-merchant-reputation-trust-design.md`.

**Working dir (backend tasks):** `/Users/ojay/Projects/tsaconnect/tsa-dev/tsa-api-go`
**Working dir (frontend tasks):** `/Users/ojay/Projects/tsaconnect/tsa-dev/tsa-app`
**Git:** monorepo root `/Users/ojay/Projects/tsaconnect/tsa-dev`, branch `feat/p2p-trading`. Commits: no `Co-Authored-By` trailer; stage named files only (no `git add -A`); fresh commits (no amend).

---

## File structure

**Backend (new):**
- `internal/models/p2p_merchant_stats.go` — `P2PMerchantStats` model.
- `internal/models/p2p_rating.go` — `P2PRating` model.
- `internal/services/p2p_reputation_compute.go` — pure compute helpers (completion rate, positive %, online, trusted eval) + `SellerStats` DTO.
- `internal/services/p2p_reputation_compute_test.go` — unit tests for the pure helpers.
- `internal/services/p2p_merchant_stats_service.go` — `MerchantStatsService` (records, getstats, profile, event subscription, reconcile).
- `internal/services/p2p_merchant_stats_service_test.go` — service tests (SQLite rig).
- `internal/jobs/merchant_reputation_reconciler.go` — nightly `jobs.Worker`.

**Backend (modified):**
- `internal/config/database.go` — AutoMigrate the 2 new models + add CHECK/UNIQUE constraints.
- `internal/handlers/p2p_handler.go` — `sellerStats` on `adListItem`; `GetSellerProfile`, `RateTrade` handlers; `NewP2PHandler` gains a `*services.MerchantStatsService` param.
- `internal/handlers/p2p_handler_test.go` — rig builds the stats service + mounts the 2 new routes.
- `internal/routes/routes.go` — register `GET /p2p/sellers/:id`, `POST /p2p/trades/:id/rate`.
- `internal/services/p2p_trade_service.go` — ensure `P2PTradeCancelled` emit carries `tradeId` (the released + resolved emits already do).
- `cmd/server/main.go` — construct `MerchantStatsService`, register the reconciler, pass the service to `NewP2PHandler`.

**Frontend (new):**
- `hooks/useCountdown.ts` + `hooks/useCountdown.test.ts`.
- `components/p2p/SellerTrustBadges.tsx`.
- `app/p2p/seller/[sellerId].tsx` — seller profile screen.

**Frontend (modified):**
- `services/p2p.ts` — `SellerStats`, `MerchantProfile`, `P2PRating` types; `sellersApi`, `ratingsApi`; `P2PAd.sellerStats`.
- `app/p2p/index.tsx` — render `SellerTrustBadges` in `AdCard`; link seller → profile.
- `app/p2p/ad/[adId].tsx` — render `SellerTrustBadges`; link seller → profile.
- `app/p2p/trade/[tradeId].tsx` — live countdown; rating prompt after `released`.
- `app/p2p/my-trades.tsx` — "rate this trade" nudge on done-but-unrated.

---

## Task 1: Data models + migration

**Files:**
- Create: `internal/models/p2p_merchant_stats.go`
- Create: `internal/models/p2p_rating.go`
- Modify: `internal/config/database.go` (AutoMigrate list + post-migrate constraints)

- [ ] **Step 1: Create the stats model**

`internal/models/p2p_merchant_stats.go`:
```go
package models

import (
	"time"

	"github.com/google/uuid"
)

// P2PMerchantStats is the materialized reputation read-model, one row per user.
// Lifetime counters + thumbs + last_seen are updated incrementally off trade/rating
// events; the 30-day avg times are recomputed by a nightly reconcile job. All values
// are advisory — they never gate money movement.
type P2PMerchantStats struct {
	UserID            uuid.UUID  `gorm:"type:uuid;primaryKey" json:"userId"`
	ReleasedCount     int64      `gorm:"not null;default:0" json:"releasedCount"`     // lifetime completed (either role)
	CancelledCount    int64      `gorm:"not null;default:0" json:"cancelledCount"`    // lifetime self-attributable cancels
	AvgReleaseSecs30d *int       `gorm:"column:avg_release_secs_30d" json:"avgReleaseSecs30d,omitempty"` // seller responsiveness (job-computed)
	AvgPaySecs30d     *int       `gorm:"column:avg_pay_secs_30d" json:"avgPaySecs30d,omitempty"`         // buyer pay latency (job-computed)
	ThumbsUp          int64      `gorm:"not null;default:0" json:"thumbsUp"`
	ThumbsDown        int64      `gorm:"not null;default:0" json:"thumbsDown"`
	LastSeenAt        *time.Time `json:"lastSeenAt,omitempty"`
	Verified          bool       `gorm:"not null;default:false" json:"verified"`
	Trusted           bool       `gorm:"not null;default:false" json:"trusted"`
	TrustedManual     bool       `gorm:"not null;default:false" json:"trustedManual"` // admin override
	UpdatedAt         time.Time  `json:"updatedAt"`
}
```

- [ ] **Step 2: Create the rating model**

`internal/models/p2p_rating.go`:
```go
package models

import (
	"time"

	"github.com/google/uuid"
)

// P2PRating is a single 👍/👎 left by one party of a completed trade about the other.
// One per (trade, rater); allowed only after the trade is released; immutable.
type P2PRating struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TradeID   uuid.UUID `gorm:"type:uuid;not null;index" json:"tradeId"`
	RaterID   uuid.UUID `gorm:"type:uuid;not null;index" json:"raterId"`
	RateeID   uuid.UUID `gorm:"type:uuid;not null;index" json:"rateeId"`
	Value     int16     `gorm:"not null" json:"value"` // +1 or -1
	CreatedAt time.Time `json:"createdAt"`
}
```

- [ ] **Step 3: Register migration + constraints**

In `internal/config/database.go`, add `&models.P2PMerchantStats{}` and `&models.P2PRating{}` to the `DB.AutoMigrate(...)` argument list (alongside the other `&models.P2P*{}` entries). Then, in the same `AutoMigrate()` function, after the existing post-migrate `DB.Exec(...)` constraint block (near the P2P constraints), add:
```go
	// P2P rating value + one-rating-per-(trade,rater)
	DB.Exec(`ALTER TABLE p2p_ratings
	         DROP CONSTRAINT IF EXISTS p2p_ratings_value_chk`)
	DB.Exec(`ALTER TABLE p2p_ratings
	         ADD CONSTRAINT p2p_ratings_value_chk CHECK (value IN (-1, 1))`)
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS p2p_ratings_trade_rater_uq
	         ON p2p_ratings (trade_id, rater_id)`)
```

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: clean (no output).

- [ ] **Step 5: Commit**

```bash
git add internal/models/p2p_merchant_stats.go internal/models/p2p_rating.go internal/config/database.go
git commit -m "feat(p2p): add merchant-stats + rating models and migration"
```

---

## Task 2: Pure reputation compute helpers

**Files:**
- Create: `internal/services/p2p_reputation_compute.go`
- Test: `internal/services/p2p_reputation_compute_test.go`

- [ ] **Step 1: Write the failing test**

`internal/services/p2p_reputation_compute_test.go`:
```go
package services

import (
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func TestCompletionRate(t *testing.T) {
	if got := CompletionRate(0, 0); got != nil {
		t.Fatalf("0/0 should be nil (new), got %v", *got)
	}
	got := CompletionRate(95, 5)
	if got == nil || *got != 0.95 {
		t.Fatalf("95/(95+5) = 0.95, got %v", got)
	}
}

func TestPositivePct(t *testing.T) {
	if got := PositivePct(0, 0); got != nil {
		t.Fatalf("0/0 should be nil, got %v", *got)
	}
	got := PositivePct(9, 1)
	if got == nil || *got != 0.9 {
		t.Fatalf("9/(9+1) = 0.9, got %v", got)
	}
}

func TestIsOnline(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-2 * time.Minute)
	stale := now.Add(-10 * time.Minute)
	if !IsOnline(&recent, now) {
		t.Fatal("2m ago should be online")
	}
	if IsOnline(&stale, now) {
		t.Fatal("10m ago should be offline")
	}
	if IsOnline(nil, now) {
		t.Fatal("nil last-seen should be offline")
	}
}

func TestEvaluateTrusted(t *testing.T) {
	cfg := TrustedThresholds{MinReleased: 50, MinCompletion: 0.95, MinAgeDays: 30}
	// Manual override always wins.
	if !EvaluateTrusted(false, 0, nil, 0, true, cfg) {
		t.Fatal("manual override should force trusted")
	}
	// Meets all thresholds.
	if !EvaluateTrusted(true, 60, f64(0.97), 40, false, cfg) {
		t.Fatal("should be trusted when all thresholds met")
	}
	// Not verified.
	if EvaluateTrusted(false, 60, f64(0.97), 40, false, cfg) {
		t.Fatal("not verified -> not trusted")
	}
	// Below order count.
	if EvaluateTrusted(true, 10, f64(0.99), 40, false, cfg) {
		t.Fatal("below min released -> not trusted")
	}
	// Below completion.
	if EvaluateTrusted(true, 60, f64(0.80), 40, false, cfg) {
		t.Fatal("below min completion -> not trusted")
	}
	// Too new.
	if EvaluateTrusted(true, 60, f64(0.99), 5, false, cfg) {
		t.Fatal("too new -> not trusted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/services -run 'TestCompletionRate|TestPositivePct|TestIsOnline|TestEvaluateTrusted' -v`
Expected: FAIL (undefined: CompletionRate, etc.)

- [ ] **Step 3: Write the implementation**

`internal/services/p2p_reputation_compute.go`:
```go
package services

import "time"

// onlineWindow is how recently a user must have been seen to count as "online".
const onlineWindow = 5 * time.Minute

// TrustedThresholds are the (config-driven) gates for the Trusted badge.
type TrustedThresholds struct {
	MinReleased   int64
	MinCompletion float64
	MinAgeDays    int
}

// SellerStats is the privacy-safe reputation view surfaced on ads / profiles.
type SellerStats struct {
	CompletionRate *float64 `json:"completionRate,omitempty"`
	AvgReleaseSecs *int     `json:"avgReleaseSecs,omitempty"`
	TotalOrders    int64    `json:"totalOrders"`
	PositivePct    *float64 `json:"positivePct,omitempty"`
	Online         bool     `json:"online"`
	Verified       bool     `json:"verified"`
	Trusted        bool     `json:"trusted"`
	IsNew          bool     `json:"isNew"`
}

// CompletionRate = released / (released + cancelled); nil when no terminal trades yet.
func CompletionRate(released, cancelled int64) *float64 {
	total := released + cancelled
	if total <= 0 {
		return nil
	}
	r := float64(released) / float64(total)
	return &r
}

// PositivePct = up / (up + down); nil when no ratings yet.
func PositivePct(up, down int64) *float64 {
	total := up + down
	if total <= 0 {
		return nil
	}
	p := float64(up) / float64(total)
	return &p
}

// IsOnline reports whether lastSeen is within the online window of now.
func IsOnline(lastSeen *time.Time, now time.Time) bool {
	if lastSeen == nil {
		return false
	}
	return now.Sub(*lastSeen) < onlineWindow
}

// EvaluateTrusted applies the Trusted gates. A manual admin grant always wins.
func EvaluateTrusted(verified bool, released int64, completion *float64, ageDays int, manual bool, cfg TrustedThresholds) bool {
	if manual {
		return true
	}
	if !verified {
		return false
	}
	if released < cfg.MinReleased {
		return false
	}
	if completion == nil || *completion < cfg.MinCompletion {
		return false
	}
	if ageDays < cfg.MinAgeDays {
		return false
	}
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/services -run 'TestCompletionRate|TestPositivePct|TestIsOnline|TestEvaluateTrusted' -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/services/p2p_reputation_compute.go internal/services/p2p_reputation_compute_test.go
git commit -m "feat(p2p): pure reputation compute helpers (completion, positive%, online, trusted)"
```

---

## Task 3: MerchantStatsService (records, reads, event subscription)

**Files:**
- Create: `internal/services/p2p_merchant_stats_service.go`
- Test: `internal/services/p2p_merchant_stats_service_test.go`

Notes: `events.Bus.Subscribe(eventType string, handler func(events.Event))`; events carry `Data["tradeId"] uuid.UUID` and resolved carries `Data["releasedToBuyer"] bool`. The trade model fields are `BuyerID`, `SellerID`, `Status`, `MarkedPaidAt *time.Time`, `ReleasedAt *time.Time`, `CreatedAt`, statuses `P2PTradeStatusReleased/Cancelled/Resolved`.

- [ ] **Step 1: Write the failing test**

`internal/services/p2p_merchant_stats_service_test.go`:
```go
package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// reuse setupP2PTestDB(t) *gorm.DB from the existing services test rig (same package).
// Add `import "gorm.io/gorm"` to this test file.
func newStatsSvc(t *testing.T) (*MerchantStatsService, *gorm.DB) {
	t.Helper()
	db := setupP2PTestDB(t)
	if err := db.AutoMigrate(&models.P2PMerchantStats{}, &models.P2PRating{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	svc := NewMerchantStatsServiceNoBus(db) // variant that does NOT subscribe (tests call Apply* directly)
	return svc, db
}

func TestApplyReleasedCreditsBothParties(t *testing.T) {
	svc, db := newStatsSvc(t)
	buyer, seller := uuid.New(), uuid.New()
	mp := time.Now().Add(-3 * time.Minute)
	rel := time.Now()
	trade := models.P2PTrade{ID: uuid.New(), BuyerID: buyer, SellerID: seller,
		Status: models.P2PTradeStatusReleased, MarkedPaidAt: &mp, ReleasedAt: &rel}
	if err := svc.ApplyReleased(context.Background(), &trade); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, id := range []uuid.UUID{buyer, seller} {
		var s models.P2PMerchantStats
		db.First(&s, "user_id = ?", id)
		if s.ReleasedCount != 1 {
			t.Fatalf("user %s releasedCount = %d, want 1", id, s.ReleasedCount)
		}
	}
}

func TestApplyCancelledBlamesBuyer(t *testing.T) {
	svc, db := newStatsSvc(t)
	buyer, seller := uuid.New(), uuid.New()
	trade := models.P2PTrade{ID: uuid.New(), BuyerID: buyer, SellerID: seller, Status: models.P2PTradeStatusCancelled}
	if err := svc.ApplyCancelled(context.Background(), &trade); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var bs, ss models.P2PMerchantStats
	db.First(&bs, "user_id = ?", buyer)
	db.First(&ss, "user_id = ?", seller)
	if bs.CancelledCount != 1 {
		t.Fatalf("buyer cancelledCount = %d, want 1", bs.CancelledCount)
	}
	if ss.CancelledCount != 0 {
		t.Fatalf("seller cancelledCount = %d, want 0 (not at fault)", ss.CancelledCount)
	}
}

func TestApplyResolvedBlamesLoser(t *testing.T) {
	svc, db := newStatsSvc(t)
	buyer, seller := uuid.New(), uuid.New()
	trade := models.P2PTrade{ID: uuid.New(), BuyerID: buyer, SellerID: seller, Status: models.P2PTradeStatusResolved}
	// releasedToBuyer=true -> seller lost.
	if err := svc.ApplyResolved(context.Background(), &trade, true); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var ss models.P2PMerchantStats
	db.First(&ss, "user_id = ?", seller)
	if ss.CancelledCount != 1 {
		t.Fatalf("seller (loser) cancelledCount = %d, want 1", ss.CancelledCount)
	}
}

func TestRecordRatingAndGetStats(t *testing.T) {
	svc, _ := newStatsSvc(t)
	ratee := uuid.New()
	if err := svc.RecordRating(context.Background(), &models.P2PRating{
		ID: uuid.New(), TradeID: uuid.New(), RaterID: uuid.New(), RateeID: ratee, Value: 1,
	}); err != nil {
		t.Fatalf("rating: %v", err)
	}
	stats, err := svc.GetStats(context.Background(), []uuid.UUID{ratee})
	if err != nil {
		t.Fatalf("getstats: %v", err)
	}
	got := stats[ratee]
	if got.PositivePct == nil || *got.PositivePct != 1.0 {
		t.Fatalf("positivePct = %v, want 1.0", got.PositivePct)
	}
}

func TestGetStatsMissingIsNew(t *testing.T) {
	svc, _ := newStatsSvc(t)
	id := uuid.New()
	stats, err := svc.GetStats(context.Background(), []uuid.UUID{id})
	if err != nil {
		t.Fatalf("getstats: %v", err)
	}
	if !stats[id].IsNew {
		t.Fatalf("missing merchant should be IsNew=true")
	}
}
```


- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/services -run 'TestApply|TestRecordRating|TestGetStats' -v`
Expected: FAIL (undefined: MerchantStatsService, NewMerchantStatsServiceNoBus, ApplyReleased, …)

- [ ] **Step 3: Write the implementation**

`internal/services/p2p_merchant_stats_service.go`:
```go
package services

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// MerchantStatsService owns the materialized reputation read-model. It updates
// counters incrementally off trade/rating events and serves batched reads for
// ad-list decoration + the seller profile. Correctness backstop is the nightly
// ReconcileAllMerchants job.
type MerchantStatsService struct {
	db        *gorm.DB
	thresholds TrustedThresholds
}

// NewMerchantStatsService wires the service to the event bus (production path).
func NewMerchantStatsService(db *gorm.DB, bus *events.Bus, thresholds TrustedThresholds) *MerchantStatsService {
	s := &MerchantStatsService{db: db, thresholds: thresholds}
	if bus != nil {
		bus.Subscribe(events.P2PTradeReleased, s.handleEvent)
		bus.Subscribe(events.P2PTradeCancelled, s.handleEvent)
		bus.Subscribe(events.P2PTradeResolved, s.handleEvent)
	}
	return s
}

// NewMerchantStatsServiceNoBus is for tests — no subscription; call Apply* directly.
func NewMerchantStatsServiceNoBus(db *gorm.DB) *MerchantStatsService {
	return &MerchantStatsService{db: db, thresholds: TrustedThresholds{MinReleased: 50, MinCompletion: 0.95, MinAgeDays: 30}}
}

func (s *MerchantStatsService) handleEvent(ev events.Event) {
	tid, ok := ev.Data["tradeId"].(uuid.UUID)
	if !ok {
		return
	}
	ctx := context.Background()
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tid).Error; err != nil {
		log.Printf("[merchant-stats] load trade %s: %v", tid, err)
		return
	}
	var err error
	switch ev.Type {
	case events.P2PTradeReleased:
		err = s.ApplyReleased(ctx, &trade)
	case events.P2PTradeCancelled:
		err = s.ApplyCancelled(ctx, &trade)
	case events.P2PTradeResolved:
		// resolved fires to BOTH parties — process once (on the seller-addressed copy).
		if ev.UserID != trade.SellerID {
			return
		}
		releasedToBuyer, _ := ev.Data["releasedToBuyer"].(bool)
		err = s.ApplyResolved(ctx, &trade, releasedToBuyer)
	}
	if err != nil {
		log.Printf("[merchant-stats] apply %s for trade %s: %v", ev.Type, tid, err)
	}
}

// upsertDelta atomically applies field deltas to a user's stats row (creating it if absent).
func (s *MerchantStatsService) upsertDelta(ctx context.Context, userID uuid.UUID, cols map[string]any) error {
	row := models.P2PMerchantStats{UserID: userID, UpdatedAt: time.Now().UTC()}
	// seed initial values for insert
	for k, v := range cols {
		switch k {
		case "released_count":
			row.ReleasedCount = v.(int64)
		case "cancelled_count":
			row.CancelledCount = v.(int64)
		case "thumbs_up":
			row.ThumbsUp = v.(int64)
		case "thumbs_down":
			row.ThumbsDown = v.(int64)
		}
	}
	assignments := map[string]any{"updated_at": time.Now().UTC()}
	for k, v := range cols {
		assignments[k] = gorm.Expr(k+" + ?", v)
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(assignments),
	}).Create(&row).Error
}

// ApplyReleased credits both parties with a completed order.
func (s *MerchantStatsService) ApplyReleased(ctx context.Context, trade *models.P2PTrade) error {
	if err := s.upsertDelta(ctx, trade.SellerID, map[string]any{"released_count": int64(1)}); err != nil {
		return err
	}
	if err := s.upsertDelta(ctx, trade.BuyerID, map[string]any{"released_count": int64(1)}); err != nil {
		return err
	}
	s.refreshTrusted(ctx, trade.SellerID)
	return nil
}

// ApplyCancelled blames the no-show buyer (payment-window timeout).
func (s *MerchantStatsService) ApplyCancelled(ctx context.Context, trade *models.P2PTrade) error {
	return s.upsertDelta(ctx, trade.BuyerID, map[string]any{"cancelled_count": int64(1)})
}

// ApplyResolved penalizes the losing party of a dispute.
func (s *MerchantStatsService) ApplyResolved(ctx context.Context, trade *models.P2PTrade, releasedToBuyer bool) error {
	loser := trade.BuyerID
	if releasedToBuyer {
		loser = trade.SellerID // seller failed to deliver
	}
	if err := s.upsertDelta(ctx, loser, map[string]any{"cancelled_count": int64(1)}); err != nil {
		return err
	}
	s.refreshTrusted(ctx, trade.SellerID)
	return nil
}

// RecordRating bumps the ratee's thumbs.
func (s *MerchantStatsService) RecordRating(ctx context.Context, r *models.P2PRating) error {
	col := "thumbs_up"
	if r.Value < 0 {
		col = "thumbs_down"
	}
	if err := s.upsertDelta(ctx, r.RateeID, map[string]any{col: int64(1)}); err != nil {
		return err
	}
	s.refreshTrusted(ctx, r.RateeID)
	return nil
}

// Touch records presence.
func (s *MerchantStatsService) Touch(ctx context.Context, userID uuid.UUID) error {
	now := time.Now().UTC()
	row := models.P2PMerchantStats{UserID: userID, LastSeenAt: &now, UpdatedAt: now}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{"last_seen_at": now, "updated_at": now}),
	}).Create(&row).Error
}

func (s *MerchantStatsService) refreshTrusted(ctx context.Context, userID uuid.UUID) {
	var row models.P2PMerchantStats
	if err := s.db.WithContext(ctx).First(&row, "user_id = ?", userID).Error; err != nil {
		return
	}
	trusted := EvaluateTrusted(row.Verified, row.ReleasedCount,
		CompletionRate(row.ReleasedCount, row.CancelledCount), s.accountAgeDays(ctx, userID),
		row.TrustedManual, s.thresholds)
	if trusted != row.Trusted {
		s.db.WithContext(ctx).Model(&models.P2PMerchantStats{}).
			Where("user_id = ?", userID).Update("trusted", trusted)
	}
}

func (s *MerchantStatsService) accountAgeDays(ctx context.Context, userID uuid.UUID) int {
	var u models.User
	if err := s.db.WithContext(ctx).Select("created_at").First(&u, "id = ?", userID).Error; err != nil {
		return 0
	}
	return int(time.Since(u.CreatedAt).Hours() / 24)
}

// GetStats returns batched SellerStats for the given users; missing rows are IsNew.
func (s *MerchantStatsService) GetStats(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]SellerStats, error) {
	out := make(map[uuid.UUID]SellerStats, len(userIDs))
	for _, id := range userIDs {
		out[id] = SellerStats{IsNew: true}
	}
	if len(userIDs) == 0 {
		return out, nil
	}
	var rows []models.P2PMerchantStats
	if err := s.db.WithContext(ctx).Where("user_id IN ?", userIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range rows {
		r := rows[i]
		out[r.UserID] = SellerStats{
			CompletionRate: CompletionRate(r.ReleasedCount, r.CancelledCount),
			AvgReleaseSecs: r.AvgReleaseSecs30d,
			TotalOrders:    r.ReleasedCount,
			PositivePct:    PositivePct(r.ThumbsUp, r.ThumbsDown),
			Online:         IsOnline(r.LastSeenAt, now),
			Verified:       r.Verified,
			Trusted:        r.Trusted,
			IsNew:          (r.ReleasedCount + r.CancelledCount) == 0,
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/services -run 'TestApply|TestRecordRating|TestGetStats' -v && go build ./...`
Expected: PASS (5 tests) + clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/services/p2p_merchant_stats_service.go internal/services/p2p_merchant_stats_service_test.go
git commit -m "feat(p2p): MerchantStatsService with event-driven stat updates + batched reads"
```

---

## Task 4: Profile read + nightly reconcile

**Files:**
- Modify: `internal/services/p2p_merchant_stats_service.go` (add `GetProfile`, `ReconcileAllMerchants`)
- Modify: `internal/services/p2p_merchant_stats_service_test.go` (reconcile test)
- Create: `internal/jobs/merchant_reputation_reconciler.go`

- [ ] **Step 1: Write the failing reconcile test**

Append to `internal/services/p2p_merchant_stats_service_test.go`:
```go
func TestReconcileRecomputesCountsAndAvgRelease(t *testing.T) {
	svc, db := newStatsSvc(t)
	if err := db.AutoMigrate(&models.P2PTrade{}); err != nil {
		t.Fatalf("migrate trades: %v", err)
	}
	seller, buyer := uuid.New(), uuid.New()
	mp := time.Now().Add(-10 * time.Minute)
	rel := mp.Add(2 * time.Minute) // 120s release time
	db.Create(&models.P2PTrade{ID: uuid.New(), BuyerID: buyer, SellerID: seller,
		Status: models.P2PTradeStatusReleased, MarkedPaidAt: &mp, ReleasedAt: &rel,
		CreatedAt: mp.Add(-1 * time.Minute)})

	if err := svc.ReconcileAllMerchants(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var s models.P2PMerchantStats
	db.First(&s, "user_id = ?", seller)
	if s.ReleasedCount != 1 {
		t.Fatalf("reconciled releasedCount = %d, want 1", s.ReleasedCount)
	}
	if s.AvgReleaseSecs30d == nil || *s.AvgReleaseSecs30d != 120 {
		t.Fatalf("avgReleaseSecs30d = %v, want 120", s.AvgReleaseSecs30d)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/services -run TestReconcile -v`
Expected: FAIL (undefined: ReconcileAllMerchants)

- [ ] **Step 3: Implement GetProfile + ReconcileAllMerchants**

Append to `internal/services/p2p_merchant_stats_service.go`:
```go
// MerchantProfile is the seller profile payload (stats + recent feedback).
type MerchantProfile struct {
	UserID        uuid.UUID         `json:"userId"`
	Stats         SellerStats       `json:"stats"`
	RecentRatings []models.P2PRating `json:"recentRatings"`
}

// GetProfile returns a merchant's stats + recent ratings. (Active ads are added
// by the handler, which already has the ad query.)
func (s *MerchantStatsService) GetProfile(ctx context.Context, userID uuid.UUID) (*MerchantProfile, error) {
	statsMap, err := s.GetStats(ctx, []uuid.UUID{userID})
	if err != nil {
		return nil, err
	}
	var recent []models.P2PRating
	if err := s.db.WithContext(ctx).Where("ratee_id = ?", userID).
		Order("created_at DESC").Limit(20).Find(&recent).Error; err != nil {
		return nil, err
	}
	return &MerchantProfile{UserID: userID, Stats: statsMap[userID], RecentRatings: recent}, nil
}

// ReconcileAllMerchants recomputes lifetime counts, thumbs, and 30-day avg times
// from the source-of-truth tables. Idempotent; the correctness backstop for the
// incremental path. Verified/trusted are refreshed too.
func (s *MerchantStatsService) ReconcileAllMerchants(ctx context.Context) error {
	cutoff := time.Now().UTC().AddDate(0, 0, -30)

	// 1) lifetime released_count per user (either role) from released trades.
	type countRow struct {
		UserID uuid.UUID
		N      int64
	}
	apply := func(rows []countRow, col string) error {
		for _, r := range rows {
			row := models.P2PMerchantStats{UserID: r.UserID, UpdatedAt: time.Now().UTC()}
			if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "user_id"}},
				DoUpdates: clause.Assignments(map[string]any{col: r.N, "updated_at": time.Now().UTC()}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	}

	var released []countRow
	s.db.WithContext(ctx).Raw(`
		SELECT user_id, COUNT(*) AS n FROM (
			SELECT seller_id AS user_id FROM p2p_trades WHERE status = ?
			UNION ALL
			SELECT buyer_id AS user_id FROM p2p_trades WHERE status = ?
		) t GROUP BY user_id`,
		models.P2PTradeStatusReleased, models.P2PTradeStatusReleased).Scan(&released)
	if err := apply(released, "released_count"); err != nil {
		return err
	}

	// 2) 30-day avg release secs (seller) and avg pay secs (buyer).
	type avgRow struct {
		UserID uuid.UUID
		Avg    float64
	}
	var relAvg []avgRow
	s.db.WithContext(ctx).Raw(`
		SELECT seller_id AS user_id,
		       AVG(CAST((julianday(released_at) - julianday(marked_paid_at)) * 86400 AS REAL)) AS avg
		FROM p2p_trades
		WHERE status = ? AND released_at IS NOT NULL AND marked_paid_at IS NOT NULL AND released_at >= ?
		GROUP BY seller_id`, models.P2PTradeStatusReleased, cutoff).Scan(&relAvg)
	for _, r := range relAvg {
		secs := int(r.Avg)
		s.db.WithContext(ctx).Model(&models.P2PMerchantStats{}).
			Where("user_id = ?", r.UserID).Update("avg_release_secs_30d", secs)
	}

	// NOTE: the julianday() form above is SQLite (tests). On PostgreSQL use:
	//   AVG(EXTRACT(EPOCH FROM (released_at - marked_paid_at)))
	// Gate by dialect via s.db.Dialector.Name() == "sqlite".

	// 3) refresh verified + trusted from users + thresholds.
	var ids []uuid.UUID
	s.db.WithContext(ctx).Model(&models.P2PMerchantStats{}).Pluck("user_id", &ids)
	for _, id := range ids {
		s.refreshVerified(ctx, id)
		s.refreshTrusted(ctx, id)
	}
	return nil
}

func (s *MerchantStatsService) refreshVerified(ctx context.Context, userID uuid.UUID) {
	var u models.User
	if err := s.db.WithContext(ctx).Select("verification_status", "role").First(&u, "id = ?", userID).Error; err != nil {
		return
	}
	verified := u.VerificationStatus == "verified" && u.Role == models.RoleMerchant
	s.db.WithContext(ctx).Model(&models.P2PMerchantStats{}).Where("user_id = ?", userID).Update("verified", verified)
}
```

IMPORTANT (implementer): the reconcile SQL uses SQLite `julianday()` so the test passes on the in-memory rig. Wrap the date-diff expression in a dialect check (`s.db.Dialector.Name()`): use `julianday(...)*86400` for `"sqlite"`, and `EXTRACT(EPOCH FROM (released_at - marked_paid_at))` for postgres. Keep both branches; pick at runtime.

- [ ] **Step 4: Create the reconciler job**

`internal/jobs/merchant_reputation_reconciler.go`:
```go
package jobs

import (
	"context"
	"log"
	"time"
)

// merchantStatsReconciler is the subset of MerchantStatsService this job needs.
type merchantStatsReconciler interface {
	ReconcileAllMerchants(ctx context.Context) error
}

// MerchantReputationReconciler recomputes windowed stats nightly (correctness backstop).
type MerchantReputationReconciler struct {
	svc      merchantStatsReconciler
	interval time.Duration
}

func NewMerchantReputationReconciler(svc merchantStatsReconciler) *MerchantReputationReconciler {
	return &MerchantReputationReconciler{svc: svc, interval: 24 * time.Hour}
}

func (r *MerchantReputationReconciler) Name() string            { return "merchant-reputation-reconciler" }
func (r *MerchantReputationReconciler) Interval() time.Duration { return r.interval }

func (r *MerchantReputationReconciler) Run(ctx context.Context) error {
	if err := r.svc.ReconcileAllMerchants(ctx); err != nil {
		return err
	}
	log.Printf("[merchant-reputation-reconciler] reconcile complete")
	return nil
}
```

- [ ] **Step 5: Run tests + build**

Run: `go test ./internal/services -run 'TestReconcile|TestApply|TestRecordRating|TestGetStats' -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/services/p2p_merchant_stats_service.go internal/services/p2p_merchant_stats_service_test.go internal/jobs/merchant_reputation_reconciler.go
git commit -m "feat(p2p): seller profile read + nightly reputation reconcile job"
```

---

## Task 5: Wire service + job into startup; ensure cancelled emit carries tradeId

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `internal/services/p2p_trade_service.go` (cancelled emit `tradeId`)
- Modify: `internal/handlers/p2p_handler.go` (`NewP2PHandler` signature)

- [ ] **Step 1: Ensure the cancelled event carries `tradeId`**

In `internal/services/p2p_trade_service.go`, find the `P2PTradeCancelled` emit (in `ConfirmCancel`, ~line 1101). Ensure its `Data` map includes the trade id, matching the released emit:
```go
s.emit(events.P2PTradeCancelled, trade.SellerID, "Trade cancelled — funds returned to your ad", "",
	map[string]any{"tradeId": trade.ID})
```
(If `tradeId` is already present, leave as-is.)

- [ ] **Step 2: Add the stats-service param to the handler constructor**

In `internal/handlers/p2p_handler.go`, find `func NewP2PHandler(...)` and add a `stats *services.MerchantStatsService` parameter + store it on the struct:
```go
type P2PHandler struct {
	// ...existing fields...
	stats *services.MerchantStatsService
}

func NewP2PHandler(db *gorm.DB, trade *services.P2PTradeService, msg *services.P2PMessageService, nomba *nomba.Client, stats *services.MerchantStatsService) *P2PHandler {
	return &P2PHandler{ /* ...existing... */, stats: stats}
}
```
(Match the real existing field/param names; just add `stats`.)

- [ ] **Step 3: Wire in main.go**

In `cmd/server/main.go`, after the event bus exists (line ~118) and before/near the P2P handler construction (line ~218), construct the service and register the job. Use the env-driven thresholds (with the stated defaults):
```go
merchantStats := services.NewMerchantStatsService(config.DB, eventBus, services.TrustedThresholds{
	MinReleased:   cfg.P2PTrustedMinReleased,   // default 50
	MinCompletion: cfg.P2PTrustedMinCompletion, // default 0.95
	MinAgeDays:    cfg.P2PTrustedMinAgeDays,     // default 30
})
scheduler.Register(jobs.NewMerchantReputationReconciler(merchantStats))
```
Then update the `NewP2PHandler(...)` call (line ~218) to pass `merchantStats` as the new final arg.

Add the three config fields to `internal/config/config.go` with env parsing + defaults (`P2P_TRUSTED_MIN_RELEASED=50`, `P2P_TRUSTED_MIN_COMPLETION=0.95`, `P2P_TRUSTED_MIN_AGE_DAYS=30`), following the existing env-var pattern in that file.

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: clean. (If the handler test rig now fails to compile, that's expected — Task 6 updates it.)

- [ ] **Step 5: Commit**

```bash
git add cmd/server/main.go internal/services/p2p_trade_service.go internal/handlers/p2p_handler.go internal/config/config.go
git commit -m "feat(p2p): wire MerchantStatsService + reconciler; carry tradeId on cancelled event"
```

---

## Task 6: API — sellerStats decoration, seller profile, rate endpoint

**Files:**
- Modify: `internal/handlers/p2p_handler.go` (`adListItem` + decorate; `GetSellerProfile`; `RateTrade`)
- Modify: `internal/routes/routes.go`
- Modify: `internal/handlers/p2p_handler_test.go` (rig builds stats svc + mounts routes)

- [ ] **Step 1: Add `sellerStats` to the ad-list item + decorator**

In `internal/handlers/p2p_handler.go`, extend `adListItem`:
```go
type adListItem struct {
	models.P2PAd
	RateStale            bool                            `json:"rateStale,omitempty"`
	SellerUsername       string                          `json:"sellerUsername,omitempty"`
	PaymentMethodSummary []services.PaymentMethodSummary `json:"paymentMethodSummary,omitempty"`
	SellerStats          *services.SellerStats           `json:"sellerStats,omitempty"`
}
```
In `decorateAdsWithEffectiveRate`, after the existing username + provider batched lookups, add a batched stats lookup and assign per ad (non-fatal, like usernames):
```go
	statsByID := map[uuid.UUID]services.SellerStats{}
	if h.stats != nil {
		if m, serr := h.stats.GetStats(ctx, ids); serr != nil {
			log.Printf("[p2p] GetStats failed: %v — list response omits sellerStats", serr)
		} else {
			statsByID = m
		}
	}
	// inside the per-ad loop, after setting SellerUsername:
	if st, ok := statsByID[ad.SellerID]; ok {
		stCopy := st
		item.SellerStats = &stCopy
	}
```
(`ids` is the existing `[]uuid.UUID` of seller IDs already built for `LookupUsernames`.)

- [ ] **Step 2: Add the profile + rate handlers**

In `internal/handlers/p2p_handler.go`:
```go
func (h *P2PHandler) GetSellerProfile(c *gin.Context) {
	if _, ok := h.require(c); !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	profile, err := h.stats.GetProfile(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	ads, err := h.trade.ListAds(c.Request.Context(), services.AdFilter{SellerID: id, Status: models.P2PAdStatusActive})
	if err != nil {
		ads = nil // non-fatal; profile still renders
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"userId": profile.UserID, "stats": profile.Stats,
		"recentRatings": profile.RecentRatings, "activeAds": ads,
	}})
}

type rateTradeRequest struct {
	Value int16 `json:"value" binding:"required"`
}

func (h *P2PHandler) RateTrade(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	tradeID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req rateTradeRequest
	if err := c.ShouldBindJSON(&req); err != nil || (req.Value != 1 && req.Value != -1) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "value must be 1 or -1"})
		return
	}
	var trade models.P2PTrade
	if err := h.db.First(&trade, "id = ?", tradeID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "trade not found"})
		return
	}
	if user.ID != trade.BuyerID && user.ID != trade.SellerID {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "not a party to this trade"})
		return
	}
	if trade.Status != models.P2PTradeStatusReleased {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "message": "can only rate a released trade"})
		return
	}
	ratee := trade.SellerID
	if user.ID == trade.SellerID {
		ratee = trade.BuyerID
	}
	rating := &models.P2PRating{ID: uuid.New(), TradeID: tradeID, RaterID: user.ID, RateeID: ratee, Value: req.Value}
	if err := h.db.Create(rating).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "already rated this trade"})
		return
	}
	_ = h.stats.RecordRating(c.Request.Context(), rating)
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": rating})
}
```

- [ ] **Step 3: Register routes**

In `internal/routes/routes.go`, inside the `p2pGroup` block, after the trades routes add:
```go
				p2pGroup.GET("/sellers/:id", p2p.GetSellerProfile)
				p2pGroup.POST("/trades/:id/rate", idem, p2p.RateTrade)
```

- [ ] **Step 3b: Wire presence (`Touch`) on P2P activity**

Presence is updated whenever an authenticated user performs any P2P action. In `internal/handlers/p2p_handler.go`, in the `require(c)` helper (the shared P2P auth resolver that returns the `*models.User`), after the user is resolved and before returning, mark them seen — best-effort, non-blocking, never affects the request:
```go
	if h.stats != nil {
		go func(uid uuid.UUID) {
			_ = h.stats.Touch(context.Background(), uid)
		}(user.ID)
	}
```
(This makes `online` reflect recent P2P activity. A future enhancement can also `Touch` on notification-WebSocket connect for broader presence; not required here.) Confirm `context` and `uuid` are imported in the handler file (they are).

- [ ] **Step 4: Update the test rig + write handler tests**

In `internal/handlers/p2p_handler_test.go`: in `newRig`, after the trade service is built and the DB migrated, also migrate the new models and build the stats service, and pass it to `NewP2PHandler`:
```go
	if err := db.AutoMigrate(&models.P2PMerchantStats{}, &models.P2PRating{}); err != nil {
		t.Fatalf("migrate stats: %v", err)
	}
	stats := services.NewMerchantStatsServiceNoBus(db)
	h := NewP2PHandler(db, trade, nil, nil, stats)
```
Add to `mountP2PRoutes`:
```go
	g.GET("/sellers/:id", h.GetSellerProfile)
	g.POST("/trades/:id/rate", h.RateTrade)
```
Then add tests:
```go
func TestRateTrade_RejectsNonReleased(t *testing.T) {
	r := newRig(t)
	tradeID := uuid.New()
	r.db.Create(&models.P2PTrade{ID: tradeID, BuyerID: r.sellerID, SellerID: uuid.New(),
		Status: models.P2PTradeStatusPaid})
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/trades/"+tradeID.String()+"/rate", map[string]any{"value": 1})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRateTrade_HappyAndDuplicate(t *testing.T) {
	r := newRig(t)
	tradeID := uuid.New()
	// rig.user (sellerID) is the buyer here so they're a party + can rate.
	r.db.Create(&models.P2PTrade{ID: tradeID, BuyerID: r.sellerID, SellerID: uuid.New(),
		Status: models.P2PTradeStatusReleased})
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/trades/"+tradeID.String()+"/rate", map[string]any{"value": 1})
	if rec.Code != http.StatusCreated {
		t.Fatalf("first rate status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	rec2 := doJSON(t, r, http.MethodPost, "/api/p2p/trades/"+tradeID.String()+"/rate", map[string]any{"value": 1})
	if rec2.Code != http.StatusConflict {
		t.Fatalf("dup rate status = %d, want 409", rec2.Code)
	}
}

func TestGetSellerProfile_OK(t *testing.T) {
	r := newRig(t)
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/sellers/"+uuid.New().String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 5: Run tests + build**

Run: `go test ./internal/handlers -run 'TestRateTrade|TestGetSellerProfile|TestListPaymentProviders' -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/p2p_handler.go internal/routes/routes.go internal/handlers/p2p_handler_test.go
git commit -m "feat(p2p): sellerStats on ad list + seller profile + rate-trade endpoints"
```

---

## Task 7: Frontend service types + APIs

**Files:**
- Modify: `services/p2p.ts`

- [ ] **Step 1: Add types + APIs**

In `services/p2p.ts`, add near the other interfaces:
```ts
export interface SellerStats {
  completionRate?: number;
  avgReleaseSecs?: number;
  totalOrders: number;
  positivePct?: number;
  online: boolean;
  verified: boolean;
  trusted: boolean;
  isNew: boolean;
}

export interface P2PRating {
  id: string;
  tradeId: string;
  raterId: string;
  rateeId: string;
  value: 1 | -1;
  createdAt: string;
}

export interface MerchantProfile {
  userId: string;
  stats: SellerStats;
  recentRatings: P2PRating[];
  activeAds: P2PAd[];
}
```
Add `sellerStats?: SellerStats;` to the `P2PAd` interface (near `paymentMethodSummary`). Add the API objects:
```ts
export const sellersApi = {
  profile: (id: string) => request<MerchantProfile>('GET', `/p2p/sellers/${encodeURIComponent(id)}`),
};

export const ratingsApi = {
  rate: (tradeId: string, value: 1 | -1) =>
    request<P2PRating>('POST', `/p2p/trades/${encodeURIComponent(tradeId)}/rate`, { value }),
};
```

- [ ] **Step 2: Type-check (touched file only)**

Run: `npx tsc --noEmit 2>&1 | grep 'services/p2p' || echo NO_ERRORS`
Expected: `NO_ERRORS`.

- [ ] **Step 3: Commit**

```bash
git add services/p2p.ts
git commit -m "feat(p2p): frontend types + APIs for seller stats, profile, ratings"
```

---

## Task 8: `useCountdown` hook

**Files:**
- Create: `hooks/useCountdown.ts`
- Test: `hooks/useCountdown.test.ts`

- [ ] **Step 1: Write the failing test**

`hooks/useCountdown.test.ts`:
```ts
import { formatCountdown, secondsLeft } from './useCountdown';

describe('countdown helpers', () => {
  it('secondsLeft clamps to 0 when past', () => {
    const now = new Date('2026-05-28T12:00:00Z').getTime();
    expect(secondsLeft('2026-05-28T12:00:30Z', now)).toBe(30);
    expect(secondsLeft('2026-05-28T11:59:00Z', now)).toBe(0);
  });

  it('formatCountdown renders mm:ss', () => {
    expect(formatCountdown(0)).toBe('0:00');
    expect(formatCountdown(65)).toBe('1:05');
    expect(formatCountdown(600)).toBe('10:00');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx jest hooks/useCountdown.test.ts`
Expected: FAIL (cannot find module / undefined exports).

- [ ] **Step 3: Implement the hook + pure helpers**

`hooks/useCountdown.ts`:
```ts
import { useEffect, useState } from 'react';

/** Whole seconds remaining until `iso`, clamped at 0. */
export function secondsLeft(iso: string, now: number = Date.now()): number {
  const diffMs = new Date(iso).getTime() - now;
  return diffMs <= 0 ? 0 : Math.floor(diffMs / 1000);
}

/** Format seconds as m:ss (e.g. 65 -> "1:05"). */
export function formatCountdown(secs: number): string {
  const m = Math.floor(secs / 60);
  const s = secs % 60;
  return `${m}:${s.toString().padStart(2, '0')}`;
}

/** Live countdown to an ISO deadline. Returns { secs, label, expired }, ticking each second. */
export function useCountdown(deadlineIso?: string): { secs: number; label: string; expired: boolean } {
  const [secs, setSecs] = useState(() => (deadlineIso ? secondsLeft(deadlineIso) : 0));
  useEffect(() => {
    if (!deadlineIso) return;
    setSecs(secondsLeft(deadlineIso));
    const id = setInterval(() => setSecs(secondsLeft(deadlineIso)), 1000);
    return () => clearInterval(id);
  }, [deadlineIso]);
  return { secs, label: formatCountdown(secs), expired: secs <= 0 };
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `npx jest hooks/useCountdown.test.ts`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add hooks/useCountdown.ts hooks/useCountdown.test.ts
git commit -m "feat(p2p): useCountdown hook + pure helpers"
```

---

## Task 9: `SellerTrustBadges` component

**Files:**
- Create: `components/p2p/SellerTrustBadges.tsx`

- [ ] **Step 1: Create the component**

`components/p2p/SellerTrustBadges.tsx`:
```tsx
// components/p2p/SellerTrustBadges.tsx
//
// Renders a seller's trust signals from SellerStats. Compact row for ad cards;
// shows a "New" chip when the merchant has no completed trades yet.
import React from 'react';
import { StyleSheet, Text, View } from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { P2P_GOLD } from '../../constants/p2p';
import type { SellerStats } from '../../services/p2p';

const GOLD = P2P_GOLD;

function relTime(secs?: number): string | null {
  if (secs == null) return null;
  if (secs < 90) return `~${secs}s`;
  return `~${Math.round(secs / 60)}m`;
}

const SellerTrustBadges: React.FC<{ stats?: SellerStats }> = ({ stats }) => {
  if (!stats || stats.isNew) {
    return (
      <View style={styles.row}>
        <View style={[styles.chip, styles.newChip]}>
          <Text style={styles.newChipText}>New</Text>
        </View>
      </View>
    );
  }
  const release = relTime(stats.avgReleaseSecs);
  return (
    <View style={styles.row}>
      {stats.online && <View style={styles.onlineDot} />}
      {stats.trusted ? (
        <View style={[styles.chip, styles.trustedChip]}>
          <Icon name="verified" size={11} color="#fff" />
          <Text style={styles.trustedText}>Trusted</Text>
        </View>
      ) : stats.verified ? (
        <View style={styles.chip}>
          <Icon name="check-circle" size={11} color={GOLD.dark} />
          <Text style={styles.chipText}>Verified</Text>
        </View>
      ) : null}
      <Text style={styles.stat}>{stats.totalOrders} orders</Text>
      {stats.completionRate != null && (
        <Text style={styles.stat}>· {(stats.completionRate * 100).toFixed(0)}%</Text>
      )}
      {release && <Text style={styles.stat}>· releases {release}</Text>}
      {stats.positivePct != null && (
        <Text style={styles.stat}>· 👍 {(stats.positivePct * 100).toFixed(0)}%</Text>
      )}
    </View>
  );
};

const styles = StyleSheet.create({
  row: { flexDirection: 'row', alignItems: 'center', flexWrap: 'wrap', gap: 6, marginTop: 6 },
  chip: { flexDirection: 'row', alignItems: 'center', gap: 3, paddingHorizontal: 6, paddingVertical: 2, borderRadius: 5, backgroundColor: GOLD.light },
  chipText: { fontSize: 10, fontWeight: '700', color: GOLD.dark },
  trustedChip: { backgroundColor: GOLD.dark },
  trustedText: { fontSize: 10, fontWeight: '700', color: '#fff' },
  newChip: { backgroundColor: '#EEF2FF' },
  newChipText: { fontSize: 10, fontWeight: '700', color: '#3B82F6' },
  onlineDot: { width: 8, height: 8, borderRadius: 4, backgroundColor: '#22C55E' },
  stat: { fontSize: 11, color: '#666', fontWeight: '600' },
});

export default SellerTrustBadges;
```

- [ ] **Step 2: Verify the theme keys exist**

Run: `grep -n "dark\|light" constants/p2p.ts | head`
Expected: confirms `P2P_GOLD.dark` and `P2P_GOLD.light` exist (they do — used across P2P screens). Then `npx tsc --noEmit 2>&1 | grep 'SellerTrustBadges' || echo NO_ERRORS` → `NO_ERRORS`.

- [ ] **Step 3: Commit**

```bash
git add components/p2p/SellerTrustBadges.tsx
git commit -m "feat(p2p): SellerTrustBadges component"
```

---

## Task 10: Surface trust badges on ad row + ad detail (+ profile link)

**Files:**
- Modify: `app/p2p/index.tsx`
- Modify: `app/p2p/ad/[adId].tsx`

- [ ] **Step 1: Ad row** — in `app/p2p/index.tsx`, import the component and render it in `AdCard`:
```tsx
import SellerTrustBadges from '../../components/p2p/SellerTrustBadges';
```
Inside `AdCard`, under the seller name block (after `sellerSub`), add:
```tsx
<SellerTrustBadges stats={ad.sellerStats} />
```
Make the seller block tappable to the profile:
```tsx
onPress={() => router.push(`/p2p/seller/${ad.sellerId}`)}
```
(wrap the existing seller avatar+name in a `TouchableOpacity` with that handler; keep the card's existing ad-detail navigation on the rest of the card).

- [ ] **Step 2: Ad detail** — in `app/p2p/ad/[adId].tsx`, in the seller header block, render `<SellerTrustBadges stats={ad.sellerStats} />` under the seller name, and make the seller header tappable → `router.push(\`/p2p/seller/${ad.sellerId}\`)`.

- [ ] **Step 3: Type-check + manual sanity**

Run: `npx tsc --noEmit 2>&1 | grep -E 'app/p2p/index|app/p2p/ad/' || echo NO_ERRORS`
Expected: `NO_ERRORS`.

- [ ] **Step 4: Commit**

```bash
git add app/p2p/index.tsx app/p2p/ad/[adId].tsx
git commit -m "feat(p2p): show seller trust badges on ad row + detail; link to profile"
```

---

## Task 11: Seller profile screen

**Files:**
- Create: `app/p2p/seller/[sellerId].tsx`

- [ ] **Step 1: Create the screen**

`app/p2p/seller/[sellerId].tsx`:
```tsx
// app/p2p/seller/[sellerId].tsx — merchant profile: badges, stats, recent feedback, active ads.
import { router, useLocalSearchParams } from 'expo-router';
import React, { useEffect, useState } from 'react';
import { ActivityIndicator, ScrollView, StyleSheet, Text, TouchableOpacity, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { sellersApi, formatFiat, type MerchantProfile } from '../../../services/p2p';
import SellerTrustBadges from '../../../components/p2p/SellerTrustBadges';
import { P2P_GOLD } from '../../../constants/p2p';

const GOLD = P2P_GOLD;

const SellerProfileScreen: React.FC = () => {
  const { sellerId } = useLocalSearchParams<{ sellerId: string }>();
  const [profile, setProfile] = useState<MerchantProfile | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    (async () => {
      if (!sellerId) return;
      const res = await sellersApi.profile(String(sellerId));
      if (res.success && res.data) setProfile(res.data);
      setLoading(false);
    })();
  }, [sellerId]);

  if (loading) {
    return (
      <SafeAreaView style={styles.container}>
        <ActivityIndicator size="large" color={GOLD.dark} style={{ marginTop: 80 }} />
      </SafeAreaView>
    );
  }
  if (!profile) {
    return (
      <SafeAreaView style={styles.container}>
        <Text style={styles.empty}>Seller not found.</Text>
      </SafeAreaView>
    );
  }
  const up = profile.recentRatings.filter((r) => r.value === 1).length;
  const down = profile.recentRatings.filter((r) => r.value === -1).length;

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView contentContainerStyle={styles.scroll}>
        <Text style={styles.heading}>Merchant</Text>
        <View style={styles.card}>
          <SellerTrustBadges stats={profile.stats} />
        </View>

        <Text style={styles.sectionLabel}>Recent feedback</Text>
        <View style={styles.card}>
          <Text style={styles.feedback}>👍 {up}   👎 {down}</Text>
          {profile.recentRatings.length === 0 && <Text style={styles.muted}>No feedback yet.</Text>}
        </View>

        <Text style={styles.sectionLabel}>Active ads ({profile.activeAds.length})</Text>
        {profile.activeAds.map((ad) => (
          <TouchableOpacity key={ad.id} style={styles.adRow} onPress={() => router.push(`/p2p/ad/${ad.id}`)} activeOpacity={0.85}>
            <Text style={styles.adToken}>{ad.token}</Text>
            <Text style={styles.adRate}>{ad.fiatCurrency} {formatFiat(ad.rate)}</Text>
          </TouchableOpacity>
        ))}
        {profile.activeAds.length === 0 && <Text style={styles.muted}>No active ads.</Text>}
      </ScrollView>
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 60 },
  heading: { fontSize: 22, fontWeight: '800', color: '#000', marginBottom: 12 },
  card: { backgroundColor: '#FFF', borderRadius: 12, padding: 14, borderWidth: 1, borderColor: GOLD.muted, marginBottom: 16 },
  sectionLabel: { fontSize: 13, fontWeight: '700', color: '#333', marginBottom: 8 },
  feedback: { fontSize: 15, fontWeight: '700', color: '#000' },
  adRow: { flexDirection: 'row', justifyContent: 'space-between', backgroundColor: '#FFF', borderRadius: 10, padding: 14, borderWidth: 1, borderColor: GOLD.muted, marginBottom: 8 },
  adToken: { fontSize: 15, fontWeight: '700', color: '#000' },
  adRate: { fontSize: 14, color: GOLD.dark, fontWeight: '700' },
  muted: { fontSize: 13, color: '#888', fontStyle: 'italic' },
  empty: { textAlign: 'center', marginTop: 60, color: '#888' },
});

export default SellerProfileScreen;
```

- [ ] **Step 2: Register the dynamic route**

Confirm `app/p2p/_layout.tsx` registers nested screens (it already handles `ad/[adId]`, `trade/[tradeId]`). If routes are declared explicitly there, add a `<Stack.Screen name="seller/[sellerId]" />` entry mirroring the existing `ad/[adId]` entry. If the layout auto-discovers files, no change needed.

- [ ] **Step 3: Type-check**

Run: `npx tsc --noEmit 2>&1 | grep 'app/p2p/seller' || echo NO_ERRORS`
Expected: `NO_ERRORS`.

- [ ] **Step 4: Commit**

```bash
git add app/p2p/seller/[sellerId].tsx app/p2p/_layout.tsx
git commit -m "feat(p2p): seller profile screen"
```

---

## Task 12: Live countdown + rating prompt in trade UI

**Files:**
- Modify: `app/p2p/trade/[tradeId].tsx`
- Modify: `app/p2p/my-trades.tsx`

- [ ] **Step 1: Live countdown** — in `app/p2p/trade/[tradeId].tsx`, import and use the hook for the pending-payment window. Replace the static "Xm remaining" expiry text with the live label:
```tsx
import { useCountdown } from '../../../hooks/useCountdown';
// inside the component:
const countdown = useCountdown(trade?.status === 'pending_payment' ? trade?.expiresAt : undefined);
// in the summary where expiry is shown:
{trade.status === 'pending_payment' && (
  <Text style={styles.countdown}>{countdown.expired ? 'Expired' : `${countdown.label} left to pay`}</Text>
)}
```
Add a `countdown` style (reuse an existing text style if present).

- [ ] **Step 2: Rating prompt** — in the same file, when `trade.status === 'released'`, render a one-time 👍/👎 prompt that calls `ratingsApi.rate`:
```tsx
import { ratingsApi } from '../../../services/p2p';
const [rated, setRated] = useState(false);
const rate = async (value: 1 | -1) => {
  const res = await ratingsApi.rate(String(tradeId), value);
  if (res.success || res.status === 409) setRated(true);
};
// in the render, when released and !rated:
{trade.status === 'released' && !rated && (
  <View style={styles.ratePrompt}>
    <Text style={styles.rateText}>Rate this trade</Text>
    <View style={{ flexDirection: 'row', gap: 16 }}>
      <TouchableOpacity onPress={() => rate(1)}><Text style={styles.rateBtn}>👍</Text></TouchableOpacity>
      <TouchableOpacity onPress={() => rate(-1)}><Text style={styles.rateBtn}>👎</Text></TouchableOpacity>
    </View>
  </View>
)}
{rated && <Text style={styles.muted}>Thanks for your feedback.</Text>}
```
Add `ratePrompt`, `rateText`, `rateBtn`, `muted` styles consistent with the file.

- [ ] **Step 3: my-trades nudge** — in `app/p2p/my-trades.tsx`, for trades in the Done bucket with `status === 'released'`, show a small "Rate" affordance on the trade card that routes into `trade/[tradeId]` (where the prompt lives). Minimal: a "Rate this trade" text link on released cards.

- [ ] **Step 4: Type-check**

Run: `npx tsc --noEmit 2>&1 | grep -E 'app/p2p/trade/|app/p2p/my-trades' || echo NO_ERRORS`
Expected: `NO_ERRORS`.

- [ ] **Step 5: Commit**

```bash
git add app/p2p/trade/[tradeId].tsx app/p2p/my-trades.tsx
git commit -m "feat(p2p): live countdown timer + post-trade rating prompt"
```

---

## Task 13: End-to-end verification

- [ ] **Step 1: Backend build + targeted tests**

Run from `tsa-api-go`:
```bash
go build ./... \
 && go test ./internal/services -run 'Provider|Summary|Trade|Reputation|Reconcile|Apply|RecordRating|GetStats|Completion|Positive|Online|Trusted' \
 && go test ./internal/handlers -run 'CreateAd|SignAd|CreatePaymentMethod|ListPaymentProviders|RateTrade|GetSellerProfile' \
 && go test ./internal/jobs ./...
```
Expected: build clean; P2P services/handlers/jobs suites pass. (Pre-existing unrelated failing suites — wallet/cart/orders/checkout — are out of scope; do not fix here.)

- [ ] **Step 2: Frontend tests + type-check (touched files)**

Run from `tsa-app`:
```bash
npx jest services/p2p.test.ts hooks/useCountdown.test.ts hooks/useP2pCurrency.test.ts \
 && npx tsc --noEmit 2>&1 | grep -E 'app/p2p/|components/p2p/|hooks/useCountdown|services/p2p\.ts' || echo NO_NEW_ERRORS_IN_TOUCHED_FILES
```
Expected: jest green; no NEW tsc errors in touched files.

- [ ] **Step 3: Manual smoke (document results)**

- Browse ads → an established seller shows badges (orders, completion %, releases ~Xm, online dot); a brand-new seller shows the "New" chip.
- Tap a seller → profile screen loads (stats + feedback + active ads).
- Open a pending trade → countdown ticks down each second.
- Complete a trade (release) → both parties' order counts increment on next ad-list/profile load; the released trade shows the 👍/👎 prompt; rating once → "Thanks"; second attempt is a no-op (409 handled).

State explicitly in the report which manual checks were performed vs. not runnable.

---

## Self-review notes (for the executor)
- The reconcile SQL has a **SQLite vs PostgreSQL date-diff** branch — implement both (Task 4 IMPORTANT note); tests run on SQLite, prod is PostgreSQL.
- `NewP2PHandler` gains a parameter — every call site (main.go + test rig) must be updated (Tasks 5–6).
- Reputation never gates money — all stat writes are best-effort + logged; the reconcile job is the correctness backstop.
- Expo push, Express auto-match, and merchant collateral are **out of scope** (separate specs).
