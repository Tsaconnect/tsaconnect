package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// ErrStableBuyBelowMinimum means the stablecoin the user would RECEIVE is
// below Flutterwave's outbound floor.
//
// Checked against the post-spread asset amount, because the floor applies to
// the delivery transfer. Unlike the sell leg — where the floor can only be
// enforced after the user has irreversibly sent funds — buy can refuse before
// any money moves at all, which is strictly safer.
var ErrStableBuyBelowMinimum = errors.New("it stable buy: delivery amount is below the provider minimum")

// StableBuyQuote prices naira into stablecoin.
type StableBuyQuote struct {
	Asset    string
	Currency string
	// FiatMinor is what the user pays, in fiat minor units.
	FiatMinor *big.Int
	// GrossAssetUnits is what the fiat buys at Flutterwave's own rate;
	// AssetUnits is that less the company spread — what the user receives.
	// Both in asset base units (6 decimals).
	GrossAssetUnits *big.Int
	AssetUnits      *big.Int
	// RatePerUnitMinor is fiat minor units per one whole asset unit, spread
	// applied. Indicative only: the intent is priced again when money lands.
	RatePerUnitMinor *big.Int
	ExpiresAt        time.Time
}

// QuoteStableBuy prices `fiatMinor` of `currency` into `asset`.
//
// Indicative by design. The binding price is struck when the user's naira
// actually lands (see AutoCreditFromVA's buy branch), which is what removes
// the rate lock and with it every "paid after the quote expired" state.
func (s *ITService) QuoteStableBuy(ctx context.Context, asset, fiatMinor, currency string) (*StableBuyQuote, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !IsStableAsset(asset) {
		return nil, fmt.Errorf("%w: got %q", ErrStableNotAnAsset, asset)
	}
	if s.rateQuoter == nil {
		return nil, ErrStableQuoteUnavailable
	}

	fiat, ok := new(big.Int).SetString(strings.TrimSpace(fiatMinor), 10)
	if !ok || fiat.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount must be a positive integer of minor units", ErrStableBuyBelowMinimum)
	}

	// Probe one whole asset unit. Direction matters and is the inverse of
	// sell: fiat is the SOURCE, the asset the DESTINATION.
	rate, err := s.rateQuoter.QuoteRate(ctx, currency, asset, 1)
	if err != nil {
		return nil, fmt.Errorf("it stable buy quote: %w", err)
	}
	fiatPerUnit, err := DeriveBuyFiatPerUnit(rate)
	if err != nil {
		return nil, err
	}
	if err := s.checkRatePlausible(ctx, fiatPerUnit, currency); err != nil {
		return nil, err
	}

	// grossUnits = fiatMinor / 100 / fiatPerUnit * 10^6, floored.
	exp := new(big.Int).Exp(big.NewInt(10), big.NewInt(StableAssetDecimals), nil)
	grossRat := new(big.Rat).SetInt(fiat)
	grossRat.Quo(grossRat, new(big.Rat).SetInt64(100))
	grossRat.Quo(grossRat, fiatPerUnit)
	grossRat.Mul(grossRat, new(big.Rat).SetInt(exp))
	gross := new(big.Int).Quo(grossRat.Num(), grossRat.Denom())

	net := ApplyBuySpread(gross, s.stableSpreadBps)

	if net.Cmp(s.stableMinSell()) < 0 {
		return nil, fmt.Errorf("%w: minimum delivery is %g %s",
			ErrStableBuyBelowMinimum, StableFromBaseUnits(s.stableMinSell()), asset)
	}

	// Effective per-unit price the user is getting, in fiat minor units.
	//
	// SetFrac64(10000, 10000-spread) divides by zero if the spread is ever
	// configured to 10000 bps. Unreachable today (SetStableSpreadBps rejects
	// that), but guarded there rather than leaving a latent panic on a money
	// path.
	perUnit := new(big.Rat).Mul(fiatPerUnit, new(big.Rat).SetInt64(100))
	perUnit.Mul(perUnit, new(big.Rat).SetFrac64(10000, 10000-s.stableSpreadBps))
	ratePerUnit := new(big.Int).Quo(perUnit.Num(), perUnit.Denom())

	return &StableBuyQuote{
		Asset:            asset,
		Currency:         currency,
		FiatMinor:        fiat,
		GrossAssetUnits:  gross,
		AssetUnits:       net,
		RatePerUnitMinor: ratePerUnit,
		ExpiresAt:        time.Now().Add(s.stableLockTTL()),
	}, nil
}
