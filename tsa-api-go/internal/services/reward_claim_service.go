package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// reward_claim_service.go — eligibility for the PM "Reward" programme forms.
//
// This composes the EXISTING RewardEligibilityService (KYC / account-status /
// anti-sybil flags / soft-delete) rather than restating those rules, and adds
// the two gates specific to this programme:
//   - onboarding needs >= MinOnboardingReferrals KYC-VERIFIED direct referrals
//   - onboarding needs at least one completed transaction
// Both forms are one-time, so an existing submission blocks re-application.
//
// Neither form pays anything today (PM 2026-10-23: no signup reward; claiming comes
// later): approving a claim records the decision only.

// RewardClaimEligibility is the per-form verdict. Reasons carries one
// human-readable string per failed rule so the app can list every blocker at
// once instead of revealing them one at a time.
type RewardClaimEligibility struct {
	Eligible          bool     `json:"eligible"`
	Reasons           []string `json:"reasons"`
	VerifiedReferrals int      `json:"verifiedReferrals"`
	HasTransaction    bool     `json:"hasTransaction"`
	AlreadySubmitted  bool     `json:"alreadySubmitted"`
	// RequiredReferrals echoes the threshold so the UI needn't hardcode it.
	RequiredReferrals int `json:"requiredReferrals"`
}

type RewardClaimService struct {
	db          *gorm.DB
	eligibility *RewardEligibilityService
	graph       *ReferralGraphService
}

func NewRewardClaimService(db *gorm.DB, elig *RewardEligibilityService, graph *ReferralGraphService) *RewardClaimService {
	return &RewardClaimService{db: db, eligibility: elig, graph: graph}
}

// Eligibility evaluates one form for one user. It fails closed: any unexpected
// DB error returns an error and a non-eligible verdict rather than letting a
// submission through on an errored check.
func (s *RewardClaimService) Eligibility(ctx context.Context, userID uuid.UUID, kind string) (RewardClaimEligibility, error) {
	if !models.ValidRewardClaimKind(kind) {
		return RewardClaimEligibility{}, fmt.Errorf("reward claim: unknown kind %q", kind)
	}

	out := RewardClaimEligibility{RequiredReferrals: models.MinOnboardingReferrals}

	// Base gates (KYC, active account, not flagged, not deleted).
	baseOK, reasons, err := s.eligibility.IsClaimEligible(ctx, userID)
	if err != nil {
		return RewardClaimEligibility{}, err
	}
	out.Reasons = append(out.Reasons, reasons...)

	// One-time: an existing row for this (user, kind) blocks re-application
	// whatever its status — a rejected application is still an application.
	var submitted int64
	if err := s.db.WithContext(ctx).Model(&models.RewardClaim{}).
		Where("user_id = ? AND kind = ?", userID, kind).
		Limit(1).Count(&submitted).Error; err != nil {
		return RewardClaimEligibility{}, fmt.Errorf("reward claim: count submissions: %w", err)
	}
	if submitted > 0 {
		out.AlreadySubmitted = true
		out.Reasons = append(out.Reasons, "You have already submitted this form")
	}

	// Referral + transaction counts are reported for BOTH forms so the app can
	// show progress ("3 of 10") on the locked onboarding card, but they only
	// gate the onboarding form.
	verified, err := s.verifiedReferralCount(ctx, userID)
	if err != nil {
		return RewardClaimEligibility{}, err
	}
	out.VerifiedReferrals = verified

	hasTx, err := s.hasCompletedTransaction(ctx, userID)
	if err != nil {
		return RewardClaimEligibility{}, err
	}
	out.HasTransaction = hasTx

	if kind == models.RewardClaimKindOnboarding {
		if verified < models.MinOnboardingReferrals {
			out.Reasons = append(out.Reasons, fmt.Sprintf(
				"Onboard at least %d verified users (you have %d)",
				models.MinOnboardingReferrals, verified))
		}
		if !hasTx {
			out.Reasons = append(out.Reasons, "Make at least one transaction")
		}
	}

	out.Eligible = baseOK && len(out.Reasons) == 0
	return out, nil
}

// verifiedReferralCount counts DIRECT referrals (generation 1) that are
// KYC-verified and active. Deeper generations don't count: the PM copy is
// "users you onboard", which is the people who used your referral code.
func (s *RewardClaimService) verifiedReferralCount(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int64
	if err := s.db.WithContext(ctx).Model(&models.User{}).
		Where("referred_by = ? AND verification_status = ? AND account_status = ? AND deleted_at IS NULL",
			userID, models.VerificationStatusVerified, models.AccountStatusActive).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("reward claim: count verified referrals: %w", err)
	}
	return int(n), nil
}

// hasCompletedTransaction reports whether the user has at least one completed
// row in the generic transactions ledger. Limit(1) keeps it an existence check.
func (s *RewardClaimService) hasCompletedTransaction(ctx context.Context, userID uuid.UUID) (bool, error) {
	var n int64
	if err := s.db.WithContext(ctx).Model(&models.Transaction{}).
		Where("user_id = ? AND status = ?", userID, models.TransactionStatusCompleted).
		Limit(1).Count(&n).Error; err != nil {
		return false, fmt.Errorf("reward claim: count transactions: %w", err)
	}
	return n > 0, nil
}
