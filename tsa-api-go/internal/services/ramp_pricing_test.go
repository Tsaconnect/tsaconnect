package services

import (
	"math/big"
	"testing"
)

func TestConversionMarkupBps(t *testing.T) {
	cases := []struct {
		name     string
		costBps  int
		want     int
		offered  bool
	}{
		{"cheap route keeps the floor, we keep the rest", 28, 50, true},
		{"at the floor", 40, 50, true},
		{"costly route covers cost plus 10bps", 62, 72, true},
		{"just under the cap", 89, 99, true},
		{"over the cap is not offered", 95, 0, false},
		{"negative cost is treated as zero", -5, 50, true},
	}
	for _, c := range cases {
		got, offered := ConversionMarkupBps(c.costBps)
		if got != c.want || offered != c.offered {
			t.Errorf("%s: ConversionMarkupBps(%d) = %d,%v; want %d,%v", c.name, c.costBps, got, offered, c.want, c.offered)
		}
	}
}

func TestConversionCostBpsAndPromise(t *testing.T) {
	route := big.NewInt(100_000000)     // 100 USDC from Paycrest
	toMin := big.NewInt(99_700000)      // LiFi guarantees 99.7
	if got := ConversionCostBps(route, toMin); got != 30 {
		t.Fatalf("cost = %d bps, want 30", got)
	}
	// 0.5% markup on 100 USDC promises 99.5.
	if got := PromisedAmount(route, 50); got.Cmp(big.NewInt(99_500000)) != 0 {
		t.Fatalf("promise = %s, want 99500000", got)
	}
}

func TestMeetsMinimumOrder(t *testing.T) {
	mins := map[string]float64{"ethereum": 5, "linea": 25}
	if !MeetsMinimumOrder("base", 1, mins, 1) {
		t.Fatal("$1 on base meets the $1 default")
	}
	if MeetsMinimumOrder("linea", 20, mins, 1) {
		t.Fatal("$20 on linea is below its $25 minimum")
	}
	if MeetsMinimumOrder("ethereum", 4.99, mins, 1) {
		t.Fatal("$4.99 on ethereum is below its $5 minimum")
	}
}
