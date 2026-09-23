package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ramp_sell_intent.go — the durable record of one sale in progress (design §5.2).
//
// She signs a swap with her own key and her tokens leave her wallet. This row
// is the only thing that remembers the swap was HERS, that the quote it
// executed was OURS, and that a naira payout is owed for what landed. Every
// field below either binds the swap back to this intent (§5.3) or is a figure
// the server itself computed — nothing here is ever taken from the client at
// claim time.

// Sell-intent statuses. swapping and swapped are the open states (she is
// mid-sale); ordered and failed are terminal.
const (
	SellIntentSwapping = "swapping"
	SellIntentSwapped  = "swapped"
	SellIntentOrdered  = "ordered"
	SellIntentFailed   = "failed"
)

type RampSellIntent struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	// SourceAddress is where the swap is SENT from — her address on the
	// source chain, which is not an EVM address for a Solana sell.
	SourceAddress string `gorm:"size:128;not null" json:"sourceAddress"`
	// WalletAddress is her EVM wallet: the USDC receiver on Base, reconciled
	// against the account before the intent was opened.
	WalletAddress      string `gorm:"size:128;not null" json:"walletAddress"`
	SourceChainID      int64  `gorm:"not null" json:"sourceChainId"`
	SourceTokenAddress string `gorm:"size:128;not null" json:"sourceTokenAddress"`
	// SourceAmount is in the source token's base units, as a decimal string.
	SourceAmount string `gorm:"size:80;not null" json:"sourceAmount"`
	QuotedUSDC   string `gorm:"size:40" json:"quotedUsdc"`
	QuotedFiat   string `gorm:"size:32" json:"quotedFiat"`
	FiatCode     string `gorm:"size:8;not null" json:"currency"`
	FeeBps       int    `json:"feeBps"`
	// LiFiTransactionID is the quote's own transactionId: what proves she
	// executed OUR quote, our integrator fee included, rather than a route
	// she assembled herself. Empty for a non-EVM source, whose calldata may
	// carry none — the rest of §5.3 binds those.
	LiFiTransactionID string `gorm:"size:80" json:"-"`
	// QuotedGasLimit is the route's own gas limit, kept so a source-chain gas
	// top-up sends what the QUOTE budgeted rather than what a live gas price
	// says a minute later.
	QuotedGasLimit uint64 `json:"-"`
	// QuotedTopUpWei is the source-chain gas top-up THIS QUOTE BUDGETED, in
	// the coin it would be paid in — the wei twin of TopUpUSDForRoute, on the
	// same gas units. Stored because the budget was spent here: whatever
	// sends a top-up for this sale must send this figure, not a fresh one
	// priced off a live gas price minutes later. Empty when the route quoted
	// no gas in wei — an unknown, which is not a zero.
	QuotedTopUpWei string  `gorm:"size:40" json:"-"`
	QuotedFromUSD  float64 `json:"-"` // the quote's fromAmountUSD, the basis our fee is booked on
	BankCode       string  `gorm:"size:16;not null" json:"bankCode"`
	AccountNumber  string  `gorm:"size:32;not null" json:"accountNumber"`
	AccountName    string  `gorm:"size:128" json:"accountName,omitempty"`
	// SwapTxHash is a POINTER so the unique index ignores intents that have
	// no swap yet: one on-chain swap can back exactly one sale, and a hash
	// already claimed elsewhere must not open a second payout.
	SwapTxHash      *string    `gorm:"size:128;uniqueIndex" json:"swapTxHash,omitempty"`
	LandedUSDC      string     `gorm:"size:40" json:"landedUsdc,omitempty"`
	PaycrestOrderID *uuid.UUID `gorm:"type:uuid" json:"paycrestOrderId,omitempty"`
	ToppedUpAt      *time.Time `json:"-"`
	Status          string     `gorm:"size:16;not null;index" json:"status"`
	Error           string     `gorm:"size:512" json:"error,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (i *RampSellIntent) BeforeCreate(tx *gorm.DB) error {
	if i.ID == uuid.Nil {
		i.ID = uuid.New()
	}
	return nil
}

// Open reports an intent still in progress.
func (i *RampSellIntent) Open() bool {
	return i.Status == SellIntentSwapping || i.Status == SellIntentSwapped
}
