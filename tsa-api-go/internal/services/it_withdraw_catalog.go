package services

import "strings"

// WithdrawalDestination is one catalog entry: a currency/rail (and, for
// mobile_money, network) combination the app's withdrawal-destination picker
// may offer. This is the single source of truth for what IT withdrawal
// supports — SupportedWithdrawalQuoteCurrencies (it_quote.go) and Sell's
// destination validation (IsSupportedWithdrawalDestination, below) both
// derive from it so the two never drift apart.
type WithdrawalDestination struct {
	Currency string `json:"currency"`
	Rail     string `json:"rail"`
	// Network is set only for the mobile_money rail (e.g. "wave").
	Network string `json:"network,omitempty"`
	Label   string `json:"label"`
}

// WithdrawalDestinationCatalog is the product catalog of withdrawal
// destinations IT offers. It is the single source of truth: the app's picker
// renders from it, SupportedWithdrawalQuoteCurrencies derives from it, and
// Sell's validation checks against it — so a rail cannot be offered that the
// API would then reject.
//
// Bank rails only. Mobile money — which matters most in Ghana — is deliberately
// still out: it needs per-network verification of the provider's `network` and
// `country` values, and offering an unverified network would fail at payout,
// after the on-chain debit.
//
// Every entry here must also be PayoutEnabled in the currency registry. The
// registry governs whether a currency may move money at all; this list governs
// whether we present it as a product.
var WithdrawalDestinationCatalog = []WithdrawalDestination{
	{Currency: "NGN", Rail: "bank", Label: "Nigerian Bank Account"},
	{Currency: "GHS", Rail: "bank", Label: "Ghanaian Bank Account"},
	{Currency: "USD", Rail: "bank", Label: "US Bank Account"},
	{Currency: "EUR", Rail: "bank", Label: "Euro Bank Account"},
	{Currency: "GBP", Rail: "bank", Label: "UK Bank Account"},
}

// IsSupportedWithdrawalDestination reports whether (currency, rail, network)
// is a catalog entry, case-insensitively. network is only compared for the
// mobile_money rail — pass "" for bank.
func IsSupportedWithdrawalDestination(currency, rail, network string) bool {
	currency = strings.TrimSpace(currency)
	rail = strings.ToLower(strings.TrimSpace(rail))
	network = strings.TrimSpace(network)
	for _, d := range WithdrawalDestinationCatalog {
		if !strings.EqualFold(d.Currency, currency) || !strings.EqualFold(d.Rail, rail) {
			continue
		}
		if rail == "mobile_money" && !strings.EqualFold(d.Network, network) {
			continue
		}
		return true
	}
	return false
}

// payoutCountryFor maps a payout currency to the ISO-2 country whose rail it
// settles on, for Flutterwave's fee endpoint.
//
// Currency and country are not interchangeable to that endpoint: it prices a
// rail, and "GHS in NG" is not one. Anything unrecognised returns "" so the
// parameter is omitted entirely rather than defaulting to a country that would
// silently price against the wrong market — an omitted filter gives a generic
// quote, a wrong one gives a confidently incorrect answer.
func payoutCountryFor(currency string) string {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "NGN":
		return "NG"
	case "GHS":
		return "GH"
	case "USD":
		return "US"
	case "GBP":
		return "GB"
	case "EUR":
		// The euro area has no single ISO-2 country. Omitting the filter is the
		// honest answer: any member state would be a guess about which rail the
		// payout actually settles on.
		return ""
	default:
		return ""
	}
}
