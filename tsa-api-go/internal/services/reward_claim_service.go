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
	tpProgram   *TPProgramConfigService
}

func NewRewardClaimService(db *gorm.DB, elig *RewardEligibilityService, graph *ReferralGraphService) *RewardClaimService {
	return &RewardClaimService{db: db, eligibility: elig, graph: graph}
}

// WithTPProgram supplies the admin-edited welcome-bonus amounts. Without it the
// compiled-in defaults (1 TP signup, 10 TP onboarding) apply.
func (s *RewardClaimService) WithTPProgram(p *TPProgramConfigService) *RewardClaimService {
	s.tpProgram = p
	return s
}

// The TP source types the instant welcome bonuses are recorded under.
const (
	tpSourceSignupBonus     = "signup_bonus"
	tpSourceOnboardingBonus = "onboarding_bonus"
)

// SubmitClaim stores a claim that already passed Eligibility and pays its welcome
// bonus in the SAME transaction (PM 2026-10-23: automated, instant, no approval).
// A duplicate submission fails on the (user, kind) unique index BEFORE any TP is
// credited, so the bonus is paid at most once per form, and a failure credits
// nothing and leaves no claim behind, so the user can submit again.
func (s *RewardClaimService) SubmitClaim(ctx context.Context, claim *models.RewardClaim) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(claim).Error; err != nil {
			return err
		}
		return s.creditWelcomeBonus(tx, claim)
	})
}

func (s *RewardClaimService) creditWelcomeBonus(tx *gorm.DB, claim *models.RewardClaim) error {
	prog := s.tpProgram.Current()
	amount, source := prog.SignupBonusTP, tpSourceSignupBonus
	if claim.Kind == models.RewardClaimKindOnboarding {
		amount, source = prog.OnboardingBonusTP, tpSourceOnboardingBonus
	}
	if amount <= 0 {
		return nil
	}
	earning := models.TPEarning{
		ID: uuid.New(), UserID: claim.UserID, SourceUserID: claim.UserID,
		SourceType: source, SourceID: claim.ID,
		Generation: 0, // a direct award, not a referral-generation share
		TPEarned:   amount, CreatedAt: time.Now(),
	}
	if err := tx.Create(&earning).Error; err != nil {
		return fmt.Errorf("reward claim: create tp earning: %w", err)
	}
	if err := tx.Model(&models.User{}).Where("id = ?", claim.UserID).
		Update("tp_balance", gorm.Expr("tp_balance + ?", amount)).Error; err != nil {
		return fmt.Errorf("reward claim: increment tp_balance: %w", err)
	}
	// Best-effort: the level-crossing job also re-scans, so a missing row delays
	// the level update rather than losing the bonus.
	tx.Model(&models.UserRewardState{}).Where("user_id = ?", claim.UserID).Update("dirty", true)
	return nil
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
