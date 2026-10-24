package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

func seedEnrollUser(t *testing.T, db *gorm.DB, role string) uuid.UUID {
	t.Helper()
	for _, col := range []string{"name TEXT", "role TEXT"} {
		if err := db.Exec("ALTER TABLE users ADD COLUMN " + col).Error; err != nil {
			t.Fatalf("alter users: %v", err)
		}
	}
	id := uuid.New()
	if err := db.Exec(`INSERT INTO users (id, name, role, verification_status, verified_legal_name)
		VALUES (?, 'Ada Obi', ?, 'verified', 'ADA CHIOMA OBI')`, id, role).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

// seedPayoutAccount saves the merchant's payout account (the one destination).
func seedPayoutAccount(t *testing.T, db *gorm.DB, userID uuid.UUID, kind, currency string, verified bool) {
	t.Helper()
	createPayoutTable(t, db)
	if err := db.Exec(`INSERT INTO seller_payout_methods
		(id, user_id, kind, currency, institution_code, institution_name, account_number, account_name, verified)
		VALUES (?, ?, ?, ?, '070', 'Fidelity Bank', '6554258761', 'ADA CHIOMA OBI', ?)`,
		uuid.New(), userID, kind, currency, verified).Error; err != nil {
		t.Fatalf("seed payout account: %v", err)
	}
}

func enrollInput(userID uuid.UUID) PMEnrollInput {
	return PMEnrollInput{UserID: userID, MakerWalletAddress: "0x00000000000000000000000000000000000000aa"}
}

func TestEnrollApprovedMerchant_SettlesIntoThePayoutAccount(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)
	userID := seedEnrollUser(t, db, models.RoleMerchant)
	seedPayoutAccount(t, db, userID, PaycrestInstitutionBank, "NGN", true)
	if err := db.Exec(`INSERT INTO merchant_requests (id, user_id, business_type, business_name, status)
		VALUES (?, ?, 'general_products', 'Ada Stores', 'approved')`, uuid.New(), userID).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}

	pm, err := svc.EnrollApprovedMerchant(context.Background(), enrollInput(userID))
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if pm.Status != models.PaymentMerchantStatusApproved {
		t.Errorf("status = %q, want approved", pm.Status)
	}
	if pm.Name != "Ada Stores - Ada Chioma Obi" {
		t.Errorf("name = %q, want the approved business name with the owner's legal name", pm.Name)
	}
	if pm.PerTradeCapUSD != 0 || pm.DailyCapUSD != 0 {
		t.Errorf("caps = %v/%v, want 0/0 (no cap)", pm.PerTradeCapUSD, pm.DailyCapUSD)
	}
	if pm.SettlementBankCode != "070" || pm.SettlementAccountNumber != "6554258761" || pm.SettlementAccountName != "ADA CHIOMA OBI" {
		t.Errorf("settlement = %q %q %q, want the payout account", pm.SettlementBankCode, pm.SettlementAccountNumber, pm.SettlementAccountName)
	}
	if pm.OperatorWalletAddress == "" {
		t.Error("the maker wallet was not stored")
	}
	var note string
	db.Raw(`SELECT review_note FROM payment_merchants WHERE id = ?`, pm.ID).Scan(&note)
	if note == "" {
		t.Error("review_note should record that this was self-enrolled")
	}
}

func TestEnrollApprovedMerchant_NeedsAVerifiedNairaBankPayoutAccount(t *testing.T) {
	cases := []struct {
		name     string
		seed     func(t *testing.T, db *gorm.DB, userID uuid.UUID)
		wantRows int64
	}{
		{"no payout account", func(*testing.T, *gorm.DB, uuid.UUID) {}, 0},
		{"mobile money", func(t *testing.T, db *gorm.DB, u uuid.UUID) { seedPayoutAccount(t, db, u, "mobile_money", "NGN", true) }, 0},
		{"other currency", func(t *testing.T, db *gorm.DB, u uuid.UUID) {
			seedPayoutAccount(t, db, u, PaycrestInstitutionBank, "GHS", true)
		}, 0},
		{"unverified account", func(t *testing.T, db *gorm.DB, u uuid.UUID) {
			seedPayoutAccount(t, db, u, PaycrestInstitutionBank, "NGN", false)
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newSQLiteForOnboarding(t)
			svc := NewPMOnboardingService(db, nil)
			userID := seedEnrollUser(t, db, models.RoleMerchant)
			createPayoutTable(t, db)
			tc.seed(t, db, userID)
			if _, err := svc.EnrollApprovedMerchant(context.Background(), enrollInput(userID)); !errors.Is(err, ErrPayoutAccountRequired) {
				t.Fatalf("err = %v, want ErrPayoutAccountRequired", err)
			}
			var n int64
			db.Raw(`SELECT COUNT(*) FROM payment_merchants`).Scan(&n)
			if n != tc.wantRows {
				t.Errorf("a refused enrollment must leave no row, found %d", n)
			}
		})
	}
}

func TestEnrollApprovedMerchant_FallsBackToAccountName(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)
	userID := seedEnrollUser(t, db, models.RoleMerchant)
	seedPayoutAccount(t, db, userID, PaycrestInstitutionBank, "NGN", true)
	pm, err := svc.EnrollApprovedMerchant(context.Background(), enrollInput(userID))
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if pm.Name != "Ada Obi - Ada Chioma Obi" {
		t.Errorf("name = %q, want the account name with the owner's legal name", pm.Name)
	}
}

func TestEnrollApprovedMerchant_RefusesNonMerchant(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)
	userID := seedEnrollUser(t, db, models.RoleUser)
	seedPayoutAccount(t, db, userID, PaycrestInstitutionBank, "NGN", true)
	if _, err := svc.EnrollApprovedMerchant(context.Background(), enrollInput(userID)); !errors.Is(err, ErrNotAMerchant) {
		t.Fatalf("err = %v, want ErrNotAMerchant", err)
	}
	if _, err := svc.EnrollApprovedMerchant(context.Background(), enrollInput(uuid.New())); !errors.Is(err, ErrNotAMerchant) {
		t.Fatalf("unknown user: err = %v, want ErrNotAMerchant", err)
	}
}

func TestEnrollApprovedMerchant_IdempotentAndRespectsAdminDecision(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)
	userID := seedEnrollUser(t, db, models.RoleMerchant)
	seedPayoutAccount(t, db, userID, PaycrestInstitutionBank, "NGN", true)
	first, err := svc.EnrollApprovedMerchant(context.Background(), enrollInput(userID))
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	again, err := svc.EnrollApprovedMerchant(context.Background(), enrollInput(userID))
	if err != nil || again.ID != first.ID {
		t.Fatalf("second enroll = %v, %v; want the same row", again, err)
	}

	// An admin's suspension (or rejection) is never replaced by self-service.
	if err := db.Exec(`UPDATE payment_merchants SET status = 'paused' WHERE id = ?`, first.ID).Error; err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := svc.EnrollApprovedMerchant(context.Background(), enrollInput(userID)); !errors.Is(err, ErrPMEnrollDecided) {
		t.Fatalf("suspended merchant: err = %v, want ErrPMEnrollDecided", err)
	}
}

// Changing the payout account changes where P2P settles: the payment merchant
// follows the saved account, and a non-bank change leaves it alone.
func TestSyncPMSettlement_FollowsTheSavedPayoutAccount(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	owner, stranger := uuid.New(), uuid.New()
	exec := func(q string, args ...any) {
		t.Helper()
		if err := db.Exec(q, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO payment_merchants (id, user_id, name, kind, settlement_bank_code, settlement_account_number, settlement_account_name)
		VALUES (?, ?, 'Ada Stores', 'merchant', '058', '0000000001', 'OLD NAME')`, uuid.New(), owner)
	exec(`INSERT INTO payment_merchants (id, user_id, name, kind, settlement_bank_code, settlement_account_number, settlement_account_name)
		VALUES (?, ?, 'Ada LP', 'lp', '058', '0000000001', 'OLD NAME')`, uuid.New(), owner)
	exec(`INSERT INTO payment_merchants (id, user_id, name, kind, settlement_bank_code, settlement_account_number, settlement_account_name)
		VALUES (?, ?, 'Other', 'merchant', '058', '0000000001', 'OLD NAME')`, uuid.New(), stranger)

	bank := &models.SellerPayoutMethod{UserID: owner, Kind: PaycrestInstitutionBank, Currency: "NGN", Verified: true,
		InstitutionCode: "070", AccountNumber: "6554258761", AccountName: "ADA CHIOMA OBI"}
	if err := syncPMSettlement(db, bank); err != nil {
		t.Fatal(err)
	}
	read := func(where string, args ...any) string {
		var out string
		db.Raw(`SELECT settlement_bank_code || ':' || settlement_account_number FROM payment_merchants WHERE `+where, args...).Scan(&out)
		return out
	}
	if got := read(`user_id = ? AND kind = 'merchant'`, owner); got != "070:6554258761" {
		t.Errorf("merchant settlement = %q, want the payout account", got)
	}
	if got := read(`user_id = ? AND kind = 'lp'`, owner); got != "058:0000000001" {
		t.Errorf("an LP row must keep its own: %q", got)
	}
	if got := read(`user_id = ?`, stranger); got != "058:0000000001" {
		t.Errorf("another user's merchant changed: %q", got)
	}

	// Mobile money, another currency, or an unverified account is not a P2P settlement.
	for _, other := range []*models.SellerPayoutMethod{
		{UserID: owner, Kind: "mobile_money", Currency: "NGN", Verified: true, InstitutionCode: "999", AccountNumber: "1"},
		{UserID: owner, Kind: PaycrestInstitutionBank, Currency: "GHS", Verified: true, InstitutionCode: "999", AccountNumber: "1"},
		{UserID: owner, Kind: PaycrestInstitutionBank, Currency: "NGN", Verified: false, InstitutionCode: "999", AccountNumber: "1"},
	} {
		if err := syncPMSettlement(db, other); err != nil {
			t.Fatal(err)
		}
	}
	if got := read(`user_id = ? AND kind = 'merchant'`, owner); got != "070:6554258761" {
		t.Errorf("a non-bank payout account moved P2P settlement: %q", got)
	}
}

// An admin's decision on a P2P application stands: self-service cannot step around
// a rejection (which leaves no payment-merchant row) or a review still waiting.
func TestEnrollApprovedMerchant_RespectsTheP2PApplicationDecision(t *testing.T) {
	cases := []struct {
		name     string
		requests [][2]string // status, created_at
		wantErr  error
	}{
		{"rejected", [][2]string{{"rejected", "2026-10-01 10:00:00"}}, ErrPMEnrollDecided},
		{"still pending", [][2]string{{"pending", "2026-10-01 10:00:00"}}, ErrPMEnrollDecided},
		{"approved", [][2]string{{"approved", "2026-10-01 10:00:00"}}, nil},
		{"approved after an earlier rejection", [][2]string{{"rejected", "2026-09-01 10:00:00"}, {"approved", "2026-10-01 10:00:00"}}, nil},
		{"rejected after an earlier approval", [][2]string{{"approved", "2026-09-01 10:00:00"}, {"rejected", "2026-10-01 10:00:00"}}, ErrPMEnrollDecided},
		{"no P2P application at all", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newSQLiteForOnboarding(t)
			svc := NewPMOnboardingService(db, nil)
			userID := seedEnrollUser(t, db, models.RoleMerchant)
			seedPayoutAccount(t, db, userID, PaycrestInstitutionBank, "NGN", true)
			for _, r := range tc.requests {
				if err := db.Exec(`INSERT INTO merchant_requests (id, user_id, business_type, business_name, status, created_at)
					VALUES (?, ?, 'p2p_merchant', 'Ada Stores', ?, ?)`, uuid.New(), userID, r[0], r[1]).Error; err != nil {
					t.Fatalf("seed request: %v", err)
				}
			}
			_, err := svc.EnrollApprovedMerchant(context.Background(), enrollInput(userID))
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
