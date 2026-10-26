package handlers

import (
	"math"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestEffectiveTPRatePct(t *testing.T) {
	const program = 0.12 // 5% user + 5% upline + 2% pool
	cases := []struct {
		name   string
		bps    int
		shares services.FeeShares
		want   float64
	}{
		// 2% fee, platform keeps 70% (50 system + 20 tp), 12% of it: 0.168% of the sale.
		{"product", 200, services.FeeShares{System: 0.5, Buyer: 0.15, Upline: 0.15, TP: 0.2}, 0.168},
		// 3% fee, no cashback: all of it is kept.
		{"spot", 300, services.FeeShares{System: 0.7, TP: 0.3}, 0.36},
		// gas mints no TP.
		{"gas", 50, services.FeeShares{System: 1}, 0},
	}
	for _, c := range cases {
		if got := effectiveTPRatePct(c.bps, c.shares, program); math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("%s: effective TP = %v%%, want %v%%", c.name, got, c.want)
		}
	}
}

func TestFeeRailFor(t *testing.T) {
	for src, want := range map[string]string{
		"checkout": feeRailEscrow, "service_contact": feeRailEscrow, "p2p_release": feeRailEscrow,
		"swap": feeRailOffchain, "send": feeRailOffchain, "it_intent": feeRailOffchain, "allswap": feeRailOffchain,
	} {
		if got := feeRailFor(src); got != want {
			t.Errorf("feeRailFor(%q) = %q, want %q", src, got, want)
		}
	}
}
