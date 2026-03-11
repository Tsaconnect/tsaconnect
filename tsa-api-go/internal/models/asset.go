package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// AssetDetails contains technical details about an asset.
type AssetDetails struct {
	Type            string `json:"type,omitempty"`
	Chain           string `json:"chain,omitempty"`
	ContractAddress string `json:"contractAddress,omitempty"`
	Decimals        int    `json:"decimals"`
	IconURL         string `json:"iconUrl,omitempty"`
	Color           string `json:"color,omitempty"`
}

// AssetMetadata contains staking and reward metadata for an asset.
type AssetMetadata struct {
	APY               float64    `json:"apy,omitempty"`
	StakedAmount      float64    `json:"stakedAmount,omitempty"`
	UnstakingDate     *time.Time `json:"unstakingDate,omitempty"`
	LockPeriod        int        `json:"lockPeriod,omitempty"`
	LastRewardClaimed *time.Time `json:"lastRewardClaimed,omitempty"`
}

// AssetPerformance tracks price change percentages over various periods.
type AssetPerformance struct {
	DailyChange   float64 `json:"dailyChange,omitempty"`
	WeeklyChange  float64 `json:"weeklyChange,omitempty"`
	MonthlyChange float64 `json:"monthlyChange,omitempty"`
}

// Asset represents a digital asset owned by a user.
type Asset struct {
	ID          uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID      uuid.UUID      `gorm:"type:uuid;index" json:"userId"`
	Symbol      string         `json:"symbol"`
	Name        string         `json:"name"`
	Balance     float64        `json:"balance"`
	USDValue    float64        `json:"usdValue"`
	IsSelected  bool           `json:"isSelected"`
	IsHidden    bool           `json:"isHidden"`
	Details     datatypes.JSON `gorm:"type:jsonb" json:"details,omitempty"`
	Metadata    datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"`
	Performance datatypes.JSON `gorm:"type:jsonb" json:"performance,omitempty"`
	LastSynced  *time.Time     `json:"lastSynced,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

// TableName overrides the default table name.
func (Asset) TableName() string {
	return "assets"
}

// Asset detail type constants.
const (
	AssetTypeToken      = "token"
	AssetTypeStablecoin = "stablecoin"
	AssetTypeGoldBacked = "gold-backed"
	AssetTypeNFT        = "nft"
	AssetTypeOther      = "other"

	DefaultDecimals = 18
)

// Asset chain constants.
const (
	ChainEthereum = "ethereum"
	ChainPolygon  = "polygon"
	ChainBinance  = "binance"
	ChainSolana   = "solana"
	ChainOther    = "other"
)

// GetDetails deserializes the Details JSONB field.
func (a *Asset) GetDetails() *AssetDetails {
	if a.Details == nil {
		return nil
	}
	var d AssetDetails
	if err := json.Unmarshal(a.Details, &d); err != nil {
		return nil
	}
	return &d
}

// SetDetails serializes the AssetDetails struct into the JSONB field.
func (a *Asset) SetDetails(d *AssetDetails) {
	if d == nil {
		a.Details = nil
		return
	}
	data, _ := json.Marshal(d)
	a.Details = data
}

// GetPerformance deserializes the Performance JSONB field.
func (a *Asset) GetPerformance() *AssetPerformance {
	if a.Performance == nil {
		return nil
	}
	var p AssetPerformance
	if err := json.Unmarshal(a.Performance, &p); err != nil {
		return nil
	}
	return &p
}

// SetPerformance serializes the AssetPerformance struct into the JSONB field.
func (a *Asset) SetPerformance(p *AssetPerformance) {
	if p == nil {
		a.Performance = nil
		return
	}
	data, _ := json.Marshal(p)
	a.Performance = data
}
