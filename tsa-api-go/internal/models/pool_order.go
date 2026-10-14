package models

import (
	"time"

	"github.com/google/uuid"
)

// PoolOrder is a pool order's per-attempt snapshot (spec 2026-10-08 §7.6,
// decision 7): the rates it was priced at and, later, its books. One row per
// generation; a re-delivery adds a generation and never overwrites (S41).
// Settle-side columns (delivered, take, LP total, payout fee, keep, settled)
// are written by plan C2. No reserve column (decision 20).
type PoolOrder struct {
	IntentID      uuid.UUID `gorm:"type:uuid;primaryKey" json:"intentId"`
	Generation    int       `gorm:"primaryKey" json:"generation"`
	ChainID       int64     `gorm:"not null" json:"chainId"`
	Token         string    `gorm:"size:16;not null" json:"token"`
	TokenDecimals int       `gorm:"not null" json:"tokenDecimals"`
	// Kobo per token.
	MidMinor         string `gorm:"type:numeric(38,0);not null" json:"midMinor"`
	PoolSpreadBps    int    `gorm:"not null" json:"poolSpreadBps"`
	PoolTakeBps      int    `gorm:"not null" json:"poolTakeBps"`
	FundingBps       int    `gorm:"not null" json:"fundingBps"`
	BandBps          int    `gorm:"not null" json:"bandBps"`
	LPRateMinor      string `gorm:"type:numeric(38,0);not null" json:"lpRateMinor"`
	DisplayRateMinor string `gorm:"type:numeric(38,0);not null" json:"displayRateMinor"`
	SliceCount       int    `gorm:"not null" json:"sliceCount"`
	// Canonical 6-dp units locked across the slices.
	OpenedUnits    string  `gorm:"type:numeric(78,0);not null" json:"openedUnits"`
	DeliveredUnits *string `gorm:"type:numeric(78,0)" json:"deliveredUnits,omitempty"`
	// Live price below what was locked (S18), canonical units.
	SurplusUnits  string  `gorm:"type:numeric(78,0);not null;default:0" json:"surplusUnits"`
	TakeKobo      *string `gorm:"type:numeric(38,0)" json:"takeKobo,omitempty"`
	LPTotalKobo   *string `gorm:"type:numeric(38,0)" json:"lpTotalKobo,omitempty"`
	PayoutFeeKobo *string `gorm:"type:numeric(38,0)" json:"payoutFeeKobo,omitempty"`
	// NetworkFeeKobo is the network fee her price carried (its fee line's
	// naira): the gas the treasury spends on the order, booked as a cost.
	NetworkFeeKobo *string `gorm:"type:numeric(38,0)" json:"networkFeeKobo,omitempty"`
	KeepKobo       *string `gorm:"type:numeric(38,0)" json:"keepKobo,omitempty"`
	// Shared-pool vault orders (spec 2026-10-14), all in the token's chain
	// units. FillUnits + SpreadUnits is what the open reserved against the
	// vault's free collateral until the fill is proven (FilledAt). FillSpreadUnits is
	// what the fill actually credited to LPs (0 when no LP held principal).
	FillUnits       *string    `gorm:"type:numeric(78,0)" json:"fillUnits,omitempty"`
	SpreadUnits     *string    `gorm:"type:numeric(78,0)" json:"spreadUnits,omitempty"`
	FillTxHash      string     `gorm:"size:66;not null;default:''" json:"fillTxHash,omitempty"`
	FillSpreadUnits *string    `gorm:"type:numeric(78,0)" json:"fillSpreadUnits,omitempty"`
	FilledAt        *time.Time `json:"filledAt,omitempty"`
	SettledAt       *time.Time `json:"settledAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (PoolOrder) TableName() string { return "pool_orders" }
