package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PoolSliceOrders is what the reconciler asks about a slice's order.
type PoolSliceOrders interface {
	// SliceOrderPaid: the order is paid (payment_received or later) and the
	// slice still backs it — never a dropped slice. A read, for alerts only.
	SliceOrderPaid(ctx context.Context, slice *models.P2PTrade) (bool, error)
	// SliceOrderHoldsMoney: the order holds her naira, whether or not the
	// slice still backs it. A read, for alerts only.
	SliceOrderHoldsMoney(ctx context.Context, slice *models.P2PTrade) (bool, error)
	// DecideSliceResolve is F21's decision, taken under the intent's row lock
	// (the claim's lock, review M7): true = resolve to the treasury; false =
	// back to the LP, with dropped_at written in the same transaction.
	DecideSliceResolve(ctx context.Context, slice *models.P2PTrade) (bool, error)
	// SliceOpenGone: the slice's open can never land (S44, review I1).
	SliceOpenGone(ctx context.Context, slice *models.P2PTrade) (bool, error)
}

// SetPoolSlices wires the pool's answers and the release key. Called once at
// boot when the pool desk exists.
func (s *P2PTradeService) SetPoolSlices(o PoolSliceOrders, sender ChainSender) {
	s.poolSlices, s.poolSender = o, sender
}

// sliceOrphanable is S44's rule (review I1): a never-noted open cannot land;
// a noted one — with or without a broadcast hash — is gone only when it can
// never land. While it still might, the row is rotated to the back of the
// reconciler's updated_at window (ListNonTerminalTrades reads 200 by
// updated_at ASC), so a stuck open never holds the head of the queue for
// every other P2P trade.
func (s *P2PTradeService) sliceOrphanable(ctx context.Context, t *models.P2PTrade) bool {
	if t.OpenNonce == nil {
		return true
	}
	if s.poolSlices != nil {
		if gone, err := s.poolSlices.SliceOpenGone(ctx, t); err == nil && gone {
			return true
		}
	}
	s.rotateSlice(ctx, t)
	log.Printf("[liquidity] METRIC liquidity_pool_slice_open_unresolved slice=%s nonce=%d", t.ID, *t.OpenNonce)
	if age := s.clock.Now().Sub(t.CreatedAt); age >= sliceOpenUnresolvedAlertAfter {
		// Review M-12: an open the probe can resolve neither way (landed but
		// unreadable, or a nonce never consumed) would otherwise wait in
		// silence; past the slice window an operator looks.
		log.Printf("[liquidity] ALERT liquidity_pool_slice_open_unresolved slice=%s nonce=%d age=%s — the open neither landed nor provably failed",
			t.ID, *t.OpenNonce, age.Truncate(time.Minute))
	}
	return false
}

// sliceOpenUnresolvedAlertAfter is when an unresolved open becomes an alert:
// the slice window, by which any open that will land has landed.
const sliceOpenUnresolvedAlertAfter = poolSliceWindow

// rotateSlice moves a waiting slice to the back of the reconciler's
// updated_at window (review I1, re-review N5).
func (s *P2PTradeService) rotateSlice(ctx context.Context, t *models.P2PTrade) {
	s.db.WithContext(ctx).Model(&models.P2PTrade{}).Where("id = ?", t.ID).Update("updated_at", s.clock.Now())
}

// notePaidSliceLost (review I3): a slice of a paid order left escrow other
// than to the treasury. Until plan C2 there is no automatic remedy; an
// operator must see it at once.
func (s *P2PTradeService) notePaidSliceLost(ctx context.Context, trade *models.P2PTrade, how string) {
	if s.poolSlices == nil {
		return
	}
	if paid, err := s.poolSlices.SliceOrderPaid(ctx, trade); err == nil && paid {
		log.Printf("[liquidity] ALERT liquidity_pool_paid_slice_lost slice=%s intent=%s how=%s", trade.ID, trade.ITIntentID, how)
	}
}

// noteResolvedToLP (review I3, C-2(a)): an LP's dispute sent a slice back to
// the LP while the order holds her naira. F21 always drops the slice it
// resolves to the LP, so SliceOrderPaid (which never counts a dropped slice)
// would stay silent; this asks about the order alone. With the claim decided
// under the same lock it means the order was short-parked or is paid on its
// other slices — either way an operator looks.
func (s *P2PTradeService) noteResolvedToLP(ctx context.Context, trade *models.P2PTrade) {
	if s.poolSlices == nil {
		return
	}
	if holds, err := s.poolSlices.SliceOrderHoldsMoney(ctx, trade); err == nil && holds {
		log.Printf("[liquidity] ALERT liquidity_pool_paid_slice_lost slice=%s intent=%s how=resolved_to_lp", trade.ID, trade.ITIntentID)
	}
}

// reconcileSliceDisputed is F21: an LP raised a dispute on a slice. No
// p2p_disputes row and no P2P dispute UI (S42). The LP is paused and the
// release key resolves at once — to the treasury when the order is paid, back
// to the LP otherwise — through the per-chain Send lock.
func (s *P2PTradeService) reconcileSliceDisputed(ctx context.Context, trade *models.P2PTrade) (ReconcileResult, error) {
	res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ? AND status NOT IN ?", trade.ID, []string{models.P2PTradeStatusReleased, models.P2PTradeStatusCancelled,
			models.P2PTradeStatusResolved, models.P2PTradeStatusDisputed}).
		Updates(map[string]any{"status": models.P2PTradeStatusDisputed, "updated_at": s.clock.Now()})
	if res.Error != nil {
		return ReconcileResult{}, res.Error
	}
	first := res.RowsAffected == 1
	if first {
		log.Printf("[liquidity] ALERT liquidity_pool_slice_disputed slice=%s intent=%s", trade.ID, trade.ITIntentID)
		s.pauseSliceLP(ctx, trade)
	}
	if trade.ReleaseTxHash != "" || s.poolSlices == nil || s.poolSender == nil {
		// Waiting for our resolve to mine (or for the pool to be wired): rotate
		// so a disputed slice never holds the head of the reconciler's window.
		if !first {
			s.rotateSlice(ctx, trade)
		}
		return ReconcileResult{Changed: first, FromStatus: trade.Status, ToStatus: models.P2PTradeStatusDisputed, Reason: "pool slice disputed"}, nil
	}
	// Review M7: decided under the intent's row lock, so an honoured claim
	// racing this cannot be paid against a slice going back to the LP; the
	// unpaid branch drops the slice in that same transaction.
	paid, err := s.poolSlices.DecideSliceResolve(ctx, trade)
	if err != nil {
		return ReconcileResult{}, err
	}
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return ReconcileResult{}, err
	}
	// PrepareAdminResolve's bool is the contract's releaseToBuyer: the buyer
	// of a slice is the conversion treasury.
	hash, err := s.poolSender.Send(ctx, trade.ChainID, func() ([]byte, error) {
		return escrow.PrepareAdminResolve(HashID(trade.ID), paid, s.poolSender.Address())
	})
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if hash != "" {
		s.db.WithContext(wctx).Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("release_tx_hash", hash)
	}
	if err != nil {
		s.rotateSlice(wctx, trade)
		return ReconcileResult{}, fmt.Errorf("auto-resolve pool slice %s: %s", trade.ID, RedactAlertText(err.Error()))
	}
	s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
		"reason": "pool slice dispute auto-resolved", "toTreasury": paid, "tx": hash})
	return ReconcileResult{Changed: true, FromStatus: trade.Status, ToStatus: models.P2PTradeStatusDisputed, Reason: "pool slice auto-resolve sent"}, nil
}

// pauseSliceLP pauses the slice's LP and tells them why (never naming a buyer).
func (s *P2PTradeService) pauseSliceLP(ctx context.Context, trade *models.P2PTrade) {
	var ad models.P2PAd
	if s.pm == nil || s.db.WithContext(ctx).First(&ad, "id = ?", trade.AdID).Error != nil || ad.PMID == nil {
		return
	}
	pm, err := s.pm.AutoPauseLP(ctx, *ad.PMID, "auto-paused: disputed a pool slice")
	if err != nil {
		log.Printf("[liquidity] ALERT liquidity_pool_lp_pause_failed lp=%s err=%s", *ad.PMID, RedactAlertText(err.Error()))
		return
	}
	if pm != nil {
		s.emit(events.LiquidityPausedByAdmin, pm.UserID, "Your liquidity is paused",
			"You disputed a TSA Connect order. Your stock is not offered until support reviews it.",
			map[string]any{"reason": "dispute"})
	}
}

// reconcileSliceResolved settles a resolved slice from the resolve's own
// receipt (never a dispute row): released to the treasury, or refunded to the LP.
func (s *P2PTradeService) reconcileSliceResolved(ctx context.Context, trade *models.P2PTrade) (ReconcileResult, error) {
	if trade.ReleaseTxHash == "" {
		log.Printf("[liquidity] ALERT liquidity_pool_slice_resolved_unknown slice=%s — resolved on-chain without our resolve hash; manual review", trade.ID)
		s.rotateSlice(ctx, trade)
		return ReconcileResult{}, nil
	}
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return ReconcileResult{}, err
	}
	ev, err := escrow.VerifyTradeResolved(trade.ReleaseTxHash, HashID(trade.ID))
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("verify slice resolve: %s", RedactAlertText(err.Error()))
	}
	now := s.clock.Now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := tx.Model(&models.P2PTrade{}).Where("id = ? AND status NOT IN ?", trade.ID,
			[]string{models.P2PTradeStatusReleased, models.P2PTradeStatusCancelled, models.P2PTradeStatusResolved}).
			Updates(map[string]any{"status": models.P2PTradeStatusResolved, "updated_at": now})
		if r.Error != nil || r.RowsAffected == 0 {
			return r.Error
		}
		upd := map[string]any{"reserved": gorm.Expr("CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)", trade.TokenAmount), "updated_at": now}
		if !ev.ReleasedToBuyer {
			upd["remaining"] = gorm.Expr("CAST(remaining AS NUMERIC) + CAST(? AS NUMERIC)", trade.TokenAmount)
		}
		return tx.Model(&models.P2PAd{}).Where("id = ?", trade.AdID).Updates(upd).Error
	})
	if err != nil {
		return ReconcileResult{}, err
	}
	if !ev.ReleasedToBuyer {
		s.noteResolvedToLP(ctx, trade)
	}
	s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
		"reason": "pool slice resolved", "toTreasury": ev.ReleasedToBuyer})
	return ReconcileResult{Changed: true, FromStatus: trade.Status, ToStatus: models.P2PTradeStatusResolved, Reason: "pool slice resolved"}, nil
}

// noteSliceSelfRelease is F22: released on-chain without our AutoRelease
// (the slice never went through releasing). Unpaid: alerted, never forwarded
// (C2). Paid: the USDT reached the treasury early; C2's settle must account
// for it, so it is alerted too (review I3).
func (s *P2PTradeService) noteSliceSelfRelease(ctx context.Context, trade *models.P2PTrade) {
	if trade.Status != models.P2PTradeStatusPendingPayment || s.poolSlices == nil {
		return
	}
	paid, err := s.poolSlices.SliceOrderPaid(ctx, trade)
	switch {
	case err != nil:
	case !paid:
		log.Printf("[liquidity] ALERT liquidity_pool_slice_self_released slice=%s intent=%s — never forwarded automatically (F22)",
			trade.ID, trade.ITIntentID)
	default:
		log.Printf("[liquidity] ALERT liquidity_pool_paid_slice_lost slice=%s intent=%s how=self_released — the USDT is at the treasury; plan C2 accounts for it",
			trade.ID, trade.ITIntentID)
	}
}

// SliceOrderPaid: the slice's order is payment_received or later and the
// slice was not dropped.
func (s *ITService) SliceOrderPaid(ctx context.Context, slice *models.P2PTrade) (bool, error) {
	if slice == nil || slice.DroppedAt != nil {
		return false, nil
	}
	return s.SliceOrderHoldsMoney(ctx, slice)
}

// SliceOrderHoldsMoney: the slice's order holds her naira (poolOrderHoldsMoney).
func (s *ITService) SliceOrderHoldsMoney(ctx context.Context, slice *models.P2PTrade) (bool, error) {
	if slice == nil || slice.ITIntentID == nil {
		return false, nil
	}
	var i models.ITIntent
	if err := s.db.WithContext(ctx).Select("id", "status").First(&i, "id = ?", *slice.ITIntentID).Error; err != nil {
		return false, err
	}
	return poolOrderHoldsMoney(i.Status), nil
}

// poolOrderHoldsMoney: her naira is in. A buy_awaiting_rate park counts
// (re-review N6) — a pool_slices_short park drops every slice, so only a
// park whose slices still back it reaches here with dropped_at NULL.
func poolOrderHoldsMoney(status string) bool {
	switch status {
	case models.ITStatusPaymentReceived, models.ITStatusDelivering, models.ITStatusSettled, models.ITStatusBuyAwaitingRate:
		return true
	}
	return false
}

// DecideSliceResolve is F21's decision under the intent's FOR UPDATE (review
// M7): the claim and S18 take the same lock, so whichever commits first wins.
func (s *ITService) DecideSliceResolve(ctx context.Context, slice *models.P2PTrade) (bool, error) {
	if slice == nil || slice.ITIntentID == nil {
		return false, nil
	}
	toTreasury := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var intent models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").First(&intent, "id = ?", *slice.ITIntentID).Error; err != nil {
			return err
		}
		var cur models.P2PTrade
		if err := tx.Select("id", "dropped_at").First(&cur, "id = ?", slice.ID).Error; err != nil {
			return err
		}
		toTreasury = poolOrderHoldsMoney(intent.Status) && cur.DroppedAt == nil
		if !toTreasury && cur.DroppedAt == nil {
			return tx.Model(&models.P2PTrade{}).Where("id = ?", cur.ID).Update("dropped_at", time.Now().UTC()).Error
		}
		return nil
	})
	return toTreasury, err
}

// SliceOpenGone is S44 (review I1): the open can never land. A slice counts
// as opened whenever its trade exists on-chain, so "gone" needs more than a
// consumed nonce: the nonce may have been stepped back and reused by another
// transaction (Task 7), and a receipt only speaks for the hash we recorded.
// Order matters:
//   - our recorded open mined and succeeded → never an orphan;
//   - it mined and reverted → a candidate once the receipt is poolProveDepth
//     deep;
//   - no receipt → a candidate once the treasury's nonce is consumed
//     poolProveDepth blocks deep (re-review N4: the escrow endpoint, P10 allows
//     a different client, has had the same blocks to show our open);
//
// and a candidate is gone only when a final ReadTrade still finds no trade and
// the probe never failed over mid-decision (S43). Anything unreadable waits.
func (s *ITService) SliceOpenGone(ctx context.Context, slice *models.P2PTrade) (bool, error) {
	if slice == nil || slice.OpenNonce == nil {
		return true, nil
	}
	d := s.poolDesk
	if d == nil || d.c.Token.ChainID != slice.ChainID {
		return false, nil
	}
	gen := d.c.Probe.EndpointGeneration()
	head, err := d.c.Probe.BlockNumber(ctx)
	if err != nil || head < poolProveDepth {
		return false, err
	}
	deep := head - poolProveDepth
	landedDeep := false
	if slice.OpenTxHash != "" {
		r, err := d.c.Probe.TransactionReceipt(ctx, common.HexToHash(slice.OpenTxHash))
		if err != nil && !errors.Is(err, ethereum.NotFound) {
			return false, err
		}
		if r != nil {
			if r.Status != types.ReceiptStatusFailed {
				return false, nil
			}
			if r.BlockNumber == nil || r.BlockNumber.Uint64() > deep {
				return false, nil // reverted, but not deep yet
			}
			landedDeep = true
		}
	}
	if !landedDeep {
		latest, err := d.c.Probe.NonceAt(ctx, common.HexToAddress(d.c.Treasury.Address()), new(big.Int).SetUint64(deep))
		if err != nil || latest <= uint64(*slice.OpenNonce) {
			return false, err // pending, dropped but reusable, shallow, or unreadable: wait
		}
	}
	t, err := d.c.Escrow.ReadTrade(HashID(slice.ID))
	if err != nil || t.Exists {
		return false, err
	}
	if err := endpointSteady(d.c.Probe, gen); err != nil {
		return false, err
	}
	return true, nil
}
