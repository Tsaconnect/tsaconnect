package services

import (
	"context"
	"log"
	"math"
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
// ReconcileAllMerchants job (added in a later task).
type MerchantStatsService struct {
	db         *gorm.DB
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
		if ev.UserID != trade.SellerID { // resolved fires to BOTH parties — process once
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

// MerchantProfile is the seller profile payload (stats + recent feedback). Active
// ads are added by the handler, which already has the ad query.
type MerchantProfile struct {
	UserID        uuid.UUID          `json:"userId"`
	Stats         SellerStats        `json:"stats"`
	RecentRatings []models.P2PRating `json:"recentRatings"`
}

// GetProfile returns a merchant's stats + up to 20 recent ratings.
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

// dateDiffSecsExpr returns a SQL expression for (a - b) in seconds, dialect-aware
// (SQLite for tests, PostgreSQL for prod).
func (s *MerchantStatsService) dateDiffSecsExpr(a, b string) string {
	if s.db.Dialector.Name() == "sqlite" {
		return "AVG(CAST((julianday(" + a + ") - julianday(" + b + ")) * 86400 AS REAL))"
	}
	return "AVG(EXTRACT(EPOCH FROM (" + a + " - " + b + ")))"
}

// ReconcileAllMerchants recomputes lifetime counts + 30-day avg times from the
// source-of-truth trades/ratings, and refreshes verified/trusted. Idempotent;
// the correctness backstop for the incremental path.
func (s *MerchantStatsService) ReconcileAllMerchants(ctx context.Context) error {
	cutoff := time.Now().UTC().AddDate(0, 0, -30)

	type countRow struct {
		UserID uuid.UUID
		N      int64
	}
	upsertCount := func(rows []countRow, col string) error {
		now := time.Now().UTC()
		for _, r := range rows {
			// Use raw SQL so the insert and conflict-update paths both write r.N.
			// GORM's Create+OnConflict only updates the DoUpdates columns on conflict;
			// the insert path would write the zero value from the struct.
			sql := `INSERT INTO p2p_merchant_stats (user_id, ` + col + `, updated_at)
				VALUES (?, ?, ?)
				ON CONFLICT(user_id) DO UPDATE SET ` + col + ` = excluded.` + col + `, updated_at = excluded.updated_at`
			if err := s.db.WithContext(ctx).Exec(sql, r.UserID.String(), r.N, now).Error; err != nil {
				return err
			}
		}
		return nil
	}

	// 1) lifetime released_count per user (either role) from released trades.
	var released []countRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT user_id, COUNT(*) AS n FROM (
			SELECT seller_id AS user_id FROM p2p_trades WHERE status = ?
			UNION ALL
			SELECT buyer_id AS user_id FROM p2p_trades WHERE status = ?
		) t GROUP BY user_id`,
		models.P2PTradeStatusReleased, models.P2PTradeStatusReleased).Scan(&released).Error; err != nil {
		return err
	}
	if err := upsertCount(released, "released_count"); err != nil {
		return err
	}

	// 2) 30-day avg release secs (seller) + avg pay secs (buyer).
	type avgRow struct {
		UserID uuid.UUID
		Avg    float64
	}
	var relAvg []avgRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT seller_id AS user_id, `+s.dateDiffSecsExpr("released_at", "marked_paid_at")+` AS avg
		FROM p2p_trades
		WHERE status = ? AND released_at IS NOT NULL AND marked_paid_at IS NOT NULL AND released_at >= ?
		GROUP BY seller_id`, models.P2PTradeStatusReleased, cutoff).Scan(&relAvg).Error; err != nil {
		log.Printf("[merchant-stats] reconcile avg-release scan: %v", err)
	}
	for _, r := range relAvg {
		secs := int(math.Round(r.Avg))
		if err := s.db.WithContext(ctx).Model(&models.P2PMerchantStats{}).
			Where("user_id = ?", r.UserID).Update("avg_release_secs_30d", secs).Error; err != nil {
			log.Printf("[merchant-stats] reconcile update avg_release for %s: %v", r.UserID, err)
		}
	}
	var payAvg []avgRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT buyer_id AS user_id, `+s.dateDiffSecsExpr("marked_paid_at", "created_at")+` AS avg
		FROM p2p_trades
		WHERE status = ? AND marked_paid_at IS NOT NULL AND marked_paid_at >= ?
		GROUP BY buyer_id`, models.P2PTradeStatusReleased, cutoff).Scan(&payAvg).Error; err != nil {
		log.Printf("[merchant-stats] reconcile avg-pay scan: %v", err)
	}
	for _, r := range payAvg {
		secs := int(math.Round(r.Avg))
		if err := s.db.WithContext(ctx).Model(&models.P2PMerchantStats{}).
			Where("user_id = ?", r.UserID).Update("avg_pay_secs_30d", secs).Error; err != nil {
			log.Printf("[merchant-stats] reconcile update avg_pay for %s: %v", r.UserID, err)
		}
	}

	// 3) refresh verified + trusted for all known merchants.
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
	verified := u.VerificationStatus == models.VerificationStatusVerified && u.Role == models.RoleMerchant
	s.db.WithContext(ctx).Model(&models.P2PMerchantStats{}).Where("user_id = ?", userID).Update("verified", verified)
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
