package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TransactionAsset represents the from/to asset in a transaction.
type TransactionAsset struct {
	Symbol  string `bson:"symbol,omitempty" json:"symbol,omitempty"`
	Amount  float64 `bson:"amount" json:"amount"`
	Address string `bson:"address,omitempty" json:"address,omitempty"`
}

// TransactionFees holds fee information for a transaction.
type TransactionFees struct {
	Network  float64 `bson:"network" json:"network"`
	Platform float64 `bson:"platform" json:"platform"`
}

// TransactionMetadata holds additional transaction details.
type TransactionMetadata struct {
	SenderAddress      string  `bson:"senderAddress,omitempty" json:"senderAddress,omitempty"`
	ReceiverAddress    string  `bson:"receiverAddress,omitempty" json:"receiverAddress,omitempty"`
	Memo               string  `bson:"memo,omitempty" json:"memo,omitempty"`
	ExchangeRate       float64 `bson:"exchangeRate,omitempty" json:"exchangeRate,omitempty"`
	GasUsed            int64   `bson:"gasUsed,omitempty" json:"gasUsed,omitempty"`
	GasPrice           float64 `bson:"gasPrice,omitempty" json:"gasPrice,omitempty"`
	ConfirmationBlocks int     `bson:"confirmationBlocks,omitempty" json:"confirmationBlocks,omitempty"`
	Notes              string  `bson:"notes,omitempty" json:"notes,omitempty"`
}

// Transaction represents a financial transaction.
type Transaction struct {
	ID               primitive.ObjectID   `bson:"_id,omitempty" json:"id,omitempty"`
	UserID           primitive.ObjectID   `bson:"userId" json:"userId"`
	Type             string               `bson:"type" json:"type"`
	Status           string               `bson:"status" json:"status"`
	AssetSymbol      string               `bson:"assetSymbol,omitempty" json:"assetSymbol,omitempty"`
	FromAsset        interface{}          `bson:"fromAsset,omitempty" json:"fromAsset,omitempty"`
	ToAsset          interface{}          `bson:"toAsset,omitempty" json:"toAsset,omitempty"`
	FromAmount       float64              `bson:"fromAmount,omitempty" json:"fromAmount,omitempty"`
	ToAmount         float64              `bson:"toAmount,omitempty" json:"toAmount,omitempty"`
	ExchangeRate     float64              `bson:"exchangeRate,omitempty" json:"exchangeRate,omitempty"`
	Amount           float64              `bson:"amount" json:"amount"`
	USDValue         float64              `bson:"usdValue,omitempty" json:"usdValue,omitempty"`
	Fee              float64              `bson:"fee,omitempty" json:"fee,omitempty"`
	FeeUSD           float64              `bson:"feeUsd,omitempty" json:"feeUsd,omitempty"`
	PlatformFee      float64              `bson:"platformFee,omitempty" json:"platformFee,omitempty"`
	NetworkFee       float64              `bson:"networkFee,omitempty" json:"networkFee,omitempty"`
	Fees             *TransactionFees     `bson:"fees,omitempty" json:"fees,omitempty"`
	Network          string               `bson:"network,omitempty" json:"network,omitempty"`
	WalletAddress    string               `bson:"walletAddress,omitempty" json:"walletAddress,omitempty"`
	TransactionHash  string               `bson:"transactionHash,omitempty" json:"transactionHash,omitempty"`
	TxHash           string               `bson:"txHash,omitempty" json:"txHash,omitempty"`
	Blockchain       string               `bson:"blockchain,omitempty" json:"blockchain,omitempty"`
	Confirmations    int                  `bson:"confirmations,omitempty" json:"confirmations,omitempty"`
	RequiredConfirms int                  `bson:"requiredConfirms,omitempty" json:"requiredConfirms,omitempty"`
	Metadata         *TransactionMetadata `bson:"metadata,omitempty" json:"metadata,omitempty"`
	Description      string               `bson:"description,omitempty" json:"description,omitempty"`
	Tags             []string             `bson:"tags,omitempty" json:"tags,omitempty"`
	CompletedAt      *time.Time           `bson:"completedAt,omitempty" json:"completedAt,omitempty"`
	ConfirmedAt      *time.Time           `bson:"confirmedAt,omitempty" json:"confirmedAt,omitempty"`
	CreatedAt        time.Time            `bson:"createdAt" json:"createdAt"`
	UpdatedAt        time.Time            `bson:"updatedAt" json:"updatedAt"`
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
