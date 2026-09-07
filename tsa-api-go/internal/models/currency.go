package models

import "time"

// Currency is one fiat currency the money paths may denominate in. This table
// is the SOLE validation authority for any currency reaching a money path —
// services.isKnownCurrency (backed by currencyMetadata, ~100 ISO codes) is
// DISPLAY metadata and must never be used to gate a payment.
//
// The distinction matters: currencyMetadata knows JPY, which has a minor unit
// of 0 decimals. Every amount helper in this codebase (nairaToKobo,
// koboToNaira*) assumes 2. A JPY amount reaching those helpers is a 100x error,
// so the gate has to be this table, not the display list.
//
// Flutterwave's own footprint is the reason the flags are separate rather than
// one "enabled" bit: the currencies it can COLLECT into (virtual accounts) and
// the currencies it can CONVERT to stablecoin are different sets that happen to
// overlap on NGN and GHS. USD/EUR/GBP can be paid out but never collected.
type Currency struct {
	// Code is the ISO 4217 alphabetic code, uppercase. Primary key.
	Code string `gorm:"size:3;primaryKey" json:"code"`

	// Exponent is the minor-unit decimal count (NGN kobo = 2, JPY = 0,
	// KWD = 3). Every currency currently seeded is 2, which is why the kobo
	// arithmetic stays numerically correct through the multi-currency work —
	// see the spec's "explicitly deferred: the exponent" section. Nothing reads
	// this yet; it exists so the first 0- or 3-decimal currency is a data
	// change plus a bounded code change, not an archaeology exercise.
	Exponent int `gorm:"not null;default:2" json:"exponent"`

	// DepositEnabled reports whether a user can pay INTO this currency, which
	// requires Flutterwave to offer a virtual account for it. False for
	// USD/EUR/GBP permanently — there is no VA product for them, so a balance
	// in those currencies arrives by settlement or treasury wire, never by a
	// user paying into an account we provisioned.
	DepositEnabled bool `gorm:"not null;default:false" json:"depositEnabled"`

	// PayoutEnabled reports whether we can pay OUT in this currency.
	PayoutEnabled bool `gorm:"not null;default:false" json:"payoutEnabled"`

	// RequiresNationalID reports whether Flutterwave demands a national
	// identity document (BVN or NIN) to create a virtual account in this
	// currency. This is a FLUTTERWAVE requirement per currency, not a TSA
	// policy — do not repurpose it as a general KYC flag. NGN requires it in
	// live mode (sandbox does not enforce); GHS documents no equivalent.
	RequiresNationalID bool `gorm:"not null;default:false" json:"requiresNationalId"`

	// IsBaseFloat marks a currency whose balance may fund a payout in a
	// DIFFERENT currency (the cross-currency fallback). Same-currency funding
	// is always preferred; this is what makes USD/EUR/GBP payouts possible at
	// all given they have no collection route of their own.
	IsBaseFloat bool `gorm:"not null;default:false" json:"isBaseFloat"`

	// HasMarketAnchor reports whether this currency is priced from a live P2P
	// market feed (Bybit) rather than Open Exchange Rates' official rate.
	// It matters because the two are not interchangeable: for currencies with a
	// parallel market the official rate can be far from what a user can
	// actually transact at, so pricing one currency off each source would put a
	// systematic error into every trade in the OER-priced one.
	HasMarketAnchor bool `gorm:"not null;default:false" json:"hasMarketAnchor"`

	// MinPayoutMinor is the smallest payout this currency permits, in minor
	// units. Replaces the scalar NGN-only floor: Flutterwave rejects transfers
	// below a per-currency limit, and a payout that fails AFTER the on-chain
	// debit strands the user's premium — so this is enforced BEFORE the debit.
	// 0 means "no floor configured", which callers must treat as "do not gate"
	// rather than "floor of zero".
	MinPayoutMinor int64 `gorm:"not null;default:0" json:"minPayoutMinor"`

	// RampProvider names who serves the stablecoin ramp for this currency:
	// "paycrest", "flutterwave", or empty for none.
	//
	// Deliberately NOT folded into DepositEnabled. That field means
	// "Flutterwave has a virtual-account product here" and it_fiat_va.go
	// depends on it; a Paycrest-only currency with DepositEnabled true would
	// make the VA path believe it can provision an account that does not
	// exist.
	//
	// Empty is ambiguous by construction: it means either "no ramp" or "this
	// row predates the column". CurrencyRegistry.RampProvider resolves it
	// against a compiled-in default rather than reading it literally.
	RampProvider string `gorm:"size:16;not null;default:''" json:"rampProvider"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (Currency) TableName() string { return "currencies" }
