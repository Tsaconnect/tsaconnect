package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Buys the ramp router opens (docs/specs/2026-10-03-ramp-flutterwave-provider-design.md
// rev 3). Two providers write them into it_intents: Flutterwave, which buys and
// sends each order, and the company wallet, which sells the company's own
// stock. Both are quoted per token DELIVERED (the effective rate the router
// ranks on) and also as a display rate plus fee lines (spec §14).

// ErrStableBuyAboveMaximum caps what honouring a quote can cost (spec §2).
var ErrStableBuyAboveMaximum = errors.New("it stable buy: amount is above the maximum for this route")

const (
	DefaultRoutedMinTokens              = "10"
	DefaultRoutedMaxUSD                 = "2000"
	DefaultRoutedSendFeeUSD             = "1.50"
	DefaultRoutedSpreadBps        int64 = 250
	DefaultRoutedConversionFeeBps int64 = 150

	DefaultRampDailyAbandonCap = 5
	DefaultRampHonourGrace     = 10 * time.Minute

	DefaultTreasuryMarginBps       int64 = 250
	DefaultTreasuryMinNGN                = "100"
	DefaultTreasuryMaxUSD                = "200"
	DefaultTreasuryDailyUserMaxUSD       = "500"
	DefaultTreasurySendStall             = 30 * time.Minute

	// A company-wallet sale settles only this many blocks deep
	// (RAMP_TREASURY_CONFIRMATIONS; review fix 2026-10-05).
	DefaultTreasuryConfirmations = 5
)

// Where Flutterwave takes its external-send fee (Decision D31). The quote
// deducts it once either way.
const (
	SendFeeFromWallet = "wallet" // on top: send AssetAmount, hold AssetAmount + fee
	SendFeeFromAmount = "amount" // inside: send AssetAmount + fee
)

// Fee lines (spec §14, Decision D39).
const (
	FeeKindConversion      = "conversion"
	FeeKindNetwork         = "network"
	FeeProviderFlutterwave = "Flutterwave"
)

// StableBuyFee is one provider fee inside a quote, stored with the intent.
// Units are base units (6 dp); NgnMinor is its naira value at the display rate.
type StableBuyFee struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Provider string `json:"provider"`
	Units    string `json:"units"`
	NgnMinor string `json:"ngnMinor"`
}

// ── configuration ──────────────────────────────────────────────────────────

// RoutedStableBuyLimits configures Flutterwave's routed buys. Amounts in major
// units; a stablecoin counts as 1 USD (Decision D5). "" or 0 keeps a value.
type RoutedStableBuyLimits struct {
	MinTokens        string // delivered, after every fee
	MaxUSD           string // delivered tokens; quotes and opens only
	SendFeeUSD       string
	SpreadBps        int64
	ConversionFeeBps int64
	SendFeeMode      string // SendFeeFromWallet | SendFeeFromAmount
}

type routedStableBuyLimits struct {
	minUnits, maxUnits, sendFeeUnits *big.Int
	spreadBps, conversionFeeBps      int64
	sendFeeFromAmount                bool
}

// RampBuyTerms are shared by both routed providers. 0 keeps a value.
type RampBuyTerms struct {
	DailyAbandonCap int           // cancelled routed buys per user per 24h
	HonourGrace     time.Duration // processing grace past RateExpiresAt
}

type rampBuyTerms struct {
	abandonCap  int
	honourGrace time.Duration
}

// TreasuryBuyLimits configures the company wallet's buys. "" or 0 keeps a value.
type TreasuryBuyLimits struct {
	MarginBps       int64
	MinNGN          string // on naira paid
	MaxUSD          string // on delivered tokens; quote, open and live pricing
	DailyUserMaxUSD string // rolling 24h per user
	SendStall       time.Duration
	Confirmations   int // blocks a sale must have before it settles; 0 keeps
}

type treasuryBuyLimits struct {
	marginBps                            int64
	minKobo, maxUnits, dailyUserMaxUnits *big.Int
	sendStall                            time.Duration
	confirmations                        int
}

func mustUnits(v string) *big.Int {
	u, _ := StableToBaseUnits(v) // valid constants
	return u
}

func (s *ITService) routedLimits() routedStableBuyLimits {
	if s.routed != nil {
		return *s.routed
	}
	return routedStableBuyLimits{
		minUnits: mustUnits(DefaultRoutedMinTokens), maxUnits: mustUnits(DefaultRoutedMaxUSD),
		sendFeeUnits: mustUnits(DefaultRoutedSendFeeUSD),
		spreadBps:    DefaultRoutedSpreadBps, conversionFeeBps: DefaultRoutedConversionFeeBps,
	}
}

func (s *ITService) rampTerms() rampBuyTerms {
	if s.rampTerm != nil {
		return *s.rampTerm
	}
	return rampBuyTerms{abandonCap: DefaultRampDailyAbandonCap, honourGrace: DefaultRampHonourGrace}
}

func (s *ITService) treasuryLimits() treasuryBuyLimits {
	if s.treasuryLims != nil {
		return *s.treasuryLims
	}
	minKobo, _ := nairaToKobo(DefaultTreasuryMinNGN)
	return treasuryBuyLimits{
		marginBps: DefaultTreasuryMarginBps, minKobo: minKobo,
		maxUnits: mustUnits(DefaultTreasuryMaxUSD), dailyUserMaxUnits: mustUnits(DefaultTreasuryDailyUserMaxUSD),
		sendStall: DefaultTreasurySendStall, confirmations: DefaultTreasuryConfirmations,
	}
}

// bpsSetting applies a bps value: 0 keeps, 1..9999 sets, anything else errors.
func bpsSetting(name string, v int64, dst *int64, errs *[]error) {
	switch {
	case v == 0:
	case v > 0 && v < 10_000:
		*dst = v
	default:
		*errs = append(*errs, fmt.Errorf("%s=%d must be between 1 and 9999", name, v))
	}
}

// unitsSetting parses a major-unit stablecoin amount: "" keeps.
func unitsSetting(name, v string, dst **big.Int, allowZero bool, errs *[]error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return
	}
	u, err := StableToBaseUnits(v)
	if err != nil || u.Sign() < 0 || (u.Sign() == 0 && !allowZero) {
		*errs = append(*errs, fmt.Errorf("%s=%q is not a usable amount", name, v))
		return
	}
	*dst = u
}

// SetRoutedStableBuyLimits wires RAMP_FLUTTERWAVE_*. Decision D25: a bad value
// keeps the previous one and is returned as an error.
func (s *ITService) SetRoutedStableBuyLimits(l RoutedStableBuyLimits) error {
	prev := s.routedLimits()
	next := prev
	var errs []error
	unitsSetting("RAMP_FLUTTERWAVE_MIN_TOKENS", l.MinTokens, &next.minUnits, false, &errs)
	unitsSetting("RAMP_FLUTTERWAVE_MAX_USD", l.MaxUSD, &next.maxUnits, false, &errs)
	unitsSetting("RAMP_FLUTTERWAVE_SEND_FEE_USD", l.SendFeeUSD, &next.sendFeeUnits, true, &errs)
	bpsSetting("RAMP_FLUTTERWAVE_SPREAD_BPS", l.SpreadBps, &next.spreadBps, &errs)
	bpsSetting("RAMP_FLUTTERWAVE_CONVERSION_FEE_BPS", l.ConversionFeeBps, &next.conversionFeeBps, &errs)
	switch mode := strings.ToLower(strings.TrimSpace(l.SendFeeMode)); mode {
	case "":
	case SendFeeFromWallet:
		next.sendFeeFromAmount = false
	case SendFeeFromAmount:
		next.sendFeeFromAmount = true
	default:
		errs = append(errs, fmt.Errorf("RAMP_FLUTTERWAVE_SEND_FEE_MODE=%q must be %q or %q", l.SendFeeMode, SendFeeFromWallet, SendFeeFromAmount))
	}
	if next.minUnits.Cmp(next.maxUnits) > 0 {
		errs = append(errs, errors.New("RAMP_FLUTTERWAVE_MIN_TOKENS is above RAMP_FLUTTERWAVE_MAX_USD; both keep their previous values"))
		next.minUnits, next.maxUnits = prev.minUnits, prev.maxUnits
	}
	s.routed = &next
	return errors.Join(errs...)
}

// SetRampBuyTerms wires the terms both routed providers share.
func (s *ITService) SetRampBuyTerms(t RampBuyTerms) error {
	next := s.rampTerms()
	var errs []error
	switch {
	case t.DailyAbandonCap > 0:
		next.abandonCap = t.DailyAbandonCap
	case t.DailyAbandonCap < 0:
		errs = append(errs, fmt.Errorf("RAMP_DAILY_ABANDON_CAP=%d must be positive", t.DailyAbandonCap))
	}
	switch {
	case t.HonourGrace > 0:
		next.honourGrace = t.HonourGrace
	case t.HonourGrace < 0:
		errs = append(errs, fmt.Errorf("RAMP_HONOUR_GRACE_SECONDS=%s must not be negative", t.HonourGrace))
	}
	s.rampTerm = &next
	return errors.Join(errs...)
}

// SetTreasuryBuyLimits wires RAMP_TREASURY_* pricing and bounds.
func (s *ITService) SetTreasuryBuyLimits(l TreasuryBuyLimits) error {
	next := s.treasuryLimits()
	var errs []error
	bpsSetting("RAMP_TREASURY_MARGIN_BPS", l.MarginBps, &next.marginBps, &errs)
	if v := strings.TrimSpace(l.MinNGN); v != "" {
		if k, err := nairaToKobo(v); err != nil || k.Sign() <= 0 {
			errs = append(errs, fmt.Errorf("RAMP_TREASURY_MIN_NGN=%q is not a usable amount", v))
		} else {
			next.minKobo = k
		}
	}
	unitsSetting("RAMP_TREASURY_MAX_USD", l.MaxUSD, &next.maxUnits, false, &errs)
	unitsSetting("RAMP_TREASURY_DAILY_USER_MAX_USD", l.DailyUserMaxUSD, &next.dailyUserMaxUnits, false, &errs)
	switch {
	case l.SendStall > 0:
		next.sendStall = l.SendStall
	case l.SendStall < 0:
		errs = append(errs, fmt.Errorf("RAMP_TREASURY_SEND_STALL_MINUTES=%s must not be negative", l.SendStall))
	}
	switch {
	case l.Confirmations > 0:
		next.confirmations = l.Confirmations
	case l.Confirmations < 0:
		errs = append(errs, fmt.Errorf("RAMP_TREASURY_CONFIRMATIONS=%d must not be negative", l.Confirmations))
	}
	s.treasuryLims = &next
	return errors.Join(errs...)
}

// RoutedAbandonCap is how many routed buys a user may abandon per 24h (both providers).
func (s *ITService) RoutedAbandonCap() int { return s.rampTerms().abandonCap }

// RoutedSpreadBps is Flutterwave's routed margin (RAMP_FLUTTERWAVE_SPREAD_BPS).
func (s *ITService) RoutedSpreadBps() int64 { return s.routedLimits().spreadBps }

// ── shared arithmetic ──────────────────────────────────────────────────────

// StableBuyFiatPerUnit is Flutterwave's fiat per one whole token for a buy,
// plausibility-checked. Extracted from QuoteStableBuy so the adapter can cache it.
func (s *ITService) StableBuyFiatPerUnit(ctx context.Context, currency, asset string) (*big.Rat, error) {
	if !s.HasStableBuy() {
		return nil, fmt.Errorf("%w: stablecoin purchases are not enabled", ErrStableQuoteUnavailable)
	}
	asset = strings.ToUpper(strings.TrimSpace(asset))
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !IsStableAsset(asset) {
		return nil, fmt.Errorf("%w: got %q", ErrStableNotAnAsset, asset)
	}
	// Probe one whole asset unit. Fiat is the SOURCE, the asset the DESTINATION.
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
	return fiatPerUnit, nil
}

func stableUnitsPerToken() *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(StableAssetDecimals), nil)
}

// grossStableUnits = fiatMinor / 100 / fiatPerUnit * 10^6, floored.
func grossStableUnits(fiatMinor *big.Int, fiatPerUnit *big.Rat) *big.Int {
	g := new(big.Rat).SetInt(fiatMinor)
	g.Quo(g, new(big.Rat).SetInt64(100))
	g.Quo(g, fiatPerUnit)
	g.Mul(g, new(big.Rat).SetInt(stableUnitsPerToken()))
	return new(big.Int).Quo(g.Num(), g.Denom())
}

// ratePerDeliveredUnit is fiat minor units per whole delivered token, rounded
// UP (Decision D6).
func ratePerDeliveredUnit(fiatMinor, units *big.Int) *big.Int {
	num := new(big.Int).Mul(fiatMinor, stableUnitsPerToken())
	q, r := new(big.Int).QuoRem(num, units, new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// lockedUnits is what fiatMinor buys at a per-token rate, floored.
func lockedUnits(fiatMinor, ratePerUnit *big.Int) *big.Int {
	num := new(big.Int).Mul(fiatMinor, stableUnitsPerToken())
	return num.Quo(num, ratePerUnit)
}

// ceilRatMinor is r × 100 rounded up to a whole minor unit.
func ceilRatMinor(r *big.Rat) *big.Int {
	m := new(big.Rat).Mul(r, big.NewRat(100, 1))
	q, rem := new(big.Int).QuoRem(m.Num(), m.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// formatBps renders bps as a trimmed percentage: 150 → "1.5".
func formatBps(bps int64) string {
	out := new(big.Rat).SetFrac64(bps, 100).FloatString(2)
	return strings.TrimSuffix(strings.TrimRight(out, "0"), ".")
}

// ── Flutterwave ────────────────────────────────────────────────────────────

// routedPricing is one Flutterwave pricing: the effective path (gross →
// conversion fee → spread → send fee, Decision D37) and the display path
// (Decision D38).
type routedPricing struct {
	gross, afterFees, net       *big.Int
	displayRate, displayUnits   *big.Int
	conversionFee, sendFeeUnits *big.Int
	conversionFeeBps            int64
}

func (s *ITService) routedPrice(fiatMinor *big.Int, fiatPerUnit *big.Rat) routedPricing {
	lim := s.routedLimits()
	p := routedPricing{conversionFeeBps: lim.conversionFeeBps, sendFeeUnits: new(big.Int).Set(lim.sendFeeUnits)}
	p.gross = grossStableUnits(fiatMinor, fiatPerUnit)
	p.afterFees = ApplyBuySpread(ApplyBuySpread(p.gross, lim.conversionFeeBps), lim.spreadBps)
	p.net = new(big.Int).Sub(p.afterFees, lim.sendFeeUnits)
	// Display rate: Flutterwave's rate plus our spread, no provider fee,
	// rounded up to a kobo. The conversion line is whatever separates the
	// tokens that rate buys from the tokens actually left after the fee and
	// spread, so amount ÷ rate − Σfees = net exactly at the 2-dp rate.
	p.displayRate = ceilRatMinor(new(big.Rat).Mul(fiatPerUnit, big.NewRat(10000, 10000-lim.spreadBps)))
	p.displayUnits = lockedUnits(fiatMinor, p.displayRate)
	p.conversionFee = new(big.Int).Sub(p.displayUnits, p.afterFees)
	if p.conversionFee.Sign() < 0 {
		p.conversionFee.SetInt64(0) // a zero fee: the display rounding is then within one unit
	}
	return p
}

// fees renders the provider fee lines, each priced at the display rate.
func (p routedPricing) fees() []StableBuyFee {
	ngn := func(units *big.Int) string {
		n := new(big.Int).Mul(units, p.displayRate)
		return n.Quo(n, stableUnitsPerToken()).String()
	}
	var out []StableBuyFee
	if p.conversionFee.Sign() > 0 {
		out = append(out, StableBuyFee{
			Kind: FeeKindConversion, Provider: FeeProviderFlutterwave,
			Label: fmt.Sprintf("Flutterwave conversion fee (%s%%)", formatBps(p.conversionFeeBps)),
			Units: p.conversionFee.String(), NgnMinor: ngn(p.conversionFee),
		})
	}
	if p.sendFeeUnits.Sign() > 0 {
		out = append(out, StableBuyFee{
			Kind: FeeKindNetwork, Provider: FeeProviderFlutterwave, Label: "Network fee",
			Units: p.sendFeeUnits.String(), NgnMinor: ngn(p.sendFeeUnits),
		})
	}
	return out
}

// QuoteRoutedStableBuy prices fiatMajor naira at Flutterwave's fiatPerUnit
// (from StableBuyFiatPerUnit, usually cached by the adapter). Pure.
func (s *ITService) QuoteRoutedStableBuy(asset, fiatMajor, currency string, fiatPerUnit *big.Rat) (*StableBuyQuote, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !IsStableAsset(asset) {
		return nil, fmt.Errorf("%w: got %q", ErrStableNotAnAsset, asset)
	}
	if fiatPerUnit == nil || fiatPerUnit.Sign() <= 0 {
		return nil, fmt.Errorf("%w: no usable rate", ErrStableQuoteUnavailable)
	}
	fiat, err := nairaToKobo(fiatMajor)
	if err != nil || fiat.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount must be positive with at most 2 decimals", ErrStableBuyBelowMinimum)
	}
	return s.routedQuoteAt(asset, currency, fiat, fiatPerUnit, true)
}

// routedQuoteAt prices on Flutterwave's routed terms. enforceMax is true for
// a quote she is shown, false for live pricing of money already received
// (owner 2026-10-04 #1: a minimum, never a maximum).
func (s *ITService) routedQuoteAt(asset, currency string, fiatMinor *big.Int, fiatPerUnit *big.Rat, enforceMax bool) (*StableBuyQuote, error) {
	lim := s.routedLimits()
	p := s.routedPrice(fiatMinor, fiatPerUnit)
	if p.net.Cmp(lim.minUnits) < 0 {
		return nil, fmt.Errorf("%w: minimum delivery is %g %s after fees",
			ErrStableBuyBelowMinimum, StableFromBaseUnits(lim.minUnits), asset)
	}
	if enforceMax && p.net.Cmp(lim.maxUnits) > 0 {
		return nil, fmt.Errorf("%w: maximum is %g %s", ErrStableBuyAboveMaximum, StableFromBaseUnits(lim.maxUnits), asset)
	}
	return &StableBuyQuote{
		Asset: asset, Currency: currency,
		FiatMinor:        new(big.Int).Set(fiatMinor),
		GrossAssetUnits:  p.gross,
		AssetUnits:       p.net,
		SendFeeUnits:     p.sendFeeUnits,
		RatePerUnitMinor: ratePerDeliveredUnit(fiatMinor, p.net),
		DisplayRateMinor: p.displayRate,
		Fees:             p.fees(),
		ExpiresAt:        time.Now().Add(s.stableLockTTL()),
	}, nil
}

// quoteRoutedLive prices fiatMinor at Flutterwave's live rate on routed terms
// (Decision D3). Never applies the maximum: the money has already arrived.
func (s *ITService) quoteRoutedLive(ctx context.Context, asset string, fiatMinor *big.Int, currency string) (*StableBuyQuote, error) {
	rate, err := s.StableBuyFiatPerUnit(ctx, currency, asset)
	if err != nil {
		return nil, err
	}
	return s.routedQuoteAt(strings.ToUpper(asset), strings.ToUpper(currency), fiatMinor, rate, false)
}

// ── company wallet ─────────────────────────────────────────────────────────

// QuoteTreasuryBuy prices fiatMajor naira at the market rate plus the
// company wallet's margin (spec §4.1). Pure.
func (s *ITService) QuoteTreasuryBuy(asset, fiatMajor, currency string, marketRate *big.Rat) (*StableBuyQuote, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !IsStableAsset(asset) {
		return nil, fmt.Errorf("%w: got %q", ErrStableNotAnAsset, asset)
	}
	if marketRate == nil || marketRate.Sign() <= 0 {
		return nil, fmt.Errorf("%w: no usable market rate", ErrStableQuoteUnavailable)
	}
	fiat, err := nairaToKobo(fiatMajor)
	if err != nil || fiat.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount must be positive with at most 2 decimals", ErrStableBuyBelowMinimum)
	}
	return s.treasuryQuoteAt(asset, currency, fiat, marketRate, true)
}

// treasuryQuoteAt prices on company-wallet terms. enforceMax is false only for
// an operator's "deliver anyway" (§13 Q9).
func (s *ITService) treasuryQuoteAt(asset, currency string, fiatMinor *big.Int, marketRate *big.Rat, enforceMax bool) (*StableBuyQuote, error) {
	lim := s.treasuryLimits()
	if fiatMinor.Cmp(lim.minKobo) < 0 {
		return nil, fmt.Errorf("%w: minimum is %s %s", ErrStableBuyBelowMinimum, FormatMinor(lim.minKobo.String()), currency)
	}
	gross, net, _ := priceStableBuyAt(marketRate, fiatMinor, lim.marginBps)
	if net.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount buys nothing", ErrStableBuyBelowMinimum)
	}
	if enforceMax && net.Cmp(lim.maxUnits) > 0 {
		return nil, fmt.Errorf("%w: maximum is %g %s", ErrStableBuyAboveMaximum, StableFromBaseUnits(lim.maxUnits), asset)
	}
	rate := ratePerDeliveredUnit(fiatMinor, net) // Decision D42
	return &StableBuyQuote{
		Asset: asset, Currency: currency,
		FiatMinor: new(big.Int).Set(fiatMinor), GrossAssetUnits: gross, AssetUnits: net,
		RatePerUnitMinor: rate, DisplayRateMinor: new(big.Int).Set(rate),
		ExpiresAt: time.Now().Add(s.stableLockTTL()),
	}, nil
}

// TreasuryMinOrderUnits is what the company wallet's minimum naira buys at
// marketRate — the "smallest order" a token's free stock must cover to be
// offered at all (spec §5.3, closed leg).
func (s *ITService) TreasuryMinOrderUnits(marketRate *big.Rat) *big.Int {
	lim := s.treasuryLimits()
	if marketRate == nil || marketRate.Sign() <= 0 {
		return new(big.Int)
	}
	_, net, _ := priceStableBuyAt(marketRate, lim.minKobo, lim.marginBps)
	return net
}

// ── display helpers ────────────────────────────────────────────────────────

// FormatMinor renders fiat minor units as a 2-dp major string; "" if unreadable.
func FormatMinor(minor string) string {
	out, err := koboToNairaString(minor)
	if err != nil {
		return ""
	}
	return out
}

// FormatStableUnits renders 6-dp base units as a trimmed decimal ("94.5375").
func FormatStableUnits(units string) string {
	n, ok := new(big.Int).SetString(strings.TrimSpace(units), 10)
	if !ok || n.Sign() < 0 {
		return ""
	}
	out := new(big.Rat).SetFrac(n, stableUnitsPerToken()).FloatString(StableAssetDecimals)
	return strings.TrimSuffix(strings.TrimRight(out, "0"), ".")
}
