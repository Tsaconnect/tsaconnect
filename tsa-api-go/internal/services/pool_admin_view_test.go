package services

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// §15: the detail an operator decides on — snapshot, slices with their LP,
// forward state and fee, the sale leg, and only the actions the server allows.
func TestPoolOrderDetail_ShowsSlicesForwardsAndServerActions(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	r.wireForwards(t)
	intent := r.openPaid(t, "100000")
	_ = r.advance(t, intent.ID)
	d, err := r.it.PoolOrderDetail(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Snapshots) != 1 || len(d.Slices) != 2 || d.Slices[0].Forward == nil || d.SaleLeg == nil || d.Slices[0].LPName == "" {
		t.Fatalf("detail = %+v", d)
	}
	for _, a := range d.Actions {
		if a == "refund" {
			t.Fatal("a delivering order with released slices is never offered a refund")
		}
	}
}

func TestPoolOrderDetail_ParkedShortOffersRedeliver(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45, 30)
	intent := parkedShort(t, r)
	d, err := r.it.PoolOrderDetail(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A slice is at the treasury: no plain refund. S37's super-admin override
	// (review I-6, Task 10) is the only refund exit offered.
	if len(d.Actions) != 2 || d.Actions[0] != "redeliver" || d.Actions[1] != "refund-override" {
		t.Fatalf("actions = %v, want [redeliver refund-override]", d.Actions)
	}
	if len(d.Slices) != 2 || d.Slices[1].Status != models.P2PTradeStatusCancelled {
		t.Fatalf("slices = %+v", d.Slices)
	}
	for _, sl := range d.Slices {
		if len(sl.SliceAction) != 0 {
			t.Fatalf("a paid order's slice is never offered F22: %+v", sl)
		}
	}
}

// F22 / review I-5: an unpaid order's self-released slice offers pay-lp and
// returned; once either exit is taken, neither is offered again.
func TestPoolOrderDetail_SelfReleasedSliceOffersF22UntilTaken(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	r.wireForwards(t)
	intent, _, err := r.open(t, "5000")
	if err != nil {
		t.Fatal(err)
	}
	sl := r.slicesOf(t, intent.ID)[0]
	r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusReleased)
	if _, err := r.trades.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatal(err)
	}
	d, err := r.it.PoolOrderDetail(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Slices[0].SliceAction; len(got) != 2 || got[0] != "pay-lp" || got[1] != "returned" {
		t.Fatalf("slice actions = %v, want [pay-lp returned]", got)
	}
	if err := r.it.NotePoolSliceReturned(context.Background(), uuid.New(), *sl.ITIntentID, sl.ID, "0xreturn", "sent back by hand"); err != nil {
		t.Fatal(err)
	}
	d, err = r.it.PoolOrderDetail(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Slices[0].SliceAction; len(got) != 0 || !d.Slices[0].Dropped {
		t.Fatalf("after returned: slice = %+v, want no F22 action", d.Slices[0])
	}
}
