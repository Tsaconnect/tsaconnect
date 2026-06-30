package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// IT (Instant Transfer) — buy/sell MCGP with Naira via the company PM, routed
// through Contract B's spend-only premiumCredited bucket.

// IT intent directions + statuses.
const (
	ITDirectionBuy  = "buy"  // NGN in  -> creditPremium
	ITDirectionSell = "sell" // debitPremium -> NGN out

	ITStatusPendingPayment = "pending_payment" // buy: awaiting the user's NGN transfer
	ITStatusProcessing     = "processing"      // operator on-chain op in flight
	ITStatusCredited       = "credited"        // buy complete (premium credited)
	ITStatusDebited        = "debited"         // sell: on-chain debit done, NGN payout in flight
	ITStatusSettled        = "settled"         // sell complete (NGN paid, backing freed)
	ITStatusReversed       = "reversed"        // sell payout failed, premium restored
	ITStatusFailed         = "failed"
	ITStatusCancelled      = "cancelled"
)

// ITIntent is one IT buy or sell. Amounts are base-unit decimal strings (MCGP
// 18dp wei; NGN in kobo). The rate is locked at creation.
type ITIntent struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`

	Direction string `gorm:"size:8;not null;index" json:"direction"`
	Status    string `gorm:"size:20;not null;default:'pending_payment';index" json:"status"`

	// WalletAddress is captured at intent creation and is the address the on-chain
	// credit/debit targets — used for event verification so a later wallet
	// rotation can't strand the intent (review M2).
	WalletAddress string `gorm:"size:42" json:"walletAddress"`

	McgpAmount string `gorm:"type:numeric(78,0);not null" json:"mcgpAmount"` // wei
	NgnKobo    string `gorm:"type:numeric(38,0);not null" json:"ngnKobo"`    // naira*100
	RateKobo   string `gorm:"type:numeric(38,0);not null" json:"rateKobo"`   // kobo per 1 MCGP, locked

	// PaymentRef is the strong NGN attribution key the user includes in their
	// transfer (narration / dedicated VA reference). Unique. Buy only.
	PaymentRef string `gorm:"size:64;index" json:"paymentRef,omitempty"`
	// DebitRef is the on-chain bytes32 ref (hex) for the sell two-phase debit.
	DebitRef string `gorm:"size:66;index" json:"debitRef,omitempty"`

	TxHash        string `gorm:"size:66" json:"txHash,omitempty"`
	FailureReason string `gorm:"size:256" json:"failureReason,omitempty"`

	// PMID is the company MCGP Payment Merchant this intent settles against.
	PMID *uuid.UUID `gorm:"type:uuid;index" json:"pmId,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (i *ITIntent) BeforeCreate(tx *gorm.DB) error {
	if i.ID == uuid.Nil {
		i.ID = uuid.New()
	}
	return nil
}

// ITRate is the admin-set MCGP price for IT (singleton). MCGP is priced in USD;
// the Naira buy/sell rates are derived at request time from the live NGN/USD FX
// feed (same as other P2P assets). Only the active row is used.
//
//   - BuyPriceUsdMicros: USD per 1 MCGP in 6dp micro-dollars ($0.02 = 20000).
//   - SellDiscountBps:   discount applied to the sell leg vs buy; 5% = 500. The
//     company margin = the spread between buy and the discounted sell price.
type ITRate struct {
	ID                uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	BuyPriceUsdMicros int64      `gorm:"not null" json:"buyPriceUsdMicros"`
	SellDiscountBps   int        `gorm:"not null;default:0" json:"sellDiscountBps"`
	Active            bool       `gorm:"not null;default:true;index" json:"active"`
	UpdatedBy         *uuid.UUID `gorm:"type:uuid" json:"updatedBy,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

func (r *ITRate) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}
