package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// WalletActivity is a cached, normalized non-EVM transaction history row.
type WalletActivity struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	ChainKey      string    `gorm:"size:16;not null;uniqueIndex:idx_chain_hash_dir" json:"chainKey"`
	Network       string    `gorm:"size:16;not null;default:'mainnet'" json:"network"` // which chain network the tx is on
	Address       string    `gorm:"size:128;not null;index" json:"address"`
	Hash          string    `gorm:"size:128;not null;uniqueIndex:idx_chain_hash_dir" json:"hash"`
	Direction     string    `gorm:"size:4;not null;uniqueIndex:idx_chain_hash_dir" json:"direction"` // "in" | "out"
	AmountRaw     string    `gorm:"type:numeric(78,0);not null" json:"amountRaw"`
	Symbol        string    `gorm:"size:16;not null" json:"symbol"`
	Counterparty  string    `gorm:"size:128" json:"counterparty"`
	Status        string    `gorm:"size:16;not null;default:'pending'" json:"status"`
	Confirmations int64     `gorm:"not null;default:0" json:"confirmations"`
	BlockTime     int64     `gorm:"index" json:"blockTime"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (WalletActivity) TableName() string { return "wallet_activity" }

// BeforeCreate assigns a UUID when one isn't set, so inserts work regardless of
// the DB's default (Postgres gen_random_uuid() vs SQLite in tests / seeds).
func (w *WalletActivity) BeforeCreate(tx *gorm.DB) error {
	if w.ID == uuid.Nil {
		w.ID = uuid.New()
	}
	return nil
}
