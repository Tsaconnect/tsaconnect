package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ─── payment layer ──────────────────────────────────────────────────────────

func loadPayment(t *testing.T, db *gorm.DB, orderID uuid.UUID) models.BillPayment {
	t.Helper()
	var pay models.BillPayment
	if err := db.Where("order_id = ?", orderID).First(&pay).Error; err != nil {
		t.Fatalf("load payment: %v", err)
	}
	return pay
}

func agePayment(t *testing.T, db *gorm.DB, orderID uuid.UUID, by time.Duration) {
	t.Helper()
	if err := db.Exec(`UPDATE bill_payments SET updated_at = ?, created_at = ? WHERE order_id = ?`,
		time.Now().Add(-by), time.Now().Add(-by), orderID).Error; err != nil {
		t.Fatal(err)
	}
}

// REGRESSION: a send that errored after the bytes may have left was marked
// failed, so a debit that then mined was never settled or reversed.
func TestBillPayment_SendErrorLeavesDebiting(t *testing.T) {
	svc, swap := newBillRig(t)
	swap.debitErr = fmt.Errorf("send raw tx: i/o timeout")
	orderID := uuid.New()
	if _, err := svc.Debit(context.Background(), debitInput(orderID, 10)); err == nil {
		t.Fatal("want the send error surfaced")
	}
	if got := loadPayment(t, svc.db, orderID); got.Status != models.BillPayStatusDebiting {
		t.Fatalf("status = %s, want debiting (outcome unknown)", got.Status)
	}
}

func TestBillPayment_VerifyErrorKeepsTxHash(t *testing.T) {
	svc, swap := newBillRig(t)
	swap.verifyErr = fmt.Errorf("receipt timeout")
	orderID := uuid.New()
	_, _ = svc.Debit(context.Background(), debitInput(orderID, 10))
	if got := loadPayment(t, svc.db, orderID); got.Status != models.BillPayStatusDebiting || got.TxHash != "0xdebit" {
		t.Fatalf("status=%s tx=%q, want debiting with the broadcast hash", got.Status, got.TxHash)
	}
}

func TestBillPayment_ResolveDebiting(t *testing.T) {
	setup := func(t *testing.T) (*BillPaymentService, *fakeBillSwap, uuid.UUID) {
		svc, swap := newBillRig(t)
		swap.verifyErr = fmt.Errorf("receipt timeout") // leaves the row debiting, debit recorded on-chain
		orderID := uuid.New()
		_, _ = svc.Debit(context.Background(), debitInput(orderID, 10))
		swap.verifyErr = nil
		return svc, swap, orderID
	}

	t.Run("on-chain pending promotes to debited", func(t *testing.T) {
		svc, swap, orderID := setup(t)
		swap.fromBought = mcgpBig(2)
		status, won, err := svc.ResolveDebiting(context.Background(), orderID)
		if err != nil || status != models.BillPayStatusDebited || !won {
			t.Fatalf("status=%s won=%v err=%v, want debited by this call", status, won, err)
		}
		// A second caller finds it already moved and must not think it won.
		if status, won, _ := svc.ResolveDebiting(context.Background(), orderID); status != models.BillPayStatusDebited || won {
			t.Fatalf("second call: status=%s won=%v, want debited and not won", status, won)
		}
		if got := loadPayment(t, svc.db, orderID); got.Status != models.BillPayStatusDebited || got.FromBought != mcgpBig(2).String() {
			t.Fatalf("row = %+v", got)
		}
	})

	t.Run("debit for another wallet is not adopted", func(t *testing.T) {
		svc, swap, orderID := setup(t)
		swap.records[billDebitRef(orderID)] = DebitRecord{User: common.HexToAddress("0x00000000000000000000000000000000000000CD"), Amount: mcgpBig(10), State: DebitStatePending}
		status, _, _ := svc.ResolveDebiting(context.Background(), orderID)
		if status != models.BillPayStatusDebiting {
			t.Fatalf("status = %s, want debiting left for an operator", status)
		}
	})

	t.Run("no on-chain debit fails only after the abandon window", func(t *testing.T) {
		svc, swap, orderID := setup(t)
		delete(swap.records, billDebitRef(orderID))
		if status, _, _ := svc.ResolveDebiting(context.Background(), orderID); status != models.BillPayStatusDebiting {
			t.Fatalf("fresh: status = %s, want debiting (a broadcast may still mine)", status)
		}
		agePayment(t, svc.db, orderID, time.Hour)
		if status, _, _ := svc.ResolveDebiting(context.Background(), orderID); status != models.BillPayStatusFailed {
			t.Fatalf("old: status = %s, want failed", status)
		}
	})

	t.Run("chain read error changes nothing", func(t *testing.T) {
		svc, swap, orderID := setup(t)
		swap.readErr = errors.New("rpc down")
		agePayment(t, svc.db, orderID, time.Hour)
		if status, _, err := svc.ResolveDebiting(context.Background(), orderID); err == nil || status != models.BillPayStatusDebiting {
			t.Fatalf("status=%s err=%v", status, err)
		}
	})
}

func TestBillPayment_AdoptLateDebit(t *testing.T) {
	svc, swap := newBillRig(t)
	orderID := uuid.New()
	swap.debitErr = notBroadcast(errors.New("gas estimation failed"))
	_, _ = svc.Debit(context.Background(), debitInput(orderID, 10))
	swap.debitErr = nil

	if adopted, err := svc.AdoptLateDebit(context.Background(), orderID); err != nil || adopted {
		t.Fatalf("adopted=%v err=%v, want nothing to adopt without an on-chain debit", adopted, err)
	}
	swap.records = map[[32]byte]DebitRecord{billDebitRef(orderID): {User: common.HexToAddress(billWallet), Amount: mcgpBig(10), State: DebitStatePending}}
	if adopted, err := svc.AdoptLateDebit(context.Background(), orderID); err != nil || !adopted {
		t.Fatalf("adopted=%v err=%v, want the late debit adopted", adopted, err)
	}
	if got := loadPayment(t, svc.db, orderID); got.Status != models.BillPayStatusDebited {
		t.Fatalf("status = %s, want debited", got.Status)
	}
}

func TestBillPayment_StuckPaymentsHonoursAge(t *testing.T) {
	svc, swap := newBillRig(t)
	swap.verifyErr = fmt.Errorf("receipt timeout")
	old, fresh := uuid.New(), uuid.New()
	_, _ = svc.Debit(context.Background(), debitInput(old, 10))
	_, _ = svc.Debit(context.Background(), debitInput(fresh, 10))
	agePayment(t, svc.db, old, time.Hour)

	rows, err := svc.StuckPayments(context.Background(), 30*time.Minute, 50)
	if err != nil || len(rows) != 1 || rows[0].OrderID != old {
		t.Fatalf("rows=%+v err=%v, want only the old payment", rows, err)
	}
}

// ─── order layer ────────────────────────────────────────────────────────────

// seedPaidOrder creates an order at `status` with a payment at `payStatus`.
func (r *billRig) seedOrder(t *testing.T, status, payStatus string, age time.Duration) uuid.UUID {
	t.Helper()
	q := r.airtimeQuote(t)
	pay := &models.BillPayment{ID: uuid.New(), UserID: r.userID, OrderID: q.Order.ID,
		WalletAddress: billWallet, McgpAmount: q.McgpAmount, NgnKobo: "100000",
		RateKobo: q.RateKobo, DebitRef: "0x" + q.Order.ID.String(), Status: payStatus}
	if err := r.db.Create(pay).Error; err != nil {
		t.Fatal(err)
	}
	backdate(t, r.db, q.Order.ID, status, age)
	agePayment(t, r.db, q.Order.ID, age)
	return q.Order.ID
}

func (r *billRig) order(t *testing.T, id uuid.UUID) models.BillOrder {
	t.Helper()
	var o models.BillOrder
	if err := r.db.First(&o, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return o
}

func TestReconcilePayingDebitingResolvesFromChain(t *testing.T) {
	t.Run("debit landed → vend", func(t *testing.T) {
		r := newBillServiceRig(t)
		id := r.seedOrder(t, models.BillStatusPaying, models.BillPayStatusDebiting, time.Hour)
		r.payer.resolveDebitingTo = models.BillPayStatusDebited
		if _, err := r.svc.Reconcile(context.Background(), 5*time.Minute); err != nil {
			t.Fatal(err)
		}
		if o := r.order(t, id); o.Status != models.BillStatusDelivered || r.vendor.vendCalls != 1 {
			t.Fatalf("status=%s vends=%d, want delivered after one vend", o.Status, r.vendor.vendCalls)
		}
	})
	t.Run("no debit → order failed, no vend", func(t *testing.T) {
		r := newBillServiceRig(t)
		id := r.seedOrder(t, models.BillStatusPaying, models.BillPayStatusDebiting, time.Hour)
		r.payer.resolveDebitingTo = models.BillPayStatusFailed
		if _, err := r.svc.Reconcile(context.Background(), 5*time.Minute); err != nil {
			t.Fatal(err)
		}
		if o := r.order(t, id); o.Status != models.BillStatusFailed || r.vendor.vendCalls != 0 {
			t.Fatalf("status=%s vends=%d, want failed without a vend", o.Status, r.vendor.vendCalls)
		}
	})
	t.Run("unknown → untouched", func(t *testing.T) {
		r := newBillServiceRig(t)
		id := r.seedOrder(t, models.BillStatusPaying, models.BillPayStatusDebiting, time.Hour)
		r.payer.resolveDebitingTo = models.BillPayStatusDebiting
		if _, err := r.svc.Reconcile(context.Background(), 5*time.Minute); err != nil {
			t.Fatal(err)
		}
		if o := r.order(t, id); o.Status != models.BillStatusPaying || r.vendor.vendCalls != 0 {
			t.Fatalf("status=%s vends=%d", o.Status, r.vendor.vendCalls)
		}
	})
}

func TestReconcileLateDebitOnFailedOrderReverses(t *testing.T) {
	r := newBillServiceRig(t)
	id := r.seedOrder(t, models.BillStatusFailed, models.BillPayStatusFailed, time.Hour)
	r.payer.lateDebit = true
	if _, err := r.svc.Reconcile(context.Background(), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if r.payer.reverses != 1 || r.payer.settles != 0 || r.vendor.vendCalls != 0 {
		t.Fatalf("reverses=%d settles=%d vends=%d, want a single reverse", r.payer.reverses, r.payer.settles, r.vendor.vendCalls)
	}
	if o := r.order(t, id); o.Status != models.BillStatusFailed {
		t.Fatalf("order status = %s, want failed (it was never vended)", o.Status)
	}
}

func TestAdminReverseBillPayment(t *testing.T) {
	cases := []struct {
		name       string
		order      string
		query      BillVendResult
		queryErr   error
		wantErr    error
		wantOrder  string
		wantRevers int
	}{
		{name: "vend confirmed failed", order: models.BillStatusVendFail, wantOrder: models.BillStatusRefunded, wantRevers: 1},
		{name: "never vended", order: models.BillStatusFailed, wantOrder: models.BillStatusFailed, wantRevers: 1},
		{name: "delivered", order: models.BillStatusDelivered, wantErr: ErrBillPaymentConflict, wantOrder: models.BillStatusDelivered},
		{name: "vendor reports success", order: models.BillStatusVending, query: BillVendResult{StatusCode: 200}, wantErr: ErrBillPaymentConflict, wantOrder: models.BillStatusVending},
		{name: "vendor reports pending", order: models.BillStatusVending, query: BillVendResult{StatusCode: 300}, wantErr: ErrBillPaymentConflict, wantOrder: models.BillStatusVending},
		{name: "vendor unreachable", order: models.BillStatusPaid, queryErr: errors.New("timeout"), wantErr: ErrBillVendUnverified, wantOrder: models.BillStatusPaid},
		{name: "vendor reports terminal failure", order: models.BillStatusVending, query: BillVendResult{OrderID: "904", StatusCode: 510, Status: "ORDER_CANCELLED"}, wantOrder: models.BillStatusRefunded, wantRevers: 1},
		{name: "vendor 4xx on the query", order: models.BillStatusVending, query: BillVendResult{OrderID: "904", StatusCode: 400, Status: "INVALID_REQUEST"}, wantErr: ErrBillVendUnverified, wantOrder: models.BillStatusVending},
		{name: "vendor failure without an order id", order: models.BillStatusVending, query: BillVendResult{StatusCode: 510, Status: "ORDER_CANCELLED"}, wantErr: ErrBillVendUnverified, wantOrder: models.BillStatusVending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newBillServiceRig(t)
			id := r.seedOrder(t, tc.order, models.BillPayStatusDebited, time.Hour)
			r.vendor.queryResult, r.vendor.queryErr = tc.query, tc.queryErr
			_, err := r.svc.AdminReversePayment(context.Background(), id)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("err = %v", err)
			}
			if r.payer.reverses != tc.wantRevers || r.payer.settles != 0 {
				t.Fatalf("reverses=%d settles=%d, want %d reverses", r.payer.reverses, r.payer.settles, tc.wantRevers)
			}
			if o := r.order(t, id); o.Status != tc.wantOrder {
				t.Fatalf("order = %s, want %s", o.Status, tc.wantOrder)
			}
		})
	}
}

func TestAdminReverseBillPayment_UnconfirmedDebitIsRefused(t *testing.T) {
	r := newBillServiceRig(t)
	id := r.seedOrder(t, models.BillStatusPaying, models.BillPayStatusDebiting, time.Hour)
	r.payer.resolveDebitingTo = models.BillPayStatusDebiting
	if _, err := r.svc.AdminReversePayment(context.Background(), id); !errors.Is(err, ErrBillPayBadState) || r.payer.reverses != 0 {
		t.Fatalf("err=%v reverses=%d, want refusal until the debit is confirmed", err, r.payer.reverses)
	}
}

func TestAdminSettleBillPayment(t *testing.T) {
	cases := []struct {
		name      string
		order     string
		query     BillVendResult
		wantErr   error
		wantOrder string
		settles   int
	}{
		{name: "delivered", order: models.BillStatusDelivered, wantOrder: models.BillStatusDelivered, settles: 1},
		{name: "vendor reports success", order: models.BillStatusVending, query: BillVendResult{OrderID: "905", StatusCode: 200, Delivery: "TOKEN-1"}, wantOrder: models.BillStatusDelivered, settles: 1},
		{name: "vendor reports pending", order: models.BillStatusVending, query: BillVendResult{StatusCode: 300}, wantErr: ErrBillPaymentConflict, wantOrder: models.BillStatusVending},
		{name: "vend failed", order: models.BillStatusVendFail, wantErr: ErrBillPaymentConflict, wantOrder: models.BillStatusVendFail},
		{name: "never vended", order: models.BillStatusFailed, wantErr: ErrBillPaymentConflict, wantOrder: models.BillStatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newBillServiceRig(t)
			id := r.seedOrder(t, tc.order, models.BillPayStatusDebited, time.Hour)
			r.vendor.queryResult = tc.query
			_, err := r.svc.AdminSettlePayment(context.Background(), id)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("err = %v", err)
			}
			if r.payer.settles != tc.settles || r.payer.reverses != 0 {
				t.Fatalf("settles=%d reverses=%d", r.payer.settles, r.payer.reverses)
			}
			if o := r.order(t, id); o.Status != tc.wantOrder {
				t.Fatalf("order = %s, want %s", o.Status, tc.wantOrder)
			}
		})
	}
}

func TestListStuckBillPayments(t *testing.T) {
	r := newBillServiceRig(t)
	stuck := r.seedOrder(t, models.BillStatusVending, models.BillPayStatusDebited, time.Hour)
	r.payer.stuck = []models.BillPayment{loadPayment(t, r.db, stuck)}

	rows, err := r.svc.ListStuckPayments(context.Background(), 30*time.Minute, 50)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows[0].OrderID != stuck || rows[0].OrderStatus != models.BillStatusVending || rows[0].Category != models.BillCategoryAirtime {
		t.Fatalf("row lacks order evidence: %+v", rows[0])
	}
	_ = big.NewInt
}

// ─── review round ───────────────────────────────────────────────────────────

// C4: a second reconcile pass holding a stale copy of the order must neither
// vend again nor overwrite what the first pass delivered.
func TestC4_StaleReconcilePassVendsOnce(t *testing.T) {
	r := newBillServiceRig(t)
	id := r.seedOrder(t, models.BillStatusPaying, models.BillPayStatusDebiting, time.Hour)
	r.vendor.vendResult = BillVendResult{OrderID: "910", StatusCode: 200, Status: "ORDER_COMPLETED", Delivery: "PIN-FIRST"}
	stale := r.order(t, id)
	first := r.order(t, id)

	r.payer.resolveDebitingTo = models.BillPayStatusDebited
	r.svc.reconcileOrder(context.Background(), &first)
	if o := r.order(t, id); o.Status != models.BillStatusDelivered || o.DeliveryPayload != "PIN-FIRST" {
		t.Fatalf("first pass: %+v", o)
	}

	// The stale pass read the payment before the first pass moved it and lost
	// the payment transition.
	r.db.Exec(`UPDATE bill_payments SET status = ? WHERE order_id = ?`, models.BillPayStatusDebiting, id)
	lost := false
	r.payer.resolveDebitingWon = &lost
	r.vendor.vendResult = BillVendResult{OrderID: "911", StatusCode: 200, Status: "ORDER_COMPLETED", Delivery: "PIN-SECOND"}
	r.svc.reconcileOrder(context.Background(), &stale)

	if r.vendor.vendCalls != 1 {
		t.Fatalf("vendCalls = %d, want exactly one vend", r.vendor.vendCalls)
	}
	if o := r.order(t, id); o.Status != models.BillStatusDelivered || o.DeliveryPayload != "PIN-FIRST" {
		t.Fatalf("delivered order overwritten by the stale pass: %+v", o)
	}
}

// C4: the payment transition alone must not be enough — a stale pass that
// did win it still cannot vend an order another pass already moved.
func TestC4_OrderClaimBlocksStaleVendEvenAfterPaymentWin(t *testing.T) {
	r := newBillServiceRig(t)
	id := r.seedOrder(t, models.BillStatusPaying, models.BillPayStatusDebited, time.Hour)
	stale := r.order(t, id)
	first := r.order(t, id)
	r.vendor.vendResult = BillVendResult{OrderID: "912", StatusCode: 200, Status: "ORDER_COMPLETED", Delivery: "PIN-A"}
	// A deferred settle keeps the payment `debited`, so the stale pass still
	// takes the debited branch and only the order claim can stop it.
	r.payer.settleErr = errors.New("rpc down")

	r.svc.reconcileOrder(context.Background(), &first)
	r.svc.reconcileOrder(context.Background(), &stale)
	if r.vendor.vendCalls != 1 {
		t.Fatalf("vendCalls = %d, want 1", r.vendor.vendCalls)
	}
	if o := r.order(t, id); o.DeliveryPayload != "PIN-A" {
		t.Fatalf("payload = %q", o.DeliveryPayload)
	}
}

func TestC4_PaidBranchClaimsBeforeRevend(t *testing.T) {
	r := newBillServiceRig(t)
	id := r.seedOrder(t, models.BillStatusPaid, models.BillPayStatusDebited, time.Hour)
	r.vendor.queryErr = fmt.Errorf("ORDER_NOT_FOUND")
	r.vendor.vendResult = BillVendResult{OrderID: "913", StatusCode: 300, Status: "ORDER_PROCESSED"}
	first := r.order(t, id)
	stale := r.order(t, id)

	r.svc.reconcileOrder(context.Background(), &first)
	r.svc.reconcileOrder(context.Background(), &stale)
	if r.vendor.vendCalls != 1 {
		t.Fatalf("vendCalls = %d, want one re-vend", r.vendor.vendCalls)
	}
}

func TestC4_AdoptVendResultNeverOverwritesATerminalOrder(t *testing.T) {
	r := newBillServiceRig(t)
	id := r.seedOrder(t, models.BillStatusVending, models.BillPayStatusDebited, time.Hour)
	stale := r.order(t, id)
	r.db.Exec(`UPDATE bill_orders SET status = ?, delivery_payload = ? WHERE id = ?`, models.BillStatusDelivered, "TOKEN-REAL", id)

	_ = r.svc.adoptVendResult(context.Background(), &stale, BillVendResult{OrderID: "914", StatusCode: 510, Status: "ORDER_CANCELLED"})
	if o := r.order(t, id); o.Status != models.BillStatusDelivered || o.DeliveryPayload != "TOKEN-REAL" {
		t.Fatalf("order = %+v, want the delivered row untouched", o)
	}
	if r.payer.reverses != 0 {
		t.Fatal("a stale failure result must not reverse a delivered bill")
	}
}

// I1: rows the reconciler can only alert on must not starve the queue.
func TestI1Bills_AlertOnlyOrdersDoNotBlockNewOnes(t *testing.T) {
	r := newBillServiceRig(t)
	for i := 0; i < 105; i++ {
		r.seedOrder(t, models.BillStatusPaying, models.BillPayStatusDebiting, 2*time.Hour)
	}
	id := r.seedOrder(t, models.BillStatusPaying, models.BillPayStatusDebited, time.Hour)
	r.vendor.vendResult = BillVendResult{OrderID: "920", StatusCode: 200, Status: "ORDER_COMPLETED"}

	for tick := 0; tick < 2 && r.order(t, id).Status != models.BillStatusDelivered; tick++ {
		if _, err := r.svc.Reconcile(context.Background(), 5*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if o := r.order(t, id); o.Status != models.BillStatusDelivered {
		t.Fatalf("status = %s, want the actionable order reached within two ticks", o.Status)
	}
}

func TestI1Bills_LateDebitScanIsPaged(t *testing.T) {
	r := newBillServiceRig(t)
	for i := 0; i < 55; i++ {
		r.seedOrder(t, models.BillStatusFailed, models.BillPayStatusFailed, 2*time.Hour)
	}
	id := r.seedOrder(t, models.BillStatusFailed, models.BillPayStatusFailed, time.Hour)
	r.payer.lateDebitOrders = map[uuid.UUID]bool{id: true}

	for tick := 0; tick < 2 && r.payer.reverses == 0; tick++ {
		if _, err := r.svc.Reconcile(context.Background(), 5*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if r.payer.reverses != 1 {
		t.Fatalf("reverses = %d, want the late debit found and reversed within two ticks", r.payer.reverses)
	}
}

// I4: an admin reverse must not race a vend that may still be in flight.
func TestI4_AdminReverseRefusesRecentVend(t *testing.T) {
	r := newBillServiceRig(t)
	id := r.seedOrder(t, models.BillStatusVending, models.BillPayStatusDebited, time.Minute)
	r.vendor.queryResult = BillVendResult{OrderID: "930", StatusCode: 510, Status: "ORDER_CANCELLED"}
	if _, err := r.svc.AdminReversePayment(context.Background(), id); !errors.Is(err, ErrBillVendUnverified) {
		t.Fatalf("err = %v, want unverified while the vend may be in flight", err)
	}
	if r.payer.reverses != 0 {
		t.Fatal("must not reverse")
	}
}

func TestAdminSettleBillPayment_SettleFailureIsReturned(t *testing.T) {
	r := newBillServiceRig(t)
	id := r.seedOrder(t, models.BillStatusVending, models.BillPayStatusDebited, time.Hour)
	r.vendor.queryResult = BillVendResult{OrderID: "940", StatusCode: 200, Status: "ORDER_COMPLETED"}
	r.payer.settleErr = errors.New("rpc down")
	if _, err := r.svc.AdminSettlePayment(context.Background(), id); err == nil {
		t.Fatal("a settle that failed must be reported to the operator")
	}
}

func TestAdminBillPayment_ChainReadErrorIsUnverified(t *testing.T) {
	r := newBillServiceRig(t)
	id := r.seedOrder(t, models.BillStatusVendFail, models.BillPayStatusDebiting, time.Hour)
	r.payer.resolveDebitingErr = errors.New("rpc down")
	if _, err := r.svc.AdminReversePayment(context.Background(), id); !errors.Is(err, ErrBillDebitUnverified) {
		t.Fatalf("err = %v, want ErrBillDebitUnverified", err)
	}
}

// I6: a debit landing after the 48h window is still reported.
func TestI6Bills_LateDebitBeyondWindowIsReported(t *testing.T) {
	svc, swap := newBillRig(t)
	orderID := uuid.New()
	swap.debitErr = notBroadcast(errors.New("gas estimation failed"))
	_, _ = svc.Debit(context.Background(), debitInput(orderID, 10))
	swap.records = map[[32]byte]DebitRecord{billDebitRef(orderID): {User: common.HexToAddress(billWallet), Amount: mcgpBig(10), State: DebitStatePending}}
	agePayment(t, svc.db, orderID, 72*time.Hour)

	n, err := svc.AlertLateDebitsBeyondWindow(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, want one alert", n, err)
	}
	if got := loadPayment(t, svc.db, orderID); got.Status != models.BillPayStatusFailed {
		t.Fatalf("status = %s — beyond the window the audit only alerts", got.Status)
	}
}

func TestI6Bills_ReconcileRunsTheDailyAudit(t *testing.T) {
	r := newBillServiceRig(t)
	_, _ = r.svc.Reconcile(context.Background(), 5*time.Minute)
	_, _ = r.svc.Reconcile(context.Background(), 5*time.Minute)
	if r.payer.deepAudits != 1 {
		t.Fatalf("audits = %d, want exactly one per day", r.payer.deepAudits)
	}
}
