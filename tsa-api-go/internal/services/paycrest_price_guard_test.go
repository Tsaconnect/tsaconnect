package services

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckPriceGuard(t *testing.T) {
	cases := []struct {
		name, side, bound, limit string
		mode                     PriceGuardMode
		refuse                   bool
	}{
		{"off never checks", "buy", "2000", "1000", PriceGuardOff, false},
		{"empty limit never checks", "buy", "2000", "", PriceGuardEnforce, false},
		{"buy at the limit is fine", "buy", "1397.59", "1397.59", PriceGuardEnforce, false},
		{"buy one past the limit refuses", "buy", "1397.5901", "1397.59", PriceGuardEnforce, true},
		{"buy cheaper than the limit is fine", "buy", "1390", "1397.59", PriceGuardEnforce, false},
		{"sell at the limit is fine", "sell", "1353.25", "1353.25", PriceGuardEnforce, false},
		{"sell one below the limit refuses", "sell", "1353.2499", "1353.25", PriceGuardEnforce, true},
		{"sell above the limit is fine", "sell", "1360", "1353.25", PriceGuardEnforce, false},
		{"log mode never refuses", "buy", "2000", "1000", PriceGuardLog, false},
		{"unreadable bound refuses when enforcing", "buy", "n/a", "1000", PriceGuardEnforce, true},
		{"unreadable bound only logs in log mode", "buy", "n/a", "1000", PriceGuardLog, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPriceGuard(tc.side, tc.bound, tc.limit, tc.mode)
			if got := errors.Is(err, ErrPriceMovedPastQuote); got != tc.refuse {
				t.Fatalf("refuse = %v (err %v), want %v", got, err, tc.refuse)
			}
		})
	}
}

// An enforce refusal names the rate it bound at, so the router can show that
// rate (not the book's) for re-confirmation — while still reading as the
// sentinel, wording unchanged.
func TestCheckPriceGuard_EnforceRefusalCarriesTheBoundRate(t *testing.T) {
	err := checkPriceGuard("buy", " 1450.5 ", "1407", PriceGuardEnforce)
	var moved *PriceMovedPastQuoteError
	if !errors.As(err, &moved) || moved.BoundRate != "1450.5" {
		t.Fatalf("err = %#v, want the bound rate 1450.5", err)
	}
	if !errors.Is(err, ErrPriceMovedPastQuote) || err.Error() != ErrPriceMovedPastQuote.Error() {
		t.Fatalf("err = %v, must stay ErrPriceMovedPastQuote with its wording", err)
	}
}

// The whole point: an enforced refusal happens BEFORE Paycrest is asked to
// open anything, so failing over cannot leave an order behind.
func TestPriceGuard_EnforceRefusesBeforeCreate(t *testing.T) {
	pc := &capturingOrderer{rate: "1390.64", feePercent: "0.5"}
	svc := newSeamService(t, pc, "buy", "1390.64") // bound buy rate: 1397.59 (fee-inclusive)
	req := seamBuyRequest(seedVerifiedUser(t, svc.db))
	req.MaxRate, req.PriceGuard = "1390.00", PriceGuardEnforce
	_, err := svc.CreateBuy(context.Background(), req)
	if !errors.Is(err, ErrPriceMovedPastQuote) {
		t.Fatalf("err = %v, want ErrPriceMovedPastQuote", err)
	}
	if pc.onramp.Reference != "" {
		t.Fatal("the provider was asked to create an order the guard refused")
	}

	spc := &capturingOrderer{rate: "1360.05", feePercent: "0.5"}
	ssvc := newSeamService(t, spc, "sell", "1360.05") // bound sell rate: 1353.25
	sreq := sellRequest()
	sreq.MinRate, sreq.PriceGuard = "1360.00", PriceGuardEnforce
	if _, err := ssvc.CreateSell(context.Background(), sreq); !errors.Is(err, ErrPriceMovedPastQuote) {
		t.Fatalf("sell err = %v, want ErrPriceMovedPastQuote", err)
	}
	if spc.offramp.Reference != "" {
		t.Fatal("the provider was asked to create a sell the guard refused")
	}
}

func TestPriceGuard_LogModeStillCreates(t *testing.T) {
	buf := captureLog(t)
	pc := &capturingOrderer{rate: "1390.64", feePercent: "0.5"}
	svc := newSeamService(t, pc, "buy", "1390.64")
	req := seamBuyRequest(seedVerifiedUser(t, svc.db))
	req.MaxRate, req.PriceGuard = "1390.00", PriceGuardLog
	row, err := svc.CreateBuy(context.Background(), req)
	if err != nil || row == nil {
		t.Fatalf("log mode must create: row=%v err=%v", row, err)
	}
	if !strings.Contains(buf.String(), "ramp_price_guard_would_refuse stage=bind provider=paycrest side=buy") {
		t.Fatalf("log mode must record the bind-stage would-refuse, got %q", buf.String())
	}
}

func TestPriceGuard_WithinToleranceCreates(t *testing.T) {
	pc := &capturingOrderer{rate: "1390.64", feePercent: "0.5"}
	svc := newSeamService(t, pc, "buy", "1390.64")
	req := seamBuyRequest(seedVerifiedUser(t, svc.db))
	req.MaxRate, req.PriceGuard = "1400.00", PriceGuardEnforce
	if row, err := svc.CreateBuy(context.Background(), req); err != nil || row == nil {
		t.Fatalf("within tolerance: row=%v err=%v", row, err)
	}
	if pc.onramp.Reference == "" {
		t.Fatal("expected the order to be created")
	}
}

// /api/paycrest/* sends no limit: behaviour is exactly today's.
func TestPriceGuard_EmptyLimitIsTodaysBehaviour(t *testing.T) {
	pc := &capturingOrderer{rate: "1390.64", feePercent: "0.5"}
	svc := newSeamService(t, pc, "buy", "1390.64")
	if _, err := svc.CreateBuy(context.Background(), seamBuyRequest(seedVerifiedUser(t, svc.db))); err != nil {
		t.Fatalf("no guard fields: %v", err)
	}
}
