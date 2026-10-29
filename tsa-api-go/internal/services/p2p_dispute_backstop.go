package services

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// p2p_dispute_backstop.go — P2PTradeEscrow returns a disputed trade's crypto to the seller once
// P2PDisputeBackstop has passed with no resolution (claimDisputeBackstop, callable by anyone). It
// is a liveness fallback, not a verdict: a seller who disputes a trade the buyer genuinely paid
// gains if nobody resolves it. Operators are told twice — once when a dispute has sat open
// (DisputeStaleAfter) and again when the backstop is near (DisputeBackstopWarn).

const (
	// DisputeStaleAfter is how long a dispute may sit open before operators are told.
	DisputeStaleAfter = 3 * 24 * time.Hour
	// DisputeBackstopWarn is how long before the backstop the second alert goes out.
	DisputeBackstopWarn = 7 * 24 * time.Hour
)

// AlertUnresolvedDisputes alerts once per trade per stage for disputed trades left unresolved;
// it returns how many alerts it sent. The dispute's age is its open dispute row's.
func (s *P2PTradeService) AlertUnresolvedDisputes(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	now := s.clock.Now()
	alerted := 0
	for _, stage := range []struct {
		action    string
		openSince time.Time
	}{
		{models.P2PAuditTradeDisputeBackstopAlerted, now.Add(-(P2PDisputeBackstop - DisputeBackstopWarn))},
		{models.P2PAuditTradeDisputeStaleAlerted, now.Add(-DisputeStaleAfter)},
	} {
		type row struct {
			models.P2PTrade
			DisputedSince time.Time
		}
		var rows []row
		if err := s.db.WithContext(ctx).Table("p2p_trades").
			Select("p2p_trades.*, d.created_at AS disputed_since").
			Joins("JOIN p2p_disputes d ON d.trade_id = p2p_trades.id AND d.status = ?", models.P2PDisputeStatusOpen).
			Where("p2p_trades.status = ? AND d.created_at < ?", models.P2PTradeStatusDisputed, stage.openSince).
			// One alert per trade per stage; a dispute already warned about the backstop needs no "stale" one.
			Where("NOT EXISTS (SELECT 1 FROM p2p_audit_logs a WHERE a.trade_id = p2p_trades.id AND a.action IN ?)",
				[]string{stage.action, models.P2PAuditTradeDisputeBackstopAlerted}).
			Order("d.created_at ASC").Limit(limit).Scan(&rows).Error; err != nil {
			return alerted, fmt.Errorf("list unresolved disputes: %w", err)
		}
		for i := range rows {
			t := &rows[i].P2PTrade
			tradeID := t.ID
			if err := s.db.WithContext(ctx).Create(&models.P2PAuditLog{ID: uuid.New(), TradeID: &tradeID,
				Action: stage.action, CreatedAt: now}).Error; err != nil {
				return alerted, fmt.Errorf("record dispute alert of trade %s: %w", t.ID, err)
			}
			age := now.Sub(rows[i].DisputedSince)
			s.alert(ctx, P2PAlertDisputeUnresolved, t, func(a *P2PAlert) {
				a.Age, a.BackstopIn = age, max(P2PDisputeBackstop-age, 0)
			})
			alerted++
		}
	}
	return alerted, nil
}
