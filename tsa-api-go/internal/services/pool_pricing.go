package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

var (
	// ErrPoolOrdersStopped: pool_order_max = 0, the incident stop (S46).
	ErrPoolOrdersStopped = errors.New("liquidity pool: new orders are paused")
	// ErrPoolCentralOnly: a pool order is paid into the company account only
	// (S15): LP forwards must be paid from naira that has already settled.
	ErrPoolCentralOnly = errors.New("liquidity pool: payable only into the company account")
)

// PoolPrice is one pool price (spec 2026-10-14 §4): the quote she sees and
// the snapshot an open writes into pool_orders.
type PoolPrice struct {
	Quote       *StableBuyQuote
	MidMinor    *big.Int // M, kobo per token, floored
	LPRateMinor *big.Int // L = floor(M × (1 + s)): what the LPs effectively sell at
	FundingBps  int64
	Settings    PoolSettings // as priced
	// Guard is the worst rate she accepted, set by the opener.
	Guard PoolPriceGuard
}

// PoolPriceGuard is the opener's price guard (CheckPriceGuard's limit/mode).
type PoolPriceGuard struct {
	Limit string
	Mode  PriceGuardMode
}

// poolTerms is everything a pool price derives from the rate, the settings
// and the channel, read once per price.
type poolTerms struct {
	set             PoolSettings
	ch              PayInChannel
	mid, base       *big.Rat
	lpRate, display *big.Int
	funding         passThroughCost
}

// poolTermsAt gates a pool price — the incident stop (quotes and opens only)
// and a channel other than central — and derives its terms.
func (s *ITService) poolTermsAt(mid *big.Rat, set PoolSettings, ch PayInChannel, enforceMax bool) (poolTerms, error) {
	if enforceMax && set.OrdersStopped {
		return poolTerms{}, ErrPoolOrdersStopped
	}
	if ch != PayInCentral {
		return poolTerms{}, ErrPoolCentralOnly
	}
	if mid == nil || mid.Sign() <= 0 {
		return poolTerms{}, fmt.Errorf("%w: no usable market rate", ErrStableQuoteUnavailable)
	}
	funding, err := s.fundingCost(ch)
	if err != nil {
		return poolTerms{}, err
	}
	t := poolTerms{set: set, ch: ch, mid: new(big.Rat).Set(mid), base: poolBaseRate(mid, set), funding: funding}
	t.lpRate = floorRatMinor(t.base)
	t.display = inHouseDisplayRate(t.base, set.TakeBps)
	return t, nil
}

// poolQuoteAt prices fiatMinor on pool terms; enforceMax false is the live
// re-price of money that already arrived (the minimum still applies).
func (s *ITService) poolQuoteAt(asset, currency string, fiatMinor *big.Int, mid *big.Rat, set PoolSettings, ch PayInChannel, enforceMax bool) (*PoolPrice, error) {
	t, err := s.poolTermsAt(mid, set, ch, enforceMax)
	if err != nil {
		return nil, err
	}
	return poolPriceOn(t, asset, currency, fiatMinor, enforceMax, time.Now().Add(s.stableLockTTL()))
}

// poolPriceOn is the pool price (owner, 2026-10-13: every actual cost, then
// the take), pure: priceInHouseBuy with base M × (1 + pool_spread), take
// pool_take, and one fee line per cost — the channel's funding and the
// network fee of the order's one fill and its delivery.
func poolPriceOn(t poolTerms, asset, currency string, fiatMinor *big.Int, enforceMax bool, expires time.Time) (*PoolPrice, error) {
	p := priceInHouseBuy(fiatMinor, t.base, t.set.TakeBps, poolCosts(t.funding, t.set))
	if p.net.Sign() <= 0 || p.net.Cmp(t.set.MinSliceUnits) < 0 {
		return nil, fmt.Errorf("%w: minimum is %g %s", ErrStableBuyBelowMinimum, StableFromBaseUnits(t.set.MinSliceUnits), asset)
	}
	if enforceMax && p.net.Cmp(t.set.OrderMaxUnits) > 0 {
		return nil, fmt.Errorf("%w: maximum is %g %s", ErrStableBuyAboveMaximum, StableFromBaseUnits(t.set.OrderMaxUnits), asset)
	}
	return &PoolPrice{
		Quote: &StableBuyQuote{
			Asset: asset, Currency: currency,
			FiatMinor: new(big.Int).Set(fiatMinor), GrossAssetUnits: p.gross, AssetUnits: p.net,
			RatePerUnitMinor: ratePerDeliveredUnit(fiatMinor, p.net),
			DisplayRateMinor: p.displayRate, Fees: p.fees, PayIn: t.ch,
			ExpiresAt: expires,
		},
		MidMinor: floorRatMinor(t.mid), LPRateMinor: t.lpRate, FundingBps: t.funding.bps, Settings: t.set,
	}, nil
}

// poolCosts are a pool order's costs: funding, then the network fee of its
// one fill (pool_gas_per_slice, the operator's call) and the delivery
// (pool_gas_delivery). LPs are paid their spread in the vault itself, so no
// payout line. A zero line is left out by priceInHouseBuy.
func poolCosts(funding passThroughCost, set PoolSettings) []passThroughCost {
	return []passThroughCost{funding, {kind: FeeKindNetwork, label: "Network fee", flat: poolNetworkFeeUnits(set)}}
}

// poolNetworkFeeUnits is the gas an order costs, in canonical units.
func poolNetworkFeeUnits(set PoolSettings) *big.Int {
	return new(big.Int).Add(bigOrZeroInt(set.GasPerSliceUnits), bigOrZeroInt(set.GasDeliveryUnits))
}

// FeeKindFees / PoolFeesLabel: the one line a pool buy's costs are shown as.
const (
	FeeKindFees   = "fees"
	PoolFeesLabel = "Fees"
)

// poolCostKinds are the lines poolCosts makes.
var poolCostKinds = map[string]bool{FeeKindFunding: true, FeeKindNetwork: true, FeeKindPayout: true}

// PoolUserFeeLines is what she is shown of a TSA pool buy's fee lines (owner,
// 2026-10-13): its own costs — funding, network and payout — as one "Fees"
// line at the first one's place: their units summed, its naira that sum at
// the display rate (one floor, as every line is priced). Any other line (a
// converted buy's conversion) stands as given. Stored lines keep the detail
// (books, admin views): merge only on the way out to her.
func PoolUserFeeLines(fees []StableBuyFee, displayRateMinor *big.Int) []StableBuyFee {
	out := make([]StableBuyFee, 0, len(fees))
	merged, units := -1, new(big.Int)
	for _, f := range fees {
		if !poolCostKinds[f.Kind] {
			out = append(out, f)
			continue
		}
		if merged < 0 {
			merged = len(out)
			out = append(out, StableBuyFee{Kind: FeeKindFees, Label: PoolFeesLabel, Provider: FeeProviderShown})
		}
		if u, ok := new(big.Int).SetString(strings.TrimSpace(f.Units), 10); ok {
			units.Add(units, u)
		}
	}
	if merged >= 0 {
		ngn := new(big.Int)
		if displayRateMinor != nil && displayRateMinor.Sign() > 0 {
			ngn.Mul(units, displayRateMinor).Quo(ngn, stableUnitsPerToken())
		}
		out[merged].Units, out[merged].NgnMinor = units.String(), ngn.String()
	}
	return out
}

// UserFeeLines is the fee lines she is shown for intent: a pool buy's merged
// (PoolUserFeeLines), every other buy's as stored.
func UserFeeLines(i *models.ITIntent) []StableBuyFee {
	if isPoolBuy(i) {
		return PoolUserFeeLines(StableBuyFees(i), intentDisplayRate(i))
	}
	return StableBuyFees(i)
}

// ShowUserFees rewrites a response copy of intent so its fee lines are the
// ones she is shown (UserFeeLines). Never call it on a row that is saved.
func ShowUserFees(i *models.ITIntent) {
	if i == nil || len(i.Fees) == 0 || !isPoolBuy(i) {
		return
	}
	i.Fees = marshalFees(PoolUserFeeLines(StableBuyFees(i), intentDisplayRate(i)))
}

func intentDisplayRate(i *models.ITIntent) *big.Int {
	d, _ := new(big.Int).SetString(StableBuyDisplayRate(i), 10)
	return d
}

// poolBaseRate is the LP's rate the pool prices on: M × (1 + pool_spread).
func poolBaseRate(mid *big.Rat, set PoolSettings) *big.Rat {
	return new(big.Rat).Mul(mid, big.NewRat(10_000+set.SpreadBps, 10_000))
}

// QuotePoolBuy prices fiatMajor naira of asset on pool terms at mid, if the
// vault's free liquidity covers what it delivers. The caller supplies the
// settings snapshot (CurrentPoolSettings in production).
func (s *ITService) QuotePoolBuy(ctx context.Context, asset, fiatMajor, currency string, mid *big.Rat, set PoolSettings) (*PoolPrice, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !IsStableAsset(asset) {
		return nil, fmt.Errorf("%w: got %q", ErrStableNotAnAsset, asset)
	}
	fiat, err := nairaToKobo(fiatMajor)
	if err != nil || fiat.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount must be positive with at most 2 decimals", ErrStableBuyBelowMinimum)
	}
	t, err := s.poolTermsAt(mid, set, s.payInChannelNow(), true)
	if err != nil {
		return nil, err
	}
	p, err := poolPriceOn(t, asset, currency, fiat, true, time.Now().Add(s.stableLockTTL()))
	if err != nil {
		return nil, err
	}
	if s.HasPool() {
		// The free-liquidity read is cached for 5 s; the gates are not, so a
		// pause (or any closed gate) stops quotes at once, not 5 s later.
		if g := s.poolDesk.c.Gates; g != nil && !g.Open() {
			return nil, ErrPoolClosed
		}
		free, err := s.poolDesk.FreeUnits(ctx, set)
		if err != nil {
			return nil, err
		}
		if free.Cmp(p.Quote.AssetUnits) < 0 {
			return nil, ErrPoolCannotFill
		}
	}
	return p, nil
}

// PoolTakeKobo is the nominal take: fiat − floor(fiat × 10000 / (10000 + t)).
// Plan C2's settlement arithmetic (the company's share of an order); no C1
// path calls it yet — the worked-example tests pin it ahead of C2 (review M-4).
func PoolTakeKobo(fiatMinor *big.Int, takeBps int64) *big.Int {
	net := new(big.Int).Mul(fiatMinor, big.NewInt(10_000))
	net.Quo(net, big.NewInt(10_000+takeBps))
	return new(big.Int).Sub(fiatMinor, net)
}
