package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// fee_split_config.go — admin-editable version of the per-feature fee split.
//
// The split decides how a settled fee is carved into system / buyer cashback /
// upline cashback / provider cashback / TP pool. It was a hardcoded Go map
// (services.featureSplits); the PM needs the per-section TP rate tunable
// without a deploy, so the map becomes the SEED and this table the override.
//
// The effective TP rate a section advertises is fee_configs.BPS × TPShare —
// e.g. spot 3% × 0.30 = 0.9%. Changing TPShare here moves that rate.
//
// INVARIANT: the five shares must sum to 1.0. It is enforced on write (a bad
// row would over- or under-credit every fee for that feature) and again when a
// row is applied, so a hand-edited DB row can't silently leak value.
type FeeSplitConfig struct {
	ID      uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Feature string    `gorm:"not null;uniqueIndex" json:"feature"`

	SystemShare   float64 `gorm:"not null" json:"systemShare"`
	BuyerShare    float64 `gorm:"not null" json:"buyerShare"`
	UplineShare   float64 `gorm:"not null" json:"uplineShare"`
	ProviderShare float64 `gorm:"not null" json:"providerShare"`
	TPShare       float64 `gorm:"not null" json:"tpShare"`

	// Inactive rows fall back to the compiled-in default for that feature —
	// an escape hatch that restores known-good behaviour without a delete.
	Active bool `gorm:"not null;default:true" json:"active"`

	UpdatedBy *uuid.UUID `gorm:"type:uuid" json:"updatedBy,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

func (FeeSplitConfig) TableName() string { return "fee_split_configs" }

func (f *FeeSplitConfig) BeforeCreate(tx *gorm.DB) error {
	if f.ID == uuid.Nil {
		f.ID = uuid.New()
	}
	return nil
}

// Sum totals the five shares. Callers compare against 1.0 within tolerance.
func (f FeeSplitConfig) Sum() float64 {
	return f.SystemShare + f.BuyerShare + f.UplineShare + f.ProviderShare + f.TPShare
}

// FeeSplitTolerance is the float slack allowed when checking Sum() == 1.0.
const FeeSplitTolerance = 1e-9

// ValidFeeSplit reports whether every share is in [0,1] and they sum to 1.0.
func ValidFeeSplit(f FeeSplitConfig) bool {
	for _, share := range []float64{f.SystemShare, f.BuyerShare, f.UplineShare, f.ProviderShare, f.TPShare} {
		if share < 0 || share > 1 {
			return false
		}
	}
	d := f.Sum() - 1.0
	return d <= FeeSplitTolerance && d >= -FeeSplitTolerance
}
