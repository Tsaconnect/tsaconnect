package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// AssetDetails contains technical details about an asset.
type AssetDetails struct {
	Type            string `bson:"type,omitempty" json:"type,omitempty"`
	Chain           string `bson:"chain,omitempty" json:"chain,omitempty"`
	ContractAddress string `bson:"contractAddress,omitempty" json:"contractAddress,omitempty"`
	Decimals        int    `bson:"decimals" json:"decimals"`
	IconURL         string `bson:"iconUrl,omitempty" json:"iconUrl,omitempty"`
	Color           string `bson:"color,omitempty" json:"color,omitempty"`
}

// AssetMetadata contains staking and reward metadata for an asset.
type AssetMetadata struct {
	APY              float64    `bson:"apy,omitempty" json:"apy,omitempty"`
	StakedAmount     float64    `bson:"stakedAmount,omitempty" json:"stakedAmount,omitempty"`
	UnstakingDate    *time.Time `bson:"unstakingDate,omitempty" json:"unstakingDate,omitempty"`
	LockPeriod       int        `bson:"lockPeriod,omitempty" json:"lockPeriod,omitempty"`
	LastRewardClaimed *time.Time `bson:"lastRewardClaimed,omitempty" json:"lastRewardClaimed,omitempty"`
}

// AssetPerformance tracks price change percentages over various periods.
type AssetPerformance struct {
	DailyChange   float64 `bson:"dailyChange,omitempty" json:"dailyChange,omitempty"`
	WeeklyChange  float64 `bson:"weeklyChange,omitempty" json:"weeklyChange,omitempty"`
	MonthlyChange float64 `bson:"monthlyChange,omitempty" json:"monthlyChange,omitempty"`
}

// Asset represents a digital asset owned by a user.
type Asset struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	UserID      primitive.ObjectID `bson:"userId" json:"userId"`
	Symbol      string             `bson:"symbol" json:"symbol"`
	Name        string             `bson:"name" json:"name"`
	Balance     float64            `bson:"balance" json:"balance"`
	USDValue    float64            `bson:"usdValue" json:"usdValue"`
	IsSelected  bool               `bson:"isSelected" json:"isSelected"`
	IsHidden    bool               `bson:"isHidden" json:"isHidden"`
	Details     *AssetDetails      `bson:"details,omitempty" json:"details,omitempty"`
	Metadata    *AssetMetadata     `bson:"metadata,omitempty" json:"metadata,omitempty"`
	Performance *AssetPerformance  `bson:"performance,omitempty" json:"performance,omitempty"`
	LastSynced  *time.Time         `bson:"lastSynced,omitempty" json:"lastSynced,omitempty"`
	CreatedAt   time.Time          `bson:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time          `bson:"updatedAt" json:"updatedAt"`
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
