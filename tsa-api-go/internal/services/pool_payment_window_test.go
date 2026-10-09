package services

import (
	"errors"
	"testing"
	"time"
)

type windowEscrow struct {
	P2PEscrowAdapter // nil: only ReadPaymentWindow is called
	w                time.Duration
	err              error
}

func (e windowEscrow) ReadPaymentWindow() (time.Duration, error) { return e.w, e.err }

// The honour guard, the canceller and every slice's expires_at assume 60 min;
// an escrow deployed with another window builds no desk.
func TestCheckPoolPaymentWindow_MustMatchTheSliceWindow(t *testing.T) {
	if err := CheckPoolPaymentWindow(windowEscrow{w: 60 * time.Minute}); err != nil {
		t.Fatalf("60 min: %v", err)
	}
	for _, e := range []windowEscrow{{w: 30 * time.Minute}, {w: 2 * time.Hour}, {err: errors.New("rpc down")}} {
		if err := CheckPoolPaymentWindow(e); err == nil {
			t.Fatalf("%+v accepted", e)
		}
	}
	if err := CheckPoolPaymentWindow(&mockEscrow{}); err == nil {
		t.Fatal("an escrow that cannot report its window must not build a desk")
	}
}
