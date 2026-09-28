package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

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
	// orderMcgpLimitsRetry is how long a failed caps read is remembered, and
	// orderMcgpLimitsReadBudget bounds one read of all three caps.
	orderMcgpLimitsRetry      = 30 * time.Second
	orderMcgpLimitsReadBudget = 10 * time.Second

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

	// abuse bounds what one account can make the operator do (see
	// order_spendable_abuse.go).
	abuse OrderMcgpAbuseLimits

	// The operator caps, cached. limitsMu guards the fields only and is never
	// held across the RPC; limitsFlight makes concurrent misses share one read.
	// A failed read is remembered for orderMcgpLimitsRetry so a dead RPC is not
	// hammered once per request.
	limitsMu     sync.Mutex
	limits       *SpendableLimits
	limitsAt     time.Time
	limitsErrAt  time.Time
	limitsFlight singleflight.Group
}

// NewOrderSpendablePaymentService builds the service. bus may be nil.
func NewOrderSpendablePaymentService(db *gorm.DB, swap orderSpendableSwap, bus *events.Bus) *OrderSpendablePaymentService {
	return &OrderSpendablePaymentService{db: db, swap: swap, bus: bus, abuse: DefaultOrderMcgpAbuseLimits()}
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
// It may read the chain; inside a DB transaction use CheckCachedLimits.
func (s *OrderSpendablePaymentService) CheckLimits(ctx context.Context, amount *big.Int) error {
	return checkSpendableLimits(s.readLimits(ctx), amount)
}

// CheckCachedLimits is CheckLimits against the cached caps only. It never
// reads the chain, so it is safe inside a DB transaction. An empty cache
// refuses nothing (Fund re-checks with a fresh read before any debit).
func (s *OrderSpendablePaymentService) CheckCachedLimits(amount *big.Int) error {
	lim, _ := s.cachedLimits()
	return checkSpendableLimits(lim, amount)
}

// WarmLimits refreshes the cached caps if they are stale, off any DB
// transaction, so a later CheckCachedLimits has something to check against.
func (s *OrderSpendablePaymentService) WarmLimits(ctx context.Context) {
	s.readLimits(ctx)
}

func checkSpendableLimits(lim *SpendableLimits, amount *big.Int) error {
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

// cachedLimits returns the cached caps (possibly stale, possibly nil) and
// whether they may be used without a read: fresh, or a read failed recently.
func (s *OrderSpendablePaymentService) cachedLimits() (*SpendableLimits, bool) {
	s.limitsMu.Lock()
	defer s.limitsMu.Unlock()
	now := time.Now()
	if s.limits != nil && now.Sub(s.limitsAt) < orderMcgpLimitsTTL {
		return s.limits, true
	}
	if !s.limitsErrAt.IsZero() && now.Sub(s.limitsErrAt) < orderMcgpLimitsRetry {
		return s.limits, true // a stale read beats none; nil means "unknown"
	}
	return s.limits, false
}

func (s *OrderSpendablePaymentService) readLimits(ctx context.Context) *SpendableLimits {
	if lim, ok := s.cachedLimits(); ok {
		return lim
	}
	v, _, _ := s.limitsFlight.Do("limits", func() (any, error) {
		if lim, ok := s.cachedLimits(); ok {
			return lim, nil
		}
		// Bounded and detached from the caller: the result is shared by every
		// request waiting on this flight, not only the one that started it.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), orderMcgpLimitsReadBudget)
		defer cancel()
		lim, err := s.swap.SpendableLimits(rctx)
		s.limitsMu.Lock()
		defer s.limitsMu.Unlock()
		if err != nil {
			s.limitsErrAt = time.Now()
			log.Printf("[order-mcgp] limits read failed (%v); not pre-checking for %s", err, orderMcgpLimitsRetry)
			return s.limits, nil
		}
		s.limits, s.limitsAt, s.limitsErrAt = &lim, time.Now(), time.Time{}
		return s.limits, nil
	})
	lim, _ := v.(*SpendableLimits)
	return lim
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

// note records why the chain work stopped and returns it as a row error.
func (s *OrderSpendablePaymentService) note(ctx context.Context, pay *models.OrderMcgpPayment, reason string) error {
	s.update(ctx, pay.ID, pay.Status, map[string]any{"failure_reason": truncateReason(reason)})
	pay.FailureReason = truncateReason(reason)
	return &orderMcgpRowError{msg: reason}
}

// orderMcgpRowError is one payment's chain outcome: a refusal, a read that
// decided nothing, a missing debit. It is recorded on the row (and alerted
// where it matters, e.g. order_mcgp_stuck), so the reconciler logs it and
// moves on instead of failing the whole tick — otherwise one stuck order
// would page as a dead worker every minute.
type orderMcgpRowError struct{ msg string }

func (e *orderMcgpRowError) Error() string { return e.msg }

func rowErrorf(format string, args ...any) error {
	return &orderMcgpRowError{msg: fmt.Sprintf(format, args...)}
}

func isOrderMcgpRowError(err error) bool {
	var row *orderMcgpRowError
	return errors.As(err, &row)
}

func bigOrZeroInt(v *big.Int) *big.Int {
	if v == nil {
		return new(big.Int)
	}
	return v
}
