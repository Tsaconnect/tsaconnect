package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

func webRefund(amount string) PayRefundRequest {
	return PayRefundRequest{Amount: amount, Via: models.PayRefundViaWeb}
}

// D19: the payer's CURRENT proven wallet — a key she holds now, which re-linking
// can't redirect without her signature.
func TestPayRefundRequest_GoesToThePayersProvenWallet(t *testing.T) {
	r := newRefundRig(t)
	proveWallet(t, r.db, r.payer, refundPayerWallet)
	p := r.paidDirect(t, "REQ-1", "34.1234567", "")
	ref, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID,
		PayRefundRequest{Amount: "12.50", Reason: "  Flight cancelled ", Via: models.PayRefundViaWeb})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Status != models.PayRefundRequested || ref.AmountMicroUSD != 12_500_000 || ref.CapMicroUSD != 34_123_456 ||
		ref.ToAddress != refundPayerWallet || ref.Reason != "Flight cancelled" || ref.RequestedVia != models.PayRefundViaWeb || ref.Mode != models.PayModeLive {
		t.Fatalf("refund %+v", ref)
	}
	seen := r.alerts()
	if len(seen) != 1 || seen[0].Type != events.PayRefundRequested || seen[0].UserID != r.m.UserID ||
		seen[0].Message != "Refund of 12.50 USD to Ada requested — approve it in TSA Connect" {
		t.Fatalf("notices %+v", seen)
	}
}

func TestPayRefundRequest_WithoutAProvenWalletGoesToTheFundingWallet(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidDirect(t, "REQ-2", "34", "")
	ref, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID, webRefund("1"))
	if err != nil || ref.ToAddress != refundFundingWallet {
		t.Fatalf("%+v %v", ref, err)
	}
}

func TestPayRefundRequest_RefusesADeletedPayer(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidDirect(t, "REQ-3", "34", "")
	if err := r.db.Exec(`UPDATE users SET account_status = 'deleted' WHERE id = ?`, r.payer).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID, webRefund("1")); !errors.Is(err, ErrPayRefundPayerDeleted) {
		t.Fatalf("%v", err)
	}
}

func TestPayRefundRequest_OnlyAPaidPaymentOfTheMerchantInThatMode(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	p := r.paidDirect(t, "REQ-4", "34", "")
	if _, err := r.s.Request(ctx, r.m, models.PayModeTest, p.ID, webRefund("1")); !errors.Is(err, ErrPayPaymentNotFound) {
		t.Fatalf("other mode: %v", err)
	}
	other := *r.m
	other.ID = uuid.New()
	if _, err := r.s.Request(ctx, &other, models.PayModeLive, p.ID, webRefund("1")); !errors.Is(err, ErrPayPaymentNotFound) {
		t.Fatalf("other merchant: %v", err)
	}
	r.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Update("status", models.PayStatusProcessing)
	if _, err := r.s.Request(ctx, r.m, models.PayModeLive, p.ID, webRefund("1")); !errors.Is(err, ErrPayRefundNotSucceeded) {
		t.Fatalf("processing: %v", err)
	}
}

func TestPayRefundRequest_RefusesAnAmountThatIsNotUSDMicros(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidDirect(t, "REQ-5", "34", "")
	for _, amount := range []string{"", "0", "-1", "12.3456789", "abc", "1e3"} {
		if _, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID, webRefund(amount)); !errors.Is(err, ErrPayRefundInvalid) {
			t.Errorf("%q: %v", amount, err)
		}
	}
}

func TestPayRefundRequest_OneOpenRefundAtATime(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	p := r.paidDirect(t, "REQ-6", "34", "")
	first, err := r.s.Request(ctx, r.m, models.PayModeLive, p.ID, webRefund("1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Request(ctx, r.m, models.PayModeLive, p.ID, webRefund("1")); !errors.Is(err, ErrPayRefundOpen) {
		t.Fatalf("second open: %v", err)
	}
	if _, err := r.s.Cancel(ctx, r.m.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Request(ctx, r.m, models.PayModeLive, p.ID, webRefund("1")); err != nil {
		t.Fatalf("after cancel: %v", err)
	}
}

// D17: partial refunds while confirmed + open + new stays within the cap.
func TestPayRefundRequest_PartialRefundsStayWithinTheCap(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	p := r.paidDirect(t, "REQ-7", "33", "")
	r.insertRefund(t, p, models.PayRefundConfirmed, 20_000_000)
	_, err := r.s.Request(ctx, r.m, models.PayModeLive, p.ID, webRefund("13.000001"))
	var over *PayRefundOverCapError
	if !errors.As(err, &over) || over.RemainingMicroUSD != 13_000_000 {
		t.Fatalf("over cap: %v", err)
	}
	if _, err := r.s.Request(ctx, r.m, models.PayModeLive, p.ID, webRefund("13")); err != nil {
		t.Fatalf("exactly the rest: %v", err)
	}
}

// Review focus 5: a failed refund is proven to have moved nothing; its amount
// is free again.
func TestPayRefundRequest_AFailedRefundFreesItsAmount(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidDirect(t, "REQ-8", "33", "")
	r.insertRefund(t, p, models.PayRefundFailed, 33_000_000)
	if _, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID, webRefund("33")); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestPayRefundRequest_FromTheAppDoesNotNotifyTheOwner(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidDirect(t, "REQ-9", "34", "")
	ref, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID, PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp})
	if err != nil || ref.RequestedVia != models.PayRefundViaApp {
		t.Fatalf("%+v %v", ref, err)
	}
	if seen := r.alerts(); len(seen) != 0 {
		t.Fatalf("notices %+v", seen)
	}
}

func TestPayRefundRequest_ConcurrentRequestsLeaveOneOpen(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidDirect(t, "REQ-10", "34", "")
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID, PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp}); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	var open int64
	r.db.Model(&models.PayRefund{}).Where("payment_id = ? AND status IN ?", p.ID, payRefundOpenStatuses).Count(&open)
	if won != 1 || open != 1 {
		t.Fatalf("won %d open %d", won, open)
	}
}

// D26: a test refund skips the chain — confirmed at once, told as a test.
func TestPayRefundRequest_TestModeSimulatesAConfirmedRefund(t *testing.T) {
	r := newRefundRig(t)
	p := r.paid(t, models.PayModeTest, "REQ-11", map[string]any{"customer_total_kobo": int64(1_500_000)})
	ref, err := r.s.Request(context.Background(), r.m, models.PayModeTest, p.ID, webRefund("2.50"))
	if err != nil {
		t.Fatal(err)
	}
	if ref.Status != models.PayRefundConfirmed || ref.CreditedMicroUSD != 2_500_000 || ref.ConfirmedAt == nil || ref.TxHash != nil {
		t.Fatalf("refund %+v", ref)
	}
	hooks := r.webhooks(t, models.PayEventRefunded)
	if len(hooks) != 1 || hooks[0]["livemode"] != false {
		t.Fatalf("hooks %+v", hooks)
	}
	data := hooks[0]["data"].(map[string]any)
	refund := hooks[0]["refund"].(map[string]any)
	if data["refundedAmount"] != "2.50" || refund["amount"] != "2.50" || refund["id"] != ref.ID.String() {
		t.Fatalf("body %+v", hooks[0])
	}
	received, confirmed := r.alertsOf(events.PayRefundReceived), r.alertsOf(events.PayRefundConfirmed)
	if len(received) != 1 || received[0].UserID != r.payer || received[0].Message != "Test: You received a 2.50 USD refund from Sky Air" {
		t.Fatalf("payer notice %+v", received)
	}
	if len(confirmed) != 1 || confirmed[0].UserID != r.m.UserID || confirmed[0].Message != "Test: Refund to Ada confirmed" {
		t.Fatalf("owner notice %+v", confirmed)
	}
	if len(r.alertsOf(events.PayRefundRequested)) != 0 || len(r.net.broadcasts()) != 0 {
		t.Fatal("a test refund announced a request or touched the chain")
	}
}

// Review M7: a test refund is created and confirmed in one transaction. If its
// webhook can't be queued, nothing is left behind to block the payment.
func TestPayRefundRequest_ATestRefundThatCannotQueueItsWebhookLeavesNothing(t *testing.T) {
	r := newRefundRig(t)
	p := r.paid(t, models.PayModeTest, "REQ-14", map[string]any{"customer_total_kobo": int64(1_500_000)})
	if err := r.db.Migrator().DropTable(&models.PayWebhookDelivery{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Request(context.Background(), r.m, models.PayModeTest, p.ID, webRefund("2.50")); err == nil {
		t.Fatal("the webhook insert failed, yet the refund was made")
	}
	var n int64
	r.db.Model(&models.PayRefund{}).Where("payment_id = ?", p.ID).Count(&n)
	if n != 0 || len(r.alertsOf(events.PayRefundConfirmed)) != 0 {
		t.Fatalf("%d refunds left behind, notices %+v", n, r.alerts())
	}
}

func TestPayRefundCancel_OnlyWhileRequested(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	p := r.paidDirect(t, "REQ-12", "34", "")
	ref, _ := r.s.Request(ctx, r.m, models.PayModeLive, p.ID, webRefund("1"))
	if _, err := r.s.Cancel(ctx, uuid.New(), ref.ID); !errors.Is(err, ErrPayRefundNotFound) {
		t.Fatalf("another merchant: %v", err)
	}
	got, err := r.s.Cancel(ctx, r.m.ID, ref.ID)
	if err != nil || got.Status != models.PayRefundCancelled {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := r.s.Cancel(ctx, r.m.ID, ref.ID); !errors.Is(err, ErrPayRefundState) {
		t.Fatalf("twice: %v", err)
	}
	sent := r.insertRefund(t, r.paidDirect(t, "REQ-13", "34", ""), models.PayRefundSubmitted, 1_000_000)
	if _, err := r.s.Cancel(ctx, r.m.ID, sent.ID); !errors.Is(err, ErrPayRefundState) {
		t.Fatalf("submitted: %v", err)
	}
}

// Every refund event is pushed; the owner's are emailed too, never the payer's.
func TestNotificationMaps_PayRefundEvents(t *testing.T) {
	owner := []string{events.PayRefundRequested, events.PayRefundConfirmed, events.PayRefundFailed, events.PayRefundNeedsReview}
	for _, kind := range append(owner, events.PayRefundReceived) {
		if !pushWorthy(kind) {
			t.Errorf("%s is not pushed", kind)
		}
	}
	for _, kind := range owner {
		if !emailEvents[kind] {
			t.Errorf("%s is not emailed", kind)
		}
	}
	if emailEvents[events.PayRefundReceived] {
		t.Error("the payer's refund notice must not be emailed")
	}
}

// assertOneOpenViolationIsRecognised: the one-open index's refusal reads as
// ErrPayRefundOpen material; another unique violation on pay_refunds (a reused
// transaction hash) does not.
func assertOneOpenViolationIsRecognised(t *testing.T, db *gorm.DB) {
	t.Helper()
	paymentID := uuid.New()
	if err := db.Create(newRefundRow(paymentID, models.PayRefundRequested)).Error; err != nil {
		t.Fatal(err)
	}
	err := db.Create(newRefundRow(paymentID, models.PayRefundSubmitted)).Error
	if !isPayRefundOneOpenViolation(err) {
		t.Fatalf("second open refund: %v", err)
	}
	hash := "0xabc"
	a, b := newRefundRow(uuid.New(), models.PayRefundConfirmed), newRefundRow(uuid.New(), models.PayRefundConfirmed)
	a.TxHash, b.TxHash = &hash, &hash
	if err := db.Create(a).Error; err != nil {
		t.Fatal(err)
	}
	err = db.Create(b).Error
	if !isUniqueViolation(err) || isPayRefundOneOpenViolation(err) {
		t.Fatalf("a reused hash is not the one-open index: %v", err)
	}
}

func TestPayRefund_OneOpenViolationIsRecognised(t *testing.T) {
	assertOneOpenViolationIsRecognised(t, newPayTestDB(t))
}

func TestPayRefund_OneOpenViolationIsRecognisedOnPostgres(t *testing.T) {
	assertOneOpenViolationIsRecognised(t, testutil.TempPostgres(t, "pay_refund_viol_", &models.PayRefund{}))
}

// newRefundPostgresRig is newRefundRig on PostgreSQL (skips without DATABASE_URL).
func newRefundPostgresRig(t *testing.T) *refundRig {
	t.Helper()
	db := testutil.TempPostgres(t, "pay_refund_req_", &models.PayMerchant{}, &models.PayPayment{}, &models.PayWebhookDelivery{},
		&models.PayAuditEvent{}, &models.PayOwnerAction{}, &models.PayOpenLink{}, &models.PayRetiredSlug{},
		&models.PayMerchantNameChange{}, &models.PayRefund{})
	if err := db.Exec(`CREATE TABLE users (id UUID PRIMARY KEY, name TEXT, email TEXT, wallet_address TEXT,
		verification_status TEXT DEFAULT 'pending', verified_legal_name TEXT, account_status TEXT DEFAULT 'active')`).Error; err != nil {
		t.Fatal(err)
	}
	return newRefundRigOn(t, db)
}

// The payment row lock serialises concurrent requests; the partial index backs
// it. Every loser is told a refund is already open.
func TestPayRefundRequest_ConcurrentRequestsLeaveOneOpenOnPostgres(t *testing.T) {
	r := newRefundPostgresRig(t)
	p := r.paidDirect(t, "REQ-PG-1", "34", "")
	var wg sync.WaitGroup
	var mu sync.Mutex
	won, open := 0, 0
	var other []error
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID, PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrPayRefundOpen):
				open++
			default:
				other = append(other, err)
			}
		}()
	}
	wg.Wait()
	var rows int64
	r.db.Model(&models.PayRefund{}).Where("payment_id = ? AND status IN ?", p.ID, payRefundOpenStatuses).Count(&rows)
	if won != 1 || open != 7 || len(other) != 0 || rows != 1 {
		t.Fatalf("won %d open %d other %v rows %d", won, open, other, rows)
	}
}

// The cap sum and the test-mode confirm, with its webhook queued in the same
// transaction, as PostgreSQL runs them.
func TestPayRefundRequest_CapAndTestModeOnPostgres(t *testing.T) {
	ctx := context.Background()
	r := newRefundPostgresRig(t)
	p := r.paidDirect(t, "REQ-PG-2", "33", "")
	r.insertRefund(t, p, models.PayRefundConfirmed, 20_000_000)
	var over *PayRefundOverCapError
	if _, err := r.s.Request(ctx, r.m, models.PayModeLive, p.ID, webRefund("13.000001")); !errors.As(err, &over) || over.RemainingMicroUSD != 13_000_000 {
		t.Fatalf("over cap: %v", err)
	}
	if _, err := r.s.Request(ctx, r.m, models.PayModeLive, p.ID, webRefund("13")); err != nil {
		t.Fatalf("exactly the rest: %v", err)
	}

	tp := r.paid(t, models.PayModeTest, "REQ-PG-3", map[string]any{"customer_total_kobo": int64(1_500_000)})
	ref, err := r.s.Request(ctx, r.m, models.PayModeTest, tp.ID, webRefund("2.50"))
	if err != nil || ref.Status != models.PayRefundConfirmed || ref.CreditedMicroUSD != 2_500_000 {
		t.Fatalf("%+v %v", ref, err)
	}
	hooks := r.webhooks(t, models.PayEventRefunded)
	if len(hooks) != 1 || hooks[0]["livemode"] != false || hooks[0]["data"].(map[string]any)["refundedAmount"] != "2.50" ||
		hooks[0]["refund"].(map[string]any)["id"] != ref.ID.String() {
		t.Fatalf("hooks %+v", hooks)
	}
}

// The payment row lock, on its own: while another transaction holds it and
// takes most of the cap, a request waits and is then sized against what that
// transaction committed — never against what it read before.
func TestPayRefundRequest_WaitsOnThePaymentRowLockOnPostgres(t *testing.T) {
	r := newRefundPostgresRig(t)
	p := r.paidDirect(t, "REQ-PG-4", "34", "")
	tx := r.db.Begin()
	defer tx.Rollback()
	if err := tx.Exec(`SELECT id FROM pay_payments WHERE id = ? FOR UPDATE`, p.ID).Error; err != nil {
		t.Fatal(err)
	}
	at := payTestNow
	if err := tx.Create(&models.PayRefund{PaymentID: p.ID, MerchantID: p.MerchantID, Mode: p.Mode, AmountMicroUSD: 20_000_000,
		CapMicroUSD: 34_000_000, CreditedMicroUSD: 20_000_000, ToAddress: refundPayerWallet, Status: models.PayRefundConfirmed,
		RequestedVia: models.PayRefundViaApp, CreatedAt: payTestNow, ConfirmedAt: &at}).Error; err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID, PayRefundRequest{Amount: "20", Via: models.PayRefundViaApp})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the request did not wait for the payment row: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	var over *PayRefundOverCapError
	if err := <-done; !errors.As(err, &over) || over.RemainingMicroUSD != 14_000_000 {
		t.Fatalf("after the other refund committed: %v", err)
	}
}

// The partial index, on its own: an open refund written by a writer that never
// took the payment row lock is still the one open — the request's insert waits
// on the index and is then refused as ErrPayRefundOpen, not a raw DB error.
func TestPayRefundRequest_TheIndexBacksTheLockOnPostgres(t *testing.T) {
	r := newRefundPostgresRig(t)
	p := r.paidDirect(t, "REQ-PG-5", "34", "")
	tx := r.db.Begin()
	defer tx.Rollback()
	if err := tx.Create(&models.PayRefund{PaymentID: p.ID, MerchantID: p.MerchantID, Mode: p.Mode, AmountMicroUSD: 1_000_000,
		CapMicroUSD: 34_000_000, ToAddress: refundPayerWallet, Status: models.PayRefundRequested,
		RequestedVia: models.PayRefundViaApp, CreatedAt: payTestNow}).Error; err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.s.Request(context.Background(), r.m, models.PayModeLive, p.ID, PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the request must not have returned yet — it should wait on the index: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrPayRefundOpen) {
		t.Fatalf("%v", err)
	}
}

// A simulated refund is committed, confirmed and announced before Request
// returns, so Request hands back the row it claimed rather than reading it
// again: a failed re-read would report an error for a refund that exists, and
// the owner's retry would simulate a second one.
func TestPayRefundRequest_ATestRefundIsReturnedAsClaimedWithoutAReRead(t *testing.T) {
	r := newRefundRig(t)
	p := r.paid(t, models.PayModeTest, "REQ-15", map[string]any{"customer_total_kobo": int64(1_500_000)})
	const failReads = "test:fail_pay_refund_reads_outside_tx"
	if err := r.db.Callback().Query().Before("gorm:query").Register(failReads, func(db *gorm.DB) {
		if _, inTx := db.Statement.ConnPool.(gorm.TxCommitter); !inTx && db.Statement.Table == "pay_refunds" {
			db.AddError(errors.New("read after commit failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	ref, err := r.s.Request(context.Background(), r.m, models.PayModeTest, p.ID, webRefund("2.50"))
	if err := r.db.Callback().Query().Remove(failReads); err != nil {
		t.Fatal(err)
	}
	if err != nil || ref.Status != models.PayRefundConfirmed || ref.CreditedMicroUSD != 2_500_000 || ref.ConfirmedAt == nil {
		t.Fatalf("%+v %v", ref, err)
	}
	var n int64
	r.db.Model(&models.PayRefund{}).Where("payment_id = ? AND status = ?", p.ID, models.PayRefundConfirmed).Count(&n)
	if n != 1 {
		t.Fatalf("%d confirmed refunds", n)
	}
}

// A failed refund always says something after "failed:", even when no reason
// was recorded.
func TestPayRefundAnnounce_AFailedRefundWithoutAReasonStillReadsWell(t *testing.T) {
	r := newRefundRig(t)
	ref := r.insertRefund(t, r.paidDirect(t, "REQ-16", "34", ""), models.PayRefundFailed, 1_000_000)
	r.s.announce(context.Background(), ref, events.PayRefundFailed)
	failed := r.alertsOf(events.PayRefundFailed)
	if len(failed) != 1 || failed[0].Message != "Refund to Ada failed: "+payRefundFailedFallback+". You can request it again." {
		t.Fatalf("%+v", failed)
	}
}
