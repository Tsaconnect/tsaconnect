package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testBuyEVM = "0x2222222222222222222222222222222222222222"

func testBuyQuote() *StableBuyQuote {
	return &StableBuyQuote{
		Asset:           "USDC",
		Currency:        "NGN",
		FiatMinor:       big.NewInt(16502500),
		GrossAssetUnits: big.NewInt(100_000_000),
		AssetUnits:      big.NewInt(98_450_000),
		// 167623 = floor(165025 * 10000 / 9845) — what QuoteStableBuy actually
		// produces for this fixture. A rounder-looking number here invites a
		// "why doesn't this match?" detour during Task 6.
		RatePerUnitMinor: big.NewInt(167623),
		ExpiresAt:        time.Now().Add(15 * time.Minute),
	}
}

func newStableBuyDBService(t *testing.T) (*ITService, uuid.UUID) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}, &models.FiatVirtualAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	userID := uuid.New()
	va := &models.FiatVirtualAccount{
		ID: uuid.New(), UserID: userID, Currency: "NGN",
		AccountNumber: "1234567890", BankName: "Wema Bank", AccountName: "TSA / Ada Okafor",
		Reference: "ITVA-" + userID.String(),
	}
	if err := db.Create(va).Error; err != nil {
		t.Fatalf("seed va: %v", err)
	}
	s := NewITService(db, nil, stubFx{ngn: 1500})
	s.SetStableSpreadBps(155)
	// A rate quoter is REQUIRED, not incidental: settleStableBuyFromCharge
	// (Task 6) prices at the live rate when money lands, so without one every
	// settlement test would park the intent in awaiting_rate and look like an
	// implementation bug rather than a fixture gap. buyRateQuoter is defined in
	// it_stable_buy_quote_test.go (Task 4) — same package, do not redefine it.
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	// The buy leg is gated on HasStableBuy (converter + merchant id + rate
	// source + deliverer), so the fixture must satisfy all four or every quote
	// and every intent creation refuses with "not enabled" — a fixture gap
	// that would read as an implementation bug. Tests that care about the
	// converter's balances or the deliverer's calls override these.
	s.SetStableConverter(&stubConverter{}, "merchant_fixture")
	s.SetStableDeliverer(&stubDeliverer{id: "trf_fixture"})
	return s, userID
}

func TestCreateStableBuyIntent_ReturnsTheUsersStandingVA(t *testing.T) {
	// The user pays into the account they already have. No per-purchase VA is
	// minted: dynamic VAs were removed in July after "Invalid Amount" and
	// silent-expiry failures in production.
	s, userID := newStableBuyDBService(t)

	intent, pay, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pay.AccountNumber != "1234567890" {
		t.Fatalf("account = %q, want the user's standing VA", pay.AccountNumber)
	}
	if intent.Status != models.ITStatusPendingPayment {
		t.Fatalf("status = %q, want pending_payment", intent.Status)
	}
	if intent.DestChain != "POLYGON" || intent.DestAddress != testBuyEVM {
		t.Fatalf("delivery target not persisted: %q %q", intent.DestChain, intent.DestAddress)
	}
	if intent.Direction != models.ITDirectionBuy {
		t.Fatalf("direction = %q, want buy", intent.Direction)
	}
}

func TestCreateStableBuyIntent_RejectsUndeliverableTarget(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	q := testBuyQuote()
	q.Asset = "USDT"

	// Flutterwave rejects Base+USDT, and a rejected transfer to a live address
	// is not recoverable — so this must fail before an intent exists.
	_, _, err := s.CreateStableBuyIntent(context.Background(), userID, q, "BASE", testBuyEVM)
	if !errors.Is(err, ErrUnsupportedChainAsset) {
		t.Fatalf("err = %v, want ErrUnsupportedChainAsset", err)
	}

	var n int64
	s.db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 {
		t.Fatalf("%d intents written for a rejected target, want 0", n)
	}
}

func TestCreateStableBuyIntent_OnlyOneAwaitingPayment(t *testing.T) {
	// pending_payment is the only state that can claim an incoming charge, so
	// two at once would make attribution ambiguous.
	s, userID := newStableBuyDBService(t)

	if _, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM); err != nil {
		t.Fatalf("first intent: %v", err)
	}
	_, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if !errors.Is(err, ErrStableBuyAlreadyOpen) {
		t.Fatalf("err = %v, want ErrStableBuyAlreadyOpen", err)
	}
}

func TestCreateStableBuyIntent_FundedIntentDoesNotBlockANewOne(t *testing.T) {
	// An intent past pending_payment has already been funded and cannot claim
	// further money, so it must not block a second purchase.
	s, userID := newStableBuyDBService(t)

	first, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("first intent: %v", err)
	}
	if err := s.db.Model(first).Update("status", models.ITStatusConverting).Error; err != nil {
		t.Fatalf("advance: %v", err)
	}
	if _, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM); err != nil {
		t.Fatalf("second intent rejected while first was converting: %v", err)
	}
}

func TestCreateStableBuyIntent_RequiresAVirtualAccount(t *testing.T) {
	s, _ := newStableBuyDBService(t)
	_, _, err := s.CreateStableBuyIntent(context.Background(), uuid.New(), testBuyQuote(), "POLYGON", testBuyEVM)
	if !errors.Is(err, ErrStableBuyNoVA) {
		t.Fatalf("err = %v, want ErrStableBuyNoVA", err)
	}
}

// newStableBuyDeliveryTestDB wires a DB with a real `users` table (raw DDL,
// not AutoMigrate — models.User's id column carries a PostgreSQL-only
// `default:gen_random_uuid()` that SQLite's driver cannot parse; mirrors
// referral_graph_service_test.go's setupReferralGraphTestDB) plus
// wallet_addresses (AutoMigrate is fine there — no Postgres-only default).
func newStableBuyDeliveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		username TEXT NOT NULL UNIQUE,
		email TEXT NOT NULL UNIQUE,
		password TEXT NOT NULL,
		wallet_address TEXT
	)`).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	if err := db.AutoMigrate(&models.WalletAddress{}); err != nil {
		t.Fatalf("migrate wallet_addresses: %v", err)
	}
	return db
}

// TestDeliverableChainsForUser_ReadsBothAddressSources is the test the brief
// calls out by name: it seeds BOTH delivery-address sources — a
// users.wallet_address row (EVM) and a wallet_addresses row with
// chain_key = "solana", lowercase, exactly as the real address-registration
// path writes it (see wallet_nonevm handlers) — and proves DeliverableChainsForUser
// reads both. A test seeding only one source would still pass against the dead
// models.Wallet.Addresses column this replaces; seeding both is what catches it.
func TestDeliverableChainsForUser_ReadsBothAddressSources(t *testing.T) {
	db := newStableBuyDeliveryTestDB(t)
	userID := uuid.New()
	if err := db.Exec(`INSERT INTO users (id, name, username, email, password, wallet_address)
		VALUES (?, 'Ada Okafor', 'ada', 'ada@test.com', 'x', ?)`,
		userID.String(), testBuyEVM).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	solRow := models.WalletAddress{
		UserID:   userID,
		WalletID: testBuyEVM,
		ChainKey: "solana", // lowercase, as the real producer writes it
		Family:   "solana",
		Address:  "5Q544fKrFoe6tsEbD7S8EmxGTJYAKtTVhAW5Q5pge4j1",
	}
	if err := db.Create(&solRow).Error; err != nil {
		t.Fatalf("seed solana address: %v", err)
	}

	s := NewITService(db, nil, stubFx{ngn: 1500})

	usdc, err := s.DeliverableChainsForUser(context.Background(), userID, "USDC")
	if err != nil {
		t.Fatalf("USDC: unexpected error: %v", err)
	}
	wantUSDC := []string{"BASE", "ETHEREUM", "POLYGON", "SOLANA"}
	if !slicesEqual(usdc, wantUSDC) {
		t.Fatalf("USDC chains = %v, want %v (EVM address source + wallet_addresses solana source both required)", usdc, wantUSDC)
	}

	usdt, err := s.DeliverableChainsForUser(context.Background(), userID, "USDT")
	if err != nil {
		t.Fatalf("USDT: unexpected error: %v", err)
	}
	wantUSDT := []string{"ETHEREUM", "POLYGON", "SOLANA"}
	if !slicesEqual(usdt, wantUSDT) {
		t.Fatalf("USDT chains = %v, want %v (BASE only delivers USDC, must be absent)", usdt, wantUSDT)
	}
}

// TestUserDeliveryAddress_ReadsEachSource proves UserDeliveryAddress resolves
// an EVM chain from users.wallet_address and SOLANA from wallet_addresses —
// the same two sources DeliverableChainsForUser draws on, exercised
// individually so a regression in either lookup fails on its own chain.
func TestUserDeliveryAddress_ReadsEachSource(t *testing.T) {
	db := newStableBuyDeliveryTestDB(t)
	userID := uuid.New()
	if err := db.Exec(`INSERT INTO users (id, name, username, email, password, wallet_address)
		VALUES (?, 'Ada Okafor', 'ada', 'ada@test.com', 'x', ?)`,
		userID.String(), testBuyEVM).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	const solAddr = "5Q544fKrFoe6tsEbD7S8EmxGTJYAKtTVhAW5Q5pge4j1"
	if err := db.Create(&models.WalletAddress{
		UserID: userID, WalletID: testBuyEVM, ChainKey: "solana", Family: "solana", Address: solAddr,
	}).Error; err != nil {
		t.Fatalf("seed solana address: %v", err)
	}

	s := NewITService(db, nil, stubFx{ngn: 1500})

	for _, chain := range []string{"ETHEREUM", "POLYGON", "BASE"} {
		got, err := s.UserDeliveryAddress(context.Background(), userID, chain)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", chain, err)
		}
		if got != testBuyEVM {
			t.Fatalf("%s address = %q, want %q", chain, got, testBuyEVM)
		}
	}

	got, err := s.UserDeliveryAddress(context.Background(), userID, "SOLANA")
	if err != nil {
		t.Fatalf("SOLANA: unexpected error: %v", err)
	}
	if got != solAddr {
		t.Fatalf("SOLANA address = %q, want %q", got, solAddr)
	}

	if _, err := s.UserDeliveryAddress(context.Background(), uuid.New(), "SOLANA"); !errors.Is(err, ErrStableBuyNoWallet) {
		t.Fatalf("unknown user: err = %v, want ErrStableBuyNoWallet", err)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
