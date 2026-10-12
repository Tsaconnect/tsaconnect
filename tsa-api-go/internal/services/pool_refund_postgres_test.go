package services

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// S37 under real row locks: the refund and the settle engine's CAS race; one
// wins every round, never both (no refund of an order that went delivering).
func TestPoolRefundVsSettle_Race_Postgres(t *testing.T) {
	r := newPoolOpenRigPG(t)
	if err := r.db.AutoMigrate(&models.ITIntentAuditLog{}, &models.RampLeg{}); err != nil {
		t.Fatal(err)
	}
	_, ad := r.addLP(t, 50)
	wins := map[string]int{}
	for round := 0; round < 20; round++ {
		id := uuid.New()
		in := poolIntentFor(r.price(t, "5000"))
		in.ID, in.Status, in.ReceivedAmount = id, models.ITStatusPaymentReceived, "500000"
		if err := r.db.Create(in).Error; err != nil {
			t.Fatal(err)
		}
		seedPoolSlice(t, r.db, sliceSeed{Intent: id, Ad: ad, Units: usdt18(3), Opened: true})
		var wg sync.WaitGroup
		var refunded, delivering bool
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := r.it.refundPoolBuy(context.Background(), uuid.New(), id, "race")
			refunded = err == nil
		}()
		go func() {
			defer wg.Done()
			won, _ := r.it.claimPoolDelivery(context.Background(), r.desk, id)
			delivering = won
		}()
		wg.Wait()
		var got models.ITIntent
		r.db.First(&got, "id = ?", id)
		if refunded == delivering || (refunded && got.Status != models.ITStatusRefunded) || (delivering && got.Status != models.ITStatusDelivering) {
			t.Fatalf("round %d: refunded=%v delivering=%v status=%s — exactly one must win", round, refunded, delivering, got.Status)
		}
		if refunded {
			wins["refund"]++
		} else {
			wins["settle"]++
		}
	}
	t.Logf("wins: %v", wins)
}

// SQLite stores B's restored ad remaining as REAL (the rig's documented
// limit: one open per ad), so re-using B is a _Postgres test.
func TestPoolRedeliver_MayReuseTheLPWhoseSliceWasCancelled_Postgres(t *testing.T) {
	r := newPoolSettleRigPG(t, 60, 45) // only A and B exist
	intent := parkedShort(t, r)
	got, err := r.it.RedeliverPoolBuy(context.Background(), uuid.New(), intent.ID, "B's stock is free again")
	if err != nil || got.Status != models.ITStatusPaymentReceived {
		t.Fatalf("redeliver = %v %v, want B re-used for the owed units", got, err)
	}
}
