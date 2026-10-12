package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// parkedShort pays a 2-slice order, releases A, loses B to cancelByTimeout,
// and lets the reconciler mark B cancelled: parked pool_slices_short.
func parkedShort(t *testing.T, r *poolSettleRig) *models.ITIntent {
	t.Helper()
	r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
	intent := r.openPaid(t, "100000")
	b := r.slicesOf(t, intent.ID)[1]
	r.chain.setTrade(HashID(b.ID), OnChainP2PStatusCancelled)
	_ = r.advance(t, intent.ID)
	if _, err := r.trades.ReconcileTrade(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	return intent
}

// F6 / S41 / review C1: owed = live − released; generation 2 allocates the
// owed units only, to an LP not already in the order.
func TestPoolRedeliver_CountsReleasedSlices(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45, 30)
	intent := parkedShort(t, r)
	got, err := r.it.RedeliverPoolBuy(context.Background(), uuid.New(), intent.ID, "re-deliver after a lost slice")
	if err != nil || got.Status != models.ITStatusPaymentReceived {
		t.Fatalf("redeliver = %v %v", got, err)
	}
	var gen2 []models.P2PTrade
	r.db.Where("it_intent_id = ? AND generation = 2", intent.ID).Find(&gen2)
	var snaps int64
	r.db.Model(&models.PoolOrder{}).Where("intent_id = ?", intent.ID).Count(&snaps)
	if len(gen2) != 1 || snaps != 2 || r.desk.toCanonical(gen2[0].TokenAmount).Cmp(big.NewInt(12_419_593)) != 0 {
		t.Fatalf("gen2 = %+v snaps=%d, want one 12.419593 slice and a second snapshot (generation 1 kept)", gen2, snaps)
	}
	var lpAd models.P2PAd
	r.db.First(&lpAd, "id = ?", gen2[0].AdID)
	if *lpAd.PMID != r.lps[2].ID {
		t.Fatal("the re-delivery must go to the LP not already in the order")
	}
}

// S41 as the spec words it (review I-6): an LP holding a RELEASED slice is
// excluded; the LP whose slice was cancelled is eligible again.
func TestPoolRedeliver_ExcludesLPsHoldingAReleasedSlice(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	intent := parkedShort(t, r)
	var aAd models.P2PAd
	r.db.First(&aAd, "id = ?", r.slicesOf(t, intent.ID)[0].AdID)
	r.esc.setBalance(aAd.SellerAddress, usdt18(50), new(big.Int))                                                           // A tops up: stock, but A released a slice here
	r.db.Model(&models.PaymentMerchant{}).Where("id = ?", r.lps[1].ID).Update("status", models.PaymentMerchantStatusPaused) // B out
	if _, err := r.it.RedeliverPoolBuy(context.Background(), uuid.New(), intent.ID, "note"); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("err = %v, want ErrPoolCannotFill: A is excluded (released slice), B paused", err)
	}
}

// Review I-6: the S37 override is the exit when re-delivery cannot fill —
// only once every at-treasury slice's LP has been forwarded.
func TestPoolRefundOverride_AfterTheLPsArePaid(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	r.wireForwards(t)
	intent := parkedShort(t, r)
	if _, err := r.it.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "note"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("plain refund = %v, want refused (a slice is at the treasury)", err)
	}
	if _, err := r.it.CompletePoolBooks(context.Background()); err != nil { // back-fills A's forward if the tick missed it
		t.Fatal(err)
	}
	got, err := r.it.RefundPoolBuyOverride(context.Background(), uuid.New(), intent.ID, "owed below the minimum slice; refunded")
	if err != nil || got.Status != models.ITStatusRefunded || len(r.forwards(t)) != 1 {
		t.Fatalf("override = %v %v forwards=%d, want refunded with LP A paid once", got, err, len(r.forwards(t)))
	}
}

// Spec F6: allowed only once every earlier slice is released or cancelled.
func TestPoolRedeliver_RefusedWhileASliceIsLive(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45, 30)
	r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
	intent := r.openPaid(t, "100000")
	r.chain.setTrade(HashID(r.slicesOf(t, intent.ID)[1].ID), OnChainP2PStatusCancelled)
	_ = r.advance(t, intent.ID) // parked; B dropped but its row is not yet reconciled
	r.db.Model(&models.P2PTrade{}).Where("id = ?", r.slicesOf(t, intent.ID)[1].ID).Update("status", models.P2PTradeStatusPendingPayment)
	if _, err := r.it.RedeliverPoolBuy(context.Background(), uuid.New(), intent.ID, "note"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want refused until B is terminal", err)
	}
}

// F22: an operator pays an LP whose self-release landed on an unpaid order —
// once, through the forwarder, audited.
func TestPoolSlice_F22OperatorPaysLPOnce(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	fwd, gw, _ := r.wireForwards(t)
	_ = fwd
	intent, _, err := r.open(t, "5000")
	if err != nil {
		t.Fatal(err)
	}
	sl := r.slicesOf(t, intent.ID)[0]
	id := HashID(sl.ID)
	r.chain.setTrade(id, OnChainP2PStatusReleased)
	self := releaseHashOf(id)
	r.chain.receipts[self] = r.chain.head
	r.sender.receipts[self] = releaseReceipt(id, common.HexToAddress(poolTestTreasury), r.chain.trades[id].Amount, r.chain.head)
	r.verifier.credited[strings.ToLower(self.Hex())] = r.chain.trades[id].Amount
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("release_tx_hash", self.Hex())
	if _, err := r.trades.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, _ = r.it.PayPoolSliceLP(context.Background(), uuid.New(), *sl.ITIntentID, sl.ID, "paid at the snapshot rate per policy")
	}
	if len(gw.calls) != 1 || len(r.forwards(t)) != 1 {
		t.Fatalf("calls=%d forwards=%d, want exactly one", len(gw.calls), len(r.forwards(t)))
	}
}

// Review I-5: once the USDT was handed back, the LP is never also paid naira.
func TestPoolSlice_F22ReturnedThenPayRefused(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	_, gw, _ := r.wireForwards(t)
	intent, _, err := r.open(t, "5000")
	if err != nil {
		t.Fatal(err)
	}
	sl := r.slicesOf(t, intent.ID)[0]
	r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusReleased)
	if _, err := r.trades.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.it.NotePoolSliceReturned(context.Background(), uuid.New(), *sl.ITIntentID, sl.ID, "0xreturn", "sent back by hand"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.it.PayPoolSliceLP(context.Background(), uuid.New(), *sl.ITIntentID, sl.ID, "pay too?"); err == nil || len(gw.calls) != 0 {
		t.Fatalf("pay after return = %v calls=%d, want refused", err, len(gw.calls))
	}
}

// Re-review I-B (a): a returned slice no longer counts at the treasury, so a
// late payment on that order has a working exit — here the plain refund.
func TestPoolSlice_F22ReturnedThenLatePaymentHasAnExit(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	r.wireForwards(t)
	r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
	intent, _, err := r.open(t, "5000")
	if err != nil {
		t.Fatal(err)
	}
	sl := r.slicesOf(t, intent.ID)[0]
	r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusReleased)
	_, _ = r.trades.ReconcileTrade(context.Background(), sl.ID)
	if err := r.it.NotePoolSliceReturned(context.Background(), uuid.New(), *sl.ITIntentID, sl.ID, "0xreturn", "sent back"); err != nil {
		t.Fatal(err)
	}
	// A late payment: nothing backs her order any more → parked short.
	late := time.Now().Add(2 * time.Hour)
	cur := r.intent(t, intent.ID)
	if ok, err := r.it.claimAndPriceStableBuy(context.Background(), &cur, "sess_late_f22", cur.AmountMinor, late, false, PayInCentral); !ok || err != nil {
		t.Fatalf("late claim %v %v", ok, err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("status = %s, want parked short", got.Status)
	}
	if _, err := r.it.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "returned in naira"); err != nil {
		t.Fatalf("refund = %v, want allowed: no slice is at the treasury", err)
	}
	if n := len(r.forwards(t)); n != 0 {
		t.Fatal("the returned slice must never be forwarded")
	}
}

// Re-review I-B (b): "returned" is refused once the order holds her naira
// (delivering here), so her delivery never rests on USDT handed back.
func TestPoolSlice_F22ReturnedRefusedWhileTheOrderHoldsMoney(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	r.sender.noMine = true
	intent := r.openPaid(t, "5000")
	_ = r.advance(t, intent.ID) // delivering
	sl := r.slicesOf(t, intent.ID)[0]
	if err := r.it.NotePoolSliceReturned(context.Background(), uuid.New(), *sl.ITIntentID, sl.ID, "0xreturn", "note"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("returned on a delivering order = %v, want refused", err)
	}
}

// Controller item 2 / review I-5: an LP whose slice was already forwarded
// naira (here automatically, before an override refund made the order
// terminal) is never also handed the USDT back.
func TestPoolSlice_F22ReturnedRefusedOnceTheLPWasForwarded(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	r.wireForwards(t)
	intent := parkedShort(t, r)
	if _, err := r.it.CompletePoolBooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.it.RefundPoolBuyOverride(context.Background(), uuid.New(), intent.ID, "owed below the minimum slice"); err != nil {
		t.Fatalf("override: %v", err)
	}
	a := r.slicesOf(t, intent.ID)[0]
	if len(r.forwards(t)) != 1 || a.CancelReason != "" {
		t.Fatalf("setup: forwards=%d cancel_reason=%q, want A forwarded with no F22 marker", len(r.forwards(t)), a.CancelReason)
	}
	err := r.it.NotePoolSliceReturned(context.Background(), uuid.New(), *a.ITIntentID, a.ID, "0xreturn", "sent back by hand")
	if !errors.Is(err, ErrITBadState) {
		t.Fatalf("returned after a forward = %v, want refused", err)
	}
	if got := r.slicesOf(t, intent.ID)[0]; got.CancelReason != "" || got.DroppedAt != nil {
		t.Fatalf("slice = %q dropped=%v, want untouched", got.CancelReason, got.DroppedAt)
	}
}

// Controller item 3: re-delivery reuses the order's one conversion record
// (reference is unique per intent), so the hand-off after generation 2 names
// that record and converted custody finds no other record naming any of the
// order's releases — no false recovery after an F6 re-delivery.
func TestPoolRedeliver_ConvertedKeepsItsOneRecordForCustody(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45, 30)
	ctx := context.Background()
	r.it.SetConvertedBuys(ConvertedBuys{TreasuryAddress: poolTestTreasury, Treasury: r.desk.c.Treasury})
	r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
	p := r.price(t, "100000")
	leg, _ := CanonicalToChainUnits(p.Quote.AssetUnits, 18)
	v := PaycrestVenue{Kind: models.RouteKindConverted, ChainKey: "bsc", Token: "USDT", MarkupBps: 100, LegDecimals: 18,
		RequestedChain: "base", RequestedToken: "USDC", TokenAmountBaseUnits: ConversionOwed(leg, 100).String()}
	intent, _, rec0, err := r.it.OpenPoolConvertedBuy(ctx, r.userID, p, ConvertedOpen{Venue: v, WalletAddress: testRoutedEVM})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := r.it.claimAndPriceStableBuy(ctx, intent, "sess_conv_f6", intent.AmountMinor, time.Now(), false, PayInCentral); !ok || err != nil {
		t.Fatalf("claim %v %v", ok, err)
	}
	b := r.slicesOf(t, intent.ID)[1]
	r.chain.setTrade(HashID(b.ID), OnChainP2PStatusCancelled)
	_ = r.advance(t, intent.ID)
	if _, err := r.trades.ReconcileTrade(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("setup: status %s, want parked short", got.Status)
	}
	if _, err := r.it.RedeliverPoolBuy(ctx, uuid.New(), intent.ID, "re-deliver the owed units"); err != nil {
		t.Fatalf("redeliver: %v", err)
	}
	for i := 0; i < 3 && r.intent(t, intent.ID).Status != models.ITStatusSettled; i++ {
		_ = r.advance(t, intent.ID)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("status = %s, want settled (handed off) after re-delivery", got.Status)
	}
	var recs []models.PaycrestOrderRecord
	r.db.Where("reference = ?", ITConversionRef(intent.ID)).Find(&recs)
	a := r.slicesOf(t, intent.ID)[0]
	if len(recs) != 1 || recs[0].ID != rec0.ID || !strings.EqualFold(recs[0].TxHash, a.ReleaseTxHash) {
		t.Fatalf("records=%d, want the one original record handed off on A's release", len(recs))
	}
	o := &ConversionOrchestrator{db: r.db}
	hashes := 0
	for _, sl := range r.slicesOf(t, intent.ID) {
		if sl.ReleaseTxHash == "" || sl.Status == models.P2PTradeStatusCancelled {
			continue
		}
		hashes++
		others, err := o.recordsNamingDelivery(ctx, &recs[0], sl.ReleaseTxHash, false)
		if err != nil || len(others) != 0 {
			t.Fatalf("release %s named by %d other records (err %v), want none: custody would go to recovery", sl.ReleaseTxHash, len(others), err)
		}
	}
	if hashes != 2 {
		t.Fatalf("releases = %d, want A (generation 1) and the re-delivered slice (generation 2)", hashes)
	}
}

// A slice of a re-delivery that never broadcast is re-picked in the
// re-delivery's own generation: its LP is paid at that generation's snapshot
// rate, never generation 1's (which would refuse the forward as a mismatch).
func TestPoolRedeliver_RepickStaysInItsGeneration(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45, 30)
	intent := parkedShort(t, r)
	d, _ := r.addLP(t, 60) // the largest share: generation 2 picks it first
	r.chain.prepareErr[walletOf(d)] = errors.New("execution reverted: SellerInsufficient")
	if _, err := r.it.RedeliverPoolBuy(context.Background(), uuid.New(), intent.ID, "note"); err != nil {
		t.Fatalf("redeliver = %v, want the re-pick to fill it", err)
	}
	var later []models.P2PTrade
	r.db.Where("it_intent_id = ? AND created_at > ?", intent.ID, r.slicesOf(t, intent.ID)[1].CreatedAt).Find(&later)
	if len(later) != 2 {
		t.Fatalf("re-delivery slices = %d, want the failed one and its re-pick", len(later))
	}
	for _, sl := range later {
		if sl.Generation != 2 {
			t.Fatalf("slice %s (%s) generation = %d, want 2", sl.ID, sl.Status, sl.Generation)
		}
	}
}

// Two re-deliveries decided on the same parked order: the second, decided on
// the slices before the first opened generation 2, never opens another
// generation for the same owed units (the LPs would be paid for USDT she is
// not owed).
func TestPoolRedeliver_StaleDecisionIsRefused(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45, 30)
	intent := parkedShort(t, r)
	seen := poolSliceStates(r.slicesOf(t, intent.ID))
	if err := slicesUnchanged(r.db, intent.ID, seen, nil); err != nil {
		t.Fatalf("unchanged = %v", err)
	}
	if _, err := r.it.RedeliverPoolBuy(context.Background(), uuid.New(), intent.ID, "first"); err != nil {
		t.Fatal(err)
	}
	if err := slicesUnchanged(r.db, intent.ID, seen, nil); !errors.Is(err, ErrITBadState) {
		t.Fatalf("stale decision = %v, want refused: generation 2 exists", err)
	}
}

// Review m-2: the F22 pay-LP exit is refused for a slice that still backs a
// paid order — its LP is the automatic forward's, never an operator's.
func TestPoolSlice_F22PayRefusedWhileTheSliceBacksAPaidOrder(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	r.sender.noMine = true
	intent := r.openPaid(t, "5000")
	_ = r.advance(t, intent.ID) // delivering, its release broadcast
	sl := r.slicesOf(t, intent.ID)[0]
	_, gw, _ := r.wireForwards(t) // wired after the tick: only the operator call could pay
	id := HashID(sl.ID)
	r.chain.setTrade(id, OnChainP2PStatusReleased) // the release lands
	h := common.HexToHash(sl.ReleaseTxHash)
	r.chain.receipts[h] = r.chain.head
	r.sender.receipts[h] = releaseReceipt(id, common.HexToAddress(poolTestTreasury), r.chain.trades[id].Amount, r.chain.head)
	r.verifier.credited[strings.ToLower(h.Hex())] = r.chain.trades[id].Amount
	if _, err := r.it.PayPoolSliceLP(context.Background(), uuid.New(), *sl.ITIntentID, sl.ID, "pay by hand"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("pay-lp on a delivering order's slice = %v, want refused", err)
	}
	var cur models.P2PTrade
	r.db.First(&cur, "id = ?", sl.ID)
	if cur.CancelReason != "" || len(gw.calls) != 0 {
		t.Fatalf("cancel_reason=%q calls=%d, want no F22 marker and no transfer", cur.CancelReason, len(gw.calls))
	}
}

// Review m-8: a slice action names its order; a slice of another order (or
// an order id that is not its own) is not found — never acted on.
func TestPoolSlice_F22ActionsRefuseAnotherOrdersSlice(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	_, gw, _ := r.wireForwards(t)
	intent, _, err := r.open(t, "5000")
	if err != nil {
		t.Fatal(err)
	}
	sl := r.slicesOf(t, intent.ID)[0]
	id := HashID(sl.ID)
	r.chain.setTrade(id, OnChainP2PStatusReleased)
	self := releaseHashOf(id)
	r.chain.receipts[self] = r.chain.head
	r.sender.receipts[self] = releaseReceipt(id, common.HexToAddress(poolTestTreasury), r.chain.trades[id].Amount, r.chain.head)
	r.verifier.credited[strings.ToLower(self.Hex())] = r.chain.trades[id].Amount
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("release_tx_hash", self.Hex())
	if _, err := r.trades.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatal(err)
	}
	other := uuid.New()
	if _, err := r.it.PayPoolSliceLP(context.Background(), uuid.New(), other, sl.ID, "wrong order"); !errors.Is(err, ErrTradeNotFound) {
		t.Fatalf("pay-lp under another order = %v, want not found", err)
	}
	if err := r.it.NotePoolSliceReturned(context.Background(), uuid.New(), other, sl.ID, "0xreturn", "wrong order"); !errors.Is(err, ErrTradeNotFound) {
		t.Fatalf("returned under another order = %v, want not found", err)
	}
	var cur models.P2PTrade
	r.db.First(&cur, "id = ?", sl.ID)
	if len(gw.calls) != 0 || cur.CancelReason != "" {
		t.Fatalf("calls=%d cancel_reason=%q, want nothing done", len(gw.calls), cur.CancelReason)
	}
}

// stuckRelease pays a one-slice order whose release was broadcast but never
// mined (dropped from the mempool), past the reconciler's releasing window.
func stuckRelease(t *testing.T, r *poolSettleRig) (*models.ITIntent, models.P2PTrade) {
	t.Helper()
	r.sender.noMine = true
	intent := r.openPaid(t, "5000")
	_ = r.advance(t, intent.ID)
	sl := r.slicesOf(t, intent.ID)[0]
	if sl.Status != models.P2PTradeStatusReleasing || sl.ReleaseTxHash == "" {
		t.Fatalf("setup: slice %s %q, want releasing with a hash", sl.Status, sl.ReleaseTxHash)
	}
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("updated_at", time.Now().Add(-releasingStaleAfter-time.Minute))
	r.db.First(&sl, "id = ?", sl.ID)
	r.chain.latest = r.chain.nextNonce // the release key is idle
	return intent, sl
}

// Review m-5: a release dropped from the mempool is cleared by a super admin
// on the chain's evidence, and the settle engine releases the slice again.
func TestPoolSlice_ClearStuckReleaseOnEvidence(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent, sl := stuckRelease(t, r)
	ctx := context.Background()
	if err := r.it.ClearPoolSliceRelease(ctx, uuid.New(), intent.ID, sl.ID, "dropped from the mempool"); err != nil {
		t.Fatal(err)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusPaid || got.ReleaseTxHash != "" {
		t.Fatalf("slice = %s %q, want paid with no hash", got.Status, got.ReleaseTxHash)
	}
	r.sender.noMine = false
	_ = r.advance(t, intent.ID)
	if len(r.sender.sends) != 2 || len(r.legs(t, intent.ID)) != 1 {
		t.Fatalf("sends=%d legs=%d, want one more release and delivered", len(r.sender.sends), len(r.legs(t, intent.ID)))
	}
}

// Review m-5: never cleared while the release can still land or has landed.
func TestPoolSlice_ClearStuckReleaseRefusedWithoutEvidence(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(*poolSettleRig, *models.P2PTrade){
		"inside the window": func(r *poolSettleRig, sl *models.P2PTrade) {
			r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("updated_at", time.Now())
		},
		"release key busy": func(r *poolSettleRig, _ *models.P2PTrade) { r.chain.latest = r.chain.nextNonce - 1 },
		"landed": func(r *poolSettleRig, sl *models.P2PTrade) { r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusReleased) },
		"logged": func(r *poolSettleRig, sl *models.P2PTrade) {
			id := HashID(sl.ID)
			r.desk.c.Probe = &logProbe{TreasuryTxProbe: r.desk.c.Probe, logs: []types.Log{{BlockNumber: r.chain.head, TxHash: common.HexToHash("0x02"),
				Address: common.HexToAddress(poolTestEscrow), Topics: []common.Hash{topicTradeReleased, common.BytesToHash(id[:])}}}}
		},
	}
	for name, spoil := range cases {
		r := newPoolSettleRig(t, 50)
		intent, sl := stuckRelease(t, r)
		spoil(r, &sl)
		if err := r.it.ClearPoolSliceRelease(ctx, uuid.New(), intent.ID, sl.ID, "note"); !errors.Is(err, ErrITBadState) {
			t.Fatalf("%s: err = %v, want refused", name, err)
		}
		var got models.P2PTrade
		r.db.First(&got, "id = ?", sl.ID)
		if got.Status != models.P2PTradeStatusReleasing || got.ReleaseTxHash != sl.ReleaseTxHash {
			t.Fatalf("%s: slice = %s %q, want untouched", name, got.Status, got.ReleaseTxHash)
		}
	}
}
