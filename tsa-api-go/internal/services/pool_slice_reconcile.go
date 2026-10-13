package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
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
	// SliceOpenGone: the slice's open can never land (S44, review I1).
	SliceOpenGone(ctx context.Context, slice *models.P2PTrade) (bool, error)
	// SliceHashReverted: the slice's recorded release_tx_hash is a real hash
	// whose receipt is reverted `need` deep — the one proof that lets a
	// recorded hash be cleared or replaced (re-review I-A). "" or a claim is
	// never "reverted" (re-review I-C).
	SliceHashReverted(ctx context.Context, slice *models.P2PTrade) (bool, error)
	// ClaimSliceResolve is F21's decision AND its exclusive right to send, in
	// one transaction under the intent's then the slice's row lock (the
	// per-slice serialisation C1 already uses): returns the decision and a
	// claim token written into release_tx_hash, or claim "" when a hash or
	// another claim is already recorded (that caller must not send).
	ClaimSliceResolve(ctx context.Context, slice *models.P2PTrade) (toTreasury bool, claim string, err error)
	// FindSliceResolve (review I-3): F21's resolve of the slice found from
	// the escrow's own TradeResolved log, with its direction read from that
	// receipt — for a slice whose recorded hash is missing or still a claim.
	// "" means not found.
	FindSliceResolve(ctx context.Context, slice *models.P2PTrade) (string, *P2PTradeResolvedEvent, error)
	// SliceResolveClaimAbandoned (review I-3): the slice's resolve claim is
	// older than sliceResolveClaimStaleAfter and the chain proves no resolve
	// landed and none is in flight — the only evidence that clears a claim.
	SliceResolveClaimAbandoned(ctx context.Context, slice *models.P2PTrade) (bool, error)
}

// sliceResolveClaimPrefix marks release_tx_hash as claimed by one F21 sender
// (re-review I-A): "resolving:<unix seconds>". Never a hash.
const sliceResolveClaimPrefix = "resolving:"

// sliceResolveClaimStaleAfter: a claim this old whose resolve neither landed
// nor sits in the release key's mempool was never sent (a crash between the
// claim's commit and Send) or was dropped (review I-3).
const sliceResolveClaimStaleAfter = 30 * time.Minute

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
	if age := s.clock.Now().Sub(t.CreatedAt); age >= sliceOpenUnresolvedAlertAfter && s.sliceOpenAlerts.due(t.ID, s.clock.Now()) {
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
// than to the treasury. The settle engine re-evaluates the order (S18: carry
// on, or park pool_slices_short for re-delivery or the S37 override); an
// operator sees it at once.
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
	if s.poolSlices == nil || s.poolSender == nil {
		// Waiting for the pool to be wired: rotate so a disputed slice never
		// holds the head of the reconciler's window.
		if !first {
			s.rotateSlice(ctx, trade)
		}
		return ReconcileResult{Changed: first, FromStatus: trade.Status, ToStatus: models.P2PTradeStatusDisputed, Reason: "pool slice disputed"}, nil
	}
	if trade.ReleaseTxHash != "" {
		// I-7 under re-review I-A's rule: a recorded hash (a resolve, a claim, a
		// stale release) is replaced only once proven reverted — or, for a
		// claim, proven abandoned (review I-3); otherwise wait.
		if !s.sliceHashProvenReverted(ctx, trade) && !s.sliceResolveClaimAbandoned(ctx, trade) {
			if strings.HasPrefix(trade.ReleaseTxHash, sliceResolveClaimPrefix) || s.clock.Now().Sub(trade.UpdatedAt) > 30*time.Minute {
				s.noteSliceUnresolved(ctx, trade, "liquidity_pool_resolve_unresolved")
			}
			s.rotateSlice(ctx, trade)
			return ReconcileResult{Changed: first, FromStatus: trade.Status, ToStatus: models.P2PTradeStatusDisputed, Reason: "pool slice resolve in flight"}, nil
		}
		res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).Where("id = ? AND release_tx_hash = ?", trade.ID, trade.ReleaseTxHash).
			Update("release_tx_hash", "")
		if res.Error != nil || res.RowsAffected != 1 {
			s.rotateSlice(ctx, trade) // changed under us: the next pass re-reads it
			return ReconcileResult{Changed: first, FromStatus: trade.Status, ToStatus: models.P2PTradeStatusDisputed, Reason: "pool slice resolve moved"}, res.Error
		}
		log.Printf("[liquidity] ALERT liquidity_pool_resolve_cleared slice=%s intent=%s was=%q — no resolve landed or is in flight; deciding again",
			trade.ID, trade.ITIntentID, trade.ReleaseTxHash)
		trade.ReleaseTxHash = ""
	}
	// Re-review I-A: decide AND claim the send under the intent's then the
	// slice's row lock; a concurrent reconciler finds the claim and stops. The
	// unpaid branch drops the slice in that same transaction (review M7).
	paid, claim, err := s.poolSlices.ClaimSliceResolve(ctx, trade)
	if err != nil {
		return ReconcileResult{}, err
	}
	if claim == "" {
		s.rotateSlice(ctx, trade)
		return ReconcileResult{Changed: first, FromStatus: trade.Status, ToStatus: models.P2PTradeStatusDisputed, Reason: "pool slice resolve claimed elsewhere"}, nil
	}
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return ReconcileResult{}, err
	}
	// PrepareAdminResolve's bool is the contract's releaseToBuyer: the buyer
	// of a slice is the conversion treasury.
	// A slice's resolve is one of the pool's reserved release-key calls.
	hash, err := s.poolSender.Send(ReservedSend(ctx), trade.ChainID, func() ([]byte, error) {
		return escrow.PrepareAdminResolve(HashID(trade.ID), paid, s.poolSender.Address())
	})
	if hash == "" {
		hash = uncertainTxHash(err)
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	switch {
	case hash != "": // the real hash replaces our own claim, never anything else
		s.db.WithContext(wctx).Model(&models.P2PTrade{}).Where("id = ? AND release_tx_hash = ?", trade.ID, claim).Update("release_tx_hash", hash)
	case errors.Is(err, ErrTxNotBroadcast): // provably unsent: release the claim
		s.db.WithContext(wctx).Model(&models.P2PTrade{}).Where("id = ? AND release_tx_hash = ?", trade.ID, claim).Update("release_tx_hash", "")
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
		s.emit(events.LiquidityPausedByAdmin, pm.UserID, uuid.Nil, "Your liquidity is paused",
			"You disputed a TSA Connect order. Your stock is not offered until support reviews it.",
			map[string]any{"reason": "dispute"})
	}
}

// reconcileSliceResolved settles a resolved slice from the resolve's own
// receipt (never a dispute row): released to the treasury, or refunded to the LP.
func (s *P2PTradeService) reconcileSliceResolved(ctx context.Context, trade *models.P2PTrade) (ReconcileResult, error) {
	var ev *P2PTradeResolvedEvent
	if trade.ReleaseTxHash == "" || strings.HasPrefix(trade.ReleaseTxHash, sliceResolveClaimPrefix) {
		// Review I-3: our resolve's hash never replaced its claim (or none was
		// recorded): the escrow's own log names it. The found hash replaces
		// the recorded value only if no one changed it meanwhile.
		found := ""
		if s.poolSlices != nil {
			var err error
			if found, ev, err = s.poolSlices.FindSliceResolve(ctx, trade); err != nil {
				log.Printf("[liquidity] resolve scan slice=%s: %s", trade.ID, RedactAlertText(err.Error()))
			}
		}
		if found == "" {
			log.Printf("[liquidity] ALERT liquidity_pool_slice_resolved_unknown slice=%s — resolved on-chain, its resolve not found yet; manual review if this repeats", trade.ID)
			s.rotateSlice(ctx, trade)
			return ReconcileResult{}, nil
		}
		res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).Where("id = ? AND release_tx_hash = ?", trade.ID, trade.ReleaseTxHash).
			Update("release_tx_hash", found)
		if res.Error != nil || res.RowsAffected != 1 {
			s.rotateSlice(ctx, trade)
			return ReconcileResult{}, res.Error
		}
		trade.ReleaseTxHash = found
	} else {
		escrow, _, _, err := s.resolve(trade.ChainID)
		if err != nil {
			return ReconcileResult{}, err
		}
		if ev, err = escrow.VerifyTradeResolved(trade.ReleaseTxHash, HashID(trade.ID)); err != nil {
			return ReconcileResult{}, fmt.Errorf("verify slice resolve: %s", RedactAlertText(err.Error()))
		}
	}
	now := s.clock.Now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
// (the slice never went through releasing). Unpaid, or dropped: alerted,
// never forwarded automatically — an operator pays the LP or hands the USDT
// back. Paid: the USDT reached the treasury early; the settle engine proves it
// and forwards its LP, and it is alerted too (review I3).
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
		log.Printf("[liquidity] ALERT liquidity_pool_paid_slice_lost slice=%s intent=%s how=self_released — the USDT is at the treasury; the settle engine proves and forwards it (F22)",
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

// decideSliceResolveTx is F21's decision under the intent's FOR UPDATE (review
// M7), inside ClaimSliceResolve's transaction: the claim and S18 take the same
// lock, so whichever commits first wins. True = resolve to the treasury;
// false = back to the LP, with dropped_at written in the same transaction.
func (s *ITService) decideSliceResolveTx(tx *gorm.DB, slice *models.P2PTrade) (bool, error) {
	var intent models.ITIntent
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").First(&intent, "id = ?", *slice.ITIntentID).Error; err != nil {
		return false, err
	}
	var cur models.P2PTrade
	if err := tx.Select("id", "dropped_at").First(&cur, "id = ?", slice.ID).Error; err != nil {
		return false, err
	}
	toTreasury := poolOrderHoldsMoney(intent.Status) && cur.DroppedAt == nil
	if !toTreasury && cur.DroppedAt == nil {
		return false, tx.Model(&models.P2PTrade{}).Where("id = ?", cur.ID).Update("dropped_at", time.Now().UTC()).Error
	}
	return toTreasury, nil
}

// ClaimSliceResolve (re-review I-A): F21's decision and its exclusive right to
// send, under the intent's then the slice's row lock — the serialisation C1
// already uses. claim "" when a hash or another claim is recorded.
func (s *ITService) ClaimSliceResolve(ctx context.Context, slice *models.P2PTrade) (bool, string, error) {
	if slice == nil || slice.ITIntentID == nil {
		return false, "", nil
	}
	toTreasury, claim := false, ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if toTreasury, err = s.decideSliceResolveTx(tx, slice); err != nil { // takes the intent's FOR UPDATE
			return err
		}
		var cur models.P2PTrade
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "release_tx_hash").First(&cur, "id = ?", slice.ID).Error; err != nil {
			return err
		}
		if cur.ReleaseTxHash != "" {
			return nil
		}
		token := fmt.Sprintf("%s%d", sliceResolveClaimPrefix, time.Now().Unix())
		res := tx.Model(&models.P2PTrade{}).Where("id = ? AND release_tx_hash = ''", slice.ID).Update("release_tx_hash", token)
		if res.Error == nil && res.RowsAffected == 1 {
			claim = token
		}
		return res.Error
	})
	return toTreasury, claim, err
}

// SliceHashReverted (re-review I-A/I-C): the recorded hash is a real hash
// whose receipt failed `need` deep. Read without waiting (review I-8).
func (s *ITService) SliceHashReverted(ctx context.Context, slice *models.P2PTrade) (bool, error) {
	d := s.poolDesk
	if slice == nil || d == nil || d.c.Token.ChainID != slice.ChainID || slice.ReleaseTxHash == "" ||
		strings.HasPrefix(slice.ReleaseTxHash, sliceResolveClaimPrefix) {
		return false, nil
	}
	gen := d.c.Probe.EndpointGeneration()
	need := s.saleDepth()
	return receiptDeep(ctx, d.c.Probe, slice.ReleaseTxHash, types.ReceiptStatusFailed, need, gen)
}

// FindSliceResolve (review I-3): the slice's resolve from the escrow's log.
func (s *ITService) FindSliceResolve(ctx context.Context, slice *models.P2PTrade) (string, *P2PTradeResolvedEvent, error) {
	d := s.poolDesk
	if slice == nil || d == nil || d.c.Token.ChainID != slice.ChainID {
		return "", nil, nil
	}
	return d.findSliceResolve(ctx, slice)
}

// SliceResolveClaimAbandoned (review I-3) clears a resolve claim only on the
// chain's evidence, all read from one endpoint: the claim is older than
// sliceResolveClaimStaleAfter, the trade is still Disputed, the release key
// has no transaction pending (its pending nonce equals its nonce at the
// head), no TradeResolved for the trade was ever logged, and — read last,
// after those (re-review N-1) — the trade is still Disputed. A dropped
// resolve that resurfaces later is harmless: every resolve of a slice goes the
// same way (the F21 decision never flips — a to-the-LP decision drops the
// slice, a to-the-treasury one blocks the refund), the escrow accepts only
// the first, and the log scan above records whichever landed.
func (s *ITService) SliceResolveClaimAbandoned(ctx context.Context, slice *models.P2PTrade) (bool, error) {
	d := s.poolDesk
	if slice == nil || !d.settleWired() || d.c.Token.ChainID != slice.ChainID ||
		!strings.HasPrefix(slice.ReleaseTxHash, sliceResolveClaimPrefix) {
		return false, nil
	}
	at, err := strconv.ParseInt(strings.TrimPrefix(slice.ReleaseTxHash, sliceResolveClaimPrefix), 10, 64)
	if err != nil || d.now().Sub(time.Unix(at, 0)) < sliceResolveClaimStaleAfter {
		return false, nil
	}
	gen := d.c.Probe.EndpointGeneration()
	if idle, err := d.releaseKeyIdle(ctx); err != nil || !idle {
		return false, err // a release-key transaction is in flight: it may be this resolve
	}
	if found, err := d.findSliceTx(ctx, slice, topicTradeResolved); err != nil || found != "" {
		return false, err
	}
	// Re-review N-1: read "still Disputed" last, at a block no earlier than the
	// nonce reads — a resolve that mined during the checks (its log not yet
	// indexed) reads as Resolved here.
	t, err := d.c.Escrow.ReadTrade(HashID(slice.ID))
	if err != nil || !t.Exists || t.Status != OnChainP2PStatusDisputed {
		return false, err
	}
	return endpointSteady(d.c.Probe, gen) == nil, nil
}

// releaseKeyIdle: the release key has no transaction pending — its pending
// nonce equals its nonce at the head (walletNoncesAtHead).
func (d *PoolDesk) releaseKeyIdle(ctx context.Context) (bool, error) {
	_, pending, latest, err := walletNoncesAtHead(ctx, d.c.Probe, common.HexToAddress(d.c.Sender.Address()))
	if err != nil {
		return false, err
	}
	return pending == latest, nil
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
