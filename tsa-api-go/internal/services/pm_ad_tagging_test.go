package services

// pm_ad_tagging_test.go — Task 6: auto-tag active-PM users' ads as PM ads,
// and enforce that the ad's wallet address equals the PM's maker wallet.

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ─── Helpers ────────────────────────────────────────────────────────────────

// setupAdTaggingDB builds a test DB with both p2p_ads (from setupTradeDB) and
// payment_merchants (from createPaymentMerchantsTable) tables.
func setupAdTaggingDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	return db
}

// seedActivePMForUser inserts an active PaymentMerchant whose UserID matches
// the given user and whose OperatorWalletAddress is the supplied maker wallet.
func seedActivePMForUser(t *testing.T, db *gorm.DB, userID uuid.UUID, makerWallet string) *models.PaymentMerchant {
	t.Helper()
	pm := &models.PaymentMerchant{
		ID:                    uuid.New(),
		UserID:                userID,
		Name:                  "Corporate PM",
		Status:                models.PaymentMerchantStatusActive,
		InboundAccountNumber:  "9999999999",
		InboundBank:           "Nomba",
		OperatorWalletAddress: makerWallet,
		NombaWalletID:         "wallet-tagging-test",
		PerTradeCapUSD:        0,
		DailyCapUSD:           0,
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seedActivePMForUser: %v", err)
	}
	return pm
}

// newServiceWithPM builds a P2PTradeService (the same way newService does) and
// additionally wires a PMService so the ad-tagging logic is active.
func newServiceWithPM(t *testing.T, db *gorm.DB) (*P2PTradeService, *mockEscrow, *mockBus) {
	t.Helper()
	svc, esc, bus := newService(t, db)
	svc.SetPMService(NewPMService(db))
	return svc, esc, bus
}

// ─── Tests ──────────────────────────────────────────────────────────────────

// (a) Active-PM user creates a sell-ad with sellerAddress == PM maker wallet
// → ad.PMID is set to the PM's ID.
func TestCreateAd_PMUser_SellAd_TaggedWithPMID(t *testing.T) {
	db := setupAdTaggingDB(t)
	svc, esc, _ := newServiceWithPM(t, db)

	makerWallet := "0xAbCd1234abCd1234abCd1234abCd1234AbCd1234"
	sellerID := uuid.New()
	pm := seedActivePMForUser(t, db, sellerID, makerWallet)
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	// Enough escrow balance to pass the pool check.
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000), Locked: big.NewInt(0)}

	res, err := svc.CreateAd(context.Background(), sellerID, makerWallet, makeCreateAdParams(pmID))
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	if res.Ad.PMID == nil {
		t.Fatal("ad.PMID is nil, want the PM's ID")
	}
	if *res.Ad.PMID != pm.ID {
		t.Fatalf("ad.PMID = %v, want %v", *res.Ad.PMID, pm.ID)
	}

	// Confirm the persisted row also has PMID set.
	var persisted models.P2PAd
	if err := db.First(&persisted, "id = ?", res.Ad.ID).Error; err != nil {
		t.Fatalf("load persisted ad: %v", err)
	}
	if persisted.PMID == nil || *persisted.PMID != pm.ID {
		t.Fatalf("persisted ad.PMID = %v, want %v", persisted.PMID, pm.ID)
	}
}

// (b) Active-PM user creates a sell-ad with sellerAddress != PM maker wallet
// → rejected with ErrInvalidParams, no ad row created.
func TestCreateAd_PMUser_SellAd_WrongWallet_Rejected(t *testing.T) {
	db := setupAdTaggingDB(t)
	svc, esc, _ := newServiceWithPM(t, db)

	makerWallet := "0xAbCd1234abCd1234abCd1234abCd1234AbCd1234"
	wrongWallet := "0x1111111111111111111111111111111111111111"
	sellerID := uuid.New()
	seedActivePMForUser(t, db, sellerID, makerWallet)
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000), Locked: big.NewInt(0)}

	_, err := svc.CreateAd(context.Background(), sellerID, wrongWallet, makeCreateAdParams(pmID))
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}

	// Confirm no ad was persisted.
	var count int64
	db.Model(&models.P2PAd{}).Where("seller_id = ?", sellerID).Count(&count)
	if count != 0 {
		t.Fatalf("ad was persisted despite invalid wallet: count=%d", count)
	}
}

// (c) Non-PM user creates a sell-ad → ad.PMID is nil, ad created normally.
func TestCreateAd_NonPMUser_SellAd_NilPMID(t *testing.T) {
	db := setupAdTaggingDB(t)
	svc, esc, _ := newServiceWithPM(t, db)

	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	_, sellerAddr := newSellerKey(t)

	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000), Locked: big.NewInt(0)}

	res, err := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	if res.Ad.PMID != nil {
		t.Fatalf("ad.PMID = %v, want nil for non-PM user", res.Ad.PMID)
	}
}

// (d) Active-PM user creates a buy-ad with merchantAddress == PM maker wallet
// → ad.PMID is set to the PM's ID.
func TestCreateAd_PMUser_BuyAd_TaggedWithPMID(t *testing.T) {
	db := setupAdTaggingDB(t)
	svc, esc, _ := newServiceWithPM(t, db)
	_ = esc

	makerWallet := "0xAbCd1234abCd1234abCd1234abCd1234AbCd1234"
	merchantID := uuid.New()
	pm := seedActivePMForUser(t, db, merchantID, makerWallet)
	pmID := seedPaymentMethod(t, db, merchantID, "NGN")

	params := makeCreateAdParams(pmID)
	params.Side = models.P2PAdSideBuy

	res, err := svc.CreateAd(context.Background(), merchantID, makerWallet, params)
	if err != nil {
		t.Fatalf("CreateAd (buy): %v", err)
	}
	if res.Ad.PMID == nil {
		t.Fatal("buy-ad.PMID is nil, want the PM's ID")
	}
	if *res.Ad.PMID != pm.ID {
		t.Fatalf("buy-ad.PMID = %v, want %v", *res.Ad.PMID, pm.ID)
	}

	// Confirm the persisted row also has PMID set.
	var persisted models.P2PAd
	if err := db.First(&persisted, "id = ?", res.Ad.ID).Error; err != nil {
		t.Fatalf("load persisted buy-ad: %v", err)
	}
	if persisted.PMID == nil || *persisted.PMID != pm.ID {
		t.Fatalf("persisted buy-ad.PMID = %v, want %v", persisted.PMID, pm.ID)
	}
}
