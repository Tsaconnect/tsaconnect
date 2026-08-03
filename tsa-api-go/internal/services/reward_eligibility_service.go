package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// RewardEligibilityService is the fee/TP overhaul SP7 claim-eligibility gate.
//
// TP keeps accruing for EVERYONE (SP3/SP5 are unchanged). This service answers a
// narrower question: may a given user CLAIM the MCGP they've accrued — i.e. may
// SP6 include them in a published merkle root? A user is claim-eligible only
// when ALL of the following hold:
//
//	(a) KYC verified — user.verification_status == "verified" (the codebase's
//	    APPROVED value, models.VerificationStatusVerified; set by the Didit
//	    webhook on status.updated with status "Approved" and by the admin
//	    KYC override).
//	(a2) Account active — user.account_status == "active". A suspended/deleted
//	    account is the admin's enforcement action against a bad actor (spec §6)
//	    and blocks the claim on its own, without also needing a reward_flag.
//	(b) Not admin-flagged — no ACTIVE reward_flags row for the user (admin OR
//	    auto/velocity source; an advisory auto-flag still gates the claim until
//	    an admin clears it).
//	(c) No self-referral (referred_by == self) and not the apex of a referral
//	    CYCLE in the user's own upline chain. Cycle detection REUSES the SP2
//	    cycle-safe ReferralGraphService.Uplines walk (path/LIKE guard, depth
//	    bound) — it cannot hang on A→B→A or a self-reference.
//
// SP7 only PROVIDES the gate + the data + the admin tools; the gate is CONSUMED
// at root-publish time by SP6. This service performs no on-chain work and holds
// no keys.
type RewardEligibilityService struct {
	db    *gorm.DB
	graph *ReferralGraphService

	// Velocity heuristic config (see RunVelocityScan). When enabled is false the
	// heuristic is a no-op; the KYC/admin-flag/cycle gates are unaffected.
	velocityEnabled    bool
	velocityMaxSignups int
	velocityWindow     time.Duration
}

// cycleScanDepth bounds the upline walk used for cycle detection. It is a safety
// depth far beyond any legitimate referral chain; the SP2 path-guard already
// guarantees termination, this just caps work. Matches the 10-generation TP
// reach plus headroom.
const cycleScanDepth = 64

// NewRewardEligibilityService constructs the SP7 gate over the given DB +
// SP2 referral graph. The velocity heuristic defaults to DISABLED here; callers
// configure it via SetVelocityConfig (main.go wires it from cfg) so unit tests
// and the disabled-by-config case are explicit.
func NewRewardEligibilityService(db *gorm.DB, graph *ReferralGraphService) *RewardEligibilityService {
	return &RewardEligibilityService{
		db:                 db,
		graph:              graph,
		velocityEnabled:    false,
		velocityMaxSignups: 20,
		velocityWindow:     24 * time.Hour,
	}
}

// SetVelocityConfig tunes (and enables/disables) the velocity heuristic. A
// non-positive maxSignups or window, or enabled=false, leaves the heuristic a
// no-op. Wired from cfg in main.go.
func (s *RewardEligibilityService) SetVelocityConfig(enabled bool, maxSignups int, window time.Duration) {
	s.velocityEnabled = enabled
	if maxSignups > 0 {
		s.velocityMaxSignups = maxSignups
	}
	if window > 0 {
		s.velocityWindow = window
	}
}

// IsClaimEligible reports whether userID may be included in a claim/merkle root.
// It returns eligible=false plus one human-readable reason PER failed rule (so
// an admin/review UI can show every blocker at once), and err only on an
// unexpected DB/lookup failure (in which case eligible is false and reasons is
// nil — fail closed: never green-light a claim on an errored check).
func (s *RewardEligibilityService) IsClaimEligible(ctx context.Context, userID uuid.UUID) (bool, []string, error) {
	var user models.User
	if err := s.db.WithContext(ctx).
		Select("id", "verification_status", "account_status", "referred_by", "deleted_at").
		First(&user, "id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, []string{"User not found"}, nil
		}
		return false, nil, fmt.Errorf("eligibility: load user %s: %w", userID, err)
	}

	var reasons []string

	// (a0) Soft-delete gate. User.DeletedAt is a plain *time.Time (NOT
	// gorm.DeletedAt), so GORM's soft-delete scope does not auto-exclude these
	// rows — and a soft-deleted user can still carry account_status "active".
	// Gate it explicitly so a deleted account can never land in the merkle root.
	if user.DeletedAt != nil {
		reasons = append(reasons, "Account deleted")
	}

	// (a) KYC gate — must equal the codebase APPROVED value.
	if user.VerificationStatus != models.VerificationStatusVerified {
		reasons = append(reasons, fmt.Sprintf(
			"KYC not verified (status %q; must be %q)",
			user.VerificationStatus, models.VerificationStatusVerified))
	}

	// (a2) Account-status gate — only an ACTIVE account may claim. Suspension/
	// deletion is the admin's natural enforcement action against a bad actor
	// (spec §6), so it must gate the claim on its own without also requiring a
	// separate reward_flag. Fail closed: any non-active status blocks the claim.
	if user.AccountStatus != models.AccountStatusActive {
		reasons = append(reasons, fmt.Sprintf(
			"Account not active (status %q)", user.AccountStatus))
	}

	// (b) Admin/auto flag gate — any ACTIVE reward_flags row blocks the claim.
	var activeFlags int64
	if err := s.db.WithContext(ctx).Model(&models.RewardFlag{}).
		Where("user_id = ? AND status = ?", userID, models.RewardFlagStatusActive).
		Count(&activeFlags).Error; err != nil {
		return false, nil, fmt.Errorf("eligibility: count flags for %s: %w", userID, err)
	}
	if activeFlags > 0 {
		reasons = append(reasons, fmt.Sprintf(
			"Account is flagged for review (%d active flag(s))", activeFlags))
	}

	// (c) Self-referral + referral-cycle gate.
	if user.ReferredBy != nil && *user.ReferredBy == userID {
		reasons = append(reasons, "Self-referral detected (referred_by == self)")
	} else if user.ReferredBy != nil {
		// Detect a referral cycle in the user's own chain. If the user is the
		// apex of a cycle, the user reappears among its REFERRER's ancestors.
		inCycle, err := s.userInReferralCycle(ctx, userID, *user.ReferredBy)
		if err != nil {
			return false, nil, fmt.Errorf("eligibility: cycle check for %s: %w", userID, err)
		}
		if inCycle {
			reasons = append(reasons, "Referral cycle detected in upline chain")
		}
	}

	if len(reasons) > 0 {
		return false, reasons, nil
	}
	return true, nil, nil
}

// userInReferralCycle reports whether userID is the apex of a referral cycle,
// i.e. whether climbing referred_by from userID eventually returns to userID.
//
// It REUSES the SP2 cycle-safe Uplines walk: that walk carries each node on a
// delimited path and refuses to revisit any node (it never re-emits the START
// node either), so it terminates on A→B→A or a self-reference instead of
// looping. Because the walk excludes its own start node, we climb from the
// user's REFERRER and ask whether userID surfaces among the referrer's
// ancestors — if it does, the chain closes back on the user: a cycle.
//
// For A→B→A starting at A (referrer B): Uplines(B) emits A (gen1), then A's
// referrer B is already on the path and the climb stops — A is present ⇒ cycle.
// For an acyclic chain the user can never be its own ancestor, so it returns
// false. Self-referral (referred_by == self) is handled by the caller before
// this is reached.
func (s *RewardEligibilityService) userInReferralCycle(ctx context.Context, userID, referrerID uuid.UUID) (bool, error) {
	// The referrer being the user themselves is self-referral, already caught.
	if referrerID == userID {
		return true, nil
	}
	ancestors, err := s.graph.Uplines(ctx, referrerID, cycleScanDepth)
	if err != nil {
		return false, err
	}
	for _, a := range ancestors {
		if a.UserID == userID {
			return true, nil
		}
	}
	return false, nil
}

// RunVelocityScan is the cheap, bounded anti-sybil heuristic. For one referrer
// it counts how many DIRECT downline accounts that referrer gained within the
// configured window; if that exceeds the configured max it writes an advisory
// RewardFlag{source:auto} for admin review (which gates the referrer's claim
// until cleared). It is idempotent: it will not stack a second active auto
// velocity-flag on a referrer who already has one.
//
// Cost is one indexed COUNT over users referred by a single referrer in a time
// window — never a full-graph scan. It returns true if a NEW flag was written.
//
// Disabled (velocityEnabled=false) or mis-configured (non-positive threshold/
// window) → no-op, returns false.
func (s *RewardEligibilityService) RunVelocityScan(ctx context.Context, referrerID uuid.UUID) (bool, error) {
	if !s.velocityEnabled || s.velocityMaxSignups <= 0 || s.velocityWindow <= 0 {
		return false, nil
	}

	since := time.Now().Add(-s.velocityWindow)

	var recent int64
	if err := s.db.WithContext(ctx).Model(&models.User{}).
		Where("referred_by = ? AND created_at >= ? AND deleted_at IS NULL", referrerID, since).
		Count(&recent).Error; err != nil {
		return false, fmt.Errorf("velocity: count recent downline for %s: %w", referrerID, err)
	}

	if int(recent) <= s.velocityMaxSignups {
		return false, nil
	}

	// Idempotency: don't stack a second active auto velocity-flag.
	var existing int64
	if err := s.db.WithContext(ctx).Model(&models.RewardFlag{}).
		Where("user_id = ? AND status = ? AND source = ? AND reason = ?",
			referrerID, models.RewardFlagStatusActive, models.RewardFlagSourceAuto,
			velocityReason(int(recent), s.velocityWindow)).
		Count(&existing).Error; err != nil {
		return false, fmt.Errorf("velocity: dedup check for %s: %w", referrerID, err)
	}
	if existing > 0 {
		return false, nil
	}

	flag := models.RewardFlag{
		UserID: referrerID,
		Reason: velocityReason(int(recent), s.velocityWindow),
		Source: models.RewardFlagSourceAuto,
		Status: models.RewardFlagStatusActive,
	}
	if err := s.db.WithContext(ctx).Create(&flag).Error; err != nil {
		return false, fmt.Errorf("velocity: write auto-flag for %s: %w", referrerID, err)
	}
	return true, nil
}

// velocityReason builds the stable reason string for a velocity auto-flag.
// Kept deterministic (same inputs → same string) so the idempotency dedup above
// can match an existing active flag by reason.
func velocityReason(count int, window time.Duration) string {
	return fmt.Sprintf("%s (%d signups in %s)", models.RewardFlagReasonVelocity, count, window)
}
