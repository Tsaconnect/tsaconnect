package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// wireForwards gives the settle rig the P2P forwarder over the LP ledger.
func (r *poolSettleRig) wireForwards(t *testing.T) (*P2PSettlementForwarder, *fakeNombaGateway, *mockBus) {
	t.Helper()
	gw := &fakeNombaGateway{payoutID: "trf_lp"}
	fwd := NewP2PSettlementForwarder(r.db, NewPMService(r.db), gw, &stubPayoutBalances{ngn: 1_000_000}, "TSA Connect")
	src := NewP2PTradeClaimSource(r.db, NewPMService(r.db), r.trades, r.sender)
	src.SetForwarder(fwd)
	r.it.SetPoolForwards(src)
	bus := &mockBus{}
	r.it.rateNotifier = bus
	return fwd, gw, bus
}

func (r *poolSettleRig) forwards(t *testing.T) []models.P2PSettlementForward {
	t.Helper()
	var out []models.P2PSettlementForward
	r.db.Order("created_at").Find(&out)
	return out
}

// Decision 7 / P7: the LP is paid floor(units × L / 10^6) — the snapshot's LP
// rate, never a re-priced one.
func TestPoolForward_PaysSnapshotLPRate(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	_, gw, bus := r.wireForwards(t)
	intent := r.openPaid(t, "100000")
	_ = r.advance(t, intent.ID)
	fw := r.forwards(t)
	if len(fw) != 2 || !ngnEqual(fw[0].Amount, "81284.40") || !ngnEqual(fw[1].Amount, "16825.31") || len(gw.calls) != 2 {
		t.Fatalf("forwards = %+v calls=%d, want ₦81,284.40 and ₦16,825.31 (spec §10.2)", fw, len(gw.calls))
	}
	fills := 0
	for _, ev := range bus.published {
		if ev.Type == events.LiquidityFill {
			fills++
		}
	}
	if fills != 2 {
		t.Fatalf("fills = %d, want one liquidity.fill per LP", fills)
	}
}

// Roadmap risk 1: replayed ticks forward each slice once and credit once.
func TestPoolForward_OncePerSliceOnReplay(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	_, gw, _ := r.wireForwards(t)
	intent := r.openPaid(t, "5000")
	for i := 0; i < 3; i++ {
		_ = r.advance(t, intent.ID)
	}
	var lp models.PaymentMerchant
	r.db.First(&lp, "id = ?", r.lps[0].ID)
	if n := len(r.forwards(t)); n != 1 || len(gw.calls) != 1 || !ngnEqual(lp.AvailableNGN, "4905.48") {
		t.Fatalf("forwards=%d calls=%d available=%s, want one forward, one transfer, ₦4,905.48 credited once", n, len(gw.calls), lp.AvailableNGN)
	}
}

// §12.5 / S19: each LP is paid when ITS release is proven, not when the
// whole order is.
func TestPoolForward_OnlyAfterSliceRelease5Deep(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	_, gw, _ := r.wireForwards(t)
	r.chain.depth = 3
	intent := r.openPaid(t, "5000")
	_ = r.advance(t, intent.ID)
	if len(gw.calls) != 0 {
		t.Fatal("forwarded on a 4-deep release")
	}
	r.chain.depth = 4
	_ = r.advance(t, intent.ID)
	if len(gw.calls) != 1 {
		t.Fatalf("calls = %d, want the forward once 5 deep", len(gw.calls))
	}
}

// F22: an LP self-release on an UNPAID order is never forwarded automatically.
func TestPoolSlice_SelfReleaseUnpaidNotForwarded(t *testing.T) {
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
	r.db.First(&sl, "id = ?", sl.ID)
	if _, err := r.it.forwardSlice(context.Background(), r.desk, &sl, poolForwardRef(sl.ID), false); err == nil {
		t.Fatal("an unpaid order's slice must not be forwarded")
	}
	if n := len(r.forwards(t)); n != 0 {
		t.Fatalf("forwards = %d", n)
	}
}

// The payout_sent event follows the forwarder's confirmed disbursement.
func TestPoolForward_PayoutSentOnConfirm(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	fwd, _, bus := r.wireForwards(t)
	fwd.SetOnSettled(r.it.PoolForwardSettled)
	intent := r.openPaid(t, "5000")
	_ = r.advance(t, intent.ID)
	f := r.forwards(t)[0]
	if err := fwd.Confirm(context.Background(), f.Reference, true, ""); err != nil {
		t.Fatal(err)
	}
	sent := false
	for _, ev := range bus.published {
		sent = sent || (ev.Type == events.LiquidityPayoutSent && ev.UserID == r.lps[0].UserID)
	}
	if !sent {
		t.Fatal("no liquidity.payout_sent to the LP")
	}
}

// Review I-3: a forward the settle tick never wrote (here: no forwarder yet)
// is back-filled by the books pass once the order has settled.
func TestPoolForward_BackfilledAfterTheOrderSettled(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	r.it.rateNotifier = &mockBus{}
	intent := r.openPaid(t, "5000")
	r.settleDirect(t, intent) // settles; poolProven alerts "unwired" and writes nothing
	if n := len(r.forwards(t)); n != 0 {
		t.Fatalf("setup: forwards = %d", n)
	}
	_, gw, _ := r.wireForwards(t)
	if _, err := r.it.CompletePoolBooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(r.forwards(t)); n != 1 || len(gw.calls) != 1 {
		t.Fatalf("forwards=%d calls=%d, want the LP back-filled once", n, len(gw.calls))
	}
}

// Review I-2: settle ticks never re-send a pending forward; only the re-drive does.
func TestPoolForward_SettleTicksNeverResend(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	_, gw, _ := r.wireForwards(t)
	gw.err = errors.New("timeout")
	r.sender.noMine = false
	intent := r.openPaid(t, "5000")
	for i := 0; i < 4; i++ {
		_ = r.advance(t, intent.ID)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("gateway calls = %d, want 1 (retries belong to the re-drive)", len(gw.calls))
	}
}

// Security scan (C2 T2): a provider_ref already owned by a forward of a
// DIFFERENT trade must never read back as this slice's forward — the slice
// would look paid while its LP never was.
func TestPoolForward_RefOwnedByAnotherTradeNeverReadsAsPaid(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	_, gw, bus := r.wireForwards(t)
	r.chain.depth = 3 // released but not proven: the tick forwards nothing itself
	intent := r.openPaid(t, "5000")
	_ = r.advance(t, intent.ID)
	sl := r.slicesOf(t, intent.ID)[0]
	if err := r.db.Create(&models.P2PSettlementForward{ID: uuid.New(), TradeID: uuid.New(), PMID: r.lps[0].ID,
		ProviderRef: poolForwardRef(sl.ID), Currency: "NGN", Amount: sl.FiatAmount, Status: models.P2PForwardPending,
		Reference: p2pForwardRefPrefix + uuid.New().String()}).Error; err != nil {
		t.Fatal(err)
	}
	fwd, err := r.it.forwardSlice(context.Background(), r.desk, &sl, poolForwardRef(sl.ID), false)
	if err == nil || fwd != nil {
		t.Fatalf("forwardSlice = %+v, %v; want a refusal, never another trade's forward", fwd, err)
	}
	var lp models.PaymentMerchant
	r.db.First(&lp, "id = ?", r.lps[0].ID)
	if !ngnEqual(lp.AvailableNGN, "0") || len(gw.calls) != 0 || len(bus.published) != 0 {
		t.Fatalf("available=%s calls=%d events=%d, want nothing credited, sent or announced", lp.AvailableNGN, len(gw.calls), len(bus.published))
	}
}

// The same guard on the merchant path: a credit ref that already settled
// trade B never settles trade A (it would release A's crypto unpaid).
func TestP2PAccept_RefThatSettledAnotherTradeNeverSettlesThisOne(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	var ad models.P2PAd
	r.db.First(&ad, "id = ?", r.trade.AdID)
	other := seedBuyTradeOnAd(t, r.db, &ad, uuid.New(), "1500.00")
	ctx := context.Background()
	setReleasedEvent(r.esc, other)
	if err := r.src.Accept(ctx, other.ID, p2pInboundCredit("sess_once", "1500.00")); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	before := r.availableNGN(t)
	setReleasedEvent(r.esc, r.trade)
	err := r.src.Accept(ctx, r.trade.ID, p2pInboundCredit("sess_once", "1500.00"))
	var cur models.P2PTrade
	r.db.First(&cur, "id = ?", r.trade.ID)
	if err == nil || cur.Status != models.P2PTradeStatusPendingPayment || !ngnEqual(r.availableNGN(t), before) {
		t.Fatalf("err=%v status=%s available=%s→%s, want a refusal with trade A untouched", err, cur.Status, before, r.availableNGN(t))
	}
}

// F22: an LP self-release on an unpaid order has one exit — paid naira
// (operator) or handed the USDT back — never both, and never paid twice.
func TestPoolForward_F22OneExitPerSlice(t *testing.T) {
	selfReleased := func(t *testing.T) (*poolSettleRig, *fakeNombaGateway, models.P2PTrade) {
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
		r.db.First(&sl, "id = ?", sl.ID)
		return r, gw, sl
	}
	available := func(r *poolSettleRig) string {
		var lp models.PaymentMerchant
		r.db.First(&lp, "id = ?", r.lps[0].ID)
		return lp.AvailableNGN
	}
	ctx := context.Background()

	r, gw, sl := selfReleased(t)
	if _, err := r.it.forwardSlice(ctx, r.desk, &sl, "pool_f22:op1", true); err != nil {
		t.Fatalf("operator pay: %v", err)
	}
	if _, err := r.it.forwardSlice(ctx, r.desk, &sl, "pool_f22:op2", true); err == nil {
		t.Fatal("a second operator pay was accepted")
	}
	var cur models.P2PTrade
	r.db.First(&cur, "id = ?", sl.ID)
	if cur.CancelReason != poolF22Paid || !ngnEqual(available(r), sl.FiatAmount) || len(gw.calls) != 1 || len(r.forwards(t)) != 1 {
		t.Fatalf("marker=%q available=%s calls=%d forwards=%d, want paid once", cur.CancelReason, available(r), len(gw.calls), len(r.forwards(t)))
	}

	r, gw, sl = selfReleased(t)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("cancel_reason", poolF22Returned)
	if _, err := r.it.forwardSlice(ctx, r.desk, &sl, "pool_f22:op1", true); !errors.Is(err, errPoolForwardRefused) {
		t.Fatalf("returned slice paid: err=%v", err)
	}
	if !ngnEqual(available(r), "0") || len(gw.calls) != 0 {
		t.Fatalf("available=%s calls=%d, want nothing for a slice handed back", available(r), len(gw.calls))
	}
}

// Review m-4: the LP hooks match the pool's exact reference prefixes
// ("pool:" and F22's "pool-f22:"), never any reference that merely starts
// with "pool" — a merchant credit's reference must never reach an LP hook.
func TestPoolForward_HooksMatchOnlyPoolReferences(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	_, _, bus := r.wireForwards(t)
	for ref, want := range map[string]bool{
		poolForwardRef(uuid.New()):        true,
		poolF22ForwardRef(uuid.New()):     true,
		"poolside-merchant-credit-123":    false,
		"POOL:" + uuid.NewString():        false,
		"pool-f22" + uuid.NewString()[:4]: false,
	} {
		if got := isPoolForwardRef(ref); got != want {
			t.Fatalf("isPoolForwardRef(%q) = %v, want %v", ref, got, want)
		}
	}
	bus.published = nil
	r.it.PoolForwardSettled(context.Background(), &models.P2PSettlementForward{ID: uuid.New(), PMID: r.lps[0].ID,
		ProviderRef: "poolside-merchant-credit-123", Amount: "100.00"})
	if len(bus.published) != 0 {
		t.Fatalf("a merchant forward reached the LP hook: %+v", bus.published)
	}
}

// Review m-4: the delayed-payout notice reads pool forwards only.
func TestPoolForward_DelayedNoticeReadsOnlyPoolReferences(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	_, _, bus := r.wireForwards(t)
	for _, ref := range []string{"poolside-merchant-credit-123", poolForwardRef(uuid.New())} {
		if err := r.db.Exec(`INSERT INTO p2p_settlement_forwards (id, trade_id, pm_id, provider_ref, currency, amount, status, reference, created_at)
			VALUES (?, ?, ?, ?, 'NGN', '100.00', ?, ?, ?)`, uuid.NewString(), uuid.NewString(), r.lps[0].ID.String(), ref,
			models.P2PForwardFailed, "ref_"+ref[:6]+uuid.NewString()[:4], time.Now()).Error; err != nil {
			t.Fatal(err)
		}
	}
	bus.published = nil
	r.it.notePoolPayoutsDelayed(context.Background())
	if len(bus.published) != 1 {
		t.Fatalf("delayed notices = %d, want 1 (the pool forward only)", len(bus.published))
	}
}
