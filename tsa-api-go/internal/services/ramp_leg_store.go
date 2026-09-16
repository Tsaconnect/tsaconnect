package services

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrLegNotPending is returned by Confirm and Fail when the targeted leg is
// no longer pending: a late or duplicate call must never overwrite a row that
// already reached a terminal status, since refunds are computed from these
// rows and a self-contradicting row is a money bug.
var ErrLegNotPending = errors.New("ramp leg is no longer pending")

// rampLegStore is the ledger of on-chain movements for a converted ramp
// order. Refund amounts are computed from these rows, never typed by hand, so
// idempotency here is what stops a retried background tick moving a user's
// money twice.
type rampLegStore interface {
	Reserve(ctx context.Context, leg *models.RampLeg) (*models.RampLeg, error)
	SetTxHash(ctx context.Context, id uuid.UUID, txHash, tool string) error
	Confirm(ctx context.Context, id uuid.UUID, amountReceived string, at time.Time) error
	Fail(ctx context.Context, id uuid.UUID, reason string) error
	ForOrder(ctx context.Context, orderID uuid.UUID) ([]models.RampLeg, error)
	NextAttempt(ctx context.Context, orderID uuid.UUID, kind string) (int, error)
}

type GormRampLegStore struct{ DB *gorm.DB }

// Reserve inserts the leg, or returns the row already holding
// (order_id, kind, attempt). The unique index settles concurrent ticks; a
// pre-check could not. Same shape as GormSponsorshipStore.Reserve.
func (s *GormRampLegStore) Reserve(ctx context.Context, leg *models.RampLeg) (*models.RampLeg, error) {
	if leg.Status == "" {
		leg.Status = models.RampLegStatusPending
	}
	if leg.Attempt <= 0 {
		leg.Attempt = 1
	}
	res := s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(leg)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 1 {
		return nil, nil
	}
	var existing models.RampLeg
	if err := s.DB.WithContext(ctx).
		Where("order_id = ? AND kind = ? AND attempt = ?", leg.OrderID, leg.Kind, leg.Attempt).
		First(&existing).Error; err != nil {
		return nil, err
	}
	return &existing, nil
}

func (s *GormRampLegStore) SetTxHash(ctx context.Context, id uuid.UUID, txHash, tool string) error {
	return s.DB.WithContext(ctx).Model(&models.RampLeg{}).
		Where("id = ?", id).
		Updates(map[string]any{"tx_hash": txHash, "lifi_tool": tool}).Error
}

// Confirm records what landed, but only on a still-pending leg: scoping the
// mutation to status=pending (mirroring GormSponsorshipStore.Release's guard
// on an empty tx_hash) stops a late or duplicate call from overwriting a row
// that already reached a terminal status.
func (s *GormRampLegStore) Confirm(ctx context.Context, id uuid.UUID, amountReceived string, at time.Time) error {
	res := s.DB.WithContext(ctx).Model(&models.RampLeg{}).
		Where("id = ? AND status = ?", id, models.RampLegStatusPending).
		Updates(map[string]any{
			"status":          models.RampLegStatusConfirmed,
			"amount_received": amountReceived,
			"confirmed_at":    at.UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrLegNotPending
	}
	return nil
}

// Fail records why a leg didn't land, but only on a still-pending leg — see
// Confirm's comment.
func (s *GormRampLegStore) Fail(ctx context.Context, id uuid.UUID, reason string) error {
	reason = truncateRunes(reason, 512)
	res := s.DB.WithContext(ctx).Model(&models.RampLeg{}).
		Where("id = ? AND status = ?", id, models.RampLegStatusPending).
		Updates(map[string]any{"status": models.RampLegStatusFailed, "error": reason})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrLegNotPending
	}
	return nil
}

// truncateRunes trims s to at most n runes so a byte-based cut never splits a
// multi-byte UTF-8 rune (the `error` column has a fixed byte budget, but the
// stored text must stay valid UTF-8).
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}

func (s *GormRampLegStore) ForOrder(ctx context.Context, orderID uuid.UUID) ([]models.RampLeg, error) {
	var legs []models.RampLeg
	err := s.DB.WithContext(ctx).Where("order_id = ?", orderID).
		Order("created_at ASC").Find(&legs).Error
	return legs, err
}

// NextAttempt is the attempt number a retry should claim: one past the
// highest attempt this order has for that kind, or 1 when there is none.
func (s *GormRampLegStore) NextAttempt(ctx context.Context, orderID uuid.UUID, kind string) (int, error) {
	var max *int
	err := s.DB.WithContext(ctx).Model(&models.RampLeg{}).
		Where("order_id = ? AND kind = ?", orderID, kind).
		Select("MAX(attempt)").Scan(&max).Error
	if err != nil {
		return 0, err
	}
	if max == nil {
		return 1, nil
	}
	return *max + 1, nil
}
