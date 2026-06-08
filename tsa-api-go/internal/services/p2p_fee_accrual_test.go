package services

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ─── P2P ↔ fee/TP v2 integration tests ──────────────────────────────────────
//
// COMPLETE per-feature split model (design §2, final PM clarification): the
// on-chain 0.5% P2P fee charged at release IS the BUYER GAS — 100% system, NO TP
// and NO cashback. P2P's TP/cashback is meant to come from a SEPARATE 1% PLATFORM
// fee (FeatureP2PPlatform) that is NOT collected today. So accrueP2PReleaseFee is
// now an explicit NO-OP and NO P2P release transition accrues anything — even
// with the v2 flag ON, a wired engine, and a positive on-chain fee.
//
// These tests lock that contract: ConfirmRelease, the reconciler auto-release
// path, and admin-resolve-to-buyer all settle ZERO into the engine (no guard row,
// no TP, no cashback), across both flag states. They are the regression guard for
// the day the 1% platform fee lands and re-wires P2P TP under FeatureP2PPlatform.

// setupP2PFeeDB builds an in-memory SQLite DB carrying BOTH the P2P trade tables
// AND the fee-distribution tables, on a single pinned connection (an in-memory
// SQLite DB is per-connection, and the engine reads uplines on the same handle).
func setupP2PFeeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	stmts := []string{
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			username TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL UNIQUE,
			password TEXT NOT NULL,
			role TEXT DEFAULT 'user',
			phone_number TEXT,
			country TEXT,
			state TEXT,
			city TEXT,
			address TEXT,
			profile_photo TEXT,
			referral_code TEXT,
			referred_by TEXT,
			persona_inquiry_id TEXT,
			verification_status TEXT DEFAULT 'pending',
			verification_notes TEXT,
			account_status TEXT DEFAULT 'active',
			last_login DATETIME,
			login_attempts INTEGER DEFAULT 0,
			lock_until DATETIME,
			wallet_address TEXT,
			seed_phrase_backed_up INTEGER DEFAULT 0,
			mute_notifications INTEGER DEFAULT 0,
			mute_email INTEGER DEFAULT 0,
			tp_balance REAL DEFAULT 0,
			cashback_balance REAL DEFAULT 0,
			last_settled_global_index INTEGER,
			email_verified INTEGER DEFAULT 0,
			deleted_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE tp_earnings (
			id TEXT PRIMARY KEY, user_id TEXT, source_user_id TEXT,
			source_type TEXT, source_id TEXT, generation INTEGER,
			fee_amount_usd REAL, percentage REAL, tp_earned REAL, created_at DATETIME
		)`,
		`CREATE TABLE cashback_earnings (
			id TEXT PRIMARY KEY, user_id TEXT, source_user_id TEXT,
			source_type TEXT, source_id TEXT, amount_usd REAL, tx_hash TEXT, created_at DATETIME
		)`,
		`CREATE TABLE fee_distributions (
			id TEXT PRIMARY KEY, source_type TEXT, source_id TEXT, user_id TEXT,
			feature TEXT, token TEXT, fee_usd REAL, system_usd REAL,
			buyer_cashback_usd REAL, upline_cashback_usd REAL,
			provider_cashback_usd REAL DEFAULT 0, tp_pool_usd REAL,
			global_pool_usd REAL DEFAULT 0, tp_per_usd REAL DEFAULT 1, created_at DATETIME,
			UNIQUE(source_type, source_id)
		)`,
		`CREATE TABLE global_tp_pool (
			singleton INTEGER PRIMARY KEY, accrued_usd REAL DEFAULT 0,
			accrued_tp REAL DEFAULT 0,
			per_user_index_scaled INTEGER NOT NULL DEFAULT 0,
			indexed_tp REAL NOT NULL DEFAULT 0,
			updated_at DATETIME
		)`,
		`CREATE TABLE global_tp_accruals (
			id TEXT PRIMARY KEY, source_type TEXT, source_id TEXT, reason TEXT,
			generation INTEGER DEFAULT -1, amount_usd REAL, amount_tp REAL, created_at DATETIME
		)`,
		`CREATE TABLE p2p_ads (
			id TEXT PRIMARY KEY, seller_id TEXT, side TEXT, token TEXT, chain_id INTEGER,
			amount TEXT, remaining TEXT, reserved TEXT, fiat_currency TEXT, rate TEXT,
			rate_type TEXT DEFAULT 'fixed', float_offset_bps INTEGER, float_anchor TEXT DEFAULT 'bybit_sell',
			min_limit TEXT, max_limit TEXT, payment_method_ids TEXT, terms TEXT,
			seller_address TEXT, voucher_nonce TEXT, voucher_deadline DATETIME,
			voucher_hash TEXT, voucher_signature TEXT, on_chain_ad_id TEXT, escrow_tx_hash TEXT,
			status TEXT DEFAULT 'active', expires_at DATETIME,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
		)`,
		`CREATE TABLE p2p_trades (
			id TEXT PRIMARY KEY, ad_id TEXT, buyer_id TEXT, seller_id TEXT, opener_id TEXT,
			token TEXT, chain_id INTEGER, funding INTEGER DEFAULT 0,
			token_amount TEXT, fiat_amount TEXT, fiat_currency TEXT, rate TEXT,
			payment_method_id TEXT, on_chain_trade_id TEXT, open_tx_hash TEXT, release_tx_hash TEXT,
			status TEXT DEFAULT 'pending_payment', proof_image_url TEXT, cancel_reason TEXT,
			resolved_by_admin TEXT, marked_paid_at DATETIME, released_at DATETIME,
			expires_at DATETIME NOT NULL, expired_notified_at DATETIME,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE p2p_audit_logs (
			id TEXT PRIMARY KEY, actor_id TEXT, trade_id TEXT, ad_id TEXT,
			action TEXT NOT NULL, metadata TEXT, created_at DATETIME
		)`,
		`CREATE TABLE p2p_disputes (
			id TEXT PRIMARY KEY, trade_id TEXT UNIQUE, raised_by TEXT, raised_role TEXT,
			reason TEXT, evidence_urls TEXT, status TEXT DEFAULT 'open',
			resolved_by TEXT, resolution TEXT, refunded_to_buyer INTEGER,
			resolve_tx_hash TEXT, created_at DATETIME, resolved_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v\nSQL=%s", err, s)
		}
	}
	return db
}

// newP2PFeeService builds a trade service wired with a REAL FeeDistributionService.
// v2Enabled toggles the SP3 gate. Returns the trade service, the mock escrow, and
// the fee service so tests can inspect both sides of the seam.
func newP2PFeeService(t *testing.T, db *gorm.DB, v2Enabled bool) (*P2PTradeService, *mockEscrow, *FeeDistributionService) {
	t.Helper()
	esc := newMockEscrow()
	svc := NewP2PTradeService(db, NewP2PEscrowRegistryFromMap(map[int64]P2PEscrowAdapter{146: esc}, "1"), &mockRates{}, &mockBus{}, 146)
	svc = svc.WithClock(fixedClock{t: time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)})
	svc.SetTokenResolver(func(chainID int64, symbol string) string {
		if symbol == "USDT" {
			return "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
		}
		return ""
	})
	feeSvc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	svc.SetFeeService(feeSvc, v2Enabled)
	return svc, esc, feeSvc
}

// mkP2PUser inserts an active user (optionally with a referrer).
func mkP2PUser(t *testing.T, db *gorm.DB, name string, referredBy *uuid.UUID) models.User {
	t.Helper()
	u := models.User{
		ID: uuid.New(), Name: name, Username: name, Email: name + "@t.co",
		Password: "x", ReferralCode: name, ReferredBy: referredBy,
		AccountStatus: models.AccountStatusActive,
		CreatedAt:     time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

// seedPaidTrade inserts a paid trade owned by sellerID/buyerID against adID, with
// the given token amount (base units). Status is 'paid' so ConfirmRelease applies.
func seedPaidTrade(t *testing.T, db *gorm.DB, adID, buyerID, sellerID uuid.UUID, amount string) *models.P2PTrade {
	t.Helper()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	tr := &models.P2PTrade{
		ID: uuid.New(), AdID: adID, BuyerID: buyerID, SellerID: sellerID,
		Token: "USDT", ChainID: 146, Funding: models.P2PTradeFundingPooled,
		TokenAmount: amount, FiatAmount: "1300", FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(), Status: models.P2PTradeStatusPaid, OpenTxHash: "0xopen",
		ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(tr).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	return tr
}

func seedAd(t *testing.T, db *gorm.DB, sellerID uuid.UUID, remaining, reserved string) uuid.UUID {
	t.Helper()
	ad := &models.P2PAd{
		ID: uuid.New(), SellerID: sellerID, Side: "sell", Token: "USDT", ChainID: 146,
		Amount: remaining, Remaining: remaining, Reserved: reserved,
		FiatCurrency: "NGN", Rate: "1300", RateType: "fixed", FloatAnchor: "bybit_sell",
		Status: models.P2PAdStatusActive, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.Create(ad).Error; err != nil {
		t.Fatalf("seed ad: %v", err)
	}
	return ad.ID
}

// guardRows counts FeeDistribution guard rows for a p2p_release source id.
func guardRows(t *testing.T, db *gorm.DB, tradeID uuid.UUID) int64 {
	t.Helper()
	var n int64
	db.Model(&models.FeeDistribution{}).
		Where("source_type = ? AND source_id = ?", "p2p_release", tradeID).Count(&n)
	return n
}

// waitForGuard polls (releasing the single SQLite connection between polls so the
// async accrual goroutine can run) until a guard row appears for tradeID or the
// deadline elapses. Returns the final count.
func waitForGuard(t *testing.T, db *gorm.DB, tradeID uuid.UUID, want int64) int64 {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var n int64
	for time.Now().Before(deadline) {
		n = guardRows(t, db, tradeID)
		if n >= want {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
	return n
}

// (a) ConfirmRelease (v2 flag ON, positive on-chain fee) accrues NOTHING under
//
//	the per-feature split model: the P2P fee is buyer gas (100% system, no TP),
//	and the carved P2P TP would come from a 1% platform fee not collected today.
func TestP2PFee_ConfirmRelease_NoAccrual(t *testing.T) {
	db := setupP2PFeeDB(t)
	svc, esc, _ := newP2PFeeService(t, db, true)

	upline := mkP2PUser(t, db, "p2pupline", nil)
	buyer := mkP2PUser(t, db, "p2pbuyer", &upline.ID) // buyer = gen0, upline = gen1
	seller := mkP2PUser(t, db, "p2pseller", nil)
	adID := seedAd(t, db, seller.ID, "0", "1000000")
	trade := seedPaidTrade(t, db, adID, buyer.ID, seller.ID, "1000000") // 1.0 USDT (6 dp)

	// Fee = 0.5% of amount = 5000 base units → $0.005 (a real positive fee).
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	fee := new(big.Int).Div(new(big.Int).Mul(tokenAmt, big.NewInt(50)), big.NewInt(10000))
	toBuyer := new(big.Int).Sub(tokenAmt, fee)
	esc.releasedEvent = &P2PTradeReleasedEvent{TradeID: HashID(trade.ID), AmountToBuyer: toBuyer, FeeToSystem: fee}

	if _, err := svc.ConfirmRelease(context.Background(), trade.ID, seller.ID, "0xrel"); err != nil {
		t.Fatalf("ConfirmRelease: %v", err)
	}

	// No guard row, no TP, no cashback — P2P release no longer accrues.
	time.Sleep(200 * time.Millisecond)
	if got := guardRows(t, db, trade.ID); got != 0 {
		t.Fatalf("P2P release must accrue NOTHING; found %d guard rows", got)
	}
	var b, u models.User
	db.First(&b, "id = ?", buyer.ID)
	db.First(&u, "id = ?", upline.ID)
	if b.CashbackBalance != 0 || b.TPBalance != 0 {
		t.Errorf("buyer must get no TP/cashback from P2P release: tp=%v cb=%v", b.TPBalance, b.CashbackBalance)
	}
	if u.CashbackBalance != 0 || u.TPBalance != 0 {
		t.Errorf("upline must get no TP/cashback from P2P release: tp=%v cb=%v", u.TPBalance, u.CashbackBalance)
	}
}

// (b) The reconciler auto-release path also accrues NOTHING, and a direct
//
//	accrueP2PReleaseFee call (the seam) is a documented no-op.
func TestP2PFee_ReconcileRelease_NoAccrual(t *testing.T) {
	db := setupP2PFeeDB(t)
	svc, esc, _ := newP2PFeeService(t, db, true)

	buyer := mkP2PUser(t, db, "rcbuyer", nil)
	seller := mkP2PUser(t, db, "rcseller", nil)
	adID := seedAd(t, db, seller.ID, "0", "1000000")
	trade := seedPaidTrade(t, db, adID, buyer.ID, seller.ID, "1000000")

	// Chain reports Released with Amount + FeeBps.
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	esc.onChainTrade = &OnChainP2PTrade{
		Amount: tokenAmt, FeeBps: 50, Funding: models.P2PTradeFundingPooled,
		Status: OnChainP2PStatusReleased, Exists: true,
	}

	// Missed confirm → reconciler drives the release. It must NOT accrue.
	if _, err := svc.ReconcileTrade(context.Background(), trade.ID); err != nil {
		t.Fatalf("ReconcileTrade: %v", err)
	}
	// Also fire the seam directly with a positive fee — still a no-op.
	expFee := onChainTradeFee(esc.onChainTrade)
	var rel models.P2PTrade
	db.First(&rel, "id = ?", trade.ID)
	svc.accrueP2PReleaseFee(&rel, expFee)

	time.Sleep(200 * time.Millisecond)
	if got := guardRows(t, db, trade.ID); got != 0 {
		t.Fatalf("reconcile auto-release must accrue NOTHING, got %d guard rows", got)
	}
}

// (c) admin-resolve-to-buyer (a release transition charging an on-chain fee) also
//
//	accrues NOTHING under the new model.
func TestP2PFee_AdminResolve_NoAccrual(t *testing.T) {
	db := setupP2PFeeDB(t)
	svc, esc, _ := newP2PFeeService(t, db, true)
	buyer := mkP2PUser(t, db, "arb_buyer", nil)
	seller := mkP2PUser(t, db, "arb_seller", nil)
	adID := seedAd(t, db, seller.ID, "0", "1000000")
	trade := seedPaidTrade(t, db, adID, buyer.ID, seller.ID, "1000000")
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("status", models.P2PTradeStatusDisputed)

	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	fee := new(big.Int).Div(new(big.Int).Mul(tokenAmt, big.NewInt(50)), big.NewInt(10000))
	moved := new(big.Int).Sub(tokenAmt, fee)
	esc.resolvedEvent = &P2PTradeResolvedEvent{
		TradeID: HashID(trade.ID), ReleasedToBuyer: true, AmountMoved: moved, FeeToSystem: fee,
	}

	if _, err := svc.ConfirmAdminResolveDispute(context.Background(), trade.ID, uuid.New(), "0xres"); err != nil {
		t.Fatalf("ConfirmAdminResolveDispute(release): %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := guardRows(t, db, trade.ID); got != 0 {
		t.Fatalf("release-to-buyer admin-resolve must accrue NOTHING, got %d guard rows", got)
	}
}

// (d) With the flag OFF, ALL release paths are a no-op too (unchanged contract).
func TestP2PFee_FlagOff_NoAccrualOnAnyPath(t *testing.T) {
	db := setupP2PFeeDB(t)
	svc, esc, _ := newP2PFeeService(t, db, false) // flag OFF

	buyer := mkP2PUser(t, db, "off_buyer", nil)
	seller := mkP2PUser(t, db, "off_seller", nil)
	adID := seedAd(t, db, seller.ID, "0", "1000000")

	// Live ConfirmRelease.
	trade := seedPaidTrade(t, db, adID, buyer.ID, seller.ID, "1000000")
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	fee := new(big.Int).Div(new(big.Int).Mul(tokenAmt, big.NewInt(50)), big.NewInt(10000))
	toBuyer := new(big.Int).Sub(tokenAmt, fee)
	esc.releasedEvent = &P2PTradeReleasedEvent{TradeID: HashID(trade.ID), AmountToBuyer: toBuyer, FeeToSystem: fee}
	if _, err := svc.ConfirmRelease(context.Background(), trade.ID, seller.ID, "0xrel"); err != nil {
		t.Fatalf("ConfirmRelease: %v", err)
	}

	// Reconciler auto-release.
	trade2 := seedPaidTrade(t, db, adID, buyer.ID, seller.ID, "1000000")
	esc.onChainTrade = &OnChainP2PTrade{
		Amount: tokenAmt, FeeBps: 50, Funding: models.P2PTradeFundingPooled,
		Status: OnChainP2PStatusReleased, Exists: true,
	}
	if _, err := svc.ReconcileTrade(context.Background(), trade2.ID); err != nil {
		t.Fatalf("ReconcileTrade: %v", err)
	}

	// Admin-resolve to buyer.
	trade3 := seedPaidTrade(t, db, adID, buyer.ID, seller.ID, "1000000")
	db.Model(&models.P2PTrade{}).Where("id = ?", trade3.ID).Update("status", models.P2PTradeStatusDisputed)
	moved := new(big.Int).Sub(tokenAmt, fee)
	esc.resolvedEvent = &P2PTradeResolvedEvent{
		TradeID: HashID(trade3.ID), ReleasedToBuyer: true, AmountMoved: moved, FeeToSystem: fee,
	}
	esc.onChainTrade = nil // resolve path uses the resolved event, not ReadTrade
	if _, err := svc.ConfirmAdminResolveDispute(context.Background(), trade3.ID, uuid.New(), "0xres"); err != nil {
		t.Fatalf("ConfirmAdminResolveDispute: %v", err)
	}

	// Flag off → no guard rows on ANY path, no TP/cashback credited anywhere.
	time.Sleep(200 * time.Millisecond)
	var totalGuards int64
	db.Model(&models.FeeDistribution{}).Count(&totalGuards)
	if totalGuards != 0 {
		t.Fatalf("flag OFF must accrue nothing; found %d guard rows", totalGuards)
	}
	var b models.User
	db.First(&b, "id = ?", buyer.ID)
	if b.TPBalance != 0 || b.CashbackBalance != 0 {
		t.Fatalf("flag OFF must not credit TP/cashback; got tp=%v cb=%v", b.TPBalance, b.CashbackBalance)
	}
}
