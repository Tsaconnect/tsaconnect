package services

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func (r *poolSettleRig) legs(t *testing.T, intentID any) []models.RampLeg {
	t.Helper()
	var legs []models.RampLeg
	r.db.Where("order_id = ?", intentID).Find(&legs)
	return legs
}

// S37: the release starts only after winning payment_received → delivering.
func TestPoolSettle_ClaimDeliveryCASIsTheOnlyWayIn(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusDelivering && got.Status != models.ITStatusSettled {
		t.Fatalf("status = %s", got.Status)
	}
	if len(r.sender.sends) != 1 {
		t.Fatalf("sends = %d, want one release", len(r.sender.sends))
	}
}

// §12.4: every slice released through Send on the pool chain, one at a time,
// never a direct SignAndBroadcast.
func TestPoolSettle_ReleasesEverySliceThroughSend(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	intent := r.openPaid(t, "100000") // the §10.2 split: 60 + 12.419593
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.sender.sends) != 2 || r.sender.sends[0] != poolTestChain || r.sender.sends[1] != poolTestChain {
		t.Fatalf("sends = %v, want two on the pool chain", r.sender.sends)
	}
	for _, sl := range r.slicesOf(t, intent.ID) {
		if sl.Status != models.P2PTradeStatusReleased || !strings.HasPrefix(sl.ReleaseTxHash, "0x") {
			t.Fatalf("slice %s = %s %q, want released with its hash", sl.ID, sl.Status, sl.ReleaseTxHash)
		}
	}
}

// Roadmap risk 2: delivery waits until EVERY release is 5 deep.
func TestPoolSettle_DeliverOnlyAfterAllReleases5Deep(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	r.chain.depth = 3 // 4 deep
	intent := r.openPaid(t, "100000")
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(r.legs(t, intent.ID)); n != 0 {
		t.Fatalf("legs = %d at 4 deep, want none", n)
	}
	r.chain.depth = 4
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatal(err)
	}
	legs := r.legs(t, intent.ID)
	if len(legs) != 1 || legs[0].Amount != usdt18Units(t, intent.AssetAmount) {
		t.Fatalf("legs = %+v, want one sale of her AssetAmount in 18 dp", legs)
	}
}

// R3: the proof is per receipt; a release that paid the treasury short is
// never counted, however much the treasury holds.
func TestPoolSettle_ProofNeverReadsABalance(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	_ = r.advance(t, intent.ID)
	sl := r.slicesOf(t, intent.ID)[0]
	r.verifier.credited[strings.ToLower(sl.ReleaseTxHash)] = big.NewInt(1) // the receipt shows 1 wei
	r.db.Exec("DELETE FROM ramp_legs")
	_ = r.advance(t, intent.ID)
	if n := len(r.legs(t, intent.ID)); n != 0 {
		t.Fatalf("legs = %d, want none: the release proved 1 wei, the sale wallet's 1,000 USDT is not proof", n)
	}
}

// Never pay her twice: one live sale leg however many ticks run.
func TestPoolSettle_OneLiveSaleLeg(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	for i := 0; i < 3; i++ {
		_ = r.advance(t, intent.ID)
	}
	if n := len(r.legs(t, intent.ID)); n != 1 || r.saleSigner.broadcasts() != 1 {
		t.Fatalf("legs=%d sends=%d, want exactly one", n, r.saleSigner.broadcasts())
	}
}

// The sale settles 5 deep on its own receipt, then the intent is settled.
func TestPoolSettle_DirectSettlesOnTheSaleProof(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	_ = r.advance(t, intent.ID) // releases, proves, sends
	legs := r.legs(t, intent.ID)
	want, _ := new(big.Int).SetString(legs[0].Amount, 10)
	r.saleVerifier.err, r.saleVerifier.credited = nil, want
	r.db.Model(&models.RampLeg{}).Where("id = ?", legs[0].ID).Update("tx_hash", "0xsale")
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("status = %s, want settled", got.Status)
	}
}

// Review C1 / S18 in the watcher: one slice released, the other cancelled by
// anyone after 60 min — R no longer covers her: park, drop, alert.
func TestPoolReeval_PartialReleaseThenCancel(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	intent := r.openPaid(t, "100000")
	sls := r.slicesOf(t, intent.ID)
	r.chain.setTrade(HashID(sls[1].ID), OnChainP2PStatusCancelled) // before our release reaches it
	_ = r.advance(t, intent.ID)
	got := r.intent(t, intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate || !strings.HasPrefix(got.FailureReason, models.ITFailurePoolSlicesShort) {
		t.Fatalf("intent = %s %q, want parked pool_slices_short", got.Status, got.FailureReason)
	}
	if n := len(r.legs(t, intent.ID)); n != 0 {
		t.Fatal("a short order is never delivered")
	}
}

// §12.4: a cancelled slice under a paid order is re-evaluated; still covered
// (S18 priced her below the locked units) → the order carries on.
func TestPoolLate_SliceCancelledUnderPaidOrderReevaluates(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	intent := r.openPaid(t, "100000")
	r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("asset_amount", "59000000") // a live re-price below slice A
	sls := r.slicesOf(t, intent.ID)
	r.chain.setTrade(HashID(sls[1].ID), OnChainP2PStatusCancelled)
	_ = r.advance(t, intent.ID)
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s, want delivering on slice A alone", got.Status)
	}
	if len(r.legs(t, intent.ID)) != 1 {
		t.Fatal("covered by R: delivered")
	}
}

// F22 on a paid order: the LP released it themselves — counts once proven,
// with the hash found from the escrow's TradeReleased log.
func TestPoolSlice_SelfReleasePaidIsProven(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	sl := r.slicesOf(t, intent.ID)[0]
	id := HashID(sl.ID)
	r.chain.setTrade(id, OnChainP2PStatusReleased)
	self := common.HexToHash("0x" + strings.Repeat("5e", 32))
	r.chain.receipts[self] = r.chain.head
	r.verifier.credited[strings.ToLower(self.Hex())] = r.chain.trades[id].Amount
	r.desk.c.Probe = &logProbe{TreasuryTxProbe: r.desk.c.Probe, logs: []types.Log{{BlockNumber: r.chain.head, TxHash: self,
		Address: common.HexToAddress(poolTestEscrow), Topics: []common.Hash{topicTradeReleased, common.BytesToHash(id[:])}}}}
	_ = r.advance(t, intent.ID)
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if !strings.EqualFold(got.ReleaseTxHash, self.Hex()) || len(r.sender.sends) != 0 || len(r.legs(t, intent.ID)) != 1 {
		t.Fatalf("hash=%s sends=%d legs=%d, want the self-release found, no second release, delivered",
			got.ReleaseTxHash, len(r.sender.sends), len(r.legs(t, intent.ID)))
	}
}

// §12.6 converted: handoff + settled in one transaction, only after every
// release is proven; the record's hash is the first release.
func TestPoolConverted_HandoffAfterAllSlicesSameTxSettles(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	r.it.SetConvertedBuys(ConvertedBuys{TreasuryAddress: poolTestTreasury, Treasury: r.desk.c.Treasury})
	r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
	p := r.price(t, "5000")
	leg, _ := CanonicalToChainUnits(p.Quote.AssetUnits, 18)
	v := PaycrestVenue{Kind: models.RouteKindConverted, ChainKey: "bsc", Token: "USDT", MarkupBps: 100, LegDecimals: 18,
		RequestedChain: "base", RequestedToken: "USDC", TokenAmountBaseUnits: ConversionOwed(leg, 100).String()}
	intent, _, _, err := r.it.OpenPoolConvertedBuy(context.Background(), r.userID, p, ConvertedOpen{Venue: v, WalletAddress: testRoutedEVM})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_conv", intent.AmountMinor, time.Now(), false, PayInCentral); !ok || err != nil {
		t.Fatalf("claim %v %v", ok, err)
	}
	r.chain.depth = 3
	_ = r.advance(t, intent.ID)
	var rec models.PaycrestOrderRecord
	r.db.First(&rec, "reference = ?", ITConversionRef(intent.ID))
	if rec.TxHash != "" {
		t.Fatal("handed off before the release was 5 deep")
	}
	r.chain.depth = 4
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatal(err)
	}
	r.db.First(&rec, "reference = ?", ITConversionRef(intent.ID))
	sl := r.slicesOf(t, intent.ID)[0]
	got := r.intent(t, intent.ID)
	if got.Status != models.ITStatusSettled || !strings.EqualFold(rec.TxHash, sl.ReleaseTxHash) || len(r.legs(t, intent.ID)) != 0 {
		t.Fatalf("intent=%s rec.tx=%s slice.tx=%s legs=%d, want settled + handed off on the release, no sale",
			got.Status, rec.TxHash, sl.ReleaseTxHash, len(r.legs(t, intent.ID)))
	}
}

// F10: a paid order whose release has no proof for 10 minutes is a stall.
func TestPoolSettle_StalledAfterTenMinutes(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	r.sender.noMine = true
	_ = r.advance(t, intent.ID)
	r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("updated_at", time.Now().Add(-11*time.Minute))
	if err := r.advance(t, intent.ID); !errors.Is(err, ErrPoolReleaseStalled) {
		t.Fatalf("err = %v, want ErrPoolReleaseStalled", err)
	}
}

// Honour window: an unproven slice of a paid order within 10 min of its
// slice window raises an alert (anyone may cancelByTimeout it after).
func TestPoolSettle_ReleaseNearWindowAlerts(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	r.sender.noMine = true
	sl := r.slicesOf(t, intent.ID)[0]
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("expires_at", time.Now().Add(5*time.Minute))
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	_ = r.advance(t, intent.ID)
	if !strings.Contains(buf.String(), "ALERT liquidity_pool_release_near_window") {
		t.Fatalf("no near-window alert:\n%s", buf.String())
	}
}

// Money rule: a slice the order dropped is never released to the treasury,
// paid or not — its LP's USDT is not hers.
func TestPoolSettle_DroppedPaidSliceIsNeverReleased(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	sl := r.slicesOf(t, intent.ID)[0]
	r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusPaid)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Updates(map[string]any{"status": models.P2PTradeStatusPaid, "dropped_at": time.Now()})
	if _, err := r.trades.AutoRelease(context.Background(), sl.ID, r.sender); !errors.Is(err, ErrSliceReleaseInFlight) {
		t.Fatalf("err = %v, want the flip refused", err)
	}
	if len(r.sender.sends) != 0 {
		t.Fatalf("sends = %d, want none for a dropped slice", len(r.sender.sends))
	}
}

// P1 / review C-1: a broadcast release (releasing, or a recorded hash) is
// never dropped by a re-evaluation.
func TestPoolSettle_DropNeverTakesABroadcastRelease(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	intent := r.openPaid(t, "100000")
	sls := r.slicesOf(t, intent.ID)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sls[0].ID).Update("status", models.P2PTradeStatusReleasing)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sls[1].ID).Updates(map[string]any{"status": models.P2PTradeStatusPaid, "release_tx_hash": "0xabc"})
	if dropped, err := r.desk.dropSlices(r.db, []uuid.UUID{sls[0].ID, sls[1].ID}, time.Now()); err != nil || len(dropped) != 0 {
		t.Fatalf("dropped=%v err=%v, want none", dropped, err)
	}
	for _, sl := range r.slicesOf(t, intent.ID) {
		if sl.DroppedAt != nil {
			t.Fatalf("slice %s (%s %q) dropped, want kept at the treasury", sl.ID, sl.Status, sl.ReleaseTxHash)
		}
	}
}

// Never deliver and also refund: once her sale leg is live, a lost slice
// never parks the order (a park opens the refund).
func TestPoolReeval_NeverParksWithALiveSale(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	_ = r.advance(t, intent.ID) // released, proven, sale leg sent
	if len(r.legs(t, intent.ID)) != 1 {
		t.Fatal("setup: no sale leg")
	}
	r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("asset_amount", "999000000") // R no longer covers
	parked, err := r.it.reevaluatePoolOrder(context.Background(), r.desk, intent.ID)
	if err != nil || parked {
		t.Fatalf("parked=%v err=%v, want not parked", parked, err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s, want delivering", got.Status)
	}
}

// Rule: the release decision is taken under the pool lock on the order's
// current state — never for an order that left delivering or a dropped slice.
func TestPoolSettle_ReleaseDecisionReadsTheLockedState(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	sl := r.slicesOf(t, intent.ID)[0]
	r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("status", models.ITStatusDelivering)
	ctx := context.Background()
	if ok, err := r.it.decidePoolRelease(ctx, r.desk, intent.ID, sl.ID); !ok || err != nil {
		t.Fatalf("live slice: %v %v, want release", ok, err)
	}
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("dropped_at", time.Now())
	if ok, _ := r.it.decidePoolRelease(ctx, r.desk, intent.ID, sl.ID); ok {
		t.Fatal("dropped slice: released")
	}
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("dropped_at", nil)
	r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("status", models.ITStatusBuyAwaitingRate)
	if ok, _ := r.it.decidePoolRelease(ctx, r.desk, intent.ID, sl.ID); ok {
		t.Fatal("parked order: released")
	}
}

// usdt18Units is canonical units in 18-dp chain units, as a string.
func usdt18Units(t *testing.T, canonical string) string {
	t.Helper()
	u, _ := new(big.Int).SetString(canonical, 10)
	c, err := CanonicalToChainUnits(u, 18)
	if err != nil {
		t.Fatal(err)
	}
	return c.String()
}
