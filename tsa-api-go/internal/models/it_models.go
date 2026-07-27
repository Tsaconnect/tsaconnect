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
	// ITStatusAwaitingRate parks a VA credit when FX is stale at settlement —
	// money already landed; never credit at a stale rate (Spec 1).
	ITStatusAwaitingRate = "awaiting_rate"
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

	// ── Fees (both directions) ──
	// NgnKobo above stays the GROSS valuation of the MCGP leg — what the asset
	// was worth at the locked rate. These record what the fee engine took out
	// of it and what actually moves as a result:
	//
	//	sell:  payout disburses NetNgnKobo; FeeNgnKobo stays in the payout balance
	//	buy:   McgpAmount credited is already net; FeeNgnKobo is the naira value
	//	       withheld, whose MCGP equivalent is never minted to the user
	//
	// Empty on intents created before fee deduction shipped — readers must
	// treat "" as "no fee taken, net == gross" so in-flight intents settle at
	// the terms they were created under.
	FeeNgnKobo string `gorm:"type:numeric(38,0)" json:"feeNgnKobo,omitempty"`
	NetNgnKobo string `gorm:"type:numeric(38,0)" json:"netNgnKobo,omitempty"`
	// PlatformKeptKobo is the slice of FeeNgnKobo the platform actually retains
	// (swap + platform fee) — the provider's pass-through and tax are excluded.
	// This is the base the cashback/TP carve is computed from, recorded so a
	// later rate change can't retro-explain an old payout.
	PlatformKeptKobo string `gorm:"type:numeric(38,0)" json:"platformKeptKobo,omitempty"`

	// PaymentRef is the strong NGN attribution key: the minted "IT-…" reference
	// at creation, overwritten with the provider charge id once VA settlement
	// attributes a payment to this intent. Buy only. Uniqueness is enforced by
	// the partial index uq_it_intents_payment_ref (config/database.go) rather
	// than a struct tag, because sell intents leave it empty.
	PaymentRef string `gorm:"size:64;index" json:"paymentRef,omitempty"`
	// DebitRef is the on-chain bytes32 ref (hex) for the sell two-phase debit.
	DebitRef string `gorm:"size:66;index" json:"debitRef,omitempty"`

	TxHash        string `gorm:"size:66" json:"txHash,omitempty"`
	FailureReason string `gorm:"size:256" json:"failureReason,omitempty"`

	// Sell payout destination (captured at sell creation for auto-payout; empty
	// on legacy manual-payout intents).
	BankCode      string `gorm:"size:16" json:"bankCode,omitempty"`
	AccountNumber string `gorm:"size:20" json:"accountNumber,omitempty"`
	AccountName   string `gorm:"size:128" json:"accountName,omitempty"`
	// Multi-currency / non-bank payout rail (optional; empty ⇒ legacy NGN bank
	// via BankCode/AccountNumber above). Mirrors services.PayoutRequest so the
	// sell payout path can pass these straight through to the gateway.
	DestinationCurrency string `gorm:"size:8" json:"destinationCurrency,omitempty"`
	RailType            string `gorm:"size:16" json:"railType,omitempty"`
	MobileNetwork       string `gorm:"size:32" json:"mobileNetwork,omitempty"`
	MobileCountry       string `gorm:"size:2" json:"mobileCountry,omitempty"`
	MobileMsisdn        string `gorm:"size:20" json:"mobileMsisdn,omitempty"`
	// PayoutID is the fiat provider's transfer id for the sell payout leg; the
	// transfer.disburse webhook closes the intent against it.
	PayoutID string `gorm:"size:64;index" json:"payoutId,omitempty"`

	// Dynamic virtual-account details issued for this buy intent (Flutterwave
	// collections); shown to the buyer as the pay-into account.
	VAAccountNumber string `gorm:"size:20" json:"vaAccountNumber,omitempty"`
	VABankName      string `gorm:"size:64" json:"vaBankName,omitempty"`

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
