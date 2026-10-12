package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// refundPoolBuy is S37 (plan C2 P11): only payment_received or
// buy_awaiting_rate, and only with zero slices at the treasury in any
// generation — none releasing, none with a broadcast release not proven gone
// (review C-1) — decided under the pool lock and the intent's row lock, the
// locks the settle engine takes before it releases anything. Every live slice
// is dropped for the canceller; if one of them moved under the decision (its
// release flip landed first), the whole refund rolls back.
func (s *ITService) refundPoolBuy(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	d := s.poolDesk
	var from string
	err := withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var intent models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&intent, "id = ?", intentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrStableIntentNotFound
			}
			return err
		}
		if intent.Status != models.ITStatusPaymentReceived && intent.Status != models.ITStatusBuyAwaitingRate {
			return fmt.Errorf("%w: a TSA pool buy is refundable only while paid and undelivered (status %s)", ErrITBadState, intent.Status)
		}
		var slices []models.P2PTrade
		if err := tx.Where("it_intent_id = ?", intentID).Find(&slices).Error; err != nil {
			return err
		}
		at, live := poolRefundSlices(slices)
		if len(at) > 0 {
			return fmt.Errorf("%w: a TSA pool buy with a slice at the treasury (%s) must be delivered, never refunded — re-deliver it",
				ErrITBadState, strings.Join(at, ", "))
		}
		from = intent.Status
		return s.writePoolRefund(ctx, tx, &intent, live, adminID, models.ITAuditStableBuyRefunded, note, "an operator", map[string]any{
			"receivedAmount": intent.ReceivedAmount, "provider": models.ITProviderPool, "fromStatus": intent.Status, "parkedReason": intent.FailureReason})
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[liquidity] METRIC ramp_it_refunded intent=%s provider=tsa_pool from=%s admin=%s", intentID, from, adminID)
	return s.getIntent(ctx, intentID)
}

// writePoolRefund is the write S37's refund and its super-admin override
// share (review m-6), under the caller's pool lock and intent row lock: never
// with a live sale leg (her delivery — review m-7), the intent CAS to
// refunded from the status the caller read, every live slice dropped or the
// whole refund rolled back, the conversion record cancelled, audited.
func (s *ITService) writePoolRefund(ctx context.Context, tx *gorm.DB, intent *models.ITIntent, live []uuid.UUID,
	adminID uuid.UUID, action, note, by string, meta map[string]any) error {
	if leg, err := liveSaleLegOn(ctx, tx, intent.ID); err != nil || leg != nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: a TSA pool buy with a sale in flight is never refunded", ErrITBadState)
	}
	res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", intent.ID, intent.Status).
		Updates(map[string]any{"status": models.ITStatusRefunded, "failure_reason": "refunded by " + by})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: the order moved while the refund was recorded", ErrITBadState)
	}
	d := s.poolDesk
	if err := d.dropAllSlices(tx, live, d.now()); err != nil {
		return err
	}
	if err := markConversion(ctx, tx, intent.ID, []string{models.PaycrestStatusAwaitingPayment, models.PaycrestStatusPending},
		map[string]any{"status": models.PaycrestStatusCancelled, "failure_reason": "refunded in naira by " + by}); err != nil {
		return err
	}
	return auditIntent(tx, intent.ID, adminID, action, note, meta)
}

// poolRefundSlices splits an order's slices for S37: the ones at the
// treasury (refusal evidence, "id8:status") and the live ones to drop.
func poolRefundSlices(slices []models.P2PTrade) (at []string, live []uuid.UUID) {
	for _, sl := range slices {
		switch {
		case sliceAtTreasury(sl.Status, sl.DroppedAt, sl.ReleaseTxHash, sl.CancelReason):
			at = append(at, sl.ID.String()[:8]+":"+sl.Status)
		case sl.DroppedAt == nil && (sl.Status == models.P2PTradeStatusPendingPayment || sl.Status == models.P2PTradeStatusPaid):
			live = append(live, sl.ID)
		}
	}
	return at, live
}

// PayPoolSliceLP is F22's operator exit (P13): an LP self-released a slice
// that does not back an order holding her naira (poolF22Open, checked under
// the locks in forwardSlice); the operator pays the LP at the snapshot rate. Proven release first; the F22 marker (CAS under the slice
// row lock) and the forward's unique trade_id make a second call a refusal,
// never a second payment.
func (s *ITService) PayPoolSliceLP(ctx context.Context, adminID, intentID, sliceID uuid.UUID, note string) (*models.P2PSettlementForward, error) {
	if note = strings.TrimSpace(note); note == "" {
		return nil, ErrITOperatorNoteRequired
	}
	d := s.poolDesk
	if !d.settleWired() {
		return nil, ErrPoolSettleUnwired
	}
	var sl models.P2PTrade
	if err := s.db.WithContext(ctx).First(&sl, "id = ? AND it_intent_id = ?", sliceID, intentID).Error; err != nil || !sl.IsPoolSlice() {
		return nil, ErrTradeNotFound // review m-8: never a slice of another order
	}
	proven, err := d.proveSliceRelease(ctx, &sl, s.saleDepth())
	if err != nil || !proven {
		return nil, fmt.Errorf("%w: the slice's release is not proven at the treasury yet", ErrITBadState)
	}
	fwd, err := s.forwardSlice(ctx, d, &sl, poolF22ForwardRef(sl.ID), true)
	if err != nil {
		return nil, err
	}
	if aerr := auditIntent(s.db.WithContext(ctx), *sl.ITIntentID, adminID, models.ITAuditPoolSliceLPPaid, note,
		map[string]any{"slice": sl.ID.String(), "forward": fwd.ID.String(), "naira": fwd.Amount}); aerr != nil {
		log.Printf("[liquidity] ALERT liquidity_pool_audit_unrecorded slice=%s err=%s", sl.ID, RedactAlertText(aerr.Error()))
	}
	return fwd, nil
}

// NotePoolSliceReturned records that an operator handed a self-released
// slice's USDT back to its LP (P13): audit, plus F22's one durable marker —
// cancel_reason = pool_f22:returned and dropped_at — taken under the slice's
// row lock. It refuses when any forward exists for the slice (automatic or
// F22): an LP gets naira OR the USDT back, never both (review I-5).
func (s *ITService) NotePoolSliceReturned(ctx context.Context, adminID, intentID, sliceID uuid.UUID, txHash, note string) error {
	if note = strings.TrimSpace(note); note == "" {
		return ErrITOperatorNoteRequired
	}
	var pre models.P2PTrade
	if err := s.db.WithContext(ctx).Select("id", "it_intent_id").First(&pre, "id = ? AND it_intent_id = ?", sliceID, intentID).Error; err != nil || !pre.IsPoolSlice() {
		return ErrTradeNotFound // review m-8: never a slice of another order
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// C1 lock order: the intent, then the slice. Re-review I-B: a slice
		// that still backs an order holding her naira (paid, parked,
		// delivering, settled) backs her delivery and is never "returned".
		var in models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").First(&in, "id = ?", *pre.ITIntentID).Error; err != nil {
			return err
		}
		var sl models.P2PTrade
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&sl, "id = ?", sliceID).Error; err != nil {
			return ErrTradeNotFound
		}
		if !poolF22Open(in.Status, sl.DroppedAt) {
			return fmt.Errorf("%w: the order is %s and this slice backs her delivery; F22 \"returned\" is only for a slice that no longer backs a paid order", ErrITBadState, in.Status)
		}
		if sl.Status != models.P2PTradeStatusReleased && sl.Status != models.P2PTradeStatusResolved {
			return fmt.Errorf("%w: slice %s is %s — only USDT that left escrow can be handed back", ErrITBadState, sl.ID, sl.Status)
		}
		var n int64
		if err := tx.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", sl.ID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: the LP was already paid naira for this slice", ErrITBadState)
		}
		res := tx.Model(&models.P2PTrade{}).Where("id = ? AND COALESCE(cancel_reason, '') = ''", sl.ID).
			Updates(map[string]any{"cancel_reason": poolF22Returned, "dropped_at": gorm.Expr("COALESCE(dropped_at, ?)", time.Now().UTC())})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: this slice's F22 exit was already taken (%s)", ErrITBadState, sl.CancelReason)
		}
		return auditIntent(tx, *sl.ITIntentID, adminID, models.ITAuditPoolSliceReturned, note,
			map[string]any{"slice": sl.ID.String(), "returnTx": strings.TrimSpace(txHash)})
	})
}

// ClearPoolSliceRelease is the operator's exit for a stuck slice release
// (review m-5): a slice left releasing (or carrying a release hash) whose
// transaction was dropped from the mempool, or a crash between Send and the
// hash write that left none. The reconciler waits on it forever and the
// expiry canceller skips it; the chain's evidence, all from one endpoint,
// decides:
//   - it is past the reconciler's releasing window;
//   - the escrow still holds the trade Pending or Paid for the treasury;
//   - the recorded hash, if any, is unknown to the node and has no receipt;
//   - the release key has nothing pending;
//   - no TradeReleased for the trade was ever logged.
//
// Then, under the intent's and the slice's row locks and guarded on exactly
// the state read, the slice goes back to paid with no hash and the settle
// engine releases it again. A resurfacing old release is harmless: a slice
// releases only to the treasury, and the escrow accepts one release.
func (s *ITService) ClearPoolSliceRelease(ctx context.Context, adminID, intentID, sliceID uuid.UUID, note string) error {
	if note = strings.TrimSpace(note); note == "" {
		return ErrITOperatorNoteRequired
	}
	d := s.poolDesk
	if !d.settleWired() {
		return ErrPoolSettleUnwired
	}
	var sl models.P2PTrade
	if err := s.db.WithContext(ctx).First(&sl, "id = ? AND it_intent_id = ?", sliceID, intentID).Error; err != nil || !sl.IsPoolSlice() {
		return ErrTradeNotFound
	}
	if !poolSliceReleaseStuck(&sl, time.Now()) {
		return fmt.Errorf("%w: slice %s is %s (tx %q) — not a release stuck past %s", ErrITBadState, sl.ID, sl.Status, sl.ReleaseTxHash, releasingStaleAfter)
	}
	if reason, err := d.releaseClearEvidence(ctx, &sl); err != nil || reason != "" {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: %s — not cleared", ErrITBadState, reason)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var in models.ITIntent // C1 lock order: the intent, then the slice
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&in, "id = ?", intentID).Error; err != nil {
			return err
		}
		res := tx.Model(&models.P2PTrade{}).Where("id = ? AND status = ? AND release_tx_hash = ?", sl.ID, sl.Status, sl.ReleaseTxHash).
			Updates(map[string]any{"status": models.P2PTradeStatusPaid, "release_tx_hash": "", "updated_at": time.Now().UTC()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: the slice moved while its release was checked — look again", ErrITBadState)
		}
		log.Printf("[liquidity] ALERT liquidity_pool_release_cleared slice=%s intent=%s was=%s tx=%q admin=%s — released again on the next pass",
			sl.ID, intentID, sl.Status, sl.ReleaseTxHash, adminID)
		return auditIntent(tx, intentID, adminID, models.ITAuditPoolSliceReleaseCleared, note,
			map[string]any{"slice": sl.ID.String(), "fromStatus": sl.Status, "releaseTx": sl.ReleaseTxHash})
	})
}

// poolSliceReleaseStuck: a release in flight (releasing, or a recorded release
// hash that is not a resolve claim) past the reconciler's releasing window.
func poolSliceReleaseStuck(sl *models.P2PTrade, now time.Time) bool {
	inFlight := sl.Status == models.P2PTradeStatusReleasing ||
		((sl.Status == models.P2PTradeStatusPendingPayment || sl.Status == models.P2PTradeStatusPaid) &&
			sl.ReleaseTxHash != "" && !strings.HasPrefix(sl.ReleaseTxHash, sliceResolveClaimPrefix))
	return inFlight && now.Sub(sl.UpdatedAt) > releasingStaleAfter
}

// releaseClearEvidence is ClearPoolSliceRelease's chain check: "" when the
// release can neither land nor has landed, else why not.
func (d *PoolDesk) releaseClearEvidence(ctx context.Context, sl *models.P2PTrade) (string, error) {
	gen := d.c.Probe.EndpointGeneration()
	if h := sl.ReleaseTxHash; h != "" {
		known, err := txHashKnown(ctx, d.c.Probe, h)
		if err != nil {
			return "", err
		}
		if known {
			return "the node still knows " + h, nil
		}
		if r, err := d.c.Probe.TransactionReceipt(ctx, common.HexToHash(h)); !errors.Is(err, ethereum.NotFound) {
			if err != nil {
				return "", err
			}
			if r != nil {
				return h + " has a receipt — the reconciler decides it", nil
			}
		}
	}
	idle, err := d.releaseKeyIdle(ctx)
	if err != nil {
		return "", err
	}
	if !idle {
		return "the release key has a transaction pending", nil
	}
	found, err := d.findReleaseTx(ctx, sl)
	if err != nil {
		return "", err
	}
	if found != "" {
		return "a release of this trade is logged in " + found, nil
	}
	// Re-review N-1: the escrow's view is read last, at a block no earlier
	// than the nonce reads — a release that mined during the checks (its log
	// not yet indexed) reads as Released here.
	t, err := d.c.Escrow.ReadTrade(HashID(sl.ID))
	if err != nil {
		return "", err
	}
	if !t.Exists || (t.Status != OnChainP2PStatusPending && t.Status != OnChainP2PStatusPaid) || !SameEVMAddress(t.Buyer.Hex(), d.c.Treasury.Address()) {
		return fmt.Sprintf("the escrow holds the trade as %v — the reconciler settles it", t.Status), nil
	}
	if err := endpointSteady(d.c.Probe, gen); err != nil {
		return "", err
	}
	return "", nil
}

// RefundPoolBuyOverride is S37's super-admin override (spec S37, review I-6):
// the exit from a pool_slices_short park that re-delivery cannot fill. Every
// at-treasury slice must already be released and forwarded (the LPs are paid
// first); the buyer's naira is then marked refunded and the USDT those slices
// moved stays with the company in the treasury (booked in the audit row and
// swept like any surplus, R4).
func (s *ITService) RefundPoolBuyOverride(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	if note = strings.TrimSpace(note); note == "" {
		return nil, ErrITOperatorNoteRequired
	}
	d := s.poolDesk
	if !d.settleWired() {
		return nil, ErrPoolSettleUnwired
	}
	held := new(big.Int)
	err := withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var intent models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&intent, "id = ?", intentID).Error; err != nil {
			return err
		}
		if !isPoolBuy(&intent) || intent.Status != models.ITStatusBuyAwaitingRate ||
			!strings.HasPrefix(intent.FailureReason, models.ITFailurePoolSlicesShort) {
			return fmt.Errorf("%w: the override exits only a %s park", ErrITBadState, models.ITFailurePoolSlicesShort)
		}
		var slices []models.P2PTrade
		if err := tx.Where("it_intent_id = ?", intentID).Find(&slices).Error; err != nil {
			return err
		}
		for _, sl := range slices {
			if !sliceAtTreasury(sl.Status, sl.DroppedAt, sl.ReleaseTxHash, sl.CancelReason) {
				continue
			}
			var n int64
			if err := tx.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", sl.ID).Count(&n).Error; err != nil {
				return err
			}
			if (sl.Status != models.P2PTradeStatusReleased && sl.Status != models.P2PTradeStatusResolved) || n == 0 {
				return fmt.Errorf("%w: slice %s is not yet released and its LP forwarded — wait for the books pass", ErrITBadState, sl.ID)
			}
			held.Add(held, d.toCanonical(sl.TokenAmount))
		}
		_, live := poolRefundSlices(slices)
		return s.writePoolRefund(ctx, tx, &intent, live, adminID, models.ITAuditPoolRefundOverride, note, "a super-admin override",
			map[string]any{"receivedAmount": intent.ReceivedAmount, "treasuryHeldUnits": held.String()})
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[liquidity] ALERT liquidity_pool_refund_override intent=%s admin=%s treasury_held_units=%s — company holds that USDT; sweep it", intentID, adminID, held)
	return s.getIntent(ctx, intentID)
}

// poolF22Open: F22's operator exits (pay the LP, or record the USDT handed
// back) apply to a slice that does not back an order holding her naira —
// the order is unpaid or terminal, or the slice was dropped (review I-1, m-2).
// A slice still backing a paid order is paid by the automatic forward only.
func poolF22Open(orderStatus string, droppedAt *time.Time) bool {
	return droppedAt != nil || !poolOrderHoldsMoney(orderStatus)
}

// poolOrderActions are the operator actions the server allows right now (P20).
func (s *ITService) poolOrderActions(ctx context.Context, intent *models.ITIntent) []string {
	out := []string{}
	var slices []models.P2PTrade
	if err := s.db.WithContext(ctx).Where("it_intent_id = ?", intent.ID).Find(&slices).Error; err != nil {
		return out // unreadable: offer nothing
	}
	atTreasury, live := false, false
	for _, sl := range slices {
		atTreasury = atTreasury || sliceAtTreasury(sl.Status, sl.DroppedAt, sl.ReleaseTxHash, sl.CancelReason)
		live = live || sl.Status == models.P2PTradeStatusPendingPayment || sl.Status == models.P2PTradeStatusPaid ||
			sl.Status == models.P2PTradeStatusReleasing || sl.Status == models.P2PTradeStatusDisputed
	}
	switch intent.Status {
	case models.ITStatusPaymentReceived:
		if !atTreasury {
			out = append(out, "refund")
		}
	case models.ITStatusBuyAwaitingRate:
		if !atTreasury {
			out = append(out, "refund")
		}
		if strings.HasPrefix(intent.FailureReason, models.ITFailurePoolSlicesShort) && !live {
			out = append(out, "redeliver")
			if atTreasury {
				out = append(out, "refund-override") // S37's super-admin exit (review I-6)
			}
		}
	case models.ITStatusDelivering:
		if leg, err := s.liveSaleLeg(ctx, intent.ID); err == nil && leg != nil && leg.Status == models.RampLegStatusPending &&
			time.Since(leg.CreatedAt) > s.treasuryLimits().sendStall {
			out = append(out, "resend") // allowed by the server only when its evidence allows (resendSale)
		}
	}
	return out
}
