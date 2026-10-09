package services

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// AlertStalePaidTrades tells operators about manual-ad trades that have been
// paid (or mid-release) for longer than olderThan, once per trade: the audit
// row it writes first is the dedupe, so a failed email never re-alerts every
// tick. PM-ad trades auto-settle and are out of scope.
func (s *P2PTradeService) AlertStalePaidTrades(ctx context.Context, olderThan time.Duration, limit int) (int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	now := s.clock.Now()
	var rows []models.P2PTrade
	if err := s.db.WithContext(ctx).
		Where("status IN ? AND marked_paid_at IS NOT NULL AND marked_paid_at < ?",
			[]string{models.P2PTradeStatusPaid, models.P2PTradeStatusReleasing}, now.Add(-olderThan)).
		Where("ad_id IN (SELECT id FROM p2p_ads WHERE pm_id IS NULL)").
		Where("NOT EXISTS (SELECT 1 FROM p2p_audit_logs a WHERE a.trade_id = p2p_trades.id AND a.action = ?)",
			models.P2PAuditTradePaidStaleAlerted).
		Order("marked_paid_at ASC").Limit(limit).Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("list stale paid trades: %w", err)
	}
	alerted := 0
	for i := range rows {
		t := &rows[i]
		tradeID := t.ID
		// Written with the service clock, like the renewal notice.
		if err := s.db.WithContext(ctx).Create(&models.P2PAuditLog{ID: uuid.New(), TradeID: &tradeID,
			Action: models.P2PAuditTradePaidStaleAlerted, CreatedAt: now}).Error; err != nil {
			return alerted, fmt.Errorf("record stale-paid alert of trade %s: %w", t.ID, err)
		}
		age := now.Sub(*t.MarkedPaidAt)
		s.alert(ctx, P2PAlertPaidUnreleased, t, func(a *P2PAlert) { a.Age = age })
		alerted++
	}
	return alerted, nil
}
