package models

import (
	"time"

	"github.com/google/uuid"
)

// FeeRateIntentState is the lifecycle state of a desired-rate intent. It fans
// out a single per-(feature, token) intent across N chains (configurable-
// FeeManager design §5): the admin authors the desired rate, the backend plans
// the unsigned FeeManager tx(s), the multisig signs/broadcasts, and each chain's
// confirm advances the state until all chains have accepted it.
//
//	draft ──intent POST──► pending
//	pending ──plan──► pending (txs emitted; no state change)
//	pending ──confirm(chainId) ok──► partially_applied (this chain done; others pending)
//	partially_applied ──all chains confirmed──► applied
//	pending/partially_applied ──confirm fail──► stays
//	pending/partially_applied ──cancel (while not fully applied)──► cancelled
type FeeRateIntentState string

const (
	FeeRateIntentDraft            FeeRateIntentState = "draft"
	FeeRateIntentPending          FeeRateIntentState = "pending"
	FeeRateIntentPartiallyApplied FeeRateIntentState = "partially_applied"
	FeeRateIntentApplied          FeeRateIntentState = "applied"
	FeeRateIntentCancelled        FeeRateIntentState = "cancelled"
)

// FeeRateIntent is the DESIRED-state record for a fee rate. It is the off-chain
// authoring surface for the configurable-FeeManager flow (design §2/§4): the
// admin expresses what a (feature, token, chain) rate SHOULD be; the backend
// later diffs it against on-chain truth and builds the FeeManager.setRate tx(s).
//
// IMPORTANT: a FeeRateIntent NEVER charges. It is desired-state only. The thing
// that is actually charged is the fee_config READ REPLICA (models.FeeConfig),
// which is written exclusively from CONFIRMED on-chain RateSet values. Do not
// read FeeRateIntent on any charging hot-path.
//
// ChainID semantics match FeeConfig: NULL = the global default for the
// (feature, token); a non-NULL ChainID is that chain's override.
type FeeRateIntent struct {
	ID      uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Feature string    `gorm:"size:32;not null;uniqueIndex:idx_fee_rate_intent_feature_token_chain" json:"feature"`
	Token   string    `gorm:"size:16;not null;uniqueIndex:idx_fee_rate_intent_feature_token_chain" json:"token"`
	ChainID *int64    `gorm:"uniqueIndex:idx_fee_rate_intent_feature_token_chain" json:"chainId,omitempty"`

	BPS        uint16  `gorm:"not null;default:0" json:"bps"`
	FlatUSD    float64 `gorm:"not null;default:0" json:"flatUsd"`
	MCGPExempt bool    `gorm:"not null;default:false" json:"mcgpExempt"`
	Active     bool    `gorm:"not null;default:true" json:"active"`

	State FeeRateIntentState `gorm:"size:24;not null;default:'draft'" json:"state"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (FeeRateIntent) TableName() string {
	return "fee_rate_intents"
}
