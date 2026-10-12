package services

import (
	"context"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// F21 on Postgres (review M7): the decision takes the intent's FOR UPDATE, a
// paid order's disputed slice resolves to the treasury through the release
// key and pauses the LP, and an unpaid order's slice goes back to the LP with
// dropped_at written in the decision's own transaction.
func TestPoolSlice_DisputeDecidedUnderIntentLock_Postgres(t *testing.T) {
	r := newPoolOpenRigPG(t)
	lp, _ := r.addLP(t, 30)
	ctx := context.Background()
	p := r.price(t, "5000")
	intent := poolIntentFor(p)
	var out []models.P2PTrade
	if err := r.desk.insertOrder(ctx, intent, p, nil, &out); err != nil || len(out) != 1 {
		t.Fatalf("insert order: %v (%d slices)", err, len(out))
	}
	sl := out[0]
	r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("status", models.ITStatusPaymentReceived)
	r.chain.cmu.Lock()
	r.chain.trades[HashID(sl.ID)] = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusDisputed, Amount: usdt18(3)}
	r.chain.cmu.Unlock()
	sender := releaseSender()
	r.trades.SetPoolSlices(r.it, sender)
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil {
		t.Fatal(err)
	}
	var lpRow models.PaymentMerchant
	r.db.First(&lpRow, "id = ?", lp.ID)
	if len(r.chain.resolves) != 1 || !r.chain.resolves[0] || len(sender.sendChains) != 1 || lpRow.Status != models.PaymentMerchantStatusPaused {
		t.Fatalf("resolves=%v sends=%v lp=%s, want one treasury resolve and the LP paused", r.chain.resolves, sender.sendChains, lpRow.Status)
	}

	r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("status", models.ITStatusPendingPayment)
	toTreasury, err := decideSliceResolve(r, &sl)
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if err != nil || toTreasury || got.DroppedAt == nil {
		t.Fatalf("unpaid: toTreasury=%v err=%v dropped=%v, want back to the LP and dropped", toTreasury, err, got.DroppedAt)
	}
	r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("status", models.ITStatusPaymentReceived)
	if again, err := decideSliceResolve(r, &got); err != nil || again {
		t.Fatalf("a dropped slice flipped to the treasury: %v %v", again, err)
	}
}

// decideSliceResolve runs F21's decision in its own transaction, as
// ClaimSliceResolve does.
func decideSliceResolve(r *poolOpenRig, sl *models.P2PTrade) (bool, error) {
	toTreasury := false
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var err error
		toTreasury, err = r.it.decideSliceResolveTx(tx, sl)
		return err
	})
	return toTreasury, err
}
