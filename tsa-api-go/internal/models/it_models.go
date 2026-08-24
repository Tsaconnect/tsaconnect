package models

import (
	"strings"
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

	// Currency is the fiat leg's ISO 4217 code. Validated against the currency
	// registry — never against display metadata.
	//
	// Deliberately has NO column default. A default would make every write path
	// that forgets this field silently produce an NGN-denominated intent, and
	// for a cedi amount that is a ~100x mispricing. Rows written before this
	// column existed are read as NGN by ITIntentCurrency() — that fallback is
	// scoped to historical rows and lives in exactly one place, so it cannot
	// quietly become the general default.
	Currency string `gorm:"size:3;index" json:"currency"`

	// ── Amounts ──
	// The *Kobo fields are the original NGN-only representation; the *Minor
	// fields are the same values in the intent currency's minor unit. Both are
	// written until the legacy columns are dropped.
	//
	// For NGN the two are identical — minor units ARE kobo — which is what lets
	// this migration be additive. Every currency in the registry today is
	// 2-decimal, so the arithmetic in it_automation.go stays correct for all of
	// them; the first 0- or 3-decimal currency is what would force it to become
	// exponent-aware.
	NgnKobo  string `gorm:"type:numeric(38,0);not null" json:"ngnKobo"`  // naira*100
	RateKobo string `gorm:"type:numeric(38,0);not null" json:"rateKobo"` // kobo per 1 MCGP, locked

	AmountMinor string `gorm:"type:numeric(38,0)" json:"amountMinor,omitempty"`
	RateMinor   string `gorm:"type:numeric(38,0)" json:"rateMinor,omitempty"`

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

	// Minor-unit twins of the three fee columns above. Same dual-write rule:
	// written alongside the *Kobo fields, identical for NGN.
	FeeMinor          string `gorm:"type:numeric(38,0)" json:"feeMinor,omitempty"`
	NetMinor          string `gorm:"type:numeric(38,0)" json:"netMinor,omitempty"`
	PlatformKeptMinor string `gorm:"type:numeric(38,0)" json:"platformKeptMinor,omitempty"`

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

// BeforeSave keeps the minor-unit columns in step with the kobo columns.
//
// Doing this in a hook rather than at each write site is deliberate: the two
// representations must never disagree, and there are enough places that build or
// update an ITIntent that relying on every one of them to remember would make
// divergence a matter of time. The hook makes the dual-write structural.
//
// The mirror is one-directional (kobo → minor) because kobo remains the
// authoritative representation until the legacy columns are dropped; at that
// point this hook and the *Kobo fields go together.
func (i *ITIntent) BeforeSave(tx *gorm.DB) error {
	i.AmountMinor = i.NgnKobo
	i.RateMinor = i.RateKobo
	i.FeeMinor = i.FeeNgnKobo
	i.NetMinor = i.NetNgnKobo
	i.PlatformKeptMinor = i.PlatformKeptKobo
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

// ITIntentCurrency returns the intent's fiat currency, treating an empty value
// as NGN.
//
// This is the ONE place that fallback is allowed to live. The column has no
// database default precisely so a write path that forgets to set Currency
// fails loudly rather than producing a silently naira-denominated intent — but
// rows written before the column existed legitimately have no value, and they
// were all naira. Mirrors the existing "an empty fee column means no fee was
// taken" convention for pre-fee-engine intents: historical absence is read as
// the behaviour of the time, and never as a default for new writes.
//
// Do not inline this as `if cur == "" { cur = "NGN" }` at a call site. Spread
// across the codebase that pattern becomes indistinguishable from a real
// default, which is exactly what the missing column default is there to prevent.
func ITIntentCurrency(i *ITIntent) string {
	if i == nil || strings.TrimSpace(i.Currency) == "" {
		return "NGN"
	}
	return strings.ToUpper(strings.TrimSpace(i.Currency))
}
