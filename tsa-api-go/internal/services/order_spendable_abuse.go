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
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Abuse bounds on Spendable MCGP checkout (security review of 406009a9).
//
// Paying from Spendable MCGP costs the buyer nothing but the MCGP, and a
// refund before delivery returns all of it at once. Every round still sends
// operator transactions and consumes the contract's rolling windows, which
// bills and IT buys/sells share: reverseDebit never gives debit-window usage
// back, and a colluding seller's confirm-receipt spends credit window. So:
//
//   - a buyer may have at most MaxOpenHolds payments open (debiting or held);
//   - a buyer with MaxRefundsPerDay refunds/cancels in the last 24h cannot
//     start another Spendable payment until the oldest ages out. Enforced at
//     PAY time, never at refund time: refusing a refund would trap the
//     buyer's MCGP in the hold;
//   - checkout's own debits and seller credits may use at most WindowSharePct
//     of debitWindowCap / creditWindowCap, measured from this table, so the
//     rest of each window stays free for bills and IT;
//   - RefundCycleAlertAt refunds in 24h by one buyer raise an ALERT.

// OrderMcgpAbuseLimits bounds what one account can make the operator do.
type OrderMcgpAbuseLimits struct {
	MaxOpenHolds       int
	MaxRefundsPerDay   int
	WindowSharePct     int
	RefundCycleAlertAt int
}

// Defaults, overridable through config (ORDER_MCGP_*).
const (
	DefaultOrderMcgpMaxOpenHolds     = 3
	DefaultOrderMcgpMaxRefundsPerDay = 5
	DefaultOrderMcgpWindowSharePct   = 30
	DefaultOrderMcgpRefundCycleAlert = 3

	// orderMcgpDefaultWindow stands in for a window length the contract did
	// not report. Longer than any real window, so usage is over-counted —
	// which errs toward keeping capacity free for bills and IT.
	orderMcgpDefaultWindow = 24 * time.Hour
	orderMcgpRefundPeriod  = 24 * time.Hour
)

var (
	ErrOrderMcgpTooManyOpen    = errors.New("too many unfinished Spendable MCGP orders")
	ErrOrderMcgpTooManyRefunds = errors.New("too many Spendable MCGP orders refunded or cancelled today")
	ErrOrderMcgpOwnOrder       = errors.New("you cannot pay for your own listing")
)

// DefaultOrderMcgpAbuseLimits returns the built-in bounds.
func DefaultOrderMcgpAbuseLimits() OrderMcgpAbuseLimits {
	return OrderMcgpAbuseLimits{
		MaxOpenHolds:       DefaultOrderMcgpMaxOpenHolds,
		MaxRefundsPerDay:   DefaultOrderMcgpMaxRefundsPerDay,
		WindowSharePct:     DefaultOrderMcgpWindowSharePct,
		RefundCycleAlertAt: DefaultOrderMcgpRefundCycleAlert,
	}
}

// SetAbuseLimits overrides the bounds. A value outside its range keeps the
// default rather than switching the bound off.
func (s *OrderSpendablePaymentService) SetAbuseLimits(l OrderMcgpAbuseLimits) {
	d := DefaultOrderMcgpAbuseLimits()
	if l.MaxOpenHolds <= 0 {
		l.MaxOpenHolds = d.MaxOpenHolds
	}
	if l.MaxRefundsPerDay <= 0 {
		l.MaxRefundsPerDay = d.MaxRefundsPerDay
	}
	if l.WindowSharePct <= 0 || l.WindowSharePct > 100 {
		l.WindowSharePct = d.WindowSharePct
	}
	if l.RefundCycleAlertAt <= 0 {
		l.RefundCycleAlertAt = d.RefundCycleAlertAt
	}
	s.abuse = l
}

// AbuseLimits reports the bounds in force.
func (s *OrderSpendablePaymentService) AbuseLimits() OrderMcgpAbuseLimits { return s.abuse }

// checkBuyerBounds refuses a new Spendable payment for a buyer at either
// per-account bound. orderID is the order being paid, which never counts
// against its own buyer.
func (s *OrderSpendablePaymentService) checkBuyerBounds(ctx context.Context, buyerID, orderID uuid.UUID) error {
	var open int64
	if err := s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Where("buyer_id = ? AND order_id <> ? AND status IN ?", buyerID, orderID,
			[]string{models.OrderMcgpDebiting, models.OrderMcgpHeld}).
		Count(&open).Error; err != nil {
		return err
	}
	if s.abuse.MaxOpenHolds > 0 && open >= int64(s.abuse.MaxOpenHolds) {
		return fmt.Errorf("%w: %d open (max %d)", ErrOrderMcgpTooManyOpen, open, s.abuse.MaxOpenHolds)
	}
	refunds, err := s.recentRefunds(ctx, buyerID)
	if err != nil {
		return err
	}
	if s.abuse.MaxRefundsPerDay > 0 && refunds >= int64(s.abuse.MaxRefundsPerDay) {
		return fmt.Errorf("%w: %d in 24h (max %d)", ErrOrderMcgpTooManyRefunds, refunds, s.abuse.MaxRefundsPerDay)
	}
	return nil
}

// recentRefunds counts the buyer's refunds and cancels decided in the last
// 24h. Escrow-window expiries are not the buyer's doing and do not count.
func (s *OrderSpendablePaymentService) recentRefunds(ctx context.Context, buyerID uuid.UUID) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Where("buyer_id = ? AND status IN ? AND decided_at > ? AND COALESCE(release_reason, '') <> ?",
			buyerID, []string{models.OrderMcgpReversing, models.OrderMcgpReversed},
			time.Now().Add(-orderMcgpRefundPeriod), models.OrderMcgpReleaseEscrowExpired).
		Count(&n).Error
	return n, err
}

// watchRefundCycles alerts when one buyer keeps paying and refunding.
func (s *OrderSpendablePaymentService) watchRefundCycles(ctx context.Context, buyerID uuid.UUID) {
	n, err := s.recentRefunds(ctx, buyerID)
	if err != nil || s.abuse.RefundCycleAlertAt <= 0 || n < int64(s.abuse.RefundCycleAlertAt) {
		return
	}
	alertf("order-mcgp-refund-cycle:"+buyerID.String(),
		"[order-mcgp] ALERT order_mcgp_refund_cycle=1 buyer=%s refunds_24h=%d — repeated pay→refund on Spendable MCGP", buyerID, n)
}

// windowShare is the part of a window's cap checkout may use, or nil when
// the cap is unknown (nothing to reserve against).
func (s *OrderSpendablePaymentService) windowShare(capAmt *big.Int) *big.Int {
	if capAmt == nil || capAmt.Sign() <= 0 || s.abuse.WindowSharePct <= 0 {
		return nil
	}
	share := new(big.Int).Mul(capAmt, big.NewInt(int64(s.abuse.WindowSharePct)))
	return share.Div(share, big.NewInt(100))
}

func windowSince(head WindowHeadroom) time.Time {
	w := orderMcgpDefaultWindow
	if head.Window != nil && head.Window.Sign() > 0 && head.Window.IsInt64() {
		w = time.Duration(head.Window.Int64()) * time.Second
	}
	return time.Now().Add(-w)
}

// checkoutDebitUsage is what checkout's own debits have taken from the debit
// window in the last window length: every debit sent (a reverse gives none
// back), plus each split's collect debit once sent. A rolling look-back is
// never shorter than the contract's fixed window, and a row's updated_at is
// never earlier than the transaction it records, so this can only over-count.
func (s *OrderSpendablePaymentService) checkoutDebitUsage(ctx context.Context, since time.Time) (*big.Int, error) {
	var rows []models.OrderMcgpPayment
	if err := s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Select("mcgp_amount", "buyer_amount", "status", "collect_tx_hash", "updated_at", "debited_at", "created_at").
		Where("(status <> ? AND COALESCE(debited_at, created_at) > ?) OR (COALESCE(collect_tx_hash, '') <> '' AND updated_at > ?)",
			models.OrderMcgpFailed, since, since).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	used := new(big.Int)
	for _, r := range rows {
		amount := parseBaseAmount(r.McgpAmount)
		debitAt := r.CreatedAt
		if r.DebitedAt != nil {
			debitAt = *r.DebitedAt
		}
		if r.Status != models.OrderMcgpFailed && debitAt.After(since) {
			used.Add(used, amount)
		}
		if r.CollectTxHash != "" && r.UpdatedAt.After(since) {
			if collect := new(big.Int).Sub(amount, parseBaseAmount(r.BuyerAmount)); collect.Sign() > 0 {
				used.Add(used, collect)
			}
		}
	}
	return used, nil
}

// checkoutCreditUsage is what seller credits sent within the look-back have
// taken from the credit window. Only credits actually sent count: counting
// releases still waiting would let a queue of them block each other forever.
func (s *OrderSpendablePaymentService) checkoutCreditUsage(ctx context.Context, since time.Time) (*big.Int, error) {
	var amounts []string
	if err := s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Where("COALESCE(seller_credit_tx_hash, '') <> '' AND updated_at > ?", since).
		Pluck("seller_amount", &amounts).Error; err != nil {
		return nil, err
	}
	used := new(big.Int)
	for _, a := range amounts {
		used.Add(used, parseBaseAmount(a))
	}
	return used, nil
}

// checkDebitShare refuses a checkout debit that would take checkout past its
// share of the debit window. An unreadable window reserves nothing: the
// chain's own cap still applies.
func (s *OrderSpendablePaymentService) checkDebitShare(ctx context.Context, amount *big.Int) error {
	head, err := s.swap.DebitHeadroom(ctx)
	if err != nil {
		log.Printf("[order-mcgp] debit window unreadable (%v); checkout share not checked", err)
		return nil
	}
	share := s.windowShare(head.WindowCap)
	if share == nil {
		return nil
	}
	used, err := s.checkoutDebitUsage(ctx, windowSince(head))
	if err != nil {
		return err
	}
	if new(big.Int).Add(used, amount).Cmp(share) > 0 {
		log.Printf("[order-mcgp] checkout debit share reached: used=%s need=%s share=%s (%d%% of %s)",
			used, amount, share, s.abuse.WindowSharePct, head.WindowCap)
		return fmt.Errorf("%w: checkout's share of the debit window is used", ErrSpendableOverWindow)
	}
	return nil
}

// checkCreditShare is checkDebitShare for a seller credit, against the credit
// headroom creditCanLand already read.
func (s *OrderSpendablePaymentService) checkCreditShare(ctx context.Context, head WindowHeadroom, amount *big.Int) error {
	share := s.windowShare(head.WindowCap)
	if share == nil {
		return nil
	}
	used, err := s.checkoutCreditUsage(ctx, windowSince(head))
	if err != nil {
		return fmt.Errorf("%w: read checkout credit usage: %v", errOrderMcgpCreditBlocked, err)
	}
	if new(big.Int).Add(used, amount).Cmp(share) > 0 {
		return fmt.Errorf("%w: checkout's share of the credit window is used (%s of %s), need %s",
			errOrderMcgpCreditBlocked, used, share, amount)
	}
	return nil
}

func parseBaseAmount(v string) *big.Int {
	n, ok := new(big.Int).SetString(strings.TrimSpace(v), 10)
	if !ok || n.Sign() < 0 {
		return new(big.Int)
	}
	return n
}
