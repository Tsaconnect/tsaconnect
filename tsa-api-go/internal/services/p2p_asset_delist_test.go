package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ─── MCGP delisted from P2P (owner decision 2026-09-28) ─────────────────────
//
// Wallet MCGP must not be usable in P2P, so MCGP left the Sonic allow-list.
// These tests pin both halves of that: every way of STARTING activity in a
// delisted asset is refused, and every way of FINISHING an existing position
// (open confirm, mark-paid, release, cancel, dispute, admin resolve, withdraw,
// deposit confirm, ad cancel) still works.

const (
	delistTestUSDT = "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
	delistTestMCGP = "0x7a1b2C3d4E5f60718293a4B5c6D7e8F901234567"
	// delistTestMaker is the seeded sell-ad's wallet: the on-chain seller.
	delistTestMaker = "0x000000000000000000000000000000000000a11c"
	// delistTestBuyer is the seeded trade's opener: the on-chain buyer.
	delistTestBuyer = "0x000000000000000000000000000000000000b0b0"
)

// delistTestService returns a trade service on chain 146 whose registry starts
// with MCGP listed, plus a delist func that removes it the way the config
// change does. Ads created before delist() model the ads live in prod today.
func delistTestService(t *testing.T) (svc *P2PTradeService, esc *mockEscrow, db *gorm.DB, delist func()) {
	t.Helper()
	db = setupTradeDB(t)
	esc = newMockEscrow()
	reg := &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{
		146: {service: esc, eip712Version: "1", domainChainID: 146, assets: []string{"USDT", "USDC", "MCGP"}},
	}}
	svc = NewP2PTradeService(db, reg, &mockRates{}, &mockBus{}, 146).
		WithClock(fixedClock{t: time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)})
	svc.SetTokenResolver(func(chainID int64, symbol string) string {
		switch symbol {
		case "USDT":
			return delistTestUSDT
		case "MCGP":
			return delistTestMCGP
		}
		return ""
	})
	return svc, esc, db, func() { reg.byChain[146].assets = []string{"USDT", "USDC"} }
}

func mcgpAdParams(pmID uuid.UUID) CreateAdParams {
	p := makeCreateAdParams(pmID)
	p.Token = "MCGP"
	return p
}

// createSignedAd publishes a sell-ad (CreateAd + SignAd) and returns its id.
func createSignedAd(t *testing.T, svc *P2PTradeService, sellerID uuid.UUID, params CreateAdParams) uuid.UUID {
	t.Helper()
	key, addr := newSellerKey(t)
	res, err := svc.CreateAd(context.Background(), sellerID, addr, params)
	if err != nil {
		t.Fatalf("CreateAd(%s): %v", params.Token, err)
	}
	var hash [32]byte
	copy(hash[:], common.FromHex(res.Ad.VoucherHash))
	if _, err := svc.SignAd(context.Background(), res.Ad.ID, sellerID, addr, signVoucherForTest(t, hash, key)); err != nil {
		t.Fatalf("SignAd(%s): %v", params.Token, err)
	}
	return res.Ad.ID
}

func TestDelistedAsset_NewActivityRefused(t *testing.T) {
	svc, esc, db, delist := delistTestService(t)
	delist()
	ctx := context.Background()
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	_, addr := newSellerKey(t)

	t.Run("sell ad", func(t *testing.T) {
		_, err := svc.CreateAd(ctx, sellerID, addr, mcgpAdParams(pmID))
		if !errors.Is(err, ErrAssetNotTradeable) || !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("CreateAd(MCGP sell) err = %v, want ErrAssetNotTradeable + ErrInvalidParams", err)
		}
	})
	t.Run("buy ad", func(t *testing.T) {
		p := mcgpAdParams(pmID)
		p.Side = "buy"
		_, err := svc.CreateAd(ctx, sellerID, addr, p)
		if !errors.Is(err, ErrAssetNotTradeable) {
			t.Fatalf("CreateAd(MCGP buy) err = %v, want ErrAssetNotTradeable", err)
		}
	})
	var ads int64
	db.Model(&models.P2PAd{}).Count(&ads)
	if ads != 0 {
		t.Fatalf("no ad row may be written for a delisted asset, found %d", ads)
	}

	t.Run("pool approve and deposit, by symbol or by address", func(t *testing.T) {
		for _, tok := range []string{"MCGP", "mcgp", delistTestMCGP} {
			if _, err := svc.PrepareDeposit(ctx, sellerID, addr, tok, "1000", 146); !errors.Is(err, ErrAssetNotTradeable) {
				t.Errorf("PrepareDeposit(%s) err = %v, want ErrAssetNotTradeable", tok, err)
			}
			if _, err := svc.PrepareApprove(ctx, sellerID, addr, tok, "1000", 146); !errors.Is(err, ErrAssetNotTradeable) {
				t.Errorf("PrepareApprove(%s) err = %v, want ErrAssetNotTradeable", tok, err)
			}
		}
		if len(esc.preparedTxs) != 0 {
			t.Fatalf("no tx may be prepared for a delisted asset, got %v", esc.preparedTxs)
		}
	})

	t.Run("listed assets unaffected", func(t *testing.T) {
		for _, tok := range []string{"USDT", delistTestUSDT} {
			if _, err := svc.PrepareDeposit(ctx, sellerID, addr, tok, "1000", 146); err != nil {
				t.Errorf("PrepareDeposit(%s): %v", tok, err)
			}
		}
		if _, err := svc.CreateAd(ctx, sellerID, addr, makeCreateAdParams(pmID)); err != nil {
			t.Errorf("CreateAd(USDT): %v", err)
		}
	})
}

func TestDelistedAsset_ExistingAdsCannotTakeTradesOrRelist(t *testing.T) {
	svc, esc, db, delist := delistTestService(t)
	ctx := context.Background()
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	// Everything here exists before the delist, as in prod.
	liveSell := createSignedAd(t, svc, sellerID, mcgpAdParams(pmID))
	pausedSell := createSignedAd(t, svc, sellerID, mcgpAdParams(pmID))
	if err := svc.PauseAd(ctx, pausedSell, sellerID); err != nil {
		t.Fatalf("PauseAd: %v", err)
	}
	_, draftAddr := newSellerKey(t)
	draft, err := svc.CreateAd(ctx, sellerID, draftAddr, mcgpAdParams(pmID))
	if err != nil {
		t.Fatalf("CreateAd(draft): %v", err)
	}
	buyParams := mcgpAdParams(pmID)
	buyParams.Side = "buy"
	buyAd, err := svc.CreateAd(ctx, sellerID, draftAddr, buyParams)
	if err != nil {
		t.Fatalf("CreateAd(buy): %v", err)
	}

	delist()

	buyerID := seedVerifiedUser(t, db)
	if _, _, err := svc.OpenTrade(ctx, liveSell, buyerID, "0x000000000000000000000000000000000000bEEF",
		OpenTradeParams{FiatAmount: "1000", PaymentMethodID: pmID}); !errors.Is(err, ErrAssetNotTradeable) {
		t.Errorf("OpenTrade(MCGP sell-ad) err = %v, want ErrAssetNotTradeable", err)
	}
	if _, _, err := svc.OpenTrade(ctx, buyAd.Ad.ID, buyerID, "0x000000000000000000000000000000000000bEEF",
		OpenTradeParams{TokenAmount: "1000", PaymentMethodID: pmID}); !errors.Is(err, ErrAssetNotTradeable) {
		t.Errorf("OpenTrade(MCGP buy-ad) err = %v, want ErrAssetNotTradeable", err)
	}
	var trades int64
	db.Model(&models.P2PTrade{}).Count(&trades)
	if trades != 0 {
		t.Errorf("no trade row may be written against a delisted asset, found %d", trades)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", liveSell)
	if ad.Remaining != "100000000" || ad.Reserved != "0" {
		t.Errorf("refused open must not touch capacity: remaining=%s reserved=%s", ad.Remaining, ad.Reserved)
	}
	if len(esc.preparedTxs) != 0 {
		t.Errorf("no open tx may be prepared, got %v", esc.preparedTxs)
	}

	// A drafted ad can't be published, a paused one can't be resumed.
	// The gate fires before signature recovery, so any 65-byte sig reaches it.
	if _, err := svc.SignAd(ctx, draft.Ad.ID, sellerID, draftAddr, make([]byte, 65)); !errors.Is(err, ErrAssetNotTradeable) {
		t.Errorf("SignAd(MCGP draft) err = %v, want ErrAssetNotTradeable", err)
	}
	if err := svc.ResumeAd(ctx, pausedSell, sellerID); !errors.Is(err, ErrAssetNotTradeable) {
		t.Errorf("ResumeAd(MCGP) err = %v, want ErrAssetNotTradeable", err)
	}
	var paused models.P2PAd
	db.First(&paused, "id = ?", pausedSell)
	if paused.Status != models.P2PAdStatusPaused {
		t.Errorf("refused resume must leave the ad paused, got %s", paused.Status)
	}

	// The owner can still take their ads down.
	if err := svc.PauseAd(ctx, liveSell, sellerID); err != nil {
		t.Errorf("PauseAd(MCGP) must still work: %v", err)
	}
	_, ownerAddr := newSellerKey(t)
	if _, err := svc.PrepareCancelAd(ctx, pausedSell, sellerID, ownerAddr); err != nil {
		t.Errorf("PrepareCancelAd(MCGP) must still work: %v", err)
	}
	if _, err := svc.CancelBuyAd(ctx, buyAd.Ad.ID, sellerID); err != nil {
		t.Errorf("CancelBuyAd(MCGP) must still work: %v", err)
	}
}

func TestDelistedAsset_HiddenFromBrowseButNotFromOwner(t *testing.T) {
	svc, _, db, delist := delistTestService(t)
	delist()
	ctx := context.Background()
	sellerID := uuid.New()
	mk := func(token string) uuid.UUID {
		id := uuid.New()
		if err := db.Create(&models.P2PAd{
			ID: id, SellerID: sellerID, Side: models.P2PAdSideSell, Token: token, ChainID: 146,
			FiatCurrency: "NGN", Rate: "1500", RateType: models.P2PAdRateTypeFixed,
			FloatAnchor: models.P2PAdFloatAnchorBybitSell,
			Amount:      "100000000", Remaining: "100000000", Reserved: "0",
			MinLimit: models.OptionalNumeric("0"), MaxLimit: models.OptionalNumeric("1000000"),
			Status: models.P2PAdStatusActive,
		}).Error; err != nil {
			t.Fatalf("create ad: %v", err)
		}
		return id
	}
	usdt := mk("USDT")
	mcgp := mk("MCGP")

	browse, err := svc.ListAds(ctx, AdFilter{Status: models.P2PAdStatusActive, ExcludeDelisted: true})
	if err != nil {
		t.Fatalf("ListAds(browse): %v", err)
	}
	if len(browse) != 1 || browse[0].ID != usdt {
		t.Errorf("browse must show only the USDT ad, got %d ads", len(browse))
	}

	mine, err := svc.ListAds(ctx, AdFilter{SellerID: sellerID})
	if err != nil {
		t.Fatalf("ListAds(mine): %v", err)
	}
	seen := map[uuid.UUID]bool{}
	for _, a := range mine {
		seen[a.ID] = true
	}
	if !seen[usdt] || !seen[mcgp] {
		t.Errorf("owner list must still show the MCGP ad (to cancel it), got %v", seen)
	}

	matches, err := svc.ExpressMatch(ctx, ExpressMatchParams{Side: "buy", Token: "MCGP", FiatCurrency: "NGN", Amount: "1000000"})
	if err != nil {
		t.Fatalf("ExpressMatch: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("express match must not offer a delisted asset, got %d", len(matches))
	}
}

// seedMCGPTrade writes an MCGP trade (and its ad, holding the trade's amount
// as reserved) the way an in-flight trade opened before the delist looks.
func seedMCGPTrade(t *testing.T, db *gorm.DB, sellerID, buyerID uuid.UUID, status, openTx string, expiresAt time.Time) *models.P2PTrade {
	t.Helper()
	adID := uuid.New()
	if err := db.Create(&models.P2PAd{
		ID: adID, SellerID: sellerID, Side: models.P2PAdSideSell, Token: "MCGP", ChainID: 146,
		FiatCurrency: "NGN", Rate: "30", RateType: models.P2PAdRateTypeFixed,
		FloatAnchor: models.P2PAdFloatAnchorBybitSell,
		Amount:      "1000", Remaining: "900", Reserved: "100",
		Status: models.P2PAdStatusActive, SellerAddress: delistTestMaker,
	}).Error; err != nil {
		t.Fatalf("seed ad: %v", err)
	}
	now := time.Date(2026, 5, 23, 11, 0, 0, 0, time.UTC)
	tr := &models.P2PTrade{
		ID: uuid.New(), AdID: adID, BuyerID: buyerID, SellerID: sellerID, OpenerID: &buyerID,
		OpenerAddress: common.HexToAddress(delistTestBuyer).Hex(),
		Token:         "MCGP", ChainID: 146, Funding: models.P2PTradeFundingPooled,
		TokenAmount: "100", FiatAmount: "3000", FiatCurrency: "NGN", Rate: "30",
		PaymentMethodID: uuid.New(), Status: status, OpenTxHash: openTx,
		ExpiresAt: expiresAt, CreatedAt: now, UpdatedAt: now,
	}
	tr.OnChainTradeID = HashIDHex(tr.ID)
	if err := db.Create(tr).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	return tr
}

func tradeStatus(t *testing.T, db *gorm.DB, id uuid.UUID) string {
	t.Helper()
	var tr models.P2PTrade
	if err := db.First(&tr, "id = ?", id).Error; err != nil {
		t.Fatalf("load trade: %v", err)
	}
	return tr.Status
}

func TestDelistedAsset_ExistingPositionsStillExit(t *testing.T) {
	svc, esc, db, delist := delistTestService(t)
	delist()
	ctx := context.Background()
	_, sellerAddr := newSellerKey(t)
	later := time.Date(2026, 5, 23, 13, 0, 0, 0, time.UTC)    // after the fixed clock
	expired := time.Date(2026, 5, 23, 11, 30, 0, 0, time.UTC) // before it

	t.Run("confirm open, mark paid, release", func(t *testing.T) {
		sellerID, buyerID := uuid.New(), uuid.New()
		// Prepared before the delist, broadcast after: the confirm must land.
		tr := seedMCGPTrade(t, db, sellerID, buyerID, models.P2PTradeStatusPendingPayment, "", later)
		esc.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending,
			Funding: models.P2PTradeFundingPooled, Seller: common.HexToAddress(delistTestMaker),
			Buyer: common.HexToAddress(delistTestBuyer),
			Token: common.HexToAddress(delistTestMCGP), Amount: big.NewInt(100)}
		if _, err := svc.ConfirmOpenTrade(ctx, tr.ID, buyerID, "0xopen"); err != nil {
			t.Fatalf("ConfirmOpenTrade: %v", err)
		}
		esc.onChainTrade = nil
		markPaidViaChain(t, svc, tr.ID, buyerID)
		if _, err := svc.PrepareRelease(ctx, tr.ID, sellerID, sellerAddr); err != nil {
			t.Fatalf("PrepareRelease: %v", err)
		}
		if _, err := svc.ConfirmRelease(ctx, tr.ID, sellerID, "0xrel"); err != nil {
			t.Fatalf("ConfirmRelease: %v", err)
		}
		if got := tradeStatus(t, db, tr.ID); got != models.P2PTradeStatusReleased {
			t.Errorf("status = %s, want released", got)
		}
	})

	t.Run("timeout cancel", func(t *testing.T) {
		sellerID, buyerID := uuid.New(), uuid.New()
		tr := seedMCGPTrade(t, db, sellerID, buyerID, models.P2PTradeStatusPendingPayment, "0xopen", expired)
		if _, err := svc.PrepareCancel(ctx, tr.ID, sellerID, sellerAddr); err != nil {
			t.Fatalf("PrepareCancel: %v", err)
		}
		if _, err := svc.ConfirmCancel(ctx, tr.ID, sellerID, "0xcancel"); err != nil {
			t.Fatalf("ConfirmCancel: %v", err)
		}
		if got := tradeStatus(t, db, tr.ID); got != models.P2PTradeStatusCancelled {
			t.Errorf("status = %s, want cancelled", got)
		}
	})

	t.Run("dispute and admin resolve", func(t *testing.T) {
		sellerID, buyerID := uuid.New(), uuid.New()
		tr := seedMCGPTrade(t, db, sellerID, buyerID, models.P2PTradeStatusPaid, "0xopen", later)
		if _, err := svc.PrepareRaiseDispute(ctx, tr.ID, buyerID, "0x000000000000000000000000000000000000bEEF", "seller unresponsive", nil); err != nil {
			t.Fatalf("PrepareRaiseDispute: %v", err)
		}
		if _, err := svc.ConfirmRaiseDispute(ctx, tr.ID, buyerID, "0xdispute"); err != nil {
			t.Fatalf("ConfirmRaiseDispute: %v", err)
		}
		adminID := uuid.New()
		if _, err := svc.PrepareAdminResolveDispute(ctx, tr.ID, adminID, sellerAddr, false, "paid"); err != nil {
			t.Fatalf("PrepareAdminResolveDispute: %v", err)
		}
		if _, err := svc.ConfirmAdminResolveDispute(ctx, tr.ID, adminID, "0xresolve"); err != nil {
			t.Fatalf("ConfirmAdminResolveDispute: %v", err)
		}
		if got := tradeStatus(t, db, tr.ID); got != models.P2PTradeStatusResolved {
			t.Errorf("status = %s, want resolved", got)
		}
	})

	t.Run("pool withdraw, balance read, late deposit confirm", func(t *testing.T) {
		sellerID := uuid.New()
		esc.balance = OnChainBalance{Available: big.NewInt(1_000), Locked: big.NewInt(0)}
		if _, err := svc.GetPoolBalance(ctx, sellerAddr, "MCGP", 146); err != nil {
			t.Fatalf("GetPoolBalance(MCGP): %v", err)
		}
		if _, err := svc.PrepareWithdraw(ctx, sellerID, sellerAddr, "MCGP", "50", 146); err != nil {
			t.Fatalf("PrepareWithdraw(MCGP): %v", err)
		}
		if esc.lastWithdrawArgs.Token != delistTestMCGP {
			t.Errorf("withdraw token = %s, want MCGP address", esc.lastWithdrawArgs.Token)
		}
		if _, err := svc.ConfirmWithdraw(ctx, sellerID, sellerAddr, "MCGP", "50", "0xwd", 146); err != nil {
			t.Fatalf("ConfirmWithdraw(MCGP): %v", err)
		}
		// A deposit broadcast before the delist has already moved the funds.
		if _, err := svc.ConfirmDeposit(ctx, sellerID, sellerAddr, "MCGP", "100", "0xdep", 146); err != nil {
			t.Fatalf("ConfirmDeposit(MCGP): %v", err)
		}
	})
}

func TestDelistedAsset_RegistryDelistedVsUnconfigured(t *testing.T) {
	reg := &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{
		146: {assets: []string{"USDT", "USDC"}},
		56:  {}, // no list = allow all
	}}
	if !reg.AssetDelisted(146, "MCGP") || !reg.AssetDelisted(146, "mcgp") {
		t.Error("MCGP must read as delisted on a chain whose list excludes it")
	}
	if reg.AssetDelisted(146, "USDT") {
		t.Error("USDT is listed")
	}
	if reg.AssetDelisted(56, "MCGP") {
		t.Error("an empty list allows everything, so nothing is delisted")
	}
	if reg.AssetDelisted(999, "MCGP") {
		t.Error("an unconfigured chain is not 'delisted' — its callers fail on resolve")
	}
}

// MCGP trades still settling after the delist must not earn cashback/TP at the
// $1-per-token stablecoin valuation (MCGP trades near $0.02).
func TestP2PFee_NonStableToken_NoAccrual(t *testing.T) {
	db := setupP2PFeeDB(t)
	svc, esc, _ := newP2PFeeService(t, db)
	esc.platformFeeBpsSnapshot = 100 // V3 trade: a 1% platform fee WOULD accrue for a stable

	buyer := mkP2PUser(t, db, "mcgpbuyer", nil)
	seller := mkP2PUser(t, db, "mcgpseller", nil)
	adID := seedAd(t, db, seller.ID, "0", "100")
	trade := seedPaidTrade(t, db, adID, buyer.ID, seller.ID, "100")
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("token", "MCGP")

	if _, err := svc.ConfirmRelease(context.Background(), trade.ID, seller.ID, "0xrel"); err != nil {
		t.Fatalf("ConfirmRelease: %v", err)
	}
	var rel models.P2PTrade
	db.First(&rel, "id = ?", trade.ID)
	svc.accrueP2PReleaseFee(&rel, big.NewInt(1)) // the seam directly, too

	time.Sleep(200 * time.Millisecond)
	if got := guardRows(t, db, trade.ID); got != 0 {
		t.Fatalf("an MCGP trade must accrue nothing, found %d guard rows", got)
	}
	var b models.User
	db.First(&b, "id = ?", buyer.ID)
	if b.CashbackBalance != 0 || b.TPBalance != 0 {
		t.Errorf("buyer must get no TP/cashback: tp=%v cb=%v", b.TPBalance, b.CashbackBalance)
	}
}
