package services

import (
	"fmt"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// ErrUnknownITProvider: an intent names a provider no branch knows. Every
// provider-specific path refuses it instead of falling through to another
// provider's terms (spec 2026-10-08 S45): a paid pool order must never be
// bought a second time at Flutterwave.
var ErrUnknownITProvider = fmt.Errorf("%w: the intent's provider has no terms", ErrITBadState)

// itProviderTerms is how one provider fills a routed stablecoin buy. Every
// branch on an intent's provider reads it from providerTerms; nothing compares
// provider strings itself (TestProviderBranches_NoDirectComparisons).
type itProviderTerms struct {
	provider string // the it_intents.provider value ('' reads as Flutterwave)
	label    string // how a refusal names it
	metric   string // the routed METRIC stem
	// flutterwaveFulfils: Flutterwave converts the naira and sends the asset —
	// ConvertStableBuy, DeliverStableBuy, their Finalize pair and
	// PendingStableBuys act on it, and its live price is Flutterwave's.
	flutterwaveFulfils bool
	// companyWallet: the company wallet sends from its own stock — a live price
	// wins stock under the lock, a re-price runs only under that lock, and a
	// refund waits for secondPaymentGate.
	companyWallet bool
	// pool: TSA Liquidity — LP escrow slices opened by the conversion treasury
	// (spec 2026-10-08). Live-priced only under the pool lock (S18); never a
	// Flutterwave or company-wallet step. C1 opens and claims it; C2 settles it.
	pool bool
}

// knownITProviders is every provider providerTerms answers for. A new provider
// is a case below plus an entry here, and nowhere else.
var knownITProviders = []string{models.ITProviderFlutterwave, models.ITProviderTreasury, models.ITProviderPool}

// providerTerms is the one branch on an intent's provider. Its default is an
// error, never another provider's terms.
func providerTerms(i *models.ITIntent) (itProviderTerms, error) {
	switch p := models.ITIntentProvider(i); p {
	case models.ITProviderFlutterwave:
		return itProviderTerms{provider: p, label: "Flutterwave", metric: "fw", flutterwaveFulfils: true}, nil
	case models.ITProviderTreasury:
		return itProviderTerms{provider: p, label: "company-wallet", metric: "treasury", companyWallet: true}, nil
	case models.ITProviderPool:
		return itProviderTerms{provider: p, label: "TSA pool", metric: "pool", pool: true}, nil
	default:
		return itProviderTerms{}, fmt.Errorf("%w: %q", ErrUnknownITProvider, p)
	}
}

// errPoolPricedUnderLock: a pool buy is priced live only by S18 under its pool
// lock (plan C1 Task 11), never by a generic re-price path.
var errPoolPricedUnderLock = fmt.Errorf("%w: a TSA pool buy is priced live only under its pool lock", ErrITBadState)

// errPoolNotRouted: a pool row without a routed quote can never price at the
// old endpoint. Deterministic (review M4): the claim parks it, never retries.
var errPoolNotRouted = fmt.Errorf("%w: a TSA pool buy has no routed quote to price against", ErrITBadState)

// requireFlutterwaveFulfils guards every Flutterwave driver step: a buy
// Flutterwave does not fulfil is refused, with the refusal text the company
// wallet always had ("a company-wallet buy is never converted at Flutterwave").
func requireFlutterwaveFulfils(i *models.ITIntent, step string) error {
	t, err := providerTerms(i)
	if err != nil {
		return err
	}
	if !t.flutterwaveFulfils {
		return fmt.Errorf("%w: a %s buy is never %s Flutterwave", ErrITBadState, t.label, step)
	}
	return nil
}

// flutterwaveFulfilledProviders is the provider column values the Flutterwave
// watcher lists: every provider Flutterwave fulfils, plus "" when a legacy row
// reads as one of them.
func flutterwaveFulfilledProviders() []string {
	var out []string
	if t, err := providerTerms(&models.ITIntent{}); err == nil && t.flutterwaveFulfils {
		out = append(out, "")
	}
	for _, p := range knownITProviders {
		if t, err := providerTerms(&models.ITIntent{Provider: p}); err == nil && t.flutterwaveFulfils {
			out = append(out, p)
		}
	}
	return out
}

// poolProviders is the provider column values a pool query lists — the one
// pool filter the P7 lint accepts.
func poolProviders() []string {
	var out []string
	for _, p := range knownITProviders {
		if t, err := providerTerms(&models.ITIntent{Provider: p}); err == nil && t.pool {
			out = append(out, p)
		}
	}
	return out
}

// companyWalletProviders is the provider column values a company-wallet query
// lists (its owed deliveries, LaneCommitments).
func companyWalletProviders() []string {
	var out []string
	for _, p := range knownITProviders {
		if t, err := providerTerms(&models.ITIntent{Provider: p}); err == nil && t.companyWallet {
			out = append(out, p)
		}
	}
	return out
}

func isCompanyWalletBuy(i *models.ITIntent) bool {
	t, err := providerTerms(i)
	return err == nil && t.companyWallet
}

func isFlutterwaveBuy(i *models.ITIntent) bool {
	t, err := providerTerms(i)
	return err == nil && t.flutterwaveFulfils
}

// intentIsProvider is a positive match against a provider a caller names (the
// order view's "is this order yours, provider X?"). Unknown never matches a
// known name.
func intentIsProvider(i *models.ITIntent, provider string) bool {
	return models.ITIntentProvider(i) == provider
}
