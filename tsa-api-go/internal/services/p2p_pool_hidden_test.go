package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func adIDs(ads []models.P2PAd) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for _, a := range ads {
		out[a.ID] = true
	}
	return out
}

// Roadmap risk 1 / Review Focus 1.
func TestLPAd_HiddenFromPublic(t *testing.T) {
	r := newPoolRig(t)
	ctx := context.Background()
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	lpAd := seedActiveAd(t, r.db, lp.UserID, poolTestChain, &lp.ID, "1000", usdt18(500000).String())
	sonicAd := seedActiveAd(t, r.db, uuid.New(), 146, nil, "1500", "100000000")

	t.Run("ListAds", func(t *testing.T) {
		yes := true
		for name, f := range map[string]AdFilter{
			"browse":     {Status: models.P2PAdStatusActive, ExcludeDelisted: true},
			"pm filter":  {Status: models.P2PAdStatusActive, ExcludeDelisted: true, PM: &yes},
			"by seller":  {SellerID: lp.UserID, Status: models.P2PAdStatusActive, ExcludeDelisted: true},
			"pool chain": {ChainID: poolTestChain, ExcludeDelisted: true},
		} {
			ads, err := r.svc.ListAds(ctx, f)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if adIDs(ads)[lpAd.ID] {
				t.Errorf("%s lists the LP stock ad", name)
			}
		}
		ads, _ := r.svc.ListAds(ctx, AdFilter{Status: models.P2PAdStatusActive, ExcludeDelisted: true})
		if !adIDs(ads)[sonicAd.ID] {
			t.Error("ordinary browse lost the Sonic ad")
		}
	})
	t.Run("ExpressMatch", func(t *testing.T) {
		ads, err := r.svc.ExpressMatch(ctx, ExpressMatchParams{Side: "buy", Token: "USDT", FiatCurrency: "NGN", Amount: "1000000"})
		if err != nil {
			t.Fatal(err)
		}
		if adIDs(ads)[lpAd.ID] {
			t.Fatal("Express offers the (cheaper) LP stock ad")
		}
	})
	t.Run("GetAd", func(t *testing.T) {
		if _, _, err := r.svc.GetAdForViewer(ctx, lpAd.ID, uuid.New()); !errors.Is(err, ErrAdNotFound) {
			t.Fatalf("non-owner: %v, want ErrAdNotFound (the voucher signature never leaves)", err)
		}
		ad, pool, err := r.svc.GetAdForViewer(ctx, lpAd.ID, lp.UserID)
		if err != nil || !pool || ad.ID != lpAd.ID {
			t.Fatalf("owner: %v %v %v", ad, pool, err)
		}
		if _, pool, err := r.svc.GetAdForViewer(ctx, sonicAd.ID, uuid.New()); err != nil || pool {
			t.Fatalf("ordinary ad: pool=%v err=%v", pool, err)
		}
	})
	t.Run("OpenTrade", func(t *testing.T) {
		_, _, err := r.svc.OpenTrade(ctx, lpAd.ID, uuid.New(), "0x00000000000000000000000000000000000000f1", OpenTradeParams{FiatAmount: "1000"})
		if !errors.Is(err, ErrAdNotActive) {
			t.Fatalf("open on an LP ad: %v, want ErrAdNotActive", err)
		}
	})
}

// Review Focus 1: hidden by its LP owner even once the pool chain is gone.
func TestLPAd_HiddenAfterPoolChainUnconfigured(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db) // Sonic only: no pool chain
	svc.SetPMService(NewPMService(db))
	lp := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	lpAd := seedActiveAd(t, db, lp.UserID, poolTestChain, &lp.ID, "1000", usdt18(10).String())
	ads, err := svc.ListAds(context.Background(), AdFilter{Status: models.P2PAdStatusActive, ExcludeDelisted: true})
	if err != nil || adIDs(ads)[lpAd.ID] {
		t.Fatalf("listed=%v err=%v", adIDs(ads)[lpAd.ID], err)
	}
	if _, _, err := svc.GetAdForViewer(context.Background(), lpAd.ID, uuid.New()); !errors.Is(err, ErrAdNotFound) {
		t.Fatalf("detail: %v", err)
	}
}

func TestLPAd_OwnerSeesOwnStockAdInMyAds(t *testing.T) {
	r := newPoolRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	lpAd := seedActiveAd(t, r.db, lp.UserID, poolTestChain, &lp.ID, "0", usdt18(10).String())
	ads, _ := r.svc.ListAds(context.Background(), AdFilter{SellerID: lp.UserID, ViewerID: lp.UserID}) // ListMyAds' filter
	if !adIDs(ads)[lpAd.ID] {
		t.Fatal("the owner must see their stock ad (to renew or reclaim)")
	}
}

// The hiding does not hang off ExcludeDelisted: any list that is not the
// caller's own hides LP stock ads, whatever its other filters.
func TestLPAd_HiddenFromEveryListButTheOwners(t *testing.T) {
	r := newPoolRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	lpAd := seedActiveAd(t, r.db, lp.UserID, poolTestChain, &lp.ID, "0", usdt18(10).String())
	for name, f := range map[string]AdFilter{
		"no filters":           {},
		"by seller, no viewer": {SellerID: lp.UserID},
		"by seller, stranger":  {SellerID: lp.UserID, ViewerID: uuid.New()},
		"viewer, no seller":    {ViewerID: lp.UserID},
		"pool chain":           {ChainID: poolTestChain, ViewerID: uuid.New()},
	} {
		ads, err := r.svc.ListAds(context.Background(), f)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if adIDs(ads)[lpAd.ID] {
			t.Errorf("%s lists the LP stock ad", name)
		}
	}
}

// assertHiddenFromStranger runs every public read against ad for a viewer who
// does not own it: browse, the chain filter, Express, detail and open.
func assertHiddenFromStranger(t *testing.T, svc *P2PTradeService, ad *models.P2PAd) {
	t.Helper()
	ctx := context.Background()
	for name, f := range map[string]AdFilter{
		"browse":       {Status: models.P2PAdStatusActive, ExcludeDelisted: true},
		"chain filter": {ChainID: ad.ChainID, ExcludeDelisted: true},
		"by seller":    {SellerID: ad.SellerID, Status: models.P2PAdStatusActive, ExcludeDelisted: true},
	} {
		ads, err := svc.ListAds(ctx, f)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if adIDs(ads)[ad.ID] {
			t.Errorf("%s lists the ad", name)
		}
	}
	ads, err := svc.ExpressMatch(ctx, ExpressMatchParams{Side: "buy", Token: "USDT", FiatCurrency: "NGN", Amount: "1000000"})
	if err != nil {
		t.Fatal(err)
	}
	if adIDs(ads)[ad.ID] {
		t.Error("Express offers the ad")
	}
	if _, _, err := svc.GetAdForViewer(ctx, ad.ID, uuid.New()); !errors.Is(err, ErrAdNotFound) {
		t.Errorf("detail for a stranger: %v, want ErrAdNotFound", err)
	}
	if _, _, err := svc.OpenTrade(ctx, ad.ID, uuid.New(), "0x00000000000000000000000000000000000000f1", OpenTradeParams{FiatAmount: "1000"}); !errors.Is(err, ErrAdNotActive) {
		t.Errorf("open: %v, want ErrAdNotActive", err)
	}
}

// Plan A P8: Chains() omits the pool chain, so delistedAdsClause never filters
// it. A pool-chain ad without an LP tag must still stay out of every read.
func TestPoolChainAd_HiddenWithoutLPTag(t *testing.T) {
	r := newPoolRig(t)
	ad := seedActiveAd(t, r.db, uuid.New(), poolTestChain, nil, "1000", usdt18(500000).String())
	assertHiddenFromStranger(t, r.svc, ad)
}

// Review Focus 1: the pool chain is repointed and the old chain becomes an
// ordinary P2P chain. The LP's ad now passes every chain check, so only its
// owner (the LP row) keeps it hidden.
func TestLPAd_HiddenWhenItsChainTurnsOrdinary(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	reg := NewP2PEscrowRegistryFromEntries(map[int64]P2PRegistryEntry{
		146:           {Escrow: newMockEscrow(), ChainKey: "sonic", Purpose: config.P2PPurposeP2P, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 6}},
		poolTestChain: {Escrow: newMockEscrow(), ChainKey: "bsc", Purpose: config.P2PPurposeP2P, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 18}},
	}, "2")
	svc := NewP2PTradeService(db, reg, &mockRates{}, &mockBus{}, 146)
	svc.SetTokenResolver(poolTestTokenAddr)
	svc.SetPMService(NewPMService(db))
	lp := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	lpAd := seedActiveAd(t, db, lp.UserID, poolTestChain, &lp.ID, "1000", usdt18(500000).String())
	assertHiddenFromStranger(t, svc, lpAd)
	if ad, pool, err := svc.GetAdForViewer(context.Background(), lpAd.ID, lp.UserID); err != nil || !pool || ad.ID != lpAd.ID {
		t.Fatalf("owner: pool=%v err=%v", pool, err)
	}
}

// Fail closed: with no PM service, a tagged ad cannot be proven not to be an
// LP's, so every public read hides it. An untagged ad is unaffected.
func TestTaggedAd_HiddenWithoutPMService(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db) // Sonic only, no PM service
	pmID := uuid.New()
	tagged := seedActiveAd(t, db, uuid.New(), 146, &pmID, "1000", "100000000")
	plain := seedActiveAd(t, db, uuid.New(), 146, nil, "1000", "100000000")
	assertHiddenFromStranger(t, svc, tagged)
	ads, err := svc.ListAds(context.Background(), AdFilter{Status: models.P2PAdStatusActive, ExcludeDelisted: true})
	if err != nil || !adIDs(ads)[plain.ID] {
		t.Fatalf("untagged ad lost: listed=%v err=%v", adIDs(ads)[plain.ID], err)
	}
	if _, pool, err := svc.GetAdForViewer(context.Background(), plain.ID, uuid.New()); err != nil || pool {
		t.Fatalf("untagged ad detail: pool=%v err=%v", pool, err)
	}
}
