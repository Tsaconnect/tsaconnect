package services

import (
	"errors"
	"math/big"
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

// Review M-3: one decode for paymentWindow()'s uint40, whichever Go type the
// ABI hands back; anything else is an error, never a zero window.
func TestPaymentWindowOf(t *testing.T) {
	for _, c := range []struct {
		in   []any
		want time.Duration
		ok   bool
	}{
		{[]any{big.NewInt(3600)}, time.Hour, true},
		{[]any{uint64(1800)}, 30 * time.Minute, true},
		{[]any{(*big.Int)(nil)}, 0, false},
		{[]any{big.NewInt(-1)}, 0, false},
		{[]any{"3600"}, 0, false},
		{nil, 0, false},
	} {
		got, err := paymentWindowOf(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("paymentWindowOf(%v) = %s, %v; want %s ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}
