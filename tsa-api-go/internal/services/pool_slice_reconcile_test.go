package services

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func releaseSender() *chainCountingSender {
	return &chainCountingSender{fakeReleaseSigner: fakeReleaseSigner{addr: "0x00000000000000000000000000000000000000a1",
		hash: "0x" + strings.Repeat("e1", 32)}}
}

// openOne opens a ₦5,000 order on one LP and wires the slice hooks.
func openOne(t *testing.T) (*poolOpenRig, *models.ITIntent, models.P2PTrade, *models.PaymentMerchant, *chainCountingSender) {
	t.Helper()
	r := newPoolOpenRig(t)
	lp, _ := r.addLP(t, 50)
	intent, _, err := r.open(t, "5000")
	if err != nil {
		t.Fatal(err)
	}
	sender := releaseSender()
	sender.onSent = r.chain.sent
	r.trades.SetPoolSlices(r.it, sender)
	return r, intent, r.slicesOf(t, intent.ID)[0], lp, sender
}

// F21: an LP disputes a slice of a PAID order → resolved to the treasury,
// LP paused, no P2P dispute row, through the release key's Send.
func TestPoolSlice_DisputeAutoResolvesToTreasuryWhenPaid(t *testing.T) {
	r, intent, sl, lp, sender := openOne(t)
	ctx := context.Background()
	if ok, err := r.it.claimAndPriceStableBuy(ctx, intent, "sess_paid", intent.AmountMinor, time.Now(), false, PayInCentral); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusDisputed)
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.chain.resolves) != 1 || !r.chain.resolves[0] || len(sender.sendChains) != 1 || sender.sendChains[0] != poolTestChain || sender.direct != 0 {
		t.Fatalf("resolves=%v sends=%v direct=%d, want one Send releasing to the treasury", r.chain.resolves, sender.sendChains, sender.direct)
	}
	var n int64
	r.db.Model(&models.P2PDispute{}).Count(&n)
	var lpRow models.PaymentMerchant
	r.db.First(&lpRow, "id = ?", lp.ID)
	if n != 0 || lpRow.Status != models.PaymentMerchantStatusPaused {
		t.Fatalf("disputes=%d lp=%s, want no dispute row and the LP paused", n, lpRow.Status)
	}
	// The next pass sees Resolved and reads the outcome from the resolve receipt.
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil {
		t.Fatal(err)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusResolved || got.DroppedAt != nil {
		t.Fatalf("slice = %+v, want resolved and still backing the order", got)
	}
}

// F21: unpaid → back to the LP; the slice no longer backs the order.
func TestPoolSlice_DisputeAutoResolvesToLPWhenUnpaid(t *testing.T) {
	r, _, sl, _, _ := openOne(t)
	ctx := context.Background()
	r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusDisputed)
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.chain.resolves) != 1 || r.chain.resolves[0] {
		t.Fatalf("resolves = %v, want one back to the LP", r.chain.resolves)
	}
	r.chain.resolvedEvent = &P2PTradeResolvedEvent{TradeID: HashID(sl.ID), ReleasedToBuyer: false}
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil {
		t.Fatal(err)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	var ad models.P2PAd
	r.db.First(&ad, "id = ?", sl.AdID)
	if got.Status != models.P2PTradeStatusResolved || got.DroppedAt == nil || ad.Reserved != "0" {
		t.Fatalf("slice=%+v ad reserved=%s, want resolved, dropped, capacity restored", got, ad.Reserved)
	}
}

// F22: a self-release on an unpaid order is recorded and alerted, never forwarded (C2).
func TestPoolSlice_SelfReleaseUnpaidAlerts(t *testing.T) {
	r, _, sl, _, _ := openOne(t)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusReleased)
	if _, err := r.trades.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "ALERT liquidity_pool_slice_self_released") {
		t.Fatalf("no self-release alert:\n%s", buf.String())
	}
}

// S44 (review I1): an unlanded slice is reclaimed only once it can never
// land — it never had a nonce, or its nonce was consumed by another
// transaction — EVEN WHEN a broadcast hash was recorded (a connection reset,
// a dropped tx). While it may still land it waits, and its row rotates to
// the back of the reconciler's updated_at window so it never starves the
// trades behind it.
func TestReconcileOrphanOpen_SliceNeedsConsumedNonce(t *testing.T) {
	r := newPoolOpenRig(t)
	r.trades.SetPoolSlices(r.it, releaseSender())
	_, ad := r.addLP(t, 50)
	nonce := int64(15)
	old := time.Now().Add(-2 * time.Hour)
	withNonce := seedPoolSlice(t, r.db, sliceSeed{Intent: r.userID, Ad: ad, Units: usdt18(3), Nonce: &nonce, CreatedAt: old})
	unminedHash := "0x" + strings.Repeat("c3", 32)                                                         // broadcast, never mined: no receipt
	r.db.Model(&models.P2PTrade{}).Where("id = ?", withNonce.ID).UpdateColumn("open_tx_hash", unminedHash) // keeps updated_at at the seed time
	noNonce := seedPoolSlice(t, r.db, sliceSeed{Intent: r.userID, Ad: ad, Units: usdt18(3), CreatedAt: old})
	r.chain.latest = 15 // nonce 15 not yet used
	ctx := context.Background()
	r.trades.ReconcileTrade(ctx, withNonce.ID)
	r.trades.ReconcileTrade(ctx, noNonce.ID)
	row := func(id interface{}) models.P2PTrade {
		var t models.P2PTrade
		r.db.First(&t, "id = ?", id)
		return t
	}
	if got := row(withNonce.ID); got.Status != models.P2PTradeStatusPendingPayment || !got.UpdatedAt.After(old) {
		t.Fatalf("in-flight slice = %s updated=%s, want waiting and rotated past its seed time", got.Status, got.UpdatedAt)
	}
	if row(noNonce.ID).Status != models.P2PTradeStatusCancelled {
		t.Fatal("a slice that never had a nonce was not reclaimed")
	}
	// Re-review N5: inside its window too, an unlanded slice rotates.
	youngAt := time.Now().Add(-10 * time.Minute)
	young := seedPoolSlice(t, r.db, sliceSeed{Intent: r.userID, Ad: ad, Units: usdt18(3), Nonce: &nonce, CreatedAt: youngAt})
	r.trades.ReconcileTrade(ctx, young.ID)
	if got := row(young.ID); got.Status != models.P2PTradeStatusPendingPayment || !got.UpdatedAt.After(youngAt) {
		t.Fatalf("in-window slice = %s updated=%s, want waiting and rotated", got.Status, got.UpdatedAt)
	}
	r.chain.latest = 16 // consumed by another transaction: it can never land
	r.trades.ReconcileTrade(ctx, withNonce.ID)
	if got := row(withNonce.ID); got.Status != models.P2PTradeStatusCancelled || got.CancelReason != "reconciled_orphan" {
		t.Fatalf("slice with a dead hash = %s (%s), want reclaimed", got.Status, got.CancelReason)
	}
}

// Review I3: before her delivery, a slice of a PAID order that is cancelled
// on-chain (cancelByTimeout is permissionless after 60 min) or resolved back
// to its LP is money with no stock behind it. That is never silent.
func TestPoolSlice_PaidSliceLostAlerts(t *testing.T) {
	r, intent, sl, _, _ := openOne(t)
	ctx := context.Background()
	if ok, err := r.it.claimAndPriceStableBuy(ctx, intent, "sess_lost", intent.AmountMinor, time.Now(), false, PayInCentral); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusCancelled)
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "ALERT liquidity_pool_paid_slice_lost") || !strings.Contains(buf.String(), "how=cancelled") {
		t.Fatalf("no paid-slice-lost alert:\n%s", buf.String())
	}
}

// receiptProbe is the pool chain with our open's receipt overridden (a
// reverted open), and an optional endpoint failover mid-decision.
type receiptProbe struct {
	*poolOpenChain
	reverted *types.Receipt
	failover bool
}

func (p *receiptProbe) TransactionReceipt(ctx context.Context, h common.Hash) (*types.Receipt, error) {
	if p.failover {
		p.gen.Add(1)
	}
	if p.reverted != nil {
		return p.reverted, nil
	}
	return p.poolOpenChain.TransactionReceipt(ctx, h)
}

// S44 (controller rule): a slice whose trade exists on-chain is opened. A
// consumed nonce, or no receipt for the hash we hold, never makes it "gone".
func TestSliceOpenGone_NeverWhileTheTradeExists(t *testing.T) {
	r, _, sl, _, _ := openOne(t)
	ctx := context.Background()
	if sl.OpenNonce == nil || sl.OpenTxHash == "" {
		t.Fatalf("landed slice has nonce=%v hash=%q", sl.OpenNonce, sl.OpenTxHash)
	}
	r.chain.latest = uint64(*sl.OpenNonce) + 5 // the nonce is long consumed
	if gone, err := r.it.SliceOpenGone(ctx, &sl); gone || err != nil {
		t.Fatalf("landed slice: gone=%v err=%v, want waiting", gone, err)
	}
	other := sl
	other.OpenTxHash = "0x" + strings.Repeat("d4", 32) // a hash with no receipt
	if gone, err := r.it.SliceOpenGone(ctx, &other); gone || err != nil {
		t.Fatalf("trade exists, receipt for another hash missing: gone=%v err=%v, want waiting", gone, err)
	}
}

// S44: a reverted open is gone only once its receipt is poolProveDepth deep,
// the trade is still absent, and the probe never failed over meanwhile.
func TestSliceOpenGone_RevertedOpenMustBeDeep(t *testing.T) {
	r := newPoolOpenRig(t)
	_, ad := r.addLP(t, 50)
	nonce := int64(15)
	sl := seedPoolSlice(t, r.db, sliceSeed{Intent: r.userID, Ad: ad, Units: usdt18(3), Nonce: &nonce})
	sl.OpenTxHash = "0x" + strings.Repeat("c5", 32)
	r.chain.latest = 15 // the nonce read alone would say "wait"
	probe := &receiptProbe{poolOpenChain: r.chain}
	r.desk.c.Probe = probe
	ctx := context.Background()
	head := r.chain.head + r.chain.depth
	probe.reverted = &types.Receipt{Status: types.ReceiptStatusFailed, BlockNumber: new(big.Int).SetUint64(head - poolProveDepth + 1)}
	if gone, err := r.it.SliceOpenGone(ctx, sl); gone || err != nil {
		t.Fatalf("shallow revert: gone=%v err=%v, want waiting", gone, err)
	}
	probe.reverted.BlockNumber = new(big.Int).SetUint64(head - poolProveDepth)
	probe.failover = true
	if gone, _ := r.it.SliceOpenGone(ctx, sl); gone {
		t.Fatal("decided across an endpoint failover")
	}
	probe.failover = false
	if gone, err := r.it.SliceOpenGone(ctx, sl); !gone || err != nil {
		t.Fatalf("deep revert: gone=%v err=%v, want gone", gone, err)
	}
	probe.reverted.Status = types.ReceiptStatusSuccessful
	if gone, _ := r.it.SliceOpenGone(ctx, sl); gone {
		t.Fatal("a mined, successful open was called gone")
	}
}

// Review I3: an orphaned slice of a PAID order is reclaimed loudly.
func TestPoolSlice_PaidOrderOrphanAlerts(t *testing.T) {
	r, intent, _, _, _ := openOne(t)
	ctx := context.Background()
	if ok, err := r.it.claimAndPriceStableBuy(ctx, intent, "sess_orphan", intent.AmountMinor, time.Now(), false, PayInCentral); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	var ad models.P2PAd
	r.db.First(&ad, "id = ?", r.slicesOf(t, intent.ID)[0].AdID)
	orphan := seedPoolSlice(t, r.db, sliceSeed{Intent: intent.ID, Ad: &ad, Units: usdt18(1), CreatedAt: time.Now().Add(-2 * time.Hour)})
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if _, err := r.trades.ReconcileTrade(ctx, orphan.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "ALERT liquidity_pool_paid_slice_lost") || !strings.Contains(buf.String(), "how=never_opened") {
		t.Fatalf("no paid-slice-lost alert:\n%s", buf.String())
	}
}

// Review C-2(a): an LP's dispute resolved back to the LP while the order holds
// her money is never silent — F21 dropped the slice, so SliceOrderPaid (which
// never counts a dropped slice) cannot be what decides this alert.
func TestPoolSlice_ResolvedToLPAfterPaymentAlerts(t *testing.T) {
	r, intent, sl, _, _ := openOne(t)
	ctx := context.Background()
	r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusDisputed)
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil {
		t.Fatal(err)
	}
	// She pays inside the honour window; the order parks short (C-2).
	if ok, err := r.it.claimAndPriceStableBuy(ctx, intent, "sess_after_f21", intent.AmountMinor, time.Now(), false, PayInCentral); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	r.chain.resolvedEvent = &P2PTradeResolvedEvent{TradeID: HashID(sl.ID), ReleasedToBuyer: false}
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "ALERT liquidity_pool_paid_slice_lost") || !strings.Contains(buf.String(), "how=resolved_to_lp") {
		t.Fatalf("no paid-slice-lost alert:\n%s", buf.String())
	}
}

// A slice S18 left unselected is dropped; the canceller freeing it after the
// order is paid is the plan, not a loss: SliceOrderPaid never counts it.
func TestPoolSlice_DroppedSliceCancelledAfterPaymentIsSilent(t *testing.T) {
	r := newPoolOpenRig(t)
	threeLPs(t, r)
	intent, _, err := r.open(t, "100000")
	if err != nil {
		t.Fatal(err)
	}
	r.trades.SetPoolSlices(r.it, releaseSender())
	if ok, err := lateClaim(t, r, intent, 82_500); !ok || err != nil { // slice A alone backs it
		t.Fatalf("claim: %v %v", ok, err)
	}
	var dropped *models.P2PTrade
	for _, sl := range r.slicesOf(t, intent.ID) {
		if sl.DroppedAt != nil {
			dropped = &sl
		}
	}
	if dropped == nil {
		t.Fatal("no unselected slice")
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	r.chain.setTrade(HashID(dropped.ID), OnChainP2PStatusCancelled)
	if _, err := r.trades.ReconcileTrade(context.Background(), dropped.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "liquidity_pool_paid_slice_lost") {
		t.Fatalf("an unselected slice's cancel alerted as lost:\n%s", buf.String())
	}
}

// Review M-12: an open that cannot be resolved either way waits, rotating;
// past an hour that is an alert, not only a per-pass metric.
func TestReconcileOrphanOpen_UnresolvedPastAnHourAlerts(t *testing.T) {
	r := newPoolOpenRig(t)
	r.trades.SetPoolSlices(r.it, releaseSender())
	_, ad := r.addLP(t, 50)
	nonce := int64(15)
	r.chain.latest = 15 // never consumed: it may still land
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	young := seedPoolSlice(t, r.db, sliceSeed{Intent: r.userID, Ad: ad, Units: usdt18(3), Nonce: &nonce, CreatedAt: time.Now().Add(-10 * time.Minute)})
	r.trades.ReconcileTrade(context.Background(), young.ID)
	if strings.Contains(buf.String(), "ALERT liquidity_pool_slice_open_unresolved") {
		t.Fatalf("alerted on a 10-minute-old open:\n%s", buf.String())
	}
	old := seedPoolSlice(t, r.db, sliceSeed{Intent: r.userID, Ad: ad, Units: usdt18(3), Nonce: &nonce, CreatedAt: time.Now().Add(-2 * time.Hour)})
	r.trades.ReconcileTrade(context.Background(), old.ID)
	if !strings.Contains(buf.String(), "ALERT liquidity_pool_slice_open_unresolved slice="+old.ID.String()) {
		t.Fatalf("no alert for a 2-hour-old unresolved open:\n%s", buf.String())
	}
	// Re-review N-6: the next passes within the hour do not repeat it.
	r.trades.ReconcileTrade(context.Background(), old.ID)
	r.trades.ReconcileTrade(context.Background(), old.ID)
	if n := strings.Count(buf.String(), "ALERT liquidity_pool_slice_open_unresolved slice="+old.ID.String()); n != 1 {
		t.Fatalf("alerted %d times within the hour, want 1", n)
	}
}

// resolveReceipt is a mined adminResolve's receipt: TradeResolved(tradeId,
// toBuyer, amount, 0) from the pool escrow.
func resolveReceipt(id [32]byte, toBuyer bool, amount *big.Int, block uint64) *types.Receipt {
	flag := make([]byte, 32)
	if toBuyer {
		flag[31] = 1
	}
	data := append(append(flag, common.LeftPadBytes(amount.Bytes(), 32)...), make([]byte, 32)...)
	return &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: new(big.Int).SetUint64(block),
		Logs: []*types.Log{{Address: common.HexToAddress(poolTestEscrow), Data: data,
			Topics: []common.Hash{topicTradeResolved, common.BytesToHash(id[:])}}}}
}

// disputedPaidSlice pays a one-slice order and leaves its slice disputed in
// the DB with F21's resolve claim recorded, claimed `age` ago — the state a
// crash between the claim and the hash write leaves behind.
func disputedPaidSlice(t *testing.T, r *poolSettleRig, age time.Duration) (*models.ITIntent, models.P2PTrade) {
	t.Helper()
	intent := r.openPaid(t, "5000")
	sl := r.slicesOf(t, intent.ID)[0]
	claim := sliceResolveClaimPrefix + strconv.FormatInt(time.Now().Add(-age).Unix(), 10)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Updates(map[string]any{
		"status": models.P2PTradeStatusDisputed, "release_tx_hash": claim})
	r.db.First(&sl, "id = ?", sl.ID)
	return intent, sl
}

// Review I-3 (crash after the broadcast): the resolve landed to the treasury
// but its hash never replaced the claim. The escrow's TradeResolved log names
// it: the reconciler records it and settles the slice, and the order delivers.
func TestPoolSlice_LostResolveHashIsFoundByTheLogScan(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent, sl := disputedPaidSlice(t, r, time.Minute)
	id := HashID(sl.ID)
	r.chain.setTrade(id, OnChainP2PStatusResolved)
	h := common.HexToHash("0x" + strings.Repeat("3d", 32))
	r.chain.receipts[h] = r.chain.head
	r.sender.receipts[h] = resolveReceipt(id, true, r.chain.trades[id].Amount, r.chain.head)
	r.verifier.credited[strings.ToLower(h.Hex())] = r.chain.trades[id].Amount
	r.desk.c.Probe = &logProbe{TreasuryTxProbe: r.desk.c.Probe, logs: []types.Log{{BlockNumber: r.chain.head, TxHash: h,
		Address: common.HexToAddress(poolTestEscrow), Topics: []common.Hash{topicTradeResolved, common.BytesToHash(id[:])}}}}
	if _, err := r.trades.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatal(err)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusResolved || !strings.EqualFold(got.ReleaseTxHash, h.Hex()) {
		t.Fatalf("slice = %s %q, want resolved on the logged resolve", got.Status, got.ReleaseTxHash)
	}
	_ = r.advance(t, intent.ID)
	if len(r.legs(t, intent.ID)) != 1 || len(r.chain.resolves) != 0 {
		t.Fatalf("legs=%d resolves=%d, want delivered and no second resolve", len(r.legs(t, intent.ID)), len(r.chain.resolves))
	}
}

// Review I-3: the settle proof finds the lost resolve by itself too, before
// the reconciler has run.
func TestPoolSlice_ProofFindsALostResolve(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent, sl := disputedPaidSlice(t, r, time.Minute)
	id := HashID(sl.ID)
	r.chain.setTrade(id, OnChainP2PStatusResolved)
	h := common.HexToHash("0x" + strings.Repeat("3e", 32))
	r.chain.receipts[h] = r.chain.head
	r.sender.receipts[h] = resolveReceipt(id, true, r.chain.trades[id].Amount, r.chain.head)
	r.verifier.credited[strings.ToLower(h.Hex())] = r.chain.trades[id].Amount
	r.desk.c.Probe = &logProbe{TreasuryTxProbe: r.desk.c.Probe, logs: []types.Log{{BlockNumber: r.chain.head, TxHash: h,
		Address: common.HexToAddress(poolTestEscrow), Topics: []common.Hash{topicTradeResolved, common.BytesToHash(id[:])}}}}
	_ = r.advance(t, intent.ID)
	if len(r.legs(t, intent.ID)) != 1 {
		t.Fatal("the logged resolve to the treasury is a proof: want delivered")
	}
}

// Review I-3 (crash between the claim and Send): no resolve landed and none is
// in flight. A young claim, or one while the release key has a transaction
// pending, is never cleared; an old one is, and the reconciler resolves again.
func TestPoolSlice_AbandonedResolveClaimIsCleared(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	_, sl := disputedPaidSlice(t, r, time.Minute)
	r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusDisputed)
	r.chain.latest = r.chain.nextNonce // the release key is idle
	ctx := context.Background()
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil || len(r.chain.resolves) != 0 {
		t.Fatalf("young claim: err=%v resolves=%d, want left alone", err, len(r.chain.resolves))
	}
	old := sliceResolveClaimPrefix + strconv.FormatInt(time.Now().Add(-31*time.Minute).Unix(), 10)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("release_tx_hash", old)
	r.chain.latest = r.chain.nextNonce - 1 // something of the release key's is pending
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil || len(r.chain.resolves) != 0 {
		t.Fatalf("pending tx: err=%v resolves=%d, want left alone", err, len(r.chain.resolves))
	}
	r.chain.latest = r.chain.nextNonce
	if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil {
		t.Fatal(err)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if len(r.chain.resolves) != 1 || !r.chain.resolves[0] || got.ReleaseTxHash == old {
		t.Fatalf("resolves=%v hash=%q, want the claim cleared and one resolve to the treasury", r.chain.resolves, got.ReleaseTxHash)
	}
}

// Review I-3: a logged resolve is never treated as abandoned, however old.
func TestPoolSlice_ResolveClaimNeverClearedWhenAResolveIsLogged(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	_, sl := disputedPaidSlice(t, r, 31*time.Minute)
	id := HashID(sl.ID)
	r.chain.setTrade(id, OnChainP2PStatusDisputed)
	r.chain.latest = r.chain.nextNonce
	r.desk.c.Probe = &logProbe{TreasuryTxProbe: r.desk.c.Probe, logs: []types.Log{{BlockNumber: r.chain.head, TxHash: common.HexToHash("0x01"),
		Address: common.HexToAddress(poolTestEscrow), Topics: []common.Hash{topicTradeResolved, common.BytesToHash(id[:])}}}}
	if gone, err := r.it.SliceResolveClaimAbandoned(context.Background(), &sl); gone || err != nil {
		t.Fatalf("abandoned=%v err=%v, want kept while a resolve is logged", gone, err)
	}
}

// minedDuringNonceRead is a probe on which a transaction mines while the
// release key's nonce is read: whatever ReadTrade said before that read is stale.
type minedDuringNonceRead struct {
	TreasuryTxProbe
	mine func()
}

func (m *minedDuringNonceRead) NonceAt(ctx context.Context, a common.Address, b *big.Int) (uint64, error) {
	if m.mine != nil {
		m.mine()
		m.mine = nil
	}
	return m.TreasuryTxProbe.NonceAt(ctx, a, b)
}

// Re-review N-1: a resolve that mines between the status read and the nonce
// reads, with the log scan lagging the head, must not read as abandoned —
// "still Disputed" is read after the idle and log checks.
func TestPoolSlice_ResolveClaimKeptWhenTheResolveMinesDuringTheChecks(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	_, sl := disputedPaidSlice(t, r, 31*time.Minute)
	id := HashID(sl.ID)
	r.chain.setTrade(id, OnChainP2PStatusDisputed)
	r.chain.latest = r.chain.nextNonce
	r.desk.c.Probe = &minedDuringNonceRead{TreasuryTxProbe: r.desk.c.Probe, mine: func() { r.chain.setTrade(id, OnChainP2PStatusResolved) }}
	if gone, err := r.it.SliceResolveClaimAbandoned(context.Background(), &sl); gone || err != nil {
		t.Fatalf("abandoned=%v err=%v, want kept: the trade is Resolved and its log not yet indexed", gone, err)
	}
}

// Re-review N-1: the same order for a super admin's clear-release.
func TestPoolSlice_ClearStuckReleaseRefusedWhenTheReleaseMinesDuringTheChecks(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent, sl := stuckRelease(t, r)
	id := HashID(sl.ID)
	r.desk.c.Probe = &minedDuringNonceRead{TreasuryTxProbe: r.desk.c.Probe, mine: func() { r.chain.setTrade(id, OnChainP2PStatusReleased) }}
	if err := r.it.ClearPoolSliceRelease(context.Background(), uuid.New(), intent.ID, sl.ID, "note"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want refused: the release landed during the checks", err)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusReleasing || got.ReleaseTxHash != sl.ReleaseTxHash {
		t.Fatalf("slice = %s %q, want untouched", got.Status, got.ReleaseTxHash)
	}
}

// Re-review N-5: a logged resolve back to the LP is never a proof that the
// slice reached the treasury, even where the transfer check alone would pass
// — and its hash is not written over the claim.
func TestPoolSlice_ProofRefusesALoggedResolveToTheLP(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	_, sl := disputedPaidSlice(t, r, time.Minute)
	id := HashID(sl.ID)
	r.chain.setTrade(id, OnChainP2PStatusResolved)
	h := common.HexToHash("0x" + strings.Repeat("3f", 32))
	r.chain.receipts[h] = r.chain.head
	r.sender.receipts[h] = resolveReceipt(id, false, r.chain.trades[id].Amount, r.chain.head)
	r.verifier.credited[strings.ToLower(h.Hex())] = r.chain.trades[id].Amount // isolate the direction check
	r.desk.c.Probe = &logProbe{TreasuryTxProbe: r.desk.c.Probe, logs: []types.Log{{BlockNumber: r.chain.head, TxHash: h,
		Address: common.HexToAddress(poolTestEscrow), Topics: []common.Hash{topicTradeResolved, common.BytesToHash(id[:])}}}}
	if ok, err := r.desk.proveSliceRelease(context.Background(), &sl, 1); ok || err != nil {
		t.Fatalf("proved=%v err=%v, want a resolve to the LP refused", ok, err)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if got.ReleaseTxHash != sl.ReleaseTxHash {
		t.Fatalf("release hash = %q, want the claim %q kept", got.ReleaseTxHash, sl.ReleaseTxHash)
	}
}

// knowsEveryHash is a node that still has every transaction it is asked about.
type knowsEveryHash struct{ TreasuryTxProbe }

func (knowsEveryHash) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	return nil, true, nil
}

// Re-review N-3: the shared txHashKnown check — a release the node still
// knows can still land, so it is never cleared.
func TestPoolSlice_ClearStuckReleaseRefusedWhileTheNodeKnowsTheHash(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent, sl := stuckRelease(t, r)
	r.desk.c.Probe = knowsEveryHash{r.desk.c.Probe}
	if err := r.it.ClearPoolSliceRelease(context.Background(), uuid.New(), intent.ID, sl.ID, "note"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want refused while the node knows the release", err)
	}
}
