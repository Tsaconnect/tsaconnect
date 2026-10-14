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

// PoolPrice is one pool price (spec §10.1): the quote she sees and the
// snapshot an open writes into pool_orders.
type PoolPrice struct {
	Quote       *StableBuyQuote
	MidMinor    *big.Int // M, kobo per token, floored
	LPRateMinor *big.Int // L = floor(M × (1 + s)), kobo per token
	FundingBps  int64
	// Settings as priced; MinSliceUnits is the effective minimum slice
	// (poolEffectiveMinSlice), so every planner downstream honours it.
	Settings PoolSettings
	// SliceUnits are the slice sizes the order delivers on: a quote's estimate
	// from the cached stock, replaced by the open's actual allocation.
	SliceUnits []*big.Int
	// Guard is the worst rate she accepted, set by the opener; the open
	// refuses (price moved) when its own allocation prices worse than that.
	Guard PoolPriceGuard
	terms poolTerms // what a re-price on another plan needs
}

// PoolPriceGuard is the opener's price guard (CheckPriceGuard's limit/mode).
type PoolPriceGuard struct {
	Limit string
	Mode  PriceGuardMode
}

// poolTerms is everything a pool price derives from the rate, the settings
// and the channel, read once per price.
type poolTerms struct {
	set             PoolSettings // MinSliceUnits effective
	ch              PayInChannel
	mid, base       *big.Rat
	lpRate, display *big.Int
	funding         passThroughCost
	// vault: the shared pool (spec 2026-10-14) — one fill, no LP naira payout,
	// so neither a payout-fee line nor the payout's transfer minimum applies.
	vault bool
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
	t := poolTerms{set: set, ch: ch, mid: new(big.Rat).Set(mid), base: poolBaseRate(mid, set), funding: funding, vault: s.PoolVaultMode()}
	t.lpRate = floorRatMinor(t.base)
	t.display = inHouseDisplayRate(t.base, set.TakeBps)
	if !t.vault {
		t.set.MinSliceUnits = poolEffectiveMinSlice(set.MinSliceUnits, t.lpRate)
	}
	return t, nil
}

// poolEffectiveMinSlice is the smallest slice any planner may make:
// pool_min_slice, or the units whose LP payout reaches Flutterwave's NGN
// transfer minimum at the LP rate, whichever is larger — so no slice ever
// pays its LP under the floor (payout_below_transfer_minimum).
func poolEffectiveMinSlice(minSlice, lpRateMinor *big.Int) *big.Int {
	out := new(big.Int).Set(bigOrZeroInt(minSlice))
	if lpRateMinor == nil || lpRateMinor.Sign() <= 0 {
		return out
	}
	q, r := new(big.Int).QuoRem(new(big.Int).Mul(big.NewInt(fwNGNTransferMinKobo), stableUnitsPerToken()), lpRateMinor, new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if q.Cmp(out) > 0 {
		return q
	}
	return out
}

// poolQuoteAt prices fiatMinor on pool terms with the given slice sizes —
// the re-price of money that already arrived (enforceMax false) uses the
// order's own slices. See poolPriceOn.
func (s *ITService) poolQuoteAt(asset, currency string, fiatMinor *big.Int, mid *big.Rat, set PoolSettings, ch PayInChannel, enforceMax bool, slices []*big.Int) (*PoolPrice, error) {
	t, err := s.poolTermsAt(mid, set, ch, enforceMax)
	if err != nil {
		return nil, err
	}
	return poolPriceOn(t, asset, currency, fiatMinor, slices, enforceMax, time.Now().Add(s.stableLockTTL()))
}

// poolPriceOn is the pool price (owner, 2026-10-13: every actual cost, then
// the take — as the Flutterwave route does), pure: priceInHouseBuy with base
// M × (1 + pool_spread), take pool_take, and one fee line per cost — the
// channel's funding (S14), the treasury's gas for the slices (each one's open
// and release) and the delivery, and the LPs' naira payout fees. slices are
// the order's slice sizes; callers supply them. enforceMax is true for a
// quote or an open, false for live pricing of money that already arrived
// (the minimum still applies; the maximum does not — S46).
func poolPriceOn(t poolTerms, asset, currency string, fiatMinor *big.Int, slices []*big.Int, enforceMax bool, expires time.Time) (*PoolPrice, error) {
	costs := poolCosts(t.funding, slices, t.lpRate, t.display, t.set)
	if t.vault {
		costs = vaultCosts(t.funding, t.set)
	}
	p := priceInHouseBuy(fiatMinor, t.base, t.set.TakeBps, costs)
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
		MidMinor: floorRatMinor(t.mid), LPRateMinor: t.lpRate,
		FundingBps: t.funding.bps, Settings: t.set, SliceUnits: copyUnits(slices), terms: t,
	}, nil
}

// repriceOn is p re-priced on other slice sizes (the open's allocation),
// same rate, settings, money and expiry; the opener's guard is kept.
func (p *PoolPrice) repriceOn(slices []*big.Int) (*PoolPrice, error) {
	if p == nil || p.Quote == nil || p.terms.display == nil {
		return nil, fmt.Errorf("%w: the pool price carries no terms to re-price on", ErrStableQuoteUnavailable)
	}
	out, err := poolPriceOn(p.terms, p.Quote.Asset, p.Quote.Currency, p.Quote.FiatMinor, slices, true, p.Quote.ExpiresAt)
	if err != nil {
		return nil, err
	}
	out.Guard = p.Guard
	return out, nil
}

func copyUnits(in []*big.Int) []*big.Int {
	out := make([]*big.Int, len(in))
	for i, u := range in {
		out[i] = new(big.Int).Set(u)
	}
	return out
}

// poolCosts are a pool order's costs in the order they come off: funding,
// then the "Network fee" and "Payout fee" lines (flat token units). A zero
// line is left out by priceInHouseBuy.
func poolCosts(funding passThroughCost, slices []*big.Int, lpRateMinor, displayRateMinor *big.Int, set PoolSettings) []passThroughCost {
	return []passThroughCost{
		funding,
		{kind: FeeKindNetwork, label: "Network fee", flat: poolNetworkFeeUnits(len(slices), set)},
		{kind: FeeKindPayout, label: "Payout fee", flat: poolPayoutFeeUnits(slices, lpRateMinor, displayRateMinor)},
	}
}

// vaultCosts are a shared-pool order's costs: funding, then the network fee
// of its one fill (pool_gas_per_slice, the release key's call) and the
// delivery. No payout line: LPs are paid their spread in the vault itself.
func vaultCosts(funding passThroughCost, set PoolSettings) []passThroughCost {
	return []passThroughCost{funding, {kind: FeeKindNetwork, label: "Network fee", flat: poolNetworkFeeUnits(1, set)}}
}

// poolNetworkFeeUnits is the gas an order costs the treasury, in canonical
// units: slices × pool_gas_per_slice (each slice's open and adminRelease) +
// pool_gas_delivery (the send to her).
func poolNetworkFeeUnits(slices int, set PoolSettings) *big.Int {
	u := new(big.Int).Mul(big.NewInt(int64(slices)), bigOrZeroInt(set.GasPerSliceUnits))
	return u.Add(u, bigOrZeroInt(set.GasDeliveryUnits))
}

// poolPayoutFeeKobo is the bank-transfer fee of one naira LP payout, by
// Flutterwave's NGN transfer tiers (VAT inclusive). The quote prices on this
// table; the books record each payout's actual fee later (TransferFeeKobo).
func poolPayoutFeeKobo(payoutKobo *big.Int) *big.Int {
	switch {
	case payoutKobo.Cmp(big.NewInt(500_000)) <= 0: // ≤ ₦5,000
		return big.NewInt(1_075)
	case payoutKobo.Cmp(big.NewInt(5_000_000)) <= 0: // ≤ ₦50,000
		return big.NewInt(2_688)
	default:
		return big.NewInt(5_375)
	}
}

// poolPayoutFeeUnits is Σ each slice's payout fee (tiered on that slice's
// naira, units × L), in token units at the display rate, rounded down —
// priceInHouseBuy's rule: rounding is hers.
func poolPayoutFeeUnits(slices []*big.Int, lpRateMinor, displayRateMinor *big.Int) *big.Int {
	kobo := new(big.Int)
	for _, u := range slices {
		kobo.Add(kobo, poolPayoutFeeKobo(PoolLPPayoutKobo(u, lpRateMinor)))
	}
	if kobo.Sign() == 0 || displayRateMinor.Sign() <= 0 {
		return new(big.Int)
	}
	return kobo.Mul(kobo, stableUnitsPerToken()).Quo(kobo, displayRateMinor)
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

// QuotePoolBuy prices fiatMajor naira of asset on pool terms at mid. The
// caller supplies the settings snapshot (CurrentPoolSettings in production).
// Its per-slice costs are an estimate on the cached stock's plan; the open
// prices on its actual allocation (fitPlanPrice).
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
	slices, err := s.poolQuoteSlices(ctx, t, fiat)
	if err != nil {
		return nil, err
	}
	return poolPriceOn(t, asset, currency, fiat, slices, true, time.Now().Add(s.stableLockTTL()))
}

// poolQuoteSlices is the slice plan a quote prices its per-slice costs on:
// the allocator on the cached stock for what fiatMinor buys after funding
// (before the flat costs, so it can only over-count). An amount already below
// the minimum plans nothing (the price refuses it). With no pool desk
// (nothing can open) it is one slice of that amount. ErrPoolCannotFill when
// no allocation fits: the stock cannot cover the order.
func (s *ITService) poolQuoteSlices(ctx context.Context, t poolTerms, fiatMinor *big.Int) ([]*big.Int, error) {
	if t.display.Sign() <= 0 {
		return nil, nil
	}
	units := lockedUnits(fiatMinor, t.display)
	units.Sub(units, new(big.Int).Quo(new(big.Int).Mul(units, big.NewInt(t.funding.bps)), big.NewInt(10_000)))
	if units.Cmp(t.set.MinSliceUnits) < 0 {
		return nil, nil
	}
	if !s.HasPool() {
		return []*big.Int{units}, nil
	}
	return s.poolDesk.QuoteSliceUnits(ctx, units, t.set)
}

// PoolLPPayoutKobo is an LP's naira for a slice: floor(units × L / 10^6)
// (spec §10.1). units are canonical.
func PoolLPPayoutKobo(units, lpRateMinor *big.Int) *big.Int {
	n := new(big.Int).Mul(units, lpRateMinor)
	return n.Quo(n, stableUnitsPerToken())
}

// PoolTakeKobo is the nominal take: fiat − floor(fiat × 10000 / (10000 + t)).
// Plan C2's settlement arithmetic (the company's share of an order); no C1
// path calls it yet — the worked-example tests pin it ahead of C2 (review M-4).
func PoolTakeKobo(fiatMinor *big.Int, takeBps int64) *big.Int {
	net := new(big.Int).Mul(fiatMinor, big.NewInt(10_000))
	net.Quo(net, big.NewInt(10_000+takeBps))
	return new(big.Int).Sub(fiatMinor, net)
}
