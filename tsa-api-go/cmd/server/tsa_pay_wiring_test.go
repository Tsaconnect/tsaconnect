package main

import (
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBuildTSAPay_DormantUnlessEnabledAndKeyed(t *testing.T) {
	sched := jobs.NewScheduler()
	if h := buildTSAPay(&config.Config{TSAPayEnabled: false}, nil, nil, nil, sched); h != nil {
		t.Fatal("disabled TSA Pay built a handler")
	}
	t.Setenv("PAYCREST_SENDER_FEE_PERCENT", "1")
	if h := buildTSAPay(&config.Config{TSAPayEnabled: true, TSAPayWebhookMasterKey: "abcd"}, nil, nil, nil, sched); h != nil {
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

	h := buildTSAPay(cfg, db, nil, nil, sched)
	if h == nil {
		t.Fatal("a valid config left TSA Pay dormant")
	}
	if sched.Count() != 2 {
		t.Fatalf("sched.Count() = %d, want 2 (webhook worker + reconciler)", sched.Count())
	}
}
