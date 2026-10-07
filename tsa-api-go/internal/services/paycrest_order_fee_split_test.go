package services

import (
	"context"
	"errors"
	"testing"
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

// The take is the one sender fee a ramp order sends; conversion is priced
// whenever it is usable.
func TestConversionPriced_FollowsTheTake(t *testing.T) {
	if got := SenderFeePercentFromBps(50); got != "0.5" {
		t.Fatalf("50 bps = %q, want 0.5", got)
	}
	if got := SenderFeePercentFromBps(0); got != "" {
		t.Fatalf("0 bps = %q, want no take", got)
	}
	s := &PaycrestOrderService{senderFeePercent: "0.5"}
	if !s.ConversionPriced() || s.TakePercent() != "0.5" {
		t.Fatal("a usable take must price conversion")
	}
	s.senderFeePercent = ""
	if s.ConversionPriced() {
		t.Fatal("no take: Paycrest would charge its dashboard default, so conversion stays off")
	}
}

// TSA Pay's split is the pre-ruling F / F − B, untouched by the take.
func TestPayFeeSplit_SenderFee(t *testing.T) {
	p := PayFeeSplit{FeePercent: "1", BudgetBps: 50}
	if got := p.SenderFee(false); got != "1" {
		t.Fatalf("direct = %q, want 1", got)
	}
	if got := p.SenderFee(true); got != "0.5" {
		t.Fatalf("converted = %q, want 0.5", got)
	}
	p.BudgetBps = 100
	if got := p.SenderFee(true); got != "" {
		t.Fatalf("F <= B: fee %q; nothing may be left for Paycrest", got)
	}
	p.BudgetBps = 0
	if got := p.SenderFee(true); got != "" {
		t.Fatalf("B = 0: fee %q; no budget for the round trip", got)
	}
	if got := (PayFeeSplit{FeePercent: " 0.50 "}).SenderFee(false); got != "0.5" {
		t.Fatalf("direct = %q, want the canonical 0.5", got)
	}
}

func TestCreateBuy_RefusesConversionWithoutAUsableTake(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	r.svc.senderFeePercent = ""
	r.svc.SetConversionEnabled(true)
	r.svc.SetTreasury("0xTreasury")
	user := seedVerifiedUser(t, r.db)
	_, err := r.svc.CreateBuy(context.Background(), r.buyRequest(user, "base", "USDC", "optimism", "USDT"))
	if !errors.Is(err, ErrConversionUnavailable) {
		t.Fatalf("err = %v, want ErrConversionUnavailable", err)
	}
}

func TestChargedFeePercent_ComparesAgainstTheOrdersOwnFee(t *testing.T) {
	s := &PaycrestOrderService{senderFeePercent: "0.5"}
	if got := s.chargedFeePercent(&PaycrestOrder{SenderFeePercent: "0.5"}, "ref", "0.5"); got != "0.5" {
		t.Fatalf("charged = %q, want 0.5", got)
	}
}
