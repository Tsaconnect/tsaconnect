package services

import (
	"context"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// PoolOrderDetail is the console's view of one pool order: the intent, its
// snapshot (fill, spread, fill hash, books), the sale leg and the conversion.
type PoolOrderDetail struct {
	Intent     *models.ITIntent            `json:"intent"`
	Snapshots  []models.PoolOrder          `json:"snapshots"`
	SaleLeg    *models.RampLeg             `json:"saleLeg,omitempty"`
	Conversion *models.PaycrestOrderRecord `json:"conversion,omitempty"`
	Actions    []string                    `json:"actions"`
}

// PoolOrderDetail reads one pool order for the console (admin only).
func (s *ITService) PoolOrderDetail(ctx context.Context, intentID uuid.UUID) (*PoolOrderDetail, error) {
	if s.poolDesk == nil {
		return nil, ErrPoolSettleUnwired
	}
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if !isPoolBuy(intent) {
		return nil, ErrStableIntentNotFound
	}
	out := &PoolOrderDetail{Intent: intent, Actions: s.poolOrderActions(ctx, intent)}
	if err := s.db.WithContext(ctx).Where("intent_id = ?", intentID).Order("generation").Find(&out.Snapshots).Error; err != nil {
		return nil, err
	}
	if leg, err := s.liveSaleLeg(ctx, intentID); err == nil {
		out.SaleLeg = leg
	}
	if rec, err := conversionRecordOn(ctx, s.db, intentID); err == nil {
		out.Conversion = rec
	}
	return out, nil
}

// ResendPoolBuy is the company wallet's evidence-gated re-send, on the pool's
// sale desk: the same dropped-transaction check decides.
func (s *ITService) ResendPoolBuy(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	sd := s.poolSale()
	if sd == nil {
		return nil, ErrPoolSettleUnwired
	}
	return s.resendSale(ctx, sd, adminID, intentID, note)
}
