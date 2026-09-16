package services

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrITQuoteUnavailable is returned when no live FX quote source is wired
// (dormant Flutterwave client) or the provider returns an unusable quote.
var ErrITQuoteUnavailable = errors.New("Instant Transfer FX quote is temporarily unavailable")

// ErrUnsupportedWithdrawalCurrency is returned when the requested destination
// currency isn't in the v1 quotable set (see SupportedWithdrawalQuoteCurrencies).
var ErrUnsupportedWithdrawalCurrency = errors.New("this currency is not yet supported for withdrawal")

// SupportedWithdrawalQuoteCurrencies is the v1 allowlist of destination
// currencies QuoteWithdrawal will FX-quote against NGN. Derived from
// WithdrawalDestinationCatalog (it_withdraw_catalog.go) — the catalog is the
// single source of truth; this is just "the catalog's currencies minus NGN"
// so the two can never drift apart. NGN itself needs no quote (it's the
// source currency — 1:1, no FX leg) even though it IS a valid withdrawal
// destination.
var SupportedWithdrawalQuoteCurrencies = quotableWithdrawalCurrencies()

// quotableWithdrawalCurrencies derives the FX-quotable currency set from the
// catalog, excluding NGN (the source currency).
func quotableWithdrawalCurrencies() map[string]bool {
	out := make(map[string]bool, len(WithdrawalDestinationCatalog))
	for _, d := range WithdrawalDestinationCatalog {
		if strings.EqualFold(d.Currency, "NGN") {
			continue
		}
		out[strings.ToUpper(d.Currency)] = true
	}
	return out
}

// quoteCacheTTL bounds how long a derived NGN-per-destination-unit rate is
// reused before QuoteWithdrawal re-probes Flutterwave.
const quoteCacheTTL = 60 * time.Second

// quoteCacheEntry is one cached rate for a destination currency.
type quoteCacheEntry struct {
	ngnPerDestUnit float64
	expiresAt      time.Time
}

// FXRateQuoter is the seam over FlutterwaveClient.QuoteRate, narrowed so this
// package's tests don't need the full Flutterwave client (mirrors the
// ProviderFeeQuoter pattern in fee_quote_service.go). *FlutterwaveClient
// satisfies this interface as-is.
type FXRateQuoter interface {
	QuoteRate(ctx context.Context, sourceCurrency, destCurrency string, destAmount float64) (FWRate, error)
}

// SetRateQuoter wires the live FX quote source (Flutterwave) used by
// QuoteWithdrawal. Optional — nil keeps QuoteWithdrawal returning
// ErrITQuoteUnavailable so dev environments without FW credentials still boot.
func (s *ITService) SetRateQuoter(q FXRateQuoter) { s.rateQuoter = q }

// QuoteWithdrawal previews the destination-currency amount a user would
// receive for an NGN withdrawal amount, via a live Flutterwave FX quote.
// Pass-through rate only — no company spread is added here or anywhere in
// this path.
//
// ── Direction-robust derivation ──
// FlutterwaveClient.QuoteRate POSTs a DESTINATION amount to /transfers/rates
// (source currency + {destination currency, amount}) and gets back
// FWRate{Source, Destination}. Rather than trust the ambiguous top-level rate
// field FW also returns (its unit direction — source-per-destination vs the
// inverse — is not documented anywhere we've found, so it isn't even modeled;
// see FWRate's doc comment), we derive the rate from the
// Source/Destination amounts FW echoes back, which are unambiguous: "to
// receive Destination.Amount units of destCurrency, you pay Source.Amount
// units of NGN". We probe with a 1-unit destination amount, so:
//
//	ngnPerDestUnit = Source.Amount / Destination.Amount   // NGN per 1 dest unit
//	destAmount     = ngnAmount / ngnPerDestUnit
//
// This is pass-through pricing assumed linear in amount (per the task's
// "no spread" constraint — the probe amount itself is discarded, only the
// per-unit rate it implies is kept and reused for the caller's actual
// ngnAmount).
//
// This is pinned by TestQuoteWithdrawal against a STUBBED response, not a
// live call. The 1-unit probe + FW's /transfers/rates response contract
// (amounts present and > 0, currencies as requested) still need FW-sandbox
// confirmation before this ships to production — confirm against a real
// POST /transfers/rates response before removing this note.
func (s *ITService) QuoteWithdrawal(ctx context.Context, ngnAmount float64, destCurrency string) (destAmount float64, rate float64, err error) {
	destCurrency = strings.ToUpper(strings.TrimSpace(destCurrency))
	if !SupportedWithdrawalQuoteCurrencies[destCurrency] {
		return 0, 0, ErrUnsupportedWithdrawalCurrency
	}

	if cached, ok := s.cachedQuoteRate(destCurrency); ok {
		return ngnAmount / cached, cached, nil
	}

	if s.rateQuoter == nil {
		return 0, 0, ErrITQuoteUnavailable
	}
	fw, err := s.rateQuoter.QuoteRate(ctx, "NGN", destCurrency, 1)
	if err != nil {
		return 0, 0, err
	}
	srcAmt, dstAmt := fw.Source.Amount.Float64(), fw.Destination.Amount.Float64()
	if srcAmt <= 0 || dstAmt <= 0 {
		return 0, 0, ErrITQuoteUnavailable
	}
	ngnPerDestUnit := srcAmt / dstAmt
	s.cacheQuoteRate(destCurrency, ngnPerDestUnit)
	return ngnAmount / ngnPerDestUnit, ngnPerDestUnit, nil
}

// cachedQuoteRate returns the cached NGN-per-destination-unit rate for
// destCurrency if present and still fresh.
func (s *ITService) cachedQuoteRate(destCurrency string) (float64, bool) {
	s.quoteCacheMu.Lock()
	defer s.quoteCacheMu.Unlock()
	entry, ok := s.quoteCache[destCurrency]
	if !ok || time.Now().After(entry.expiresAt) {
		return 0, false
	}
	return entry.ngnPerDestUnit, true
}

// cacheQuoteRate stores a freshly-probed rate for destCurrency with a
// quoteCacheTTL expiry.
func (s *ITService) cacheQuoteRate(destCurrency string, ngnPerDestUnit float64) {
	s.quoteCacheMu.Lock()
	defer s.quoteCacheMu.Unlock()
	if s.quoteCache == nil {
		s.quoteCache = make(map[string]quoteCacheEntry)
	}
	s.quoteCache[destCurrency] = quoteCacheEntry{
		ngnPerDestUnit: ngnPerDestUnit,
		expiresAt:      time.Now().Add(quoteCacheTTL),
	}
}
