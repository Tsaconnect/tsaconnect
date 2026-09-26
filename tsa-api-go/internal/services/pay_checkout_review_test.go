package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// Tests for the Task 13/15 review: an attempt that may exist is never released,
// and the start path's bookkeeping survives a dead request.

// failPaymentLinks makes every write that links an attempt to a payment fail,
// whatever the request's context: a database that drops exactly that update.
func failPaymentLinks(t *testing.T, db *gorm.DB) {
	t.Helper()
	err := db.Callback().Update().Before("gorm:update").Register("test:fail_link", func(tx *gorm.DB) {
		m, ok := tx.Statement.Dest.(map[string]any)
		if !ok || tx.Statement.Table != "pay_payments" {
			return
		}
		for _, col := range []string{"paycrest_order_id", "sell_intent_id"} {
			if v, has := m[col]; has && v != nil {
				tx.AddError(errors.New("link write failed"))
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

// killOnCreate cancels the returned context from inside the provider call, so
// the order or intent exists by the time the request is dead.
func killOnCreate(r *payCheckoutRig) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	r.orders.onCreate, r.intents.onCreate = cancel, cancel
	return ctx
}

func TestPayDirect_AFailedLinkFailsThePaymentWithAnAlertAndNeverReleasesIt(t *testing.T) {
	r := newPayCheckoutRig(t)
	failPaymentLinks(t, r.pay.db)
	logs := captureLog(t)
	if _, err := r.direct(t); err == nil {
		t.Fatal("a payment that could not link its order must not look started")
	}
	got := r.get(t)
	if got.Status != models.PayStatusFailed || got.PayerUserID == nil {
		t.Fatalf("payment = %+v: an order exists, so it is failed for support, never handed back", got)
	}
	if !strings.Contains(logs.String(), "ALERT pay_link_failed_total=1 payment="+r.p.ID.String()) {
		t.Fatalf("no ALERT in %q", logs.String())
	}
}

func TestPayConverted_AFailedLinkEndsTheIntentFailsThePaymentAndAlerts(t *testing.T) {
	r := newPayCheckoutRig(t)
	failPaymentLinks(t, r.pay.db)
	logs := captureLog(t)
	if _, _, err := r.converted(t); err == nil {
		t.Fatal("a payment that could not link its intent must not look started")
	}
	if got := r.get(t); got.Status != models.PayStatusFailed || r.intents.abandoned != 1 {
		t.Fatalf("payment = %+v abandoned %d", got, r.intents.abandoned)
	}
	if !strings.Contains(logs.String(), "ALERT pay_link_failed_total=1") {
		t.Fatalf("no ALERT in %q", logs.String())
	}
}

// The provider call succeeded and THEN the request died: the bookkeeping that
// records what was opened must not depend on that request.
func TestPayDirect_ARequestThatDiesAfterTheOrderExistsStillLinksIt(t *testing.T) {
	r := newPayCheckoutRig(t)
	ctx := killOnCreate(r)
	order, err := r.svc.PayDirect(ctx, r.alice, r.p.ID, PayDirectInput{ChainKey: "base", Token: "USDC"})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.get(t); got.Status != models.PayStatusProcessing || got.PaycrestOrderID == nil || *got.PaycrestOrderID != order.ID {
		t.Fatalf("payment = %+v: an order she can fund is not linked to its payment", got)
	}
}

func TestPayConverted_ARequestThatDiesAfterTheIntentExistsStillLinksIt(t *testing.T) {
	r := newPayCheckoutRig(t)
	ctx := killOnCreate(r)
	intent, _, err := r.svc.PayConverted(ctx, r.alice, r.p.ID, PayConvertedInput{SourceChainID: 56, SourceToken: evmNativeAddress, SourceAddress: r.alice.WalletAddress})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.get(t); got.Status != models.PayStatusProcessing || got.SellIntentID == nil || *got.SellIntentID != intent.ID {
		t.Fatalf("payment = %+v: an intent she can sign is not linked to its payment", got)
	}
}

func TestPayCrypto_ARequestThatDiesAfterTheIntentExistsStillLinksIt(t *testing.T) {
	r := newCryptoRig(t)
	ctx := killOnCreate(r.payCheckoutRig)
	intent, _, err := r.svc.PayCrypto(ctx, r.alice, r.p.ID, payCryptoSource(r.payCheckoutRig))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.get(t); got.Status != models.PayStatusProcessing || got.SellIntentID == nil || *got.SellIntentID != intent.ID {
		t.Fatalf("payment = %+v", got)
	}
}

// Both the link and the request fail: the failure is still recorded, or the
// payment would sit processing and unlinked until the no-attempt rule.
func TestPayDirect_AFailedLinkOnADeadRequestStillFailsThePayment(t *testing.T) {
	r := newPayCheckoutRig(t)
	failPaymentLinks(t, r.pay.db)
	ctx := killOnCreate(r)
	if _, err := r.svc.PayDirect(ctx, r.alice, r.p.ID, PayDirectInput{ChainKey: "base", Token: "USDC"}); err == nil {
		t.Fatal("expected an error")
	}
	if got := r.get(t); got.Status != models.PayStatusFailed {
		t.Fatalf("payment %s: Fail ran on the dead request's context and was lost", got.Status)
	}
}

func TestPayConverted_AFailedLinkOnADeadRequestStillEndsTheIntentAndFailsThePayment(t *testing.T) {
	r := newPayCheckoutRig(t)
	failPaymentLinks(t, r.pay.db)
	ctx := killOnCreate(r)
	if _, _, err := r.svc.PayConverted(ctx, r.alice, r.p.ID, PayConvertedInput{SourceChainID: 56, SourceToken: evmNativeAddress, SourceAddress: r.alice.WalletAddress}); err == nil {
		t.Fatal("expected an error")
	}
	if got := r.get(t); got.Status != models.PayStatusFailed {
		t.Fatalf("payment %s", got.Status)
	}
}

// ---- the no-attempt rule must look for an attempt that may exist ----

// lockAlice takes the payment for alice at payTestNow and nothing more: the
// state a start leaves when its bookkeeping was lost.
func (r *payCheckoutRig) lockAlice(t *testing.T) {
	t.Helper()
	if _, fresh, err := r.pay.Lock(context.Background(), r.alice, r.p.ID, true); err != nil || !fresh {
		t.Fatalf("lock: %v fresh=%v", err, fresh)
	}
}

func (r *payCheckoutRig) tickAt(t *testing.T, d time.Duration) *models.PayPayment {
	t.Helper()
	r.pay.now = func() time.Time { return payTestNow.Add(d) }
	if err := r.pay.ReconcileTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r.get(t)
}

// orphanOrder is a payout order her start opened for this payment whose link
// was lost: her sell to the merchant's snapshotted bank for the merchant's net,
// marked as this payment's the way every pay start marks its order.
func (r *payCheckoutRig) orphanOrder(t *testing.T, mutate func(*models.PaycrestOrderRecord)) *models.PaycrestOrderRecord {
	t.Helper()
	o := &models.PaycrestOrderRecord{UserID: r.alice.ID, Direction: models.PaycrestDirectionSell, Status: models.PaycrestStatusAwaitingDeposit,
		Reference: "PCS-" + uuid.NewString()[:8], FiatCode: "NGN", FiatAmount: "50000.00", DepositAddress: "0xdeposit",
		PayoutBankCode: "058", PayoutAccount: "0123456789", CreatedAt: payTestNow.Add(time.Minute), PayPaymentID: &r.p.ID}
	if mutate != nil {
		mutate(o)
	}
	if err := r.pay.db.Create(o).Error; err != nil {
		t.Fatal(err)
	}
	return o
}

func TestPayReconcile_AnUnlinkedFundableOrderIsAdoptedNeverReleased(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.lockAlice(t)
	o := r.orphanOrder(t, nil)
	logs := captureLog(t)
	got := r.tickAt(t, 11*time.Minute)
	if got.Status != models.PayStatusProcessing || got.PaycrestOrderID == nil || *got.PaycrestOrderID != o.ID {
		t.Fatalf("payment = %+v: another payer could now pay while her order is fundable", got)
	}
	if !strings.Contains(logs.String(), "ALERT pay_unlinked_attempt_total=1 payment="+r.p.ID.String()) {
		t.Fatalf("no ALERT in %q", logs.String())
	}
	// From here the ordinary order rules own it: it settles, the payment does.
	r.pay.db.Model(o).Update("status", models.PaycrestStatusSettled)
	if got := r.tickAt(t, 12*time.Minute); got.Status != models.PayStatusSucceeded || got.SettlementRef != o.ID.String() {
		t.Fatalf("payment = %+v", got)
	}
}

// A settled order is money that reached the merchant: also never released.
func TestPayReconcile_AnUnlinkedSettledOrderSucceedsThePayment(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.lockAlice(t)
	o := r.orphanOrder(t, func(o *models.PaycrestOrderRecord) { o.Status = models.PaycrestStatusSettled })
	r.tickAt(t, 11*time.Minute)
	if got := r.tickAt(t, 12*time.Minute); got.Status != models.PayStatusSucceeded || got.SettlementRef != o.ID.String() {
		t.Fatalf("payment = %+v", got)
	}
}

// Optional finding from the whole-branch review: the heuristic order match
// (same bank+account+amount) could in principle misattribute between two of
// the SAME payer's own concurrent payments if they coincide on every one of
// those fields — e.g. two payments to the same merchant for the same amount.
// The exact pay_payment_id match tried first is what actually prevents it:
// every order this codebase opens for a payment carries the payment's own id
// from the moment it is created. Prove each payment adopts its OWN order,
// never the other's, even with identical heuristic fields.
func TestPayReconcile_TwoOfHerOwnPaymentsThatCoincideOnEveryHeuristicFieldEachAdoptTheirOwnOrder(t *testing.T) {
	r := newPayCheckoutRig(t)
	in2 := payIn()
	in2.Reference = "PNR-XYZ789" // same merchant, same amount, same settlement bank+account — a DIFFERENT payment
	p2, _, err := r.pay.Create(context.Background(), r.m, models.PayModeLive, in2)
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := r.pay.Lock(context.Background(), r.alice, r.p.ID, true); err != nil || !fresh {
		t.Fatalf("lock p1: %v fresh=%v", err, fresh)
	}
	if _, fresh, err := r.pay.Lock(context.Background(), r.alice, p2.ID, true); err != nil || !fresh {
		t.Fatalf("lock p2: %v fresh=%v", err, fresh)
	}

	o1 := r.orphanOrder(t, func(o *models.PaycrestOrderRecord) { o.PayPaymentID = &r.p.ID; o.Reference = "PCS-p1" })
	o2 := r.orphanOrder(t, func(o *models.PaycrestOrderRecord) { o.PayPaymentID = &p2.ID; o.Reference = "PCS-p2" })

	r.tickAt(t, 11*time.Minute)

	got1 := r.get(t)
	var got2 models.PayPayment
	if err := r.pay.db.First(&got2, "id = ?", p2.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got1.PaycrestOrderID == nil || *got1.PaycrestOrderID != o1.ID {
		t.Fatalf("payment 1 adopted %v, want its own order %s (never o2 %s)", got1.PaycrestOrderID, o1.ID, o2.ID)
	}
	if got2.PaycrestOrderID == nil || *got2.PaycrestOrderID != o2.ID {
		t.Fatalf("payment 2 adopted %v, want its own order %s (never o1 %s)", got2.PaycrestOrderID, o2.ID, o1.ID)
	}
}

func TestPayReconcile_OrdersThatCannotPayThisMerchantDoNotBlockTheRelease(t *testing.T) {
	for name, mutate := range map[string]func(*models.PaycrestOrderRecord){
		"her own sale, no payment's attempt": func(o *models.PaycrestOrderRecord) { o.PayPaymentID = nil },
		"another payment's attempt":          func(o *models.PaycrestOrderRecord) { id := uuid.New(); o.PayPaymentID = &id },
		"opened before she locked":           func(o *models.PaycrestOrderRecord) { o.CreatedAt = payTestNow.Add(-time.Hour) },
		"someone else's":                     func(o *models.PaycrestOrderRecord) { o.UserID = uuid.New() },
		"cancelled with its deposit address withdrawn": func(o *models.PaycrestOrderRecord) {
			o.Status, o.DepositAddress = models.PaycrestStatusCancelled, ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newPayCheckoutRig(t)
			r.lockAlice(t)
			r.orphanOrder(t, mutate)
			if got := r.tickAt(t, 11*time.Minute); got.Status != models.PayStatusPending || got.PayerUserID != nil {
				t.Fatalf("payment = %+v", got)
			}
		})
	}
}

// secondPayment is another payment to the same merchant for the same net —
// her second booking with the same fare — which is exactly the case an
// amount-and-bank match cannot tell apart from the first.
func (r *payCheckoutRig) secondPayment(t *testing.T, reference string) *models.PayPayment {
	t.Helper()
	in := payIn()
	in.Reference = reference
	p, _, err := r.pay.Create(context.Background(), r.m, models.PayModeLive, in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPayReconcile_AStuckPaymentNeverAdoptsAnotherPaymentsOrder(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.lockAlice(t) // payment A: locked, its start died before anything was opened
	b := r.secondPayment(t, "PNR-SECOND")
	if _, _, err := r.pay.Lock(context.Background(), r.alice, b.ID, true); err != nil {
		t.Fatal(err)
	}
	// B's order: same customer, merchant, bank and net as A, opened after A's
	// lock, and not yet linked to B (a converted start links it on the next tick).
	ob := r.orphanOrder(t, func(o *models.PaycrestOrderRecord) { o.PayPaymentID = &b.ID })

	got := r.tickAt(t, 11*time.Minute)
	if got.PaycrestOrderID != nil {
		t.Fatalf("payment A adopted payment B's order %s: %+v", ob.ID, got)
	}
	if got.Status != models.PayStatusPending {
		t.Fatalf("A with nothing behind its lock = %+v, want released", got)
	}
}

func TestPayReconcile_OnePaymentsAdoptionErrorDoesNotStopTheReleaseSweep(t *testing.T) {
	r := newPayCheckoutRig(t)
	ctx := context.Background()
	r.lockAlice(t)
	// A corrupt row: A's order is already carried by another payment, so linking
	// it to A hits the unique index on paycrest_order_id.
	o := r.orphanOrder(t, nil)
	other := r.secondPayment(t, "PNR-SECOND")
	if err := r.pay.db.Model(&models.PayPayment{}).Where("id = ?", other.ID).
		Updates(map[string]any{"status": models.PayStatusProcessing, "paycrest_order_id": o.ID, "locked_at": payTestNow}).Error; err != nil {
		t.Fatal(err)
	}
	// A third payment, someone else's lock with nothing behind it, locked after
	// A so the oldest-first sweep reaches A first.
	third := r.secondPayment(t, "PNR-THIRD")
	bob := payUser(t, r.pay, true)
	r.pay.now = func() time.Time { return payTestNow.Add(time.Second) }
	if _, _, err := r.pay.Lock(ctx, bob, third.ID, true); err != nil {
		t.Fatal(err)
	}

	logs := captureLog(t)
	r.pay.now = func() time.Time { return payTestNow.Add(11 * time.Minute) }
	_ = r.pay.ReconcileTick(ctx) // the corrupt row may report an error; the sweep must go on

	if got, _ := r.pay.Get(ctx, third.ID); got.Status != models.PayStatusPending {
		t.Fatalf("the payment behind A was never released: %+v", got)
	}
	if got := r.get(t); got.Status != models.PayStatusProcessing {
		t.Fatalf("A = %+v: a payment whose order is claimed elsewhere must never be released", got)
	}
	if !strings.Contains(logs.String(), "ALERT pay_stray_attempt_linked_elsewhere_total=1 payment="+r.p.ID.String()) {
		t.Fatalf("no ALERT in %q", logs.String())
	}
	// Bounded like every other wait: failed for support once it has run too long.
	r.pay.now = func() time.Time { return payTestNow.Add(payStuckWaitMax + time.Minute) }
	_ = r.pay.ReconcileTick(ctx)
	if got := r.get(t); got.Status != models.PayStatusFailed {
		t.Fatalf("A = %+v, want failed for support after the bound", got)
	}
}

// intentRow stores the intent a start opened for this payment whose link was
// lost, in the state and with the swap the test names.
func (r *payCheckoutRig) orphanIntent(t *testing.T, status string, hash string, mutate func(*models.RampSellIntent)) *models.RampSellIntent {
	t.Helper()
	in := &models.RampSellIntent{UserID: r.alice.ID, WalletAddress: r.alice.WalletAddress, SourceAddress: r.alice.WalletAddress,
		SourceChainID: 56, SourceTokenAddress: evmNativeAddress, SourceAmount: "1", FiatCode: "NGN", Status: status,
		BankCode: "058", AccountNumber: "0123456789", PayPaymentID: &r.p.ID, PayoutFiat: "50000.00"}
	if hash != "" {
		in.SwapTxHash = &hash
	}
	if mutate != nil {
		mutate(in)
	}
	if err := r.pay.db.Create(in).Error; err != nil {
		t.Fatal(err)
	}
	return in
}

// She signed and her swap is on its way: the intent stays open and pays the
// merchant's bank when it lands, so the payment must not be handed to anyone.
func TestPayReconcile_AnUnlinkedIntentWithASwapIsAdoptedNeverReleased(t *testing.T) {
	hash := "0x" + strings.Repeat("ab", 32)
	for name, status := range map[string]string{"swapped": models.SellIntentSwapped, "ordering": models.SellIntentOrdering} {
		t.Run(name, func(t *testing.T) {
			r := newPayCheckoutRig(t)
			r.lockAlice(t)
			in := r.orphanIntent(t, status, hash, nil)
			r.intents.status = &LiFiTransferStatus{Status: LiFiStatusPending}
			logs := captureLog(t)
			got := r.tickAt(t, 11*time.Minute)
			if got.Status != models.PayStatusProcessing || got.SellIntentID == nil || *got.SellIntentID != in.ID {
				t.Fatalf("payment = %+v", got)
			}
			if r.intents.abandoned != 0 {
				t.Fatalf("a sent swap must not be abandoned (%d)", r.intents.abandoned)
			}
			if !strings.Contains(logs.String(), "ALERT pay_unlinked_attempt_total=1") {
				t.Fatalf("no ALERT in %q", logs.String())
			}
		})
	}
}

// Nothing was signed: she keeps her tokens, the intent is ended FIRST and only
// then is the payment freed — by the ordinary rules, in the same tick.
func TestPayReconcile_AnUnlinkedIntentWithNoSwapIsEndedBeforeThePaymentIsFreed(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.lockAlice(t)
	r.orphanIntent(t, models.SellIntentSwapping, "", nil)
	got := r.tickAt(t, 11*time.Minute)
	if r.intents.abandoned != 1 || (got.Status != models.PayStatusPending && got.Status != models.PayStatusExpired) || got.SellIntentID != nil {
		t.Fatalf("abandoned %d payment %+v", r.intents.abandoned, got)
	}
}

func TestPayReconcile_AnUnlinkedCryptoDeliveryIsAdoptedNeverReleased(t *testing.T) {
	r := newCryptoRig(t)
	r.lockAlice(t)
	in := r.orphanIntent(t, models.SellIntentSwapped, "0x"+strings.Repeat("ab", 32), func(in *models.RampSellIntent) {
		in.PayoutFiat, in.DestChainID, in.DestTokenAddress, in.DestAddress, in.DestAmount = "", 56, bscUSDT, cryptoMerchant, "33000000000000000000"
	})
	r.intents.deliverErr = ErrSwapNotLanded
	r.intents.status = &LiFiTransferStatus{Status: LiFiStatusPending}
	if got := r.tickAt(t, 11*time.Minute); got.Status != models.PayStatusProcessing || got.SellIntentID == nil || *got.SellIntentID != in.ID {
		t.Fatalf("payment = %+v", got)
	}
}

// An intent of another payment, or one already failed, is no attempt of this one.
func TestPayReconcile_IntentsThatCannotPayThisPaymentDoNotBlockTheRelease(t *testing.T) {
	other := uuid.New()
	for name, mutate := range map[string]func(*models.RampSellIntent){
		"another payment's": func(in *models.RampSellIntent) { in.PayPaymentID = &other },
		"an ordinary sale":  func(in *models.RampSellIntent) { in.PayPaymentID = nil },
	} {
		t.Run(name, func(t *testing.T) {
			r := newPayCheckoutRig(t)
			r.lockAlice(t)
			r.orphanIntent(t, models.SellIntentSwapping, "", mutate)
			if got := r.tickAt(t, 11*time.Minute); got.Status != models.PayStatusPending {
				t.Fatalf("payment = %+v", got)
			}
		})
	}
	t.Run("already failed", func(t *testing.T) {
		r := newPayCheckoutRig(t)
		r.lockAlice(t)
		r.orphanIntent(t, models.SellIntentFailed, "", nil)
		if got := r.tickAt(t, 11*time.Minute); got.Status != models.PayStatusPending {
			t.Fatalf("payment = %+v", got)
		}
	})
}

// ---- I2: an attempt that may exist is failed, never released ----

func TestPayDirect_AnOrderThatMayStillExistFailsThePaymentWithAnAlertAndNeverReleasesIt(t *testing.T) {
	for name, cause := range map[string]error{
		"a short order that could not be cancelled": fmt.Errorf("%w: %w", ErrSellAttemptMayExist, ErrSellRateMoved),
		"an order that could not be recorded":       fmt.Errorf("%w: connection reset", ErrSellAttemptMayExist),
	} {
		t.Run(name, func(t *testing.T) {
			r := newPayCheckoutRig(t)
			r.orders.err = cause
			logs := captureLog(t)
			_, err := r.direct(t)
			if !errors.Is(err, ErrSellAttemptMayExist) {
				t.Fatalf("err = %v: the caller must be told an attempt may exist", err)
			}
			if got := r.get(t); got.Status != models.PayStatusFailed || got.PayerUserID == nil {
				t.Fatalf("payment = %+v: another payer could pay while her order may be funded", got)
			}
			if !strings.Contains(logs.String(), "ALERT pay_start_attempt_may_exist_total=1 payment="+r.p.ID.String()) {
				t.Fatalf("no ALERT in %q", logs.String())
			}
		})
	}
}

// The ordinary rate-moved answer means the short order WAS cancelled: free.
func TestPayDirect_ACancelledShortOrderStillReleasesThePayment(t *testing.T) {
	r := newPayCheckoutRig(t)
	r.orders.err = ErrSellRateMoved
	if _, err := r.direct(t); !errors.Is(err, ErrSellRateMoved) {
		t.Fatalf("err = %v", err)
	}
	if got := r.get(t); got.Status != models.PayStatusPending || got.PayerUserID != nil {
		t.Fatalf("payment = %+v", got)
	}
}

// ---- S1: a claim that died holding the ordering state is not silent ----

func TestPayReconcile_AnOrderingClaimPastTheStaleWindowFailsThePaymentWithOneAlert(t *testing.T) {
	r := newPayCheckoutRig(t)
	in := r.startConverted(t)
	r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{
		"status": models.SellIntentOrdering, "swap_tx_hash": "0x" + strings.Repeat("ab", 32), "updated_at": payTestNow})
	logs := captureLog(t)

	r.tickAt(t, sellIntentClaimStale-time.Second)
	if strings.Contains(logs.String(), "ordering_claim_stale") {
		t.Fatalf("alerted inside the claim window: %q", logs.String())
	}
	r.tickAt(t, sellIntentClaimStale+time.Minute)
	r.tickAt(t, sellIntentClaimStale+2*time.Minute)
	if n := strings.Count(logs.String(), "ALERT pay_ordering_claim_stale_total=1 payment="+r.p.ID.String()); n != 1 {
		t.Fatalf("%d alerts, want exactly 1: %q", n, logs.String())
	}
	// A claim that died with no order to show for it: never released, never
	// abandoned — failed for support.
	if got := r.get(t); got.Status != models.PayStatusFailed || r.intents.abandoned != 0 {
		t.Fatalf("payment %+v abandoned %d", got, r.intents.abandoned)
	}
}

// ---- S2: the abandon is guarded on the swap the sweep read ----

func TestPayReconcile_TheAbandonIsGuardedOnTheSwapItRead(t *testing.T) {
	hash := "0x" + strings.Repeat("ab", 32)
	t.Run("no swap seen", func(t *testing.T) {
		r := newPayCheckoutRig(t)
		in := r.startConverted(t)
		r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Update("status", models.SellIntentSwapped)
		r.tickAt(t, 11*time.Minute)
		if r.intents.abandoned != 1 || r.intents.abandonedHash != nil {
			t.Fatalf("abandoned %d guard %v, want one abandon guarded on 'no hash'", r.intents.abandoned, r.intents.abandonedHash)
		}
	})
	t.Run("a swap seen", func(t *testing.T) {
		r := newPayCheckoutRig(t)
		in := r.startConverted(t)
		r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"status": models.SellIntentSwapped, "swap_tx_hash": hash})
		r.intents.status = &LiFiTransferStatus{Status: LiFiStatusFailed}
		r.tickAt(t, time.Minute)
		if r.intents.abandoned != 1 || r.intents.abandonedHash == nil || *r.intents.abandonedHash != hash {
			t.Fatalf("abandoned %d guard %v, want one abandon guarded on %s", r.intents.abandoned, r.intents.abandonedHash, hash)
		}
	})
}

// Her swap was recorded after the sweep read the intent: the abandon matched
// nothing, so the payment is NOT released, and the next tick follows the swap.
func TestPayReconcile_ASwapSentDuringTheAbandonKeepsThePaymentWithTheSwap(t *testing.T) {
	r := newPayCheckoutRig(t)
	in := r.startConverted(t)
	hash := "0x" + strings.Repeat("ab", 32)
	r.intents.abandonErr = ErrSellIntentState
	r.intents.abandonHook = func() {
		r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"status": models.SellIntentSwapped, "swap_tx_hash": hash})
	}
	logs := captureLog(t)
	got := r.tickAt(t, 11*time.Minute)
	if got.Status != models.PayStatusProcessing || got.SellIntentID == nil {
		t.Fatalf("payment = %+v: released after her swap was sent", got)
	}
	if !strings.Contains(logs.String(), "ALERT pay_swap_sent_during_abandon_total=1") {
		t.Fatalf("no ALERT in %q", logs.String())
	}
	// Next tick: the swap is followed by the ordinary rules (still pending).
	r.intents.abandonErr, r.intents.abandonHook = nil, nil
	r.intents.status = &LiFiTransferStatus{Status: LiFiStatusPending}
	if got := r.tickAt(t, 12*time.Minute); got.Status != models.PayStatusProcessing {
		t.Fatalf("payment = %+v", got)
	}
}

// ---- S6: fewer LiFi calls ----

func TestPayPollDue_EveryTickForAnHourThenAboutEveryFiveMinutes(t *testing.T) {
	for _, age := range []time.Duration{0, time.Minute, 59 * time.Minute, time.Hour} {
		if !payPollDue(age) {
			t.Fatalf("age %s: a young payment is asked about on every tick", age)
		}
	}
	// Over any five minutes after the first hour, the 30 s ticks that ask are
	// the ones in a single minute: two of ten, never all of them.
	for start := time.Hour + time.Minute; start < 3*time.Hour; start += 7 * time.Minute {
		asked := 0
		for tick := start; tick < start+5*time.Minute; tick += 30 * time.Second {
			if payPollDue(tick) {
				asked++
			}
		}
		if asked != 2 {
			t.Fatalf("window from %s: %d of 10 ticks ask, want 2", start, asked)
		}
	}
}

func TestPayReconcile_AnOldSwapIsAskedAboutOnlyWhenItsPollIsDue(t *testing.T) {
	r := newPayCheckoutRig(t)
	in := r.startConverted(t)
	r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"status": models.SellIntentSwapped, "swap_tx_hash": "0x" + strings.Repeat("ab", 32)})
	r.intents.status = &LiFiTransferStatus{Status: LiFiStatusPending}
	for _, tc := range []struct {
		age   time.Duration
		calls int
	}{
		{30 * time.Minute, 1}, {59 * time.Minute, 1}, // every tick in the first hour
		{2*time.Hour + time.Minute, 0}, {2*time.Hour + 3*time.Minute, 0}, // not due
		{2*time.Hour + 5*time.Minute, 1}, // due
	} {
		before := r.intents.statusCalls
		r.tickAt(t, tc.age)
		if got := r.intents.statusCalls - before; got != tc.calls {
			t.Fatalf("age %s: %d LiFi calls, want %d", tc.age, got, tc.calls)
		}
	}
	if got := r.get(t); got.Status != models.PayStatusProcessing {
		t.Fatalf("payment = %+v", got)
	}
}

// A pending crypto swap used to cost two LiFi calls a tick: ConfirmDelivery
// read the status and found it not landed, then the reconciler read it again.
func TestPayReconcile_ACryptoSwapThatHasNotLandedCostsOneStatusRead(t *testing.T) {
	r := newCryptoRig(t)
	in := r.startCrypto(t)
	r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"status": models.SellIntentSwapped, "swap_tx_hash": "0x" + strings.Repeat("ab", 32)})
	// ConfirmDelivery is the one LiFi read: the reconciler is handed its answer.
	r.intents.deliverErr = &SwapNotLandedError{Status: &LiFiTransferStatus{Status: LiFiStatusFailed}}
	got := r.tickAt(t, time.Minute)
	if r.intents.statusCalls != 0 || r.intents.confirms != 1 {
		t.Fatalf("SwapStatus calls %d, ConfirmDelivery calls %d: the status ConfirmDelivery read was read again", r.intents.statusCalls, r.intents.confirms)
	}
	if got.Status != models.PayStatusPending || r.intents.abandoned != 1 {
		t.Fatalf("payment = %+v abandoned %d: LiFi's failure, as ConfirmDelivery saw it, must release", got, r.intents.abandoned)
	}
}

func TestPayReconcile_ASwapLiFiHasNoRecordOfIsWaitedOnAsAnUnfoundDelivery(t *testing.T) {
	r := newCryptoRig(t)
	in := r.startCrypto(t)
	r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"status": models.SellIntentSwapped, "swap_tx_hash": "0x" + strings.Repeat("ab", 32)})
	r.intents.deliverErr = &SwapNotLandedError{Status: nil}
	if got := r.tickAt(t, 10*time.Minute); got.Status != models.PayStatusProcessing || r.intents.statusCalls != 0 {
		t.Fatalf("payment = %+v calls %d", got, r.intents.statusCalls)
	}
	// A delivery's unfound swap is waited on, not released (see I5 tests below).
	if got := r.tickAt(t, 31*time.Minute); got.Status != models.PayStatusProcessing {
		t.Fatalf("payment = %+v", got)
	}
	if got := r.tickAt(t, payPendingSwapMax+5*time.Minute); got.Status != models.PayStatusFailed {
		t.Fatalf("payment = %+v", got)
	}
}

// ---- S4 / S5: honest errors ----

func TestPayCheckout_AStoreFailureAtTheLockIsInternalNotAProviderFailure(t *testing.T) {
	r := newPayCheckoutRig(t)
	if err := r.pay.db.Migrator().DropTable(&models.PayMerchant{}); err != nil {
		t.Fatal(err)
	}
	_, err := r.direct(t)
	if !errors.Is(err, ErrPayInternal) {
		t.Fatalf("err = %v, want ErrPayInternal", err)
	}
	if strings.Contains(err.Error(), "no such table") {
		t.Fatalf("the store's error reached the caller: %v", err)
	}
	// The payment's own refusals are untouched.
	r2 := newPayCheckoutRig(t)
	r2.direct(t)
	if _, err := r2.direct(t); !errors.Is(err, ErrPayInProgress) || errors.Is(err, ErrPayInternal) {
		t.Fatalf("err = %v", err)
	}
}

func TestPayCheckout_TheWrongRouteSaysHowThePaymentReallySettles(t *testing.T) {
	c := newCryptoRig(t)
	c.payFromCryptoMerchant(t, "PNR-NAIRA-ON-CRYPTO")
	c.pay.db.Model(&models.PayPayment{}).Where("id = ?", c.p.ID).Update("settlement_mode", models.PaySettlementFiat)
	_, _, err := c.svc.PayCrypto(context.Background(), c.alice, c.p.ID, payCryptoSource(c.payCheckoutRig))
	if !errors.Is(err, ErrPayWrongRoute) || !strings.Contains(err.Error(), "settles in naira") || strings.Contains(err.Error(), "settles in crypto") {
		t.Fatalf("a naira payment on the crypto route: %v", err)
	}
	c2 := newCryptoRig(t)
	_, err = c2.direct(t)
	if !errors.Is(err, ErrPayWrongRoute) || !strings.Contains(err.Error(), "settles in crypto") || strings.Contains(err.Error(), "settles in naira") {
		t.Fatalf("a crypto payment on the naira route: %v", err)
	}
	if _, err := c2.svc.Quote(context.Background(), c2.alice, c2.p.ID, PayConvertedInput{SourceChainID: 56, SourceToken: evmNativeAddress, SourceAddress: c2.alice.WalletAddress}); !errors.Is(err, ErrPayWrongRoute) || !strings.Contains(err.Error(), "settles in crypto") {
		t.Fatalf("the naira quote on a crypto payment: %v", err)
	}
}

// ---- A1: the payer resumes her order from the checkout view ----

func TestPayCheckoutView_OrderIDIsForThePayersEyesOnly(t *testing.T) {
	ctx := context.Background()
	r := newPayCheckoutRig(t)
	bob := payUser(t, r.pay, true)
	view := func(u *models.User) *PayCheckoutView {
		t.Helper()
		v, err := r.pay.CheckoutView(ctx, u, r.p.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := view(r.alice); v.OrderID != nil {
		t.Fatalf("no order yet: %v", v.OrderID)
	}
	order, err := r.direct(t)
	if err != nil {
		t.Fatal(err)
	}
	if v := view(r.alice); !v.YouArePaying || v.OrderID == nil || *v.OrderID != order.ID {
		t.Fatalf("the payer's view = %+v, want orderId %s", v, order.ID)
	}
	if v := view(bob); v.OrderID != nil {
		t.Fatalf("another customer saw the order: %v", v.OrderID)
	}
	body, _ := json.Marshal(view(bob))
	if strings.Contains(string(body), "orderId") {
		t.Fatalf("orderId leaked into another viewer's JSON: %s", body)
	}
	if body, _ := json.Marshal(view(r.alice)); !strings.Contains(string(body), `"orderId":"`+order.ID.String()+`"`) {
		t.Fatalf("payer JSON = %s", body)
	}
	// Once the payment is over there is nothing left to resume — but its payer
	// keeps the order, so she can still report it ("it never arrived"): the
	// report is filed on the order. Still hers alone.
	if err := r.pay.Succeed(ctx, r.p.ID, order.ID.String()); err != nil {
		t.Fatal(err)
	}
	if v := view(r.alice); v.YouArePaying || v.OrderID == nil || *v.OrderID != order.ID {
		t.Fatalf("a finished payment's payer: %+v, want its order and nothing to resume", v)
	}
	if v := view(bob); v.OrderID != nil || v.OrderStatus != "" {
		t.Fatalf("another customer saw a finished payment's order: %+v", v)
	}
}

// ---- Postgres (skipped without DATABASE_URL) ----

func TestPayReconcile_AnUnlinkedOrderOrIntentIsAdoptedNeverReleasedOnPostgres(t *testing.T) {
	t.Run("order", func(t *testing.T) {
		r := newPayCheckoutPostgresRig(t)
		r.lockAlice(t)
		o := r.orphanOrder(t, nil)
		r.orphanOrder(t, func(o *models.PaycrestOrderRecord) { // a withdrawn short order does not count
			o.Status, o.DepositAddress, o.CreatedAt = models.PaycrestStatusCancelled, "", payTestNow.Add(2*time.Minute)
		})
		if got := r.tickAt(t, 11*time.Minute); got.Status != models.PayStatusProcessing || got.PaycrestOrderID == nil || *got.PaycrestOrderID != o.ID {
			t.Fatalf("payment = %+v", got)
		}
	})
	t.Run("intent", func(t *testing.T) {
		r := newPayCheckoutPostgresRig(t)
		r.lockAlice(t)
		in := r.orphanIntent(t, models.SellIntentSwapped, "0x"+strings.Repeat("ab", 32), nil)
		r.intents.status = &LiFiTransferStatus{Status: LiFiStatusPending}
		if got := r.tickAt(t, 11*time.Minute); got.Status != models.PayStatusProcessing || got.SellIntentID == nil || *got.SellIntentID != in.ID {
			t.Fatalf("payment = %+v", got)
		}
	})
	t.Run("nothing behind the lock is still released", func(t *testing.T) {
		r := newPayCheckoutPostgresRig(t)
		r.lockAlice(t)
		if got := r.tickAt(t, 11*time.Minute); got.Status != models.PayStatusPending || got.PayerUserID != nil {
			t.Fatalf("payment = %+v", got)
		}
	})
}

// ---- Task 15 review ----

// sentSwap starts a crypto payment and records its intent's swap: the state a
// reconciler tick finds a delivery in.
func (c *cryptoRig) sentSwap(t *testing.T) *models.RampSellIntent {
	t.Helper()
	in := c.startCrypto(t)
	c.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"status": models.SellIntentSwapped, "swap_tx_hash": "0x" + strings.Repeat("ab", 32)})
	return in
}

// I2: LiFi is read twice a tick — once by ConfirmDelivery, once by the
// reconciler — and the swap can complete between the reads. A swap LiFi now
// calls COMPLETED has paid the merchant: it is never failed as unproven, the
// next tick's ConfirmDelivery decides it.
func TestPayReconcile_ASwapThatCompletesBetweenTwoLiFiReadsIsNotFailedAsUnproven(t *testing.T) {
	completed := &LiFiTransferStatus{Status: LiFiStatusDone, Substatus: LiFiSubstatusCompleted}
	for name, setup := range map[string]func(r *cryptoRig){
		"the reconciler's own read is the one that sees it complete": func(r *cryptoRig) {
			r.intents.deliverErr, r.intents.status = ErrSwapNotLanded, completed
		},
		"the status ConfirmDelivery carried is stale": func(r *cryptoRig) {
			r.intents.deliverErr = &SwapNotLandedError{Status: &LiFiTransferStatus{Status: LiFiStatusPending}}
			r.intents.status = completed
		},
		"the carried status is already completed": func(r *cryptoRig) {
			r.intents.deliverErr = &SwapNotLandedError{Status: completed}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newCryptoRig(t)
			r.sentSwap(t)
			setup(r)
			logs := captureLog(t)
			got := r.tickAt(t, time.Minute)
			if got.Status != models.PayStatusProcessing || r.intents.abandoned != 0 {
				t.Fatalf("payment = %+v abandoned %d: a paid delivery was failed as unproven", got, r.intents.abandoned)
			}
			if strings.Contains(logs.String(), "delivery_unproven") {
				t.Fatalf("alerted unproven: %q", logs.String())
			}
			// The next tick's ConfirmDelivery proves it.
			r.intents.deliverErr, r.intents.destTx = nil, "0x"+strings.Repeat("cd", 32)
			if got := r.tickAt(t, time.Minute+30*time.Second); got.Status != models.PayStatusSucceeded {
				t.Fatalf("payment = %+v", got)
			}
		})
	}
}

// A sale already ended is not asked again, so a completed swap there stays a
// failure for support — and every other DONE that is not COMPLETED does too.
func TestPayReconcile_OnlyACompletedSwapOnAnOpenSaleWaitsForTheNextProof(t *testing.T) {
	r := newCryptoRig(t)
	in := r.sentSwap(t)
	r.pay.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).Update("status", models.SellIntentFailed)
	r.intents.status = &LiFiTransferStatus{Status: LiFiStatusDone, Substatus: LiFiSubstatusCompleted}
	if got := r.tickAt(t, time.Minute); got.Status != models.PayStatusFailed {
		t.Fatalf("a completed swap on an ended sale: %+v", got)
	}
	r2 := newCryptoRig(t)
	r2.sentSwap(t)
	r2.intents.deliverErr = ErrSwapNotLanded
	r2.intents.status = &LiFiTransferStatus{Status: LiFiStatusDone, Substatus: "SOMETHING_ELSE"}
	if got := r2.tickAt(t, time.Minute); got.Status != models.PayStatusFailed {
		t.Fatalf("an unexplained DONE: %+v", got)
	}
}

// I3: the intent is ended with the swap the sweep read, so the customer is not
// locked out of every other sale by a dead one.
func TestPayReconcile_AMismatchEndsTheIntentGuardedOnItsSwapThenFailsThePayment(t *testing.T) {
	r := newCryptoRig(t)
	r.sentSwap(t)
	r.intents.deliverErr = fmt.Errorf("%w: credited less than owed", ErrSwapMismatch)
	logs := captureLog(t)
	got := r.tickAt(t, time.Minute)
	hash := "0x" + strings.Repeat("ab", 32)
	if got.Status != models.PayStatusFailed || r.intents.abandoned != 1 || r.intents.abandonedHash == nil || *r.intents.abandonedHash != hash {
		t.Fatalf("payment %+v abandoned %d guard %v", got, r.intents.abandoned, r.intents.abandonedHash)
	}
	if !strings.Contains(logs.String(), "ALERT pay_delivery_mismatch_total=1") {
		t.Fatalf("no ALERT in %q", logs.String())
	}
}

// If the abandon matched nothing because a swap appeared meanwhile the payment
// is not failed on a stale verdict either: the next tick decides.
func TestPayReconcile_AMismatchWhoseAbandonMatchesNothingDoesNotFailThePayment(t *testing.T) {
	r := newCryptoRig(t)
	r.sentSwap(t)
	r.intents.deliverErr = ErrSwapMismatch
	r.intents.abandonErr = ErrSellIntentState
	if got := r.tickAt(t, time.Minute); got.Status != models.PayStatusProcessing {
		t.Fatalf("payment = %+v", got)
	}
}

// I5: what a stuck delivery does. Naira intents keep their rules (pinned by
// TestPayReconcile_IntentOutcomes); a delivery is failed with an ALERT after a day.
func TestPayReconcile_ADeliverySwapLiFiCannotFindIsFailedWithAnAlertNeverReleased(t *testing.T) {
	r := newCryptoRig(t)
	r.sentSwap(t)
	r.intents.deliverErr = ErrSwapNotLanded
	r.intents.status = &LiFiTransferStatus{Status: "NOT_FOUND"}
	logs := captureLog(t)
	if got := r.tickAt(t, payPendingSwapMax-time.Minute); got.Status != models.PayStatusProcessing {
		t.Fatalf("payment = %+v", got)
	}
	got := r.tickAt(t, payPendingSwapMax+5*time.Minute)
	if got.Status != models.PayStatusFailed || got.PayerUserID == nil || r.intents.abandoned != 1 {
		t.Fatalf("payment %+v abandoned %d", got, r.intents.abandoned)
	}
	if !strings.Contains(logs.String(), "ALERT pay_delivery_swap_unknown_total=1 payment="+r.p.ID.String()) {
		t.Fatalf("no ALERT in %q", logs.String())
	}
}

// LiFi saying FAILED or REFUNDED is conclusive: the value did not go to the merchant.
func TestPayReconcile_ADeliverySwapLiFiSaysFailedIsStillReleased(t *testing.T) {
	for _, st := range []*LiFiTransferStatus{{Status: LiFiStatusFailed}, {Status: LiFiStatusDone, Substatus: LiFiSubstatusRefunded}} {
		r := newCryptoRig(t)
		r.sentSwap(t)
		r.intents.deliverErr, r.intents.status = ErrSwapNotLanded, st
		if got := r.tickAt(t, time.Minute); got.Status != models.PayStatusPending {
			t.Fatalf("%v: payment = %+v", st, got)
		}
	}
}

// ---- S2 / S3 / S4 on the pay half ----

func TestPayCrypto_RefusesAPaymentThatSnapshottedNoFee(t *testing.T) {
	r := newCryptoRig(t)
	r.pay.db.Model(&models.PayPayment{}).Where("id = ?", r.p.ID).Update("fee_bps", 0)
	if _, _, err := r.crypto(t); !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if r.intents.in.ToAmount != "" {
		t.Fatalf("an intent was asked for: %+v", r.intents.in)
	}
	if got := r.get(t); got.Status != models.PayStatusPending || got.PayerUserID != nil {
		t.Fatalf("payment = %+v", got)
	}
	if _, err := r.svc.CryptoQuote(context.Background(), r.alice, r.p.ID, payCryptoSource(r.payCheckoutRig)); !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("quote: err = %v", err)
	}
}

func TestPayCrypto_HandsTheIntentTheValueItsPriceGaveTheAmount(t *testing.T) {
	r := newCryptoRig(t)
	if _, _, err := r.crypto(t); err != nil {
		t.Fatal(err)
	}
	// toAmount 33.4907… tokens at $1.0003.
	if got := r.intents.in.ExpectedToUSD; got < 33.49 || got > 33.52 {
		t.Fatalf("ExpectedToUSD = %v", got)
	}
}

func TestPayCryptoQuote_RefusesAPriceTheRoutesOwnValueContradicts(t *testing.T) {
	for name, tc := range map[string]struct {
		toUSD float64
		ok    bool
	}{"agrees": {33.5, true}, "route says far less": {20, false}, "route says far more": {60, false}, "route says nothing": {0, false}} {
		t.Run(name, func(t *testing.T) {
			r := newCryptoRig(t)
			r.quoter.toUSD = tc.toUSD
			_, err := r.svc.CryptoQuote(context.Background(), r.alice, r.p.ID, payCryptoSource(r.payCheckoutRig))
			if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrSellUnavailable)) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
