package models

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestP2PPMFields_Persist(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	// Use raw DDL to avoid PostgreSQL-specific numeric(78,0) types that SQLite
	// cannot parse. We only create the columns needed for the PM-field assertions.
	stmts := []string{
		`CREATE TABLE p2p_ads (
			id TEXT PRIMARY KEY,
			seller_id TEXT NOT NULL,
			side TEXT NOT NULL DEFAULT 'sell',
			token TEXT NOT NULL,
			chain_id INTEGER NOT NULL DEFAULT 146,
			amount TEXT NOT NULL DEFAULT '0',
			remaining TEXT NOT NULL DEFAULT '0',
			reserved TEXT NOT NULL DEFAULT '0',
			fiat_currency TEXT NOT NULL DEFAULT '',
			rate TEXT NOT NULL DEFAULT '0',
			rate_type TEXT NOT NULL DEFAULT 'fixed',
			float_offset_bps INTEGER,
			float_anchor TEXT NOT NULL DEFAULT 'bybit_sell',
			min_limit TEXT,
			max_limit TEXT,
			payment_method_ids JSON NOT NULL DEFAULT '[]',
			terms TEXT,
			seller_address TEXT,
			voucher_nonce TEXT,
			voucher_deadline DATETIME,
			voucher_hash TEXT,
			voucher_signature TEXT,
			on_chain_ad_id TEXT,
			escrow_tx_hash TEXT,
			pm_id TEXT,
			status TEXT NOT NULL DEFAULT 'pending',
			expires_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`,
		`CREATE TABLE p2p_trades (
			id TEXT PRIMARY KEY,
			ad_id TEXT NOT NULL,
			buyer_id TEXT NOT NULL,
			seller_id TEXT NOT NULL,
			opener_id TEXT,
			token TEXT NOT NULL,
			chain_id INTEGER NOT NULL DEFAULT 146,
			funding INTEGER NOT NULL DEFAULT 0,
			token_amount TEXT NOT NULL DEFAULT '0',
			fiat_amount TEXT NOT NULL DEFAULT '0',
			fiat_currency TEXT NOT NULL DEFAULT '',
			rate TEXT NOT NULL DEFAULT '0',
			payment_method_id TEXT NOT NULL,
			on_chain_trade_id TEXT UNIQUE,
			open_tx_hash TEXT,
			release_tx_hash TEXT,
			status TEXT NOT NULL DEFAULT 'pending_payment',
			proof_image_url TEXT,
			cancel_reason TEXT,
			ngn_reservation_released INTEGER NOT NULL DEFAULT 0,
			resolved_by_admin TEXT,
			marked_paid_at DATETIME,
			released_at DATETIME,
			expires_at DATETIME NOT NULL,
			expired_notified_at DATETIME,
			payment_ref TEXT,
			nomba_inbound_event_id TEXT,
			nomba_payout_id TEXT,
			payout_account_number TEXT,
			payout_bank TEXT,
			payout_account_name TEXT,
			created_at DATETIME,
			updated_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v\nSQL=%s", err, s)
		}
	}

	pmID := uuid.New()
	ad := P2PAd{
		ID: uuid.New(), SellerID: uuid.New(), Side: P2PAdSideSell, Token: "USDT",
		FiatCurrency: "NGN", Amount: "100", Remaining: "100", Rate: "1500",
		PMID: &pmID,
	}
	if err := db.Create(&ad).Error; err != nil {
		t.Fatalf("create ad: %v", err)
	}
	var gotAd P2PAd
	db.First(&gotAd, "id = ?", ad.ID)
	if gotAd.PMID == nil || *gotAd.PMID != pmID {
		t.Fatalf("ad.PMID = %v, want %v", gotAd.PMID, pmID)
	}

	tr := P2PTrade{
		ID: uuid.New(), AdID: ad.ID, BuyerID: uuid.New(), SellerID: uuid.New(),
		Token: "USDT", ChainID: 146, TokenAmount: "1", FiatAmount: "1500", FiatCurrency: "NGN",
		Rate: "1500", PaymentMethodID: uuid.New(), ExpiresAt: time.Now().Add(30 * time.Minute),
		PaymentRef: "TSA-7F3A", NombaPayoutID: "po_1",
		PayoutAccountNumber: "0123456789", PayoutBank: "044", PayoutAccountName: "JOHN DOE",
	}
	if err := db.Create(&tr).Error; err != nil {
		t.Fatalf("create trade: %v", err)
	}
	var gotTr P2PTrade
	db.First(&gotTr, "id = ?", tr.ID)
	if gotTr.PaymentRef != "TSA-7F3A" || gotTr.PayoutAccountName != "JOHN DOE" {
		t.Fatalf("trade fields not persisted: %+v", gotTr)
	}
}
