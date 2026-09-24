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

// Sell-intent statuses. swapping, swapped and ordering are the open states
// (she is mid-sale); ordered, delivered and failed are terminal.
//
// ordering is the claim on the row while its Paycrest order is being opened:
// exactly one caller moves swapped -> ordering, so exactly one offramp is
// opened for the USDC that landed.
//
// delivered is the terminal state of a sale that pays a token to someone
// else's wallet (TSA Pay's crypto route, DestAddress set) once the chain has
// proven the recipient was credited in full. It is the twin of ordered, which
// a delivery never reaches: there is no Paycrest order to open.
const (
	SellIntentSwapping  = "swapping"
	SellIntentSwapped   = "swapped"
	SellIntentOrdering  = "ordering"
	SellIntentOrdered   = "ordered"
	SellIntentDelivered = "delivered"
	SellIntentFailed    = "failed"
)

// SellIntentOpenStatuses are the statuses of a sale still in progress — the
// set the one-open-sale index is partial over. delivered is terminal, so it is
// not here: a customer whose payment was delivered is free to sell again.
var SellIntentOpenStatuses = []string{SellIntentSwapping, SellIntentSwapped, SellIntentOrdering}

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
	// PayPaymentID and PayoutFiat mark a sale that pays SOMEONE ELSE: a
	// TSA Pay checkout. PayoutFiat is the exact naira the recipient is owed.
	PayPaymentID *uuid.UUID `gorm:"type:uuid;index" json:"payPaymentId,omitempty"`
	PayoutFiat   string     `gorm:"size:32" json:"payoutFiat,omitempty"`
	// DestChainID, DestTokenAddress, DestAddress and DestAmount mark a sale that
	// DELIVERS a token to someone else's wallet instead of opening an offramp:
	// TSA Pay's crypto route. DestAmount is the least the recipient must be
	// credited, in the token's base units; the chain, not LiFi, decides whether
	// it was. All empty for an ordinary sale, whose destination is USDC on Base
	// in her own wallet (see Dest).
	DestChainID      int64  `gorm:"not null;default:0" json:"destChainId,omitempty"`
	DestTokenAddress string `gorm:"size:128" json:"destTokenAddress,omitempty"`
	DestAddress      string `gorm:"size:128" json:"destAddress,omitempty"`
	DestAmount       string `gorm:"size:80" json:"destAmount,omitempty"`
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
	QuotedTopUpWei string `gorm:"size:40" json:"-"`
	// TopUpBudgeted is whether the quote priced a top-up into the budget it
	// admitted this sale on. False means none was paid for, so none may be
	// sent — whatever QuotedTopUpWei says. Kept apart from QuotedTopUpWei
	// because "" there already means "budgeted, but the route quoted no wei".
	TopUpBudgeted bool    `gorm:"not null;default:false" json:"-"`
	QuotedFromUSD float64 `json:"-"` // the quote's fromAmountUSD, the basis our fee is booked on
	BankCode      string  `gorm:"size:16;not null" json:"bankCode"`
	AccountNumber string  `gorm:"size:32;not null" json:"accountNumber"`
	AccountName   string  `gorm:"size:128" json:"accountName,omitempty"`
	// SwapTxHash is a POINTER so the unique index ignores intents that have
	// no swap yet: one on-chain swap can back exactly one sale, and a hash
	// already claimed elsewhere must not open a second payout.
	SwapTxHash *string `gorm:"size:128;uniqueIndex" json:"swapTxHash,omitempty"`
	LandedUSDC string  `gorm:"size:40" json:"landedUsdc,omitempty"`
	// DeliverySeenAt is when a delivery sale's swap was first seen COMPLETED: the
	// start of the wait for the destination receipt to be readable (RPC lag).
	// DestTxHash is the destination transaction that proved the delivery. Its
	// unique index (NULLs ignored, like SwapTxHash's) means one destination
	// transaction can prove one sale: a batched relayer transaction that credits
	// the same merchant for two customers cannot satisfy both.
	DeliverySeenAt  *time.Time `json:"-"`
	DestTxHash      *string    `gorm:"size:128;uniqueIndex" json:"-"`
	PaycrestOrderID *uuid.UUID `gorm:"type:uuid" json:"paycrestOrderId,omitempty"`
	ToppedUpAt      *time.Time `json:"-"`
	// TopUpTxHash / TopUpWei are the source-chain gas top-up actually sent
	// for this sale: its transaction and the wei it moved (the shortfall, not
	// the budget). The order path's sponsorship row carries the same two
	// figures; these are the sell side's, for reconciling platform gas.
	// Empty while nothing has been sent, and after a broadcast whose outcome
	// was unknown (topped_up_at is still set then).
	TopUpTxHash string    `gorm:"size:128" json:"-"`
	TopUpWei    string    `gorm:"size:80" json:"-"`
	Status      string    `gorm:"size:16;not null;index" json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (i *RampSellIntent) BeforeCreate(tx *gorm.DB) error {
	if i.ID == uuid.Nil {
		i.ID = uuid.New()
	}
	return nil
}

// WithBankDetailsCleared adds, to a column update, the blanking of the payout
// destination she typed in. A sale that ended (failed) or handed off to its
// Paycrest order (ordered — the order keeps its own copy) no longer needs
// her bank account, so it is erased in the same write as the status change.
// Blank, not NULL: the columns are NOT NULL, and a schema change is not
// needed to forget a value.
func WithBankDetailsCleared(updates map[string]any) map[string]any {
	updates["bank_code"] = ""
	updates["account_number"] = ""
	updates["account_name"] = ""
	return updates
}

// Open reports an intent still in progress.
func (i *RampSellIntent) Open() bool {
	for _, st := range SellIntentOpenStatuses {
		if i.Status == st {
			return true
		}
	}
	return false
}

// IsDelivery reports a sale that hands a token to someone else's wallet
// (TSA Pay's crypto route) instead of opening an offramp.
func (i *RampSellIntent) IsDelivery() bool { return i.DestAddress != "" }

// IsNairaPay reports a sale that pays an exact naira amount to someone else's
// bank (TSA Pay's naira route).
func (i *RampSellIntent) IsNairaPay() bool { return i.PayoutFiat != "" }

// Dest is where this sale's swap must deliver: the chain, token and receiver
// the binding check compares LiFi's status against. An ordinary sale names none
// of them and lands baseUSDC on baseChainID in her own wallet.
func (i *RampSellIntent) Dest(baseChainID int64, baseUSDC string) (chainID int64, token, address string) {
	chainID, token, address = i.DestChainID, i.DestTokenAddress, i.DestAddress
	if chainID == 0 {
		chainID = baseChainID
	}
	if token == "" {
		token = baseUSDC
	}
	if address == "" {
		address = i.WalletAddress
	}
	return chainID, token, address
}
