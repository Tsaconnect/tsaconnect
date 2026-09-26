package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBuildTSAPay_DormantUnlessEnabledAndKeyed(t *testing.T) {
	sched := jobs.NewScheduler()
	if h := buildTSAPay(&config.Config{TSAPayEnabled: false}, nil, nil, nil, sched, nil, nil, nil, nil, nil, nil); h != nil {
		t.Fatal("disabled TSA Pay built a handler")
	}
	t.Setenv("PAYCREST_SENDER_FEE_PERCENT", "1")
	if h := buildTSAPay(&config.Config{TSAPayEnabled: true, TSAPayWebhookMasterKey: "abcd"}, nil, nil, nil, sched, nil, nil, nil, nil, nil, nil); h != nil {
		t.Fatal("a short master key must leave TSA Pay dormant")
	}
	if sched.Count() != 0 {
		t.Fatal("a dormant TSA Pay registered workers")
	}
}

// A live build must register both background workers TSA Pay needs to make
// progress on its own — the webhook deliverer and the reconciler — so a
// deploy that flips TSA_PAY_ENABLED on gets them for free rather than
// needing a second change to main.go.
func TestBuildTSAPay_LiveRegistersTheWebhookAndReconcilerWorkers(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAYCREST_SENDER_FEE_PERCENT", "1")
	sched := jobs.NewScheduler()
	cfg := &config.Config{TSAPayEnabled: true, TSAPayWebhookMasterKey: strings.Repeat("ab", 32)}

	h := buildTSAPay(cfg, db, nil, nil, sched, nil, nil, nil, nil, nil, nil)
	if h == nil {
		t.Fatal("a valid config left TSA Pay dormant")
	}
	if sched.Count() != 2 {
		t.Fatalf("sched.Count() = %d, want 2 (webhook worker + reconciler)", sched.Count())
	}
}

// The naira start path is wired only when a Paycrest order service exists;
// without swapped-sell services it still starts direct payments (converted
// ones answer "unavailable"). A nil pointer must never be handed over as an
// interface, which would read as wired.
func TestBuildPayCheckout_NeedsOrdersAndNeverWrapsANilPointer(t *testing.T) {
	orders := &services.PaycrestOrderService{}
	if buildPayCheckout(nil, nil, nil, nil, nil, true) != nil {
		t.Fatal("no order service must leave the start routes unwired")
	}
	if buildPayCheckout(nil, orders, nil, nil, nil, true) == nil {
		t.Fatal("direct payments need only the order service")
	}
	if buildPayCheckout(nil, orders, &services.SellIntentService{}, nil, nil, true) == nil {
		t.Fatal("intents without a quoter must still start direct payments")
	}
}

// The reconciler follows converted and crypto payments through the intent
// service alone: turning the ramp-sell quoter (or Paycrest orders) off must not
// leave payments already in flight with nothing following them.
func TestBuildPayCheckout_TheReconcilerFollowsIntentsWhateverElseIsOff(t *testing.T) {
	for name, orders := range map[string]*services.PaycrestOrderService{"no quoter": &services.PaycrestOrderService{}, "no orders and no quoter": nil} {
		payments := services.NewPayPaymentService(nil, nil, 100, "", time.Now)
		buildPayCheckout(payments, orders, &services.SellIntentService{}, nil, nil, true)
		if !payments.IntentsWired() {
			t.Fatalf("%s: in-flight converted payments are stranded", name)
		}
	}
	payments := services.NewPayPaymentService(nil, nil, 100, "", time.Now)
	buildPayCheckout(payments, &services.PaycrestOrderService{}, nil, nil, nil, true)
	if payments.IntentsWired() {
		t.Fatal("no intent service must leave the reconciler with none")
	}
}
