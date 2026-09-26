package services

import (
	"context"
	"errors"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestFeePercentBpsRoundTrip(t *testing.T) {
	for in, want := range map[string]int{"1": 100, "0.5": 50, "0.75": 75, "1.25": 125} {
		got, ok := feePercentToBps(in)
		if !ok || got != want {
			t.Fatalf("feePercentToBps(%q) = %d,%v want %d", in, got, ok, want)
		}
		if back := bpsToFeePercent(got); back != in {
			t.Fatalf("bpsToFeePercent(%d) = %q want %q", got, back, in)
		}
	}
	if _, ok := feePercentToBps(""); ok {
		t.Fatal("empty fee must not parse")
	}
}

func TestSenderFeeFor(t *testing.T) {
	s := &PaycrestOrderService{senderFeePercent: "1", roundTripBudgetBps: 50}
	if got := s.senderFeeFor(false); got != "1" {
		t.Fatalf("direct = %q, want 1", got)
	}
	if got := s.senderFeeFor(true); got != "0.5" {
		t.Fatalf("converted = %q, want 0.5", got)
	}
	if !s.ConversionPriced() {
		t.Fatal("0 < B < F must price conversion")
	}
	s.roundTripBudgetBps = 100
	if got := s.senderFeeFor(true); got != "" || s.ConversionPriced() {
		t.Fatalf("F <= B: fee %q priced %v; nothing may be left for Paycrest", got, s.ConversionPriced())
	}
	s.roundTripBudgetBps = 0
	if s.ConversionPriced() {
		t.Fatal("B = 0 leaves no budget for the round trip")
	}
}

func TestCreateBuy_SenderFeeIsFMinusBOnConvertedOrders(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	r.svc.senderFeePercent = "1"
	r.svc.SetRoundTripBudgetBps(50)
	r.svc.SetConversionEnabled(true)
	r.svc.SetTreasury("0xTreasury")
	user := seedVerifiedUser(t, r.db)

	row, err := r.svc.CreateBuy(context.Background(), r.buyRequest(user, "base", "USDC", "optimism", "USDT"))
	if err != nil {
		t.Fatalf("CreateBuy: %v", err)
	}
	if r.client.lastCreate.SenderFeePercent != "0.5" {
		t.Fatalf("sender fee sent = %q, want 0.5", r.client.lastCreate.SenderFeePercent)
	}
	if row.SenderFeePercent != "0.5" {
		t.Fatalf("row fee = %q, want the order's own 0.5 (not the global F)", row.SenderFeePercent)
	}
	if row.RouteKind != models.RouteKindConverted {
		t.Fatalf("routeKind = %s", row.RouteKind)
	}
}

func TestCreateBuy_RefusesConversionWhenBudgetEatsTheWholeFee(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	r.svc.senderFeePercent = "0.5"
	r.svc.SetRoundTripBudgetBps(50)
	r.svc.SetConversionEnabled(true)
	r.svc.SetTreasury("0xTreasury")
	user := seedVerifiedUser(t, r.db)
	_, err := r.svc.CreateBuy(context.Background(), r.buyRequest(user, "base", "USDC", "optimism", "USDT"))
	if !errors.Is(err, ErrConversionUnavailable) {
		t.Fatalf("err = %v, want ErrConversionUnavailable", err)
	}
}

func TestChargedFeePercent_ComparesAgainstTheOrdersOwnFee(t *testing.T) {
	s := &PaycrestOrderService{senderFeePercent: "1"}
	if got := s.chargedFeePercent(&PaycrestOrder{SenderFeePercent: "0.5"}, "ref", "0.5"); got != "0.5" {
		t.Fatalf("charged = %q, want 0.5", got)
	}
}
