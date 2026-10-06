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
	// DisplayRateMinor is the rate shown beside the fee lines (spec §14): our
	// margin in, provider fees out. Equal to RatePerUnitMinor when Fees is empty.
	DisplayRateMinor *big.Int
	// Fees are the provider fees already taken out of AssetUnits, one line each.
	// AssetUnits = units(FiatMinor ÷ DisplayRateMinor) − Σ Fees (Decision D38).
	Fees []StableBuyFee
	// SendFeeUnits is Flutterwave's external-send fee inside AssetUnits. Nil
	// for every other quote.
	SendFeeUnits *big.Int
	ExpiresAt    time.Time
}

// QuoteStableBuy prices `fiatMinor` of `currency` into `asset`.
//
// Indicative by design. The binding price is struck when the user's naira
// actually lands (see AutoCreditFromVA's buy branch), which is what removes
// the rate lock and with it every "paid after the quote expired" state.
func (s *ITService) QuoteStableBuy(ctx context.Context, asset, fiatMinor, currency string) (*StableBuyQuote, error) {
	// The whole buy path is gated on the same check, quote included: showing a
	// price for a purchase nothing can fulfil is the first step toward taking
	// naira for one. See HasStableBuy.
	if !s.HasStableBuy() {
		return nil, fmt.Errorf("%w: stablecoin purchases are not enabled", ErrStableQuoteUnavailable)
	}
	asset = strings.ToUpper(strings.TrimSpace(asset))
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !IsStableAsset(asset) {
		return nil, fmt.Errorf("%w: got %q", ErrStableNotAnAsset, asset)
	}

	fiat, ok := new(big.Int).SetString(strings.TrimSpace(fiatMinor), 10)
	if !ok || fiat.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount must be a positive integer of minor units", ErrStableBuyBelowMinimum)
	}

	fiatPerUnit, err := s.StableBuyFiatPerUnit(ctx, currency, asset)
	if err != nil {
		return nil, err
	}
	gross, net, ratePerUnit := priceStableBuyAt(fiatPerUnit, fiat, s.stableSpread())
	if net.Cmp(s.stableMinSell()) < 0 {
		return nil, fmt.Errorf("%w: minimum delivery is %g %s",
			ErrStableBuyBelowMinimum, StableFromBaseUnits(s.stableMinSell()), asset)
	}
	return &StableBuyQuote{
		Asset:            asset,
		Currency:         currency,
		FiatMinor:        fiat,
		GrossAssetUnits:  gross,
		AssetUnits:       net,
		RatePerUnitMinor: ratePerUnit,
		DisplayRateMinor: new(big.Int).Set(ratePerUnit),
		ExpiresAt:        time.Now().Add(s.stableLockTTL()),
	}, nil
}

// priceStableBuyAt is the arithmetic every stablecoin buy quote shares (spec
// §4.1 extraction): gross units from fiat per unit, then the spread, then the
// per-unit rate with the spread applied (floored, as QuoteStableBuy always
// showed it). spreadBps is IT_STABLE_SPREAD_BPS on the old endpoint and the
// margin on the company wallet.
func priceStableBuyAt(fiatPerUnit *big.Rat, fiatMinor *big.Int, spreadBps int64) (gross, net, spreadRate *big.Int) {
	gross = grossStableUnits(fiatMinor, fiatPerUnit)
	net = ApplyBuySpread(gross, spreadBps)
	// SetFrac64(10000, 10000-spread) would divide by zero at 10000 bps; every
	// setter refuses that, so it is unreachable — kept guarded all the same.
	perUnit := new(big.Rat).Mul(fiatPerUnit, new(big.Rat).SetInt64(100))
	if spreadBps > 0 && spreadBps < 10000 {
		perUnit.Mul(perUnit, new(big.Rat).SetFrac64(10000, 10000-spreadBps))
	}
	spreadRate = new(big.Int).Quo(perUnit.Num(), perUnit.Denom())
	return gross, net, spreadRate
}
