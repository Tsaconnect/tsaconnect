package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TPProgramConfig is the admin-editable TP model (PM 2026-10-21): a fixed
// fraction of the system's retained share of each fee becomes TP, split between
// the transacting user, their direct upline and the global pool. One row.
//
// The three percentages are fractions OF THE SYSTEM'S SHARE (0.05 = 5%), so the
// TP carved from a fee is systemShare × (User+Upline+Pool).
type TPProgramConfig struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`

	UserPct   float64 `gorm:"not null" json:"userPct"`
	UplinePct float64 `gorm:"not null" json:"uplinePct"`
	PoolPct   float64 `gorm:"not null" json:"poolPct"`

	// ZeroFeeTP is credited to the user for a transaction that carries no fee.
	ZeroFeeTP float64 `gorm:"not null" json:"zeroFeeTp"`

	// PayoutDay is the day of month (Africa/Lagos, 00:00) the global pool is paid.
	PayoutDay int `gorm:"not null" json:"payoutDay"`

	UpdatedBy *uuid.UUID `gorm:"type:uuid" json:"updatedBy,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

func (TPProgramConfig) TableName() string { return "tp_program_configs" }

func (c *TPProgramConfig) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

// Defaults are the PM's numbers and apply whenever no row exists.
const (
	DefaultTPUserPct   = 0.05
	DefaultTPUplinePct = 0.05
	DefaultTPPoolPct   = 0.02
	DefaultZeroFeeTP   = 0.001
	DefaultPayoutDay   = 24
)

// DefaultTPProgramConfig returns the compiled-in program.
func DefaultTPProgramConfig() TPProgramConfig {
	return TPProgramConfig{
		UserPct:   DefaultTPUserPct,
		UplinePct: DefaultTPUplinePct,
		PoolPct:   DefaultTPPoolPct,
		ZeroFeeTP: DefaultZeroFeeTP,
		PayoutDay: DefaultPayoutDay,
	}
}

// TotalPct is the share of the system's fee share that becomes TP.
func (c TPProgramConfig) TotalPct() float64 { return c.UserPct + c.UplinePct + c.PoolPct }

// ValidTPProgramConfig: each pct within 0..1, the total within 0..1, a
// non-negative zero-fee reward, and a payout day every month has (1..28).
func ValidTPProgramConfig(c TPProgramConfig) bool {
	for _, p := range []float64{c.UserPct, c.UplinePct, c.PoolPct} {
		if p < 0 || p > 1 {
			return false
		}
	}
	return c.TotalPct() <= 1 && c.ZeroFeeTP >= 0 && c.PayoutDay >= 1 && c.PayoutDay <= 28
}
