package services

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

var (
	// ErrPoolOrdersStopped: pool_order_max = 0, the incident stop (S46).
	ErrPoolOrdersStopped = errors.New("liquidity pool: new orders are paused")
	// ErrPoolCentralOnly: a pool order is paid into the company account only
	// (S15): LP forwards must be paid from naira that has already settled.
	ErrPoolCentralOnly = errors.New("liquidity pool: payable only into the company account")
)

// PoolFeeProvider tags the pool's fee lines (review I5, §19): the buyer sees
// "provider", never the name of the company the cost is paid to.
const PoolFeeProvider = "provider"

// PoolPrice is one pool price (spec §10.1): the quote she sees and the
// snapshot an open writes into pool_orders.
type PoolPrice struct {
	Quote       *StableBuyQuote
	MidMinor    *big.Int // M, kobo per token, floored
	LPRateMinor *big.Int // L = floor(M × (1 + s)), kobo per token
	FundingBps  int64
	Settings    PoolSettings
}

// poolQuoteAt prices fiatMinor on pool terms: priceInHouseBuy with base
// M × (1 + pool_spread), take pool_take and the channel's funding cost (S14).
// enforceMax is true for a quote or an open, false for live pricing of money
// that already arrived (the minimum still applies; the stop and the maximum
// do not — S46: settlement keeps running).
func (s *ITService) poolQuoteAt(asset, currency string, fiatMinor *big.Int, mid *big.Rat, set PoolSettings, ch PayInChannel, enforceMax bool) (*PoolPrice, error) {
	if enforceMax && set.OrdersStopped {
		return nil, ErrPoolOrdersStopped
	}
	if ch != PayInCentral {
		return nil, ErrPoolCentralOnly
	}
	if mid == nil || mid.Sign() <= 0 {
		return nil, fmt.Errorf("%w: no usable market rate", ErrStableQuoteUnavailable)
	}
	funding, err := s.fundingCost(ch)
	if err != nil {
		return nil, err
	}
	funding.provider = PoolFeeProvider // review I5: never a provider's name in her quote
	base := new(big.Rat).Mul(mid, big.NewRat(10_000+set.SpreadBps, 10_000))
	p := priceInHouseBuy(fiatMinor, base, set.TakeBps, []passThroughCost{funding})
	if p.net.Sign() <= 0 || p.net.Cmp(set.MinSliceUnits) < 0 {
		return nil, fmt.Errorf("%w: minimum is %g %s", ErrStableBuyBelowMinimum, StableFromBaseUnits(set.MinSliceUnits), asset)
	}
	if enforceMax && p.net.Cmp(set.OrderMaxUnits) > 0 {
		return nil, fmt.Errorf("%w: maximum is %g %s", ErrStableBuyAboveMaximum, StableFromBaseUnits(set.OrderMaxUnits), asset)
	}
	return &PoolPrice{
		Quote: &StableBuyQuote{
			Asset: asset, Currency: currency,
			FiatMinor: new(big.Int).Set(fiatMinor), GrossAssetUnits: p.gross, AssetUnits: p.net,
			RatePerUnitMinor: ratePerDeliveredUnit(fiatMinor, p.net),
			DisplayRateMinor: p.displayRate, Fees: p.fees, PayIn: ch,
			ExpiresAt: time.Now().Add(s.stableLockTTL()),
		},
		MidMinor: floorRatMinor(mid), LPRateMinor: floorRatMinor(base),
		FundingBps: funding.bps, Settings: set,
	}, nil
}

// QuotePoolBuy prices fiatMajor naira of asset on pool terms at mid. Pure; the
// caller supplies the settings snapshot (CurrentPoolSettings in production).
func (s *ITService) QuotePoolBuy(asset, fiatMajor, currency string, mid *big.Rat, set PoolSettings) (*PoolPrice, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !IsStableAsset(asset) {
		return nil, fmt.Errorf("%w: got %q", ErrStableNotAnAsset, asset)
	}
	fiat, err := nairaToKobo(fiatMajor)
	if err != nil || fiat.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount must be positive with at most 2 decimals", ErrStableBuyBelowMinimum)
	}
	return s.poolQuoteAt(asset, currency, fiat, mid, set, s.payInChannelNow(), true)
}

// PoolLPPayoutKobo is an LP's naira for a slice: floor(units × L / 10^6)
// (spec §10.1). units are canonical.
func PoolLPPayoutKobo(units, lpRateMinor *big.Int) *big.Int {
	n := new(big.Int).Mul(units, lpRateMinor)
	return n.Quo(n, stableUnitsPerToken())
}

// PoolTakeKobo is the nominal take: fiat − floor(fiat × 10000 / (10000 + t)).
func PoolTakeKobo(fiatMinor *big.Int, takeBps int64) *big.Int {
	net := new(big.Int).Mul(fiatMinor, big.NewInt(10_000))
	net.Quo(net, big.NewInt(10_000+takeBps))
	return new(big.Int).Sub(fiatMinor, net)
}
