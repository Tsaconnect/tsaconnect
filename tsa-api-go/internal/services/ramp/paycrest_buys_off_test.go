package ramp

import (
	"context"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// This package's other tests exercise the Paycrest buy path kept for later.
func init() { services.PaycrestBuysOff = false }

// Owner 2026-10-16: no user KYC goes to Paycrest, so it never quotes a buy (the
// router goes straight to the pool and the others) and reports the leg closed.
func TestPaycrest_NoBuyQuotesOrBuyLegWhileBuysAreOff(t *testing.T) {
	services.PaycrestBuysOff = true
	t.Cleanup(func() { services.PaycrestBuysOff = false })
	ctx := context.Background()
	p := newTestPaycrest(&fakePcOrders{}, &fakePcQuotes{}, nil)
	if _, err := p.Quote(ctx, Request{Side: Buy, FiatCode: "NGN", FiatAmount: "1000"}); err == nil {
		t.Fatal("quoted a Paycrest buy while buys are off")
	}
}
