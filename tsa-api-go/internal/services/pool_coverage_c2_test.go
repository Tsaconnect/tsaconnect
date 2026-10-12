package services

import (
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestSliceAtTreasury_Rule(t *testing.T) {
	now := time.Now()
	cases := []struct {
		status  string
		dropped *time.Time
		hash    string
		reason  string
		want    bool
	}{
		{models.P2PTradeStatusReleasing, nil, "", "", true},
		{models.P2PTradeStatusReleased, nil, "", "", true},
		{models.P2PTradeStatusReleased, &now, "0xself", "", false}, // review I-1: dropped, then self-released — the LP's USDT (F22)
		{models.P2PTradeStatusDisputed, nil, "", "", true},         // a paid order's dispute resolves to the treasury (F21)
		{models.P2PTradeStatusResolved, nil, "", "", true},
		{models.P2PTradeStatusDisputed, &now, "", "", false}, // F21 dropped it: going back to the LP
		{models.P2PTradeStatusResolved, &now, "", "", false},
		{models.P2PTradeStatusPendingPayment, nil, "", "", false},
		{models.P2PTradeStatusPendingPayment, nil, "0xrel", "", true}, // review C-1: a broadcast release, unresolved
		{models.P2PTradeStatusPaid, &now, "0xrel", "", true},          // even if something dropped it
		{models.P2PTradeStatusCancelled, nil, "0xrel", "", false},
		{models.P2PTradeStatusReleased, &now, "0xrel", poolF22Returned, false}, // re-review I-B: the USDT went back to the LP
	}
	for _, c := range cases {
		if got := sliceAtTreasury(c.status, c.dropped, c.hash, c.reason); got != c.want {
			t.Errorf("%s dropped=%v: %v, want %v", c.status, c.dropped != nil, got, c.want)
		}
	}
}

// P1 / hand-off: a releasing slice is R, never "pending" — otherwise S18
// would release more than she paid for.
func TestPoolCoverage_ReleasingCountsAsAtTreasury(t *testing.T) {
	r := newPoolOpenRig(t)
	_, ad := r.addLP(t, 50)
	intent := uuid.New()
	u := big.NewInt(10_000_000) // 10 USDT canonical
	chain, _ := CanonicalToChainUnits(u, 18)
	seedPoolSlice(t, r.db, sliceSeed{Intent: intent, Ad: ad, Units: chain, Status: models.P2PTradeStatusReleasing, Opened: true})
	seedPoolSlice(t, r.db, sliceSeed{Intent: intent, Ad: ad, Units: chain, Opened: true})
	seedPoolSlice(t, r.db, sliceSeed{Intent: intent, Ad: ad, Units: chain, Status: models.P2PTradeStatusResolved, Opened: true, Dropped: true})
	released, pending, err := r.desk.orderSlices(r.db, intent)
	if err != nil {
		t.Fatal(err)
	}
	if released.Cmp(u) != 0 || len(pending) != 1 {
		t.Fatalf("R=%s pending=%d, want R=10 USDT (the releasing slice) and one pending", released, len(pending))
	}
	cov := EvaluatePoolCoverage(u, released, pending, big.NewInt(2_000_000))
	if !cov.Covered || len(cov.Keep) != 0 || len(cov.Drop) != 1 {
		t.Fatalf("coverage = %+v, want covered by R alone, the pending slice dropped", cov)
	}
}
