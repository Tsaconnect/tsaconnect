package services

import (
	"context"
	"fmt"
	"time"

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

// SignupRewardTP is the TradePoint award for an approved signup claim. The
// programme pays "airtime or TP"; airtime has no rail in this tree yet (Nomba
// bill vending lives on the unmerged feat/utility-bills branch), so TP is the
// leg that can actually settle today.
const SignupRewardTP = 100.0

// AwardSignupTP credits the signup reward when a claim is approved. Idempotent:
// the (source_type, source_id) pair is checked first so re-approving — or a
// retry after a partial failure — can't double-credit.
//
// Mirrors creditTP (fee_distribution_service.go): a TPEarning row plus an
// atomic tp_balance increment in ONE transaction, then mark the user's reward
// state dirty so the level-crossing job re-evaluates.
func (s *RewardClaimService) AwardSignupTP(ctx context.Context, claim *models.RewardClaim) error {
	if claim == nil || claim.Kind != models.RewardClaimKindSignup {
		return nil // onboarding pays cash off-platform; nothing to credit here
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&models.TPEarning{}).
			Where("source_type = ? AND source_id = ?", tpSourceSignupReward, claim.ID).
			Limit(1).Count(&existing).Error; err != nil {
			return fmt.Errorf("reward claim: check existing tp award: %w", err)
		}
		if existing > 0 {
			return nil // already credited
		}

		earning := models.TPEarning{
			ID:           uuid.New(),
			UserID:       claim.UserID,
			SourceUserID: claim.UserID,
			SourceType:   tpSourceSignupReward,
			SourceID:     claim.ID,
			Generation:   0, // direct award, not a referral-generation share
			TPEarned:     SignupRewardTP,
			CreatedAt:    time.Now(),
		}
		if err := tx.Create(&earning).Error; err != nil {
			return fmt.Errorf("reward claim: create tp earning: %w", err)
		}
		if err := tx.Model(&models.User{}).Where("id = ?", claim.UserID).
			Update("tp_balance", gorm.Expr("tp_balance + ?", SignupRewardTP)).Error; err != nil {
			return fmt.Errorf("reward claim: increment tp_balance: %w", err)
		}
		// Best-effort: the level-crossing job also re-scans, so a missing row
		// here delays the level update rather than losing the award.
		tx.Model(&models.UserRewardState{}).Where("user_id = ?", claim.UserID).
			Update("dirty", true)
		return nil
	})
}

const tpSourceSignupReward = "signup_reward"

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
