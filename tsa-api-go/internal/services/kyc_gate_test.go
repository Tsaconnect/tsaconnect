package services

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

// testUsersDDL is the shared users table for service tests. Raw DDL, not
// AutoMigrate: models.User's id column carries a PostgreSQL-only
// `default:gen_random_uuid()` that SQLite's driver cannot parse (repo
// convention, see the same note in it_stable_buy_test.go).
//
// verification_status defaults to 'pending' exactly as the column does in
// production, so a fixture that forgets to seed a user gets the unverified
// behaviour rather than an accidental pass.
const testUsersDDL = `CREATE TABLE users (
	id TEXT PRIMARY KEY,
	name TEXT,
	username TEXT,
	email TEXT,
	password TEXT,
	phone_number TEXT,
	wallet_address TEXT,
	verification_status TEXT DEFAULT 'pending',
	account_status TEXT DEFAULT 'active',
	verified_legal_name TEXT,
	verified_legal_name_at DATETIME, declared_given_names TEXT, declared_surname TEXT,
	suspension_reason TEXT,
	suspended_at DATETIME,
	suspended_by TEXT,
	suspended_from_status TEXT
)`

func createTestUsersTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(testUsersDDL).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
}

// seedUserAt inserts a user carrying the given verification status and returns
// its id. Raw INSERT to match the raw table above.
//
// A verified user is seeded WITH a legal name, because that is what a real
// passed verification leaves behind and what the matcher needs. The
// name-less-but-verified case is a distinct fixture below — it is a real state
// the provider can produce, not the ordinary one.
func seedUserAt(t *testing.T, db *gorm.DB, id uuid.UUID, status string) uuid.UUID {
	t.Helper()
	legalName := ""
	if status == models.VerificationStatusVerified {
		legalName = "Ada Okafor"
	}
	if err := db.Exec(
		`INSERT INTO users (id, name, email, wallet_address, verification_status, verified_legal_name) VALUES (?, ?, ?, ?, ?, ?)`,
		id, "Ada Okafor", id.String()+"@test.com", "0x000000000000000000000000000000000000dEaD", status, legalName,
	).Error; err != nil {
		t.Fatalf("seed user (%s): %v", status, err)
	}
	return id
}

// seedVerifiedUser is the fixture most tests want: a user who may transact.
func seedVerifiedUser(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	return seedUserAt(t, db, uuid.New(), models.VerificationStatusVerified)
}

// seedVerifiedWithoutLegalName is the state the gate's second clause exists
// for: the provider approved the user but withheld or failed to return the
// name, so there is nothing for the matcher to compare a sender against.
func seedVerifiedWithoutLegalName(t *testing.T, db *gorm.DB, storedName string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := db.Exec(
		`INSERT INTO users (id, name, email, wallet_address, verification_status, verified_legal_name) VALUES (?, ?, ?, ?, ?, ?)`,
		id, "Ada Okafor", id.String()+"@test.com", "0x000000000000000000000000000000000000dEaD",
		models.VerificationStatusVerified, storedName,
	).Error; err != nil {
		t.Fatalf("seed nameless verified user: %v", err)
	}
	return id
}

// unverifiedStatuses are every status that is not `verified`, plus the
// no-user-row case each test adds separately. in_review matters as much as
// pending: a user mid-Didit-session has submitted documents and feels done,
// and is exactly the person who would otherwise send money against a name we
// cannot yet check.
var unverifiedStatuses = []string{
	models.VerificationStatusPending,
	models.VerificationStatusInReview,
	models.VerificationStatusRejected,
}

// ─── The gate itself ────────────────────────────────────────────────────────

func TestRequireVerifiedForMoneyIn(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()

	for _, status := range unverifiedStatuses {
		id := seedUserAt(t, s.db, uuid.New(), status)
		if err := requireVerifiedForMoneyIn(ctx, s.db, id); !errors.Is(err, ErrKYCVerificationRequired) {
			t.Fatalf("status %q = %v, want ErrKYCVerificationRequired", status, err)
		}
	}
	// No row at all must fail closed, not fall through as "no status to
	// object to".
	if err := requireVerifiedForMoneyIn(ctx, s.db, uuid.New()); !errors.Is(err, ErrKYCVerificationRequired) {
		t.Fatalf("unknown user = %v, want ErrKYCVerificationRequired", err)
	}
	verified := seedVerifiedUser(t, s.db)
	if err := requireVerifiedForMoneyIn(ctx, s.db, verified); err != nil {
		t.Fatalf("verified user refused: %v", err)
	}
}

func TestIsKYCVerified(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	for _, status := range unverifiedStatuses {
		if ok, err := IsKYCVerified(ctx, s.db, seedUserAt(t, s.db, uuid.New(), status)); ok || err != nil {
			t.Fatalf("status %q = %v %v, want not verified", status, ok, err)
		}
	}
	if ok, err := IsKYCVerified(ctx, s.db, uuid.New()); ok || err != nil {
		t.Fatalf("unknown user = %v %v, want not verified", ok, err)
	}
	// Status only: a verified user whose name lookup failed is still verified.
	for _, id := range []uuid.UUID{seedVerifiedUser(t, s.db), seedVerifiedWithoutLegalName(t, s.db, "")} {
		if ok, err := IsKYCVerified(ctx, s.db, id); !ok || err != nil {
			t.Fatalf("verified user = %v %v", ok, err)
		}
	}
}

// ─── Money in #1: the MCGP buy order ────────────────────────────────────────

func TestOpenBuyOrder_RefusesUnverifiedBuyer(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	if _, err := s.SetRate(ctx, uuid.New(), 20000, 500); err != nil {
		t.Fatalf("set rate: %v", err)
	}

	for _, status := range unverifiedStatuses {
		id := seedUserAt(t, s.db, uuid.New(), status)
		_, err := s.OpenBuyOrder(ctx, id, "0xWALLET", "100000")
		if !errors.Is(err, ErrKYCVerificationRequired) {
			t.Fatalf("OpenBuyOrder for %q user = %v, want ErrKYCVerificationRequired", status, err)
		}
		var n int64
		s.db.Model(&models.ITIntent{}).Where("user_id = ?", id).Count(&n)
		if n != 0 {
			t.Fatalf("status %q: %d intent(s) written despite the refusal", status, n)
		}
	}

	verified := seedVerifiedUser(t, s.db)
	if _, err := s.OpenBuyOrder(ctx, verified, "0xWALLET", "100000"); err != nil {
		t.Fatalf("verified user refused: %v", err)
	}
}

// A refused buyer must not lose the order they already have. The gate sits
// ahead of everything that writes (the stale-order sweep included), so a user
// who was verified when they opened an order and is refused a new one still
// has the first order to pay.
func TestOpenBuyOrder_RefusalLeavesThePriorOrderPayable(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	if _, err := s.SetRate(ctx, uuid.New(), 20000, 500); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	id := uuid.New()
	// Opened before the gate shipped: no user row was ever required.
	prior, err := s.CreateBuyIntent(ctx, id, "0xWALLET", "100000", nil)
	if err != nil {
		t.Fatalf("seed prior order: %v", err)
	}

	if _, err := s.OpenBuyOrder(ctx, id, "0xWALLET", "200000"); !errors.Is(err, ErrKYCVerificationRequired) {
		t.Fatalf("OpenBuyOrder = %v, want ErrKYCVerificationRequired", err)
	}

	var got models.ITIntent
	if err := s.db.First(&got, "id = ?", prior.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != models.ITStatusPendingPayment {
		t.Fatalf("prior order status = %q, want it still awaiting payment — the gate cancelled an order the user may already have paid into", got.Status)
	}
}

// The gate is on OPENING an order. Orders already in flight when it ships were
// opened by users who are still unverified, and their money is already moving —
// settlement must not start refusing them.
func TestAutoCreditFromVA_SettlesAnOrderOpenedBeforeTheGate(t *testing.T) {
	s, user, va := newVATestService(t)
	ctx := context.Background()
	if err := s.db.Exec(`UPDATE users SET verification_status = ? WHERE id = ?`,
		models.VerificationStatusPending, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	// CreateBuyIntent, not OpenBuyOrder: this is the row as it exists in
	// production today, written before the gate was in the code path.
	if _, err := s.CreateBuyIntent(ctx, user.ID, user.WalletAddress, "100000", nil); err != nil {
		t.Fatalf("seed pre-gate order: %v", err)
	}

	_ = s.AutoCreditFromVA(ctx, va.FwCustomerID, va.Reference, testChargeID, "5000")

	rows := buyIntents(t, s, user.ID)
	if len(rows) != 1 {
		t.Fatalf("want the pre-gate order reused, got %d intents", len(rows))
	}
	if rows[0].PaymentRef != testChargeID {
		t.Fatalf("payment_ref = %q, want the charge id — the payment was not attributed to an order opened before the gate", rows[0].PaymentRef)
	}
}

// ─── Money in #2: the stablecoin buy intent ─────────────────────────────────

func TestCreateStableBuyIntent_RefusesUnverifiedBuyer(t *testing.T) {
	s, _ := newStableBuyDBService(t)
	ctx := context.Background()

	for _, status := range unverifiedStatuses {
		id := seedUserAt(t, s.db, uuid.New(), status)
		_, _, err := s.CreateStableBuyIntent(ctx, id, testBuyQuote(), "POLYGON", testBuyEVM)
		if !errors.Is(err, ErrKYCVerificationRequired) {
			t.Fatalf("CreateStableBuyIntent for %q user = %v, want ErrKYCVerificationRequired", status, err)
		}
		var n int64
		s.db.Model(&models.ITIntent{}).Where("user_id = ?", id).Count(&n)
		if n != 0 {
			t.Fatalf("status %q: %d intent(s) written despite the refusal", status, n)
		}
	}
	if _, _, err := s.CreateStableBuyIntent(ctx, uuid.New(), testBuyQuote(), "POLYGON", testBuyEVM); !errors.Is(err, ErrKYCVerificationRequired) {
		t.Fatalf("unknown user = %v, want ErrKYCVerificationRequired", err)
	}
}

// ─── Money in #3: opening a P2P trade as the buyer ──────────────────────────

func TestOpenTrade_RefusesUnverifiedBuyer(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	ctx := context.Background()
	sellerID := seedVerifiedUser(t, db)
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	key, sellerAddr := newSellerKey(t)

	res, err := svc.CreateAd(ctx, sellerID, sellerAddr, makeCreateAdParams(pmID))
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	var hash [32]byte
	copy(hash[:], common.FromHex(res.Ad.VoucherHash))
	if _, err := svc.SignAd(ctx, res.Ad.ID, sellerID, sellerAddr, signVoucherForTest(t, hash, key)); err != nil {
		t.Fatalf("SignAd: %v", err)
	}

	for _, status := range unverifiedStatuses {
		buyerID := seedUserAt(t, db, uuid.New(), status)
		_, _, err := svc.OpenTrade(ctx, res.Ad.ID, buyerID, "0x000000000000000000000000000000000000bEEF",
			OpenTradeParams{FiatAmount: "1000", PaymentMethodID: pmID})
		if !errors.Is(err, ErrKYCVerificationRequired) {
			t.Fatalf("OpenTrade for %q buyer = %v, want ErrKYCVerificationRequired", status, err)
		}
		var n int64
		db.Model(&models.P2PTrade{}).Where("buyer_id = ?", buyerID).Count(&n)
		if n != 0 {
			t.Fatalf("status %q: %d trade(s) opened despite the refusal", status, n)
		}
	}

	buyerID := seedVerifiedUser(t, db)
	if _, _, err := svc.OpenTrade(ctx, res.Ad.ID, buyerID, "0x000000000000000000000000000000000000bEEF",
		OpenTradeParams{FiatAmount: "1000", PaymentMethodID: pmID}); err != nil {
		t.Fatalf("verified buyer refused: %v", err)
	}
}

// ─── Money out must not change ──────────────────────────────────────────────

// Selling MCGP is an exit. An unverified holder refused here would have their
// balance trapped, which is a worse outcome than the entry refusal above.
func TestCreateSellIntent_IsNotGatedOnVerification(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	if _, err := s.SetRate(ctx, uuid.New(), 20000, 500); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	id := seedUserAt(t, s.db, uuid.New(), models.VerificationStatusPending)

	_, err := s.CreateSellIntent(ctx, id, "0xWALLET", "1000000000000000000", nil)
	if errors.Is(err, ErrKYCVerificationRequired) {
		t.Fatal("the sell leg refused an unverified holder — money-out must not be gated")
	}
	// Any other failure is the fixture's missing chain client, which is what
	// every other sell test in this package hits too.
}

// The buy-ad (JIT-funded) opener sells crypto into someone's ad. Owner decision
// 2026-10-09: that user must be KYC-verified too. They have not deposited yet
// when the gate runs, so refusing traps nothing.
func TestOpenTrade_BuyAdOpenerMustBeVerified(t *testing.T) {
	svc, _, _, _, adID, _, db := buyAdFixture(t)
	pending := seedUserAt(t, db, uuid.New(), models.VerificationStatusPending)
	pm := seedPaymentMethod(t, db, pending, "NGN")
	_, _, err := svc.OpenTrade(context.Background(), adID, pending, "0x000000000000000000000000000000000000beef",
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: pm})
	if !errors.Is(err, ErrKYCVerificationRequired) {
		t.Fatalf("unverified buy-ad opener must be refused: %v", err)
	}
	ok := seedVerifiedUser(t, db)
	pm2 := seedPaymentMethod(t, db, ok, "NGN")
	if _, _, err := svc.OpenTrade(context.Background(), adID, ok, "0x000000000000000000000000000000000000beef",
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: pm2}); err != nil {
		t.Fatal(err)
	}
}

// A passed verification with no stored legal name must be refused at the gate.
//
// This is the gap the status-only check left open. Such a user is `verified`,
// so a status check waves them through; they pay; the matcher then cannot
// identify them, holds their credit, and — because an unidentifiable rival
// claim can never be ruled out — holds every other credit at that amount too.
// The gate exists to stop exactly that, so it has to refuse here.
func TestRequireVerifiedForMoneyIn_RefusesVerifiedWithNoLegalName(t *testing.T) {
	s := newITTestService(t)

	for _, stored := range []string{"", "   ", "\t"} {
		userID := seedVerifiedWithoutLegalName(t, s.db, stored)
		err := requireVerifiedForMoneyIn(context.Background(), s.db, userID)
		if !errors.Is(err, ErrKYCVerificationRequired) {
			t.Errorf("stored legal name %q: err = %v, want ErrKYCVerificationRequired — "+
				"a verified user the matcher cannot identify must not be able to pay in", stored, err)
		}
	}
}

func TestRequireVerifiedForMoneyIn_AdmitsVerifiedWithALegalName(t *testing.T) {
	s := newITTestService(t)
	userID := seedVerifiedUser(t, s.db)

	if err := requireVerifiedForMoneyIn(context.Background(), s.db, userID); err != nil {
		t.Fatalf("err = %v, want nil — a fully verified user must still be admitted", err)
	}
}

// The gate must fail CLOSED on a database error, and must not report that
// failure as the ordinary refusal.
//
// Distinguishing the two matters: the refusal is a 403 telling the user to go
// and verify, while this is a 500 that should page somebody. A refactor that
// swallowed the query error would turn an outage into "every user suddenly
// needs to verify again", and the whole suite would still pass.
func TestRequireVerifiedForMoneyIn_DatabaseErrorIsNotARefusal(t *testing.T) {
	s := newITTestService(t)
	userID := seedVerifiedUser(t, s.db)

	if err := s.db.Exec(`DROP TABLE users`).Error; err != nil {
		t.Fatalf("drop users: %v", err)
	}

	err := requireVerifiedForMoneyIn(context.Background(), s.db, userID)
	if err == nil {
		t.Fatal("err = nil — an unreadable users table must never read as verified")
	}
	if errors.Is(err, ErrKYCVerificationRequired) {
		t.Fatal("a database failure was reported as the ordinary refusal; " +
			"the user would be told to re-verify instead of the outage being surfaced")
	}
}

// A verified user with no stored legal name cannot fix that themselves: the
// KYC screen already reads "verified" and a new Didit session is refused as
// "Already verified". Telling them to "finish verification" is a dead end, so
// the refusal must be distinguishable — while still being a money-in refusal
// on every gated path.
func TestRequireVerifiedForMoneyIn_NamelessVerifiedIsASupportRefusal(t *testing.T) {
	s := newITTestService(t)
	userID := seedVerifiedWithoutLegalName(t, s.db, "")

	err := requireVerifiedForMoneyIn(context.Background(), s.db, userID)
	if !errors.Is(err, ErrKYCLegalNameMissing) {
		t.Fatalf("err = %v, want ErrKYCLegalNameMissing", err)
	}
	if !errors.Is(err, ErrKYCVerificationRequired) {
		t.Fatal("ErrKYCLegalNameMissing must still read as a money-in refusal, or a caller checking only the general sentinel lets the user through")
	}

	pending := seedUserAt(t, s.db, uuid.New(), models.VerificationStatusPending)
	if err := requireVerifiedForMoneyIn(context.Background(), s.db, pending); errors.Is(err, ErrKYCLegalNameMissing) {
		t.Fatal("an unverified user was given the support refusal; they can verify themselves and must be told to")
	}
}

// ─── Money in #4: minting a virtual account (VA mode) ───────────────────────

// A virtual account is a standing pay-in account: AutoCreditFromVA credits
// whatever lands on it, with or without an open order. Activating one is
// therefore a money-in entry point, and the gate is unconditional on inbound
// mode, so an unverified user must not be handed one.
func TestActivateFiatVA_RefusesUnverifiedUser(t *testing.T) {
	s := newITTestService(t)
	s.SetInboundCentral(false, fullCentralAccount())
	// Provisioning wired, so the refusal is proven to be the gate and not the
	// not-configured early return. The gate runs before any provider call.
	s.SetFlutterwaveClient(NewFlutterwaveClient("", "", "test-id", "test-secret"))

	ids := []uuid.UUID{uuid.New()} // no user row at all
	for _, status := range unverifiedStatuses {
		ids = append(ids, seedUserAt(t, s.db, uuid.New(), status))
	}
	ids = append(ids, seedVerifiedWithoutLegalName(t, s.db, ""))

	for _, id := range ids {
		user := &models.User{ID: id, Name: "JOHN DOE", WalletAddress: "0xWALLET"}
		_, err := s.ActivateFiatVA(context.Background(), user, "BVN", "12345678901")
		if !errors.Is(err, ErrKYCVerificationRequired) {
			t.Fatalf("user %s: ActivateFiatVA err = %v, want ErrKYCVerificationRequired", id, err)
		}
		var n int64
		s.db.Model(&models.FiatVirtualAccount{}).Where("user_id = ?", id).Count(&n)
		if n != 0 {
			t.Fatalf("user %s: %d virtual account(s) written despite the refusal", id, n)
		}
	}
}

// Under central inbound the retirement answer must still win: that user has
// no VA to activate whatever their status, and 410 is the honest reply.
func TestActivateFiatVA_CentralRetirementOutranksTheGate(t *testing.T) {
	s := newITTestService(t)
	s.SetInboundCentral(true, fullCentralAccount())
	id := seedUserAt(t, s.db, uuid.New(), models.VerificationStatusPending)
	user := &models.User{ID: id, Name: "JOHN DOE", WalletAddress: "0xWALLET"}

	if _, err := s.ActivateFiatVA(context.Background(), user, "BVN", "12345678901"); !errors.Is(err, ErrITVARetired) {
		t.Fatalf("ActivateFiatVA err = %v, want ErrITVARetired", err)
	}
}

// ─── Money in #5: the Paycrest onramp ───────────────────────────────────────

func paycrestGateBuyRequest(userID uuid.UUID) PaycrestBuyRequest {
	return PaycrestBuyRequest{
		UserID:            userID,
		WalletAddress:     "0xabc",
		FiatCode:          "NGN",
		FiatAmount:        "800",
		ChainKey:          "base",
		Token:             "USDC",
		RefundBankCode:    "044",
		RefundAccount:     "0123456789",
		RefundAccountName: "Test User",
	}
}

// A Paycrest buy hands the user a provider bank account to pay, so it is money
// in. The refusal must land before any provider call: this fixture's quoter and
// orderer panic when reached, so a gate placed too late fails the test instead
// of passing it.
func TestPaycrestCreateBuy_RefusesUnverifiedBuyer(t *testing.T) {
	svc, registry := newPaycrestOrderTestService(t)
	svc.SetCurrencyRegistry(registry)
	ctx := context.Background()

	ids := []uuid.UUID{uuid.New()} // no user row at all
	for _, status := range unverifiedStatuses {
		ids = append(ids, seedUserAt(t, svc.db, uuid.New(), status))
	}

	for _, id := range ids {
		_, err := svc.CreateBuy(ctx, paycrestGateBuyRequest(id))
		if !errors.Is(err, ErrKYCVerificationRequired) {
			t.Fatalf("user %s: CreateBuy err = %v, want ErrKYCVerificationRequired", id, err)
		}
		if errors.Is(err, ErrKYCLegalNameMissing) {
			t.Fatalf("user %s: an unverified buyer was given the support refusal; they can verify themselves", id)
		}
		var n int64
		svc.db.Model(&models.PaycrestOrderRecord{}).Where("user_id = ?", id).Count(&n)
		if n != 0 {
			t.Fatalf("user %s: %d order(s) written despite the refusal", id, n)
		}
	}
}

func TestPaycrestCreateBuy_NamelessVerifiedIsASupportRefusal(t *testing.T) {
	svc, registry := newPaycrestOrderTestService(t)
	svc.SetCurrencyRegistry(registry)
	id := seedVerifiedWithoutLegalName(t, svc.db, "")

	_, err := svc.CreateBuy(context.Background(), paycrestGateBuyRequest(id))
	if !errors.Is(err, ErrKYCLegalNameMissing) {
		t.Fatalf("CreateBuy err = %v, want ErrKYCLegalNameMissing", err)
	}
}

func TestPaycrestCreateBuy_AdmitsVerifiedBuyer(t *testing.T) {
	pc := &capturingOrderer{rate: "1390.64", feePercent: "0.5"}
	svc := newFeeTestService(t, "0.5", pc, "buy", "1390.64")
	id := seedVerifiedUser(t, svc.db)

	row, err := svc.CreateBuy(context.Background(), paycrestGateBuyRequest(id))
	if err != nil {
		t.Fatalf("verified buyer refused: %v", err)
	}
	if row == nil || pc.onramp.Reference == "" {
		t.Fatal("verified buyer's order never reached the provider")
	}
}

// Selling to Paycrest is an exit: the user sends stablecoin they already hold
// and receives fiat. Refusing it would trap the balance, so an unverified
// seller — or one with no user row at all — must still get an order.
func TestPaycrestCreateSell_IsNotGatedOnVerification(t *testing.T) {
	pc := &capturingOrderer{rate: "1360.05", feePercent: "0.5"}
	svc := newFeeTestService(t, "0.5", pc, "sell", "1360.05")

	sellers := []uuid.UUID{uuid.New()}
	for _, status := range unverifiedStatuses {
		sellers = append(sellers, seedUserAt(t, svc.db, uuid.New(), status))
	}
	sellers = append(sellers, seedVerifiedWithoutLegalName(t, svc.db, ""))

	for _, id := range sellers {
		req := sellRequest()
		req.UserID = id
		if _, err := svc.CreateSell(context.Background(), req); err != nil {
			t.Fatalf("seller %s: CreateSell err = %v — money-out must not be gated", id, err)
		}
	}
}
