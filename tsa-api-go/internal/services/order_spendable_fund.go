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
	"gorm.io/gorm"
)

// OrderMcgpFundInput is one order's payment.
type OrderMcgpFundInput struct {
	OrderID     uuid.UUID
	BuyerID     uuid.UUID
	BuyerWallet string
	Amount      *big.Int // the order's whole total, 18dp
}

// Fund debits the buyer's Spendable MCGP into the contract's pending bucket
// and marks the order escrowed. Idempotent per order: a payment already held
// (or further along) is returned as it is, a failed one is retried under the
// same ref and the same pinned wallet, and one still confirming answers
// ErrOrderMcgpPending.
func (s *OrderSpendablePaymentService) Fund(ctx context.Context, in OrderMcgpFundInput) (*models.OrderMcgpPayment, error) {
	if !s.IsEnabled() {
		return nil, ErrOrderMcgpDisabled
	}
	if in.Amount == nil || in.Amount.Sign() <= 0 {
		return nil, fmt.Errorf("order payment: amount must be > 0")
	}
	wallet := strings.TrimSpace(in.BuyerWallet)
	if !common.IsHexAddress(wallet) {
		return nil, fmt.Errorf("order payment: buyer has no wallet")
	}

	existing, err := s.paymentForOrder(ctx, in.OrderID)
	switch {
	case err == nil:
		if existing.Status == models.OrderMcgpDebiting {
			// An earlier attempt's outcome is unknown: ask the chain first.
			status, _, rerr := s.resolveDebiting(ctx, existing)
			switch {
			case rerr != nil:
				return existing, ErrOrderMcgpPending
			case status == models.OrderMcgpHeld:
				return s.paymentForOrder(ctx, in.OrderID)
			case status == models.OrderMcgpReversing || status == models.OrderMcgpReversed:
				return nil, ErrOrderNotPayable
			case status == models.OrderMcgpFailed:
				return nil, ErrOrderMcgpRetry
			}
			return existing, ErrOrderMcgpPending
		}
		if existing.Status != models.OrderMcgpFailed {
			return existing, nil
		}
		// A retry reuses the ref, so it must reuse the wallet: a first debit
		// that lands late is under the pinned wallet.
		if !strings.EqualFold(existing.BuyerWallet, wallet) {
			return nil, ErrOrderMcgpWalletChanged
		}
		wallet = existing.BuyerWallet
	case !errors.Is(err, ErrOrderMcgpNotFound):
		return nil, err
	}

	var order models.Order
	if err := s.db.WithContext(ctx).First(&order, "id = ?", in.OrderID).Error; err != nil {
		return nil, ErrOrderNotPayable
	}
	// Buying from yourself moves nothing but still spends operator
	// transactions and window shared with bills and IT.
	if order.SellerID == in.BuyerID {
		return nil, ErrOrderMcgpOwnOrder
	}
	// Soft bounds (read, then act): the per-user rate limit on pay-spendable
	// keeps a concurrent burst small.
	if err := s.checkBuyerBounds(ctx, in.BuyerID, in.OrderID); err != nil {
		return nil, err
	}
	// An order not yet claimed for Spendable MCGP may have been paid through
	// the escrow by an older app that never reported it. Charging it again
	// here would take the buyer's money twice.
	//
	// Known gap: an old app's createOrder can still mine between this read
	// and the claim below (seconds). Only a pre-update client that had the
	// order open can do it, and it is caught afterwards: that app's
	// submit-escrow for the now-spendable order is refused with an ALERT
	// (order_mcgp_double_funded), and the escrow deposit stays in
	// ProductEscrow, refundable to the buyer on-chain. Left to that alert
	// rather than a lock here.
	if order.PaymentSource == "" && s.escrow != nil {
		onChain, err := s.escrow.EscrowedOrder(ctx, GenerateOrderID(in.OrderID))
		if err != nil {
			log.Printf("[order-mcgp] order=%s escrow read failed (%v); refusing to fund", in.OrderID, err)
			return nil, ErrOrderMcgpEscrowUnknown
		}
		if onChain != nil {
			log.Printf("[order-mcgp] ALERT order_mcgp_escrow_already_funded=1 order=%s — ProductEscrow holds a payment; not debiting Spendable MCGP", in.OrderID)
			return nil, ErrOrderNotPayable
		}
	}

	// Refuse early with the numbers rather than let the chain revert. An
	// unreadable balance is not a refusal — the chain still decides.
	if balance, berr := s.swap.PremiumBalance(wallet); berr == nil && balance.Cmp(in.Amount) < 0 {
		return nil, &SpendableShortfallError{Need: new(big.Int).Set(in.Amount), Have: balance}
	}
	if err := s.CheckLimits(ctx, in.Amount); err != nil {
		return nil, err
	}
	if err := s.checkDebitShare(ctx, in.Amount); err != nil {
		return nil, err
	}

	// Claim the order for this payment source while it is still unpaid. After
	// this no ProductEscrow submission can mark it paid, so one order can never
	// be funded twice by two different paths.
	claim := s.db.WithContext(ctx).Model(&models.Order{}).
		Where("id = ? AND buyer_id = ? AND status = ? AND payment_source IN ? AND (escrow_tx_hash IS NULL OR escrow_tx_hash = '')",
			in.OrderID, in.BuyerID, models.OrderStatusPendingPayment, []string{"", models.OrderPaymentSpendableMCGP}).
		Updates(map[string]any{"payment_source": models.OrderPaymentSpendableMCGP, "updated_at": time.Now()})
	if claim.Error != nil {
		return nil, claim.Error
	}
	if claim.RowsAffected == 0 {
		return nil, ErrOrderNotPayable
	}

	ref := OrderMcgpDebitRef(in.OrderID)
	pay := existing
	debitedAt := time.Now()
	if pay == nil {
		pay = &models.OrderMcgpPayment{
			ID:          uuid.New(),
			OrderID:     in.OrderID,
			BuyerID:     in.BuyerID,
			BuyerWallet: wallet,
			McgpAmount:  in.Amount.String(),
			DebitRef:    common.BytesToHash(ref[:]).Hex(),
			Status:      models.OrderMcgpDebiting,
			DebitedAt:   &debitedAt,
		}
		// Persisted BEFORE broadcasting: if the process dies mid-flight this
		// row is the only evidence a debit may exist on-chain.
		if err := s.db.WithContext(ctx).Create(pay).Error; err != nil {
			// The unique order index: a concurrent attempt got there first.
			return nil, ErrOrderMcgpPending
		}
	} else {
		moved, err := s.transition(ctx, pay.ID, models.OrderMcgpFailed, map[string]any{
			"status": models.OrderMcgpDebiting, "mcgp_amount": in.Amount.String(), "failure_reason": "",
			"debited_at": &debitedAt,
		})
		if err != nil {
			return nil, err
		}
		if !moved {
			return nil, ErrOrderMcgpPending
		}
		pay.Status, pay.McgpAmount = models.OrderMcgpDebiting, in.Amount.String()
	}

	// maxFromBought = the whole amount: paying for goods with MCGP you bought
	// is the point, and the contract drains credited premium first anyway.
	txHash, err := s.swap.OperatorDebitSpendable(ctx, wallet, in.Amount, in.Amount, ref)
	if err != nil {
		if errors.Is(err, ErrTxNotBroadcast) && !strings.Contains(err.Error(), "Debit exists") {
			// Definitely not on-chain: the order stays payable.
			s.update(ctx, pay.ID, models.OrderMcgpDebiting, map[string]any{
				"status": models.OrderMcgpFailed, "failure_reason": truncateReason("debit: " + err.Error()),
			})
			if typed := classifyDebitRevert(err); typed != nil {
				return nil, typed
			}
			return nil, err
		}
		// Sent (or already on-chain under our ref) but unconfirmed: only chain
		// state can say, so the row stays debiting for the reconciler.
		s.update(ctx, pay.ID, models.OrderMcgpDebiting, map[string]any{
			"failure_reason": truncateReason("debit outcome unknown: " + err.Error()),
		})
		log.Printf("[order-mcgp] order=%s debit outcome unknown: %v", in.OrderID, err)
		return nil, ErrOrderMcgpPending
	}
	s.update(ctx, pay.ID, models.OrderMcgpDebiting, map[string]any{"debit_tx_hash": txHash})
	if _, err := s.swap.VerifyOperatorEvent("PremiumDebitPending", txHash, common.HexToAddress(wallet), ref); err != nil {
		s.update(ctx, pay.ID, models.OrderMcgpDebiting, map[string]any{
			"failure_reason": truncateReason("debit verify: " + err.Error()),
		})
		log.Printf("[order-mcgp] order=%s debit %s unverified: %v", in.OrderID, txHash, err)
		return nil, ErrOrderMcgpPending
	}
	if _, err := s.adoptHeld(ctx, pay.ID, in.OrderID); err != nil {
		return nil, err
	}
	return s.paymentForOrder(ctx, in.OrderID)
}

// adoptHeld records a debit confirmed on-chain: debiting → held and the order
// pending_payment → escrowed, in one transaction. If the order stopped being
// payable while the debit was in flight (cancelled, expired), the MCGP is
// returned instead and ErrOrderNotPayable is reported.
func (s *OrderSpendablePaymentService) adoptHeld(ctx context.Context, payID, orderID uuid.UUID) (bool, error) {
	updates := map[string]any{"status": models.OrderMcgpHeld, "failure_reason": ""}
	if fromBought, err := s.swap.DebitFromBought(OrderMcgpDebitRef(orderID)); err == nil && fromBought != nil {
		updates["from_bought"] = fromBought.String()
	}
	expires := time.Now().Add(EscrowWindow)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.OrderMcgpPayment{}).
			Where("id = ? AND status = ?", payID, models.OrderMcgpDebiting).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errOrderMcgpLostRace
		}
		return MarkOrderEscrowed(tx, orderID, map[string]any{
			"status":            models.OrderStatusEscrowed,
			"payment_source":    models.OrderPaymentSpendableMCGP,
			"escrow_expires_at": expires,
		})
	})
	switch {
	case err == nil:
		log.Printf("[order-mcgp] order=%s paid from Spendable MCGP → held", orderID)
		var order models.Order
		if s.db.WithContext(ctx).First(&order, "id = ?", orderID).Error == nil {
			NotifyOrderEscrowed(s.bus, &order, expires)
		}
		return true, nil
	case errors.Is(err, errOrderMcgpLostRace):
		return false, nil // someone else moved it; they own the outcome
	case errors.Is(err, ErrOrderNotPayable):
		log.Printf("[order-mcgp] ALERT order_mcgp_paid_after_close=1 order=%s — debit landed after the order closed; returning it", orderID)
		now := time.Now()
		if moved, merr := s.transition(ctx, payID, models.OrderMcgpDebiting, map[string]any{
			"status": models.OrderMcgpReversing, "decided_at": &now, "failure_reason": "order closed before the debit confirmed",
		}); merr == nil && moved {
			_ = s.driveByID(ctx, payID)
		}
		return false, ErrOrderNotPayable
	default:
		return false, err
	}
}

var errOrderMcgpLostRace = errors.New("order payment moved concurrently")

// resolveDebiting settles a `debiting` payment from the debit record (read
// against the wallet pinned on the row): adopt a landed debit, fail one that
// never came, alert on anything else. Returns the status afterwards and
// whether this call moved it.
func (s *OrderSpendablePaymentService) resolveDebiting(ctx context.Context, pay *models.OrderMcgpPayment) (string, bool, error) {
	rec, err := s.swap.DebitRecord(ctx, OrderMcgpDebitRef(pay.OrderID))
	if err != nil {
		return pay.Status, false, fmt.Errorf("order payment: read debit: %w", err)
	}
	switch classifyDebiting(rec, pay.BuyerWallet, pay.McgpAmount, time.Since(pay.UpdatedAt), orderMcgpAbandonAfter) {
	case debitingLanded:
		moved, err := s.adoptHeld(ctx, pay.ID, pay.OrderID)
		if errors.Is(err, ErrOrderNotPayable) {
			return models.OrderMcgpReversing, true, nil
		}
		if err != nil || !moved {
			return pay.Status, false, err
		}
		return models.OrderMcgpHeld, true, nil
	case debitingAbandon:
		reason := truncateReason("debit never landed on-chain (was: " + pay.FailureReason + ")")
		moved, err := s.transition(ctx, pay.ID, models.OrderMcgpDebiting,
			map[string]any{"status": models.OrderMcgpFailed, "failure_reason": reason})
		if err != nil || !moved {
			return pay.Status, false, err
		}
		return models.OrderMcgpFailed, true, nil
	case debitingMismatch:
		alertf("order-mcgp-mismatch:"+pay.OrderID.String(),
			"[order-mcgp] ALERT order_mcgp_debit_mismatch=1 order=%s on-chain user=%s amount=%s payment wallet=%s amount=%s",
			pay.OrderID, rec.User.Hex(), rec.Amount, pay.BuyerWallet, pay.McgpAmount)
	case debitingResolved:
		alertf("order-mcgp-resolved:"+pay.OrderID.String(),
			"[order-mcgp] ALERT order_mcgp_debit_resolved_unrecorded=1 order=%s — debit settled or reversed on-chain while the payment reads debiting", pay.OrderID)
	}
	return pay.Status, false, nil
}

// adoptLateDebit brings back a `failed` payment whose debit mined after it
// was given up on: held (and the order escrowed) if the order still awaits
// payment, otherwise the MCGP is returned.
func (s *OrderSpendablePaymentService) adoptLateDebit(ctx context.Context, pay *models.OrderMcgpPayment) (bool, error) {
	rec, err := s.swap.DebitRecord(ctx, OrderMcgpDebitRef(pay.OrderID))
	if err != nil || rec.State != DebitStatePending {
		return false, err
	}
	if !debitRecordMatches(rec, pay.BuyerWallet, pay.McgpAmount) {
		alertf("order-mcgp-mismatch:"+pay.OrderID.String(),
			"[order-mcgp] ALERT order_mcgp_debit_mismatch=1 order=%s (failed payment) on-chain user=%s amount=%s", pay.OrderID, rec.User.Hex(), rec.Amount)
		return false, nil
	}
	moved, err := s.transition(ctx, pay.ID, models.OrderMcgpFailed, map[string]any{
		"status": models.OrderMcgpDebiting, "failure_reason": "debit landed after the payment was marked failed",
	})
	if err != nil || !moved {
		return false, err
	}
	log.Printf("[order-mcgp] ALERT order_mcgp_late_debit=1 order=%s — debit mined after the payment failed; adopting", pay.OrderID)
	if _, err := s.adoptHeld(ctx, pay.ID, pay.OrderID); err != nil && !errors.Is(err, ErrOrderNotPayable) {
		return true, err
	}
	return true, nil
}
