package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// reward_claim.go — the PM "Reward" programme: two ONE-TIME enrolment forms.
//
// NAMING: this is deliberately NOT called Reward*. `RewardEntry`,
// `RewardClaimable`, `RewardFlag` and `RewardDistribution` already exist and all
// mean the TP-ladder MCGP claim pipeline. This is a different concept — a
// one-off signup/onboarding bonus application — so it lives in `reward_claims`.

// Claim kinds. Signup pays airtime or TP; onboarding pays cash (fiat or token)
// and unlocks only after the referral + transaction gates pass.
const (
	RewardClaimKindSignup     = "signup"
	RewardClaimKindOnboarding = "onboarding"
)

// ValidRewardClaimKind reports whether kind is a supported form.
func ValidRewardClaimKind(kind string) bool {
	return kind == RewardClaimKindSignup || kind == RewardClaimKindOnboarding
}

// Claim lifecycle. `paid` is terminal and set once a payout rail actually
// settles — nothing sets it today (see RewardPayoutRail seam).
const (
	RewardClaimStatusPending  = "pending"
	RewardClaimStatusApproved = "approved"
	RewardClaimStatusRejected = "rejected"
	RewardClaimStatusPaid     = "paid"
)

// ValidRewardClaimStatus reports whether status is a recognised state.
func ValidRewardClaimStatus(status string) bool {
	switch status {
	case RewardClaimStatusPending, RewardClaimStatusApproved,
		RewardClaimStatusRejected, RewardClaimStatusPaid:
		return true
	default:
		return false
	}
}

// Signup reward pays airtime, so the phone line's network matters.
var RewardPhoneNetworks = map[string]bool{
	"mtn": true, "airtel": true, "glo": true, "9mobile": true,
}

// ValidRewardPhoneNetwork reports whether n is a supported airtime network
// (lowercase).
func ValidRewardPhoneNetwork(n string) bool {
	return RewardPhoneNetworks[n]
}

// RewardClaim is one submitted enrolment form. One row per (user, kind) — the
// composite unique index is what makes each form one-time; the handler turns
// the constraint violation into a 409 rather than pre-checking and racing.
//
// Fields are split by form: PhoneNumber/PhoneNetwork/InviterUsername/SocialLinks
// belong to signup; PayoutMethod/PayoutDetails/InvitedUsernames to onboarding.
// Both forms share Name/Username.
type RewardClaim struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_reward_claim_user_kind" json:"userId"`
	Kind   string    `gorm:"not null;uniqueIndex:idx_reward_claim_user_kind" json:"kind"`
	Status string    `gorm:"not null;default:'pending';index" json:"status"`

	// Shared
	Name     string `gorm:"not null" json:"name"`
	Username string `gorm:"not null;index" json:"username"`

	// Signup form
	PhoneNumber     string `json:"phoneNumber,omitempty"`
	PhoneNetwork    string `json:"phoneNetwork,omitempty"`
	InviterUsername string `json:"inviterUsername,omitempty"`
	SocialLinks     string `json:"socialLinks,omitempty"`

	// Onboarding form. PayoutDetails is free text (bank account or mobile-money
	// handle); InvitedUsernames is a comma-separated list captured at submit
	// time, kept verbatim as the applicant's own claim of who they onboarded.
	PayoutMethod     string `json:"payoutMethod,omitempty"`
	PayoutDetails    string `json:"payoutDetails,omitempty"`
	InvitedUsernames string `json:"invitedUsernames,omitempty"`

	// Set when an admin approves/rejects.
	ReviewedBy  *uuid.UUID `gorm:"type:uuid" json:"reviewedBy,omitempty"`
	ReviewNotes string     `json:"reviewNotes,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (RewardClaim) TableName() string {
	return "reward_claims"
}

// BeforeCreate assigns a UUID when unset so inserts work on both Postgres
// (gen_random_uuid()) and the SQLite used in tests.
func (r *RewardClaim) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// MinOnboardingReferrals is the number of KYC-verified referrals the onboarding
// form requires. The PM copy says "minimum of 10", so this is a floor, not an
// exact count.
const MinOnboardingReferrals = 10
