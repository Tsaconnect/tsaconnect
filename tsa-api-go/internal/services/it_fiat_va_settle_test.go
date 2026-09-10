package services

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Settlement tests for the Spec 1 standing-VA buy path.
//
// The test harness has no EVM client, so the on-chain credit inside
// ApproveBuyCredit always fails and the intent lands in `failed`. That is the
// seam these tests use: everything up to the broadcast — attribution, pricing,
// idempotency, and the park/recover decisions — is observable in the DB, which
// is exactly the logic that decides whether a user's money is accounted for.

const testChargeID = "chg_test_0001"

// newVATestService returns a service whose DB also has users (settlement loads
// the payer) plus a seeded rate, an active VA, and the owning user.
func newVATestService(t *testing.T) (*ITService, *models.User, *models.FiatVirtualAccount) {
	t.Helper()
	s := newITTestService(t)
	// Raw DDL, not AutoMigrate — models.User carries PostgreSQL-specific
	// defaults SQLite rejects (repo convention, see tsa-api-go/CLAUDE.md). Only
	// the columns settlement reads are needed.
	if err := s.db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		name TEXT,
		email TEXT,
		phone_number TEXT,
		wallet_address TEXT,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME
		)`).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	if _, err := s.SetRate(context.Background(), uuid.New(), 20000, 0); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	user := &models.User{
		ID:            uuid.New(),
		Email:         "ada@example.com",
		Name:          "Ada Lovelace",
		WalletAddress: "0x000000000000000000000000000000000000dEaD",
	}
	// Raw insert to match the raw table above (GORM would write every column of
	// models.User).
	if err := s.db.Exec(
		`INSERT INTO users (id, name, email, phone_number, wallet_address) VALUES (?, ?, ?, ?, ?)`,
		user.ID, user.Name, user.Email, "08031234567", user.WalletAddress,
	).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	va := &models.FiatVirtualAccount{
		UserID: user.ID, Currency: "NGN", Provider: models.FiatVAProviderFlutterwave,
		FwCustomerID: "cus_ada", Reference: "resp-ref-ada",
		AccountNumber: "1234567890", BankName: "Test Bank", AccountName: "Ada Lovelace",
		Status: models.FiatVAStatusActive,
	}
	if err := s.db.Create(va).Error; err != nil {
		t.Fatalf("create va: %v", err)
	}
	return s, user, va
}

func buyIntents(t *testing.T, s *ITService, userID uuid.UUID) []models.ITIntent {
	t.Helper()
	var rows []models.ITIntent
	if err := s.db.Where("user_id = ? AND direction = ?", userID, models.ITDirectionBuy).
		Order("created_at ASC").Find(&rows).Error; err != nil {
		t.Fatalf("load intents: %v", err)
	}
	return rows
}

// A charge landing with no prior order creates exactly one intent, attributed
// to the charge and priced on the amount ACTUALLY transferred.
func TestAutoCreditFromVA_AttributesChargeAndPricesPaidAmount(t *testing.T) {
	s, user, va := newVATestService(t)

	// Credit broadcast fails (no chain in tests); attribution still must land.
	_ = s.AutoCreditFromVA(context.Background(), va.FwCustomerID, va.Reference, testChargeID, "5000")

	rows := buyIntents(t, s, user.ID)
	if len(rows) != 1 {
		t.Fatalf("want 1 intent, got %d", len(rows))
	}
	if rows[0].PaymentRef != testChargeID {
		t.Errorf("payment_ref = %q, want the charge id %q", rows[0].PaymentRef, testChargeID)
	}
	if rows[0].NgnKobo != "500000" { // ₦5000 → kobo
		t.Errorf("ngnKobo = %q, want 500000 (the transferred amount)", rows[0].NgnKobo)
	}
	if rows[0].UserID != user.ID {
		t.Errorf("credited the wrong user: %s", rows[0].UserID)
	}
}

// Webhook redelivery must not produce a second intent for one charge.
func TestAutoCreditFromVA_RedeliveryCreditsOnce(t *testing.T) {
	s, user, va := newVATestService(t)
	ctx := context.Background()

	_ = s.AutoCreditFromVA(ctx, va.FwCustomerID, va.Reference, testChargeID, "5000")
	_ = s.AutoCreditFromVA(ctx, va.FwCustomerID, va.Reference, testChargeID, "5000")

	if rows := buyIntents(t, s, user.ID); len(rows) != 1 {
		t.Fatalf("redelivery created %d intents, want 1", len(rows))
	}
}

// REGRESSION: a charge whose intent is mid-broadcast must NOT report success.
// Returning nil marks the webhook processed and the money is never credited.
func TestAutoCreditFromVA_ProcessingIntentIsNotSilentlyDropped(t *testing.T) {
	s, user, va := newVATestService(t)

	stuck := &models.ITIntent{
		ID: uuid.New(), UserID: user.ID, WalletAddress: user.WalletAddress,
		Direction: models.ITDirectionBuy, Status: models.ITStatusProcessing,
		McgpAmount: "1", NgnKobo: "500000", RateKobo: "1", PaymentRef: testChargeID,
	}
	if err := s.db.Create(stuck).Error; err != nil {
		t.Fatal(err)
	}

	err := s.AutoCreditFromVA(context.Background(), va.FwCustomerID, va.Reference, testChargeID, "5000")
	if err == nil {
		t.Fatal("returned nil for an uncredited charge — the webhook would be marked processed and the payment dropped")
	}
}

// REGRESSION: if the order was superseded while the payment was landing, the
// charge must be re-settled against a fresh intent, not silently dropped.
func TestAutoCreditFromVA_SupersededOrderIsReSettled(t *testing.T) {
	s, user, va := newVATestService(t)

	dead := &models.ITIntent{
		ID: uuid.New(), UserID: user.ID, WalletAddress: user.WalletAddress,
		Direction: models.ITDirectionBuy, Status: models.ITStatusCancelled,
		McgpAmount: "1", NgnKobo: "500000", RateKobo: "1", PaymentRef: testChargeID,
	}
	if err := s.db.Create(dead).Error; err != nil {
		t.Fatal(err)
	}

	_ = s.AutoCreditFromVA(context.Background(), va.FwCustomerID, va.Reference, testChargeID, "5000")

	var reloaded models.ITIntent
	if err := s.db.First(&reloaded, "id = ?", dead.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.PaymentRef != "" {
		t.Errorf("cancelled intent still owns the charge (payment_ref=%q)", reloaded.PaymentRef)
	}
	rows := buyIntents(t, s, user.ID)
	var carrier int
	for _, r := range rows {
		if r.PaymentRef == testChargeID {
			carrier++
		}
	}
	if carrier != 1 {
		t.Fatalf("want exactly 1 live intent carrying the charge, got %d", carrier)
	}
}

// An open buy order absorbs the payment rather than orphaning it beside a new
// intent, and is repriced to what was actually transferred.
func TestAutoCreditFromVA_SettlesIntoOpenBuyOrder(t *testing.T) {
	s, user, va := newVATestService(t)
	ctx := context.Background()

	opened, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000") // ₦1000 quoted
	if err != nil {
		t.Fatalf("open buy order: %v", err)
	}

	_ = s.AutoCreditFromVA(ctx, va.FwCustomerID, va.Reference, testChargeID, "5000") // ₦5000 sent

	rows := buyIntents(t, s, user.ID)
	if len(rows) != 1 {
		t.Fatalf("want the open order reused, got %d intents", len(rows))
	}
	if rows[0].ID != opened.ID {
		t.Errorf("settled into a new intent instead of the open order")
	}
	if rows[0].NgnKobo != "500000" {
		t.Errorf("ngnKobo = %q, want it repriced to the transferred 500000", rows[0].NgnKobo)
	}
}

// Money landed while FX was stale: park it, never credit at a stale rate.
func TestAutoCreditFromVA_StaleFxParksAwaitingRate(t *testing.T) {
	s, user, va := newVATestService(t)
	s.fx = stubFx{ngn: 1500, stale: true}

	if err := s.AutoCreditFromVA(context.Background(), va.FwCustomerID, va.Reference, testChargeID, "5000"); err != nil {
		t.Fatalf("parking should not error: %v", err)
	}

	rows := buyIntents(t, s, user.ID)
	if len(rows) != 1 {
		t.Fatalf("want 1 parked intent, got %d", len(rows))
	}
	if rows[0].Status != models.ITStatusAwaitingRate {
		t.Errorf("status = %q, want awaiting_rate", rows[0].Status)
	}
	if rows[0].NgnKobo != "500000" {
		t.Errorf("parked intent lost the amount: ngnKobo = %q", rows[0].NgnKobo)
	}
}

// The operator recovery for a parked credit: reprice at the live rate, then
// credit. Refuses while the feed is still stale.
func TestRepriceAndCreditAwaitingRate(t *testing.T) {
	s, user, va := newVATestService(t)
	ctx := context.Background()

	s.fx = stubFx{ngn: 1500, stale: true}
	if err := s.AutoCreditFromVA(ctx, va.FwCustomerID, va.Reference, testChargeID, "5000"); err != nil {
		t.Fatalf("park: %v", err)
	}
	parked := buyIntents(t, s, user.ID)[0]

	if _, err := s.RepriceAndCreditAwaitingRate(ctx, parked.ID); err == nil {
		t.Fatal("repriced while FX was still stale — that is what the park exists to prevent")
	}

	s.fx = stubFx{ngn: 1500}
	// Credit broadcast fails without a chain; the repricing before it is the
	// part under test.
	_, _ = s.RepriceAndCreditAwaitingRate(ctx, parked.ID)

	var after models.ITIntent
	if err := s.db.First(&after, "id = ?", parked.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status == models.ITStatusAwaitingRate {
		t.Error("intent still parked after a successful reprice")
	}
	if after.RateKobo == "0" || after.RateKobo == "" {
		t.Errorf("rateKobo = %q, want a live rate", after.RateKobo)
	}
	if after.McgpAmount == "0" || after.McgpAmount == "" {
		t.Errorf("mcgpAmount = %q, want a repriced amount", after.McgpAmount)
	}
}

// A charge that resolves to no known VA must stay retryable for the operator,
// never be acknowledged as settled.
func TestAutoCreditFromVA_UnknownAccountIsNotAcknowledged(t *testing.T) {
	s, _, _ := newVATestService(t)
	err := s.AutoCreditFromVA(context.Background(), "cus_nobody", "ref-nobody", "chg_orphan", "5000")
	if err == nil {
		t.Fatal("unattributable charge returned nil — it would be marked processed and lost")
	}
}

// The on-chain single-use ref for VA settlement keys on the CHARGE, so two
// intent rows for one charge collide on-chain instead of double-crediting.
func TestVACreditRefKeysOnChargeNotIntent(t *testing.T) {
	a := itRefKey(itVARefDomain, testChargeID)
	b := itRefKey(itVARefDomain, testChargeID)
	if a != b {
		t.Fatal("charge-derived ref is not stable")
	}
	if other := itRefKey(itVARefDomain, "chg_other"); a == other {
		t.Fatal("distinct charges produced the same ref")
	}
	if intentKeyed := itRef("IT:credit:", uuid.New()); a == intentKeyed {
		t.Fatal("charge ref collided with an intent-keyed ref")
	}
}

// An order a payment is already landing against must not be superseded.
func TestOpenBuyOrder_DoesNotSupersedeOrderWithLandedPayment(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()

	settling, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatal(err)
	}
	// Settlement has attributed a real charge to it.
	if err := s.db.Model(settling).Update("payment_ref", testChargeID).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000"); err != nil {
		t.Fatal(err)
	}

	var reloaded models.ITIntent
	if err := s.db.First(&reloaded, "id = ?", settling.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status == models.ITStatusCancelled {
		t.Error("cancelled an order that a payment had already been attributed to")
	}
}

// The service re-validates the identity number itself — 11 characters is not
// 11 digits.
func TestActivateFiatVA_RejectsNonDigitIDNumber(t *testing.T) {
	s, user, _ := newVATestService(t)
	// Provisioning is unconfigured in tests, so assert the ID rule specifically.
	_, err := s.ActivateFiatVA(context.Background(), user, "BVN", "1234567890a")
	if err == nil {
		t.Fatal("accepted a non-numeric id number")
	}
}
