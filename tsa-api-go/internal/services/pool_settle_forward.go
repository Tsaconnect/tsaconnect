package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errPoolForwardRefused = fmt.Errorf("%w: this slice is not forwarded automatically", ErrITBadState)

func (s *ITService) SetPoolForwards(src *P2PTradeClaimSource) { s.poolForwards = src }

// poolForwardRef is the forward's provider_ref for a pool slice: unique per
// slice, so a replay finds its own row (§12.5).
func poolForwardRef(sliceID uuid.UUID) string { return "pool:" + sliceID.String() }

// publish sends an event through the IT service's bus (nil: none).
func (s *ITService) publish(ev events.Event) {
	if s.rateNotifier != nil {
		s.rateNotifier.Publish(ev)
	}
}

// poolProven is §12.5 (S19): every proven slice of a paid order is forwarded,
// each on its own, as soon as its release is proven.
func (s *ITService) poolProven(ctx context.Context, d *PoolDesk, intent *models.ITIntent, proven []models.P2PTrade) {
	if s.poolForwards == nil {
		if len(proven) > 0 {
			log.Printf("[liquidity] ALERT liquidity_pool_forward_unwired intent=%s proven=%d — LPs are not being paid", intent.ID, len(proven))
		}
		return
	}
	for i := range proven {
		if _, err := s.forwardSlice(ctx, d, &proven[i], poolForwardRef(proven[i].ID), false); err != nil && !errors.Is(err, errPoolForwardRefused) {
			log.Printf("[liquidity] forward slice=%s: %s", proven[i].ID, RedactAlertText(err.Error()))
		}
	}
}

// forwardSlice credits the slice's LP its snapshot naira and starts the
// transfer (P7). Automatic (operator=false): only while the order holds her
// naira and the slice still backs it. operator=true is F22's audited path
// (Task 10): the slice must be at the treasury, whatever the order. Both
// lock the intent, then the slice (C1 lock order), and the forward row's
// unique provider_ref makes a replay credit nothing.
func (s *ITService) forwardSlice(ctx context.Context, d *PoolDesk, sl *models.P2PTrade, ref string, operator bool) (*models.P2PSettlementForward, error) {
	src := s.poolForwards
	if src == nil || sl.ITIntentID == nil {
		return nil, ErrPoolSettleUnwired
	}
	var ad models.P2PAd
	if err := s.db.WithContext(ctx).First(&ad, "id = ?", sl.AdID).Error; err != nil {
		return nil, err
	}
	if ad.PMID == nil {
		return nil, fmt.Errorf("liquidity pool: slice %s has no LP", sl.ID)
	}
	lp, err := d.c.PM.GetByID(ctx, *ad.PMID)
	if err != nil {
		return nil, err
	}
	var snap models.PoolOrder
	if err := s.db.WithContext(ctx).First(&snap, "intent_id = ? AND generation = ?", *sl.ITIntentID, sl.Generation).Error; err != nil {
		return nil, fmt.Errorf("liquidity pool: no snapshot for slice %s: %w", sl.ID, err)
	}
	rate, ok := new(big.Int).SetString(snap.LPRateMinor, 10)
	paid, okF := ngnToKobo(sl.FiatAmount)
	want := PoolLPPayoutKobo(d.toCanonical(sl.TokenAmount), rate)
	if !ok || !okF || want.Cmp(big.NewInt(paid)) != 0 {
		log.Printf("[liquidity] ALERT liquidity_pool_payout_mismatch slice=%s fiat=%s want_kobo=%s — not forwarded", sl.ID, sl.FiatAmount, want)
		return nil, fmt.Errorf("%w: payout mismatch", errPoolForwardRefused)
	}
	var fwd *models.P2PSettlementForward
	fresh := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var in models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").First(&in, "id = ?", *sl.ITIntentID).Error; err != nil {
			return err
		}
		var cur models.P2PTrade
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, "id = ?", sl.ID).Error; err != nil {
			return err
		}
		switch {
		case !sliceAtTreasury(cur.Status, nil, cur.ReleaseTxHash, cur.CancelReason):
			return errPoolForwardRefused
		case !operator && (cur.DroppedAt != nil || !poolOrderHoldsMoney(in.Status)):
			return errPoolForwardRefused // F22 unpaid: alerted by the reconciler, decided by an operator
		case operator:
			// Review I-5: F22's one durable marker, under this row lock — the
			// LP is paid naira OR handed the USDT back (NotePoolSliceReturned
			// takes the same marker), never both.
			res := tx.Model(&models.P2PTrade{}).Where("id = ? AND COALESCE(cancel_reason, '') = ''", cur.ID).
				Update("cancel_reason", poolF22Paid)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return fmt.Errorf("%w: this slice's F22 exit was already taken (%s)", errPoolForwardRefused, cur.CancelReason)
			}
		}
		var err error
		fwd, fresh, err = src.insertForwardAndCredit(tx, &cur, lp, ref)
		return err
	})
	if err != nil {
		return nil, err
	}
	if fresh {
		s.publish(events.Event{Type: events.LiquidityFill, UserID: lp.UserID, Title: "TSA Connect sold some of your stock",
			Message: fmt.Sprintf("%s %s sold. ₦%s is on its way to your bank.", FormatStableUnits(d.toCanonical(sl.TokenAmount).String()), d.TokenSymbol(), fwd.Amount),
			Data:    map[string]any{"sliceId": sl.ID.String(), "units": FormatStableUnits(d.toCanonical(sl.TokenAmount).String()), "naira": fwd.Amount}})
		log.Printf("[liquidity] METRIC liquidity_pool_fill slice=%s lp=%s naira=%s", sl.ID, lp.ID, fwd.Amount)
	}
	// Review I-2: only the call that created the row starts the transfer.
	// Every retry belongs to the forwarder's own re-drive (5-minute job,
	// attempt ceiling, lookup by reference) — never a re-send per settle tick.
	if fresh && src.forwarder != nil {
		if err := src.forwarder.Forward(ctx, fwd.ID); err != nil {
			log.Printf("[liquidity] forward=%s deferred to the re-drive: %s", fwd.ID, RedactAlertText(err.Error()))
		}
	}
	return fwd, nil
}

// (poolF22Paid / poolF22Returned are declared in pool_coverage.go, Task 3.)

// PoolForwardSettled is the forwarder's onSettled hook: an LP's naira landed.
func (s *ITService) PoolForwardSettled(ctx context.Context, fwd *models.P2PSettlementForward) {
	if !strings.HasPrefix(fwd.ProviderRef, "pool") {
		return
	}
	pm, err := NewPMService(s.db).GetByID(ctx, fwd.PMID)
	if err != nil {
		return
	}
	s.publish(events.Event{Type: events.LiquidityPayoutSent, UserID: pm.UserID, Title: "Payout sent",
		Message: "₦" + fwd.Amount + " from TSA Connect has been paid to your bank.",
		Data:    map[string]any{"forwardId": fwd.ID.String(), "naira": fwd.Amount}})
}

// notePoolPayoutsDelayed tells an LP their payout is held (failed, or
// started/pending past the stale window). Once per forward per process.
func (s *ITService) notePoolPayoutsDelayed(ctx context.Context) {
	var rows []models.P2PSettlementForward
	s.db.WithContext(ctx).Where("provider_ref LIKE ? AND (status = ? OR (status = ? AND created_at < ?))", "pool%",
		models.P2PForwardFailed, models.P2PForwardPending, time.Now().Add(-p2pForwardStaleAfter)).Limit(200).Find(&rows)
	for i := range rows {
		// Durable dedupe (review minor): a job_run_markers claim per forward,
		// at most daily, so a deploy restart never re-notifies the LP.
		if !claimRunAt(ctx, s.db, "pool_payout_delayed:"+rows[i].ID.String(), 24*time.Hour, time.Now()) {
			continue
		}
		if pm, err := NewPMService(s.db).GetByID(ctx, rows[i].PMID); err == nil {
			s.publish(events.Event{Type: events.LiquidityPayoutDelayed, UserID: pm.UserID, Title: "Payout delayed",
				Message: "Your ₦" + rows[i].Amount + " payout from TSA Connect is delayed. It is safe and support is on it.",
				Data:    map[string]any{"forwardId": rows[i].ID.String(), "naira": rows[i].Amount}})
		}
	}
}
