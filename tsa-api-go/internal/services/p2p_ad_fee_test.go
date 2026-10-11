package services

import (
	"context"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// countingFeeChain counts every chain read and, when hang is set, blocks each one
// until the test ends: a fee read that must never happen, or one that is slow.
type countingFeeChain struct {
	inner *feeViewChain
	hang  chan struct{}
	calls atomic.Int32
}

func (c *countingFeeChain) CallContract(ctx context.Context, call ethereum.CallMsg, n *big.Int) ([]byte, error) {
	c.calls.Add(1)
	if c.hang != nil {
		<-c.hang
	}
	return c.inner.CallContract(ctx, call, n)
}

func newCountingFeeView(t *testing.T, hang bool) (*P2PFeeView, *countingFeeChain) {
	t.Helper()
	cc := &countingFeeChain{inner: &feeViewChain{feeManager: true, rates: map[string][4]interface{}{
		"p2p_platform": {uint16(50), uint64(0), false, true},
		"p2p":          {uint16(10), uint64(0), false, true},
	}}}
	if hang {
		cc.hang = make(chan struct{})
		t.Cleanup(func() { close(cc.hang) })
	}
	v := NewP2PFeeView(
		func(int64) (contractCaller, string, error) { return cc, feeViewEscrow, nil },
		func(int64, string) string { return feeViewToken },
	)
	return v, cc
}

// Plain ad reads (cancel, express matching, the open path) never wait on the chain.
func TestAdReads_DoNoFeeRead(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	view, chain := newCountingFeeView(t, false)
	svc.SetFeeView(view)
	seller := seedVerifiedUser(t, db)
	adID := seedAdBy(t, db, seller, "buy")
	ctx := context.Background()

	ad, err := svc.GetAd(ctx, adID)
	if err != nil || ad.FeeBps != nil {
		t.Fatalf("GetAd: fee=%v err=%v", ad.FeeBps, err)
	}
	if _, _, err := svc.GetAdForViewer(ctx, adID, seller); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.ListAds(ctx, AdFilter{Status: models.P2PAdStatusActive})
	if err != nil || len(rows) != 1 || rows[0].FeeBps != nil {
		t.Fatalf("ListAds: rows=%v err=%v", rows, err)
	}
	if _, err := svc.CancelBuyAd(ctx, adID, seller); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ExpressMatch(ctx, ExpressMatchParams{Side: "sell", Token: "USDT", FiatCurrency: "NGN", Amount: "1"}); err != nil {
		t.Fatal(err)
	}
	if n := chain.calls.Load(); n != 0 {
		t.Fatalf("ad reads made %d chain calls, want 0", n)
	}
}

func TestFillAdFees_OneReadPerChainToken(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	view, chain := newCountingFeeView(t, false)
	svc.SetFeeView(view)
	a, b := &models.P2PAd{ChainID: 146, Token: "USDT"}, &models.P2PAd{ChainID: 146, Token: "usdt"}

	svc.FillAdFees(context.Background(), a, b, nil)

	if a.FeeBps == nil || *a.FeeBps != 60 || b.FeeBps == nil || *b.FeeBps != 60 {
		t.Fatalf("fees = %v %v, want 60", a.FeeBps, b.FeeBps)
	}
	perRead := chain.calls.Load()
	svc.FillAdFees(context.Background(), &models.P2PAd{ChainID: 146, Token: "USDT"})
	if chain.calls.Load() != perRead {
		t.Fatal("a repeated (chain, token) read missed the cache")
	}
}

// A slow chain costs the list at most the budget; the fee is then omitted.
func TestFillAdFees_SlowReaderBoundedByBudget(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	view, _ := newCountingFeeView(t, true)
	svc.SetFeeView(view)
	svc.adFeeBudget = 50 * time.Millisecond
	ad := &models.P2PAd{ChainID: 146, Token: "USDT"}

	start := time.Now()
	svc.FillAdFees(context.Background(), ad)

	if d := time.Since(start); d > time.Second {
		t.Fatalf("FillAdFees waited %v on a hung chain", d)
	}
	if ad.FeeBps != nil {
		t.Fatalf("fee = %d from a read that never finished", *ad.FeeBps)
	}
}

func TestFillAdFees_FailingReaderOmitsFee(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.SetFeeView(newFeeViewFailing(t))
	ad := &models.P2PAd{ChainID: 146, Token: "USDT"}
	svc.FillAdFees(context.Background(), ad)
	if ad.FeeBps != nil {
		t.Fatalf("fee = %d on a failed read", *ad.FeeBps)
	}
}

func TestDefaultAdFeeBudgetIsTwoSeconds(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	if svc.adFeeBudget != 2*time.Second {
		t.Fatalf("budget = %v", svc.adFeeBudget)
	}
}
