package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// TransactionAsset represents the from/to asset in a transaction.
type TransactionAsset struct {
	Symbol  string  `json:"symbol,omitempty"`
	Amount  float64 `json:"amount"`
	Address string  `json:"address,omitempty"`
}

// TransactionFees holds fee information for a transaction.
type TransactionFees struct {
	Network  float64 `json:"network"`
	Platform float64 `json:"platform"`
}

// TransactionMetadata holds additional transaction details.
type TransactionMetadata struct {
	SenderAddress      string  `json:"senderAddress,omitempty"`
	ReceiverAddress    string  `json:"receiverAddress,omitempty"`
	Memo               string  `json:"memo,omitempty"`
	ExchangeRate       float64 `json:"exchangeRate,omitempty"`
	GasUsed            int64   `json:"gasUsed,omitempty"`
	GasPrice           float64 `json:"gasPrice,omitempty"`
	ConfirmationBlocks int     `json:"confirmationBlocks,omitempty"`
	Notes              string  `json:"notes,omitempty"`
}

// Transaction represents a financial transaction.
type Transaction struct {
	ID               uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID           uuid.UUID      `gorm:"type:uuid;index" json:"userId"`
	Type             string         `json:"type"`
	Status           string         `json:"status"`
	AssetSymbol      string         `json:"assetSymbol,omitempty"`
	FromAsset        datatypes.JSON `gorm:"type:jsonb" json:"fromAsset,omitempty"`
	ToAsset          datatypes.JSON `gorm:"type:jsonb" json:"toAsset,omitempty"`
	FromAmount       float64        `json:"fromAmount,omitempty"`
	ToAmount         float64        `json:"toAmount,omitempty"`
	ExchangeRate     float64        `json:"exchangeRate,omitempty"`
	Amount           float64        `json:"amount"`
	USDValue         float64        `json:"usdValue,omitempty"`
	Fee              float64        `json:"fee,omitempty"`
	FeeUSD           float64        `json:"feeUsd,omitempty"`
	PlatformFee      float64        `json:"platformFee,omitempty"`
	NetworkFee       float64        `json:"networkFee,omitempty"`
	Fees             datatypes.JSON `gorm:"type:jsonb" json:"fees,omitempty"`
	Network          string         `json:"network,omitempty"`
	WalletAddress    string         `json:"walletAddress,omitempty"`
	TransactionHash  string         `json:"transactionHash,omitempty"`
	TxHash           string         `json:"txHash,omitempty"`
	Blockchain       string         `json:"blockchain,omitempty"`
	Confirmations    int            `json:"confirmations,omitempty"`
	RequiredConfirms int            `json:"requiredConfirms,omitempty"`
	Metadata         datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"`
	Description      string         `json:"description,omitempty"`
	Tags             datatypes.JSON `gorm:"type:jsonb" json:"tags,omitempty"`
	CompletedAt      *time.Time     `json:"completedAt,omitempty"`
	ConfirmedAt      *time.Time     `json:"confirmedAt,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}

// TableName overrides the default table name.
func (Transaction) TableName() string {
	return "transactions"
}

// TransactionStats holds aggregated transaction statistics.
type TransactionStats struct {
	TotalTransactions int     `json:"totalTransactions"`
	TotalDeposits     float64 `json:"totalDeposits"`
	TotalWithdrawals  float64 `json:"totalWithdrawals"`
	TotalSwaps        float64 `json:"totalSwaps"`
	TotalFees         float64 `json:"totalFees"`
	NetFlow           float64 `json:"netFlow"`
}

// Transaction type constants.
const (
	TransactionTypeDeposit    = "deposit"
	TransactionTypeWithdrawal = "withdrawal"
	TransactionTypeTransfer   = "transfer"
	TransactionTypeSwap       = "swap"
	TransactionTypeTrade      = "trade"
	TransactionTypeReward     = "reward"
	TransactionTypeStaking    = "staking"
)

// Transaction status constants.
const (
	TransactionStatusPending    = "pending"
	TransactionStatusCompleted  = "completed"
	TransactionStatusFailed     = "failed"
	TransactionStatusCancelled  = "cancelled"
	TransactionStatusProcessing = "processing"
)

// Transaction blockchain constants.
const (
	BlockchainEthereum = "ethereum"
	BlockchainPolygon  = "polygon"
	BlockchainBinance  = "binance"
	BlockchainSolana   = "solana"
	BlockchainInternal = "internal"
)

// GetFromAsset deserializes the FromAsset JSONB field.
func (t *Transaction) GetFromAsset() *TransactionAsset {
	if t.FromAsset == nil {
		return nil
	}
	var a TransactionAsset
	if err := json.Unmarshal(t.FromAsset, &a); err != nil {
		return nil
	}
	return &a
}

// SetFromAsset serializes the TransactionAsset struct into the FromAsset JSONB field.
func (t *Transaction) SetFromAsset(a *TransactionAsset) {
	if a == nil {
		t.FromAsset = nil
		return
	}
	data, _ := json.Marshal(a)
	t.FromAsset = data
}

// GetToAsset deserializes the ToAsset JSONB field.
func (t *Transaction) GetToAsset() *TransactionAsset {
	if t.ToAsset == nil {
		return nil
	}
	var a TransactionAsset
	if err := json.Unmarshal(t.ToAsset, &a); err != nil {
		return nil
	}
	return &a
}

// SetToAsset serializes the TransactionAsset struct into the ToAsset JSONB field.
func (t *Transaction) SetToAsset(a *TransactionAsset) {
	if a == nil {
		t.ToAsset = nil
		return
	}
	data, _ := json.Marshal(a)
	t.ToAsset = data
}
