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
// destinations IT currently offers. NGN bank only until African-currency /
// mobile-money payouts are sandbox-proven and re-enabled as product.
//
// Cross-currency gateway + quote code remains in-tree; with no non-NGN catalog
// entries, SupportedWithdrawalQuoteCurrencies is empty and Sell rejects any
// foreign destination at the handler.
//
// NGN needs no FX quote (it's the source currency, 1:1) but IS a valid
// withdrawal destination, so it is listed even though it is excluded from the
// quote allowlist.
var WithdrawalDestinationCatalog = []WithdrawalDestination{
	{Currency: "NGN", Rail: "bank", Label: "Nigerian Bank Account"},
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
