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

// WithdrawalDestinationCatalog is the resolved v1 catalog. Deliberately
// minimal/conservative — FW-account-confirmation (a separate sandbox gate) is
// not a reason to withhold a currency here; only add an entry once the rail
// is actually fulfillable end-to-end.
//
// NGN needs no FX quote (it's the source currency, 1:1 — see
// SupportedWithdrawalQuoteCurrencies) but IS a valid withdrawal destination,
// so it's listed here even though it's excluded from the quote allowlist.
var WithdrawalDestinationCatalog = []WithdrawalDestination{
	{Currency: "NGN", Rail: "bank", Label: "Nigerian Bank Account"},
	{Currency: "GHS", Rail: "bank", Label: "Ghanaian Bank Account"},
	{Currency: "XOF", Rail: "mobile_money", Network: "wave", Label: "Wave Mobile Money"},
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
