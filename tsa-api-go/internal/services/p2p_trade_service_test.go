package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ─── Mock infra ─────────────────────────────────────────────────────────────

type mockEscrow struct {
	createAdCalled    bool
	openTradeCalled   bool
	releaseCalled     bool
	verifyAdResult    *P2PAdCreatedEvent
	verifyAdErr       error
	verifyOpenResult  *P2PTradeOpenedEvent
	verifyOpenErr     error
	verifyReleaseRes  *P2PTradeReleasedEvent
	verifyReleaseErr  error

	// Track what was prepared so Verify* can echo the matching amount by
	// default — keeps the happy-path tests honest without per-test config.
	lastAdAmount    *big.Int
	lastTradeAmount *big.Int
}

func (m *mockEscrow) ContractAddress() string { return "0xb5B073BF578fE24311ED50e540D9d632f1679261" }
func (m *mockEscrow) PrepareCreateAd(adId [32]byte, seller, token string, amount *big.Int) ([]byte, error) {
	m.createAdCalled = true
	m.lastAdAmount = new(big.Int).Set(amount)
	return []byte(`{"to":"0xescrow","data":"0xdeadbeef"}`), nil
}
func (m *mockEscrow) PrepareOpenTrade(tradeId, adId [32]byte, buyer string, amount *big.Int) ([]byte, error) {
	m.openTradeCalled = true
	m.lastTradeAmount = new(big.Int).Set(amount)
	return []byte(`{"to":"0xescrow","data":"0xfeedface"}`), nil
}
func (m *mockEscrow) PrepareMarkPaid(tradeId [32]byte, buyer string) ([]byte, error) {
	return []byte(`{"to":"0xescrow"}`), nil
}
func (m *mockEscrow) PrepareRelease(tradeId [32]byte, seller string) ([]byte, error) {
	m.releaseCalled = true
	return []byte(`{"to":"0xescrow","data":"0xrelease"}`), nil
}
func (m *mockEscrow) PrepareCloseAd(adId [32]byte, seller string) ([]byte, error) {
	return []byte(`{"to":"0xescrow"}`), nil
}
func (m *mockEscrow) VerifyAdCreated(txHash string, expectedAdId [32]byte) (*P2PAdCreatedEvent, error) {
	if m.verifyAdErr != nil {
		return nil, m.verifyAdErr
	}
	if m.verifyAdResult != nil {
		return m.verifyAdResult, nil
	}
	amount := m.lastAdAmount
	if amount == nil {
		amount = big.NewInt(0)
	}
	return &P2PAdCreatedEvent{
		AdID:   expectedAdId,
		Seller: common.HexToAddress("0xbeef"),
		Token:  common.HexToAddress("0xfeed"),
		Amount: amount,
	}, nil
}
func (m *mockEscrow) VerifyTradeOpened(txHash string, expectedTradeId [32]byte) (*P2PTradeOpenedEvent, error) {
	if m.verifyOpenErr != nil {
		return nil, m.verifyOpenErr
	}
	if m.verifyOpenResult != nil {
		return m.verifyOpenResult, nil
	}
	amount := m.lastTradeAmount
	if amount == nil {
		amount = big.NewInt(0)
	}
	return &P2PTradeOpenedEvent{
		TradeID: expectedTradeId,
		Buyer:   common.HexToAddress("0xd00d"),
		Amount:  amount,
	}, nil
}
func (m *mockEscrow) VerifyTradeReleased(txHash string, expectedTradeId [32]byte) (*P2PTradeReleasedEvent, error) {
	if m.verifyReleaseErr != nil {
		return nil, m.verifyReleaseErr
	}
	if m.verifyReleaseRes != nil {
		return m.verifyReleaseRes, nil
	}
	// Default: echo the last prepared trade amount split per the contract's
	// 0.5% fee (50 bps). Keeps happy-path tests honest after the C1 fix:
	// ConfirmRelease verifies AmountToBuyer + FeeToSystem == trade.TokenAmount.
	amt := m.lastTradeAmount
	if amt == nil {
		amt = big.NewInt(0)
	}
	fee := new(big.Int).Quo(new(big.Int).Mul(amt, big.NewInt(50)), big.NewInt(10000))
	toBuyer := new(big.Int).Sub(amt, fee)
	return &P2PTradeReleasedEvent{
		TradeID:       expectedTradeId,
		Buyer:         common.HexToAddress("0xd00d"),
		AmountToBuyer: toBuyer,
		FeeToSystem:   fee,
	}, nil
}

type mockRates struct {
	snap *RatesSnapshot
	err  error
}

func (m *mockRates) GetSnapshot() (*RatesSnapshot, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.snap != nil {
		return m.snap, nil
	}
	return &RatesSnapshot{
		Rates: map[string]P2PRate{
			"NGN": {Currency: "NGN", MidRate: 1500, Stale: false},
		},
		CachedAt: time.Now(),
	}, nil
}

type mockBus struct {
	events []events.Event
}

func (m *mockBus) Publish(ev events.Event) {
	m.events = append(m.events, ev)
}
func (m *mockBus) countByType(t string) int {
	n := 0
	for _, ev := range m.events {
		if ev.Type == t {
			n++
		}
	}
	return n
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// ─── Test DB setup ──────────────────────────────────────────────────────────

func setupTradeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	// SQLite-compatible DDL — Postgres uses jsonb / numeric, SQLite uses TEXT.
	// All amounts are TEXT here; CAST(... AS NUMERIC) is supported by SQLite.
	stmts := []string{
		`CREATE TABLE p2p_payment_methods (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			type TEXT, country TEXT, currency TEXT,
			bank_name TEXT, bank_code TEXT,
			account_number TEXT, account_name TEXT,
			verified INTEGER DEFAULT 0,
			is_default INTEGER DEFAULT 0,
			deleted_at DATETIME,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE p2p_ads (
			id TEXT PRIMARY KEY,
			seller_id TEXT NOT NULL,
			side TEXT, token TEXT, chain_id INTEGER,
			amount TEXT, remaining TEXT, reserved TEXT,
			fiat_currency TEXT, rate TEXT,
			min_limit TEXT, max_limit TEXT,
			payment_method_ids TEXT, terms TEXT,
			on_chain_ad_id TEXT, escrow_tx_hash TEXT,
			status TEXT DEFAULT 'pending',
			expires_at DATETIME,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
		)`,
		`CREATE TABLE p2p_trades (
			id TEXT PRIMARY KEY,
			ad_id TEXT NOT NULL,
			buyer_id TEXT NOT NULL,
			seller_id TEXT NOT NULL,
			token TEXT, chain_id INTEGER,
			token_amount TEXT, fiat_amount TEXT,
			fiat_currency TEXT, rate TEXT,
			payment_method_id TEXT,
			on_chain_trade_id TEXT UNIQUE,
			open_tx_hash TEXT, release_tx_hash TEXT,
			status TEXT DEFAULT 'pending_payment',
			proof_image_url TEXT, cancel_reason TEXT,
			resolved_by_admin TEXT,
			marked_paid_at DATETIME, released_at DATETIME,
			expires_at DATETIME NOT NULL,
			created_at DATETIME, updated_at DATETIME,
			CHECK (buyer_id <> seller_id)
		)`,
		`CREATE TABLE p2p_audit_logs (
			id TEXT PRIMARY KEY,
			actor_id TEXT,
			trade_id TEXT,
			ad_id TEXT,
			action TEXT NOT NULL,
			metadata TEXT,
			created_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v\nSQL=%s", err, s)
		}
	}
	return db
}

func seedPaymentMethod(t *testing.T, db *gorm.DB, userID uuid.UUID, currency string) uuid.UUID {
	t.Helper()
	pm := &models.P2PPaymentMethod{
		ID:            uuid.New(),
		UserID:        userID,
		Type:          models.P2PPaymentTypeBank,
		Country:       "NG",
		Currency:      currency,
		BankName:      "Test Bank",
		AccountNumber: "1234567890",
		AccountName:   "Test User",
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed pm: %v", err)
	}
	return pm.ID
}

func newService(t *testing.T, db *gorm.DB) (*P2PTradeService, *mockEscrow, *mockBus) {
	t.Helper()
	esc := &mockEscrow{}
	bus := &mockBus{}
	svc := NewP2PTradeService(db, esc, &mockRates{}, bus, 146)
	svc = svc.WithClock(fixedClock{t: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)})
	svc.SetTokenResolver(func(symbol string) string {
		switch symbol {
		case "USDT":
			return "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
		case "MCGP":
			return "0x517600323e5E2938207fA2e2e915B9D80e5B2b21"
		}
		return ""
	})
	return svc, esc, bus
}

// ─── Happy-path E2E ────────────────────────────────────────────────────────

func TestE2E_CreateAd_OpenTrade_MarkPaid_Release(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, bus := newService(t, db)
	ctx := context.Background()
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	// 1. createAd
	result, err := svc.CreateAd(ctx, sellerID, "0xseller", CreateAdParams{
		Token:            "USDT",
		TokenAmount:      "1000000000", // 1000 USDT
		FiatCurrency:     "NGN",
		Rate:             "1500", // 1 USDT base unit = 1500 NGN base unit
		MinLimit:         "10000000000",
		MaxLimit:         "500000000000",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	if result.Ad.Status != models.P2PAdStatusPending {
		t.Fatalf("ad status: want pending, got %s", result.Ad.Status)
	}
	if !esc.createAdCalled {
		t.Fatalf("escrow.PrepareCreateAd not called")
	}
	if bus.countByType(events.P2PAdCreated) != 1 {
		t.Fatalf("expected 1 P2PAdCreated event")
	}

	// 2. confirmAd
	ad, err := svc.ConfirmAd(ctx, result.Ad.ID, sellerID, "", "0xadtxhash")
	if err != nil {
		t.Fatalf("ConfirmAd: %v", err)
	}
	if ad.Status != models.P2PAdStatusActive {
		t.Fatalf("ad status after confirm: want active, got %s", ad.Status)
	}
	if ad.EscrowTxHash != "0xadtxhash" {
		t.Fatalf("escrow tx not recorded")
	}

	// 3. openTrade
	esc.verifyOpenResult = &P2PTradeOpenedEvent{
		Buyer:  common.HexToAddress("0xd00d"),
		Amount: big.NewInt(300_000_000_000_000), // matches the computed token amount
	}
	trade, _, err := svc.OpenTrade(ctx, ad.ID, buyerID, "0xbuyer", OpenTradeParams{
		FiatAmount:      "450000000000000000", // 450,000,000 NGN scaled? Let's use a simpler value.
		PaymentMethodID: pmID,
	})
	if err == nil {
		t.Fatalf("expected error: fiat amount out of range")
	}
	// Re-issue within max
	trade, openTx, err := svc.OpenTrade(ctx, ad.ID, buyerID, "0xbuyer", OpenTradeParams{
		FiatAmount:      "150000000000", // 150B (within max 500B)
		PaymentMethodID: pmID,
	})
	if err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	if trade.Status != models.P2PTradeStatusPendingPayment {
		t.Fatalf("trade status: want pending_payment, got %s", trade.Status)
	}
	if len(openTx) == 0 {
		t.Fatalf("expected unsigned open tx")
	}

	// Inject correct amount into verify mock
	expectedTokenAmount, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	esc.verifyOpenResult = &P2PTradeOpenedEvent{
		Buyer:  common.HexToAddress("0xd00d"),
		Amount: expectedTokenAmount,
	}

	// 4. confirmOpenTrade
	trade2, err := svc.ConfirmOpenTrade(ctx, trade.ID, buyerID, "", "0xopentxhash")
	if err != nil {
		t.Fatalf("ConfirmOpenTrade: %v", err)
	}
	if trade2.OpenTxHash != "0xopentxhash" {
		t.Fatalf("open tx hash not recorded")
	}

	// 5. markPaid
	trade3, err := svc.MarkPaid(ctx, trade.ID, buyerID, "https://cloudinary.example/proof.png")
	if err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if trade3.Status != models.P2PTradeStatusPaid {
		t.Fatalf("trade status after markPaid: want paid, got %s", trade3.Status)
	}
	if trade3.ProofImageURL != "https://cloudinary.example/proof.png" {
		t.Fatalf("proof URL not stored")
	}

	// 6. release
	relResult, err := svc.Release(ctx, trade.ID, sellerID, "0xseller")
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if len(relResult.UnsignedTx) == 0 {
		t.Fatalf("expected unsigned release tx")
	}
	// Trade now in releasing state
	var tradeReloaded models.P2PTrade
	db.First(&tradeReloaded, "id = ?", trade.ID)
	if tradeReloaded.Status != models.P2PTradeStatusReleasing {
		t.Fatalf("trade status after release: want releasing, got %s", tradeReloaded.Status)
	}

	// 7. confirmRelease
	esc.verifyReleaseRes = &P2PTradeReleasedEvent{
		Buyer:         common.HexToAddress("0xd00d"),
		AmountToBuyer: new(big.Int).Sub(expectedTokenAmount, new(big.Int).Quo(new(big.Int).Mul(expectedTokenAmount, big.NewInt(50)), big.NewInt(10000))),
		FeeToSystem:   new(big.Int).Quo(new(big.Int).Mul(expectedTokenAmount, big.NewInt(50)), big.NewInt(10000)),
	}
	trade4, err := svc.ConfirmRelease(ctx, trade.ID, sellerID, "0xreltxhash")
	if err != nil {
		t.Fatalf("ConfirmRelease: %v", err)
	}
	if trade4.Status != models.P2PTradeStatusReleased {
		t.Fatalf("trade status after confirm: want released, got %s", trade4.Status)
	}
	if trade4.ReleaseTxHash != "0xreltxhash" {
		t.Fatalf("release tx hash not recorded")
	}
	if trade4.ReleasedAt == nil {
		t.Fatalf("released_at not set")
	}

	// Ad accounting: reserved should be back to 0 after release
	var adFinal models.P2PAd
	db.First(&adFinal, "id = ?", ad.ID)
	if adFinal.Reserved != "0" {
		t.Fatalf("ad reserved after release: want 0, got %s", adFinal.Reserved)
	}

	// Event audit
	if bus.countByType(events.P2PTradeReleased) < 2 { // one for seller, one for buyer
		t.Fatalf("expected 2 P2PTradeReleased events, got %d", bus.countByType(events.P2PTradeReleased))
	}

	// Audit log rows persisted
	var auditCount int64
	db.Model(&models.P2PAuditLog{}).Count(&auditCount)
	if auditCount < 5 {
		t.Fatalf("expected ≥5 audit rows, got %d", auditCount)
	}
}

// ─── Negative paths ─────────────────────────────────────────────────────────

func TestCreateAd_RejectsStaleRate(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.rates = &mockRates{snap: &RatesSnapshot{
		Rates:        map[string]P2PRate{"NGN": {Currency: "NGN", Stale: true}},
		StaleSeconds: 600,
	}}
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	_, err := svc.CreateAd(context.Background(), sellerID, "0xseller", CreateAdParams{
		Token: "USDT", TokenAmount: "1000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if !errors.Is(err, ErrRateStale) {
		t.Fatalf("want ErrRateStale, got %v", err)
	}
}

func TestCreateAd_RejectsOverflowAmount(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	over := new(big.Int).Add(uint96Max, big.NewInt(1)).String()
	_, err := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: over, FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if !errors.Is(err, ErrAmountOverflow) {
		t.Fatalf("want ErrAmountOverflow, got %v", err)
	}
}

func TestCreateAd_RejectsForeignPaymentMethod(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	otherID := uuid.New()
	pmID := seedPaymentMethod(t, db, otherID, "NGN") // belongs to someone else

	_, err := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if !errors.Is(err, ErrPaymentMethodMissing) {
		t.Fatalf("want ErrPaymentMethodMissing, got %v", err)
	}
}

func TestOpenTrade_RejectsSelfTrade(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, err := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if err != nil {
		t.Fatalf("create ad: %v", err)
	}
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtxhash")

	_, _, err = svc.OpenTrade(context.Background(), result.Ad.ID, sellerID, "0xs", OpenTradeParams{
		FiatAmount: "100000", PaymentMethodID: pmID,
	})
	if !errors.Is(err, ErrSelfTrade) {
		t.Fatalf("want ErrSelfTrade, got %v", err)
	}
}

func TestOpenTrade_RejectsInsufficientRemaining(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "100", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")

	// remaining = 100 token base units. fiat 151500 / rate 1500 = 101 token
	// → exceeds remaining 100.
	_, _, err := svc.OpenTrade(context.Background(), result.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "151500", PaymentMethodID: pmID,
	})
	if !errors.Is(err, ErrInsufficientRemaining) {
		t.Fatalf("want ErrInsufficientRemaining, got %v", err)
	}
}

func TestMarkPaid_OnlyBuyer(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	otherID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")

	trade, _, err := svc.OpenTrade(context.Background(), result.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000000", PaymentMethodID: pmID,
	})
	if err != nil {
		t.Fatalf("openTrade: %v", err)
	}
	_, err = svc.MarkPaid(context.Background(), trade.ID, otherID, "url")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
}

func TestRelease_OnlySeller(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")
	trade, _, _ := svc.OpenTrade(context.Background(), result.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000000", PaymentMethodID: pmID,
	})
	_, _ = svc.MarkPaid(context.Background(), trade.ID, buyerID, "url")

	_, err := svc.Release(context.Background(), trade.ID, buyerID, "0xb") // buyer attempting release
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden for buyer-initiated release, got %v", err)
	}
}

func TestPauseResume_OnlySeller(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	otherID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")

	if err := svc.PauseAd(context.Background(), result.Ad.ID, otherID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
	if err := svc.PauseAd(context.Background(), result.Ad.ID, sellerID); err != nil {
		t.Fatalf("pause by seller: %v", err)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", result.Ad.ID)
	if ad.Status != models.P2PAdStatusPaused {
		t.Fatalf("ad not paused: %s", ad.Status)
	}
	if err := svc.ResumeAd(context.Background(), result.Ad.ID, sellerID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	db.First(&ad, "id = ?", result.Ad.ID)
	if ad.Status != models.P2PAdStatusActive {
		t.Fatalf("ad not active after resume: %s", ad.Status)
	}
}

// ─── Pure helpers ───────────────────────────────────────────────────────────

// ─── Gate-3 review additions ────────────────────────────────────────────────

func TestConfirmAd_RejectsOnChainAmountMismatch(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	// Force the mock to return a mismatched amount.
	esc.verifyAdResult = &P2PAdCreatedEvent{
		AdID:   HashID(result.Ad.ID),
		Seller: common.HexToAddress("0xbeef"),
		Token:  common.HexToAddress("0xfeed"),
		Amount: big.NewInt(1), // wildly wrong
	}
	_, err := svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")
	if err == nil || !errorContains(err, "does not match") {
		t.Fatalf("want amount-mismatch error, got %v", err)
	}
}

func TestConfirmAd_RejectsForeignSeller(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	// On-chain seller is someone else — adId collision attempt.
	esc.verifyAdResult = &P2PAdCreatedEvent{
		AdID:   HashID(result.Ad.ID),
		Seller: common.HexToAddress("0x9999"),
		Token:  common.HexToAddress("0xfeed"),
		Amount: big.NewInt(1000000000),
	}
	_, err := svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID,
		"0x1234567890abcdef1234567890abcdef12345678", "0xtx")
	if err == nil || !errorContains(err, "on-chain seller") {
		t.Fatalf("want on-chain-seller mismatch, got %v", err)
	}
}

func TestConfirmAd_IdempotentReplay(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	first, err := svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xreplay")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// Second call with same txHash must succeed and return same status.
	second, err := svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xreplay")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.ID != second.ID || second.Status != models.P2PAdStatusActive {
		t.Fatalf("replay returned different ad or wrong status: %+v", second)
	}
	// No new audit row beyond the initial set.
	var auditCount int64
	db.Model(&models.P2PAuditLog{}).Where("ad_id = ?", result.Ad.ID).Count(&auditCount)
	if auditCount > 2 { // ad.created + ad.created.confirmed
		t.Fatalf("idempotent replay produced extra audit rows: %d", auditCount)
	}
}

func TestConfirmOpenTrade_RejectsOnChainAmountMismatch(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")
	trade, _, _ := svc.OpenTrade(context.Background(), result.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000000", PaymentMethodID: pmID,
	})

	esc.verifyOpenResult = &P2PTradeOpenedEvent{
		TradeID: HashID(trade.ID),
		Buyer:   common.HexToAddress("0xd00d"),
		Amount:  big.NewInt(1), // mismatch
	}
	_, err := svc.ConfirmOpenTrade(context.Background(), trade.ID, buyerID, "", "0xopen")
	if err == nil || !errorContains(err, "does not match") {
		t.Fatalf("want amount-mismatch error, got %v", err)
	}
}

func TestConfirmRelease_RejectsOnChainAmountMismatch(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")
	trade, _, _ := svc.OpenTrade(context.Background(), result.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000000", PaymentMethodID: pmID,
	})
	_, _ = svc.ConfirmOpenTrade(context.Background(), trade.ID, buyerID, "", "0xopen")
	_, _ = svc.MarkPaid(context.Background(), trade.ID, buyerID, "url")

	// Force release event to report a smaller total than trade.TokenAmount.
	esc.verifyReleaseRes = &P2PTradeReleasedEvent{
		TradeID:       HashID(trade.ID),
		Buyer:         common.HexToAddress("0xd00d"),
		AmountToBuyer: big.NewInt(1),
		FeeToSystem:   big.NewInt(0),
	}
	_, err := svc.ConfirmRelease(context.Background(), trade.ID, sellerID, "0xrel")
	if err == nil || !errorContains(err, "does not match trade amount") {
		t.Fatalf("want C1 amount-mismatch error, got %v", err)
	}
}

func TestRelease_RejectsWhenTradeNotPaid(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")
	trade, _, _ := svc.OpenTrade(context.Background(), result.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000000", PaymentMethodID: pmID,
	})
	// Skip MarkPaid — trade is still pending_payment.
	_, err := svc.Release(context.Background(), trade.ID, sellerID, "0xs")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

func TestOpenTrade_RejectsInactiveAd(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	// Pending — confirm not yet called.
	_, _, err := svc.OpenTrade(context.Background(), result.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000000", PaymentMethodID: pmID,
	})
	if !errors.Is(err, ErrAdNotActive) {
		t.Fatalf("pending ad: want ErrAdNotActive, got %v", err)
	}

	// Confirm to active, then pause.
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")
	_ = svc.PauseAd(context.Background(), result.Ad.ID, sellerID)
	_, _, err = svc.OpenTrade(context.Background(), result.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000000", PaymentMethodID: pmID,
	})
	if !errors.Is(err, ErrAdNotActive) {
		t.Fatalf("paused ad: want ErrAdNotActive, got %v", err)
	}
}

func TestOpenTrade_PartialFillsAccumulateCorrectly(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	buyer1ID := uuid.New()
	buyer2ID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	// Ad of 1000 token base units, rate 1500 → max fiat per buyer = 1500*1000.
	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")

	// First buyer takes 400 token (= 600000 fiat).
	_, _, err := svc.OpenTrade(context.Background(), result.Ad.ID, buyer1ID, "0xb1", OpenTradeParams{
		FiatAmount: "600000", PaymentMethodID: pmID,
	})
	if err != nil {
		t.Fatalf("first openTrade: %v", err)
	}
	// Second buyer takes 400 token (= 600000 fiat).
	_, _, err = svc.OpenTrade(context.Background(), result.Ad.ID, buyer2ID, "0xb2", OpenTradeParams{
		FiatAmount: "600000", PaymentMethodID: pmID,
	})
	if err != nil {
		t.Fatalf("second openTrade: %v", err)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", result.Ad.ID)
	if ad.Remaining != "200" || ad.Reserved != "800" {
		t.Fatalf("after two partials: remaining=%s reserved=%s; want 200/800",
			ad.Remaining, ad.Reserved)
	}
	// Third buyer attempt exceeding remaining must reject.
	_, _, err = svc.OpenTrade(context.Background(), result.Ad.ID, uuid.New(), "0xb3", OpenTradeParams{
		FiatAmount: "450000", PaymentMethodID: pmID, // 450000/1500 = 300 > 200
	})
	if !errors.Is(err, ErrInsufficientRemaining) {
		t.Fatalf("third openTrade: want ErrInsufficientRemaining, got %v", err)
	}
}

func TestRelease_AdStaysActiveOnPartialRelease(t *testing.T) {
	// H4 negative path: releasing one of two open trades must NOT flip the ad
	// to Exhausted. The status guard `remaining = 0 AND reserved = 0` is the
	// only thing keeping a future bug from prematurely exhausting an ad with
	// in-flight trades.
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	buyer1ID := uuid.New()
	buyer2ID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	// Ad with 2 token total; two buyers each take 1.
	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "2", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")
	t1, _, _ := svc.OpenTrade(context.Background(), result.Ad.ID, buyer1ID, "0xb1", OpenTradeParams{
		FiatAmount: "1500", PaymentMethodID: pmID,
	})
	_, _, _ = svc.OpenTrade(context.Background(), result.Ad.ID, buyer2ID, "0xb2", OpenTradeParams{
		FiatAmount: "1500", PaymentMethodID: pmID,
	})

	// Release only trade 1. remaining=0, reserved=1 → ad stays active.
	_, _ = svc.ConfirmOpenTrade(context.Background(), t1.ID, buyer1ID, "", "0xopen1")
	_, _ = svc.MarkPaid(context.Background(), t1.ID, buyer1ID, "url")
	_, _ = svc.Release(context.Background(), t1.ID, sellerID, "0xs")
	_, err := svc.ConfirmRelease(context.Background(), t1.ID, sellerID, "0xrel1")
	if err != nil {
		t.Fatalf("confirm release: %v", err)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", result.Ad.ID)
	if ad.Status != models.P2PAdStatusActive {
		t.Fatalf("partial release flipped ad to %s; want active (other trade still in-flight)", ad.Status)
	}
	if ad.Reserved == "0" {
		t.Fatalf("reserved should reflect the still-open trade, got 0")
	}
}

func TestRelease_ExhaustedAdAfterFullDrain(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	// Tiny ad: 1 token. Buyer drains it, releases it, ad must become exhausted.
	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")
	trade, _, err := svc.OpenTrade(context.Background(), result.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "1500", PaymentMethodID: pmID, // 1500/1500 = 1 token
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = svc.ConfirmOpenTrade(context.Background(), trade.ID, buyerID, "", "0xopen")
	_, _ = svc.MarkPaid(context.Background(), trade.ID, buyerID, "url")
	_, _ = svc.Release(context.Background(), trade.ID, sellerID, "0xs")
	_, err = svc.ConfirmRelease(context.Background(), trade.ID, sellerID, "0xrel")
	if err != nil {
		t.Fatalf("confirm release: %v", err)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", result.Ad.ID)
	if ad.Status != models.P2PAdStatusExhausted {
		t.Fatalf("want status=exhausted after full drain, got %s (remaining=%s reserved=%s)",
			ad.Status, ad.Remaining, ad.Reserved)
	}
}

func errorContains(err error, sub string) bool {
	return err != nil && strings.Contains(err.Error(), sub)
}

func TestComputeTokenFromFiat(t *testing.T) {
	tests := []struct {
		fiat, rate, wantToken string
	}{
		{"1500", "1500", "1"},        // 1500 NGN / 1500 = 1 USDT base
		{"150000000", "1500", "100000"},
		{"100", "1500", "0"},         // truncates
		{"1500", "1500.5", "0"},      // truncates because 1500 / 1500.5 < 1
	}
	for _, tc := range tests {
		token, _, err := computeTokenFromFiat(tc.fiat, tc.rate)
		if err != nil {
			t.Errorf("fiat=%s rate=%s: %v", tc.fiat, tc.rate, err)
			continue
		}
		if token.String() != tc.wantToken {
			t.Errorf("fiat=%s rate=%s: want %s got %s", tc.fiat, tc.rate, tc.wantToken, token)
		}
	}
}

func TestComputeTokenFromFiat_RejectsZeroAndNegative(t *testing.T) {
	cases := []struct{ fiat, rate string }{
		{"0", "1500"},
		{"-1", "1500"},
		{"1500", "0"},
		{"1500", "-1500"},
	}
	for _, c := range cases {
		if _, _, err := computeTokenFromFiat(c.fiat, c.rate); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("fiat=%s rate=%s: want ErrInvalidParams, got %v", c.fiat, c.rate, err)
		}
	}
}

func TestScaleDecimal(t *testing.T) {
	cases := map[string]struct {
		in    string
		scale int
		want  string
	}{
		"int":     {"1500", 2, "150000"},
		"frac":    {"1500.50", 2, "150050"},
		"trunc":   {"1.234", 2, "123"},
		"short":   {"1.2", 2, "120"},
	}
	for name, c := range cases {
		got, err := scaleDecimal(c.in, c.scale)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got.String() != c.want {
			t.Errorf("%s: want %s got %s", name, c.want, got)
		}
	}
}

func TestCheckFiatLimits(t *testing.T) {
	fiat := big.NewInt(1000)
	if err := checkFiatLimits(fiat, "500", "2000"); err != nil {
		t.Errorf("in-range: want nil, got %v", err)
	}
	if err := checkFiatLimits(fiat, "1500", "2000"); !errors.Is(err, ErrAmountOutOfRange) {
		t.Errorf("below min: want ErrAmountOutOfRange, got %v", err)
	}
	if err := checkFiatLimits(fiat, "100", "500"); !errors.Is(err, ErrAmountOutOfRange) {
		t.Errorf("above max: want ErrAmountOutOfRange, got %v", err)
	}
	if err := checkFiatLimits(fiat, "", ""); err != nil {
		t.Errorf("no limits: want nil, got %v", err)
	}
}
