package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

type fakePaymentChecker struct {
	ran   bool
	calls int
	// onCheck simulates the matcher settling the order during the tick.
	onCheck func()
	block   chan struct{}
	ctx     context.Context
}

func (f *fakePaymentChecker) CheckNow(ctx context.Context) (bool, error) {
	f.calls++
	f.ctx = ctx
	if f.block != nil {
		<-f.block
	}
	if f.onCheck != nil {
		f.onCheck()
	}
	return f.ran, nil
}

// "I've paid" runs the lookup and answers with the order as it stands after it.
func TestPostCheckBuyPayment_RunsLookupAndReturnsFreshOrder(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	id := openedIntentID(t, doITBuy(t, h, user, "500000"))

	checker := &fakePaymentChecker{ran: true, onCheck: func() {
		db.Model(&models.ITIntent{}).Where("id = ?", id).Update("status", models.ITStatusCredited)
	}}
	h.SetPaymentChecker(checker, context.Background())

	data := itBuyData(t, doITIntentAction(t, h, user, id, h.PostCheckBuyPayment))
	if checker.calls != 1 {
		t.Fatalf("lookup calls = %d, want 1", checker.calls)
	}
	if data["checked"] != true {
		t.Errorf("checked = %v, want true", data["checked"])
	}
	if intent, _ := data["intent"].(map[string]any); intent["status"] != models.ITStatusCredited {
		t.Errorf("status = %v, want the credited order", intent["status"])
	}
	if data["payTo"] != nil {
		t.Error("a credited order still carries a pay-into card")
	}
}

// A lookup that ran no tick of its own may still have waited for one that
// matched the payment, so the answer is re-read regardless.
func TestPostCheckBuyPayment_ReturnsFreshOrderWhenLookupSkipped(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	id := openedIntentID(t, doITBuy(t, h, user, "500000"))

	h.SetPaymentChecker(&fakePaymentChecker{ran: false, onCheck: func() {
		db.Model(&models.ITIntent{}).Where("id = ?", id).Update("status", models.ITStatusCredited)
	}}, context.Background())

	data := itBuyData(t, doITIntentAction(t, h, user, id, h.PostCheckBuyPayment))
	if data["checked"] != false {
		t.Errorf("checked = %v, want false", data["checked"])
	}
	if intent, _ := data["intent"].(map[string]any); intent["status"] != models.ITStatusCredited {
		t.Errorf("status = %v, want the order credited by the tick it waited for", intent["status"])
	}
}

// Only a buy still waiting for money triggers a lookup.
func TestPostCheckBuyPayment_SkipsOrdersNotAwaitingTransfer(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	id := openedIntentID(t, doITBuy(t, h, user, "500000"))
	db.Model(&models.ITIntent{}).Where("id = ?", id).Update("status", models.ITStatusCancelled)

	checker := &fakePaymentChecker{ran: true}
	h.SetPaymentChecker(checker, context.Background())

	data := itBuyData(t, doITIntentAction(t, h, user, id, h.PostCheckBuyPayment))
	if checker.calls != 0 {
		t.Errorf("lookup calls = %d, want 0", checker.calls)
	}
	if data["checked"] != false {
		t.Errorf("checked = %v, want false", data["checked"])
	}
}

func TestPostCheckBuyPayment_OtherUsersOrderIs404(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	owner := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	id := openedIntentID(t, doITBuy(t, h, owner, "500000"))
	checker := &fakePaymentChecker{ran: true}
	h.SetPaymentChecker(checker, context.Background())

	other := &models.User{ID: uuid.New(), Name: "JANE ROE"}
	rec := doITIntentAction(t, h, other, id, h.PostCheckBuyPayment)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if checker.calls != 0 {
		t.Errorf("lookup calls = %d, want 0", checker.calls)
	}
}

// A slow lookup does not hold the request past the wait; it answers with the
// order as it stands and the lookup finishes on its own.
func TestPostCheckBuyPayment_SlowLookupAnswersAfterWait(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	id := openedIntentID(t, doITBuy(t, h, user, "500000"))

	prev := checkPaymentWait
	checkPaymentWait = 20 * time.Millisecond
	t.Cleanup(func() { checkPaymentWait = prev })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h.SetPaymentChecker(&fakePaymentChecker{ran: true, block: release}, context.Background())

	data := itBuyData(t, doITIntentAction(t, h, user, id, h.PostCheckBuyPayment))
	if data["checked"] != false {
		t.Errorf("checked = %v, want false after the wait", data["checked"])
	}
	if intent, _ := data["intent"].(map[string]any); intent["status"] != models.ITStatusPendingPayment {
		t.Errorf("status = %v, want pending_payment", intent["status"])
	}
}

// The lookup obeys server shutdown, like the scheduled poll, rather than
// running on a context nothing can ever cancel.
func TestPostCheckBuyPayment_LookupRunsOnServerLifetime(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	id := openedIntentID(t, doITBuy(t, h, user, "500000"))

	lifetime, shutdown := context.WithCancel(context.Background())
	checker := &fakePaymentChecker{ran: true}
	h.SetPaymentChecker(checker, lifetime)
	itBuyData(t, doITIntentAction(t, h, user, id, h.PostCheckBuyPayment))

	shutdown()
	if checker.ctx == nil || checker.ctx.Err() == nil {
		t.Error("the lookup's context was not cancelled by server shutdown")
	}
}
