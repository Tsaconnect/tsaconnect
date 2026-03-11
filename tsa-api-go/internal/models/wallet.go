package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// WalletAddress represents a blockchain address associated with the wallet.
type WalletAddress struct {
	Blockchain string     `json:"blockchain,omitempty"`
	Address    string     `json:"address,omitempty"`
	IsActive   bool       `json:"isActive"`
	IsExternal bool       `json:"isExternal"`
	LastSynced *time.Time `json:"lastSynced,omitempty"`
	Nonce      int        `json:"nonce"`
}

// WithdrawalWhitelistEntry represents a whitelisted withdrawal address.
type WithdrawalWhitelistEntry struct {
	Address    string `json:"address,omitempty"`
	Blockchain string `json:"blockchain,omitempty"`
	Label      string `json:"label,omitempty"`
}

// WalletSecurity holds security settings for the wallet.
type WalletSecurity struct {
	WithdrawalWhitelist []WithdrawalWhitelistEntry `json:"withdrawalWhitelist,omitempty"`
	DailyLimit          float64                    `json:"dailyLimit"`
	TransactionLimit    float64                    `json:"transactionLimit"`
	TwoFactorEnabled    bool                       `json:"twoFactorEnabled"`
}

// WalletSettings holds user preferences for the wallet.
type WalletSettings struct {
	DefaultCurrency string `json:"defaultCurrency,omitempty"`
	HideBalances    bool   `json:"hideBalances"`
	PriceAlerts     bool   `json:"priceAlerts"`
	AutoSync        bool   `json:"autoSync"`
}

// Wallet represents a user's wallet.
type Wallet struct {
	ID               uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID           uuid.UUID      `gorm:"type:uuid;uniqueIndex" json:"userId"`
	TotalBalance     float64        `json:"totalBalance"`
	TotalUSDValue    float64        `json:"totalUsdValue"`
	SelectedAsset    string         `json:"selectedAsset,omitempty"`
	Addresses        datatypes.JSON `gorm:"type:jsonb" json:"addresses,omitempty"`
	Security         datatypes.JSON `gorm:"type:jsonb" json:"security"`
	Settings         datatypes.JSON `gorm:"type:jsonb" json:"settings"`
	TransactionLimit float64        `json:"transactionLimit"`
	DailyLimit       float64        `json:"dailyLimit"`
	LastSynced       *time.Time     `json:"lastSynced,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}

// TableName overrides the default table name.
func (Wallet) TableName() string {
	return "wallets"
}

// Wallet security default constants.
const (
	DefaultDailyLimit       = 10000.0
	DefaultTransactionLimit = 1000.0
)
