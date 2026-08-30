package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

var (
	ErrStableQuoteUnavailable = errors.New("it stable: no live rate source configured")
	ErrStableNotAnAsset       = errors.New("it stable: asset must be USDC or USDT")
	ErrStableBelowMinimum     = errors.New("it stable: amount is below the minimum trade size")
	// ErrStableRateImplausible means the provider's quoted rate is too far from
	// the independent FX feed to be believed. Sandbox proved this is a real
	// failure mode, not a theoretical one.
	ErrStableRateImplausible = errors.New("it stable: provider rate failed the plausibility check")
)

// StableSellQuote is a priced, time-bounded stablecoin sell.
//
// GrossMinor is the trade's fiat value at Flutterwave's own rate; NetMinor is
// what the user is quoted after the company spread. The platform holds no
// position on this trade — the difference is margin realised only when the
// trade completes, never capital advanced to start one.
type StableSellQuote struct {
	Asset    string
	Currency string
	// AssetAmount is what the user sells, in asset base units (6 decimals).
	AssetAmount *big.Int
	// RatePerUnitMinor is fiat minor units per one whole asset unit, spread
	// already applied. This is the number locked onto the intent.
	RatePerUnitMinor *big.Int
	// GrossMinor is the trade's value at Flutterwave's own rate.
	// SpreadMinor is that less the company spread.
	// FeeMinor is the itemised fee line (platform fee + provider pass-through +
	// tax) which the spread does NOT replace — only the swap/routing bps legs
	// are replaced. NetMinor is what the user is actually quoted.
	GrossMinor  *big.Int
	SpreadMinor *big.Int
	FeeMinor    *big.Int
	NetMinor    *big.Int
	ExpiresAt   time.Time
}

// checkRatePlausible rejects a Flutterwave rate that cannot be true.
//
// This exists because the sandbox `/transfers/rates` endpoint returned
// 1,406,790 USDC as the cost of 1,000 NGN — wrong by roughly six orders of
// magnitude, with its own `rate` field internally consistent with the bad
// amounts. Pricing real money off an endpoint that can do that, without a
// sanity check, is how a user gets quoted a catastrophically wrong number.
//
// A stablecoin is ~1 USD by construction, so the fiat-per-unit rate must sit
// near the fiat-per-USD rate from the service's own independent FX feed — the
// same feed EffectiveKoboRates already trusts. Outside the band we quote
// NOTHING rather than guess, mirroring how CreateBuyIntent refuses to lock a
// quote against stale FX.
func (s *ITService) checkRatePlausible(ctx context.Context, fiatPerUnit *big.Rat, currency string) error {
	// stablePlausibilityRateFor, not ratePerUsdFor: the guard must refuse to
	// act on a rate it cannot trust, so a stale independent feed has to
	// collapse to "no opinion" here. `ok == false` means no independent
	// opinion is available, in which case there is nothing to assert and
	// quoting proceeds.
	perUSD, ok := s.stablePlausibilityRateFor(ctx, currency)
	if !ok || perUSD <= 0 {
		return nil
	}
	tol := s.stableRateTolBps
	if tol <= 0 {
		tol = 2500 // ±25%
	}
	ref := new(big.Rat).SetFloat64(perUSD)
	lo := new(big.Rat).Mul(ref, new(big.Rat).SetFrac64(10000-tol, 10000))
	hi := new(big.Rat).Mul(ref, new(big.Rat).SetFrac64(10000+tol, 10000))
	if fiatPerUnit.Cmp(lo) < 0 || fiatPerUnit.Cmp(hi) > 0 {
		return fmt.Errorf("%w: provider quoted %s %s per unit, independent feed says ~%.2f",
			ErrStableRateImplausible, fiatPerUnit.FloatString(4), currency, perUSD)
	}
	return nil
}

// stablePlausibilityRateFor resolves an independent fiat-per-USD rate for the
// plausibility guard, honouring staleness.
//
// This deliberately duplicates ratePerUsdFor's FxProvider/FxRateProvider
// type-assert and NGN fallback rather than reusing it. The two call sites have
// opposite requirements: it_fees.go's fee pricing wants a best-effort answer
// even from a stale feed (ratePerUsdFor discards the stale flag on purpose),
// while this guard exists specifically to REFUSE to act on a rate it cannot
// trust — so here a stale feed must collapse to "no opinion" (skip the check)
// rather than being treated as ground truth. Making ratePerUsdFor itself
// fail-closed on stale would silently change live fee pricing as a side
// effect, so that function is left untouched.
func (s *ITService) stablePlausibilityRateFor(ctx context.Context, currency string) (float64, bool) {
	if s.fx == nil {
		return 0, false
	}
	if multi, ok := s.fx.(FxRateProvider); ok {
		rate, stale, err := multi.RatePerUsd(ctx, currency)
		if err != nil || stale || rate <= 0 {
			return 0, false
		}
		return rate, true
	}
	if !strings.EqualFold(strings.TrimSpace(currency), "NGN") {
		return 0, false
	}
	rate, stale, err := s.fx.NgnPerUsd(ctx)
	if err != nil || stale || rate <= 0 {
		return 0, false
	}
	return rate, true
}

// SetStableRateToleranceBps sets how far the provider rate may stray from the
// independent FX feed before a quote is refused. 0 ⇒ 2500 (±25%).
func (s *ITService) SetStableRateToleranceBps(bps int64) { s.stableRateTolBps = bps }

// SetStableSpreadBps wires the company margin for brokered stablecoin trades.
//
// Rejects >= 10000 bps: QuoteStableBuy's per-unit rate divides by
// (10000-spread), and a 100%+ spread would either divide by zero or invert
// the price. Unreachable via any configured value today, but this is the
// single place that guards the money-path panic rather than each caller.
func (s *ITService) SetStableSpreadBps(bps int64) error {
	if bps >= 10000 {
		return fmt.Errorf("it stable: spread must be less than 10000 bps, got %d", bps)
	}
	s.stableSpreadBps = bps
	return nil
}

// SetStableRateLockTTL bounds how long a quoted rate is honoured.
func (s *ITService) SetStableRateLockTTL(d time.Duration) { s.stableRateLockTTL = d }

func (s *ITService) stableLockTTL() time.Duration {
	if s.stableRateLockTTL <= 0 {
		return 15 * time.Minute
	}
	return s.stableRateLockTTL
}

// DefaultStableMinSellMajor is the sell floor applied when none is configured.
//
// Flutterwave documents a 10 USDC/USDT floor. The app enforces the same number
// (utils/stableSellQuote.ts), but a client-side check does not protect a direct
// API caller — which is why this exists. Below the floor the provider rejects
// the conversion AFTER the user has irreversibly sent funds, so the quote must
// refuse first: this is the only point in the flow where refusing is still free.
const DefaultStableMinSellMajor = "10"

// SetStableMinSell wires the provider floor from config, in major units.
// An empty or unparseable value leaves the default in place rather than
// removing the floor — a money guard must not be disabled by a typo.
func (s *ITService) SetStableMinSell(amountMajor string) error {
	amountMajor = strings.TrimSpace(amountMajor)
	if amountMajor == "" {
		return nil
	}
	units, err := StableToBaseUnits(amountMajor)
	if err != nil {
		return fmt.Errorf("it stable minimum %q: %w", amountMajor, err)
	}
	if units.Sign() < 0 {
		return fmt.Errorf("it stable minimum %q: must not be negative", amountMajor)
	}
	s.stableMinSellUnits = units
	return nil
}

// stableMinSell returns the configured floor, or the documented default.
// It never returns nil, so the floor cannot be lost by leaving config unset.
func (s *ITService) stableMinSell() *big.Int {
	if s.stableMinSellUnits != nil {
		return s.stableMinSellUnits
	}
	units, err := StableToBaseUnits(DefaultStableMinSellMajor)
	if err != nil {
		// Unreachable: the constant is a valid 6dp decimal.
		return big.NewInt(0)
	}
	return units
}

// QuoteStableSell prices selling `amountMajor` of `asset` for `currency`.
//
// The rate comes from Flutterwave because Flutterwave is who the trade actually
// executes against — pricing off the P2P feed would quote one market and settle
// in another. The spread is the entire company margin (the bps legs are not
// charged on brokered flows), and it is also the buffer absorbing FX drift
// inside the lock window and provider-fee estimation error.
func (s *ITService) QuoteStableSell(ctx context.Context, asset, amountMajor, currency string) (*StableSellQuote, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !IsStableAsset(asset) {
		return nil, fmt.Errorf("%w: got %q", ErrStableNotAnAsset, asset)
	}
	if s.rateQuoter == nil {
		return nil, ErrStableQuoteUnavailable
	}

	units, err := StableToBaseUnits(amountMajor)
	if err != nil {
		return nil, err
	}
	if units.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount must be positive", ErrStableBelowMinimum)
	}
	// The provider floor. Checked here because QuoteStableSell is the single
	// chokepoint: PostStableSell re-quotes server-side rather than trusting a
	// client-supplied quote, so both /stable/quote and /stable/sell pass through
	// this line and neither can route around it.
	if min := s.stableMinSell(); units.Cmp(min) < 0 {
		return nil, fmt.Errorf("%w: minimum sell is %g %s",
			ErrStableBelowMinimum, StableFromBaseUnits(min), asset)
	}

	// Probe one whole destination unit and reuse the implied per-unit rate.
	// Direction matters: asset is the SOURCE, fiat the DESTINATION.
	rate, err := s.rateQuoter.QuoteRate(ctx, asset, currency, 1)
	if err != nil {
		return nil, fmt.Errorf("it stable quote: %w", err)
	}
	fiatPerUnit, err := DeriveFiatPerUnit(rate)
	if err != nil {
		return nil, err
	}
	if err := s.checkRatePlausible(ctx, fiatPerUnit, currency); err != nil {
		return nil, err
	}

	// gross = units * fiatPerUnit * 100 / 10^6, floored.
	//
	// All five registry currencies are 2-decimal, so the ×100 is correct for
	// every currency this ships with; a 0- or 3-decimal currency would be what
	// forces this to become exponent-aware, exactly as it_automation.go notes.
	exp := new(big.Int).Exp(big.NewInt(10), big.NewInt(StableAssetDecimals), nil)
	grossRat := new(big.Rat).SetInt(units)
	grossRat.Mul(grossRat, fiatPerUnit)
	grossRat.Mul(grossRat, new(big.Rat).SetInt64(100))
	grossRat.Quo(grossRat, new(big.Rat).SetInt(exp))
	gross := new(big.Int).Quo(grossRat.Num(), grossRat.Denom())

	net := ApplySellSpread(gross, s.stableSpreadBps)

	// Per-unit rate at the spread price, for the intent's rate lock.
	perUnit := new(big.Rat).Mul(fiatPerUnit, new(big.Rat).SetInt64(100))
	perUnit.Mul(perUnit, new(big.Rat).SetFrac64(10000-s.stableSpreadBps, 10000))
	ratePerUnit := new(big.Int).Quo(perUnit.Num(), perUnit.Denom())

	q := &StableSellQuote{
		Asset:            asset,
		Currency:         currency,
		AssetAmount:      units,
		RatePerUnitMinor: ratePerUnit,
		GrossMinor:       gross,
		SpreadMinor:      net,
		FeeMinor:         big.NewInt(0),
		NetMinor:         net,
		ExpiresAt:        time.Now().Add(s.stableLockTTL()),
	}
	if err := s.applyStableSellFees(ctx, q); err != nil {
		return nil, err
	}
	return q, nil
}

// ErrStableFeeExceedsProceeds means the itemised fees are at least the trade's
// value after spread — there is nothing left to pay the user.
var ErrStableFeeExceedsProceeds = errors.New("it stable: fees exceed the trade proceeds")

// applyStableSellFees charges the itemised fee line on top of the spread.
//
// The spread replaces only the swap and routing BPS legs (see
// FeeQuoteService.swapLegs) — the platform fee, the Flutterwave pass-through
// and the tax line are separate charges and still apply. A nil quoter is
// tolerated so dev environments without the fee engine still price a trade,
// matching how every other optional dependency in this service degrades.
//
// Chain is pinned to feeChainPolygon: QuoteStableSell prices a sell before any
// delivery chain is chosen (that happens when the intent is created), and a
// brokered stablecoin flow charges 0 swap bps on every chain regardless (see
// swapLegs) — so the choice only matters to satisfy FeeQuoteService's
// chain-required guard for stablecoin withdrawals, not to change the price.
func (s *ITService) applyStableSellFees(ctx context.Context, q *StableSellQuote) error {
	if s.feeQuoter == nil {
		return nil
	}
	// FeeQuoteInput.Amount is a decimal string in major units of Currency
	// (nairaToKobo re-multiplies by 100) — SpreadMinor is already minor units,
	// so it must be rendered back to a 2dp decimal here, not passed as-is.
	// Passing it raw would silently inflate every fee quote 100x.
	amountMajor, err := koboToNairaString(q.SpreadMinor.String())
	if err != nil {
		return fmt.Errorf("it stable quote: %w", err)
	}
	// The platform fee on stablecoin flows is a flat USD amount
	// (config.PlatformStableUSD) that FeeQuoteService folds into Currency via
	// FxMinorPerUSD — required for any non-USD currency, else Quote returns
	// ErrFeeQuoteNeedFx (see fee_quote_service.go). Resolved the same
	// best-effort way GetQuote's handler does for the same field: ok==false
	// (fx unavailable) leaves it nil and the fee engine's own ErrFeeQuoteNeedFx
	// surfaces naturally — this is fee pricing, not the price-safety guard, so
	// ratePerUsdFor's best-effort-on-stale behaviour is correct here, not
	// stablePlausibilityRateFor's fail-on-stale.
	var fxMinorPerUSD *big.Int
	if !strings.EqualFold(strings.TrimSpace(q.Currency), "USD") {
		if usdRate, ok := s.ratePerUsdFor(ctx, q.Currency); ok {
			if minor, mErr := MinorPerUSDFromRate(usdRate); mErr == nil {
				fxMinorPerUSD = minor
			}
		}
	}
	fees, err := s.feeQuoter.Quote(ctx, FeeQuoteInput{
		Flow:            FeeFlowWithdraw,
		Asset:           FeeAsset(q.Asset),
		Chain:           feeChainPolygon,
		Amount:          amountMajor,
		Currency:        q.Currency,
		PaymentCurrency: q.Currency,
		FxMinorPerUSD:   fxMinorPerUSD,
	})
	if err != nil {
		return fmt.Errorf("it stable quote: %w", err)
	}
	if fees == nil || fees.TotalFees.Minor == nil {
		return nil
	}
	// Viable is the fee engine's own dust check — fees meeting or exceeding the
	// amount. Trust it rather than re-deriving the same rule here.
	if !fees.Viable {
		return fmt.Errorf("%w: fee=%s proceeds=%s", ErrStableFeeExceedsProceeds, fees.TotalFees.Minor, q.SpreadMinor)
	}
	q.FeeMinor = new(big.Int).Set(fees.TotalFees.Minor)
	net := new(big.Int).Sub(q.SpreadMinor, q.FeeMinor)
	if net.Sign() <= 0 {
		return fmt.Errorf("%w: fee=%s proceeds=%s", ErrStableFeeExceedsProceeds, q.FeeMinor, q.SpreadMinor)
	}
	q.NetMinor = net
	return nil
}
