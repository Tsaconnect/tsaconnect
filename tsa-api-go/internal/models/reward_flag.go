package models

import (
	"time"

	"github.com/google/uuid"
)

// Anti-sybil / claim-eligibility flags (fee/TP overhaul SP7).
//
// TP keeps accruing for everyone (SP3/SP5 are unchanged); a RewardFlag only
// gates whether a user may CLAIM the MCGP they've accrued — i.e. whether SP6
// may include them in a published merkle root. An ACTIVE flag (any source)
// makes the user claim-ineligible until an admin clears it.
//
// Two sources:
//   - RewardFlagSourceAuto  — written by the cheap, bounded velocity heuristic
//                             (RewardEligibilityService.RunVelocityScan). These
//                             are ADVISORY: they still gate the claim, but they
//                             exist primarily to surface suspicious referral
//                             growth to an admin for review.
//   - RewardFlagSourceAdmin — written by an admin via the admin API (manual
//                             flag of a confirmed bad actor).
//
// Flags are append-only in spirit: clearing sets Status=cleared rather than
// deleting the row, so the review history is preserved for audit.

// RewardFlag sources.
const (
	RewardFlagSourceAuto  = "auto"
	RewardFlagSourceAdmin = "admin"
)

// RewardFlag statuses.
const (
	// RewardFlagStatusActive gates the user's claim eligibility.
	RewardFlagStatusActive = "active"
	// RewardFlagStatusCleared is a resolved flag — no longer gates eligibility,
	// retained for audit.
	RewardFlagStatusCleared = "cleared"
)

// Velocity heuristic reason codes (RewardFlag.Reason prefix for auto-flags).
const (
	// RewardFlagReasonVelocity is set when a referrer's downline grew by more
	// than the configured threshold within the configured window.
	RewardFlagReasonVelocity = "velocity:downline_surge"
)

// RewardFlag is one claim-eligibility flag against a user.
type RewardFlag struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index:idx_reward_flags_user_status,priority:1" json:"userId"`

	// Reason is a human-readable explanation (admin note) or a machine reason
	// code (auto-flag, e.g. RewardFlagReasonVelocity).
	Reason string `gorm:"size:512;not null" json:"reason"`

	// Source is RewardFlagSourceAuto or RewardFlagSourceAdmin.
	Source string `gorm:"size:16;not null;default:'admin'" json:"source"`

	// Status is RewardFlagStatusActive or RewardFlagStatusCleared. Only an
	// active flag gates eligibility. Indexed with user_id so the active-flag
	// lookup in IsClaimEligible is a single indexed read.
	Status string `gorm:"size:16;not null;default:'active';index:idx_reward_flags_user_status,priority:2" json:"status"`

	// FlaggedBy is the admin who raised the flag (nil for auto-flags).
	FlaggedBy *uuid.UUID `gorm:"type:uuid" json:"flaggedBy,omitempty"`

	// ClearedBy is the admin who cleared the flag (nil while active).
	ClearedBy *uuid.UUID `gorm:"type:uuid" json:"clearedBy,omitempty"`
	ClearedAt *time.Time `json:"clearedAt,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (RewardFlag) TableName() string {
	return "reward_flags"
}
