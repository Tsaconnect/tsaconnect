package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestStableBuyWatcher_DrivesCompanyWalletBuys(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	f := &fakeBuyDriver{treasuryReceived: []uuid.UUID{a}, treasuryDelivering: []uuid.UUID{b}}
	if err := NewStableBuyWatcher(f, 0).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.sendCalls) != 2 || len(f.finalizeTCalls) != 2 {
		t.Fatalf("send=%v finalize=%v, want both orders sent then proven on one tick", f.sendCalls, f.finalizeTCalls)
	}
}

// D43: stock short at send alerts after 5 minutes; a stalled send at once;
// neither fails the tick.
func TestStableBuyWatcher_CompanyWalletAlertsOnTheirOwnChannels(t *testing.T) {
	f := &fakeBuyDriver{treasuryDelivering: []uuid.UUID{uuid.New()},
		sendErr:      fmt.Errorf("%w: needed 7 balance 1", services.ErrTreasuryStockShort),
		finalizeTErr: fmt.Errorf("%w: unproven", services.ErrTreasurySendStalled)}
	n := &recordingNotifier{}
	w := NewStableBuyWatcher(f, 0)
	w.SetAlerter(n)
	start := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	clock := start
	w.now = func() time.Time { return clock }
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("a waiting delivery must never fail the tick: %v", err)
	}
	if len(n.failing) != 1 || n.failing[0].Worker != "ramp-treasury-send-stalled" {
		t.Fatalf("first tick alerts = %+v, want only the stall", n.failing)
	}
	clock = start.Add(6 * time.Minute)
	_ = w.Run(context.Background())
	var stock bool
	for _, a := range n.failing {
		if a.Worker == "it-stable-buy-stock" {
			stock = true
		}
	}
	if !stock {
		t.Fatalf("no stock-short alert after 6 minutes: %+v", n.failing)
	}
}

// Review C2: a send refused for any reason but stock (kill switch, gas, a node
// refusal) is a stuck order — it alerts once sustained for 10 minutes.
func TestStableBuyWatcher_ASendRefusedForAnotherReasonAlerts(t *testing.T) {
	f := &fakeBuyDriver{treasuryDelivering: []uuid.UUID{uuid.New()}, sendErr: services.ErrTreasuryKilled}
	n := &recordingNotifier{}
	w := NewStableBuyWatcher(f, 0)
	w.SetAlerter(n)
	start := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	clock := start
	w.now = func() time.Time { return clock }
	_ = w.Run(context.Background())
	if len(n.failing) != 0 {
		t.Fatalf("alerted at once: %+v", n.failing)
	}
	clock = start.Add(11 * time.Minute)
	_ = w.Run(context.Background())
	if len(n.failing) != 1 || n.failing[0].Worker != "ramp-treasury-send-blocked" {
		t.Fatalf("alerts = %+v, want the send-blocked alert", n.failing)
	}
}
