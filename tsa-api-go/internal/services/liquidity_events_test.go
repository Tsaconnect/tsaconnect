package services

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/events"
)

// §19: an LP hears about a fill and its payout out of the app; a delayed
// payout reaches them by email too (their money is late).
func TestLiquidityEvents_Channels(t *testing.T) {
	if events.LiquidityFill != "liquidity.fill" || events.LiquidityPayoutSent != "liquidity.payout_sent" ||
		events.LiquidityPayoutDelayed != "liquidity.payout_delayed" {
		t.Fatal("event names are the spec's (§19)")
	}
	for _, e := range []string{events.LiquidityFill, events.LiquidityPayoutSent, events.LiquidityPayoutDelayed} {
		if !pushEvents[e] {
			t.Errorf("%s must push", e)
		}
	}
	if !emailEvents[events.LiquidityPayoutDelayed] {
		t.Error("a delayed payout must also email")
	}
}
