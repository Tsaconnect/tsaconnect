package main

import (
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// Refunds need the chain registry; without one they stay unwired — no routes,
// no worker, and payments read as never refunded.
func TestBuildPayRefunds_NeedsAChainRegistry(t *testing.T) {
	sched := jobs.NewScheduler()
	payments := services.NewPayPaymentService(nil, nil, 100, "", time.Now)
	if r := buildPayRefunds(&config.Config{}, nil, payments, nil, nil, nil, sched); r != nil || sched.Count() != 0 || payments.RefundsWired() {
		t.Fatal("refunds wired with no chain registry")
	}
}

// With one, the confirmer is registered and the payment views read refunds —
// whether or not the ramp treasury exists (no treasury is wired here).
func TestBuildPayRefunds_RegistersTheConfirmerAndTheAggregator(t *testing.T) {
	sched := jobs.NewScheduler()
	payments := services.NewPayPaymentService(nil, nil, 100, "", time.Now)
	cfg := &config.Config{RampTreasuryConfirmations: 5}
	chains := services.NewBlockchainService(cfg)
	if r := buildPayRefunds(cfg, nil, payments, nil, chains, nil, sched); r == nil {
		t.Fatal("refunds not built")
	}
	if sched.Count() != 1 || !payments.RefundsWired() {
		t.Fatalf("workers %d wired %v", sched.Count(), payments.RefundsWired())
	}
}
