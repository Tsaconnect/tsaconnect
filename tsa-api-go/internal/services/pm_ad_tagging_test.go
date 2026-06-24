package services

// pm_ad_tagging_test.go — Task 6: auto-tag active-PM users' ads as PM ads,
// and enforce that the ad's wallet address equals the PM's maker wallet.

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
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

// (a-2) Active-PM user creates a sell-ad with NO payment method → allowed. A PM
// settles through its Nomba VA (buyers pay the VA), so a manual payment method
// is not required for PM ads.
func TestCreateAd_PMUser_SellAd_NoPaymentMethod_OK(t *testing.T) {
	db := setupAdTaggingDB(t)
	svc, esc, _ := newServiceWithPM(t, db)

	makerWallet := "0xAbCd1234abCd1234abCd1234abCd1234AbCd1234"
	sellerID := uuid.New()
	pm := seedActivePMForUser(t, db, sellerID, makerWallet)

	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000), Locked: big.NewInt(0)}

	params := makeCreateAdParams(uuid.Nil)
	params.PaymentMethodIDs = nil // PM ad: no manual payment method attached

	res, err := svc.CreateAd(context.Background(), sellerID, makerWallet, params)
	if err != nil {
		t.Fatalf("CreateAd (PM, no payment method) rejected: %v", err)
	}
	if res.Ad.PMID == nil || *res.Ad.PMID != pm.ID {
		t.Fatalf("ad.PMID = %v, want %v", res.Ad.PMID, pm.ID)
	}
}

// (a-3) Non-PM user creates a sell-ad with NO payment method → still rejected.
// The waiver applies only to PMs; normal sellers must attach one.
func TestCreateAd_NonPMUser_SellAd_NoPaymentMethod_Rejected(t *testing.T) {
	db := setupAdTaggingDB(t)
	svc, esc, _ := newServiceWithPM(t, db)

	sellerID := uuid.New()
	sellerAddr := "0x2222222222222222222222222222222222222222"

	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000), Locked: big.NewInt(0)}

	params := makeCreateAdParams(uuid.Nil)
	params.PaymentMethodIDs = nil

	_, err := svc.CreateAd(context.Background(), sellerID, sellerAddr, params)
	if err == nil {
		t.Fatal("expected error for non-PM sell-ad with no payment method, got nil")
	}
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
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
	// Symmetric with the PM-tagged tests: confirm the row was actually persisted.
	var persisted models.P2PAd
	if err := db.First(&persisted, "id = ?", res.Ad.ID).Error; err != nil {
		t.Fatalf("load persisted ad: %v", err)
	}
	if persisted.PMID != nil {
		t.Fatalf("persisted ad.PMID = %v, want nil", persisted.PMID)
	}
}

// (e) A non-active PM (pending/paused) user creates a sell-ad → NOT tagged
// (PMID nil) and NOT wallet-gated, even though they use a wallet that differs
// from the PM's maker wallet. Guards the status='active' filter in
// GetActiveByUserID against a regression that drops it.
func TestCreateAd_NonActivePM_SellAd_NotTagged(t *testing.T) {
	db := setupAdTaggingDB(t)
	svc, esc, _ := newServiceWithPM(t, db)

	makerWallet := "0xAbCd1234abCd1234abCd1234abCd1234AbCd1234"
	sellerID := uuid.New()
	// Seed an active PM first, then flip it to paused so the row exists but is
	// not active.
	pm := seedActivePMForUser(t, db, sellerID, makerWallet)
	if err := db.Model(&models.PaymentMerchant{}).
		Where("id = ?", pm.ID).
		Update("status", models.PaymentMerchantStatusPaused).Error; err != nil {
		t.Fatalf("pause PM: %v", err)
	}

	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	_, otherWallet := newSellerKey(t) // deliberately NOT the maker wallet
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000), Locked: big.NewInt(0)}

	res, err := svc.CreateAd(context.Background(), sellerID, otherWallet, makeCreateAdParams(pmID))
	if err != nil {
		t.Fatalf("CreateAd (paused PM): %v", err)
	}
	if res.Ad.PMID != nil {
		t.Fatalf("ad.PMID = %v, want nil for a paused PM", res.Ad.PMID)
	}
}

// (d) Active-PM user creates a buy-ad with merchantAddress == PM maker wallet
// → ad.PMID is set to the PM's ID.
// PM buy-ad with NO payment method → allowed (the PM pays users out via Nomba),
// mirroring the sell-ad waiver. Non-PM buy-ads still require a method.
func TestCreateAd_PMUser_BuyAd_NoPaymentMethod_OK(t *testing.T) {
	db := setupAdTaggingDB(t)
	svc, _, _ := newServiceWithPM(t, db)

	makerWallet := "0xAbCd1234abCd1234abCd1234abCd1234AbCd1234"
	merchantID := uuid.New()
	pm := seedActivePMForUser(t, db, merchantID, makerWallet)

	params := makeCreateAdParams(uuid.Nil)
	params.Side = models.P2PAdSideBuy
	params.PaymentMethodIDs = nil // PM buy-ad: no manual payment method attached

	res, err := svc.CreateAd(context.Background(), merchantID, makerWallet, params)
	if err != nil {
		t.Fatalf("CreateAd (PM buy-ad, no payment method) rejected: %v", err)
	}
	if res.Ad.PMID == nil || *res.Ad.PMID != pm.ID {
		t.Fatalf("buy-ad.PMID = %v, want %v", res.Ad.PMID, pm.ID)
	}
}

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

// TestCreateAd_PMUser_EmptyMakerWallet_Rejected — an active PM with no registered
// maker wallet (OperatorWalletAddress == "") must never match any caller-supplied
// address, including the zero address. Without the zero-address guard, both sides
// normalise to 0x000...000 and the ad is incorrectly PM-tagged.
func TestCreateAd_PMUser_EmptyMakerWallet_Rejected(t *testing.T) {
	db := setupAdTaggingDB(t)
	svc, esc, _ := newServiceWithPM(t, db)

	// Seed an ACTIVE PM with no registered maker wallet.
	sellerID := uuid.New()
	seedActivePMForUser(t, db, sellerID, "" /* empty OperatorWalletAddress */)
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000), Locked: big.NewInt(0)}

	// Caller supplies the zero address — without the guard this passes the
	// EqualFold check because common.HexToAddress("") == common.Address{}.
	zeroAddr := "0x0000000000000000000000000000000000000000"

	_, err := svc.CreateAd(context.Background(), sellerID, zeroAddr, makeCreateAdParams(pmID))
	if err == nil {
		t.Fatal("expected an error wrapping ErrInvalidParams but got nil")
	}
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}

	// No ad row should have been persisted.
	var count int64
	db.Model(&models.P2PAd{}).Where("seller_id = ?", sellerID).Count(&count)
	if count != 0 {
		t.Fatalf("ad was persisted despite empty maker wallet: count=%d", count)
	}
}

// (f) The wallet match is normalization-insensitive: a PM whose stored
// OperatorWalletAddress is all-lowercase still tags an ad created with the
// checksummed form of the same address (and vice-versa). Guards the
// common.HexToAddress() normalization in applyPMAdTag against a regression to
// a raw-string compare.
func TestCreateAd_PMUser_WalletNormalization_Tagged(t *testing.T) {
	db := setupAdTaggingDB(t)
	svc, esc, _ := newServiceWithPM(t, db)

	// PM row stores the address in all-lowercase (a common DB-normalization).
	storedLower := "0xabcd1234abcd1234abcd1234abcd1234abcd1234"
	// Caller supplies the EIP-55 checksummed form of the SAME address.
	callerChecksummed := common.HexToAddress(storedLower).Hex()
	if callerChecksummed == storedLower {
		t.Fatal("test setup: checksummed form should differ in case from lowercase")
	}

	sellerID := uuid.New()
	pm := seedActivePMForUser(t, db, sellerID, storedLower)
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000), Locked: big.NewInt(0)}

	res, err := svc.CreateAd(context.Background(), sellerID, callerChecksummed, makeCreateAdParams(pmID))
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	if res.Ad.PMID == nil || *res.Ad.PMID != pm.ID {
		t.Fatalf("ad.PMID = %v, want %v (normalization should match)", res.Ad.PMID, pm.ID)
	}
}
