package config

import (
	"reflect"
	"testing"
)

func TestPayoutFeesFromEnv(t *testing.T) {
	got := payoutFeesFromEnv([]string{
		"FEE_PAYOUT_USD_BPS=120",
		"FEE_PAYOUT_USD_FLAT_NGN=2500",
		"FEE_PAYOUT_GHS_FLAT_NGN=50",
		"FEE_PAYOUT_GBP_BPS=abc",     // malformed: corridor dropped
		"FEE_PAYOUT_GBP_FLAT_NGN=10", // even though this half parses
		"FEE_PAYOUT_NGN_BPS=10",      // naira is not a foreign corridor
		"FEE_PAYOUT_EUR_OTHER=1",     // unknown field
		"FEE_PROVIDER_FALLBACK_BPS=200",
	})
	want := map[string]PayoutFee{
		"USD": {Bps: 120, FlatNGN: 2500},
		"GHS": {FlatNGN: 50},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
