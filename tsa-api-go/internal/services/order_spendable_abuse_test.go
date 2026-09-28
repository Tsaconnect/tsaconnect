package services

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// orderFor is pendingOrder for a given buyer, so per-buyer bounds can be hit.
func (r *orderRig) orderFor(t *testing.T, buyer uuid.UUID, totalMcgp int64) rigOrder {
	t.Helper()
	o := r.pendingOrder(t, totalMcgp)
	if err := r.db.Exec(`UPDATE orders SET buyer_id = ? WHERE id = ?`, buyer, o.id).Error; err != nil {
		t.Fatalf("set buyer: %v", err)
	}
	o.buyer = buyer
	return o
}

func refundOrder(o rigOrder) func(tx *gorm.DB) error {
	return func(tx *gorm.DB) error {
		return tx.Model(&models.Order{}).Where("id = ?", o.id).Update("status", models.OrderStatusRefunded).Error
	}
}

func TestOrderMcgpAbuse_FundRefusesYourOwnListing(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 10)
	if err := r.db.Exec(`UPDATE orders SET buyer_id = seller_id WHERE id = ?`, o.id).Error; err != nil {
		t.Fatal(err)
	}
	o.buyer = o.seller

	if _, err := r.fund(o); !errors.Is(err, ErrOrderMcgpOwnOrder) {
		t.Fatalf("expected ErrOrderMcgpOwnOrder, got %v", err)
	}
	if r.chain.debitCalls != 0 {
		t.Fatal("nothing may be debited for an order with yourself")
	}
}

func TestOrderMcgpAbuse_OpenHoldsPerBuyerAreCapped(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(1000))
	buyer := uuid.New()
	for i := 0; i < DefaultOrderMcgpMaxOpenHolds; i++ {
		if _, err := r.fund(r.orderFor(t, buyer, 10)); err != nil {
			t.Fatalf("hold %d: %v", i+1, err)
		}
	}
	extra := r.orderFor(t, buyer, 10)
	if _, err := r.fund(extra); !errors.Is(err, ErrOrderMcgpTooManyOpen) {
		t.Fatalf("expected the open-hold cap, got %v", err)
	}
	if r.chain.debitCalls != DefaultOrderMcgpMaxOpenHolds {
		t.Fatalf("debits = %d, the capped payment must not be sent", r.chain.debitCalls)
	}
	if ord := r.order(t, extra.id); ord.Status != models.OrderStatusPendingPayment || ord.PaymentSource != "" {
		t.Fatalf("a refused order must stay unclaimed and payable, got %s/%q", ord.Status, ord.PaymentSource)
	}

	// Another buyer is unaffected.
	if _, err := r.fund(r.pendingOrder(t, 10)); err != nil {
		t.Fatalf("other buyer: %v", err)
	}
}

func TestOrderMcgpAbuse_RefundsPerDayStopNewPaymentsNotRefunds(t *testing.T) {
	r := newOrderRig(t)
	r.svc.SetAbuseLimits(OrderMcgpAbuseLimits{MaxOpenHolds: 10, MaxRefundsPerDay: 2})
	r.chain.buy(orderBuyerWallet, mcgpBig(1000))
	buyer := uuid.New()

	held := r.orderFor(t, buyer, 10) // stays held across the cycles below
	if _, err := r.fund(held); err != nil {
		t.Fatalf("fund held: %v", err)
	}
	for i := 0; i < 2; i++ {
		o := r.orderFor(t, buyer, 10)
		if _, err := r.fund(o); err != nil {
			t.Fatalf("cycle %d fund: %v", i, err)
		}
		if _, err := r.svc.Refund(context.Background(), o.id, refundOrder(o)); err != nil {
			t.Fatalf("cycle %d refund: %v", i, err)
		}
	}
	if _, err := r.fund(r.orderFor(t, buyer, 10)); !errors.Is(err, ErrOrderMcgpTooManyRefunds) {
		t.Fatalf("expected the daily refund bound, got %v", err)
	}
	// A refund is never refused: that would trap the buyer's MCGP.
	if _, err := r.svc.Refund(context.Background(), held.id, refundOrder(held)); err != nil {
		t.Fatalf("a refund past the bound must still go through: %v", err)
	}
	r.chain.invariants(t)
}

func TestOrderMcgpAbuse_EscrowExpiryRefundsDoNotCount(t *testing.T) {
	r := newOrderRig(t)
	r.svc.SetAbuseLimits(OrderMcgpAbuseLimits{MaxRefundsPerDay: 1})
	r.chain.buy(orderBuyerWallet, mcgpBig(1000))
	buyer := uuid.New()
	o := r.orderFor(t, buyer, 10)
	if _, err := r.fund(o); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if _, err := r.svc.refund(context.Background(), o.id, models.OrderMcgpReleaseEscrowExpired, refundOrder(o), true); err != nil {
		t.Fatalf("expiry refund: %v", err)
	}
	if _, err := r.fund(r.orderFor(t, buyer, 10)); err != nil {
		t.Fatalf("an expiry is not the buyer's refund; got %v", err)
	}
}

func TestOrderMcgpAbuse_RepeatedRefundCyclesAlert(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(1000))
	buyer := uuid.New()

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	for i := 0; i < DefaultOrderMcgpRefundCycleAlert; i++ {
		o := r.orderFor(t, buyer, 10)
		if _, err := r.fund(o); err != nil {
			t.Fatalf("fund %d: %v", i, err)
		}
		if _, err := r.svc.Refund(context.Background(), o.id, refundOrder(o)); err != nil {
			t.Fatalf("refund %d: %v", i, err)
		}
	}
	if !strings.Contains(buf.String(), "ALERT order_mcgp_refund_cycle=1 buyer="+buyer.String()) {
		t.Fatalf("expected a refund-cycle alert, log:\n%s", buf.String())
	}
}

// Checkout may use only its share of the debit window, measured from its own
// payments, so the rest stays free for bills and IT.
func TestOrderMcgpAbuse_CheckoutKeepsToItsDebitWindowShare(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(1000))
	r.chain.debitWindowCap = mcgpBig(100) // 30% share = 30 MCGP

	if _, err := r.fund(r.pendingOrder(t, 20)); err != nil {
		t.Fatalf("first payment inside the share: %v", err)
	}
	o := r.pendingOrder(t, 20)
	if _, err := r.fund(o); !errors.Is(err, ErrSpendableOverWindow) {
		t.Fatalf("expected the checkout share refusal, got %v", err)
	}
	if r.chain.debitCalls != 1 {
		t.Fatalf("debits = %d, the second payment must not be sent", r.chain.debitCalls)
	}
	// A refund does not give the window back (reverseDebit never does).
	first := r.payment(t, r.firstOrderID(t))
	if _, err := r.svc.Refund(context.Background(), first.OrderID, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if _, err := r.fund(o); !errors.Is(err, ErrSpendableOverWindow) {
		t.Fatalf("a refunded debit still used the window; got %v", err)
	}
	// Outside the look-back it no longer counts.
	if err := r.db.Exec(`UPDATE order_mcgp_payments SET debited_at = ?, created_at = ?`,
		time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.fund(o); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func (r *orderRig) firstOrderID(t *testing.T) uuid.UUID {
	t.Helper()
	var p models.OrderMcgpPayment
	if err := r.db.Order("created_at ASC").First(&p).Error; err != nil {
		t.Fatal(err)
	}
	return p.OrderID
}

// Seller credits are held to the same share of the credit window; a release
// past it waits (nothing settled) and finishes once there is room.
func TestOrderMcgpAbuse_ReleaseKeepsToItsCreditWindowShare(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(1000))
	a, b := r.pendingOrder(t, 20), r.pendingOrder(t, 20)
	for _, o := range []rigOrder{a, b} {
		if _, err := r.fund(o); err != nil {
			t.Fatalf("fund: %v", err)
		}
	}
	r.chain.creditWindowCap = mcgpBig(100) // share = 30 MCGP

	if _, err := r.release(t, a); err != nil {
		t.Fatalf("release a: %v", err)
	}
	if p := r.payment(t, a.id); p.Status != models.OrderMcgpReleased {
		t.Fatalf("a = %s, want released", p.Status)
	}
	settles := r.chain.settleCalls
	if _, err := r.release(t, b); err != nil {
		t.Fatalf("release b is decided even while it waits: %v", err)
	}
	p := r.payment(t, b.id)
	if p.Status != models.OrderMcgpReleasing || !strings.Contains(p.FailureReason, "share of the credit window") {
		t.Fatalf("b = %s (%q), want releasing and waiting on the share", p.Status, p.FailureReason)
	}
	if r.chain.settleCalls != settles {
		t.Fatal("nothing may be settled while the credit cannot land")
	}

	// The waiting row is a row outcome, not a failing worker.
	r.age(t, b.id, 10*time.Minute)
	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("a release waiting on the share must not fail the reconciler: %v", err)
	}

	// The window rolls on: a's credit leaves the look-back and b completes.
	if err := r.db.Exec(`UPDATE order_mcgp_payments SET updated_at = ? WHERE order_id = ?`, time.Now().Add(-48*time.Hour), a.id).Error; err != nil {
		t.Fatal(err)
	}
	r.age(t, b.id, 10*time.Minute)
	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if p := r.payment(t, b.id); p.Status != models.OrderMcgpReleased {
		t.Fatalf("b = %s (%q), want released once the share frees", p.Status, p.FailureReason)
	}
	r.chain.invariants(t)
}

func TestOrderMcgpAbuse_SetAbuseLimitsKeepsDefaultsForBadValues(t *testing.T) {
	s := NewOrderSpendablePaymentService(nil, nil, nil)
	s.SetAbuseLimits(OrderMcgpAbuseLimits{MaxOpenHolds: -1, MaxRefundsPerDay: 0, WindowSharePct: 150})
	if got := s.AbuseLimits(); got != DefaultOrderMcgpAbuseLimits() {
		t.Fatalf("limits = %+v, want the defaults", got)
	}
	s.SetAbuseLimits(OrderMcgpAbuseLimits{MaxOpenHolds: 7, MaxRefundsPerDay: 9, WindowSharePct: 45})
	if got := s.AbuseLimits(); got.MaxOpenHolds != 7 || got.MaxRefundsPerDay != 9 || got.WindowSharePct != 45 {
		t.Fatalf("limits = %+v", got)
	}
}

// ── R3: the caps cache ───────────────────────────────────────────────────

func TestOrderMcgpLimits_FailedReadIsCachedBriefly(t *testing.T) {
	r := newOrderRig(t)
	r.chain.limitsErr = errors.New("rpc down")

	for i := 0; i < 5; i++ {
		if err := r.svc.CheckLimits(context.Background(), mcgpBig(1)); err != nil {
			t.Fatalf("an unreadable cap never refuses: %v", err)
		}
	}
	if r.chain.limitsReads != 1 {
		t.Fatalf("reads = %d, a failed read must be remembered, not retried per call", r.chain.limitsReads)
	}

	// After the retry window it is read again, and a success is cached.
	r.chain.limitsErr = nil
	r.svc.limitsMu.Lock()
	r.svc.limitsErrAt = time.Now().Add(-orderMcgpLimitsRetry - time.Second)
	r.svc.limitsMu.Unlock()
	r.chain.limits.MaxDebitPerTx = mcgpBig(5)
	var lim *SpendableLimitError
	if err := r.svc.CheckLimits(context.Background(), mcgpBig(6)); !errors.As(err, &lim) {
		t.Fatalf("expected the cap once readable, got %v", err)
	}
	_ = r.svc.CheckLimits(context.Background(), mcgpBig(6))
	if r.chain.limitsReads != 2 {
		t.Fatalf("reads = %d, want 2", r.chain.limitsReads)
	}
}

func TestOrderMcgpLimits_CachedCheckNeverReadsTheChain(t *testing.T) {
	r := newOrderRig(t)
	r.chain.limits.MaxDebitPerTx = mcgpBig(5)

	if err := r.svc.CheckCachedLimits(mcgpBig(6)); err != nil {
		t.Fatalf("an empty cache refuses nothing: %v", err)
	}
	if r.chain.limitsReads != 0 {
		t.Fatal("CheckCachedLimits must never read the chain")
	}
	r.svc.WarmLimits(context.Background())
	if r.chain.limitsReads != 1 {
		t.Fatalf("WarmLimits reads = %d, want 1", r.chain.limitsReads)
	}
	// Even once stale, the in-transaction check uses what it has.
	r.svc.limitsMu.Lock()
	r.svc.limitsAt = time.Now().Add(-time.Hour)
	r.svc.limitsMu.Unlock()
	var lim *SpendableLimitError
	if err := r.svc.CheckCachedLimits(mcgpBig(6)); !errors.As(err, &lim) {
		t.Fatalf("expected the cached cap, got %v", err)
	}
	if r.chain.limitsReads != 1 {
		t.Fatalf("reads = %d, CheckCachedLimits read the chain", r.chain.limitsReads)
	}
}

// ── reconcile error handling ─────────────────────────────────────────────

// A row the chain refuses is logged and recorded, not returned: otherwise one
// stuck order fails every tick and pages as a dead worker.
func TestOrderMcgpReconcile_ChainRefusalsDoNotFailTheTick(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 20)
	if _, err := r.fund(o); err != nil {
		t.Fatalf("fund: %v", err)
	}
	r.chain.paused = true
	if _, err := r.release(t, o); err != nil {
		t.Fatalf("release: %v", err)
	}
	r.age(t, o.id, 10*time.Minute)
	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("a paused swap is a row outcome, got %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReleasing || !strings.Contains(p.FailureReason, "paused") {
		t.Fatalf("payment = %s (%q), want releasing with the reason recorded", p.Status, p.FailureReason)
	}
}

func TestOrderMcgpReconcile_DatabaseErrorsFailTheTick(t *testing.T) {
	r := newOrderRig(t)
	if err := r.db.Exec(`DROP TABLE order_mcgp_payments`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Reconcile(context.Background()); err == nil {
		t.Fatal("an unreadable payments table must fail the tick")
	}
}
