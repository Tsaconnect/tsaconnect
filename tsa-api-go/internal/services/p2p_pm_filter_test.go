package services

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestListAds_PMFilter(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Raw DDL for p2p_ads (SQLite can't parse the numeric(78,0) types AutoMigrate
	// would emit). Includes all columns the model writes on Create.
	if err := db.Exec(`CREATE TABLE p2p_ads (
		id TEXT PRIMARY KEY, seller_id TEXT, side TEXT, token TEXT, chain_id INTEGER,
		amount TEXT, remaining TEXT, reserved TEXT DEFAULT '0',
		fiat_currency TEXT, rate TEXT, rate_type TEXT DEFAULT 'fixed',
		float_offset_bps INTEGER, float_anchor TEXT DEFAULT 'bybit_sell',
		min_limit TEXT, max_limit TEXT, payment_method_ids TEXT, terms TEXT,
		seller_address TEXT, voucher_nonce TEXT, voucher_deadline DATETIME,
		voucher_hash TEXT, voucher_signature TEXT, on_chain_ad_id TEXT, escrow_tx_hash TEXT,
		pm_id TEXT, status TEXT DEFAULT 'active', expires_at DATETIME,
		created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create p2p_ads: %v", err)
	}
	pmID := uuid.New()
	mk := func(pm *uuid.UUID) {
		ad := models.P2PAd{ID: uuid.New(), SellerID: uuid.New(), Side: models.P2PAdSideSell, Token: "USDT", Status: models.P2PAdStatusActive, PMID: pm}
		if err := db.Create(&ad).Error; err != nil {
			t.Fatalf("create ad: %v", err)
		}
	}
	mk(&pmID) // PM ad
	mk(nil)   // normal ad

	svc := &P2PTradeService{db: db}
	yes := true
	pmOnly, err := svc.ListAds(context.Background(), AdFilter{Status: models.P2PAdStatusActive, PM: &yes})
	if err != nil {
		t.Fatalf("list pm: %v", err)
	}
	if len(pmOnly) != 1 || pmOnly[0].PMID == nil {
		t.Fatalf("pm=true returned %d ads, want 1 PM ad", len(pmOnly))
	}
	no := false
	plainOnly, _ := svc.ListAds(context.Background(), AdFilter{Status: models.P2PAdStatusActive, PM: &no})
	if len(plainOnly) != 1 || plainOnly[0].PMID != nil {
		t.Fatalf("pm=false returned %d ads, want 1 non-PM ad", len(plainOnly))
	}
	all, _ := svc.ListAds(context.Background(), AdFilter{Status: models.P2PAdStatusActive})
	if len(all) != 2 {
		t.Fatalf("no filter returned %d ads, want 2", len(all))
	}
}
