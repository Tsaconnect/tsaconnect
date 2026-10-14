package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrPoolReleaseStalled: a paid pool order has an unproven slice release
	// past poolReleaseStallAfter (F10). The watcher alerts it, sustained.
	ErrPoolReleaseStalled = errors.New("liquidity pool: a paid order's release has no proof past the stall window")
	// ErrPoolSettleUnwired: the desk has no release key, verifier or sale wallet.
	ErrPoolSettleUnwired = errors.New("liquidity pool: the settle side is not wired")
)

const (
	poolReleaseStallAfter = 10 * time.Minute // F10, §22
	poolReleaseNearWindow = 10 * time.Minute // a slice this close to cancelByTimeout and still unproven
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
	return d != nil && d.c.Sender != nil && d.c.Verifier != nil && d.c.Sale != nil
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
	if d.vaultMode() {
		return s.advanceVaultDelivering(ctx, d, intent)
	}
	return s.advancePoolDelivering(ctx, d, intent)
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

// keptSlices are the slices still backing the order, every generation:
// not dropped, not cancelled (a slice released to the treasury stays kept
// even if an LP self-released it).
func (d *PoolDesk) keptSlices(ctx context.Context, db *gorm.DB, intentID uuid.UUID) ([]models.P2PTrade, error) {
	var rows []models.P2PTrade
	err := db.WithContext(ctx).Where("it_intent_id = ? AND dropped_at IS NULL AND status <> ?", intentID, models.P2PTradeStatusCancelled).
		Order("created_at").Find(&rows).Error
	return rows, err
}

func (s *ITService) advancePoolDelivering(ctx context.Context, d *PoolDesk, intent *models.ITIntent) error {
	kept, err := d.keptSlices(ctx, s.db, intent.ID)
	if err != nil {
		return err
	}
	// 1. Bring each kept slice's row up to the chain (F21/F22/cancel land now,
	//    not after the 200-row reconciler window, pre-merge N-3), then release
	//    what is still Pending — one at a time, through the release key (S39).
	lost := false
	for i := range kept {
		sl := &kept[i]
		if sl.Status == models.P2PTradeStatusPendingPayment || sl.Status == models.P2PTradeStatusPaid || sl.Status == models.P2PTradeStatusReleasing {
			if _, err := d.c.Trades.ReconcileTrade(ctx, sl.ID); err != nil {
				log.Printf("[liquidity] reconcile slice=%s: %s", sl.ID, RedactAlertText(err.Error()))
			}
			if err := s.db.WithContext(ctx).First(sl, "id = ?", sl.ID).Error; err != nil {
				return err
			}
		}
		switch {
		case sl.Status == models.P2PTradeStatusCancelled, sl.DroppedAt != nil:
			lost = true
			continue
		case sl.Status != models.P2PTradeStatusPendingPayment && sl.Status != models.P2PTradeStatusPaid:
			continue
		}
		if sl.ReleaseTxHash != "" {
			continue // review C-1: a release was broadcast; the reconciler decides it — never a second broadcast per tick
		}
		t, err := d.c.Escrow.ReadTrade(HashID(sl.ID))
		if err != nil || !t.Exists || (t.Status != OnChainP2PStatusPending && t.Status != OnChainP2PStatusPaid) ||
			!SameEVMAddress(t.Buyer.Hex(), d.c.Treasury.Address()) {
			continue // the reconciler or the next tick decides it
		}
		release, err := s.decidePoolRelease(ctx, d, intent.ID, sl.ID)
		if err != nil {
			log.Printf("[liquidity] release decision slice=%s intent=%s: %s", sl.ID, intent.ID, RedactAlertText(err.Error()))
			continue
		}
		if !release {
			continue
		}
		if _, err := d.c.Trades.AutoRelease(ctx, sl.ID, d.c.Sender); err != nil && !errors.Is(err, ErrSliceReleaseInFlight) {
			// In flight (ErrSliceReleaseInFlight) is never re-sent. Anything else
			// was either not broadcast (rolled back: the next tick decides again)
			// or stays at the treasury with its hash for the proof.
			log.Printf("[liquidity] release slice=%s intent=%s: %s", sl.ID, intent.ID, RedactAlertText(err.Error()))
		}
	}
	// 2. A slice lost under the paid order: S18 again (P5).
	if lost {
		parked, err := s.reevaluatePoolOrder(ctx, d, intent.ID, "a slice left escrow before its release")
		if err != nil || parked {
			return err
		}
		if kept, err = d.keptSlices(ctx, s.db, intent.ID); err != nil {
			return err
		}
	}
	// 3. Prove each release on its own receipt.
	need := s.saleDepth()
	var proven []models.P2PTrade
	all := len(kept) > 0
	for i := range kept {
		sl := &kept[i]
		if err := s.db.WithContext(ctx).First(sl, "id = ?", sl.ID).Error; err != nil {
			return err
		}
		ok := false
		if sliceAtTreasury(sl.Status, sl.DroppedAt, sl.ReleaseTxHash, sl.CancelReason) {
			if ok, err = d.proveSliceRelease(ctx, sl, need); err != nil {
				log.Printf("[liquidity] prove slice=%s: %s", sl.ID, RedactAlertText(err.Error()))
			}
		}
		if ok {
			proven = append(proven, *sl)
			continue
		}
		all = false
		if time.Until(sl.ExpiresAt) < poolReleaseNearWindow {
			// Once per slice per day, durably (review m-10: job_run_markers, as
			// every other dedupe here — never a process map that only grows).
			if claimRunAt(ctx, s.db, "pool_release_near_window:"+sl.ID.String(), 24*time.Hour, time.Now()) {
				log.Printf("[liquidity] ALERT liquidity_pool_release_near_window slice=%s intent=%s expires=%s status=%s",
					sl.ID, intent.ID, sl.ExpiresAt.UTC().Format(time.RFC3339), sl.Status)
			}
		}
	}
	s.poolProven(ctx, d, intent, proven) // §12.5: each LP is paid as soon as its own release is proven (Task 8)
	if !all {
		if time.Since(intent.UpdatedAt) > poolReleaseStallAfter {
			log.Printf("[liquidity] ALERT liquidity_pool_release_stalled intent=%s proven=%d of %d", intent.ID, len(proven), len(kept))
			return fmt.Errorf("%w: intent %s", ErrPoolReleaseStalled, intent.ID)
		}
		return nil
	}
	// 4. Every kept slice is proven: the units at the treasury must cover her.
	total := new(big.Int)
	for i := range proven {
		total.Add(total, d.toCanonical(proven[i].TokenAmount))
	}
	owed, ok := new(big.Int).SetString(intent.AssetAmount, 10)
	if !ok || total.Cmp(owed) < 0 {
		log.Printf("[liquidity] ALERT liquidity_pool_short_release intent=%s proven=%s owed=%s — not delivered", intent.ID, total, intent.AssetAmount)
		// Review I-2: never delivered short, never left without an exit. S18
		// under the locks parks it pool_slices_short (refused while a sale leg
		// is live), which opens re-delivery and the S37 override.
		if ok {
			if parked, err := s.reevaluatePoolOrder(ctx, d, intent.ID, "the proven releases fall short of her amount"); err != nil || parked {
				return err
			}
		}
		return fmt.Errorf("%w: proven %s of %s", ErrITBadState, total, intent.AssetAmount)
	}
	// 5. Deliver: hand off (converted) or send (direct).
	rec, err := s.ConversionFor(ctx, intent)
	if err != nil {
		return err
	}
	if rec != nil {
		return s.handOffPoolOrder(ctx, intent, proven[0].ReleaseTxHash)
	}
	sd := s.poolSale()
	if err := s.sendSale(ctx, sd, intent.ID); err != nil {
		log.Printf("[liquidity] send intent=%s: %s", intent.ID, RedactAlertText(err.Error()))
	}
	return s.finalizeSale(ctx, sd, intent.ID)
}

// decidePoolRelease is the release decision (plan C2 P4.2), taken under the
// pool lock and the intent's row lock — the locks every drop, park and refund
// takes: the order is still delivering and the slice still backs it, live
// (opened, not dropped) and with no release recorded. AutoRelease's flip CAS then makes one caller the
// only sender.
func (s *ITService) decidePoolRelease(ctx context.Context, d *PoolDesk, intentID, sliceID uuid.UUID) (bool, error) {
	release := false
	err := withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var locked models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").First(&locked, "id = ?", intentID).Error; err != nil {
			return err
		}
		if locked.Status != models.ITStatusDelivering {
			return nil
		}
		var sl models.P2PTrade
		if err := tx.First(&sl, "id = ? AND it_intent_id = ?", sliceID, intentID).Error; err != nil {
			return err
		}
		// Review m-1: opened_at too — the same "live" orderSlices uses for a
		// park's drop list, so a slice a park cannot drop is never released.
		release = sl.DroppedAt == nil && sl.OpenedAt != nil && sl.ReleaseTxHash == "" &&
			(sl.Status == models.P2PTradeStatusPendingPayment || sl.Status == models.P2PTradeStatusPaid)
		return nil
	})
	return release, err
}

// handOffPoolOrder is §12.6 converted: the record takes the first release as
// its hash and the intent settles, in one transaction (S22; the orchestrator
// then sums every release, Task 6).
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

// reevaluatePoolOrder is S18 in the watcher (plan C2 P5), under the pool lock
// and the intent's row lock: R = at-treasury units, pending = kept Pending
// slices. Covered: drop what is not needed. Not: park pool_slices_short,
// stating why.
func (s *ITService) reevaluatePoolOrder(ctx context.Context, d *PoolDesk, id uuid.UUID, why string) (bool, error) {
	parked := false
	err := withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var locked models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", id).Error; err != nil {
			return err
		}
		if locked.Status != models.ITStatusDelivering {
			return nil
		}
		// Her sale leg is in flight or done: she is being paid, so the order is
		// never parked (a park opens the refund). The operator sees the alert.
		if leg, err := liveSaleLegOn(ctx, tx, id); err != nil || leg != nil {
			if leg != nil {
				log.Printf("[liquidity] ALERT liquidity_pool_slice_lost_after_send intent=%s leg=%s — not parked", id, leg.ID)
			}
			return err
		}
		released, pending, err := d.orderSlices(tx, id)
		if err != nil {
			return err
		}
		live, ok := new(big.Int).SetString(locked.AssetAmount, 10)
		if !ok {
			return fmt.Errorf("liquidity pool: intent %s has unusable amount %q", id, locked.AssetAmount)
		}
		cov := EvaluatePoolCoverage(live, released, pending, CurrentPoolSettings().MinSliceUnits)
		now := d.now()
		if cov.Covered {
			return d.dropAllSlices(tx, cov.Drop, now)
		}
		all := make([]uuid.UUID, len(pending))
		for i := range pending {
			all[i] = pending[i].ID
		}
		if err := d.dropAllSlices(tx, all, now); err != nil {
			return err
		}
		res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", id, models.ITStatusDelivering).
			Updates(map[string]any{"status": models.ITStatusBuyAwaitingRate,
				"failure_reason": models.ITFailurePoolSlicesShort + ": " + why + "; owed " + cov.Owed.String()})
		parked = res.Error == nil && res.RowsAffected == 1
		if parked {
			log.Printf("[liquidity] ALERT liquidity_pool_slices_short intent=%s owed=%s at_treasury=%s — re-deliver or refund", id, cov.Owed, released)
		}
		return res.Error
	})
	return parked, err
}
