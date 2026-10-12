package services

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// The liquidity-pool-settle job walks PendingPoolBuys and calls AdvancePoolBuy
// on each. These tests run that walk against the real engine next to the two
// jobs whose work borders it, and prove nothing is processed twice.

// settleTick is one liquidity-pool-settle tick over the real engine.
func (r *poolSettleRig) settleTick(t *testing.T) []uuid.UUID {
	t.Helper()
	received, delivering, err := r.it.PendingPoolBuys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	listed := slices.Concat(received, delivering)
	for _, id := range listed {
		_ = r.it.AdvancePoolBuy(context.Background(), id)
	}
	return listed
}

// chainReconcilerTick is one p2p-chain-reconciler tick (jobs.ChainReconciler.Run).
func (r *poolSettleRig) chainReconcilerTick(t *testing.T) {
	t.Helper()
	trades, err := r.trades.ListNonTerminalTrades(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range trades {
		_, _ = r.trades.ReconcileTrade(context.Background(), tr.ID)
	}
}

// The chain reconciler moving a slice the settle job released never makes
// the settle job release it again or send the buyer a second delivery.
func TestPoolSettleScope_ReconcilerTicksNeverCauseASecondReleaseOrSale(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	intent := r.openPaid(t, "100000")
	r.chain.depth = 3 // released, not yet 5 deep
	r.settleTick(t)
	if len(r.sender.sends) != 2 {
		t.Fatalf("sends = %d, want one release per slice", len(r.sender.sends))
	}
	for range 2 {
		r.chainReconcilerTick(t)
		r.settleTick(t)
	}
	r.chain.depth = 4
	for range 3 {
		r.settleTick(t)
		r.chainReconcilerTick(t)
	}
	if len(r.sender.sends) != 2 {
		t.Fatalf("sends = %d after reconciler ticks, want the two releases only", len(r.sender.sends))
	}
	if legs := r.legs(t, intent.ID); len(legs) != 1 || r.saleSigner.broadcasts() != 1 {
		t.Fatalf("sale legs=%d broadcasts=%d, want exactly one delivery", len(legs), r.saleSigner.broadcasts())
	}
}

// A converted order handed to the conversion orchestrator leaves the settle
// job's list, and a stray advance on it touches neither the record nor chain.
func TestPoolSettleScope_HandedOffConvertedOrderIsTheOrchestratorsAlone(t *testing.T) {
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
	if listed := r.settleTick(t); !slices.Contains(listed, intent.ID) {
		t.Fatalf("listed %v, want the paid converted order before hand-off", listed)
	}
	var before models.PaycrestOrderRecord
	r.db.First(&before, "reference = ?", ITConversionRef(intent.ID))
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusSettled || before.TxHash == "" {
		t.Fatalf("intent=%s rec.tx=%q, want handed off", got.Status, before.TxHash)
	}
	sends := len(r.sender.sends)

	if listed := r.settleTick(t); slices.Contains(listed, intent.ID) {
		t.Fatal("the settle job still lists an order the orchestrator owns")
	}
	if err := r.it.AdvancePoolBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("advance on a handed-off order: %v", err)
	}
	r.chainReconcilerTick(t)
	var after models.PaycrestOrderRecord
	r.db.First(&after, "reference = ?", ITConversionRef(intent.ID))
	if len(r.sender.sends) != sends || after.TxHash != before.TxHash || after.Status != before.Status ||
		!after.UpdatedAt.Equal(before.UpdatedAt) || len(r.legs(t, intent.ID)) != 0 {
		t.Fatalf("sends %d→%d rec %q/%s → %q/%s legs=%d, want the settle side to leave the handed-off order alone",
			sends, len(r.sender.sends), before.TxHash, before.Status, after.TxHash, after.Status, len(r.legs(t, intent.ID)))
	}
}

// The settle job lists only pool buys: a company-wallet buy in the same
// state stays the StableBuyWatcher's.
func TestPoolSettleScope_ListsOnlyPoolBuys(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	pool := r.openPaid(t, "5000")
	other := *pool
	other.ID = uuid.New()
	other.Provider = models.ITProviderTreasury
	other.Status = models.ITStatusPaymentReceived
	if err := r.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	received, delivering, err := r.it.PendingPoolBuys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	all := slices.Concat(received, delivering)
	if !slices.Contains(all, pool.ID) || slices.Contains(all, other.ID) {
		t.Fatalf("listed %v, want the pool order only", all)
	}
}
