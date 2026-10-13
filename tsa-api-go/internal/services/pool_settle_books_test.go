package services

import (
	"context"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func (r *poolSettleRig) settleDirect(t *testing.T, intent *models.ITIntent) {
	t.Helper()
	_ = r.advance(t, intent.ID)
	legs := r.legs(t, intent.ID)
	want, _ := new(big.Int).SetString(legs[0].Amount, 10)
	r.saleVerifier.err, r.saleVerifier.credited = nil, want
	r.db.Model(&models.RampLeg{}).Where("id = ?", legs[0].ID).Update("tx_hash", "0xsale")
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatal(err)
	}
}

func (r *poolSettleRig) book(t *testing.T, id uuid.UUID) models.PoolOrder {
	t.Helper()
	var o models.PoolOrder
	r.db.Where("intent_id = ?", id).Order("generation DESC").First(&o)
	return o
}

// §10.2 golden with per-order costs priced in (net 72.290372 over two slices):
// take ₦497.52, LP total ₦97,934.65, network fee ₦95.30 (two slices' gas +
// delivery, 0.07 USDT at ₦1,361.51) booked as the treasury's gas cost, keep
// ₦550.05 = 100,000 − 1,400 funding − 97,934.65 LPs − 20 actual payout fees
// − 95.30 gas. Keep still holds the take and the payout-fee estimate's excess.
func TestPoolBooks_WorkedExample100k(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	fwd, _, _ := r.wireForwards(t)
	fwd.SetFeeReader(&fakeFeeReader{kobo: big.NewInt(1000)})
	intent := r.openPaid(t, "100000")
	r.db.Create(&models.InboundCredit{ID: uuid.New(), Provider: models.InboundCreditProviderFlutterwave, ProviderRef: "sess_fund_100k",
		Currency: "NGN", AmountKobo: 9_860_000, FeeKobo: 140_000, GrossKobo: 10_000_000, Status: "matched",
		MatchedClaimType: InboundClaimTypeITStable, MatchedClaimID: &intent.ID})
	r.settleDirect(t, intent)
	b := r.book(t, intent.ID)
	if b.TakeKobo == nil || *b.TakeKobo != "49752" || b.LPTotalKobo == nil || *b.LPTotalKobo != "9793465" || b.NetworkFeeKobo == nil || *b.NetworkFeeKobo != "9530" ||
		b.DeliveredUnits == nil || *b.DeliveredUnits != intent.AssetAmount || b.SettledAt == nil || b.KeepKobo != nil {
		t.Fatalf("books at settle = %+v", b)
	}
	for _, f := range r.forwards(t) {
		if err := fwd.Confirm(context.Background(), f.Reference, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.it.CompletePoolBooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	b = r.book(t, intent.ID)
	if b.PayoutFeeKobo == nil || *b.PayoutFeeKobo != "2000" || b.KeepKobo == nil || *b.KeepKobo != "55005" {
		t.Fatalf("books = fee %v keep %v, want ₦20.00 and ₦550.05", b.PayoutFeeKobo, b.KeepKobo)
	}
}

// S47: keep is written only once every LP forward settled with its actual fee.
func TestPoolBooks_KeepWaitsForEveryActualFee(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	fwd, _, _ := r.wireForwards(t)
	intent := r.openPaid(t, "100000")
	r.settleDirect(t, intent)
	fw := r.forwards(t)
	_ = fwd.Confirm(context.Background(), fw[0].Reference, true, "") // one of two, and no fee reader yet
	if _, err := r.it.CompletePoolBooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b := r.book(t, intent.ID); b.KeepKobo != nil {
		t.Fatal("keep written before every forward settled with a fee")
	}
	fwd.SetFeeReader(&fakeFeeReader{kobo: big.NewInt(1000)})
	_ = fwd.Confirm(context.Background(), fw[1].Reference, true, "")
	if _, err := r.it.CompletePoolBooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b := r.book(t, intent.ID); b.KeepKobo == nil {
		t.Fatal("keep not written once both fees are known (the pass back-fills the first one's fee)")
	}
}

// §10.3 golden, funding estimated from the snapshot when the credit carries
// no fee row: keep = 5,000 − 70 − 4,840.60 − 10 − 54.46 gas = ₦24.94 (net
// 3.573085 after one slice's gas and payout fee; the gas line is the cost).
func TestPoolBooks_WorkedExample5kEstimatedFunding(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	fwd, _, _ := r.wireForwards(t)
	fwd.SetFeeReader(&fakeFeeReader{kobo: big.NewInt(1000)})
	intent := r.openPaid(t, "5000")
	r.settleDirect(t, intent)
	for _, f := range r.forwards(t) {
		_ = fwd.Confirm(context.Background(), f.Reference, true, "")
	}
	_, _ = r.it.CompletePoolBooks(context.Background())
	if b := r.book(t, intent.ID); b.KeepKobo == nil || *b.KeepKobo != "2494" {
		t.Fatalf("keep = %v, want 2494 kobo", b.KeepKobo)
	}
}
