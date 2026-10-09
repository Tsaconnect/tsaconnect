package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// §10.2's LPs: free 60 / 45 / 20 USDT.
func threeLPs(t *testing.T, r *poolOpenRig) (a, b, c *models.PaymentMerchant) {
	a, _ = r.addLP(t, 60)
	b, _ = r.addLP(t, 45)
	c, _ = r.addLP(t, 20)
	return
}

// Roadmap risk 1: her pay-in exists only once every slice is proven on-chain.
func TestPoolOpen_PayInOnlyAfterAllSlicesProven(t *testing.T) {
	r := newPoolOpenRig(t)
	_, b, _ := threeLPs(t, r)
	r.chain.neverLand[walletOf(b)] = true // B's open is broadcast but never mined
	intent, pay, err := r.open(t, "100000")
	if !errors.Is(err, ErrPoolCannotFill) || pay != nil || intent != nil {
		t.Fatalf("open with an unproven slice = %v %v %v, want refused with no pay-in", intent, pay, err)
	}
	var failed models.ITIntent
	r.db.Where("provider = ?", models.ITProviderPool).First(&failed)
	if failed.Status != models.ITStatusCancelled || failed.FailureReason != models.ITFailurePoolOpenFailed || failed.RateExpiresAt != nil {
		t.Fatalf("failed order = %+v", failed)
	}
	for _, sl := range r.slicesOf(t, failed.ID) {
		if sl.Status == models.P2PTradeStatusPendingPayment && sl.DroppedAt == nil {
			t.Fatalf("slice %s still backs a failed order (canceller must be free to cancel it)", sl.ID)
		}
	}

	r2 := newPoolOpenRig(t)
	threeLPs(t, r2)
	intent, pay, err = r2.open(t, "100000")
	if err != nil || pay == nil || pay.AmountNGN == "" || intent.Status != models.ITStatusPendingPayment || intent.RateExpiresAt == nil {
		t.Fatalf("happy open = %+v %+v %v", intent, pay, err)
	}
	slices := r2.slicesOf(t, intent.ID)
	if len(slices) != 2 || slices[0].OpenedAt == nil || slices[1].OpenedAt == nil || slices[0].OpenNonce == nil {
		t.Fatalf("slices = %+v, want two proven slices", slices)
	}
	var po models.PoolOrder
	r2.db.First(&po, "intent_id = ? AND generation = 1", intent.ID)
	if po.SliceCount != 2 || po.OpenedUnits != "72419593" || po.LPRateMinor != "135474" || po.PoolSpreadBps != 50 {
		t.Fatalf("snapshot = %+v", po)
	}
	if slices[0].BuyerID != r2.buyer || slices[0].OpenerID != nil || slices[0].FiatAmount != "81284.40" || slices[0].TokenAmount != "60000000000000000000" {
		t.Fatalf("slice A = %+v (buyer = company user, opener NULL, LP naira, 18-dp units)", slices[0])
	}
}

// S43: each open's nonce is on its slice before the transaction leaves.
func TestPoolOpen_NonceRecordedBeforeBroadcast(t *testing.T) {
	r := newPoolOpenRig(t)
	threeLPs(t, r)
	var bad []string
	r.chain.onBroadcast = func(nonce uint64, tradeID [32]byte) {
		var sl models.P2PTrade
		r.db.Where("on_chain_trade_id = ?", "0x"+hexOf(tradeID)).First(&sl)
		if sl.OpenNonce == nil || uint64(*sl.OpenNonce) != nonce {
			bad = append(bad, sl.ID.String())
		}
	}
	if _, _, err := r.open(t, "100000"); err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("slices broadcast before their nonce was recorded: %v", bad)
	}
}

// S43: a burst never straddles two RPC endpoints; the rest abort and re-pick.
func TestPoolOpen_AbortsBurstOnEndpointGenerationChange(t *testing.T) {
	r := newPoolOpenRig(t)
	a, b, c := threeLPs(t, r)
	r.chain.bumpAfter = 1 // the endpoint changes right after A's broadcast
	intent, _, err := r.open(t, "100000")
	if err != nil {
		t.Fatalf("open = %v, want success after re-picking B's slice to C", err)
	}
	byLP := map[uuid.UUID]models.P2PTrade{}
	for _, sl := range r.slicesOf(t, intent.ID) {
		var ad models.P2PAd
		r.db.First(&ad, "id = ?", sl.AdID)
		byLP[*ad.PMID] = sl
	}
	if byLP[b.ID].Status != models.P2PTradeStatusCancelled || byLP[b.ID].OpenNonce != nil {
		t.Fatalf("B = %+v, want reclaimed and never broadcast", byLP[b.ID])
	}
	if byLP[a.ID].OpenedAt == nil || byLP[c.ID].OpenedAt == nil || byLP[c.ID].TokenAmount != "12419593000000000000" {
		t.Fatalf("A/C = %+v %+v", byLP[a.ID], byLP[c.ID])
	}
}

// F24 / S43: no open while the treasury lane has a stuck nonce.
func TestPoolOpen_RefusedOnStuckNonce(t *testing.T) {
	r := newPoolOpenRig(t)
	threeLPs(t, r)
	r.chain.nextNonce, r.chain.latest = 12, 10 // two in flight
	_ = r.desk.c.Nonces.Check(context.Background())
	r.nonceNow = r.nonceNow.Add(31 * time.Second)
	if _, _, err := r.open(t, "5000"); !errors.Is(err, ErrTreasuryNonceStuck) {
		t.Fatalf("open = %v, want ErrTreasuryNonceStuck", err)
	}
	var n int64
	r.db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 || r.chain.broadcasts != 0 {
		t.Fatalf("intents=%d broadcasts=%d, want nothing", n, r.chain.broadcasts)
	}
}

// §12.2 step 5: one re-pick per failed slice, else refuse.
func TestPoolOpen_RepickOnceThenRefuse(t *testing.T) {
	r := newPoolOpenRig(t)
	a, _ := r.addLP(t, 50)
	r.addLP(t, 40)
	r.chain.prepareErr[walletOf(a)] = errors.New("estimate gas (would revert): execution reverted: SellerInsufficient")
	intent, _, err := r.open(t, "5000") // 3.62: a whole order, to A (the larger stock share)
	if err != nil || len(r.slicesOf(t, intent.ID)) != 2 {
		t.Fatalf("open = %v, want success on the re-picked LP (one cancelled slice + one proven)", err)
	}
	r2 := newPoolOpenRig(t)
	x, _ := r2.addLP(t, 50)
	y, _ := r2.addLP(t, 40)
	for _, lp := range []*models.PaymentMerchant{x, y} {
		r2.chain.prepareErr[walletOf(lp)] = errors.New("execution reverted: SellerInsufficient")
	}
	if _, _, err := r2.open(t, "5000"); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("both revert = %v, want ErrPoolCannotFill", err)
	}
}

// F15: a non-zero fee snapshot closes the pool and fails the order.
func TestPoolOpen_FeeSnapshotNonZeroClosesPool(t *testing.T) {
	r := newPoolOpenRig(t)
	lp, _ := r.addLP(t, 50)
	r.chain.onBroadcast = func(_ uint64, id [32]byte) {
		r.chain.cmu.Lock()
		r.chain.snaps[id] = [2]uint16{50, 0}
		r.chain.cmu.Unlock()
	}
	if _, _, err := r.open(t, "5000"); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("open = %v", err)
	}
	if r.gates.closed == "" {
		t.Fatalf("pool still open after a %s slice snapshotted a fee", walletOf(lp))
	}
}

// M2 / F5: our own failure is hidden and never an abandon.
func TestPoolOpen_FailedOpenNotCountedAsAbandon(t *testing.T) {
	r := newPoolOpenRig(t)
	lp, _ := r.addLP(t, 50)
	r.chain.prepareErr[walletOf(lp)] = errors.New("execution reverted: VoucherExpired")
	if _, _, err := r.open(t, "5000"); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatal(err)
	}
	n, err := r.it.CountAbandonedRoutedStableBuys(context.Background(), r.userID, time.Now().Add(-time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("abandoned = %d %v", n, err)
	}
	list, _ := r.it.ListUserIntents(context.Background(), r.userID)
	if len(list) != 0 {
		t.Fatalf("her list shows %d hidden pool failures (M14)", len(list))
	}
}

// Review I4: an honour window (lock + grace) that reaches the escrow's 60-min
// payment window minus the safety room refuses before anything is written.
func TestPoolOpen_RefusedWhenHonourWindowNearsSliceWindow(t *testing.T) {
	r := newPoolOpenRig(t)
	r.addLP(t, 50)
	r.it.SetStableRateLockTTL(41 * time.Minute) // 41 + 10 grace + 10 safety = 61 ≥ 60
	if _, _, err := r.open(t, "5000"); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("open = %v, want ErrPoolClosed", err)
	}
	var n int64
	r.db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 || r.chain.broadcasts != 0 {
		t.Fatalf("intents=%d broadcasts=%d, want nothing written or sent", n, r.chain.broadcasts)
	}
	r.it.SetStableRateLockTTL(39 * time.Minute) // 59 < 60: allowed
	if _, _, err := r.open(t, "5000"); err != nil {
		t.Fatalf("open inside the window: %v", err)
	}
}

// §10.4: the per-user daily cap refuses before anything is written.
func TestPoolOpen_DailyCapRefusesForFailover(t *testing.T) {
	r := newPoolOpenRig(t)
	r.addLP(t, 50)
	prior := seedProviderIntent(t, &ITService{db: r.db}, r.userID, models.ITProviderPool, models.ITStatusPaymentReceived)
	r.db.Model(prior).Update("asset_amount", "1998000000") // 1,998 USDT today
	if _, _, err := r.open(t, "5000"); !errors.Is(err, ErrPoolDailyCapReached) {
		t.Fatalf("open = %v, want ErrPoolDailyCapReached", err)
	}
	if r.chain.broadcasts != 0 {
		t.Fatal("broadcast despite the cap")
	}
}

func TestPoolOpen_RefusedWhileGatesClosedOrReleaseGasLow(t *testing.T) {
	r := newPoolOpenRig(t)
	r.addLP(t, 50)
	r.gates.gasOK = false
	if _, _, err := r.open(t, "5000"); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("release gas low = %v", err)
	}
	r.gates.gasOK, r.gates.open = true, false
	if _, _, err := r.open(t, "5000"); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("gates closed = %v", err)
	}
}

// A slice is broadcast at most once by our code: open_nonce is written once,
// so a second attempt on the same row records nothing and sends nothing (the
// chain's TradeExists revert is the second guard, deterministic tradeId).
func TestPoolOpen_DuplicateTradeIdNeverLocksTwice(t *testing.T) {
	r := newPoolOpenRig(t)
	lp, _ := r.addLP(t, 50)
	r.chain.neverLand[walletOf(lp)] = true // sent, never mined: the chain would accept a second open
	if _, _, err := r.open(t, "5000"); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatal(err)
	}
	var failed models.ITIntent
	r.db.Where("provider = ?", models.ITProviderPool).First(&failed)
	sl := r.slicesOf(t, failed.ID)[0]
	before := r.chain.broadcasts
	var ad models.P2PAd
	r.db.First(&ad, "id = ?", sl.AdID)
	units, _ := new(big.Int).SetString(sl.TokenAmount, 10)
	o := &sliceOpen{slice: sl, ad: ad, units: units}
	r.desk.broadcast(context.Background(), []*sliceOpen{o})
	if r.chain.broadcasts != before || o.sent {
		t.Fatalf("re-broadcast of slice %s: broadcasts %d → %d", sl.ID, before, r.chain.broadcasts)
	}
}

// hexOf is a tradeId as slices store it (HashIDHex), without the 0x.
func hexOf(b [32]byte) string { return strings.TrimPrefix(common.BytesToHash(b[:]).Hex(), "0x") }
