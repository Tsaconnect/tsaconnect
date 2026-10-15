package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

const bscDirectChain = int64(56)

// audienceRig is the switched-on world: Sonic (146, 6 dp) for Payment
// Merchants only, and the direct BSC escrow (56, 18 dp) for everyone else.
type audienceRig struct {
	db         *gorm.DB
	svc        *P2PTradeService
	sonic, bsc *mockEscrow
}

func newAudienceRig(t *testing.T) *audienceRig {
	t.Helper()
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	sonic, bsc := newMockEscrow(), newMockEscrow()
	bsc.contractAddress = "0x00000000000000000000000000000000000B5C56"
	reg := NewP2PEscrowRegistryFromEntries(map[int64]P2PRegistryEntry{
		146: {Escrow: sonic, ChainKey: "sonic", Purpose: config.P2PPurposeP2P, Assets: []string{"USDT"},
			Decimals: map[string]int{"USDT": 6}, Audience: config.P2PAudiencePM},
		bscDirectChain: {Escrow: bsc, ChainKey: "bsc", Purpose: config.P2PPurposeP2P, Assets: []string{"USDT", "USDC"},
			Decimals: map[string]int{"USDT": 18, "USDC": 18}, Audience: config.P2PAudienceDirect},
	}, "2")
	svc := NewP2PTradeService(db, reg, &mockRates{}, &mockBus{}, 146).
		WithClock(fixedClock{t: time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)})
	svc.SetTokenResolver(func(int64, string) string { return "0x29219dd400f2Bf60E5a23d13Be72B486D4038894" })
	svc.SetPMService(NewPMService(db))
	sonic.balance = OnChainBalance{Available: big.NewInt(1_000_000_000), Locked: big.NewInt(0)}
	bsc.balance = OnChainBalance{Available: usdt18(1_000), Locked: big.NewInt(0)}
	return &audienceRig{db: db, svc: svc, sonic: sonic, bsc: bsc}
}

func wantRefusal(t *testing.T, what string, err, want error) {
	t.Helper()
	var r p2pRefusal
	if !errors.Is(err, want) || !errors.As(err, &r) {
		t.Fatalf("%s: err = %v, want the %v refusal", what, err, want)
	}
}

func notAudienceRefused(t *testing.T, what string, err error) {
	t.Helper()
	if errors.Is(err, ErrP2PDirectMoved) || errors.Is(err, ErrPMNotOnDirect) {
		t.Fatalf("%s: refused by the audience gate: %v", what, err)
	}
}

// Posting: a non-PM is sent to BSC (an omitted chainId is Sonic, never BSC);
// a PM is kept off the direct escrow.
func TestAudience_CreateAd(t *testing.T) {
	r := newAudienceRig(t)
	ctx := context.Background()
	user := uuid.New()
	method := seedPaymentMethod(t, r.db, user, "NGN")
	_, addr := newSellerKey(t)

	p := makeCreateAdParams(method)
	_, err := r.svc.CreateAd(ctx, user, addr, p) // chainId omitted → Sonic
	wantRefusal(t, "non-PM sell ad, default chain", err, ErrP2PDirectMoved)
	p.ChainID = 146
	_, err = r.svc.CreateAd(ctx, user, addr, p)
	wantRefusal(t, "non-PM sell ad on Sonic", err, ErrP2PDirectMoved)
	buy := makeCreateAdParams(method)
	buy.Side = models.P2PAdSideBuy
	_, err = r.svc.CreateAd(ctx, user, addr, buy)
	wantRefusal(t, "non-PM buy ad on Sonic", err, ErrP2PDirectMoved)

	p.ChainID, p.TokenAmount = bscDirectChain, usdt18(100).String()
	res, err := r.svc.CreateAd(ctx, user, addr, p)
	if err != nil {
		t.Fatalf("non-PM sell ad on BSC: %v", err)
	}
	if res.Ad.ChainID != bscDirectChain {
		t.Fatalf("ad on chain %d", res.Ad.ChainID)
	}

	pm := seedActivePM(t, r.db, "0123456789", 0)
	_, err = r.svc.CreateAd(ctx, pm.UserID, addr, p)
	wantRefusal(t, "PM on BSC direct", err, ErrPMNotOnDirect)
	p.ChainID, p.TokenAmount = 146, "100000000"
	_, err = r.svc.CreateAd(ctx, pm.UserID, addr, p)
	notAudienceRefused(t, "PM on Sonic", err)
}

// Signing, resuming and funding are new activity too; withdrawing is an exit.
func TestAudience_SignResumeDepositWithdraw(t *testing.T) {
	r := newAudienceRig(t)
	ctx := context.Background()
	user := uuid.New()
	_, addr := newSellerKey(t)

	pending := seedActiveAd(t, r.db, user, 146, nil, "1500", "100000000")
	r.db.Model(pending).Updates(map[string]any{"status": models.P2PAdStatusPending, "voucher_signature": ""})
	_, err := r.svc.SignAd(ctx, pending.ID, user, addr, make([]byte, 65))
	wantRefusal(t, "SignAd on Sonic", err, ErrP2PDirectMoved)

	paused := seedActiveAd(t, r.db, user, 146, nil, "1500", "100000000")
	r.db.Model(paused).Update("status", models.P2PAdStatusPaused)
	wantRefusal(t, "ResumeAd on Sonic", r.svc.ResumeAd(ctx, paused.ID, user), ErrP2PDirectMoved)
	bscPaused := seedActiveAd(t, r.db, user, bscDirectChain, nil, "1500", usdt18(100).String())
	r.db.Model(bscPaused).Update("status", models.P2PAdStatusPaused)
	if err := r.svc.ResumeAd(ctx, bscPaused.ID, user); err != nil {
		t.Fatalf("ResumeAd on BSC: %v", err)
	}

	_, err = r.svc.PrepareDeposit(ctx, user, addr, "USDT", "1000", 0)
	wantRefusal(t, "deposit on Sonic (default)", err, ErrP2PDirectMoved)
	if _, err := r.svc.PrepareDeposit(ctx, user, addr, "USDT", usdt18(1).String(), bscDirectChain); err != nil {
		t.Fatalf("deposit on BSC: %v", err)
	}
	pm := seedActivePM(t, r.db, "0123456789", 0)
	if _, err := r.svc.PrepareDeposit(ctx, pm.UserID, addr, "USDT", "1000", 146); err != nil {
		t.Fatalf("PM deposit on Sonic: %v", err)
	}
	if _, err := r.svc.PrepareWithdraw(ctx, user, addr, "USDT", "1000", 146); err != nil {
		t.Fatalf("non-PM withdraw on Sonic (an exit): %v", err)
	}
	if _, err := r.svc.PrepareApprove(ctx, user, addr, "USDT", "1000", 146); err != nil {
		t.Fatalf("approve is not gated (a PM buy-ad's JIT open needs one): %v", err)
	}
}

// Opening: on Sonic only a PM ad opens; browse hides the rest from everyone
// but their owner; BSC ads open and list.
func TestAudience_OpenAndBrowse(t *testing.T) {
	r := newAudienceRig(t)
	ctx := context.Background()
	seller, opener := uuid.New(), uuid.New()
	pm := seedActivePM(t, r.db, "0123456789", 0)
	sonicAd := seedActiveAd(t, r.db, seller, 146, nil, "1500", "100000000")
	pmAd := seedActiveAd(t, r.db, pm.UserID, 146, &pm.ID, "1500", "100000000")
	bscAd := seedActiveAd(t, r.db, seller, bscDirectChain, nil, "1500", usdt18(100).String())

	_, _, err := r.svc.OpenTrade(ctx, sonicAd.ID, opener, "0x000000000000000000000000000000000000b0b0", OpenTradeParams{})
	wantRefusal(t, "open a non-PM Sonic ad", err, ErrP2PDirectMoved)
	_, _, err = r.svc.OpenTrade(ctx, pmAd.ID, opener, "0x000000000000000000000000000000000000b0b0", OpenTradeParams{})
	notAudienceRefused(t, "open a PM Sonic ad", err)
	_, _, err = r.svc.OpenTrade(ctx, bscAd.ID, opener, "0x000000000000000000000000000000000000b0b0", OpenTradeParams{})
	notAudienceRefused(t, "open a BSC ad", err)

	ads, err := r.svc.ListAds(ctx, AdFilter{Status: models.P2PAdStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	got := adIDs(ads)
	if got[sonicAd.ID] || !got[pmAd.ID] || !got[bscAd.ID] {
		t.Fatalf("browse = %v; want the PM and BSC ads, not the moved Sonic ad", got)
	}
	own, err := r.svc.ListAds(ctx, AdFilter{SellerID: seller, ViewerID: seller})
	if err != nil {
		t.Fatal(err)
	}
	if !adIDs(own)[sonicAd.ID] {
		t.Fatal("the owner must still see her moved ad (to cancel it)")
	}
}

// GET /p2p/chains: adsOpen per caller.
func TestAudience_ChainsForAdsOpen(t *testing.T) {
	r := newAudienceRig(t)
	pm := seedActivePM(t, r.db, "0123456789", 0)
	open := func(user uuid.UUID) map[int64]bool {
		out := map[int64]bool{}
		for _, c := range r.svc.ChainsFor(context.Background(), user) {
			if c.AdsOpen == nil {
				t.Fatalf("chain %d has no adsOpen", c.ChainID)
			}
			out[c.ChainID] = *c.AdsOpen
		}
		return out
	}
	if got := open(uuid.New()); got[146] || !got[bscDirectChain] {
		t.Fatalf("non-PM adsOpen = %v, want Sonic closed and BSC open", got)
	}
	if got := open(pm.UserID); !got[146] || got[bscDirectChain] {
		t.Fatalf("PM adsOpen = %v, want Sonic open and BSC closed", got)
	}
}

// PM automation never settles on the direct escrow (defence in depth).
func TestAudience_AutoReleaseRefusedOnDirect(t *testing.T) {
	r := newAudienceRig(t)
	ad := seedActiveAd(t, r.db, uuid.New(), bscDirectChain, nil, "1500", usdt18(100).String())
	tr := seedBuyTradeOnAd(t, r.db, ad, uuid.New(), "150000")
	r.db.Model(tr).Updates(map[string]any{"chain_id": bscDirectChain, "status": models.P2PTradeStatusPaid})
	sender := releaseSender()
	if _, err := r.svc.AutoRelease(context.Background(), tr.ID, sender); !errors.Is(err, ErrForbidden) {
		t.Fatalf("AutoRelease on BSC direct: %v, want ErrForbidden", err)
	}
	if len(sender.sendChains) != 0 || sender.direct != 0 {
		t.Fatalf("sent txs on %v", sender.sendChains)
	}
}

// Every exit of an existing position on a PM-only escrow still works for a
// non-PM: the gate stops new activity only.
func TestAudience_PMEscrowExitsStillWork(t *testing.T) {
	svc, esc, db, delist := delistTestService(t)
	delist()
	svc.escrows.byChain[146].audience = config.P2PAudiencePM
	exitPathsStillWork(t, svc, esc, db)
}

// Plan API-6: Express compares one base-unit amount against ads of ONE chain.
// 1 USDT is 1e6 on Sonic and 1e18 on BSC; read across chains it would match a
// BSC ad for 1e-12 USDT or a Sonic ad for a trillion.
func TestExpressMatch_AmountReadOnTheRequestedChainOnly(t *testing.T) {
	r := newAudienceRig(t)
	r.svc.escrows.byChain[146].audience = config.P2PAudienceAll // both chains browsable
	ctx := context.Background()
	sonicAd := seedActiveAd(t, r.db, uuid.New(), 146, nil, "1500", "100000000")                   // 100 USDT, 6 dp
	bscAd := seedActiveAd(t, r.db, uuid.New(), bscDirectChain, nil, "1500", usdt18(100).String()) // 100 USDT, 18 dp
	for name, tc := range map[string]struct {
		chain    int64
		amount   string
		want     uuid.UUID
		not      uuid.UUID
		onChainN int64
	}{
		"default is Sonic": {0, "1000000", sonicAd.ID, bscAd.ID, 146},
		"Sonic":            {146, "1000000", sonicAd.ID, bscAd.ID, 146},
		"BSC":              {bscDirectChain, usdt18(1).String(), bscAd.ID, sonicAd.ID, bscDirectChain},
		// A Sonic-scaled amount sent for BSC is read as 1e-12 USDT there: it
		// can never pick up the Sonic ad it was sized for.
		"6-dp amount on BSC": {bscDirectChain, "1000000", uuid.Nil, sonicAd.ID, bscDirectChain},
	} {
		ads, err := r.svc.ExpressMatch(ctx, ExpressMatchParams{Side: "buy", Token: "USDT", FiatCurrency: "NGN", Amount: tc.amount, ChainID: tc.chain})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := adIDs(ads)
		if (tc.want != uuid.Nil && !got[tc.want]) || got[tc.not] {
			t.Errorf("%s: matched %v", name, got)
		}
		for _, ad := range ads {
			if ad.ChainID != tc.onChainN {
				t.Errorf("%s: ad on chain %d", name, ad.ChainID)
			}
		}
	}
}

// The gas need answers the chain the tx is on, and follows the audience.
func TestAudience_GasNeedPerChain(t *testing.T) {
	r := newAudienceRig(t)
	ctx := context.Background()
	user := &models.User{ID: uuid.New(), WalletAddress: gasNeedWallet}
	need := func(req P2PGasRequest) (int64, error) {
		return r.svc.RequireP2PGasNeed(ctx, user, gasNeedWallet, req)
	}
	if _, err := need(P2PGasRequest{Action: P2PGasDeposit}); !errors.Is(err, ErrP2PGasNotNeeded) {
		t.Fatalf("non-PM deposit gas on Sonic: %v, want gas_not_needed", err)
	}
	if id, err := need(P2PGasRequest{Action: P2PGasDeposit, ChainID: bscDirectChain}); err != nil || id != bscDirectChain {
		t.Fatalf("deposit gas on BSC = %d, %v", id, err)
	}
	if _, err := need(P2PGasRequest{Action: P2PGasDeposit, ChainID: 97}); !errors.Is(err, ErrP2PGasNotNeeded) {
		t.Fatalf("a chain with no escrow: %v, want gas_not_needed", err)
	}
	r.bsc.balance = OnChainBalance{Available: big.NewInt(1), Locked: big.NewInt(0)}
	r.sonic.balance = OnChainBalance{Available: big.NewInt(0), Locked: big.NewInt(0)}
	if id, err := need(P2PGasRequest{Action: P2PGasWithdraw, ChainID: bscDirectChain}); err != nil || id != bscDirectChain {
		t.Fatalf("withdraw gas on BSC = %d, %v", id, err)
	}
	if _, err := need(P2PGasRequest{Action: P2PGasWithdraw}); !errors.Is(err, ErrP2PGasNotNeeded) {
		t.Fatalf("withdraw gas on Sonic with nothing there: %v", err)
	}

	sonicAd := seedActiveAd(t, r.db, uuid.New(), 146, nil, "1500", "100000000")
	if _, err := need(P2PGasRequest{Action: P2PGasOpenTrade, AdID: sonicAd.ID}); !errors.Is(err, ErrP2PGasNotNeeded) {
		t.Fatalf("open_trade gas on a moved Sonic ad: %v", err)
	}
	bscAd := seedActiveAd(t, r.db, uuid.New(), bscDirectChain, nil, "1500", usdt18(100).String())
	if _, err := need(P2PGasRequest{Action: P2PGasCancelAd, AdID: bscAd.ID, ChainID: 146}); !errors.Is(err, ErrP2PGasNotNeeded) {
		t.Fatalf("a chainId that is not the ad's: %v", err)
	}
	tr := seedBuyTradeOnAd(t, r.db, bscAd, user.ID, "150000")
	r.db.Model(tr).Update("chain_id", bscDirectChain)
	id, err := need(P2PGasRequest{Action: P2PGasRelease, TradeID: tr.ID})
	if id != bscDirectChain || !errors.Is(err, ErrP2PGasNotNeeded) { // she is the buyer: not her release
		t.Fatalf("release gas = %d, %v; want chain 56 and not her turn", id, err)
	}
}
