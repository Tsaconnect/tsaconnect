package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// OrderMcgpReleasePlan is who is paid, and how much, when a hold is released.
// SellerAmount + BuyerAmount must not exceed the debit; the remainder stays
// in the contract as platform excess. BuyerAmount > 0 makes it a split: the
// buyer keeps that share in the buckets it came from (see the service doc).
type OrderMcgpReleasePlan struct {
	Reason       string
	SellerWallet string
	SellerAmount *big.Int
	BuyerAmount  *big.Int
}

// Release decides a held payment in the seller's favour (or split), applying
// applyOrder to the order in the SAME transaction that claims the payment, so
// the order's outcome and the payment's intent can never disagree. The chain
// work follows in the background; a failure there is retried by the
// reconciler. A payment already releasing or released returns
// ErrOrderMcgpAlreadyDecided (and the payment) without doing anything.
func (s *OrderSpendablePaymentService) Release(ctx context.Context, orderID uuid.UUID, plan OrderMcgpReleasePlan, applyOrder func(tx *gorm.DB) error) (*models.OrderMcgpPayment, error) {
	return s.release(ctx, orderID, plan, applyOrder, false)
}

// release is Release; inline drives the chain work in the caller (the
// reconciler) instead of a goroutine.
func (s *OrderSpendablePaymentService) release(ctx context.Context, orderID uuid.UUID, plan OrderMcgpReleasePlan, applyOrder func(tx *gorm.DB) error, inline bool) (*models.OrderMcgpPayment, error) {
	if !s.IsEnabled() {
		return nil, ErrOrderMcgpDisabled
	}
	pay, err := s.paymentForOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	switch pay.Status {
	case models.OrderMcgpReleasing, models.OrderMcgpReleased:
		return pay, ErrOrderMcgpAlreadyDecided
	case models.OrderMcgpHeld:
	default:
		return pay, ErrOrderMcgpBadState
	}

	amount, _ := new(big.Int).SetString(pay.McgpAmount, 10)
	seller := new(big.Int).Set(bigOrZeroInt(plan.SellerAmount))
	buyer := new(big.Int).Set(bigOrZeroInt(plan.BuyerAmount))
	if amount == nil || seller.Sign() < 0 || buyer.Sign() < 0 ||
		new(big.Int).Add(seller, buyer).Cmp(amount) > 0 {
		return nil, fmt.Errorf("order payment: release plan pays more than was held")
	}
	if belowMinCredit(s.readLimits(ctx), seller) {
		if buyer.Sign() == 0 {
			// A FULL release below the contract's minimum credit would settle
			// the buyer's MCGP and pay the seller nothing. That happens only if
			// minCredit was raised after the order was funded (Fund refuses
			// dust). Leave it held for an admin, who can refund it.
			alertf("order-mcgp-below-min:"+orderID.String(),
				"[order-mcgp] ALERT order_mcgp_payout_below_min=1 order=%s seller_amount=%s — held for admin review", orderID, seller)
			return pay, ErrOrderMcgpPayoutBelowMin
		}
		// In a split the seller's share is a small slice (3% at expiry);
		// below the minimum it is dust and stays with the platform rather
		// than stalling the buyer's share.
		log.Printf("[order-mcgp] order=%s split seller share %s below minCredit; left with the platform", orderID, seller)
		seller.SetInt64(0)
	}
	if seller.Sign() > 0 && !common.IsHexAddress(strings.TrimSpace(plan.SellerWallet)) {
		return nil, ErrOrderMcgpSellerWallet
	}

	now := time.Now()
	updates := map[string]any{
		"status":         models.OrderMcgpReleasing,
		"decided_at":     &now,
		"release_reason": plan.Reason,
		"seller_wallet":  strings.TrimSpace(plan.SellerWallet),
		"seller_amount":  seller.String(),
		"buyer_amount":   buyer.String(),
	}
	if err := s.claim(ctx, pay.ID, models.OrderMcgpHeld, updates, applyOrder); err != nil {
		return nil, err
	}
	return s.afterClaim(ctx, pay.ID, orderID, inline)
}

// Refund decides a held payment in the buyer's favour: reverseDebit restores
// each bucket exactly as drawn. applyOrder runs in the claiming transaction,
// as for Release. A payment already reversing or reversed returns
// ErrOrderMcgpAlreadyDecided.
func (s *OrderSpendablePaymentService) Refund(ctx context.Context, orderID uuid.UUID, applyOrder func(tx *gorm.DB) error) (*models.OrderMcgpPayment, error) {
	return s.refund(ctx, orderID, "", applyOrder, false)
}

func (s *OrderSpendablePaymentService) refund(ctx context.Context, orderID uuid.UUID, reason string, applyOrder func(tx *gorm.DB) error, inline bool) (*models.OrderMcgpPayment, error) {
	if !s.IsEnabled() {
		return nil, ErrOrderMcgpDisabled
	}
	pay, err := s.paymentForOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	switch pay.Status {
	case models.OrderMcgpReversing, models.OrderMcgpReversed:
		return pay, ErrOrderMcgpAlreadyDecided
	case models.OrderMcgpHeld:
	default:
		return pay, ErrOrderMcgpBadState
	}
	now := time.Now()
	updates := map[string]any{"status": models.OrderMcgpReversing, "decided_at": &now}
	if reason != "" {
		updates["release_reason"] = reason
	}
	if err := s.claim(ctx, pay.ID, models.OrderMcgpHeld, updates, applyOrder); err != nil {
		return nil, err
	}
	if reason != models.OrderMcgpReleaseEscrowExpired {
		s.watchRefundCycles(ctx, pay.BuyerID)
	}
	return s.afterClaim(ctx, pay.ID, orderID, inline)
}

// claim moves the payment out of `from` and applies the order's outcome, both
// or neither. Losing the race to another claim is ErrOrderMcgpBadState.
func (s *OrderSpendablePaymentService) claim(ctx context.Context, payID uuid.UUID, from string, updates map[string]any, applyOrder func(tx *gorm.DB) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.OrderMcgpPayment{}).Where("id = ? AND status = ?", payID, from).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrOrderMcgpBadState
		}
		if applyOrder != nil {
			return applyOrder(tx)
		}
		return nil
	})
}

// afterClaim runs the decided chain work: inline for the reconciler (one row
// at a time, so a batch of expiries cannot pile up on the operator signing
// lock and starve bills and IT), in the background for a user request.
func (s *OrderSpendablePaymentService) afterClaim(ctx context.Context, payID, orderID uuid.UUID, inline bool) (*models.OrderMcgpPayment, error) {
	if inline || s.driveInline {
		if err := s.driveByID(ctx, payID); err != nil {
			log.Printf("[order-mcgp] order=%s chain work deferred to the reconciler: %v", orderID, err)
		}
	} else {
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), orderMcgpGrace)
			defer cancel()
			if err := s.driveByID(bg, payID); err != nil {
				log.Printf("[order-mcgp] order=%s chain work deferred to the reconciler: %v", orderID, err)
			}
		}()
	}
	return s.paymentForOrder(ctx, orderID)
}

func (s *OrderSpendablePaymentService) driveByID(ctx context.Context, payID uuid.UUID) error {
	var pay models.OrderMcgpPayment
	if err := s.db.WithContext(ctx).First(&pay, "id = ?", payID).Error; err != nil {
		return err
	}
	return s.drive(ctx, &pay)
}

// drive advances a decided payment on-chain. Every chain read that decides
// a step happens inside OperatorSequence, so two drivers of the same row (a
// request's goroutine and the reconciler) act one after the other on fresh
// state; and each step is itself refused on-chain if already done.
func (s *OrderSpendablePaymentService) drive(ctx context.Context, pay *models.OrderMcgpPayment) error {
	switch pay.Status {
	case models.OrderMcgpReleasing:
		if pay.IsSplit() {
			return s.driveSplit(ctx, pay)
		}
		return s.driveRelease(ctx, pay)
	case models.OrderMcgpReversing:
		return s.driveReverse(ctx, pay)
	}
	return nil
}

// sellerCredit is the seller's share still to credit, or nil when there is
// none or it already landed.
func (s *OrderSpendablePaymentService) sellerCredit(ctx context.Context, pay *models.OrderMcgpPayment) (*big.Int, error) {
	amount, ok := new(big.Int).SetString(strings.TrimSpace(pay.SellerAmount), 10)
	if !ok || amount.Sign() <= 0 {
		return nil, nil
	}
	used, err := s.swap.UsedCreditRef(ctx, orderMcgpSellerCreditRef(pay.OrderID))
	if err != nil {
		return nil, fmt.Errorf("read credit ref: %w", err)
	}
	if used {
		return nil, nil
	}
	return amount, nil
}

// windowMargin is headroom kept back from a rolling window. The window is
// read from latest state, but another operator transaction (a bill or IT
// credit/debit) may already be broadcast and not yet mined — it counts
// against the window when it lands, ahead of ours.
func windowMargin(capAmt *big.Int) *big.Int {
	if capAmt == nil {
		return new(big.Int)
	}
	return new(big.Int).Div(capAmt, big.NewInt(orderMcgpWindowMarginDiv))
}

// creditCanLand checks, with fresh reads, everything creditPremium checks
// that can make it revert: pause, the per-tx cap, the minimum, and the
// rolling window it shares with IT credits (less a safety margin). It is
// called inside OperatorSequence, right before the settle/credit decision, so
// no operator transaction of ours can intervene. A settle is never sent while
// the credit that must follow it could not land.
//
// Runbook: if the owner raises minCredit or lowers maxCreditPerTx after a
// release was decided, the row waits in `releasing` with this reason and the
// order_mcgp_stuck alert fires after an hour. Nothing has moved; an admin
// either restores the cap or has the order refunded out of band.
//
// enforceShare also holds checkout to its share of the credit window (see
// order_spendable_abuse.go); a release waiting on it is retried each tick.
func (s *OrderSpendablePaymentService) creditCanLand(ctx context.Context, amount *big.Int, enforceShare bool) error {
	// Headroom and caps are independent reads: fetched together, so the
	// signing lock is held for one round trip rather than several.
	var head WindowHeadroom
	var lim SpendableLimits
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		if head, err = s.swap.CreditHeadroom(gctx); err != nil {
			return fmt.Errorf("read headroom: %v", err)
		}
		return nil
	})
	g.Go(func() (err error) {
		// Fresh, not cached: a cap lowered since the last read would make the
		// credit revert AFTER the settle, which is the case this check exists for.
		if lim, err = s.swap.SpendableLimits(gctx); err != nil {
			return fmt.Errorf("read caps: %v", err)
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		return fmt.Errorf("%w: %v", errOrderMcgpCreditBlocked, err)
	}
	if head.Paused {
		return fmt.Errorf("%w: swap is paused", errOrderMcgpCreditBlocked)
	}
	if head.WindowRemaining != nil {
		usable := new(big.Int).Sub(head.WindowRemaining, windowMargin(head.WindowCap))
		if usable.Cmp(amount) < 0 {
			return fmt.Errorf("%w: credit window has %s left (margin kept), need %s", errOrderMcgpCreditBlocked, head.WindowRemaining, amount)
		}
	}
	if lim.MaxCreditPerTx != nil && lim.MaxCreditPerTx.Sign() > 0 && amount.Cmp(lim.MaxCreditPerTx) > 0 {
		return fmt.Errorf("%w: above maxCreditPerTx %s", errOrderMcgpCreditBlocked, lim.MaxCreditPerTx)
	}
	if belowMinCredit(&lim, amount) {
		return fmt.Errorf("%w: below minCredit %s", errOrderMcgpCreditBlocked, lim.MinCredit)
	}
	if enforceShare {
		return s.checkCreditShare(ctx, head, amount)
	}
	return nil
}

// debitCanLand is creditCanLand for a split's collect debit: pause, the
// per-call cap and the debit window (less the margin). A split reverses the
// whole hold before collecting, and reverseDebit does not give its window
// usage back, so the reverse is only sent once the collect is known to fit.
func (s *OrderSpendablePaymentService) debitCanLand(ctx context.Context, amount *big.Int) error {
	head, err := s.swap.DebitHeadroom(ctx)
	if err != nil {
		return fmt.Errorf("%w: read debit headroom: %v", errOrderMcgpCreditBlocked, err)
	}
	if head.Paused {
		return fmt.Errorf("%w: swap is paused", errOrderMcgpCreditBlocked)
	}
	if head.MaxPerTx != nil && head.MaxPerTx.Sign() > 0 && amount.Cmp(head.MaxPerTx) > 0 {
		return fmt.Errorf("%w: collect %s above maxDebitPerTx %s", errOrderMcgpCreditBlocked, amount, head.MaxPerTx)
	}
	if head.WindowRemaining != nil {
		usable := new(big.Int).Sub(head.WindowRemaining, windowMargin(head.WindowCap))
		if usable.Cmp(amount) < 0 {
			return fmt.Errorf("%w: debit window has %s left (margin kept), need %s", errOrderMcgpCreditBlocked, head.WindowRemaining, amount)
		}
	}
	return nil
}

// creditFirst reports whether the contract already holds enough unaccounted
// MCGP to credit before settling, which leaves no window in which a settled
// debit's value is uncredited.
func (s *OrderSpendablePaymentService) creditFirst(credit *big.Int) bool {
	held, owed, err := s.swap.Backing()
	if err != nil || held == nil || owed == nil {
		return false
	}
	return new(big.Int).Sub(held, owed).Cmp(credit) >= 0
}

// settleAndCredit resolves a Pending debit in the seller's favour: settle
// ref (the order's debit, or a split's collect debit) and credit the seller,
// credit-first when surplus allows. Called inside OperatorSequence.
func (s *OrderSpendablePaymentService) settleAndCredit(ctx context.Context, ops OperatorOps, pay *models.OrderMcgpPayment,
	ref [32]byte, debitUser common.Address, credit *big.Int, settleHashCol string) error {
	first := credit != nil && s.creditFirst(credit)
	if first {
		if err := s.creditSeller(ctx, ops, pay, credit); err != nil {
			return err
		}
	}
	txHash, err := ops.OperatorSettleDebit(ctx, ref)
	if err != nil {
		return s.note(ctx, pay, "settle: "+err.Error())
	}
	s.update(ctx, pay.ID, models.OrderMcgpReleasing, map[string]any{settleHashCol: txHash})
	if _, err := s.swap.VerifyOperatorEvent("PremiumDebitSettled", txHash, debitUser, ref); err != nil {
		return s.note(ctx, pay, "settle verify: "+err.Error())
	}
	if credit != nil && !first {
		return s.creditSeller(ctx, ops, pay, credit)
	}
	return nil
}

func (s *OrderSpendablePaymentService) creditSeller(ctx context.Context, ops OperatorOps, pay *models.OrderMcgpPayment, amount *big.Int) error {
	ref := orderMcgpSellerCreditRef(pay.OrderID)
	txHash, err := ops.OperatorCreditPremium(ctx, pay.SellerWallet, amount, ref)
	if err != nil {
		return s.note(ctx, pay, "credit: "+err.Error())
	}
	s.update(ctx, pay.ID, models.OrderMcgpReleasing, map[string]any{"seller_credit_tx_hash": txHash})
	if _, err := s.swap.VerifyOperatorEvent("PremiumCredited", txHash, common.HexToAddress(pay.SellerWallet), ref); err != nil {
		return s.note(ctx, pay, "credit verify: "+err.Error())
	}
	return nil
}

// creditAfterSettle handles a debit already settled whose seller credit has
// not landed — the seller's value is sitting as platform excess, so it is
// alerted at once rather than after the stuck window. Inside OperatorSequence.
func (s *OrderSpendablePaymentService) creditAfterSettle(ctx context.Context, ops OperatorOps, pay *models.OrderMcgpPayment, credit *big.Int) error {
	alertf("order-mcgp-settled-uncredited:"+pay.OrderID.String(),
		"[order-mcgp] ALERT order_mcgp_settled_not_credited=1 order=%s seller=%s amount=%s reason=%q",
		pay.OrderID, pay.SellerWallet, credit, pay.FailureReason)
	// No checkout-share check here: the buyer's MCGP is already settled, so
	// the seller's credit goes ahead of reserving window for others.
	if err := s.creditCanLand(ctx, credit, false); err != nil {
		return s.note(ctx, pay, err.Error())
	}
	return s.creditSeller(ctx, ops, pay, credit)
}

// readReleaseState reads the debit records under refs and the seller credit
// still owed, concurrently (inside OperatorSequence, see readUints).
func (s *OrderSpendablePaymentService) readReleaseState(ctx context.Context, pay *models.OrderMcgpPayment, refs ...[32]byte) ([]DebitRecord, *big.Int, error) {
	recs := make([]DebitRecord, len(refs))
	var credit *big.Int
	g, gctx := errgroup.WithContext(ctx)
	for i, ref := range refs {
		g.Go(func() error {
			rec, err := s.swap.DebitRecord(gctx, ref)
			if err != nil {
				return fmt.Errorf("read debit: %v", err)
			}
			recs[i] = rec
			return nil
		})
	}
	g.Go(func() (err error) {
		credit, err = s.sellerCredit(gctx, pay)
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, nil, err
	}
	return recs, credit, nil
}

func (s *OrderSpendablePaymentService) driveRelease(ctx context.Context, pay *models.OrderMcgpPayment) error {
	ref := OrderMcgpDebitRef(pay.OrderID)
	err := s.swap.OperatorSequence(ctx, func(ops OperatorOps) error {
		recs, credit, err := s.readReleaseState(ctx, pay, ref)
		if err != nil {
			return s.note(ctx, pay, "release: "+err.Error())
		}
		switch recs[0].State {
		case DebitStatePending:
			if credit != nil {
				if err := s.creditCanLand(ctx, credit, true); err != nil {
					return s.note(ctx, pay, err.Error()) // nothing settled: retry later
				}
			}
			return s.settleAndCredit(ctx, ops, pay, ref, common.HexToAddress(pay.BuyerWallet), credit, "resolve_tx_hash")
		case DebitStateResolved:
			// Settled by an earlier pass whose result was lost. Nothing but
			// this row's own release can resolve the ref: refunds claim
			// `reversing`, which is exclusive with `releasing`.
			if credit != nil {
				return s.creditAfterSettle(ctx, ops, pay, credit)
			}
			return nil
		default:
			alertf("order-mcgp-nodebit:"+pay.OrderID.String(),
				"[order-mcgp] ALERT order_mcgp_release_without_debit=1 order=%s — no debit on-chain under the order's ref", pay.OrderID)
			return rowErrorf("order %s: no debit on-chain to settle", pay.OrderID)
		}
	})
	if err != nil {
		return err
	}
	return s.finishRelease(ctx, pay, "")
}

// errSplitUncollectable ends a split whose non-buyer share cannot be taken
// back from the buyer.
var errSplitUncollectable = errors.New(orderMcgpUncollectablePrefix)

// driveSplit releases a split without crediting the buyer: reverse the whole
// hold (buckets restored exactly), take back the non-buyer share with a
// second debit (credited premium first), settle it and credit the seller.
// Every decision is made inside one OperatorSequence on fresh chain state.
func (s *OrderSpendablePaymentService) driveSplit(ctx context.Context, pay *models.OrderMcgpPayment) error {
	amount, _ := new(big.Int).SetString(pay.McgpAmount, 10)
	keep, _ := new(big.Int).SetString(pay.BuyerAmount, 10)
	if amount == nil || keep == nil {
		return fmt.Errorf("order %s: unusable split amounts", pay.OrderID)
	}
	collect := new(big.Int).Sub(amount, keep)
	buyer := common.HexToAddress(pay.BuyerWallet)
	mainRef, collectRef := OrderMcgpDebitRef(pay.OrderID), orderMcgpCollectRef(pay.OrderID)

	uncollectable := ""
	err := s.swap.OperatorSequence(ctx, func(ops OperatorOps) error {
		recs, credit, err := s.readReleaseState(ctx, pay, mainRef, collectRef)
		if err != nil {
			return s.note(ctx, pay, "split: "+err.Error())
		}
		main, coll := recs[0], recs[1]
		if main.State == DebitStateNone {
			alertf("order-mcgp-nodebit:"+pay.OrderID.String(),
				"[order-mcgp] ALERT order_mcgp_release_without_debit=1 order=%s — no debit on-chain under the order's ref", pay.OrderID)
			return rowErrorf("order %s: no debit on-chain to split", pay.OrderID)
		}
		switch coll.State {
		case DebitStateResolved:
			if credit != nil {
				return s.creditAfterSettle(ctx, ops, pay, credit)
			}
			return nil
		case DebitStatePending:
			if credit != nil {
				if err := s.creditCanLand(ctx, credit, true); err != nil {
					return s.note(ctx, pay, err.Error())
				}
			}
			return s.settleAndCredit(ctx, ops, pay, collectRef, buyer, credit, "collect_settle_tx_hash")
		}

		// Collect debit not yet on-chain. Nothing moves until both the seller
		// credit that ends the split and the collect itself can land.
		g, gctx := errgroup.WithContext(ctx)
		if credit != nil {
			g.Go(func() error { return s.creditCanLand(gctx, credit, true) })
		}
		if collect.Sign() > 0 {
			g.Go(func() error { return s.debitCanLand(gctx, collect) })
		}
		if err := g.Wait(); err != nil {
			return s.note(ctx, pay, err.Error())
		}
		if main.State == DebitStatePending {
			txHash, err := ops.OperatorReverseDebit(ctx, mainRef)
			if err != nil {
				return s.note(ctx, pay, "split reverse: "+err.Error())
			}
			s.update(ctx, pay.ID, models.OrderMcgpReleasing, map[string]any{"resolve_tx_hash": txHash})
			if _, err := s.swap.VerifyOperatorEvent("PremiumDebitReversed", txHash, buyer, mainRef); err != nil {
				return s.note(ctx, pay, "split reverse verify: "+err.Error())
			}
		}
		if collect.Sign() == 0 {
			return nil
		}
		txHash, err := ops.OperatorDebitSpendable(ctx, pay.BuyerWallet, collect, collect, collectRef)
		if err != nil {
			if errors.Is(classifyDebitRevert(err), ErrSpendableInsufficient) {
				if reason, final := s.collectShortfall(ctx, pay, buyer, collect, collectRef, err); final {
					uncollectable = reason
					return errSplitUncollectable
				}
				return s.note(ctx, pay, "split collect refused (insufficient), retrying: "+err.Error())
			}
			return s.note(ctx, pay, "split collect: "+err.Error())
		}
		s.update(ctx, pay.ID, models.OrderMcgpReleasing, map[string]any{"collect_tx_hash": txHash})
		if _, err := s.swap.VerifyOperatorEvent("PremiumDebitPending", txHash, buyer, collectRef); err != nil {
			return s.note(ctx, pay, "split collect verify: "+err.Error())
		}
		return s.settleAndCredit(ctx, ops, pay, collectRef, buyer, credit, "collect_settle_tx_hash")
	})
	if errors.Is(err, errSplitUncollectable) {
		alertf("order-mcgp-uncollectable:"+pay.OrderID.String(),
			"[order-mcgp] ALERT order_mcgp_split_uncollectable=1 order=%s — buyer kept the whole hold; seller share %s unpaid", pay.OrderID, pay.SellerAmount)
		return s.finishRelease(ctx, pay, uncollectable)
	}
	if err != nil {
		return err
	}
	return s.finishRelease(ctx, pay, "")
}

// collectShortfall decides whether an "insufficient premium" refusal of the
// collect debit is final. It is not while the decision is under a day old —
// the balance may be tied up in another pending debit that later reverses, or
// the node answering may lag. After that the chain is re-read: a collect that
// landed meanwhile, or a balance that now covers it, keeps retrying; only a
// confirmed shortfall closes the split.
func (s *OrderSpendablePaymentService) collectShortfall(ctx context.Context, pay *models.OrderMcgpPayment, buyer common.Address,
	collect *big.Int, collectRef [32]byte, cause error) (string, bool) {
	if pay.DecidedAt == nil || time.Since(*pay.DecidedAt) < orderMcgpUncollectableAfter {
		return "", false
	}
	if rec, err := s.swap.DebitRecord(ctx, collectRef); err != nil || rec.State != DebitStateNone {
		return "", false
	}
	if bal, err := s.swap.PremiumBalance(buyer.Hex()); err != nil || bal.Cmp(collect) >= 0 {
		return "", false
	}
	return truncateReason(orderMcgpUncollectablePrefix + ": " + cause.Error()), true
}

// recoverOrphanCollect reopens a split closed as uncollectable whose collect
// debit mined afterwards (a broadcast whose outcome was unknown): the
// buyer's MCGP is sitting in pendingSettle under that ref, so the split is
// completed — settle it and credit the seller.
func (s *OrderSpendablePaymentService) recoverOrphanCollect(ctx context.Context, pay *models.OrderMcgpPayment) (bool, error) {
	rec, err := s.swap.DebitRecord(ctx, orderMcgpCollectRef(pay.OrderID))
	if err != nil || rec.State != DebitStatePending {
		return false, err
	}
	alertf("order-mcgp-orphan-collect:"+pay.OrderID.String(),
		"[order-mcgp] ALERT order_mcgp_orphan_collect=1 order=%s — collect debit mined after the split closed; completing it", pay.OrderID)
	moved, err := s.transition(ctx, pay.ID, models.OrderMcgpReleased, map[string]any{
		"status": models.OrderMcgpReleasing, "failure_reason": "collect debit landed after close; completing split",
	})
	if err != nil || !moved {
		return false, err
	}
	pay.Status = models.OrderMcgpReleasing
	return true, s.driveSplit(ctx, pay)
}

func (s *OrderSpendablePaymentService) finishRelease(ctx context.Context, pay *models.OrderMcgpPayment, reason string) error {
	moved, err := s.transition(ctx, pay.ID, models.OrderMcgpReleasing,
		map[string]any{"status": models.OrderMcgpReleased, "failure_reason": truncateReason(reason)})
	if err == nil && moved {
		log.Printf("[order-mcgp] order=%s released (%s) seller=%s buyer-kept=%s", pay.OrderID, pay.ReleaseReason, pay.SellerAmount, pay.BuyerAmount)
		if pay.ReleaseReason == models.OrderMcgpReleaseEscrowExpired {
			s.notifyExpired(pay, reason != "")
		}
	}
	return err
}

func (s *OrderSpendablePaymentService) driveReverse(ctx context.Context, pay *models.OrderMcgpPayment) error {
	ref := OrderMcgpDebitRef(pay.OrderID)
	err := s.swap.OperatorSequence(ctx, func(ops OperatorOps) error {
		rec, err := s.swap.DebitRecord(ctx, ref)
		if err != nil {
			return s.note(ctx, pay, "reverse: read debit: "+err.Error())
		}
		switch rec.State {
		case DebitStatePending:
			txHash, err := ops.OperatorReverseDebit(ctx, ref)
			if err != nil {
				return s.note(ctx, pay, "reverse: "+err.Error())
			}
			s.update(ctx, pay.ID, models.OrderMcgpReversing, map[string]any{"resolve_tx_hash": txHash})
			if _, err := s.swap.VerifyOperatorEvent("PremiumDebitReversed", txHash, common.HexToAddress(pay.BuyerWallet), ref); err != nil {
				return s.note(ctx, pay, "reverse verify: "+err.Error())
			}
			return nil
		case DebitStateResolved:
			return nil // reversed by an earlier pass whose result was lost
		default:
			alertf("order-mcgp-nodebit:"+pay.OrderID.String(),
				"[order-mcgp] ALERT order_mcgp_refund_without_debit=1 order=%s — no debit on-chain under the order's ref", pay.OrderID)
			return rowErrorf("order %s: no debit on-chain to reverse", pay.OrderID)
		}
	})
	if err != nil {
		return err
	}
	moved, err := s.transition(ctx, pay.ID, models.OrderMcgpReversing,
		map[string]any{"status": models.OrderMcgpReversed, "failure_reason": ""})
	if err == nil && moved {
		log.Printf("[order-mcgp] order=%s reversed: buyer made whole", pay.OrderID)
		if pay.ReleaseReason == models.OrderMcgpReleaseEscrowExpired {
			s.notifyExpired(pay, false)
		}
	}
	return err
}
