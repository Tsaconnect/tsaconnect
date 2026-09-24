package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// Whole-branch review of TSA Pay: transitions guarded on the attempt read, an
// attempt that may exist is never released or retried, an expired order is
// proven empty, and no wait is silent forever.

// startedOrder starts alice's direct payment and stores the order row the fake
// returned, at a deposit address, in the status the test names.
func (r *payCheckoutRig) startedOrder(t *testing.T, status string, mutate func(*models.PaycrestOrderRecord)) *models.PaycrestOrderRecord {
	t.Helper()
	order, err := r.direct(t)
	if err != nil {
		t.Fatal(err)
	}
	o := &models.PaycrestOrderRecord{ID: order.ID, UserID: r.alice.ID, Direction: models.PaycrestDirectionSell, Status: status,
		Reference: "PCS-" + order.ID.String()[:8], DepositAddress: "0xdeposit", ChainKey: "base", Token: "USDC", PayPaymentID: &r.p.ID}
	if mutate != nil {
		mutate(o)
	}
	if err := r.pay.db.Create(o).Error; err != nil {
		t.Fatal(err)
	}
	return o
}

// ---- B1: a cancelled delivery is not an instant release ----
// (rows in TestPayReconcile_CryptoIntentOutcomes; the naira row keeps its immediate release)

// ---- B2: an order that may still exist keeps its claim ----

func TestSellIntent_OpenOrderKeepsTheClaimWhenAnOrderMayStillExist(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		cancelFails bool
		wantStatus  string
	}{
		"the short order could not be cancelled":          {true, models.SellIntentOrdering},
		"the short order was cancelled: claim given back": {false, models.SellIntentSwapped},
	} {
		t.Run(name, func(t *testing.T) {
			r := payRig(t)
			r.base.amount = big.NewInt(34_000000)
			r.svc.SetUSDCBalanceReader(balanceOf(500_000000))
			in, _, err := r.svc.Create(ctx, r.user, payIntentInput(uuid.New(), r.user))
			if err != nil {
				t.Fatal(err)
			}
			r.mustRecord(t, in.ID, testSwapHash)
			r.status.st = r.landed(in)
			r.client.offrampRate = "1400" // the provider's order pays short of the naira owed
			if tc.cancelFails {
				failOrderWrites(t, r.svc.db, "update")
			}
			logs := captureLog(t)
			_, err = r.svc.OpenOrder(ctx, r.user, in.ID, false)
			if tc.cancelFails != errors.Is(err, ErrSellAttemptMayExist) || !errors.Is(err, ErrSellRateMoved) {
				t.Fatalf("err = %v", err)
			}
			if got := r.reload(t, in.ID); got.Status != tc.wantStatus {
				t.Fatalf("intent status %s, want %s", got.Status, tc.wantStatus)
			}
			if tc.cancelFails && !strings.Contains(logs.String(), "ALERT ramp_sell_attempt_may_exist_total=1 intent="+in.ID.String()) {
				t.Fatalf("no ALERT in %q", logs.String())
			}
		})
	}
}

// staleOrdering is a converted payment whose intent died `ordering` with a swap
// recorded, at the moment the claim has just gone stale.
func (r *payCheckoutRig) staleOrdering(t *testing.T) *models.RampSellIntent {
	t.Helper()
	in := r.startConverted(t)
	r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{
		"status": models.SellIntentOrdering, "swap_tx_hash": "0x" + strings.Repeat("ab", 32), "updated_at": payTestNow})
	return in
}

func TestPayReconcile_AStaleOrderingClaimAdoptsTheOrderItOpenedForThisPayment(t *testing.T) {
	r := newPayCheckoutRig(t)
	in := r.staleOrdering(t)
	o := &models.PaycrestOrderRecord{UserID: r.alice.ID, Direction: models.PaycrestDirectionSell, Status: models.PaycrestStatusAwaitingDeposit,
		Reference: "PCS-adopt", DepositAddress: "0xdep", PayPaymentID: &r.p.ID}
	if err := r.pay.db.Create(o).Error; err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	got := r.tickAt(t, sellIntentClaimStale+time.Minute)
	if got.Status != models.PayStatusProcessing || got.PaycrestOrderID == nil || *got.PaycrestOrderID != o.ID || got.SellIntentID != nil {
		t.Fatalf("payment = %+v", got)
	}
	var stored models.RampSellIntent
	r.pay.db.First(&stored, "id = ?", in.ID)
	if stored.Status != models.SellIntentOrdered || stored.PaycrestOrderID == nil || *stored.PaycrestOrderID != o.ID || stored.AccountNumber != "" {
		t.Fatalf("intent = %+v: recorded as ordered with its order, bank details cleared", stored)
	}
	if !strings.Contains(logs.String(), "ALERT pay_ordering_order_adopted_total=1") || r.intents.abandoned != 0 {
		t.Fatalf("logs %q abandoned %d", logs.String(), r.intents.abandoned)
	}
	// From here the order's own rules decide it.
	r.pay.db.Model(o).Update("status", models.PaycrestStatusSettled)
	if got := r.tickAt(t, sellIntentClaimStale+2*time.Minute); got.Status != models.PayStatusSucceeded {
		t.Fatalf("payment = %+v", got)
	}
}

func TestPayReconcile_AStaleOrderingClaimIsNeverReleasedAndAdoptsNoOneElsesOrder(t *testing.T) {
	other := uuid.New()
	for name, mutate := range map[string]func(*models.PaycrestOrderRecord){
		"none at all":                     nil,
		"another payment's":               func(o *models.PaycrestOrderRecord) { o.PayPaymentID = &other },
		"not marked as a payment's":       func(o *models.PaycrestOrderRecord) { o.PayPaymentID = nil },
		"another user's":                  func(o *models.PaycrestOrderRecord) { o.UserID = uuid.New() },
		"cancelled, deposit withdrawn":    func(o *models.PaycrestOrderRecord) { o.Status, o.DepositAddress = models.PaycrestStatusCancelled, "" },
		"opened before she took the lock": func(o *models.PaycrestOrderRecord) { o.CreatedAt = payTestNow.Add(-time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newPayCheckoutRig(t)
			in := r.staleOrdering(t)
			if mutate != nil {
				o := &models.PaycrestOrderRecord{UserID: r.alice.ID, Direction: models.PaycrestDirectionSell, Status: models.PaycrestStatusAwaitingDeposit,
					Reference: "PCS-" + uuid.NewString()[:8], DepositAddress: "0xdep", PayPaymentID: &r.p.ID}
				mutate(o)
				if err := r.pay.db.Create(o).Error; err != nil {
					t.Fatal(err)
				}
			}
			got := r.tickAt(t, sellIntentClaimStale+time.Minute)
			var stored models.RampSellIntent
			r.pay.db.First(&stored, "id = ?", in.ID)
			// Failed for support — never released, never handed to AbandonForPay
			// (the intent is ended by the reconciler's own guarded write), and no
			// order adopted.
			if got.Status != models.PayStatusFailed || got.PayerUserID == nil || got.PaycrestOrderID != nil ||
				stored.Status != models.SellIntentFailed || r.intents.abandoned != 0 {
				t.Fatalf("payment %+v intent %s abandoned %d", got, stored.Status, r.intents.abandoned)
			}
		})
	}
}

// ---- B4: a transition only matches the attempt it was decided from ----

func TestPayTransitions_AreGuardedOnTheAttemptTheCallerRead(t *testing.T) {
	ctx := context.Background()
	for name, do := range map[string]func(r *payCheckoutRig, stale *models.PayPayment) error{
		"release": func(r *payCheckoutRig, p *models.PayPayment) error { return r.pay.releaseFrom(ctx, p, "x") },
		"fail":    func(r *payCheckoutRig, p *models.PayPayment) error { return r.pay.failFrom(ctx, p, "x") },
		"succeed": func(r *payCheckoutRig, p *models.PayPayment) error { return r.pay.succeedFrom(ctx, p, "x") },
	} {
		t.Run(name, func(t *testing.T) {
			r := newPayCheckoutRig(t)
			r.lockAlice(t)
			stale := r.get(t) // read while nothing was linked
			order := uuid.New()
			if err := r.pay.LinkOrder(ctx, r.p.ID, order); err != nil { // a new attempt lands
				t.Fatal(err)
			}
			if err := do(r, stale); !errors.Is(err, ErrPayTransition) {
				t.Fatalf("err = %v: a decision made on a payment with no attempt moved one that has one", err)
			}
			if got := r.get(t); got.Status != models.PayStatusProcessing || got.PaycrestOrderID == nil || *got.PaycrestOrderID != order {
				t.Fatalf("payment = %+v", got)
			}
			// Read again, the same call matches.
			if err := do(r, r.get(t)); err != nil {
				t.Fatalf("guarded on the attempt it did read: %v", err)
			}
		})
	}
	t.Run("a swapped intent is a different attempt", func(t *testing.T) {
		r := newPayCheckoutRig(t)
		r.lockAlice(t)
		first := uuid.New()
		r.pay.LinkIntent(ctx, r.p.ID, first)
		stale := r.get(t)
		r.pay.db.Model(&models.PayPayment{}).Where("id = ?", r.p.ID).Update("sell_intent_id", uuid.New())
		if err := r.pay.releaseFrom(ctx, stale, "x"); !errors.Is(err, ErrPayTransition) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the unguarded calls keep their contract", func(t *testing.T) {
		r := newPayCheckoutRig(t)
		r.lockAlice(t)
		r.pay.LinkOrder(ctx, r.p.ID, uuid.New())
		if err := r.pay.Release(ctx, r.p.ID, "x"); err != nil {
			t.Fatal(err)
		}
	})
}

// The reconciler reads a payment, asks LiFi for up to 15 s, then decides. If the
// payment was released and re-locked with a NEW attempt meanwhile (a slower
// instance, a deploy overlap), the stale decision must not release it.
func TestPayReconcile_AnAttemptThatChangesUnderAReconcilerIsNotReleased(t *testing.T) {
	r := newPayCheckoutRig(t)
	in := r.startConverted(t)
	r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"status": models.SellIntentSwapped, "swap_tx_hash": "0x" + strings.Repeat("ab", 32)})
	r.intents.status = &LiFiTransferStatus{Status: LiFiStatusFailed} // conclusive: the sweep decides Release
	newIntent := uuid.New()
	r.intents.statusHook = func() {
		r.pay.db.Model(&models.PayPayment{}).Where("id = ?", r.p.ID).Update("sell_intent_id", newIntent)
	}
	got := r.tickAt(t, time.Minute)
	if got.Status != models.PayStatusProcessing || got.SellIntentID == nil || *got.SellIntentID != newIntent {
		t.Fatalf("payment = %+v: a stale decision released the new attempt", got)
	}
}

func TestPayReleaseUnattempted_NeverReleasesAPaymentThatLinkedAnAttemptSinceTheSelect(t *testing.T) {
	ctx := context.Background()
	r := newPayCheckoutRig(t)
	r.lockAlice(t)
	var stale []models.PayPayment
	r.pay.db.Where("id = ?", r.p.ID).Find(&stale) // what releaseUnattempted selected
	r.pay.LinkIntent(ctx, r.p.ID, uuid.New())
	if err := r.pay.releaseFrom(ctx, &stale[0], "released: no payment was started"); !errors.Is(err, ErrPayTransition) {
		t.Fatalf("err = %v", err)
	}
	if got := r.get(t); got.Status != models.PayStatusProcessing || got.SellIntentID == nil {
		t.Fatalf("payment = %+v", got)
	}
}

// ---- B5: an expired order is proven empty before its payment is handed back ----

func TestPayReconcile_AnExpiredOrderIsOnlyReleasedOnceItsDepositAddressIsProvenEmpty(t *testing.T) {
	for name, tc := range map[string]struct {
		setup    func(r *payCheckoutRig, o *models.PaycrestOrderRecord)
		want     string
		reads    int
		alertHas string
	}{
		"the address holds tokens": {func(r *payCheckoutRig, _ *models.PaycrestOrderRecord) { r.deposit.bal = big.NewInt(1) }, models.PayStatusFailed, 1, "order_expired_after_deposit"},
		"Paycrest reported the deposit tx": {func(r *payCheckoutRig, o *models.PaycrestOrderRecord) {
			r.pay.db.Model(o).Update("tx_hash", "0xdeposit")
		}, models.PayStatusFailed, 0, "order_expired_after_deposit"},
		"Paycrest returned money to her": {func(r *payCheckoutRig, o *models.PaycrestOrderRecord) {
			r.pay.db.Model(o).Update("amount_returned", "12.5")
		}, models.PayStatusFailed, 0, "order_expired_after_deposit"},
		"the address is empty":     {func(r *payCheckoutRig, _ *models.PaycrestOrderRecord) {}, models.PayStatusPending, 1, ""},
		"the chain cannot be read": {func(r *payCheckoutRig, _ *models.PaycrestOrderRecord) { r.deposit.err = errors.New("rpc down") }, models.PayStatusProcessing, 1, "expired_order_unverified"},
		"no reader is wired":       {func(r *payCheckoutRig, _ *models.PaycrestOrderRecord) { r.pay.SetDepositReader(nil) }, models.PayStatusProcessing, 0, "expired_order_unverified"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newPayCheckoutRig(t)
			o := r.startedOrder(t, models.PaycrestStatusExpired, nil)
			tc.setup(r, o)
			logs := captureLog(t)
			got := r.tickAt(t, time.Hour)
			if got.Status != tc.want {
				t.Fatalf("payment %s, want %s", got.Status, tc.want)
			}
			if r.deposit.calls != tc.reads {
				t.Fatalf("%d chain reads, want %d", r.deposit.calls, tc.reads)
			}
			if tc.alertHas != "" && !strings.Contains(logs.String(), "ALERT pay_"+tc.alertHas+"_total=1") {
				t.Fatalf("no %s ALERT in %q", tc.alertHas, logs.String())
			}
		})
	}
}

func TestPayReconcile_AnUnverifiableExpiredOrderAlertsOnceAndFailsAfterTheBound(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.startedOrder(t, models.PaycrestStatusExpired, nil)
	r.deposit.err = errors.New("rpc down")
	logs := captureLog(t)
	r.tickAt(t, time.Hour)
	r.tickAt(t, time.Hour+30*time.Second)
	if n := strings.Count(logs.String(), "ALERT pay_expired_order_unverified_total=1"); n != 1 {
		t.Fatalf("%d alerts, want 1: %q", n, logs.String())
	}
	if got := r.tickAt(t, payStuckWaitMax+time.Hour); got.Status != models.PayStatusFailed || got.PayerUserID == nil {
		t.Fatalf("payment = %+v: never released, failed for support once the wait ran out", got)
	}
	// Once the chain can be read the wait would have ended differently.
	r2 := newPayCheckoutRig(t)
	r2.startedOrder(t, models.PaycrestStatusExpired, nil)
	r2.deposit.err = errors.New("rpc down")
	r2.tickAt(t, time.Hour)
	r2.deposit.err = nil
	if got := r2.tickAt(t, time.Hour+time.Minute); got.Status != models.PayStatusPending {
		t.Fatalf("payment = %+v", got)
	}
}

// ---- B6: no stale reason follows a payment to succeeded ----

func TestPay_AReleasedThenRetriedPaymentSucceedsWithoutTheOldReason(t *testing.T) {
	r := newPayCheckoutRig(t)
	withWebhooks(t, r.pay, r.m)
	first := r.startedOrder(t, models.PaycrestStatusRefunded, nil)
	if got := r.tickAt(t, time.Minute); got.Status != models.PayStatusPending || !strings.Contains(got.FailureReason, "refunded") {
		t.Fatalf("after the release: %+v", got)
	}
	bob := payUser(t, r.pay, true)
	second, err := r.svc.PayDirect(context.Background(), bob, r.p.ID, PayDirectInput{ChainKey: "base", Token: "USDC"})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.get(t); got.FailureReason != "" {
		t.Fatalf("the new attempt's lock kept %q", got.FailureReason)
	}
	r.pay.db.Create(&models.PaycrestOrderRecord{ID: second.ID, UserID: bob.ID, Direction: models.PaycrestDirectionSell,
		Status: models.PaycrestStatusSettled, Reference: "PCS-second"})
	got := r.tickAt(t, 2*time.Minute)
	if got.Status != models.PayStatusSucceeded || got.FailureReason != "" || got.SettlementRef != second.ID.String() {
		t.Fatalf("payment = %+v", got)
	}
	if view := r.pay.View(got); view.FailureReason != "" {
		t.Fatalf("the merchant's view says %q", view.FailureReason)
	}
	ds := deliveries(t, r.pay)
	last := ds[len(ds)-1]
	if last.Event != models.PayEventSucceeded || strings.Contains(last.Payload, "refunded") || strings.Contains(last.Payload, first.ID.String()) {
		t.Fatalf("payment.succeeded payload = %s", last.Payload)
	}
}

// ---- B7 ----

// (a) An earlier attempt's ordered intent is not this attempt's.
func TestPayReconcile_AnIntentOfAnEarlierAttemptIsNotAdopted(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.lockAlice(t)
	r.orphanIntent(t, models.SellIntentOrdered, "0x"+strings.Repeat("ab", 32), func(in *models.RampSellIntent) {
		in.CreatedAt = payTestNow.Add(-time.Hour)
	})
	if got := r.tickAt(t, 11*time.Minute); got.Status != models.PayStatusPending || got.SellIntentID != nil {
		t.Fatalf("payment = %+v", got)
	}
}

// (b) A call on the wrong route or a test payment never takes the payment's lock.
func TestPayCheckout_AWrongRouteCallNeverWritesToThePayment(t *testing.T) {
	r := newCryptoRig(t)
	var writes atomic.Int32
	if err := r.pay.db.Callback().Update().Before("gorm:update").Register("test:count_pay_writes", func(tx *gorm.DB) {
		if tx.Statement.Table == "pay_payments" {
			writes.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.direct(t); !errors.Is(err, ErrPayWrongRoute) { // naira route, crypto payment
		t.Fatalf("err = %v", err)
	}
	r.pay.db.Model(&models.PayPayment{}).Where("id = ?", r.p.ID).Update("mode", models.PayModeTest)
	writes.Store(0)
	if _, _, err := r.crypto(t); !errors.Is(err, ErrPayNotPayable) {
		t.Fatalf("err = %v", err)
	}
	if n := writes.Load(); n != 0 {
		t.Fatalf("%d writes to the payment: a refused call locked it", n)
	}
}

// (c) A LiFi outage never stalls order settlement, and is asked about once a tick.
func TestPayReconcile_ALiFiOutageDoesNotStallOrdersAndIsNotAskedAgainThisTick(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.startedOrder(t, models.PaycrestStatusSettled, nil)
	for i := 0; i < 3; i++ {
		u := payUser(t, r.pay, true)
		in := payIn()
		in.Reference = fmt.Sprintf("OUTAGE-%d", i)
		p, _, _ := r.pay.Create(context.Background(), r.m, models.PayModeLive, in)
		intent, _, err := r.svc.PayConverted(context.Background(), u, p.ID, PayConvertedInput{SourceChainID: 56, SourceToken: evmNativeAddress, SourceAddress: u.WalletAddress})
		if err != nil {
			t.Fatal(err)
		}
		row := *intent
		row.UserID, row.WalletAddress, row.SourceAddress, row.SourceChainID = u.ID, u.WalletAddress, u.WalletAddress, 56
		row.SourceTokenAddress, row.SourceAmount, row.FiatCode, row.Status = evmNativeAddress, "1", "NGN", models.SellIntentSwapped
		h := "0x" + strings.Repeat(fmt.Sprintf("%02x", i+1), 32)
		row.SwapTxHash = &h
		if err := r.pay.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	r.intents.statusErr = errors.New("lifi is down")
	r.pay.now = func() time.Time { return payTestNow.Add(time.Minute) }
	if err := r.pay.ReconcileTick(context.Background()); err == nil {
		t.Fatal("the outage must still reach the failure-streak alert")
	}
	if got := r.get(t); got.Status != models.PayStatusSucceeded {
		t.Fatalf("the settled order's payment was stalled: %+v", got)
	}
	if r.intents.statusCalls != 1 {
		t.Fatalf("%d LiFi reads in one tick after the first failure, want 1", r.intents.statusCalls)
	}
	// Next tick LiFi is asked again.
	r.intents.statusErr = nil
	before := r.intents.statusCalls
	r.pay.now = func() time.Time { return payTestNow.Add(2 * time.Minute) }
	r.pay.ReconcileTick(context.Background())
	if r.intents.statusCalls-before != 3 {
		t.Fatalf("next tick made %d reads, want 3", r.intents.statusCalls-before)
	}
}

// (d) No wait is silent forever.
func TestPayReconcile_EveryWaitItCannotResolveAlertsOnceAndFailsAfterTheBound(t *testing.T) {
	for name, tc := range map[string]struct {
		alert string
		setup func(t *testing.T) (tick func(d time.Duration) *models.PayPayment, id uuid.UUID)
	}{
		"no intent service wired": {"no_intent_service", func(t *testing.T) (func(time.Duration) *models.PayPayment, uuid.UUID) {
			r := newPayCheckoutRig(t)
			in := r.startConverted(t)
			r.pay.intents = nil
			return func(d time.Duration) *models.PayPayment { return r.tickAt(t, d) }, in.ID
		}},
		"the linked order has no row": {"order_row_missing", func(t *testing.T) (func(time.Duration) *models.PayPayment, uuid.UUID) {
			r := newPayCheckoutRig(t)
			o, _ := r.direct(t)
			return func(d time.Duration) *models.PayPayment { return r.tickAt(t, d) }, o.ID
		}},
		"the order's status is unknown": {"order_status_unknown", func(t *testing.T) (func(time.Duration) *models.PayPayment, uuid.UUID) {
			r := newPayCheckoutRig(t)
			o := r.startedOrder(t, "quantum", nil)
			return func(d time.Duration) *models.PayPayment { return r.tickAt(t, d) }, o.ID
		}},
		"the intent has no row": {"intent_row_missing", func(t *testing.T) (func(time.Duration) *models.PayPayment, uuid.UUID) {
			r := newPayCheckoutRig(t)
			in, _, _ := r.converted(t)
			return func(d time.Duration) *models.PayPayment { return r.tickAt(t, d) }, in.ID
		}},
		"ordered with no order id": {"ordered_without_order", func(t *testing.T) (func(time.Duration) *models.PayPayment, uuid.UUID) {
			r := newPayCheckoutRig(t)
			in := r.startConverted(t)
			r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Update("status", models.SellIntentOrdered)
			return func(d time.Duration) *models.PayPayment { return r.tickAt(t, d) }, in.ID
		}},
		"the delivery cannot be verified": {"delivery_unverifiable", func(t *testing.T) (func(time.Duration) *models.PayPayment, uuid.UUID) {
			r := newCryptoRig(t)
			in := r.sentSwap(t)
			r.intents.deliverErr = ErrSellUnavailable
			return func(d time.Duration) *models.PayPayment { return r.tickAt(t, d) }, in.ID
		}},
	} {
		t.Run(name, func(t *testing.T) {
			tick, _ := tc.setup(t)
			logs := captureLog(t)
			for _, d := range []time.Duration{time.Hour, time.Hour + 30*time.Second, 2 * time.Hour} {
				if got := tick(d); got.Status != models.PayStatusProcessing {
					t.Fatalf("at %s: %+v", d, got)
				}
			}
			if n := strings.Count(logs.String(), "ALERT pay_"+tc.alert+"_total=1"); n != 1 {
				t.Fatalf("%d %s alerts, want 1: %q", n, tc.alert, logs.String())
			}
			got := tick(payStuckWaitMax + time.Hour)
			if got.Status != models.PayStatusFailed || got.PayerUserID == nil {
				t.Fatalf("after the bound: %+v", got)
			}
			if !strings.Contains(logs.String(), "ALERT pay_"+tc.alert+"_timeout_total=1") {
				t.Fatalf("no timeout ALERT in %q", logs.String())
			}
		})
	}
}

// (f) The proof reads ERC-20 Transfer logs: a native coin cannot be a delivery.
func TestSellIntent_CreateDeliveryRefusesANativeCoin(t *testing.T) {
	for _, native := range []string{evmNativeAddress, evmNativeAliasAddress, strings.ToUpper(evmNativeAddress)} {
		r, _, _ := newDeliveryRig(t)
		in := deliveryInput(r)
		in.DestTokenAddress = native
		if _, _, err := r.svc.Create(context.Background(), r.user, in); !errors.Is(err, ErrSellIntentState) {
			t.Fatalf("%s: err = %v", native, err)
		}
		if r.quoter.calls != 0 {
			t.Fatal("quoted a native-coin delivery")
		}
	}
}

// (g) A quoter whose LiFi client cannot price a token says so.
func TestRampSellQuoter_APriceItsClientCannotGiveIsUnavailableNotSilent(t *testing.T) {
	q, _ := newSellQuoterWithFakes(t, sellRoute(t, 9.98))
	_, _, err := q.TokenPriceUSD(context.Background(), 56, "0xabc")
	if !errors.Is(err, ErrSellUnavailable) || !strings.Contains(err.Error(), "cannot price") {
		t.Fatalf("err = %v", err)
	}
}

// (i) ConfirmDelivery's unique-index backstop: another sale takes the destination
// transaction after the pre-check but before the write.
func TestSellIntent_ConfirmDeliveryRefusesWhenTheUniqueIndexCatchesAReusedTransaction(t *testing.T) {
	ctx := context.Background()
	r, _, fees := newDeliveryRig(t)
	in := r.createDelivery(t)
	r.status.st = r.deliveredStatus(in)
	other := "0x" + strings.Repeat("ee", 32)
	fees.onEnqueue = func() { // runs as the fee is booked: after the pre-check, before the update
		row := models.RampSellIntent{UserID: r.user.ID, WalletAddress: r.user.WalletAddress, SourceAddress: r.user.WalletAddress,
			SourceChainID: 56, SourceTokenAddress: evmNativeAddress, SourceAmount: "1", FiatCode: "NGN",
			Status: models.SellIntentDelivered, DestTxHash: &[]string{deliveryDestTx}[0], SwapTxHash: &other}
		if err := r.svc.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	logs := captureLog(t)
	if _, err := r.svc.ConfirmDelivery(ctx, in.ID); !errors.Is(err, ErrSwapMismatch) {
		t.Fatalf("err = %v, want ErrSwapMismatch from the unique index", err)
	}
	if !strings.Contains(logs.String(), "ALERT ramp_sell_delivery_tx_reused_total=1 intent="+in.ID.String()) {
		t.Fatalf("no ALERT in %q", logs.String())
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentSwapped || got.DestTxHash != nil {
		t.Fatalf("intent = %+v: delivered on a transaction another sale had proved", got)
	}
}

// ---- B4 on Postgres (skipped without DATABASE_URL) ----

func TestPayReconcile_AnAttemptThatChangesUnderAReconcilerIsNotReleasedOnPostgres(t *testing.T) {
	r := newPayCheckoutPostgresRig(t)
	r.lockAlice(t)
	in := r.orphanIntent(t, models.SellIntentSwapped, "0x"+strings.Repeat("ab", 32), nil)
	if err := r.pay.LinkIntent(context.Background(), r.p.ID, in.ID); err != nil {
		t.Fatal(err)
	}
	r.intents.status = &LiFiTransferStatus{Status: LiFiStatusFailed}
	newIntent := uuid.New()
	r.intents.statusHook = func() {
		r.pay.db.Model(&models.PayPayment{}).Where("id = ?", r.p.ID).Update("sell_intent_id", newIntent)
	}
	got := r.tickAt(t, time.Minute)
	if got.Status != models.PayStatusProcessing || got.SellIntentID == nil || *got.SellIntentID != newIntent {
		t.Fatalf("payment = %+v", got)
	}
}

// One instance reconciles at a time: a tick that finds the advisory lock held
// does nothing, and the same tick runs once it is free.
func TestPayReconcileTick_OnlyOneInstanceRunsAtATimeOnPostgres(t *testing.T) {
	ctx := context.Background()
	r := newPayCheckoutPostgresRig(t)
	r.lockAlice(t) // a lock with no attempt: released by the tick, after ten minutes
	other := NewPayPaymentService(r.pay.db, r.pay.merchants, 100, "https://tsaconnectworld.com/pay", func() time.Time { return payTestNow.Add(11 * time.Minute) })
	other.SetIntents(r.intents)
	other.SetDepositReader(r.deposit)

	sqlDB, err := r.pay.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", payReconcileLockKey); err != nil {
		t.Fatal(err)
	}
	if err := other.ReconcileTick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.get(t); got.Status != models.PayStatusProcessing {
		t.Fatalf("a second instance reconciled while the first held the lock: %+v", got)
	}
	unlockAdvisory(t, conn)
	if err := other.ReconcileTick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.get(t); got.Status != models.PayStatusPending {
		t.Fatalf("the tick did not run once the lock was free: %+v", got)
	}
	// And it releases its own lock: nothing is left holding it.
	err = r.pay.db.Connection(func(c *gorm.DB) error {
		var held bool
		if err := c.Raw("SELECT pg_try_advisory_lock(?)", payReconcileLockKey).Scan(&held).Error; err != nil {
			return err
		}
		if !held {
			t.Error("ReconcileTick left the advisory lock held")
			return nil
		}
		return c.Exec("SELECT pg_advisory_unlock(?)", payReconcileLockKey).Error
	})
	if err != nil {
		t.Fatal(err)
	}
}

func unlockAdvisory(t *testing.T, conn *sql.Conn) {
	t.Helper()
	if _, err := conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", payReconcileLockKey); err != nil {
		t.Fatal(err)
	}
}

// ---- a payment failed over a stale ordering claim can never have its payout opened later ----

// The reconciler ends the intent FIRST, atomically and only while it is still the
// dead claim it read, so an OpenOrder retry finds a failed intent.
func TestPayReconcile_AStaleOrderingClaimEndsTheIntentBeforeFailingThePayment(t *testing.T) {
	r := newPayCheckoutRig(t)
	in := r.staleOrdering(t)
	got := r.tickAt(t, sellIntentClaimStale+time.Minute)
	if got.Status != models.PayStatusFailed {
		t.Fatalf("payment = %+v", got)
	}
	var stored models.RampSellIntent
	r.pay.db.First(&stored, "id = ?", in.ID)
	if stored.Status != models.SellIntentFailed || stored.AccountNumber != "" || stored.BankCode != "" || stored.PaycrestOrderID != nil {
		t.Fatalf("intent = %+v: ended, its bank details scrubbed like any failed sale", stored)
	}
}

// A retry that took over the dead claim between the reconciler's read and its
// write holds a live claim and is opening the order: the end must not match it.
func TestPayReconcile_AClaimTakenOverBeforeTheEndIsNeverEnded(t *testing.T) {
	ctx := context.Background()
	r := newPayCheckoutRig(t)
	in := r.staleOrdering(t)
	var read models.RampSellIntent
	r.pay.db.First(&read, "id = ?", in.ID) // what the reconciler read: a dead claim
	takeover := payTestNow.Add(sellIntentClaimStale + 30*time.Second)
	r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Update("updated_at", takeover) // the retry's claim
	r.pay.now = func() time.Time { return payTestNow.Add(sellIntentClaimStale + time.Minute) }
	if err := r.pay.reconcileStaleOrdering(ctx, r.get(t), &read); err != nil {
		t.Fatal(err)
	}
	var stored models.RampSellIntent
	r.pay.db.First(&stored, "id = ?", in.ID)
	if got := r.get(t); got.Status != models.PayStatusProcessing || stored.Status != models.SellIntentOrdering {
		t.Fatalf("payment %+v intent %s: a live claim was ended under its holder", got, stored.Status)
	}
}

// The claim finished between the read and the write: follow what it did.
func TestPayReconcile_AClaimThatCompletedBeforeTheEndIsFollowedNotFailed(t *testing.T) {
	ctx := context.Background()
	r := newPayCheckoutRig(t)
	in := r.staleOrdering(t)
	var read models.RampSellIntent
	r.pay.db.First(&read, "id = ?", in.ID)
	orderID := uuid.New()
	r.pay.db.Create(&models.PaycrestOrderRecord{ID: orderID, UserID: r.alice.ID, Direction: models.PaycrestDirectionSell,
		Status: models.PaycrestStatusAwaitingDeposit, Reference: "PCS-done", DepositAddress: "0xdep"}) // not marked: found only via the intent
	r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"status": models.SellIntentOrdered, "paycrest_order_id": orderID})
	r.pay.now = func() time.Time { return payTestNow.Add(sellIntentClaimStale + time.Minute) }
	if err := r.pay.reconcileStaleOrdering(ctx, r.get(t), &read); err != nil {
		t.Fatal(err)
	}
	if got := r.get(t); got.Status != models.PayStatusProcessing || got.PaycrestOrderID == nil || *got.PaycrestOrderID != orderID {
		t.Fatalf("payment = %+v", got)
	}
}

// The other half: once the intent is failed, no retry can claim it or open an order.
func TestSellIntent_AnIntentEndedOverAStaleClaimCanNeverHaveItsPayoutOpened(t *testing.T) {
	ctx := context.Background()
	r := payRig(t)
	r.base.amount = big.NewInt(34_000000)
	r.svc.SetUSDCBalanceReader(balanceOf(500_000000))
	in, _, err := r.svc.Create(ctx, r.user, payIntentInput(uuid.New(), r.user))
	if err != nil {
		t.Fatal(err)
	}
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	stale := sponsorTestNow.Add(-time.Hour)
	r.svc.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"status": models.SellIntentOrdering, "updated_at": stale})
	var read models.RampSellIntent
	r.svc.db.First(&read, "id = ?", in.ID) // the retry's read: a stale ordering claim it may take over
	// The reconciler's end.
	if res := r.svc.db.Model(&models.RampSellIntent{}).Where("id = ? AND status = ? AND paycrest_order_id IS NULL", in.ID, models.SellIntentOrdering).
		Updates(models.WithBankDetailsCleared(map[string]any{"status": models.SellIntentFailed})); res.RowsAffected != 1 {
		t.Fatalf("setup: %v", res.Error)
	}
	if claimed, err := r.svc.claim(ctx, &read); err != nil || claimed {
		t.Fatalf("claim on a stale read of an ended intent: claimed=%v err=%v", claimed, err)
	}
	if _, err := r.svc.OpenOrder(ctx, r.user, in.ID, false); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("err = %v", err)
	}
	if r.client.offrampCalls != 0 {
		t.Fatalf("%d offramps opened for a failed payment's intent", r.client.offrampCalls)
	}
}

// ---- scoped re-review ----

// I1: a stale claim on a PAY intent is never taken over by a retry: the
// reconciler owns it, because an order that pays the merchant may exist.
func TestSellIntent_AStaleClaimOnAPayIntentIsNeverTakenOver(t *testing.T) {
	ctx := context.Background()
	r := payRig(t)
	r.base.amount = big.NewInt(34_000000)
	r.svc.SetUSDCBalanceReader(balanceOf(500_000000))
	pid := uuid.New()
	in, _, err := r.svc.Create(ctx, r.user, payIntentInput(pid, r.user))
	if err != nil {
		t.Fatal(err)
	}
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	r.svc.db.Create(&models.PaycrestOrderRecord{UserID: r.user.ID, Direction: models.PaycrestDirectionSell,
		Status: models.PaycrestStatusAwaitingDeposit, Reference: "PCS-open", DepositAddress: "0xdep", PayPaymentID: &pid})
	r.svc.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{
		"status": models.SellIntentOrdering, "updated_at": sponsorTestNow.Add(-sellIntentClaimStale - time.Second)})
	var read models.RampSellIntent
	r.svc.db.First(&read, "id = ?", in.ID)

	if _, err := r.svc.OpenOrder(ctx, r.user, in.ID, false); !errors.Is(err, ErrSellIntentOrdering) {
		t.Fatalf("err = %v, want ErrSellIntentOrdering: a retry must leave a pay claim to the reconciler", err)
	}
	if claimed, err := r.svc.claim(ctx, &read); err != nil || claimed {
		t.Fatalf("claim took over a pay intent's stale claim: %v %v", claimed, err)
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentOrdering || r.client.offrampCalls != 0 {
		t.Fatalf("intent %s offramps %d: dropped back to swapped, or an order opened", got.Status, r.client.offrampCalls)
	}
}

// A retry that finds this payment's own order open must not hand the intent back
// to `swapped` (where a release could follow while that order is fundable); an
// unrelated open order of hers still gives the claim back as before.
func TestSellIntent_APayIntentWhoseOwnOrderIsOpenKeepsItsClaimOnRetry(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		marked bool
		want   string
	}{"this payment's own order": {true, models.SellIntentOrdering}, "an unrelated open order": {false, models.SellIntentSwapped}} {
		t.Run(name, func(t *testing.T) {
			r := payRig(t)
			r.base.amount = big.NewInt(34_000000)
			r.svc.SetUSDCBalanceReader(balanceOf(500_000000))
			pid := uuid.New()
			in, _, err := r.svc.Create(ctx, r.user, payIntentInput(pid, r.user))
			if err != nil {
				t.Fatal(err)
			}
			r.mustRecord(t, in.ID, testSwapHash)
			r.status.st = r.landed(in)
			o := &models.PaycrestOrderRecord{UserID: r.user.ID, Direction: models.PaycrestDirectionSell,
				Status: models.PaycrestStatusAwaitingDeposit, Reference: "PCS-open", DepositAddress: "0xdep"}
			if tc.marked {
				o.PayPaymentID = &pid
			}
			r.svc.db.Create(o)
			if _, err := r.svc.OpenOrder(ctx, r.user, in.ID, false); !errors.Is(err, ErrPaycrestOrderOpen) {
				t.Fatalf("err = %v", err)
			}
			if got := r.reload(t, in.ID); got.Status != tc.want {
				t.Fatalf("intent %s, want %s", got.Status, tc.want)
			}
		})
	}
}

// The adoption write is guarded like the end: a retry that took the claim over
// meanwhile holds it, and adopting would race its order.
func TestPayReconcile_AdoptionIsGuardedOnTheClaimItRead(t *testing.T) {
	ctx := context.Background()
	r := newPayCheckoutRig(t)
	in := r.staleOrdering(t)
	r.pay.db.Create(&models.PaycrestOrderRecord{UserID: r.alice.ID, Direction: models.PaycrestDirectionSell, Status: models.PaycrestStatusAwaitingDeposit,
		Reference: "PCS-adopt2", DepositAddress: "0xdep", PayPaymentID: &r.p.ID})
	var read models.RampSellIntent
	r.pay.db.First(&read, "id = ?", in.ID)
	r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Update("updated_at", payTestNow.Add(sellIntentClaimStale+30*time.Second))
	r.pay.now = func() time.Time { return payTestNow.Add(sellIntentClaimStale + time.Minute) }
	if err := r.pay.reconcileStaleOrdering(ctx, r.get(t), &read); err != nil {
		t.Fatal(err)
	}
	var stored models.RampSellIntent
	r.pay.db.First(&stored, "id = ?", in.ID)
	if got := r.get(t); got.PaycrestOrderID != nil || stored.Status != models.SellIntentOrdering {
		t.Fatalf("payment %+v intent %s: adopted under a live claim", got, stored.Status)
	}
}

// S4: ending the intent and failing the payment are ONE transaction, so a
// transient failure between them cannot leave an ended intent behind a payment
// that then reads as an ordinary swapped-and-done one and is released.
func TestPayReconcile_EndingAStaleClaimAndFailingItsPaymentAreOneTransaction(t *testing.T) {
	r := newPayCheckoutRig(t)
	in := r.staleOrdering(t)
	var failing atomic.Bool
	failing.Store(true)
	if err := r.pay.db.Callback().Update().Before("gorm:update").Register("test:fail_pay_fail", func(tx *gorm.DB) {
		m, ok := tx.Statement.Dest.(map[string]any)
		if ok && failing.Load() && tx.Statement.Table == "pay_payments" && m["status"] == models.PayStatusFailed {
			tx.AddError(errors.New("payment write dropped"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	r.pay.now = func() time.Time { return payTestNow.Add(sellIntentClaimStale + time.Minute) }
	if err := r.pay.ReconcileTick(context.Background()); err == nil {
		t.Fatal("the dropped write must reach the failure-streak alert")
	}
	var stored models.RampSellIntent
	r.pay.db.First(&stored, "id = ?", in.ID)
	if stored.Status != models.SellIntentOrdering || r.get(t).Status != models.PayStatusProcessing {
		t.Fatalf("intent %s payment %s: the end survived a payment write that did not", stored.Status, r.get(t).Status)
	}
	failing.Store(false)
	r.pay.now = func() time.Time { return payTestNow.Add(sellIntentClaimStale + 2*time.Minute) }
	r.pay.ReconcileTick(context.Background())
	r.pay.db.First(&stored, "id = ?", in.ID)
	if stored.Status != models.SellIntentFailed || r.get(t).Status != models.PayStatusFailed {
		t.Fatalf("intent %s payment %s", stored.Status, r.get(t).Status)
	}
}

// S5: a payment released and locked again between a reconciler's read and its
// write is a different lock; the stale decision must not touch it.
func TestPayTransitions_AreGuardedOnTheLockTheCallerRead(t *testing.T) {
	ctx := context.Background()
	r := newPayCheckoutRig(t)
	r.lockAlice(t)
	stale := r.get(t) // no attempt, locked at payTestNow
	if err := r.pay.Release(ctx, r.p.ID, "x"); err != nil {
		t.Fatal(err)
	}
	r.pay.now = func() time.Time { return payTestNow.Add(5 * time.Minute) }
	bob := payUser(t, r.pay, true)
	if _, fresh, err := r.pay.Lock(ctx, bob, r.p.ID, true); err != nil || !fresh {
		t.Fatalf("relock: %v %v", err, fresh)
	}
	for name, err := range map[string]error{
		"release": r.pay.releaseFrom(ctx, stale, "x"),
		"fail":    r.pay.failFrom(ctx, stale, "x"),
		"succeed": r.pay.succeedFrom(ctx, stale, "x"),
	} {
		if !errors.Is(err, ErrPayTransition) {
			t.Fatalf("%s: err = %v: a stale decision moved a fresh lock", name, err)
		}
	}
	if got := r.get(t); got.Status != models.PayStatusProcessing || got.PayerUserID == nil || *got.PayerUserID != bob.ID {
		t.Fatalf("payment = %+v", got)
	}
}

// I3: the deposit read asks about the order's own chain, token and address.
func TestPayReconcile_TheDepositReadAsksAboutTheOrdersOwnChainTokenAndAddress(t *testing.T) {
	r := newPayCheckoutRig(t)
	o := r.startedOrder(t, models.PaycrestStatusExpired, func(o *models.PaycrestOrderRecord) {
		o.ChainKey, o.Token, o.DepositAddress = "bsc", "USDT", "0xOrdersOwnDepositAddress"
	})
	r.deposit.want = &fakeDepositArgs{"bsc", "USDT", o.DepositAddress}
	if got := r.tickAt(t, time.Hour); got.Status != models.PayStatusPending {
		t.Fatalf("payment = %+v", got)
	}
	if len(r.deposit.got) != 1 || r.deposit.got[0] != *r.deposit.want {
		t.Fatalf("read %+v", r.deposit.got)
	}
	// Mangled arguments are an error, which is unknown, never "proven empty".
	r2 := newPayCheckoutRig(t)
	r2.startedOrder(t, models.PaycrestStatusExpired, nil)
	r2.deposit.want = &fakeDepositArgs{"somewhere-else", "USDC", "0xdeposit"}
	if got := r2.tickAt(t, time.Hour); got.Status != models.PayStatusProcessing {
		t.Fatalf("a read of the wrong place released the payment: %+v", got)
	}
}

// I2: a skipped reconcile tick is never silent.
func TestPayReconcileTick_ASkippedTickIsLoggedAndTheStreakAlertsEveryNAndResets(t *testing.T) {
	s := &PayPaymentService{}
	logs := captureLog(t)
	for i := 1; i <= 2*payReconcileSkipAlertAfter+3; i++ {
		s.noteTick(false)
	}
	out := logs.String()
	if n := strings.Count(out, "reconcile tick skipped"); n != 2*payReconcileSkipAlertAfter+3 {
		t.Fatalf("%d skip lines: every skip is logged", n)
	}
	for _, want := range []int{payReconcileSkipAlertAfter, 2 * payReconcileSkipAlertAfter} {
		if !strings.Contains(out, fmt.Sprintf("ALERT pay_reconcile_skipped_total=1 consecutive=%d", want)) {
			t.Fatalf("no ALERT at %d skips in %q", want, out)
		}
	}
	if n := strings.Count(out, "ALERT pay_reconcile_skipped_total"); n != 2 {
		t.Fatalf("%d alerts, want one per %d skips", n, payReconcileSkipAlertAfter)
	}
	// A tick that ran resets the streak: the next alert is N skips away again.
	s.noteTick(true)
	logs.Reset()
	for i := 1; i < payReconcileSkipAlertAfter; i++ {
		s.noteTick(false)
	}
	if strings.Contains(logs.String(), "ALERT") {
		t.Fatalf("alerted before %d consecutive skips after a recovery: %q", payReconcileSkipAlertAfter, logs.String())
	}
}

func TestPayReconcileTick_APersistentlyLockedTickAlertsAndARecoveredOneResetsOnPostgres(t *testing.T) {
	ctx := context.Background()
	r := newPayCheckoutPostgresRig(t)
	sqlDB, _ := r.pay.db.DB()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", payReconcileLockKey); err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	for i := 0; i < payReconcileSkipAlertAfter; i++ {
		if err := r.pay.ReconcileTick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(logs.String(), fmt.Sprintf("ALERT pay_reconcile_skipped_total=1 consecutive=%d", payReconcileSkipAlertAfter)) {
		t.Fatalf("no ALERT after %d skipped ticks in %q", payReconcileSkipAlertAfter, logs.String())
	}
	unlockAdvisory(t, conn)
	if err := r.pay.ReconcileTick(ctx); err != nil {
		t.Fatal(err)
	}
	if r.pay.skipped.Load() != 0 {
		t.Fatalf("streak = %d after a tick that ran", r.pay.skipped.Load())
	}
}

// The lock dies with its transaction: a tick whose context is already cancelled
// (its transaction never commits) leaves nothing held, so no instance is silenced.
func TestPayReconcileTick_TheLockIsReleasedWhenTheTickEndsHoweverItEndsOnPostgres(t *testing.T) {
	r := newPayCheckoutPostgresRig(t)
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.pay.ReconcileTick(cctx) // errors: nothing to assert but the lock afterwards
	for i := 0; i < 3; i++ {
		if err := r.pay.ReconcileTick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if r.pay.skipped.Load() != 0 {
		t.Fatalf("%d ticks skipped: a lock outlived its tick", r.pay.skipped.Load())
	}
}
