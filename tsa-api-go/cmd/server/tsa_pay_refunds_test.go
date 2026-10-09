package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// Refunds need the chain registry; without one they stay unwired — the routes
// answer 503, no worker runs, and payments read as never refunded.
func TestBuildPayRefunds_NeedsAChainRegistry(t *testing.T) {
	sched := jobs.NewScheduler()
	payments := services.NewPayPaymentService(nil, nil, 100, "", time.Now)
	if r := buildPayRefunds(&config.Config{TSAPayRefundsEnabled: true}, nil, payments, nil, nil, nil, sched); r != nil || sched.Count() != 0 || payments.RefundsWired() {
		t.Fatal("refunds wired with no chain registry")
	}
}

// captureBootLog collects what the standard logger prints during the test.
func captureBootLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &logs
}

// With TSA_PAY_REFUNDS on, the confirmer is registered, the routes get the
// service and the payment views read refunds — whether or not the ramp
// treasury exists (no treasury is wired here).
func TestBuildPayRefunds_OnRegistersTheConfirmerAndTheAggregator(t *testing.T) {
	logs := captureBootLog(t)
	sched := jobs.NewScheduler()
	payments := services.NewPayPaymentService(nil, nil, 100, "", time.Now)
	cfg := &config.Config{RampTreasuryConfirmations: 5, TSAPayRefundsEnabled: true}
	chains := services.NewBlockchainService(cfg)
	if r := buildPayRefunds(cfg, nil, payments, nil, chains, nil, sched); r == nil {
		t.Fatal("refunds not built")
	}
	if sched.Count() != 1 || !payments.RefundsWired() {
		t.Fatalf("workers %d wired %v", sched.Count(), payments.RefundsWired())
	}
	if !strings.Contains(logs.String(), "[tsa-pay] refunds: on (TSA_PAY_REFUNDS)") {
		t.Fatalf("boot log %q", logs.String())
	}
}

// Off (the default): no confirmer and nothing for the routes, which answer
// 503 — but payment views, aggregates and webhooks still read the refunds
// table, so a refund made while it was on is never hidden.
func TestBuildPayRefunds_OffKeepsTheViewsButNoConfirmerOrRoutes(t *testing.T) {
	logs := captureBootLog(t)
	sched := jobs.NewScheduler()
	payments := services.NewPayPaymentService(nil, nil, 100, "", time.Now)
	cfg := &config.Config{RampTreasuryConfirmations: 5}
	chains := services.NewBlockchainService(cfg)
	if r := buildPayRefunds(cfg, nil, payments, nil, chains, nil, sched); r != nil {
		t.Fatal("refund routes wired with TSA_PAY_REFUNDS off")
	}
	if sched.Count() != 0 || !payments.RefundsWired() {
		t.Fatalf("workers %d wired %v", sched.Count(), payments.RefundsWired())
	}
	if !strings.Contains(logs.String(), "[tsa-pay] refunds: off (TSA_PAY_REFUNDS)") {
		t.Fatalf("boot log %q", logs.String())
	}
}
