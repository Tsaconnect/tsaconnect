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
}

// paymentProviders is the ordered catalog. Add a provider by appending one
// line — no other code needs to change.
var paymentProviders = []PaymentProvider{
	{Slug: "bank_transfer", Label: "Bank Transfer", Type: "bank", IdentifierLabel: "Account number"},
	{Slug: "opay", Label: "OPay", Type: "mobile_money", IdentifierLabel: "Account/phone"},
	{Slug: "palmpay", Label: "PalmPay", Type: "mobile_money", IdentifierLabel: "Account/phone"},
	{Slug: "kuda", Label: "Kuda", Type: "bank", IdentifierLabel: "Account number"},
	{Slug: "interac", Label: "Interac e-Transfer", Type: "bank", IdentifierLabel: "Email"},
	{Slug: "paypal", Label: "PayPal", Type: "wallet", IdentifierLabel: "Email"},
	{Slug: "revolut", Label: "Revolut", Type: "wallet", IdentifierLabel: "Tag/phone"},
	{Slug: "wise", Label: "Wise", Type: "wallet", IdentifierLabel: "Email"},
	{Slug: "skrill", Label: "Skrill", Type: "wallet", IdentifierLabel: "Email"},
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
