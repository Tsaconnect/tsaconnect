package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// The table comes from the existing createPaymentMerchantsTable
// (pm_automation_buy_test.go), extended with kind and pool_chain_id.

// seedPMRow inserts a PaymentMerchant of the given kind and status for a fresh
// user. An LP gets pool chain 97 (BSC testnet, the rehearsed pool).
func seedPMRow(t *testing.T, db *gorm.DB, kind, status, wallet string) *models.PaymentMerchant {
	t.Helper()
	pm := &models.PaymentMerchant{
		ID: uuid.New(), UserID: uuid.New(), Name: "Ada Obi", Kind: kind, Status: status,
		OperatorWalletAddress: wallet, SettlementBankCode: "058",
		SettlementAccountNumber: "0123456789", SettlementAccountName: "ADA OBI",
	}
	if kind == models.PaymentMerchantKindLP {
		chain := int64(97)
		pm.PoolChainID = &chain
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed pm: %v", err)
	}
	return pm
}

func TestPMService_ActiveLookupByKind(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)
	ctx := context.Background()
	lp := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000a1")

	if _, err := svc.GetActiveByUserIDKind(ctx, lp.UserID, models.PaymentMerchantKindMerchant); !errors.Is(err, ErrPMNotFound) {
		t.Fatalf("merchant lookup on an LP = %v, want ErrPMNotFound", err)
	}
	got, err := svc.GetActiveByUserIDKind(ctx, lp.UserID, models.PaymentMerchantKindLP)
	if err != nil || got.ID != lp.ID {
		t.Fatalf("lp lookup = %v %v", got, err)
	}
	// Any status, by kind: the LP row is the LP's, and there is no merchant row.
	if got, err := svc.GetByUserIDKind(ctx, lp.UserID, models.PaymentMerchantKindLP); err != nil || got.ID != lp.ID {
		t.Fatalf("lp by kind = %v %v", got, err)
	}
	if _, err := svc.GetByUserIDKind(ctx, lp.UserID, models.PaymentMerchantKindMerchant); !errors.Is(err, ErrPMNotFound) {
		t.Fatalf("merchant by kind on an LP = %v, want ErrPMNotFound", err)
	}
}

func TestPMService_ListByKindAndLPIDs(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)
	ctx := context.Background()
	m := seedPMRow(t, db, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000b1")
	lp := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusPaused, "0x00000000000000000000000000000000000000b2")

	all, _ := svc.ListByKind(ctx, "")
	lps, _ := svc.ListByKind(ctx, models.PaymentMerchantKindLP)
	merchants, _ := svc.ListByKind(ctx, models.PaymentMerchantKindMerchant)
	if len(all) != 2 || len(lps) != 1 || lps[0].ID != lp.ID || len(merchants) != 1 || merchants[0].ID != m.ID {
		t.Fatalf("all=%d lps=%v merchants=%v", len(all), lps, merchants)
	}
}

// Public ad reads hide every ad owned by an LP row (legacy slice stock), of
// any status and whether or not the row was later activated on the vault —
// through a correlated subquery, never an id list that grows with every LP.
func TestListAds_HidesLPStockAdsWithoutAnIDList(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)
	svc.SetPMService(NewPMService(db))
	ctx := context.Background()
	merchant := seedPMRow(t, db, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000c1")
	legacy := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusPaused, "0x00000000000000000000000000000000000000c2")
	reactivated := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000c3")
	if err := db.Exec("ALTER TABLE payment_merchants ADD COLUMN vault_activated_at DATETIME").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.PaymentMerchant{}).Where("id = ?", reactivated.ID).Update("vault_activated_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ { // vault LPs that own nothing
		seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, fmt.Sprintf("0x%040x", 0xd0+i))
	}
	plain := seedActiveAd(t, db, uuid.New(), 146, nil, "1500", "100")
	pmAd := seedActiveAd(t, db, merchant.UserID, 146, &merchant.ID, "1500", "100")
	legacyAd := seedActiveAd(t, db, legacy.UserID, 97, &legacy.ID, "1500", "100")
	reactAd := seedActiveAd(t, db, reactivated.UserID, 97, &reactivated.ID, "1500", "100")

	rows, err := svc.ListAds(ctx, AdFilter{Status: models.P2PAdStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	got := map[uuid.UUID]bool{}
	for _, a := range rows {
		got[a.ID] = true
	}
	if !got[plain.ID] || !got[pmAd.ID] || got[legacyAd.ID] || got[reactAd.ID] || len(rows) != 2 {
		t.Fatalf("listed %v: want the plain and PM ads only (legacy %s / reactivated %s hidden)", got, legacyAd.ID, reactAd.ID)
	}
	own, _ := svc.ListAds(ctx, AdFilter{SellerID: legacy.UserID, ViewerID: legacy.UserID})
	if len(own) != 1 {
		t.Fatalf("own list = %d, want the LP's own ad", len(own))
	}

	q, err := svc.excludePoolAds(ctx, db.Session(&gorm.Session{DryRun: true}).Model(&models.P2PAd{}))
	if err != nil {
		t.Fatal(err)
	}
	stmt := q.Find(&[]models.P2PAd{}).Statement
	if len(stmt.Vars) != 1 || !strings.Contains(stmt.SQL.String(), "NOT EXISTS") {
		t.Fatalf("SQL %q vars %v: want one NOT EXISTS subquery, no LP id list", stmt.SQL.String(), stmt.Vars)
	}
}

// Review Focus 3: an LP who is also a marketplace merchant keeps an ordinary
// Sonic ad untagged — the LP row must never become its pm_id.
func TestCreateAd_LPUserOrdinarySonicAdNotTagged(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)
	svc.SetPMService(NewPMService(db))
	_, sellerAddr := newSellerKey(t)
	lp := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, sellerAddr)
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000), Locked: big.NewInt(0)}
	pmID := seedPaymentMethod(t, db, lp.UserID, "NGN")

	res, err := svc.CreateAd(context.Background(), lp.UserID, sellerAddr, makeCreateAdParams(pmID))
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	if res.Ad.PMID != nil {
		t.Fatalf("ordinary ad tagged with pm_id %s — an LP row is never a PM ad tag", *res.Ad.PMID)
	}
}
