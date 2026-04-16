package models

import (
	"time"

	"github.com/google/uuid"
)

// WalletTransaction represents an on-chain transaction record for a user's wallet.
type WalletTransaction struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID      uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	TxHash      string    `gorm:"uniqueIndex;not null" json:"txHash"`
	TokenSymbol string    `gorm:"not null" json:"tokenSymbol"` // MCGP, USDT, USDC, S
	TxType      string    `gorm:"not null" json:"txType"`      // send, receive, approve, escrow, swap
	FromAddress string    `gorm:"not null" json:"fromAddress"`
	ToAddress   string    `gorm:"not null" json:"toAddress"`
	Amount      string    `gorm:"not null" json:"amount"`                   // stored as string to avoid precision loss
	Status      string    `gorm:"not null;default:'pending'" json:"status"` // pending, confirmed, failed
	Chain       string    `gorm:"not null;default:'sonic'" json:"chain"`    // sonic, bsc
	ChainID     int64     `gorm:"not null;default:14601" json:"chainId"`
	BlockNumber *int64    `json:"blockNumber,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (WalletTransaction) TableName() string {
	return "wallet_transactions"
}

// Constants for wallet transaction types.
const (
	TxTypeSend    = "send"
	TxTypeReceive = "receive"
	TxTypeApprove = "approve"
	TxTypeEscrow  = "escrow"
	TxTypeSwap    = "swap"
)

// Constants for wallet transaction statuses.
const (
	TxStatusPending   = "pending"
	TxStatusConfirmed = "confirmed"
	TxStatusFailed    = "failed"
)

// Supported token symbols.
var SupportedTokens = map[string]bool{
	"MCGP": true,
	"USDT": true,
	"USDC": true,
	"S":    true,
}
