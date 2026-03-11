package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// WalletAddress represents a blockchain address associated with the wallet.
type WalletAddress struct {
	Blockchain string     `bson:"blockchain,omitempty" json:"blockchain,omitempty"`
	Address    string     `bson:"address,omitempty" json:"address,omitempty"`
	IsActive   bool       `bson:"isActive" json:"isActive"`
	IsExternal bool       `bson:"isExternal" json:"isExternal"`
	LastSynced *time.Time `bson:"lastSynced,omitempty" json:"lastSynced,omitempty"`
	Nonce      int        `bson:"nonce" json:"nonce"`
}

// WithdrawalWhitelistEntry represents a whitelisted withdrawal address.
type WithdrawalWhitelistEntry struct {
	Address    string `bson:"address,omitempty" json:"address,omitempty"`
	Blockchain string `bson:"blockchain,omitempty" json:"blockchain,omitempty"`
	Label      string `bson:"label,omitempty" json:"label,omitempty"`
}

// WalletSecurity holds security settings for the wallet.
type WalletSecurity struct {
	WithdrawalWhitelist []WithdrawalWhitelistEntry `bson:"withdrawalWhitelist,omitempty" json:"withdrawalWhitelist,omitempty"`
	DailyLimit          float64                    `bson:"dailyLimit" json:"dailyLimit"`
	TransactionLimit    float64                    `bson:"transactionLimit" json:"transactionLimit"`
	TwoFactorEnabled    bool                       `bson:"twoFactorEnabled" json:"twoFactorEnabled"`
}

// WalletSettings holds user preferences for the wallet.
type WalletSettings struct {
	DefaultCurrency string `bson:"defaultCurrency,omitempty" json:"defaultCurrency,omitempty"`
	HideBalances    bool   `bson:"hideBalances" json:"hideBalances"`
	PriceAlerts     bool   `bson:"priceAlerts" json:"priceAlerts"`
	AutoSync        bool   `bson:"autoSync" json:"autoSync"`
}

// Wallet represents a user's wallet.
type Wallet struct {
	ID               primitive.ObjectID  `bson:"_id,omitempty" json:"id,omitempty"`
	UserID           primitive.ObjectID  `bson:"userId" json:"userId"`
	TotalBalance     float64             `bson:"totalBalance" json:"totalBalance"`
	TotalUSDValue    float64             `bson:"totalUsdValue" json:"totalUsdValue"`
	SelectedAsset    *primitive.ObjectID `bson:"selectedAsset,omitempty" json:"selectedAsset,omitempty"`
	Addresses        []WalletAddress     `bson:"addresses,omitempty" json:"addresses,omitempty"`
	Security         WalletSecurity      `bson:"security" json:"security"`
	Settings         WalletSettings      `bson:"settings" json:"settings"`
	TransactionLimit float64             `bson:"transactionLimit" json:"transactionLimit"`
	DailyLimit       float64             `bson:"dailyLimit" json:"dailyLimit"`
	LastSynced       *time.Time          `bson:"lastSynced,omitempty" json:"lastSynced,omitempty"`
	CreatedAt        time.Time           `bson:"createdAt" json:"createdAt"`
	UpdatedAt        time.Time           `bson:"updatedAt" json:"updatedAt"`
}

// Wallet security default constants.
const (
	DefaultDailyLimit       = 10000.0
	DefaultTransactionLimit = 1000.0
)
