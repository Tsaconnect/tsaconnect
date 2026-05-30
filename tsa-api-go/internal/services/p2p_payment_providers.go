package services

import "strings"

// PaymentProvider is one entry in the curated P2P payment-method catalog.
// The catalog is the single source of truth shared with the frontend (via
// GET /p2p/payment-providers) so labels/types never drift between client and
// server. `Type` is the coarse category derived for a provider; `Provider`
// (the slug) is the precise, filterable identifier stored on payment methods.
type PaymentProvider struct {
	Slug            string `json:"slug"`
	Label           string `json:"label"`
	Type            string `json:"type"`            // bank | mobile_money | wallet
	IdentifierLabel string `json:"identifierLabel"` // UI label for the account-id field
	Speed           string `json:"speed"`           // instant | fast | standard | slow
	SpeedLabel      string `json:"speedLabel"`      // human label, e.g. "Instant", "~10 min"
	// Currencies this provider serves (ISO codes). Empty = universal (e.g. bank
	// transfer works in any currency). Used to scope the picker to the active fiat.
	Currencies []string `json:"currencies,omitempty"`
}

// paymentProviders is the ordered catalog. Add a provider by appending one
// line — no other code needs to change.
var paymentProviders = []PaymentProvider{
	{Slug: "bank_transfer", Label: "Bank Transfer", Type: "bank", IdentifierLabel: "Account number", Speed: "standard", SpeedLabel: "~30 min"},
	{Slug: "opay", Label: "OPay", Type: "mobile_money", IdentifierLabel: "Account/phone", Speed: "instant", SpeedLabel: "Instant", Currencies: []string{"NGN"}},
	{Slug: "palmpay", Label: "PalmPay", Type: "mobile_money", IdentifierLabel: "Account/phone", Speed: "instant", SpeedLabel: "Instant", Currencies: []string{"NGN"}},
	{Slug: "kuda", Label: "Kuda", Type: "bank", IdentifierLabel: "Account number", Speed: "fast", SpeedLabel: "~10 min", Currencies: []string{"NGN"}},
	{Slug: "interac", Label: "Interac e-Transfer", Type: "bank", IdentifierLabel: "Email", Speed: "fast", SpeedLabel: "~10 min", Currencies: []string{"CAD"}},
	{Slug: "sepa", Label: "SEPA Transfer", Type: "bank", IdentifierLabel: "IBAN", Speed: "slow", SpeedLabel: "1–2 days", Currencies: []string{"EUR"}},
	{Slug: "paypal", Label: "PayPal", Type: "wallet", IdentifierLabel: "Email", Speed: "instant", SpeedLabel: "Instant", Currencies: []string{"USD", "EUR", "GBP", "CAD", "AUD"}},
	{Slug: "revolut", Label: "Revolut", Type: "wallet", IdentifierLabel: "Tag/phone", Speed: "instant", SpeedLabel: "Instant", Currencies: []string{"GBP", "EUR", "USD"}},
	{Slug: "wise", Label: "Wise", Type: "wallet", IdentifierLabel: "Email", Speed: "fast", SpeedLabel: "~10 min", Currencies: []string{"USD", "EUR", "GBP", "CAD", "AUD"}},
	{Slug: "skrill", Label: "Skrill", Type: "wallet", IdentifierLabel: "Email", Speed: "instant", SpeedLabel: "Instant", Currencies: []string{"USD", "EUR", "GBP"}},
}

var providerBySlug = func() map[string]PaymentProvider {
	m := make(map[string]PaymentProvider, len(paymentProviders))
	for _, p := range paymentProviders {
		m[p.Slug] = p
	}
	return m
}()

// AllPaymentProviders returns the full catalog (ordered) for the create-ad picker.
func AllPaymentProviders() []PaymentProvider { return paymentProviders }

// ProvidersForCurrency returns the catalog scoped to a fiat currency: universal
// providers (empty Currencies, e.g. bank transfer) plus those serving the currency.
// Case-insensitive. An empty currency returns the full catalog (back-compat).
func ProvidersForCurrency(currency string) []PaymentProvider {
	c := strings.ToUpper(strings.TrimSpace(currency))
	if c == "" {
		return paymentProviders
	}
	out := make([]PaymentProvider, 0, len(paymentProviders))
	for _, p := range paymentProviders {
		if len(p.Currencies) == 0 || containsFold(p.Currencies, c) {
			out = append(out, p)
		}
	}
	return out
}

func containsFold(xs []string, target string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, target) {
			return true
		}
	}
	return false
}

// PaymentProviderBySlug looks up a catalog entry. ok is false for unknown slugs.
func PaymentProviderBySlug(slug string) (PaymentProvider, bool) {
	p, ok := providerBySlug[slug]
	return p, ok
}

// IsValidProvider reports whether slug is in the catalog.
func IsValidProvider(slug string) bool {
	_, ok := providerBySlug[slug]
	return ok
}

// ProviderLabel returns the display label for a slug, falling back to a
// title-cased slug for forward-compatibility with rows written by a newer
// client than this server.
func ProviderLabel(slug string) string {
	if p, ok := providerBySlug[slug]; ok {
		return p.Label
	}
	return titleCaseSlug(slug)
}

// ProviderType returns the coarse category for a slug, or "" if unknown.
func ProviderType(slug string) string {
	if p, ok := providerBySlug[slug]; ok {
		return p.Type
	}
	return ""
}

func titleCaseSlug(slug string) string {
	parts := strings.Split(slug, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// PaymentMethodSummary is the privacy-safe per-ad payment descriptor exposed
// in the marketplace list — provider + display label only, never account data.
type PaymentMethodSummary struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

// PaymentSummaryFromProviders maps a list of provider slugs to deduped,
// order-stable summaries. Unknown slugs fall back to a title-cased label.
func PaymentSummaryFromProviders(slugs []string) []PaymentMethodSummary {
	seen := make(map[string]struct{}, len(slugs))
	out := make([]PaymentMethodSummary, 0, len(slugs))
	for _, s := range slugs {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, PaymentMethodSummary{Provider: s, Label: ProviderLabel(s)})
	}
	return out
}
