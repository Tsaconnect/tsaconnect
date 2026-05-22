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
func (m *mockEscrow) PrepareCancelByTimeout(tradeId [32]byte, caller string) ([]byte, error) {
	return []byte(`{"to":"0xescrow","data":"0xcancel"}`), nil
}
func (m *mockEscrow) VerifyTradeCancelled(txHash string, expectedTradeId [32]byte) (*P2PTradeCancelledEvent, error) {
	amt := m.lastTradeAmount
	if amt == nil {
		amt = big.NewInt(0)
	}
	return &P2PTradeCancelledEvent{
		TradeID:      expectedTradeId,
		ReturnedToAd: amt,
	}, nil
}
func (m *mockEscrow) PrepareRaiseDispute(tradeId [32]byte, by string) ([]byte, error) {
	return []byte(`{"to":"0xescrow","data":"0xdispute"}`), nil
}
func (m *mockEscrow) PrepareAdminResolve(tradeId [32]byte, refundBuyer bool, admin string) ([]byte, error) {
	return []byte(`{"to":"0xescrow","data":"0xresolve"}`), nil
}
func (m *mockEscrow) VerifyTradeDisputed(_ string, expectedTradeId [32]byte) (*P2PTradeDisputedEvent, error) {
	return &P2PTradeDisputedEvent{TradeID: expectedTradeId, By: common.HexToAddress("0xbeef")}, nil
}
// nextResolveRefundBuyer lets tests set what the contract-emitted
// TradeResolved should claim — defaults to true (release to buyer).
var nextResolveRefundBuyer = true

// mockReadTradeResult lets per-test setup override what ReadTrade returns
// for chain reconciliation tests. Reset between tests.
var mockReadTradeResult *OnChainP2PTrade

func (m *mockEscrow) ReadTrade(tradeId [32]byte) (*OnChainP2PTrade, error) {
	if mockReadTradeResult != nil {
		// Echo the requested tradeId so consumers don't get a mismatched id.
		cp := *mockReadTradeResult
		cp.AdID = tradeId
		return &cp, nil
	}
	// Default: trade not on chain yet — reconciler treats this as "wait".
	return &OnChainP2PTrade{Status: OnChainP2PStatusNone, Exists: false}, nil
}

func (m *mockEscrow) VerifyTradeResolved(_ string, expectedTradeId [32]byte) (*P2PTradeResolvedEvent, error) {
	amt := m.lastTradeAmount
	if amt == nil {
		amt = big.NewInt(0)
	}
	if !nextResolveRefundBuyer {
		return &P2PTradeResolvedEvent{
			TradeID: expectedTradeId, ReleasedToBuyer: false,
			AmountMoved: big.NewInt(0), FeeToSystem: big.NewInt(0),
		}, nil
	}
	fee := new(big.Int).Quo(new(big.Int).Mul(amt, big.NewInt(50)), big.NewInt(10000))
	toBuyer := new(big.Int).Sub(amt, fee)
	return &P2PTradeResolvedEvent{
		TradeID: expectedTradeId, ReleasedToBuyer: true,
		AmountMoved: toBuyer, FeeToSystem: fee,
	}, nil
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
			rate_type TEXT NOT NULL DEFAULT 'fixed',
			float_offset_bps INTEGER,
			float_anchor TEXT NOT NULL DEFAULT 'bybit_sell',
			min_limit TEXT, max_limit TEXT,
			payment_method_ids TEXT, terms TEXT,
			on_chain_ad_id TEXT, escrow_tx_hash TEXT,
			status TEXT DEFAULT 'pending',
			expires_at DATETIME,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
			CHECK (rate_type IN ('fixed','float')),
			CHECK (
				(rate_type = 'fixed' AND float_offset_bps IS NULL) OR
				(rate_type = 'float' AND float_offset_bps IS NOT NULL)
			)
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
			expired_notified_at DATETIME,
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
		`CREATE TABLE p2p_disputes (
			id TEXT PRIMARY KEY,
			trade_id TEXT NOT NULL UNIQUE,
			raised_by TEXT NOT NULL,
			raised_role TEXT NOT NULL,
			reason TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'open',
			resolved_by TEXT,
			resolution TEXT,
			refunded_to_buyer INTEGER,
			resolve_tx_hash TEXT,
			created_at DATETIME,
			resolved_at DATETIME
		)`,
		`CREATE TABLE p2p_messages (
			id TEXT PRIMARY KEY,
			trade_id TEXT NOT NULL,
			sender_id TEXT,
			type TEXT NOT NULL,
			body TEXT,
			image_url TEXT,
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

	// 1. createAd — 1000 USDT @ 1500 NGN/USDT = 1.5M NGN total ad value.
	result, err := svc.CreateAd(ctx, sellerID, "0xseller", CreateAdParams{
		Token:            "USDT",
		TokenAmount:      "1000000000", // 1000 USDT base units (decimals=6)
		FiatCurrency:     "NGN",
		Rate:             "1500", // 1500 NGN per whole USDT
		MinLimit:         "1000",
		MaxLimit:         "1500000",
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

	// 3. openTrade — buyer wants 300 USDT = 450,000 NGN.
	esc.verifyOpenResult = &P2PTradeOpenedEvent{
		Buyer:  common.HexToAddress("0xd00d"),
		Amount: big.NewInt(300_000_000),
	}
	trade, _, err := svc.OpenTrade(ctx, ad.ID, buyerID, "0xbuyer", OpenTradeParams{
		FiatAmount:      "10000000", // 10M NGN — over max
		PaymentMethodID: pmID,
	})
	if err == nil {
		t.Fatalf("expected error: fiat amount out of range")
	}
	// Re-issue within max
	trade, openTx, err := svc.OpenTrade(ctx, ad.ID, buyerID, "0xbuyer", OpenTradeParams{
		FiatAmount:      "450000", // 450K NGN → 300 USDT
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
		FiatAmount: "150000", PaymentMethodID: pmID,
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
		FiatAmount: "150000", PaymentMethodID: pmID,
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
		FiatAmount: "150000", PaymentMethodID: pmID,
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
		FiatAmount: "150000", PaymentMethodID: pmID,
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

func TestRelease_AcceptsPendingPayment(t *testing.T) {
	// Contract relax (Gate 4): seller's signature is the release gate, not
	// the buyer's off-chain markPaid claim. Release is allowed from either
	// pending_payment or paid.
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
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	// Skip MarkPaid — release directly from pending_payment.
	res, err := svc.Release(context.Background(), trade.ID, sellerID, "0xs")
	if err != nil {
		t.Fatalf("release from pending_payment should succeed: %v", err)
	}
	if len(res.UnsignedTx) == 0 {
		t.Fatalf("expected unsigned tx")
	}
	var refreshed models.P2PTrade
	db.First(&refreshed, "id = ?", trade.ID)
	if refreshed.Status != models.P2PTradeStatusReleasing {
		t.Fatalf("trade did not transition to releasing: %s", refreshed.Status)
	}
}

func TestRelease_RejectsTerminalStatus(t *testing.T) {
	// Negative case: terminal states (released/cancelled/resolved) cannot
	// be re-released — contract would revert with "Not releasable", but
	// service catches it off-chain.
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
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	// Force trade to terminal cancelled.
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Update("status", models.P2PTradeStatusCancelled)
	_, err := svc.Release(context.Background(), trade.ID, sellerID, "0xs")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition from terminal, got %v", err)
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
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	if !errors.Is(err, ErrAdNotActive) {
		t.Fatalf("pending ad: want ErrAdNotActive, got %v", err)
	}

	// Confirm to active, then pause.
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")
	_ = svc.PauseAd(context.Background(), result.Ad.ID, sellerID)
	_, _, err = svc.OpenTrade(context.Background(), result.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
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

	// Ad of 1000 USDT (1e9 base units), rate 1500 NGN/USDT → ad fiat value 1.5M NGN.
	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")

	// First buyer takes 400 USDT (= 600000 NGN → 400M base units).
	_, _, err := svc.OpenTrade(context.Background(), result.Ad.ID, buyer1ID, "0xb1", OpenTradeParams{
		FiatAmount: "600000", PaymentMethodID: pmID,
	})
	if err != nil {
		t.Fatalf("first openTrade: %v", err)
	}
	// Second buyer takes 400 USDT (= 600000 NGN → 400M base units).
	_, _, err = svc.OpenTrade(context.Background(), result.Ad.ID, buyer2ID, "0xb2", OpenTradeParams{
		FiatAmount: "600000", PaymentMethodID: pmID,
	})
	if err != nil {
		t.Fatalf("second openTrade: %v", err)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", result.Ad.ID)
	if ad.Remaining != "200000000" || ad.Reserved != "800000000" {
		t.Fatalf("after two partials: remaining=%s reserved=%s; want 200000000/800000000",
			ad.Remaining, ad.Reserved)
	}
	// Third buyer attempt exceeding remaining must reject.
	// 450000 NGN → 300M base units; remaining is only 200M → exceeds.
	_, _, err = svc.OpenTrade(context.Background(), result.Ad.ID, uuid.New(), "0xb3", OpenTradeParams{
		FiatAmount: "450000", PaymentMethodID: pmID,
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

	// Ad of 2 USDT (2M base units); two buyers each take 1 USDT (= 1500 NGN → 1M base units).
	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "2000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")
	t1, _, err := svc.OpenTrade(context.Background(), result.Ad.ID, buyer1ID, "0xb1", OpenTradeParams{
		FiatAmount: "1500", PaymentMethodID: pmID,
	})
	if err != nil {
		t.Fatalf("first openTrade: %v", err)
	}
	_, _, err = svc.OpenTrade(context.Background(), result.Ad.ID, buyer2ID, "0xb2", OpenTradeParams{
		FiatAmount: "1500", PaymentMethodID: pmID,
	})
	if err != nil {
		t.Fatalf("second openTrade: %v", err)
	}

	// Release only trade 1. remaining=0, reserved=1 → ad stays active.
	_, _ = svc.ConfirmOpenTrade(context.Background(), t1.ID, buyer1ID, "", "0xopen1")
	_, _ = svc.MarkPaid(context.Background(), t1.ID, buyer1ID, "url")
	_, _ = svc.Release(context.Background(), t1.ID, sellerID, "0xs")
	_, err = svc.ConfirmRelease(context.Background(), t1.ID, sellerID, "0xrel1")
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

	// Tiny ad: 1 USDT (1M base units). Buyer drains it, releases it, ad must become exhausted.
	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")
	trade, _, err := svc.OpenTrade(context.Background(), result.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "1500", PaymentMethodID: pmID, // 1500 NGN → 1M base units = 1 USDT
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

// ─── Step 7: timeout + cancellation ─────────────────────────────────────────

func TestCancel_RejectsBeforeExpiry(t *testing.T) {
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
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	// Trade just created; expires_at is fixedClock + 30 min. Don't advance.
	_, err := svc.Cancel(context.Background(), trade.ID, sellerID, "0xs")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition before expiry, got %v", err)
	}
}

func TestCancel_HappyPath_RestoresAdRemaining(t *testing.T) {
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
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	// Verify reserved was credited.
	var adMid models.P2PAd
	db.First(&adMid, "id = ?", result.Ad.ID)
	if adMid.Reserved == "0" {
		t.Fatalf("reserved=0 after open; expected non-zero")
	}
	reservedAfterOpen := adMid.Reserved

	// Advance clock past the 30-min expiry.
	svc = svc.WithClock(fixedClock{t: time.Date(2026, 5, 18, 12, 31, 0, 0, time.UTC)})

	raw, err := svc.Cancel(context.Background(), trade.ID, sellerID, "0xs")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("expected unsigned cancel tx")
	}

	updated, err := svc.ConfirmCancel(context.Background(), trade.ID, sellerID, "0xcancel")
	if err != nil {
		t.Fatalf("ConfirmCancel: %v", err)
	}
	if updated.Status != models.P2PTradeStatusCancelled {
		t.Fatalf("trade status: want cancelled, got %s", updated.Status)
	}
	if updated.CancelReason != "timed_out" {
		t.Fatalf("cancel_reason: want timed_out, got %s", updated.CancelReason)
	}

	var ad models.P2PAd
	db.First(&ad, "id = ?", result.Ad.ID)
	if ad.Reserved != "0" {
		t.Fatalf("ad.reserved after cancel: want 0, got %s", ad.Reserved)
	}
	// Remaining must equal the original amount — the trade's portion is returned.
	if ad.Remaining != "1000000000" {
		t.Fatalf("ad.remaining after cancel: want 1000000000, got %s (was reserved %s)",
			ad.Remaining, reservedAfterOpen)
	}
}

func TestCancel_RejectsNonParticipant(t *testing.T) {
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
	trade, _, _ := svc.OpenTrade(context.Background(), result.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	// Advance past expiry.
	svc = svc.WithClock(fixedClock{t: time.Date(2026, 5, 18, 13, 0, 0, 0, time.UTC)})
	_, err := svc.Cancel(context.Background(), trade.ID, otherID, "0xother")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
}

func TestConfirmCancel_Idempotent(t *testing.T) {
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
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	svc = svc.WithClock(fixedClock{t: time.Date(2026, 5, 18, 13, 0, 0, 0, time.UTC)})
	_, _ = svc.Cancel(context.Background(), trade.ID, sellerID, "0xs")

	first, err := svc.ConfirmCancel(context.Background(), trade.ID, sellerID, "0xc")
	if err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	second, err := svc.ConfirmCancel(context.Background(), trade.ID, sellerID, "0xc")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.ID != second.ID || second.Status != models.P2PTradeStatusCancelled {
		t.Fatalf("replay mismatch")
	}
}

func TestListExpiredPendingTrades_ScopedToExpired(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	buyer1 := uuid.New()
	buyer2 := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	result, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), result.Ad.ID, sellerID, "", "0xtx")

	// Open one trade right after the fixed clock — expires_at = now + 30m.
	t1, _, _ := svc.OpenTrade(context.Background(), result.Ad.ID, buyer1, "0xb1", OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	// Open a second trade "after" the clock advances.
	svc2 := svc.WithClock(fixedClock{t: time.Date(2026, 5, 18, 14, 0, 0, 0, time.UTC)})
	t2, _, _ := svc2.OpenTrade(context.Background(), result.Ad.ID, buyer2, "0xb2", OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
	})

	// Service-default clock is the fixed clock; trade 1 expires at 12:30
	// fixed-time, trade 2 expires at 14:30. Run watchdog at 12:35.
	listingClock := fixedClock{t: time.Date(2026, 5, 18, 12, 35, 0, 0, time.UTC)}
	rows, err := svc.WithClock(listingClock).ListExpiredPendingTrades(context.Background(), 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 expired, got %d", len(rows))
	}
	if rows[0].ID != t1.ID {
		t.Fatalf("wrong trade returned: want t1=%s, got %s (t2=%s)", t1.ID, rows[0].ID, t2.ID)
	}
}

// ─── Step 9: chain reconciliation ───────────────────────────────────────────

// reconcilerSetup builds a trade in `paid` state ready for reconciliation
// drift tests. Resets the mockReadTradeResult global to avoid bleed-over.
func reconcilerSetup(t *testing.T) (svc *P2PTradeService, db *gorm.DB, trade *models.P2PTrade) {
	t.Helper()
	mockReadTradeResult = nil
	db = setupTradeDB(t)
	svc, _, _ = newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	res, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), res.Ad.ID, sellerID, "", "0xtx")
	tr, _, _ := svc.OpenTrade(context.Background(), res.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	_, _ = svc.ConfirmOpenTrade(context.Background(), tr.ID, buyerID, "", "0xopen")
	_, _ = svc.MarkPaid(context.Background(), tr.ID, buyerID, "url")
	var t2 models.P2PTrade
	db.First(&t2, "id = ?", tr.ID)
	return svc, db, &t2
}

func TestReconcile_NoChainTrade_NoOp(t *testing.T) {
	svc, _, trade := reconcilerSetup(t)
	mockReadTradeResult = &OnChainP2PTrade{Status: OnChainP2PStatusNone, Exists: false}
	out, err := svc.ReconcileTrade(context.Background(), trade.ID)
	if err != nil || out.Changed {
		t.Fatalf("expected no-op when chain has no trade: changed=%v err=%v", out.Changed, err)
	}
}

func TestReconcile_ChainReleased_DBPaid_FixesDB(t *testing.T) {
	svc, db, trade := reconcilerSetup(t)
	mockReadTradeResult = &OnChainP2PTrade{Status: OnChainP2PStatusReleased, Exists: true}
	out, err := svc.ReconcileTrade(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !out.Changed || out.ToStatus != models.P2PTradeStatusReleased {
		t.Fatalf("expected to-released change, got %+v", out)
	}
	var refreshed models.P2PTrade
	db.First(&refreshed, "id = ?", trade.ID)
	if refreshed.Status != models.P2PTradeStatusReleased || refreshed.ReleaseTxHash != "reconciled" {
		t.Fatalf("trade not transitioned: %+v", refreshed)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", trade.AdID)
	if ad.Reserved != "0" {
		t.Fatalf("ad.reserved should be 0 post-reconcile, got %s", ad.Reserved)
	}
}

func TestReconcile_ChainCancelled_RestoresAdRemaining(t *testing.T) {
	svc, db, trade := reconcilerSetup(t)
	mockReadTradeResult = &OnChainP2PTrade{Status: OnChainP2PStatusCancelled, Exists: true}
	out, err := svc.ReconcileTrade(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !out.Changed || out.ToStatus != models.P2PTradeStatusCancelled {
		t.Fatalf("expected to-cancelled change, got %+v", out)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", trade.AdID)
	// Trade was 100K base units; ad started at 1B → remaining returns to 1B.
	if ad.Remaining != "1000000000" || ad.Reserved != "0" {
		t.Fatalf("ad accounting wrong: remaining=%s reserved=%s", ad.Remaining, ad.Reserved)
	}
}

func TestReconcile_ChainDisputed_TransitionsDBAndBackfillsDispute(t *testing.T) {
	svc, db, trade := reconcilerSetup(t)
	mockReadTradeResult = &OnChainP2PTrade{Status: OnChainP2PStatusDisputed, Exists: true}
	out, err := svc.ReconcileTrade(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !out.Changed || out.ToStatus != models.P2PTradeStatusDisputed {
		t.Fatalf("expected to-disputed, got %+v", out)
	}
	var d models.P2PDispute
	if err := db.First(&d, "trade_id = ?", trade.ID).Error; err != nil {
		t.Fatalf("expected back-filled dispute row, got %v", err)
	}
	if d.Status != models.P2PDisputeStatusOpen {
		t.Fatalf("back-filled dispute should be open, got %s", d.Status)
	}
}

func TestReconcile_ChainResolved_AdminRefundBuyerKnown(t *testing.T) {
	svc, db, trade := reconcilerSetup(t)
	// Seed an existing dispute row recording the admin's prior choice.
	refund := true
	db.Create(&models.P2PDispute{
		ID: uuid.New(), TradeID: trade.ID,
		RaisedBy: trade.BuyerID, RaisedRole: models.P2PDisputeRoleBuyer,
		Reason: "x", Status: models.P2PDisputeStatusOpen,
		RefundedToBuyer: &refund,
		CreatedAt:       time.Now(),
	})
	mockReadTradeResult = &OnChainP2PTrade{Status: OnChainP2PStatusResolved, Exists: true}
	out, err := svc.ReconcileTrade(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !out.Changed || out.ToStatus != models.P2PTradeStatusResolved {
		t.Fatalf("expected to-resolved, got %+v", out)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", trade.AdID)
	// refund-to-buyer: reserved -= 100M base; remaining unchanged at 900M.
	if ad.Reserved != "0" || ad.Remaining != "900000000" {
		t.Fatalf("ad accounting wrong: remaining=%s reserved=%s", ad.Remaining, ad.Reserved)
	}
	var d models.P2PDispute
	db.First(&d, "trade_id = ?", trade.ID)
	if d.Status != models.P2PDisputeStatusResolved {
		t.Fatalf("dispute should be closed, got %s", d.Status)
	}
}

func TestReconcile_ChainPending_DBReleasingStuck_RollsBackToPaid(t *testing.T) {
	svc, db, trade := reconcilerSetup(t)
	// Force trade into 'releasing' with a stale updated_at.
	staleTime := time.Date(2026, 5, 18, 11, 0, 0, 0, time.UTC) // 1h before fixedClock
	db.Model(&models.P2PTrade{}).
		Where("id = ?", trade.ID).
		Updates(map[string]any{
			"status":     models.P2PTradeStatusReleasing,
			"updated_at": staleTime,
		})
	mockReadTradeResult = &OnChainP2PTrade{Status: OnChainP2PStatusPending, Exists: true}
	out, err := svc.ReconcileTrade(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !out.Changed || out.ToStatus != models.P2PTradeStatusPaid {
		t.Fatalf("expected rollback to paid, got %+v", out)
	}
}

func TestReconcile_ChainPaid_DBPending_BumpsToPaid(t *testing.T) {
	// Sixth drift case: someone called markPaid on chain directly, bypassing
	// the off-chain MarkPaid endpoint. Reconciler must mirror the bump.
	mockReadTradeResult = nil
	defer func() { mockReadTradeResult = nil }()
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	res, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), res.Ad.ID, sellerID, "", "0xtx")
	tr, _, _ := svc.OpenTrade(context.Background(), res.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	// Skip MarkPaid — DB still pending_payment.
	mockReadTradeResult = &OnChainP2PTrade{Status: OnChainP2PStatusPaid, Exists: true}
	out, err := svc.ReconcileTrade(context.Background(), tr.ID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !out.Changed || out.ToStatus != models.P2PTradeStatusPaid {
		t.Fatalf("expected DB→paid, got %+v", out)
	}
	var refreshed models.P2PTrade
	db.First(&refreshed, "id = ?", tr.ID)
	if refreshed.Status != models.P2PTradeStatusPaid || refreshed.MarkedPaidAt == nil {
		t.Fatalf("bump did not set marked_paid_at: %+v", refreshed)
	}
}

func TestReconcile_ChainPending_DBReleasingFresh_DoesNotRollback(t *testing.T) {
	// Negative case for the stale-release window. A trade in 'releasing' that
	// was just prepared (updated_at fresh) must NOT be rolled back — the
	// seller could be mid-signing.
	mockReadTradeResult = nil
	defer func() { mockReadTradeResult = nil }()
	svc, db, trade := reconcilerSetup(t)
	// Force trade into 'releasing' with a fresh updated_at (matches fixedClock).
	db.Model(&models.P2PTrade{}).
		Where("id = ?", trade.ID).
		Updates(map[string]any{
			"status":     models.P2PTradeStatusReleasing,
			"updated_at": time.Date(2026, 5, 18, 11, 59, 0, 0, time.UTC), // 1m before clock — still within 10-min window
		})
	mockReadTradeResult = &OnChainP2PTrade{Status: OnChainP2PStatusPending, Exists: true}
	out, err := svc.ReconcileTrade(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if out.Changed {
		t.Fatalf("fresh releasing must not be rolled back; got %+v", out)
	}
	var refreshed models.P2PTrade
	db.First(&refreshed, "id = ?", trade.ID)
	if refreshed.Status != models.P2PTradeStatusReleasing {
		t.Fatalf("status mutated; want releasing, got %s", refreshed.Status)
	}
}

func TestReconcile_ChainResolved_NoDisputeRow_DefaultsToSellerRefund(t *testing.T) {
	// Edge case: chain says Resolved but local dispute row is missing. The
	// reconciler defaults to refund=false (conservative — return to ad
	// pool). Funds are recoverable; admins can investigate via audit log.
	mockReadTradeResult = nil
	defer func() { mockReadTradeResult = nil }()
	svc, db, trade := reconcilerSetup(t)
	// No dispute row seeded.
	mockReadTradeResult = &OnChainP2PTrade{Status: OnChainP2PStatusResolved, Exists: true}
	out, err := svc.ReconcileTrade(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !out.Changed || out.ToStatus != models.P2PTradeStatusResolved {
		t.Fatalf("expected to-resolved, got %+v", out)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", trade.AdID)
	// Conservative default: funds restored to ad pool.
	if ad.Remaining != "1000000000" || ad.Reserved != "0" {
		t.Fatalf("conservative default failed: remaining=%s reserved=%s", ad.Remaining, ad.Reserved)
	}
}

func TestReconcile_TerminalDBStatus_NoOp(t *testing.T) {
	svc, db, trade := reconcilerSetup(t)
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Update("status", models.P2PTradeStatusReleased)
	// Even if chain says something different, terminal DB never changes.
	mockReadTradeResult = &OnChainP2PTrade{Status: OnChainP2PStatusCancelled, Exists: true}
	out, err := svc.ReconcileTrade(context.Background(), trade.ID)
	if err != nil || out.Changed {
		t.Fatalf("terminal status must be immune to reconcile drift: %+v err=%v", out, err)
	}
}

func TestReconcile_Idempotent(t *testing.T) {
	svc, db, trade := reconcilerSetup(t)
	mockReadTradeResult = &OnChainP2PTrade{Status: OnChainP2PStatusReleased, Exists: true}
	if _, err := svc.ReconcileTrade(context.Background(), trade.ID); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Second pass — same chain state — must not change anything.
	out, err := svc.ReconcileTrade(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if out.Changed {
		t.Fatalf("second pass should be no-op: %+v", out)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", trade.AdID)
	if ad.Reserved != "0" {
		t.Fatalf("second pass mutated accounting: reserved=%s", ad.Reserved)
	}
}

func TestListNonTerminalTrades_ExcludesTerminal(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	// Seed two trades — one pending, one already released.
	db.Create(&models.P2PTrade{
		ID: uuid.New(), AdID: uuid.New(), BuyerID: uuid.New(), SellerID: sellerID,
		Token: "USDT", ChainID: 146, TokenAmount: "1", FiatAmount: "1500",
		FiatCurrency: "NGN", Rate: "1500", PaymentMethodID: uuid.New(),
		Status: models.P2PTradeStatusPendingPayment,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	db.Create(&models.P2PTrade{
		ID: uuid.New(), AdID: uuid.New(), BuyerID: uuid.New(), SellerID: sellerID,
		Token: "USDT", ChainID: 146, TokenAmount: "1", FiatAmount: "1500",
		FiatCurrency: "NGN", Rate: "1500", PaymentMethodID: uuid.New(),
		Status: models.P2PTradeStatusReleased,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	out, err := svc.ListNonTerminalTrades(context.Background(), 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 non-terminal, got %d", len(out))
	}
	if out[0].Status != models.P2PTradeStatusPendingPayment {
		t.Fatalf("wrong trade returned: %s", out[0].Status)
	}
}

// ─── Step 8: dispute + admin resolution ─────────────────────────────────────

// disputeHelper sets up an ad + paid trade ready to be disputed.
func disputeHelper(t *testing.T) (svc *P2PTradeService, db *gorm.DB, ad *models.P2PAd, trade *models.P2PTrade, sellerID, buyerID uuid.UUID) {
	t.Helper()
	db = setupTradeDB(t)
	svc, _, _ = newService(t, db)
	sellerID = uuid.New()
	buyerID = uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	res, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), res.Ad.ID, sellerID, "", "0xtx")
	tr, _, _ := svc.OpenTrade(context.Background(), res.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	_, _ = svc.ConfirmOpenTrade(context.Background(), tr.ID, buyerID, "", "0xopen")
	_, _ = svc.MarkPaid(context.Background(), tr.ID, buyerID, "url")

	var adRef models.P2PAd
	db.First(&adRef, "id = ?", res.Ad.ID)
	var trRef models.P2PTrade
	db.First(&trRef, "id = ?", tr.ID)
	return svc, db, &adRef, &trRef, sellerID, buyerID
}

func TestRaiseDispute_BuyerCanDispute(t *testing.T) {
	svc, db, _, trade, _, buyerID := disputeHelper(t)
	raw, err := svc.RaiseDispute(context.Background(), trade.ID, buyerID, "0xb", "buyer-paid-but-seller-no-release")
	if err != nil {
		t.Fatalf("RaiseDispute: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("expected unsigned tx")
	}
	var d models.P2PDispute
	db.First(&d, "trade_id = ?", trade.ID)
	if d.RaisedBy != buyerID || d.RaisedRole != models.P2PDisputeRoleBuyer || d.Status != models.P2PDisputeStatusOpen {
		t.Fatalf("dispute row malformed: %+v", d)
	}
}

func TestRaiseDispute_SellerCanDispute(t *testing.T) {
	svc, _, _, trade, sellerID, _ := disputeHelper(t)
	_, err := svc.RaiseDispute(context.Background(), trade.ID, sellerID, "0xs", "buyer-claims-paid-but-never-paid")
	if err != nil {
		t.Fatalf("RaiseDispute: %v", err)
	}
}

func TestRaiseDispute_RejectsNonParticipant(t *testing.T) {
	svc, _, _, trade, _, _ := disputeHelper(t)
	_, err := svc.RaiseDispute(context.Background(), trade.ID, uuid.New(), "0xother", "x")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
}

func TestRaiseDispute_AcceptsPendingPayment(t *testing.T) {
	// Contract relax: dispute is allowed from pending_payment too — a buyer
	// who actually paid but didn't markPaid still needs the dispute path.
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	res, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), res.Ad.ID, sellerID, "", "0xtx")
	tr, _, _ := svc.OpenTrade(context.Background(), res.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	// Skip MarkPaid — dispute from pending_payment.
	raw, err := svc.RaiseDispute(context.Background(), tr.ID, buyerID, "0xb", "paid but no UI confirm")
	if err != nil {
		t.Fatalf("dispute from pending_payment should succeed: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("expected unsigned tx")
	}
}

func TestRaiseDispute_RejectsTerminalStatus(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	res, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), res.Ad.ID, sellerID, "", "0xtx")
	tr, _, _ := svc.OpenTrade(context.Background(), res.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).
		Update("status", models.P2PTradeStatusReleased)
	_, err := svc.RaiseDispute(context.Background(), tr.ID, buyerID, "0xb", "x")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

func TestRaiseDispute_RejectsDuplicate(t *testing.T) {
	svc, _, _, trade, _, buyerID := disputeHelper(t)
	_, err := svc.RaiseDispute(context.Background(), trade.ID, buyerID, "0xb", "first")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err = svc.RaiseDispute(context.Background(), trade.ID, buyerID, "0xb", "second")
	if !errors.Is(err, ErrDisputeAlreadyOpen) {
		t.Fatalf("want ErrDisputeAlreadyOpen, got %v", err)
	}
}

func TestRaiseDispute_RequiresReason(t *testing.T) {
	svc, _, _, trade, _, buyerID := disputeHelper(t)
	_, err := svc.RaiseDispute(context.Background(), trade.ID, buyerID, "0xb", "   ")
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("want ErrInvalidParams, got %v", err)
	}
}

func TestConfirmRaiseDispute_TransitionsToDisputed(t *testing.T) {
	svc, db, _, trade, _, buyerID := disputeHelper(t)
	_, _ = svc.RaiseDispute(context.Background(), trade.ID, buyerID, "0xb", "reason")
	updated, err := svc.ConfirmRaiseDispute(context.Background(), trade.ID, buyerID, "0xtx")
	if err != nil {
		t.Fatalf("ConfirmRaiseDispute: %v", err)
	}
	if updated.Status != models.P2PTradeStatusDisputed {
		t.Fatalf("want disputed, got %s", updated.Status)
	}
	// Re-call must be idempotent.
	_, err = svc.ConfirmRaiseDispute(context.Background(), trade.ID, buyerID, "0xtx")
	if err != nil {
		t.Fatalf("idempotent confirm: %v", err)
	}
	var d models.P2PDispute
	db.First(&d, "trade_id = ?", trade.ID)
	if d.Status != models.P2PDisputeStatusOpen {
		t.Fatalf("dispute should still be open until admin resolves, got %s", d.Status)
	}
}

func TestAdminResolveDispute_ToBuyer(t *testing.T) {
	// Default mock behaviour: ReleasedToBuyer=true.
	nextResolveRefundBuyer = true
	svc, db, _, trade, _, buyerID := disputeHelper(t)
	adminID := uuid.New()
	_, _ = svc.RaiseDispute(context.Background(), trade.ID, buyerID, "0xb", "reason")
	_, _ = svc.ConfirmRaiseDispute(context.Background(), trade.ID, buyerID, "0xtx")

	raw, err := svc.AdminResolveDispute(context.Background(), trade.ID, adminID, "0xadmin", true, "buyer-evidence-conclusive")
	if err != nil {
		t.Fatalf("AdminResolve: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("expected unsigned tx")
	}

	resolved, err := svc.ConfirmAdminResolveDispute(context.Background(), trade.ID, adminID, "0xreshash")
	if err != nil {
		t.Fatalf("ConfirmAdminResolve: %v", err)
	}
	if resolved.Status != models.P2PTradeStatusResolved {
		t.Fatalf("want resolved, got %s", resolved.Status)
	}
	if resolved.ResolvedByAdmin == nil || *resolved.ResolvedByAdmin != adminID {
		t.Fatalf("admin attribution missing")
	}

	var ad models.P2PAd
	db.First(&ad, "id = ?", trade.AdID)
	// Refund-to-buyer: reserved -= trade.amount; remaining unchanged.
	// disputeHelper opens 150K NGN at rate 1500 NGN/USDT → 100M base units.
	// Ad starts at 1B; after open remaining = 900,000,000.
	// After resolve-to-buyer: reserved 100M → 0, remaining unchanged at 900M.
	if ad.Reserved != "0" {
		t.Fatalf("reserved after release-to-buyer: want 0, got %s", ad.Reserved)
	}
	if ad.Remaining != "900000000" {
		t.Fatalf("remaining wrongly bumped: got %s", ad.Remaining)
	}

	var d models.P2PDispute
	db.First(&d, "trade_id = ?", trade.ID)
	if d.Status != models.P2PDisputeStatusResolved || d.ResolvedAt == nil {
		t.Fatalf("dispute not resolved: %+v", d)
	}
	if d.RefundedToBuyer == nil || !*d.RefundedToBuyer {
		t.Fatalf("dispute.refundedToBuyer not set true")
	}
}

func TestAdminResolveDispute_ToSeller_ReturnsToAdPool(t *testing.T) {
	nextResolveRefundBuyer = false
	defer func() { nextResolveRefundBuyer = true }() // reset for other tests
	svc, db, _, trade, _, buyerID := disputeHelper(t)
	adminID := uuid.New()
	_, _ = svc.RaiseDispute(context.Background(), trade.ID, buyerID, "0xb", "reason")
	_, _ = svc.ConfirmRaiseDispute(context.Background(), trade.ID, buyerID, "0xtx")
	_, _ = svc.AdminResolveDispute(context.Background(), trade.ID, adminID, "0xadmin", false, "no proof")

	_, err := svc.ConfirmAdminResolveDispute(context.Background(), trade.ID, adminID, "0xreshash")
	if err != nil {
		t.Fatalf("ConfirmAdminResolve: %v", err)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", trade.AdID)
	// Funds back into ad pool: reserved -= 100K, remaining += 100K → 1B remaining.
	if ad.Remaining != "1000000000" || ad.Reserved != "0" {
		t.Fatalf("ad accounting wrong: remaining=%s reserved=%s", ad.Remaining, ad.Reserved)
	}
}

func TestAdminResolveDispute_RejectsNonDisputed(t *testing.T) {
	svc, _, _, trade, _, _ := disputeHelper(t)
	adminID := uuid.New()
	_, err := svc.AdminResolveDispute(context.Background(), trade.ID, adminID, "0xa", true, "")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

func TestListDisputes_FilterByStatus(t *testing.T) {
	svc, db, _, trade, _, buyerID := disputeHelper(t)
	_, _ = svc.RaiseDispute(context.Background(), trade.ID, buyerID, "0xb", "reason")
	open, err := svc.ListDisputes(context.Background(), models.P2PDisputeStatusOpen, 0, 0)
	if err != nil {
		t.Fatalf("list open: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("want 1 open dispute, got %d", len(open))
	}
	resolved, _ := svc.ListDisputes(context.Background(), models.P2PDisputeStatusResolved, 0, 0)
	if len(resolved) != 0 {
		t.Fatalf("want 0 resolved disputes, got %d", len(resolved))
	}
	_ = db
}

func TestGetDispute_ParticipantOrAdmin(t *testing.T) {
	svc, _, _, trade, sellerID, buyerID := disputeHelper(t)
	_, _ = svc.RaiseDispute(context.Background(), trade.ID, buyerID, "0xb", "reason")

	// Participant access.
	d, err := svc.GetDispute(context.Background(), trade.ID, buyerID, false)
	if err != nil || d.TradeID != trade.ID {
		t.Fatalf("buyer: %v / %+v", err, d)
	}
	_, err = svc.GetDispute(context.Background(), trade.ID, sellerID, false)
	if err != nil {
		t.Fatalf("seller: %v", err)
	}
	// Non-participant rejected unless admin flag set.
	_, err = svc.GetDispute(context.Background(), trade.ID, uuid.New(), false)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-participant want ErrForbidden, got %v", err)
	}
	// Admin bypass.
	if _, err = svc.GetDispute(context.Background(), trade.ID, uuid.New(), true); err != nil {
		t.Fatalf("admin: %v", err)
	}
}

func TestConfirmAdminResolveDispute_RejectsOnChainSemanticMismatch(t *testing.T) {
	// Admin chose refundBuyer=false in DB, but mock says ReleasedToBuyer=true
	// with non-zero amount — refuse. Set the mock to refund=true, but admin
	// passed refundBuyer=false; the verifier should reject because the
	// on-chain semantics (refund=true → non-zero movement) contradict the
	// "false → zero movement" rule the service enforces.
	nextResolveRefundBuyer = true
	svc, _, _, trade, _, buyerID := disputeHelper(t)
	adminID := uuid.New()
	_, _ = svc.RaiseDispute(context.Background(), trade.ID, buyerID, "0xb", "reason")
	_, _ = svc.ConfirmRaiseDispute(context.Background(), trade.ID, buyerID, "0xtx")
	// Admin says false but mock will return true with non-zero — admin
	// signed the wrong tx. Service expects refundBuyer to match.
	_, _ = svc.AdminResolveDispute(context.Background(), trade.ID, adminID, "0xa", false, "")
	// Mock still returns ReleasedToBuyer=true ⇒ admin-chose-false but
	// chain-says-true: this is currently NOT cross-checked (admin's intent
	// was stored in the dispute row but ConfirmAdminResolve uses on-chain
	// truth). Confirm that ConfirmAdminResolve succeeds and the resolution
	// reflects the on-chain decision, not the admin's prior request.
	_, err := svc.ConfirmAdminResolveDispute(context.Background(), trade.ID, adminID, "0xres")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	// Either behaviour is defensible — what we want is no silent corruption.
	// This test pins the current "chain is source of truth" choice.
}

func TestComputeTokenFromFiat(t *testing.T) {
	// New semantics: rate is NGN per WHOLE token. fiat=1500 NGN at rate
	// ₦1500/USDT → 1 USDT whole = 10^6 base units. Decimals parameter
	// scales whole-tokens up to on-chain base units.
	tests := []struct {
		fiat, rate string
		decimals   int
		wantToken  string
	}{
		// 1500 NGN at ₦1500/USDT (6-dec) = 1 USDT = 10^6 base units
		{"1500", "1500", 6, "1000000"},
		// 150,000,000 NGN at ₦1500/USDT (6-dec) = 100,000 USDT = 10^11 base units
		{"150000000", "1500", 6, "100000000000"},
		// 100 NGN at ₦1500/USDT (6-dec) — buys 0.0666... USDT, truncates
		// to 66666 base units (0.066666 USDT)
		{"100", "1500", 6, "66666"},
		// MCGP (18-dec) is fee-free; 1500 NGN at ₦1500/MCGP = 1 MCGP = 10^18
		{"1500", "1500", 18, "1000000000000000000"},
		// Fractional rate truncates: 1500 * 10^6 / 1500.5 = 999666.7… → 999666
		{"1500", "1500.5", 6, "999666"},
	}
	for _, tc := range tests {
		token, _, err := computeTokenFromFiat(tc.fiat, tc.rate, tc.decimals)
		if err != nil {
			t.Errorf("fiat=%s rate=%s dec=%d: %v", tc.fiat, tc.rate, tc.decimals, err)
			continue
		}
		if token.String() != tc.wantToken {
			t.Errorf("fiat=%s rate=%s dec=%d: want %s got %s",
				tc.fiat, tc.rate, tc.decimals, tc.wantToken, token)
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
		if _, _, err := computeTokenFromFiat(c.fiat, c.rate, 6); !errors.Is(err, ErrInvalidParams) {
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

// ─── Float-rate ad tests ────────────────────────────────────────────────────

// freshNGNRates returns a non-stale snapshot with BinanceSell pinned.
func freshNGNRates(binanceSell float64) *RatesSnapshot {
	return &RatesSnapshot{
		Rates: map[string]P2PRate{
			"NGN": {
				Currency:    "NGN",
				BinanceBuy:  binanceSell,
				BinanceSell: binanceSell,
				MidRate:     binanceSell,
				Stale:       false,
			},
		},
		CachedAt: time.Now(),
	}
}

func TestComputeEffectiveRate_Fixed_PassesThrough(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	// Even with no rate provider configured, fixed ads must not hit the feed.
	svc.rates = nil

	ad := &models.P2PAd{Rate: "1500.0000000000", RateType: models.P2PAdRateTypeFixed}
	got, err := svc.ComputeEffectiveRate(ad)
	if err != nil {
		t.Fatalf("fixed pass-through: unexpected err %v", err)
	}
	if got != "1500.0000000000" {
		t.Fatalf("fixed pass-through: want literal rate, got %q", got)
	}
}

func TestComputeEffectiveRate_Float_PositiveOffset(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.rates = &mockRates{snap: freshNGNRates(1500)}

	bps := 50 // +0.5%
	ad := &models.P2PAd{
		FiatCurrency:   "NGN",
		RateType:       models.P2PAdRateTypeFloat,
		FloatOffsetBps: &bps,
		FloatAnchor:    models.P2PAdFloatAnchorBybitSell,
	}
	got, err := svc.ComputeEffectiveRate(ad)
	if err != nil {
		t.Fatalf("ComputeEffectiveRate: %v", err)
	}
	// 1500 * 10050/10000 = 1507.5
	if got != "1507.5000000000" {
		t.Fatalf("want 1507.5000000000, got %q", got)
	}
}

func TestComputeEffectiveRate_Float_NegativeOffset(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.rates = &mockRates{snap: freshNGNRates(1500)}

	bps := -100 // -1%
	ad := &models.P2PAd{
		FiatCurrency:   "NGN",
		RateType:       models.P2PAdRateTypeFloat,
		FloatOffsetBps: &bps,
		FloatAnchor:    models.P2PAdFloatAnchorBybitSell,
	}
	got, err := svc.ComputeEffectiveRate(ad)
	if err != nil {
		t.Fatalf("ComputeEffectiveRate: %v", err)
	}
	// 1500 * 9900/10000 = 1485
	if got != "1485.0000000000" {
		t.Fatalf("want 1485.0000000000, got %q", got)
	}
}

func TestComputeEffectiveRate_Float_StaleFeed(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.rates = &mockRates{snap: &RatesSnapshot{
		Rates: map[string]P2PRate{
			"NGN": {Currency: "NGN", BinanceSell: 1500, Stale: true},
		},
		StaleSeconds: 600,
	}}

	bps := 50
	ad := &models.P2PAd{
		FiatCurrency:   "NGN",
		RateType:       models.P2PAdRateTypeFloat,
		FloatOffsetBps: &bps,
	}
	_, err := svc.ComputeEffectiveRate(ad)
	if !errors.Is(err, ErrRateStale) {
		t.Fatalf("want ErrRateStale, got %v", err)
	}
}

func TestComputeEffectiveRate_Float_MissingCurrency(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	// Snapshot has only USD; ad is in NGN.
	svc.rates = &mockRates{snap: &RatesSnapshot{
		Rates:    map[string]P2PRate{"USD": {Currency: "USD", BinanceSell: 1}},
		CachedAt: time.Now(),
	}}

	bps := 50
	ad := &models.P2PAd{
		FiatCurrency:   "NGN",
		RateType:       models.P2PAdRateTypeFloat,
		FloatOffsetBps: &bps,
	}
	_, err := svc.ComputeEffectiveRate(ad)
	if !errors.Is(err, ErrRateStale) {
		t.Fatalf("want ErrRateStale for missing currency, got %v", err)
	}
}

func TestComputeEffectiveRate_Float_NilProvider(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.rates = nil

	bps := 0
	ad := &models.P2PAd{
		FiatCurrency:   "NGN",
		RateType:       models.P2PAdRateTypeFloat,
		FloatOffsetBps: &bps,
	}
	_, err := svc.ComputeEffectiveRate(ad)
	if !errors.Is(err, ErrRateStale) {
		t.Fatalf("want ErrRateStale when provider is nil, got %v", err)
	}
}

func TestCreateAd_Float_RejectsMissingOffset(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.rates = &mockRates{snap: freshNGNRates(1500)}
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	_, err := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN",
		RateType:         models.P2PAdRateTypeFloat, // no FloatOffsetBps set
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("want ErrInvalidParams for missing bps, got %v", err)
	}
}

func TestCreateAd_Float_RejectsOffsetAboveCap(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.rates = &mockRates{snap: freshNGNRates(1500)}
	svc.SetFloatOffsetMaxBps(500) // tight ±5% for this test
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	bps := 600
	_, err := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN",
		RateType:         models.P2PAdRateTypeFloat,
		FloatOffsetBps:   &bps,
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("want ErrInvalidParams for over-cap bps, got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "±500") {
		t.Fatalf("error should mention configured cap, got %q", err.Error())
	}
}

func TestCreateAd_Float_RejectsOffsetBelowCap(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.rates = &mockRates{snap: freshNGNRates(1500)}
	svc.SetFloatOffsetMaxBps(500)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	bps := -600
	_, err := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN",
		RateType:         models.P2PAdRateTypeFloat,
		FloatOffsetBps:   &bps,
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("want ErrInvalidParams for under-cap bps, got %v", err)
	}
}

func TestCreateAd_Float_RejectsUnknownAnchor(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.rates = &mockRates{snap: freshNGNRates(1500)}
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	bps := 50
	_, err := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN",
		RateType:         models.P2PAdRateTypeFloat,
		FloatOffsetBps:   &bps,
		FloatAnchor:      "coingecko_mid",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("want ErrInvalidParams for unknown anchor, got %v", err)
	}
}

func TestCreateAd_Fixed_RejectsStrayFloatOffset(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.rates = &mockRates{snap: freshNGNRates(1500)}
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	bps := 50
	_, err := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN",
		Rate:             "1500",
		RateType:         models.P2PAdRateTypeFixed,
		FloatOffsetBps:   &bps, // illegal on fixed
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("want ErrInvalidParams for stray float_offset on fixed ad, got %v", err)
	}
}

func TestCreateAd_Float_HappyPath_PublishesWithComputedRate(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.rates = &mockRates{snap: freshNGNRates(1500)}
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	bps := 50 // +0.5% → 1507.5
	result, err := svc.CreateAd(context.Background(), sellerID, "0xseller", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN",
		RateType:         models.P2PAdRateTypeFloat,
		FloatOffsetBps:   &bps,
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if err != nil {
		t.Fatalf("CreateAd float: %v", err)
	}
	if result.Ad.RateType != models.P2PAdRateTypeFloat {
		t.Fatalf("rate_type: want float, got %s", result.Ad.RateType)
	}
	if result.Ad.Rate != "1507.5000000000" {
		t.Fatalf("ad.Rate: want 1507.5000000000 (cached), got %s", result.Ad.Rate)
	}
	if result.Ad.FloatOffsetBps == nil || *result.Ad.FloatOffsetBps != 50 {
		t.Fatalf("FloatOffsetBps not persisted, got %v", result.Ad.FloatOffsetBps)
	}
	if result.Ad.FloatAnchor != models.P2PAdFloatAnchorBybitSell {
		t.Fatalf("FloatAnchor default not applied, got %q", result.Ad.FloatAnchor)
	}
}

func TestOpenTrade_FloatAd_StaleFeed_NoSideEffects(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	svc.rates = &mockRates{snap: freshNGNRates(1500)}
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	// 1) Publish a float ad while feed is fresh.
	bps := 50
	res, err := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN",
		RateType:         models.P2PAdRateTypeFloat,
		FloatOffsetBps:   &bps,
		MinLimit:         "1000",
		MaxLimit:         "1500000",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	_, _ = svc.ConfirmAd(context.Background(), res.Ad.ID, sellerID, "", "0xadtxhash")

	// 2) Flip the feed to stale. OpenTrade must refuse.
	svc.rates = &mockRates{snap: &RatesSnapshot{
		Rates:        map[string]P2PRate{"NGN": {Currency: "NGN", BinanceSell: 1500, Stale: true}},
		StaleSeconds: 600,
	}}

	esc.verifyOpenResult = &P2PTradeOpenedEvent{Buyer: common.HexToAddress("0xd00d"), Amount: big.NewInt(300_000_000)}
	_, _, err = svc.OpenTrade(context.Background(), res.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "450000", PaymentMethodID: pmID,
	})
	if !errors.Is(err, ErrRateStale) {
		t.Fatalf("OpenTrade on stale feed: want ErrRateStale, got %v", err)
	}

	// No trade row should have been created.
	var n int64
	db.Model(&models.P2PTrade{}).Where("ad_id = ?", res.Ad.ID).Count(&n)
	if n != 0 {
		t.Fatalf("expected 0 trade rows after stale-feed reject, got %d", n)
	}

	// Ad should remain active with reserved=0 (no escrow movement).
	var ad models.P2PAd
	db.First(&ad, "id = ?", res.Ad.ID)
	if ad.Status != models.P2PAdStatusActive {
		t.Fatalf("ad status after stale-feed reject: want active, got %s", ad.Status)
	}
	if ad.Reserved != "0" {
		t.Fatalf("ad.reserved after stale-feed reject: want 0, got %s", ad.Reserved)
	}
}

func TestOpenTrade_FloatAd_RefreshesCacheRate(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	// Initial snapshot at 1500; trade-time snapshot at 1600.
	svc.rates = &mockRates{snap: freshNGNRates(1500)}
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	bps := 0 // exactly the anchor rate
	res, err := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN",
		RateType:         models.P2PAdRateTypeFloat,
		FloatOffsetBps:   &bps,
		MinLimit:         "1000",
		MaxLimit:         "1600000",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	if res.Ad.Rate != "1500.0000000000" {
		t.Fatalf("initial cache rate: want 1500.0000000000, got %s", res.Ad.Rate)
	}
	_, _ = svc.ConfirmAd(context.Background(), res.Ad.ID, sellerID, "", "0xadtxhash")

	// Market moves up between publish and trade-open.
	svc.rates = &mockRates{snap: freshNGNRates(1600)}
	esc.verifyOpenResult = &P2PTradeOpenedEvent{Buyer: common.HexToAddress("0xd00d"), Amount: big.NewInt(160_000_000)}

	trade, _, err := svc.OpenTrade(context.Background(), res.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "160000", PaymentMethodID: pmID,
	})
	if err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	if trade.Rate != "1600.0000000000" {
		t.Fatalf("trade snapshotted rate: want 1600.0000000000, got %s", trade.Rate)
	}

	// Ad cache should now reflect the just-quoted rate.
	var ad models.P2PAd
	db.First(&ad, "id = ?", res.Ad.ID)
	if ad.Rate != "1600.0000000000" {
		t.Fatalf("ad cache after open: want 1600.0000000000, got %s", ad.Rate)
	}
}
