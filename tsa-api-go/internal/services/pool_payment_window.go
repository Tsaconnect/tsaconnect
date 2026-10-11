package services

import (
	"fmt"
	"time"
)

// CheckPoolPaymentWindow (re-review N7): the pool escrow's paymentWindow()
// must equal poolSliceWindow, which PoolHonourFits, the canceller and every
// slice's expires_at assume. An escrow that cannot say is refused too.
func CheckPoolPaymentWindow(e P2PEscrowAdapter) error {
	r, ok := e.(interface{ ReadPaymentWindow() (time.Duration, error) })
	if !ok {
		return fmt.Errorf("liquidity pool: the escrow cannot report its payment window")
	}
	w, err := r.ReadPaymentWindow()
	if err != nil {
		return fmt.Errorf("liquidity pool: read the payment window: %w", err)
	}
	if w != poolSliceWindow {
		return fmt.Errorf("liquidity pool: escrow payment window %s, the pool assumes %s", w, poolSliceWindow)
	}
	return nil
}
