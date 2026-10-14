package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrPoolReleaseStalled: a paid pool order's fill has no proof past
	// poolFillStallAfter. The settle job alerts it, sustained.
	ErrPoolReleaseStalled = errors.New("liquidity pool: a paid order's fill has no proof past the stall window")
	// ErrPoolSettleUnwired: the desk has no operator, vault or sale wallet.
	ErrPoolSettleUnwired = errors.New("liquidity pool: the settle side is not wired")
)

func isPoolBuy(i *models.ITIntent) bool {
	t, err := providerTerms(i)
	return err == nil && t.pool
}

// poolSale is the pool's sale desk: the conversion treasury's pool-chain
// lane as a CompanyWallet (S20, P8). The lane's own CheckCaps (kill switch,
// per-order cap) runs inside sendSale; pool_order_max was enforced at open.
func (s *ITService) poolSale() *saleDesk {
	d := s.poolDesk
	if d == nil || d.c.Sale == nil {
		return nil
	}
	return &saleDesk{wallet: d.c.Sale, label: "TSA pool", tag: "[liquidity]", scope: scopePoolBuys, owns: isPoolBuy,
		checkOrder: func(*models.ITIntent, *big.Int) error { return nil },
		onSettle: func(tx *gorm.DB, intent *models.ITIntent, _ *models.RampLeg) error {
			return s.poolSettleBooks(tx, intent)
		}}
}

func (d *PoolDesk) settleWired() bool {
	return d != nil && d.c.Sender != nil && d.c.Sale != nil && d.c.Vault != nil
}

// PendingPoolBuys lists pool buys the watcher must advance.
func (s *ITService) PendingPoolBuys(ctx context.Context) (received, delivering []uuid.UUID, err error) {
	sd := s.poolSale()
	if sd == nil {
		return nil, nil, nil
	}
	return s.pendingSaleBuys(ctx, sd)
}

// AdvancePoolBuy is the pool's settle step (plan C2 P4): idempotent, safe on
// every tick, everything re-read.
func (s *ITService) AdvancePoolBuy(ctx context.Context, id uuid.UUID) error {
	d := s.poolDesk
	if !d.settleWired() {
		return ErrPoolSettleUnwired
	}
	intent, err := s.getIntent(ctx, id)
	if err != nil {
		return err
	}
	if intent.Direction != models.ITDirectionBuy || !isPoolBuy(intent) {
		return ErrITBadState
	}
	switch intent.Status {
	case models.ITStatusPaymentReceived:
		won, err := s.claimPoolDelivery(ctx, d, intent.ID)
		if err != nil || !won {
			return err
		}
		if intent, err = s.getIntent(ctx, id); err != nil { // the claim moved status and updated_at
			return err
		}
	case models.ITStatusDelivering:
	default:
		return nil
	}
	return s.advanceVaultDelivering(ctx, d, intent)
}

// claimPoolDelivery is S37's other half: under the pool lock and the
// intent's row lock — the refund's locks — payment_received → delivering.
// Only the winner releases anything.
func (s *ITService) claimPoolDelivery(ctx context.Context, d *PoolDesk, id uuid.UUID) (bool, error) {
	won := false
	err := withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var locked models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").First(&locked, "id = ?", id).Error; err != nil {
			return err
		}
		if locked.Status != models.ITStatusPaymentReceived {
			return nil
		}
		res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", id, models.ITStatusPaymentReceived).
			Update("status", models.ITStatusDelivering)
		won = res.Error == nil && res.RowsAffected == 1
		return res.Error
	})
	return won, err
}

// handOffPoolOrder is a converted pool order's delivery: the record takes the
// proven fill as its hash and the intent settles, in one transaction; the
// orchestrator then proves custody from that fill.
func (s *ITService) handOffPoolOrder(ctx context.Context, intent *models.ITIntent, firstRelease string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", intent.ID).Error; err != nil {
			return err
		}
		if locked.Status != models.ITStatusDelivering {
			return nil
		}
		res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", intent.ID, models.ITStatusDelivering).
			Updates(map[string]any{"status": models.ITStatusSettled, "tx_hash": firstRelease, "failure_reason": ""})
		if res.Error != nil || res.RowsAffected != 1 {
			return res.Error
		}
		rec, err := conversionRecordOn(ctx, tx, intent.ID)
		if err != nil {
			return err
		}
		if rec == nil { // never a settled converted order with nothing for takeCustody to wait on
			return fmt.Errorf("%w: intent %s has no conversion record to hand off", ErrITBadState, intent.ID)
		}
		if err := s.handoffConversion(ctx, tx, &locked, firstRelease); err != nil {
			return err
		}
		log.Printf("[liquidity] METRIC liquidity_pool_handoff intent=%s tx=%s", intent.ID, firstRelease)
		return s.poolSettleBooks(tx, &locked)
	})
}
