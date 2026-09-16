package services

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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

func (s *GormRampLegStore) Confirm(ctx context.Context, id uuid.UUID, amountReceived string, at time.Time) error {
	return s.DB.WithContext(ctx).Model(&models.RampLeg{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":          models.RampLegStatusConfirmed,
			"amount_received": amountReceived,
			"confirmed_at":    at.UTC(),
		}).Error
}

func (s *GormRampLegStore) Fail(ctx context.Context, id uuid.UUID, reason string) error {
	if len(reason) > 512 {
		reason = reason[:512]
	}
	return s.DB.WithContext(ctx).Model(&models.RampLeg{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": models.RampLegStatusFailed, "error": reason}).Error
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
