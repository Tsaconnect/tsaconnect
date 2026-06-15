package models

import (
	"time"

	"github.com/google/uuid"
)

// PaymentTokenAny is the wildcard payment-token key: the fallback row for a
// reward kind when no payment-token-specific mapping exists (e.g. TP-level
// rewards, which are not tied to a single payment token).
const PaymentTokenAny = "*"

// RewardPayoutConfig maps (reward kind, the token the user paid the fee with) to
// the token + chain the reward is PAID OUT in. The payout token is deliberately
// NOT hardcoded (PM 2026-06-15): an admin can repoint cashback / TP-level
// payouts to USDC, USDT, or MCGP on any chain from the admin portal. Resolved
// per settlement by RewardPayoutConfigService. Unique on (reward_kind, payment_token).
type RewardPayoutConfig struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`

	// RewardKind is RewardKindLevel ("level_reward") or RewardKindCashback.
	RewardKind string `gorm:"size:24;not null;uniqueIndex:idx_reward_payout_kind_token" json:"rewardKind"`

	// PaymentToken is the symbol the user paid the originating fee with (e.g.
	// "USDC", "USDT", "MCGP"), or PaymentTokenAny ("*") for the kind-wide default.
	PaymentToken string `gorm:"size:16;not null;uniqueIndex:idx_reward_payout_kind_token" json:"paymentToken"`

	// PayoutToken is the symbol the reward is paid in ("USDC", "USDT", "MCGP").
	PayoutToken string `gorm:"size:16;not null" json:"payoutToken"`

	// Chain is the chain key the payout lands on ("sonic", "bsc", …).
	Chain string `gorm:"size:32;not null" json:"chain"`

	Active    bool      `gorm:"not null;default:true" json:"active"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
