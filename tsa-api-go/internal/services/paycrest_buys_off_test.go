package services

import (
	"context"
	"errors"
	"testing"
)

// The rest of this package's tests exercise the Paycrest buy path, which is
// kept for when buys are switched back on; they run with it on.
func init() { PaycrestBuysOff = false }

// Owner 2026-10-16: no user KYC is shared with Paycrest, so buys are refused.
func TestPaycrestCreateBuy_RefusedWhileBuysAreOff(t *testing.T) {
	PaycrestBuysOff = true
	t.Cleanup(func() { PaycrestBuysOff = false })
	_, err := (&PaycrestOrderService{}).CreateBuy(context.Background(), PaycrestBuyRequest{WalletAddress: "0xabc"})
	if !errors.Is(err, ErrPaycrestLegClosed) {
		t.Fatalf("err = %v, want ErrPaycrestLegClosed", err)
	}
}
