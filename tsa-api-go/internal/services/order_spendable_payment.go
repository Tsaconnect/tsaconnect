package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// OrderSpendablePaymentService pays marketplace orders from the buyer's
// Spendable MCGP (the entitlement contract's premium balance) and holds it
// until the order resolves — the buyer protection ProductEscrow gives a wallet
// payment, rebuilt on the operator debit path bills already use.
//
//	fund     debitSpendable(buyer, total, maxFromBought=total, ref)       → held
//	release  settleDebit(ref) + creditPremium(seller, share, ref')        → released
//	refund   reverseDebit(ref)                                             → reversed
//	split    reverseDebit(ref), debitSpendable(buyer, non-buyer share,
//	         ref''), settleDebit(ref''), creditPremium(seller, share, ref') → released
//
// A split never CREDITS the buyer: creditPremium only mints spend-only
// premium, so crediting the buyer's share back would quietly turn MCGP they
// bought with USDC into MCGP they can never withdraw. Instead the whole hold is
// reversed (every bucket restored exactly) and only the non-buyer share is
// taken back with a second debit, which drains credited premium first.
//
// Settle and credit move backing in opposite directions: settleDebit lowers
// accountedMcgp by the whole debit, creditPremium raises it by the seller's
// share and requires the contract to hold enough MCGP for it. The credit can
// still fail after a settle (pause, the shared credit window, a lowered cap, or
// the freed MCGP withdrawn by the owner key), which would leave the seller's
// value stranded as platform excess. So a release (1) pre-checks everything
// creditPremium checks and does not settle if the credit could not land,
// (2) credits FIRST when the contract already holds enough surplus, (3) runs
// the pair inside OperatorSequence so no other operator transaction (an IT
// credit) can consume the freed MCGP in between, and (4) alerts at once on a
// settled debit whose credit is still unused.
//
// Every step is idempotent on chain, not only in this database: a debit ref
// can hold one debit, a debit can be settled or reversed once, and a credit
// ref can be used once. Each step reads that chain state before acting, so a
// crash or lost RPC response between a chain call and the next write is
// retried by the reconciler without ever paying twice.
//
// Dormant-by-default: without an operator signer IsEnabled() is false.
var (
	ErrOrderMcgpDisabled       = errors.New("paying with Spendable MCGP is not available right now")
	ErrOrderMcgpNotFound       = errors.New("this order has no Spendable MCGP payment")
	ErrOrderMcgpBadState       = errors.New("this order's Spendable MCGP payment cannot do that in its current state")
	ErrOrderMcgpPending        = errors.New("your Spendable MCGP payment is still being confirmed")
	ErrOrderMcgpMinimum        = errors.New("below the minimum Spendable MCGP payout")
	ErrOrderMcgpAlreadyDecided = errors.New("this order's Spendable MCGP payment has already been decided")
	ErrOrderMcgpRetry          = errors.New("the previous Spendable MCGP payment attempt did not go through; try again")
	ErrOrderMcgpWalletChanged  = errors.New("your wallet changed since this order's first payment attempt")
	ErrOrderMcgpEscrowUnknown  = errors.New("could not confirm this order has no escrow payment")
	ErrOrderMcgpPayoutBelowMin = errors.New("the seller's payout is below the minimum Spendable MCGP credit")
	ErrOrderMcgpSellerWallet   = errors.New("the seller has no wallet to be paid into")
	errOrderMcgpCreditBlocked  = errors.New("the seller credit cannot land right now")
)

// SpendableShortfallError is ErrSpendableInsufficient with the amounts.
type SpendableShortfallError struct{ Need, Have *big.Int }

func (e *SpendableShortfallError) Error() string { return ErrSpendableInsufficient.Error() }
func (e *SpendableShortfallError) Unwrap() error { return ErrSpendableInsufficient }

// SpendableLimitError is ErrSpendableOverTxCap with the cap that applies.
type SpendableLimitError struct{ Max *big.Int }

func (e *SpendableLimitError) Error() string { return ErrSpendableOverTxCap.Error() }
func (e *SpendableLimitError) Unwrap() error { return ErrSpendableOverTxCap }

// orderSpendableSwap is the slice of *MCGPSwapService an order payment needs.
type orderSpendableSwap interface {
	spendableDebitSwap
	OperatorCreditPremium(ctx context.Context, user string, amount *big.Int, ref [32]byte) (string, error)
	UsedCreditRef(ctx context.Context, ref [32]byte) (bool, error)
	SpendableLimits(ctx context.Context) (SpendableLimits, error)
	CreditHeadroom(ctx context.Context) (WindowHeadroom, error)
	DebitHeadroom(ctx context.Context) (WindowHeadroom, error)
	Backing() (held, owed *big.Int, err error)
	OperatorSequence(ctx context.Context, fn func(ops OperatorOps) error) error
}

// orderEscrowChecker reads ProductEscrow's record of an order. *EscrowService fills it.
type orderEscrowChecker interface {
	EscrowedOrder(ctx context.Context, orderId [32]byte) (*OnChainOrder, error)
}

const (
	orderMcgpDebitDomain        = "mcgp-order:"
	orderMcgpCollectDomain      = "mcgp-order-collect:"
	orderMcgpSellerCreditDomain = "mcgp-order-credit-seller:"

	// orderMcgpAbandonAfter is how long a debit with no on-chain record is
	// given to mine before its payment is marked failed. A debit that lands
	// later is still found (late-debit scan) and adopted or returned.
	orderMcgpAbandonAfter = 30 * time.Minute
	orderMcgpLateWindow   = 48 * time.Hour
	// orderMcgpGrace keeps the reconciler off rows a request is still driving:
	// a release is up to four transactions, each waiting for a receipt.
	orderMcgpGrace    = 5 * time.Minute
	orderMcgpStuckFor = time.Hour

	orderMcgpLimitsTTL = 5 * time.Minute

	// A split's "insufficient premium" collect refusal is retried this long
	// (from the decision) before the split is closed as uncollectable, and a
	// closed split is watched this long for a collect debit that mined late.
	orderMcgpUncollectableAfter  = 24 * time.Hour
	orderMcgpOrphanWindow        = 7 * 24 * time.Hour
	orderMcgpUncollectablePrefix = "split: non-buyer share uncollectable"
	// orderMcgpWindowMarginDiv keeps 1/20 (5%) of a rolling window's cap in
	// reserve for operator transactions already broadcast but not yet mined.
	orderMcgpWindowMarginDiv = 20

	// Escrow-expiry, mirroring ProductEscrowV2.autoRefund: after the 30-day
	// escrow window plus its 3-day grace an unresolved order is settled for
	// the buyer — in full if the seller never delivered, else 90% buyer / 3%
	// seller / 7% platform.
	orderMcgpExpiryGrace      = 3 * 24 * time.Hour
	orderMcgpExpiryBuyerBPS   = 9000
	orderMcgpExpiryPlatformBP = 700
)

// orderMcgpScanLimit is one reconciler page (a var so tests can page).
var orderMcgpScanLimit = 50

// OrderMcgpDebitRef is the on-chain debit key for an order's payment.
func OrderMcgpDebitRef(orderID uuid.UUID) [32]byte {
	return spendableRef(orderMcgpDebitDomain, orderID)
}

func orderMcgpCollectRef(orderID uuid.UUID) [32]byte {
	return spendableRef(orderMcgpCollectDomain, orderID)
}

func orderMcgpSellerCreditRef(orderID uuid.UUID) [32]byte {
	return spendableRef(orderMcgpSellerCreditDomain, orderID)
}

type OrderSpendablePaymentService struct {
	db     *gorm.DB
	swap   orderSpendableSwap
	bus    *events.Bus
	escrow orderEscrowChecker

	// driveInline runs a release/refund's chain work inside the request
	// instead of after it. Tests set it; production answers the request once
	// the decision is recorded and lets the chain work finish in the
	// background, with the reconciler as the backstop.
	driveInline bool

	scan reconcileCursor

	limitsMu sync.Mutex
	limits   *SpendableLimits
	limitsAt time.Time
}

// NewOrderSpendablePaymentService builds the service. bus may be nil.
func NewOrderSpendablePaymentService(db *gorm.DB, swap orderSpendableSwap, bus *events.Bus) *OrderSpendablePaymentService {
	return &OrderSpendablePaymentService{db: db, swap: swap, bus: bus}
}

// SetEscrowReader lets Fund refuse an order the ProductEscrow already holds a
// payment for (an app from before this change that paid but never reported).
func (s *OrderSpendablePaymentService) SetEscrowReader(r orderEscrowChecker) { s.escrow = r }

func (s *OrderSpendablePaymentService) IsEnabled() bool {
	return s != nil && s.db != nil && s.swap != nil && s.swap.HasOperator()
}

// Balance is the wallet's Spendable MCGP (bought + credited premium, 18dp).
func (s *OrderSpendablePaymentService) Balance(wallet string) (*big.Int, error) {
	if !s.IsEnabled() {
		return nil, ErrOrderMcgpDisabled
	}
	return s.swap.PremiumBalance(wallet)
}

// CheckLimits refuses an amount one operator debit, or the single seller
// credit that releases it in full, could never carry: above the per-tx debit
// or credit cap, or below the contract's minimum credit. Release applies the
// same minimum to each share (folding a dust share into the platform), so
// the two never disagree. An unreadable cap never refuses: the chain decides.
func (s *OrderSpendablePaymentService) CheckLimits(ctx context.Context, amount *big.Int) error {
	lim := s.readLimits(ctx)
	if lim == nil || amount == nil {
		return nil
	}
	max := minPositive(lim.MaxDebitPerTx, lim.MaxCreditPerTx)
	if max != nil && amount.Cmp(max) > 0 {
		return &SpendableLimitError{Max: max}
	}
	if belowMinCredit(lim, amount) {
		return ErrOrderMcgpMinimum
	}
	return nil
}

func belowMinCredit(lim *SpendableLimits, amount *big.Int) bool {
	return lim != nil && lim.MinCredit != nil && lim.MinCredit.Sign() > 0 &&
		amount.Sign() > 0 && amount.Cmp(lim.MinCredit) < 0
}

func minPositive(a, b *big.Int) *big.Int {
	switch {
	case a == nil || a.Sign() <= 0:
		return b
	case b == nil || b.Sign() <= 0:
		return a
	case a.Cmp(b) <= 0:
		return a
	default:
		return b
	}
}

func (s *OrderSpendablePaymentService) readLimits(ctx context.Context) *SpendableLimits {
	s.limitsMu.Lock()
	defer s.limitsMu.Unlock()
	if s.limits != nil && time.Since(s.limitsAt) < orderMcgpLimitsTTL {
		return s.limits
	}
	lim, err := s.swap.SpendableLimits(ctx)
	if err != nil {
		log.Printf("[order-mcgp] limits read failed (%v); not pre-checking", err)
		return s.limits // a stale read beats none
	}
	s.limits, s.limitsAt = &lim, time.Now()
	return s.limits
}

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
	if pay == nil {
		pay = &models.OrderMcgpPayment{
			ID:          uuid.New(),
			OrderID:     in.OrderID,
			BuyerID:     in.BuyerID,
			BuyerWallet: wallet,
			McgpAmount:  in.Amount.String(),
			DebitRef:    common.BytesToHash(ref[:]).Hex(),
			Status:      models.OrderMcgpDebiting,
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
func (s *OrderSpendablePaymentService) creditCanLand(ctx context.Context, amount *big.Int) error {
	head, err := s.swap.CreditHeadroom(ctx)
	if err != nil {
		return fmt.Errorf("%w: read headroom: %v", errOrderMcgpCreditBlocked, err)
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
	lim, err := s.swap.SpendableLimits(ctx)
	if err != nil {
		return fmt.Errorf("%w: read caps: %v", errOrderMcgpCreditBlocked, err)
	}
	if lim.MaxCreditPerTx != nil && lim.MaxCreditPerTx.Sign() > 0 && amount.Cmp(lim.MaxCreditPerTx) > 0 {
		return fmt.Errorf("%w: above maxCreditPerTx %s", errOrderMcgpCreditBlocked, lim.MaxCreditPerTx)
	}
	if belowMinCredit(&lim, amount) {
		return fmt.Errorf("%w: below minCredit %s", errOrderMcgpCreditBlocked, lim.MinCredit)
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
	if err := s.creditCanLand(ctx, credit); err != nil {
		return s.note(ctx, pay, err.Error())
	}
	return s.creditSeller(ctx, ops, pay, credit)
}

func (s *OrderSpendablePaymentService) driveRelease(ctx context.Context, pay *models.OrderMcgpPayment) error {
	ref := OrderMcgpDebitRef(pay.OrderID)
	err := s.swap.OperatorSequence(ctx, func(ops OperatorOps) error {
		rec, err := s.swap.DebitRecord(ctx, ref)
		if err != nil {
			return s.note(ctx, pay, "release: read debit: "+err.Error())
		}
		credit, err := s.sellerCredit(ctx, pay)
		if err != nil {
			return s.note(ctx, pay, "release: "+err.Error())
		}
		switch rec.State {
		case DebitStatePending:
			if credit != nil {
				if err := s.creditCanLand(ctx, credit); err != nil {
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
			return fmt.Errorf("order %s: no debit on-chain to settle", pay.OrderID)
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
		main, err := s.swap.DebitRecord(ctx, mainRef)
		if err != nil {
			return s.note(ctx, pay, "split: read debit: "+err.Error())
		}
		coll, err := s.swap.DebitRecord(ctx, collectRef)
		if err != nil {
			return s.note(ctx, pay, "split: read collect debit: "+err.Error())
		}
		credit, err := s.sellerCredit(ctx, pay)
		if err != nil {
			return s.note(ctx, pay, "split: "+err.Error())
		}
		if main.State == DebitStateNone {
			alertf("order-mcgp-nodebit:"+pay.OrderID.String(),
				"[order-mcgp] ALERT order_mcgp_release_without_debit=1 order=%s — no debit on-chain under the order's ref", pay.OrderID)
			return fmt.Errorf("order %s: no debit on-chain to split", pay.OrderID)
		}
		switch coll.State {
		case DebitStateResolved:
			if credit != nil {
				return s.creditAfterSettle(ctx, ops, pay, credit)
			}
			return nil
		case DebitStatePending:
			if credit != nil {
				if err := s.creditCanLand(ctx, credit); err != nil {
					return s.note(ctx, pay, err.Error())
				}
			}
			return s.settleAndCredit(ctx, ops, pay, collectRef, buyer, credit, "collect_settle_tx_hash")
		}

		// Collect debit not yet on-chain. Nothing moves until both the seller
		// credit that ends the split and the collect itself can land.
		if credit != nil {
			if err := s.creditCanLand(ctx, credit); err != nil {
				return s.note(ctx, pay, err.Error())
			}
		}
		if collect.Sign() > 0 {
			if err := s.debitCanLand(ctx, collect); err != nil {
				return s.note(ctx, pay, err.Error())
			}
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
			return fmt.Errorf("order %s: no debit on-chain to reverse", pay.OrderID)
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

// Reconcile drives every payment the request path left unfinished and
// returns how many it moved:
//
//   - debiting past the grace: adopt the landed debit, or fail the missing one
//   - failed within the late window whose debit mined anyway: adopt or return it
//   - releasing / reversing past the grace: finish the chain work
//   - released-uncollectable splits whose collect debit mined late: complete them
//   - held past the escrow window + grace, undisputed: resolve as autoRefund would
//
// Rows are driven one at a time, inline.
func (s *OrderSpendablePaymentService) Reconcile(ctx context.Context) (int, error) {
	if !s.IsEnabled() {
		return 0, nil
	}
	moved := 0
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	stale := time.Now().Add(-orderMcgpGrace)
	var debiting []models.OrderMcgpPayment
	if err := s.scan.page("debiting", "", s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Where("status = ? AND updated_at <= ?", models.OrderMcgpDebiting, stale), orderMcgpScanLimit).
		Find(&debiting).Error; err != nil {
		return 0, err
	}
	s.advance("debiting", debiting)
	for i := range debiting {
		_, won, err := s.resolveDebiting(ctx, &debiting[i])
		keep(err)
		if won {
			moved++
		}
	}

	var failed []models.OrderMcgpPayment
	if err := s.scan.page("late", "", s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Where("status = ? AND updated_at > ?", models.OrderMcgpFailed, time.Now().Add(-orderMcgpLateWindow)), orderMcgpScanLimit).
		Find(&failed).Error; err != nil {
		return moved, err
	}
	s.advance("late", failed)
	for i := range failed {
		adopted, err := s.adoptLateDebit(ctx, &failed[i])
		keep(err)
		if adopted {
			moved++
		}
	}

	var deciding []models.OrderMcgpPayment
	if err := s.scan.page("deciding", "", s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Where("status IN ? AND updated_at <= ?", []string{models.OrderMcgpReleasing, models.OrderMcgpReversing}, stale),
		orderMcgpScanLimit).Find(&deciding).Error; err != nil {
		return moved, err
	}
	s.advance("deciding", deciding)
	for i := range deciding {
		p := &deciding[i]
		err := s.drive(ctx, p)
		keep(err)
		if err == nil {
			moved++
			continue
		}
		// Measured from the decision, not the payment: an old hold that was
		// only just released is not stuck.
		if p.DecidedAt != nil && time.Since(*p.DecidedAt) > orderMcgpStuckFor {
			alertf("order-mcgp-stuck:"+p.OrderID.String(),
				"[order-mcgp] ALERT order_mcgp_stuck=1 order=%s status=%s decided=%s reason=%q",
				p.OrderID, p.Status, p.DecidedAt.Format(time.RFC3339), p.FailureReason)
		}
	}

	var closed []models.OrderMcgpPayment
	if err := s.scan.page("orphan", "", s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Where("status = ? AND failure_reason LIKE ? AND updated_at > ?",
			models.OrderMcgpReleased, orderMcgpUncollectablePrefix+"%", time.Now().Add(-orderMcgpOrphanWindow)),
		orderMcgpScanLimit).Find(&closed).Error; err != nil {
		return moved, err
	}
	s.advance("orphan", closed)
	for i := range closed {
		recovered, err := s.recoverOrphanCollect(ctx, &closed[i])
		keep(err)
		if recovered {
			moved++
		}
	}

	n, err := s.expireHeld(ctx)
	keep(err)
	return moved + n, firstErr
}

func (s *OrderSpendablePaymentService) advance(key string, rows []models.OrderMcgpPayment) {
	if n := len(rows); n > 0 {
		s.scan.advance(key, n, orderMcgpScanLimit, rows[n-1].UpdatedAt, rows[n-1].ID)
		return
	}
	s.scan.advance(key, 0, orderMcgpScanLimit, time.Time{}, uuid.Nil)
}

// heldOrder is a held payment with its order's resolution-relevant fields.
type heldOrder struct {
	models.OrderMcgpPayment
	OrderStatus       string
	OrderKind         string
	OrderProductID    uuid.UUID
	OrderQuantity     int
	SellerDeliveredAt *time.Time
	SellerWalletAddr  string
}

// expireHeld resolves holds whose escrow window has run out the way
// ProductEscrowV2.autoRefund resolves an ERC-20 order: in full to the buyer if
// the seller never marked it delivered; otherwise the buyer keeps 90%, the
// seller gets 3% and 7% stays with the platform. Disputed orders and orders
// awaiting a refund/cancel decision are left for a human. Paged with a
// cursor, so rows that keep failing cannot starve newer expiries; each row's
// chain work runs inline, one after another.
func (s *OrderSpendablePaymentService) expireHeld(ctx context.Context) (int, error) {
	cutoff := time.Now().Add(-orderMcgpExpiryGrace)
	var rows []heldOrder
	q := s.db.WithContext(ctx).Table("order_mcgp_payments").
		Select(`order_mcgp_payments.*, orders.status AS order_status, orders.kind AS order_kind,
			orders.product_id AS order_product_id, orders.quantity AS order_quantity,
			orders.seller_delivered_at AS seller_delivered_at,
			sellers.wallet_address AS seller_wallet_addr`).
		Joins("JOIN orders ON orders.id = order_mcgp_payments.order_id").
		Joins("LEFT JOIN users AS sellers ON sellers.id = orders.seller_id").
		Where("order_mcgp_payments.status = ? AND orders.escrow_expires_at IS NOT NULL AND orders.escrow_expires_at < ? AND orders.disputed_at IS NULL AND orders.status IN ?",
			models.OrderMcgpHeld, cutoff,
			[]string{models.OrderStatusEscrowed, models.OrderStatusShipped, models.OrderStatusDelivered})
	if err := s.scan.page("expiry", "order_mcgp_payments", q, orderMcgpScanLimit).Scan(&rows).Error; err != nil {
		return 0, err
	}
	if n := len(rows); n > 0 {
		s.scan.advance("expiry", n, orderMcgpScanLimit, rows[n-1].UpdatedAt, rows[n-1].ID)
	} else {
		s.scan.advance("expiry", 0, orderMcgpScanLimit, time.Time{}, uuid.Nil)
	}

	moved := 0
	var firstErr error
	for i := range rows {
		if err := ctx.Err(); err != nil {
			return moved, err
		}
		r := &rows[i]
		order := models.Order{ID: r.OrderID, Kind: r.OrderKind, ProductID: r.OrderProductID, Quantity: r.OrderQuantity}
		fromStatus := r.OrderStatus
		markRefunded := func(restock bool) func(tx *gorm.DB) error {
			return func(tx *gorm.DB) error {
				res := tx.Model(&models.Order{}).Where("id = ? AND status = ? AND disputed_at IS NULL", r.OrderID, fromStatus).
					Updates(map[string]any{"status": models.OrderStatusRefunded, "updated_at": time.Now()})
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected == 0 {
					return ErrOrderMcgpBadState // the order moved; decide next tick
				}
				if restock {
					return RestoreOrderStock(tx, &order)
				}
				return nil
			}
		}
		delivered := r.SellerDeliveredAt != nil
		var err error
		if !delivered {
			_, err = s.refund(ctx, r.OrderID, models.OrderMcgpReleaseEscrowExpired, markRefunded(true), true)
		} else {
			amount, _ := new(big.Int).SetString(r.McgpAmount, 10)
			if amount == nil {
				continue
			}
			buyerShare := new(big.Int).Div(new(big.Int).Mul(amount, big.NewInt(orderMcgpExpiryBuyerBPS)), big.NewInt(10000))
			platform := new(big.Int).Div(new(big.Int).Mul(amount, big.NewInt(orderMcgpExpiryPlatformBP)), big.NewInt(10000))
			sellerShare := new(big.Int).Sub(new(big.Int).Sub(amount, buyerShare), platform) // remainder, as on-chain
			wallet := r.SellerWalletAddr
			if !common.IsHexAddress(wallet) {
				// A seller with no wallet cannot be credited: their share stays
				// with the platform rather than blocking the buyer's 90%.
				sellerShare.SetInt64(0)
				wallet = ""
			}
			_, err = s.release(ctx, r.OrderID, OrderMcgpReleasePlan{
				Reason:       models.OrderMcgpReleaseEscrowExpired,
				SellerWallet: wallet,
				SellerAmount: sellerShare,
				BuyerAmount:  buyerShare,
			}, markRefunded(false), true)
		}
		if err != nil {
			if !errors.Is(err, ErrOrderMcgpBadState) && !errors.Is(err, ErrOrderMcgpAlreadyDecided) {
				alertf("order-mcgp-expiry:"+r.OrderID.String(),
					"[order-mcgp] ALERT order_mcgp_expiry_failed=1 order=%s: %v", r.OrderID, err)
				if firstErr == nil {
					firstErr = err
				}
			}
			continue
		}
		moved++
		log.Printf("[order-mcgp] order=%s escrow window expired → decided (delivered=%v)", r.OrderID, delivered)
	}
	return moved, firstErr
}

// notifyExpired tells both parties how an escrow-window expiry actually
// ended. It is sent when the payment reaches its terminal state, from the
// amounts recorded on the row, so it matches what moved on-chain.
func (s *OrderSpendablePaymentService) notifyExpired(pay *models.OrderMcgpPayment, uncollectable bool) {
	if s.bus == nil {
		return
	}
	var order models.Order
	if err := s.db.First(&order, "id = ?", pay.OrderID).Error; err != nil {
		return
	}
	short := order.ID.String()[:8]
	total := formatMcgp(pay.McgpAmount)
	var buyerMsg, sellerMsg string
	switch {
	case !pay.IsSplit():
		buyerMsg = fmt.Sprintf("Order #%s was not completed within the escrow window, so your %s MCGP was returned to your Spendable balance in full.", short, total)
		sellerMsg = fmt.Sprintf("Order #%s was not completed within the escrow window and has been refunded to the buyer.", short)
	case uncollectable:
		buyerMsg = fmt.Sprintf("Order #%s was marked delivered but never confirmed within the escrow window. Your %s MCGP stays in your Spendable balance.", short, total)
		sellerMsg = fmt.Sprintf("Order #%s was not confirmed within the escrow window, and your share could not be collected from the buyer. Support has been alerted.", short)
	default:
		kept := formatMcgp(pay.BuyerAmount)
		sellerShare := formatMcgp(pay.SellerAmount)
		if pay.SellerAmount == "" || pay.SellerAmount == "0" {
			buyerMsg = fmt.Sprintf("Order #%s was marked delivered but never confirmed within the escrow window. You keep %s of your %s MCGP; the seller's share was below the minimum payout and stays with the platform.", short, kept, total)
			sellerMsg = fmt.Sprintf("Order #%s was not confirmed within the escrow window. Your share was below the minimum payout, so nothing could be paid to you.", short)
		} else {
			buyerMsg = fmt.Sprintf("Order #%s was marked delivered but never confirmed within the escrow window. You keep %s of your %s MCGP; %s MCGP went to the seller.", short, kept, total, sellerShare)
			sellerMsg = fmt.Sprintf("Order #%s was not confirmed within the escrow window. As it was marked delivered, you received %s MCGP; the buyer kept %s MCGP.", short, sellerShare, kept)
		}
	}
	data := map[string]any{"orderId": order.ID.String(), "status": order.Status, "resolvedBy": "escrow_expiry"}
	s.bus.Publish(events.Event{
		Type: events.OrderRefunded, UserID: order.BuyerID, Title: "Order Refunded", Message: buyerMsg, Data: data,
	})
	s.bus.Publish(events.Event{
		Type: events.OrderRefunded, UserID: order.SellerID, Title: "Order Closed", Message: sellerMsg, Data: data,
	})
}

// formatMcgp renders an 18dp base amount as MCGP with up to 4 decimals.
func formatMcgp(base string) string {
	v, ok := new(big.Rat).SetString(strings.TrimSpace(base))
	if !ok {
		return "0"
	}
	v.Quo(v, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)))
	out := v.FloatString(4)
	out = strings.TrimRight(out, "0")
	return strings.TrimSuffix(out, ".")
}

// PaymentForOrder returns the order's payment, or ErrOrderMcgpNotFound.
func (s *OrderSpendablePaymentService) PaymentForOrder(ctx context.Context, orderID uuid.UUID) (*models.OrderMcgpPayment, error) {
	return s.paymentForOrder(ctx, orderID)
}

func (s *OrderSpendablePaymentService) paymentForOrder(ctx context.Context, orderID uuid.UUID) (*models.OrderMcgpPayment, error) {
	var pay models.OrderMcgpPayment
	if err := s.db.WithContext(ctx).Where("order_id = ?", orderID).First(&pay).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrOrderMcgpNotFound
		}
		return nil, err
	}
	return &pay, nil
}

// transition applies updates only if the payment is still in `from`, so two
// drivers (a request and the reconciler) can never both move it.
func (s *OrderSpendablePaymentService) transition(ctx context.Context, id uuid.UUID, from string, updates map[string]any) (bool, error) {
	res := s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Where("id = ? AND status = ?", id, from).Updates(updates)
	return res.RowsAffected == 1, res.Error
}

// update is transition for writes whose loss is harmless (a tx hash, a
// reason): the chain, not the column, is what the next pass reads.
func (s *OrderSpendablePaymentService) update(ctx context.Context, id uuid.UUID, from string, updates map[string]any) {
	if _, err := s.transition(ctx, id, from, updates); err != nil {
		log.Printf("[order-mcgp] payment=%s write failed: %v", id, err)
	}
}

// note records why the chain work stopped and returns it as an error.
func (s *OrderSpendablePaymentService) note(ctx context.Context, pay *models.OrderMcgpPayment, reason string) error {
	s.update(ctx, pay.ID, pay.Status, map[string]any{"failure_reason": truncateReason(reason)})
	pay.FailureReason = truncateReason(reason)
	return errors.New(reason)
}

func bigOrZeroInt(v *big.Int) *big.Int {
	if v == nil {
		return new(big.Int)
	}
	return v
}
