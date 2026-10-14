package models

import (
	"time"

	"github.com/google/uuid"
)

// LiquidityLedgerEntry is one event of the shared-pool vault (spec 2026-10-14
// §4.4), indexed from the chain once it is final. The vault is the source of
// truth; this is its readable history (LP activity, earnings, the reconciler).
// Amounts are the token's chain units.
type LiquidityLedgerEntry struct {
	ID       uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	ChainID  int64     `gorm:"not null;uniqueIndex:uq_liquidity_ledger_log,priority:1" json:"chainId"`
	Vault    string    `gorm:"size:42;not null;index" json:"vault"`
	TxHash   string    `gorm:"size:66;not null;uniqueIndex:uq_liquidity_ledger_log,priority:2" json:"txHash"`
	LogIndex uint      `gorm:"not null;uniqueIndex:uq_liquidity_ledger_log,priority:3" json:"logIndex"`
	Block    uint64    `gorm:"not null;index" json:"block"`
	// Kind is one of the services.VaultEvent* kinds.
	Kind    string  `gorm:"size:32;not null;index" json:"kind"`
	Account string  `gorm:"size:42;index" json:"account,omitempty"` // the LP (or depositor named)
	To      string  `gorm:"size:42" json:"to,omitempty"`
	Amount  *string `gorm:"type:numeric(78,0)" json:"amount,omitempty"`
	Spread  *string `gorm:"type:numeric(78,0)" json:"spread,omitempty"`
	Ref     string  `gorm:"size:66;index" json:"ref,omitempty"`
	Allowed *bool   `json:"allowed,omitempty"`
	// BlockTime is the block's timestamp: earnings history and APR windows.
	BlockTime time.Time `gorm:"not null;index" json:"blockTime"`
	CreatedAt time.Time `json:"createdAt"`
}

func (LiquidityLedgerEntry) TableName() string { return "liquidity_ledger" }

// LiquidityIndexCursor is where the vault indexer resumes: every block below
// NextBlock is in the ledger.
type LiquidityIndexCursor struct {
	ChainID   int64     `gorm:"primaryKey" json:"chainId"`
	Vault     string    `gorm:"primaryKey;size:42" json:"vault"`
	NextBlock uint64    `gorm:"not null" json:"nextBlock"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (LiquidityIndexCursor) TableName() string { return "liquidity_index_cursors" }
