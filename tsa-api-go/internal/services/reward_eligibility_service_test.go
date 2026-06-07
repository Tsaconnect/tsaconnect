package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// addRewardFlagsTable creates the reward_flags table on the in-memory SQLite DB
// produced by setupReferralGraphTestDB (defined in referral_graph_service_test.go,
// same package). Raw DDL avoids the PostgreSQL gen_random_uuid() default that
// AutoMigrate would emit. The eligibility service only reads/writes the columns
// declared here.
func addRewardFlagsTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE reward_flags (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		reason TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT 'admin',
		status TEXT NOT NULL DEFAULT 'active',
		flagged_by TEXT,
		cleared_by TEXT,
		cleared_at DATETIME,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create reward_flags: %v", err)
	}
}

// newEligTestService wires the SP7 service over a fresh SQLite DB with both the
// users table (from setupReferralGraphTestDB) and reward_flags. Velocity is left
// DISABLED by default; tests that exercise it enable it explicitly.
func newEligTestService(t *testing.T) (*RewardEligibilityService, *gorm.DB) {
	t.Helper()
	db := setupReferralGraphTestDB(t)
	addRewardFlagsTable(t, db)
	graph := NewReferralGraphService(db)
	return NewRewardEligibilityService(db, graph), db
}

// mkVerifiedUser inserts an active, KYC-verified user with the given referrer.
func mkVerifiedUser(t *testing.T, db *gorm.DB, username string, referredBy *uuid.UUID) models.User {
	t.Helper()
	u := mkUser(t, db, username, referredBy, models.AccountStatusActive)
	if err := db.Model(&models.User{}).Where("id = ?", u.ID).
		Update("verification_status", models.VerificationStatusVerified).Error; err != nil {
		t.Fatalf("verify user %s: %v", username, err)
	}
	u.VerificationStatus = models.VerificationStatusVerified
	return u
}

// hasReason reports whether any reason contains the given substring
// (case-insensitive).
func hasReason(reasons []string, substr string) bool {
	for _, r := range reasons {
		if strings.Contains(strings.ToLower(r), strings.ToLower(substr)) {
			return true
		}
	}
	return false
}

// TestEligibility_UnverifiedKYC_Ineligible: an unverified user is ineligible with
// a KYC reason. Confirms the gate keys off the REAL approved value "verified".
func TestEligibility_UnverifiedKYC_Ineligible(t *testing.T) {
	svc, db := newEligTestService(t)

	// Default verification_status is 'pending' (raw DDL default).
	u := mkUser(t, db, "pending_user", nil, models.AccountStatusActive)

	eligible, reasons, err := svc.IsClaimEligible(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("IsClaimEligible: %v", err)
	}
	if eligible {
		t.Fatalf("expected ineligible for unverified KYC, got eligible")
	}
	if !hasReason(reasons, "KYC not verified") {
		t.Errorf("expected a KYC reason, got %v", reasons)
	}
}

// TestEligibility_VerifiedClean_Eligible: a verified, unflagged, cycle-free user
// is eligible with no reasons.
func TestEligibility_VerifiedClean_Eligible(t *testing.T) {
	svc, db := newEligTestService(t)

	root := mkVerifiedUser(t, db, "clean_root", nil)
	u := mkVerifiedUser(t, db, "clean_user", ptr(root.ID))

	eligible, reasons, err := svc.IsClaimEligible(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("IsClaimEligible: %v", err)
	}
	if !eligible {
		t.Fatalf("expected eligible for verified+clean user, got reasons %v", reasons)
	}
	if len(reasons) != 0 {
		t.Errorf("expected no reasons, got %v", reasons)
	}
}

// TestEligibility_VerifiedValueIsReal pins that the gate accepts ONLY the real
// approved constant. A user set to any other status (e.g. "approved", a common
// wrong guess) must remain ineligible.
func TestEligibility_VerifiedValueIsReal(t *testing.T) {
	svc, db := newEligTestService(t)

	if models.VerificationStatusVerified != "verified" {
		t.Fatalf("APPROVED value drifted: VerificationStatusVerified = %q, want \"verified\"",
			models.VerificationStatusVerified)
	}

	u := mkUser(t, db, "wrong_status_user", nil, models.AccountStatusActive)
	// Deliberately set a plausible-but-WRONG value.
	if err := db.Model(&models.User{}).Where("id = ?", u.ID).
		Update("verification_status", "approved").Error; err != nil {
		t.Fatalf("set status: %v", err)
	}
	eligible, reasons, err := svc.IsClaimEligible(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("IsClaimEligible: %v", err)
	}
	if eligible {
		t.Fatalf("status \"approved\" must NOT satisfy the gate; only \"verified\" does")
	}
	if !hasReason(reasons, "KYC not verified") {
		t.Errorf("expected KYC reason, got %v", reasons)
	}
}

// TestEligibility_NonActiveAccount_Ineligible: a KYC-verified, unflagged,
// cycle-free user whose account_status is NOT "active" (suspended/deleted) is
// ineligible. Suspension is the admin's enforcement action against a bad actor
// (spec §6) and must gate the claim on its own, without a separate reward_flag.
func TestEligibility_NonActiveAccount_Ineligible(t *testing.T) {
	for _, status := range []string{models.AccountStatusSuspended, models.AccountStatusDeleted, models.AccountStatusInactive} {
		status := status
		t.Run(status, func(t *testing.T) {
			svc, db := newEligTestService(t)

			// Verified + unflagged + no referrer (cycle-free), but non-active.
			u := mkVerifiedUser(t, db, "nonactive_"+status, nil)
			if err := db.Model(&models.User{}).Where("id = ?", u.ID).
				Update("account_status", status).Error; err != nil {
				t.Fatalf("set account_status=%s: %v", status, err)
			}

			eligible, reasons, err := svc.IsClaimEligible(context.Background(), u.ID)
			if err != nil {
				t.Fatalf("IsClaimEligible: %v", err)
			}
			if eligible {
				t.Fatalf("expected ineligible for account_status=%q, got eligible", status)
			}
			if !hasReason(reasons, "not active") {
				t.Errorf("expected an account-status reason for %q, got %v", status, reasons)
			}
		})
	}
}

// TestEligibility_AdminFlagged_IneligibleUntilCleared: an active admin flag
// makes a verified user ineligible; clearing it restores eligibility.
func TestEligibility_AdminFlagged_IneligibleUntilCleared(t *testing.T) {
	svc, db := newEligTestService(t)

	u := mkVerifiedUser(t, db, "flagged_user", nil)
	adminID := uuid.New()

	flag := models.RewardFlag{
		ID:        uuid.New(),
		UserID:    u.ID,
		Reason:    "manual: suspected multi-account",
		Source:    models.RewardFlagSourceAdmin,
		Status:    models.RewardFlagStatusActive,
		FlaggedBy: &adminID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := db.Create(&flag).Error; err != nil {
		t.Fatalf("create flag: %v", err)
	}

	// Flagged -> ineligible.
	eligible, reasons, err := svc.IsClaimEligible(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("IsClaimEligible: %v", err)
	}
	if eligible {
		t.Fatalf("expected ineligible while flagged, got eligible")
	}
	if !hasReason(reasons, "flagged") {
		t.Errorf("expected a flagged reason, got %v", reasons)
	}

	// Clear the flag -> eligible again.
	now := time.Now()
	if err := db.Model(&models.RewardFlag{}).Where("id = ?", flag.ID).Updates(map[string]interface{}{
		"status":     models.RewardFlagStatusCleared,
		"cleared_by": adminID,
		"cleared_at": now,
	}).Error; err != nil {
		t.Fatalf("clear flag: %v", err)
	}

	eligible, reasons, err = svc.IsClaimEligible(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("IsClaimEligible after clear: %v", err)
	}
	if !eligible {
		t.Fatalf("expected eligible after clearing flag, got reasons %v", reasons)
	}
}

// TestEligibility_SelfReferral_Ineligible: referred_by == self blocks the claim.
func TestEligibility_SelfReferral_Ineligible(t *testing.T) {
	svc, db := newEligTestService(t)

	u := mkVerifiedUser(t, db, "selfref_user", nil)
	if err := db.Model(&models.User{}).Where("id = ?", u.ID).
		Update("referred_by", u.ID).Error; err != nil {
		t.Fatalf("set self-ref: %v", err)
	}

	eligible, reasons, err := svc.IsClaimEligible(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("IsClaimEligible: %v", err)
	}
	if eligible {
		t.Fatalf("expected ineligible for self-referral, got eligible")
	}
	if !hasReason(reasons, "Self-referral") {
		t.Errorf("expected self-referral reason, got %v", reasons)
	}
}

// TestEligibility_ReferralCycle_IneligibleNoHang: a user that is the apex of a
// referral cycle (A->B->A) is ineligible, and the check terminates (no hang).
func TestEligibility_ReferralCycle_IneligibleNoHang(t *testing.T) {
	svc, db := newEligTestService(t)

	// A.referred_by = B, B.referred_by = A. A is verified (the user under test).
	a := mkVerifiedUser(t, db, "cyc_user_a", nil)
	b := mkVerifiedUser(t, db, "cyc_user_b", ptr(a.ID))
	if err := db.Model(&models.User{}).Where("id = ?", a.ID).
		Update("referred_by", b.ID).Error; err != nil {
		t.Fatalf("close cycle: %v", err)
	}

	type res struct {
		eligible bool
		reasons  []string
		err      error
	}
	done := make(chan res, 1)
	go func() {
		e, r, err := svc.IsClaimEligible(context.Background(), a.ID)
		done <- res{e, r, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("IsClaimEligible on cycle: %v", got.err)
		}
		if got.eligible {
			t.Fatalf("expected ineligible for referral cycle, got eligible")
		}
		if !hasReason(got.reasons, "cycle") {
			t.Errorf("expected a cycle reason, got %v", got.reasons)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("IsClaimEligible hung on a referral cycle (no termination)")
	}
}

// TestVelocity_AutoFlagsOverThreshold: a referrer whose direct downline grows
// past the threshold within the window gets an advisory auto-flag, which then
// makes the (otherwise-clean, verified) referrer ineligible.
func TestVelocity_AutoFlagsOverThreshold(t *testing.T) {
	svc, db := newEligTestService(t)
	// Enable: > 3 direct signups in 24h trips a flag.
	svc.SetVelocityConfig(true, 3, 24*time.Hour)

	referrer := mkVerifiedUser(t, db, "vel_referrer", nil)

	// 4 fresh direct downline accounts (> threshold of 3).
	for i := 0; i < 4; i++ {
		mkUser(t, db, "vel_dl_"+uuid.NewString()[:8], ptr(referrer.ID), models.AccountStatusActive)
	}

	flagged, err := svc.RunVelocityScan(context.Background(), referrer.ID)
	if err != nil {
		t.Fatalf("RunVelocityScan: %v", err)
	}
	if !flagged {
		t.Fatalf("expected an auto-flag for downline surge over threshold")
	}

	// An auto-flag exists, active, source=auto.
	var cnt int64
	if err := db.Model(&models.RewardFlag{}).
		Where("user_id = ? AND status = ? AND source = ?",
			referrer.ID, models.RewardFlagStatusActive, models.RewardFlagSourceAuto).
		Count(&cnt).Error; err != nil {
		t.Fatalf("count flags: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("expected exactly 1 active auto-flag, got %d", cnt)
	}

	// The advisory auto-flag gates the referrer's claim despite being verified.
	eligible, reasons, err := svc.IsClaimEligible(context.Background(), referrer.ID)
	if err != nil {
		t.Fatalf("IsClaimEligible: %v", err)
	}
	if eligible {
		t.Fatalf("expected referrer ineligible while auto-flagged, got eligible")
	}
	if !hasReason(reasons, "flagged") {
		t.Errorf("expected flagged reason, got %v", reasons)
	}

	// Idempotent: a second scan over the same surge must not stack a flag.
	flagged2, err := svc.RunVelocityScan(context.Background(), referrer.ID)
	if err != nil {
		t.Fatalf("RunVelocityScan (2nd): %v", err)
	}
	if flagged2 {
		t.Errorf("second scan stacked a duplicate auto-flag")
	}
}

// TestVelocity_UnderThreshold_NoFlag: at or below the threshold, no flag; and a
// disabled heuristic never flags even over the threshold.
func TestVelocity_UnderThreshold_NoFlag(t *testing.T) {
	svc, db := newEligTestService(t)
	svc.SetVelocityConfig(true, 3, 24*time.Hour)

	referrer := mkVerifiedUser(t, db, "vel_under_referrer", nil)
	// Exactly 3 direct downline (== threshold, not > threshold).
	for i := 0; i < 3; i++ {
		mkUser(t, db, "vel_under_dl_"+uuid.NewString()[:8], ptr(referrer.ID), models.AccountStatusActive)
	}

	flagged, err := svc.RunVelocityScan(context.Background(), referrer.ID)
	if err != nil {
		t.Fatalf("RunVelocityScan: %v", err)
	}
	if flagged {
		t.Fatalf("expected NO flag at/under threshold")
	}

	// Referrer is verified + clean -> eligible.
	eligible, reasons, err := svc.IsClaimEligible(context.Background(), referrer.ID)
	if err != nil {
		t.Fatalf("IsClaimEligible: %v", err)
	}
	if !eligible {
		t.Fatalf("expected eligible (no flag), got reasons %v", reasons)
	}

	// Disabled heuristic: even over threshold, no flag.
	svc.SetVelocityConfig(false, 3, 24*time.Hour)
	r2 := mkVerifiedUser(t, db, "vel_disabled_referrer", nil)
	for i := 0; i < 10; i++ {
		mkUser(t, db, "vel_disabled_dl_"+uuid.NewString()[:8], ptr(r2.ID), models.AccountStatusActive)
	}
	flagged2, err := svc.RunVelocityScan(context.Background(), r2.ID)
	if err != nil {
		t.Fatalf("RunVelocityScan (disabled): %v", err)
	}
	if flagged2 {
		t.Fatalf("disabled heuristic must never flag")
	}
}

// TestVelocity_StaleSignupsExcluded: signups older than the window do not count
// toward the velocity surge.
func TestVelocity_StaleSignupsExcluded(t *testing.T) {
	svc, db := newEligTestService(t)
	svc.SetVelocityConfig(true, 3, 1*time.Hour)

	referrer := mkVerifiedUser(t, db, "vel_stale_referrer", nil)

	// 5 downline, but all created 2h ago (outside the 1h window).
	old := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 5; i++ {
		u := mkUser(t, db, "vel_stale_dl_"+uuid.NewString()[:8], ptr(referrer.ID), models.AccountStatusActive)
		if err := db.Model(&models.User{}).Where("id = ?", u.ID).
			Update("created_at", old).Error; err != nil {
			t.Fatalf("backdate signup: %v", err)
		}
	}

	flagged, err := svc.RunVelocityScan(context.Background(), referrer.ID)
	if err != nil {
		t.Fatalf("RunVelocityScan: %v", err)
	}
	if flagged {
		t.Fatalf("stale (out-of-window) signups must not trip the velocity flag")
	}
}
