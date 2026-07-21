package services

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// addRewardClaimTables creates reward_claims + transactions on the in-memory
// SQLite DB from setupReferralGraphTestDB. Raw DDL for the usual reason: the
// Postgres gen_random_uuid() default breaks AutoMigrate on SQLite.
func addRewardClaimTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	stmts := []string{
		`CREATE TABLE reward_claims (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			name TEXT NOT NULL,
			username TEXT NOT NULL,
			phone_number TEXT,
			phone_network TEXT,
			inviter_username TEXT,
			social_links TEXT,
			payout_method TEXT,
			payout_details TEXT,
			invited_usernames TEXT,
			reviewed_by TEXT,
			review_notes TEXT,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE UNIQUE INDEX idx_reward_claim_user_kind ON reward_claims (user_id, kind)`,
		`CREATE TABLE transactions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			type TEXT,
			status TEXT,
			amount REAL,
			created_at DATETIME,
			updated_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
}

func newClaimTestService(t *testing.T) (*RewardClaimService, *gorm.DB) {
	t.Helper()
	db := setupReferralGraphTestDB(t)
	addRewardFlagsTable(t, db)
	addRewardClaimTables(t, db)
	graph := NewReferralGraphService(db)
	elig := NewRewardEligibilityService(db, graph)
	return NewRewardClaimService(db, elig, graph), db
}

func mkCompletedTx(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO transactions (id, user_id, type, status, amount) VALUES (?, ?, 'deposit', ?, 1)`,
		uuid.New().String(), userID.String(), models.TransactionStatusCompleted,
	).Error; err != nil {
		t.Fatalf("insert tx: %v", err)
	}
}

// mkVerifiedReferrals creates n KYC-verified active users referred by referrer.
func mkVerifiedReferrals(t *testing.T, db *gorm.DB, referrer uuid.UUID, n int, prefix string) {
	t.Helper()
	for i := 0; i < n; i++ {
		mkVerifiedUser(t, db, prefix+string(rune('a'+i)), &referrer)
	}
}

func TestClaimEligibility_SignupRequiresKycOnly(t *testing.T) {
	svc, db := newClaimTestService(t)
	// Unverified user: signup form must be blocked on KYC.
	pending := mkUser(t, db, "pending1", nil, models.AccountStatusActive)
	got, err := svc.Eligibility(context.Background(), pending.ID, models.RewardClaimKindSignup)
	if err != nil {
		t.Fatalf("eligibility: %v", err)
	}
	if got.Eligible {
		t.Fatalf("unverified user should not be eligible; reasons=%v", got.Reasons)
	}
	if !hasReason(got.Reasons, "KYC") {
		t.Fatalf("expected a KYC reason, got %v", got.Reasons)
	}

	// Verified user with no referrals and no transactions still qualifies for
	// signup — that form is open to everyone who's KYC'd.
	verified := mkVerifiedUser(t, db, "verified1", nil)
	got, err = svc.Eligibility(context.Background(), verified.ID, models.RewardClaimKindSignup)
	if err != nil {
		t.Fatalf("eligibility: %v", err)
	}
	if !got.Eligible {
		t.Fatalf("verified user should be signup-eligible; reasons=%v", got.Reasons)
	}
}

func TestClaimEligibility_OnboardingNeedsReferralsAndTransaction(t *testing.T) {
	svc, db := newClaimTestService(t)
	user := mkVerifiedUser(t, db, "onboarder", nil)

	// No referrals, no transaction → two distinct blockers.
	got, err := svc.Eligibility(context.Background(), user.ID, models.RewardClaimKindOnboarding)
	if err != nil {
		t.Fatalf("eligibility: %v", err)
	}
	if got.Eligible {
		t.Fatal("should not be eligible with 0 referrals and no transaction")
	}
	if !hasReason(got.Reasons, "Onboard at least") {
		t.Fatalf("expected a referral-count reason, got %v", got.Reasons)
	}
	if !hasReason(got.Reasons, "transaction") {
		t.Fatalf("expected a transaction reason, got %v", got.Reasons)
	}

	// Exactly the minimum verified referrals, still no transaction.
	mkVerifiedReferrals(t, db, user.ID, models.MinOnboardingReferrals, "ref")
	got, _ = svc.Eligibility(context.Background(), user.ID, models.RewardClaimKindOnboarding)
	if got.VerifiedReferrals != models.MinOnboardingReferrals {
		t.Fatalf("verifiedReferrals = %d, want %d", got.VerifiedReferrals, models.MinOnboardingReferrals)
	}
	if got.Eligible {
		t.Fatal("still blocked: no transaction")
	}
	if hasReason(got.Reasons, "Onboard at least") {
		t.Fatalf("referral gate should pass at exactly the minimum; got %v", got.Reasons)
	}

	// Add a completed transaction → eligible.
	mkCompletedTx(t, db, user.ID)
	got, _ = svc.Eligibility(context.Background(), user.ID, models.RewardClaimKindOnboarding)
	if !got.Eligible {
		t.Fatalf("should be eligible now; reasons=%v", got.Reasons)
	}
	if !got.HasTransaction {
		t.Fatal("HasTransaction should be true")
	}
}

func TestClaimEligibility_UnverifiedReferralsDoNotCount(t *testing.T) {
	svc, db := newClaimTestService(t)
	user := mkVerifiedUser(t, db, "counter", nil)
	mkCompletedTx(t, db, user.ID)
	// Enough referrals in number, but none KYC-verified.
	for i := 0; i < models.MinOnboardingReferrals; i++ {
		mkUser(t, db, "unver"+string(rune('a'+i)), &user.ID, models.AccountStatusActive)
	}
	got, err := svc.Eligibility(context.Background(), user.ID, models.RewardClaimKindOnboarding)
	if err != nil {
		t.Fatalf("eligibility: %v", err)
	}
	if got.VerifiedReferrals != 0 {
		t.Fatalf("verifiedReferrals = %d, want 0 (none verified)", got.VerifiedReferrals)
	}
	if got.Eligible {
		t.Fatal("unverified referrals must not satisfy the gate")
	}
}

func TestClaimEligibility_AlreadySubmittedBlocksSameKindOnly(t *testing.T) {
	svc, db := newClaimTestService(t)
	user := mkVerifiedUser(t, db, "repeat", nil)

	claim := models.RewardClaim{
		UserID: user.ID, Kind: models.RewardClaimKindSignup,
		Status: models.RewardClaimStatusPending, Name: "R", Username: "repeat",
	}
	if err := db.Create(&claim).Error; err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	got, _ := svc.Eligibility(context.Background(), user.ID, models.RewardClaimKindSignup)
	if got.Eligible {
		t.Fatal("signup already submitted — must be blocked")
	}
	if !got.AlreadySubmitted {
		t.Fatal("AlreadySubmitted should be true")
	}

	// The other form is independent: onboarding is blocked by its own gates,
	// not by the signup submission.
	got, _ = svc.Eligibility(context.Background(), user.ID, models.RewardClaimKindOnboarding)
	if got.AlreadySubmitted {
		t.Fatal("onboarding must not be marked already-submitted")
	}
}

func TestClaimEligibility_UnknownKindErrors(t *testing.T) {
	svc, db := newClaimTestService(t)
	user := mkVerifiedUser(t, db, "badkind", nil)
	if _, err := svc.Eligibility(context.Background(), user.ID, "lottery"); err == nil {
		t.Fatal("expected an error for an unknown kind")
	}
}
