package services

import "context"

// NombaGateway is the per-PM boundary to Nomba. A later plan implements it
// against the live API (payout endpoint + webhook payloads + signature) once
// design §12 is confirmed; the automation orchestration codes against this
// interface only.
type NombaGateway interface {
	// InitiatePayout sends NGN to a bank account from the PM's Nomba account and
	// returns Nomba's payout id. Status arrives later via a PayoutStatus webhook.
	InitiatePayout(ctx context.Context, req PayoutRequest) (payoutID string, err error)
}

// PayoutRequest is a sell-flow naira payout to the user's bank.
type PayoutRequest struct {
	BankCode      string
	AccountNumber string
	// AmountNGN is the legacy amount field, retained because it is read in a
	// dozen files including the dormant Nomba client and the out-of-scope bills
	// service. New code sets Amount + SourceCurrency; both are populated during
	// the transition and the gateway prefers the explicit pair.
	//
	// Deprecated: use Amount with SourceCurrency.
	AmountNGN string // decimal string, e.g. "1500.00"

	// Amount is what the RECIPIENT receives, denominated in DestinationCurrency
	// (empty destination ⇒ NGN). The gateway sends applies_to=destination_currency
	// on every transfer, so the provider debits our source balance with whatever
	// that costs and bills its fee separately.
	//
	// Callers converting from another currency must therefore convert BEFORE
	// setting this. Getting the denomination wrong is not a rounding error — a
	// naira figure sent as cedis is off by roughly 100x.
	Amount string
	// SourceCurrency is the balance the payout draws FROM. Empty means NGN.
	// It does not describe Amount; see above.
	SourceCurrency string
	Reference      string // our trade reference (idempotency)
	AccountName    string // recipient account name (used by live gateway)
	SenderName     string // sender display name (used by live gateway)
	// Deprecated: SubAccountID is UNUSED under the VA-only corporate model — all
	// payouts (sell + withdrawal) come from TSA's single parent Nomba account via
	// client.Transfer. The field is retained so existing call sites compile, but
	// nothing sets it and the gateway ignores it.
	SubAccountID string

	// Multi-currency withdrawal (optional; empty ⇒ legacy NGN bank behavior).
	DestinationCurrency string // ISO, e.g. "KES"; "" ⇒ "NGN"
	RailType            string // "bank" | "mobile_money"; "" ⇒ "bank"
	MobileNetwork       string // mobile_money only, e.g. "wave"
	MobileCountry       string // mobile_money only, ISO-2, e.g. "SN"
	MobileMsisdn        string // mobile_money only

	// Details is a foreign corridor's recipient detail keyed by the
	// PayoutField* names (it_withdraw_catalog.go). Nil ⇒ the legacy body,
	// unchanged. When set it supplies the bank identifiers, name, contact and
	// address; BankCode/AccountNumber above are ignored.
	Details map[string]string
}

// InboundPayment is a normalized inbound naira credit (buy flow). The webhook
// receiver parses Nomba's raw payload into this. Status is "settled" when the
// credit is final (we only auto-release on settled — design §9).
type InboundPayment struct {
	EventID             string
	WalletID            string // data.merchant.walletId — the single-TSA-parent inbound key (env-configured)
	AccountRef          string // VA accountRef — the corporate-PM (VA-model) inbound key
	DestinationAccount  string // kept for back-compat with existing references; not populated by the real parser
	SenderAccountName   string
	SenderAccountNumber string
	AmountNGN           string
	// Currency of the credited amount. Empty means NGN (the Nomba parser
	// predates multi-currency and leaves it unset); the Flutterwave parser
	// always sets it so consumers can reject non-NGN credits they can't price.
	Currency string
	Status   string // "settled" | "pending" | ...
	// ProviderChargeID is the provider's authoritative charge/transaction id
	// (Flutterwave's data.id, e.g. "chg_..."). Used to re-fetch the charge from
	// the provider API and verify it before crediting. Empty for providers that
	// don't support charge verification (Nomba).
	ProviderChargeID string
	// CustomerID is the Flutterwave customer id on charge.completed (Spec 1
	// standing-VA attribution). Empty for Nomba / older parsers.
	CustomerID string
}

// PayoutStatus is a normalized payout-result webhook (sell flow).
type PayoutStatus struct {
	EventID       string
	PayoutID      string
	MerchantTxRef string // data.transaction.merchantTxRef — matches our trade UUID
	Status        string // "success" | "failed" | "pending"
}
