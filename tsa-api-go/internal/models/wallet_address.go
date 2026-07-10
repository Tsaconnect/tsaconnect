package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// WalletAddress is a public per-chain receive address for a given wallet.
// Keyed uniquely on (UserID, WalletID, ChainKey): scoping the unique index to
// the owner respects the app's multi-wallet reality AND prevents a cross-user
// lockout — since WalletID is a public EVM address, a two-column (WalletID,
// ChainKey) unique index would let anyone pre-register a victim's address and
// block the victim's own later registration.
type WalletAddress struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_wallet_chain" json:"userId"`
	WalletID  string    `gorm:"size:64;not null;uniqueIndex:idx_wallet_chain" json:"walletId"` // the EVM address that anchors the mnemonic
	ChainKey  string    `gorm:"size:16;not null;uniqueIndex:idx_wallet_chain" json:"chainKey"`
	Family    string    `gorm:"size:16;not null" json:"family"`
	Address   string    `gorm:"size:128;not null;index" json:"address"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (WalletAddress) TableName() string { return "wallet_addresses" }

// BeforeCreate assigns a UUID when one isn't set, so inserts work regardless of
// the DB's default (Postgres gen_random_uuid() vs SQLite in tests / seeds).
func (w *WalletAddress) BeforeCreate(tx *gorm.DB) error {
	if w.ID == uuid.Nil {
		w.ID = uuid.New()
	}
	return nil
}
