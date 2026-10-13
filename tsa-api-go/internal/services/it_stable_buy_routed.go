package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
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
	DefaultRoutedConversionFeeBps int64 = 150

	// Owner ruling 2026-10-07: she pays the base rate grossed up by the
	// company take (RAMP_COMPANY_TAKE_BPS, every provider) plus each
	// third-party cost at cost. The funding fee is the measured cost of the
	// channel her naira arrives through — Flutterwave wallet funding into the
	// central account 1.40%, a virtual-account collection 2.15% (2.00% + VAT,
	// measured 2026-09-02) — so they are defaults, not settings an operator
	// has to make (RAMP_FUNDING_FEE_BPS / RAMP_VA_FUNDING_FEE_BPS override).
	DefaultRampCompanyTakeBps  int64 = config.DefaultCompanyTakeBps
	DefaultRampFundingFeeBps   int64 = 140
	DefaultRampVAFundingFeeBps int64 = 215

	DefaultRampDailyAbandonCap = 5
	DefaultRampHonourGrace     = 10 * time.Minute
	// A converted buy Flutterwave reports SUCCESSFUL without an on-chain hash
	// waits this long from the send before it parks (Decision C13).
	DefaultRampFlutterwaveHashWait = 10 * time.Minute

	DefaultTreasuryMinNGN          = "100"
	DefaultTreasuryMaxUSD          = "200"
	DefaultTreasuryDailyUserMaxUSD = "500"
	DefaultTreasurySendStall       = 30 * time.Minute

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
	FeeKindFunding    = "funding"
	FeeKindConversion = "conversion"
	FeeKindNetwork    = "network"
	// FeeKindPayout is the bank-transfer fee of the naira payouts a buy makes
	// on her behalf (the pool's LP payouts, one per slice).
	FeeKindPayout = "payout"
	// FeeProviderShown is the provider tag on every fee line she sees, ramp
	// and IT alike (owner rule, review I-4): never the name of the company
	// the cost is paid to. Set where the line is made, never patched later.
	FeeProviderShown = "provider"
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
	ConversionFeeBps int64
	SendFeeMode      string // SendFeeFromWallet | SendFeeFromAmount
}

type routedStableBuyLimits struct {
	minUnits, maxUnits, sendFeeUnits *big.Int
	conversionFeeBps                 int64
	sendFeeFromAmount                bool
}

// RampBuyTerms are shared by both routed providers. 0 keeps a value.
type RampBuyTerms struct {
	DailyAbandonCap     int           // cancelled routed buys per user per 24h
	HonourGrace         time.Duration // processing grace past RateExpiresAt
	FlutterwaveHashWait time.Duration // RAMP_FLUTTERWAVE_HASH_WAIT_MINUTES
	CompanyTakeBps      int64         // RAMP_COMPANY_TAKE_BPS: our only margin (config resolves it)
	FundingFeeBps       int64         // RAMP_FUNDING_FEE_BPS: central-account funding, at cost
	VAFundingFeeBps     int64         // RAMP_VA_FUNDING_FEE_BPS: virtual-account collection, at cost
}

type rampBuyTerms struct {
	abandonCap                              int
	honourGrace                             time.Duration
	fwHashWait                              time.Duration
	takeBps, fundingFeeBps, vaFundingFeeBps int64
}

// PayInChannel is the way her naira reaches us, which decides the funding
// fee she is passed: the central account (wallet funding) or her own virtual
// account (a collection). "" on a quote that carries no funding line.
type PayInChannel string

const (
	PayInCentral PayInChannel = "central"
	PayInVA      PayInChannel = "va"
)

// TreasuryBuyLimits configures the company wallet's buys. "" or 0 keeps a value.
type TreasuryBuyLimits struct {
	MinNGN          string // on naira paid
	MaxUSD          string // on delivered tokens; quote, open and live pricing
	DailyUserMaxUSD string // rolling 24h per user
	SendStall       time.Duration
	Confirmations   int // blocks a sale must have before it settles; 0 keeps
}

type treasuryBuyLimits struct {
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
		sendFeeUnits: mustUnits(DefaultRoutedSendFeeUSD), conversionFeeBps: DefaultRoutedConversionFeeBps,
	}
}

func (s *ITService) rampTerms() rampBuyTerms {
	if s.rampTerm != nil {
		return *s.rampTerm
	}
	return rampBuyTerms{abandonCap: DefaultRampDailyAbandonCap, honourGrace: DefaultRampHonourGrace,
		fwHashWait: DefaultRampFlutterwaveHashWait,
		takeBps:    DefaultRampCompanyTakeBps, fundingFeeBps: DefaultRampFundingFeeBps,
		vaFundingFeeBps: DefaultRampVAFundingFeeBps}
}

func (s *ITService) treasuryLimits() treasuryBuyLimits {
	if s.treasuryLims != nil {
		return *s.treasuryLims
	}
	minKobo, _ := nairaToKobo(DefaultTreasuryMinNGN)
	return treasuryBuyLimits{
		minKobo:  minKobo,
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

// ParseStableLimit is the one parser for a stablecoin limit in major units
// (boot checks and pricing share it): positive, or zero when allowZero.
func ParseStableLimit(v string, allowZero bool) (*big.Int, error) {
	u, err := StableToBaseUnits(strings.TrimSpace(v))
	if err != nil || u.Sign() < 0 || (u.Sign() == 0 && !allowZero) {
		return nil, fmt.Errorf("%q is not a usable amount", v)
	}
	return u, nil
}

// unitsSetting parses a major-unit stablecoin amount: "" keeps.
func unitsSetting(name, v string, dst **big.Int, allowZero bool, errs *[]error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return
	}
	u, err := ParseStableLimit(v, allowZero)
	if err != nil {
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
	switch {
	case t.FlutterwaveHashWait > 0:
		next.fwHashWait = t.FlutterwaveHashWait
	case t.FlutterwaveHashWait < 0:
		errs = append(errs, fmt.Errorf("RAMP_FLUTTERWAVE_HASH_WAIT_MINUTES=%s must not be negative", t.FlutterwaveHashWait))
	}
	// The take's rule is config's, shared with Paycrest: one value everywhere.
	switch {
	case t.CompanyTakeBps == 0:
	case config.ValidCompanyTakeBps(t.CompanyTakeBps):
		next.takeBps = t.CompanyTakeBps
	default:
		errs = append(errs, fmt.Errorf("RAMP_COMPANY_TAKE_BPS=%d must be between 1 and %d", t.CompanyTakeBps, config.MaxCompanyTakeBps))
	}
	bpsSetting("RAMP_FUNDING_FEE_BPS", t.FundingFeeBps, &next.fundingFeeBps, &errs)
	bpsSetting("RAMP_VA_FUNDING_FEE_BPS", t.VAFundingFeeBps, &next.vaFundingFeeBps, &errs)
	s.rampTerm = &next
	return errors.Join(errs...)
}

// SetTreasuryBuyLimits wires RAMP_TREASURY_* pricing and bounds.
func (s *ITService) SetTreasuryBuyLimits(l TreasuryBuyLimits) error {
	next := s.treasuryLimits()
	var errs []error
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

// floorRatMinor is r × 100 rounded down to a whole minor unit.
func floorRatMinor(r *big.Rat) *big.Int {
	m := new(big.Rat).Mul(r, big.NewRat(100, 1))
	return new(big.Int).Quo(m.Num(), m.Denom())
}

// formatBps renders bps as a trimmed percentage: 150 → "1.5".
func formatBps(bps int64) string {
	out := new(big.Rat).SetFrac64(bps, 100).FloatString(2)
	return strings.TrimSuffix(strings.TrimRight(out, "0"), ".")
}

// ── in-house pricing (owner ruling 2026-10-07) ─────────────────────────────

// passThroughCost is a third-party cost an in-house buy passes on at cost,
// shown as its own fee line: bps of what is left when it is charged, or a
// flat number of token units.
type passThroughCost struct {
	kind, label string
	bps         int64
	flat        *big.Int
}

// inHousePrice is one in-house buy price: the display rate, what she
// receives, and the fee lines between them.
type inHousePrice struct {
	gross, displayRate, net *big.Int
	fees                    []StableBuyFee
}

// inHouseDisplayRate is the rate an in-house buy shows: the base rate grossed
// up by the company take, floored to a kobo. The quote and the pool card both
// read it here (review M-2), so they cannot drift.
func inHouseDisplayRate(baseRate *big.Rat, takeBps int64) *big.Int {
	return floorRatMinor(new(big.Rat).Mul(baseRate, big.NewRat(10_000+takeBps, 10_000)))
}

// priceInHouseBuy is the one price of both in-house providers — the company
// wallet and Flutterwave — for a quote, an open and every live re-price alike.
// The display rate is the base rate (the market, or Flutterwave's own) grossed
// up by the company take and nothing else; every third-party cost then comes
// off what that rate buys, in order, at cost, one line each. So
// fiat ÷ displayRate − Σfees = net exactly (spec §14). Rounding is hers: the
// rate is rounded down to a kobo and each fee down to a base unit. net can be
// zero or negative; the caller's minimum refuses it.
func priceInHouseBuy(fiatMinor *big.Int, baseRate *big.Rat, takeBps int64, costs []passThroughCost) inHousePrice {
	p := inHousePrice{
		gross:       grossStableUnits(fiatMinor, baseRate),
		displayRate: inHouseDisplayRate(baseRate, takeBps),
	}
	if p.displayRate.Sign() <= 0 {
		p.net = new(big.Int)
		return p
	}
	left := lockedUnits(fiatMinor, p.displayRate)
	for _, c := range costs {
		units := new(big.Int)
		if c.flat != nil {
			units.Set(c.flat)
		} else {
			units.Mul(left, big.NewInt(c.bps)).Quo(units, big.NewInt(10_000))
		}
		if units.Sign() <= 0 {
			continue
		}
		left.Sub(left, units)
		ngn := new(big.Int).Mul(units, p.displayRate)
		p.fees = append(p.fees, StableBuyFee{Kind: c.kind, Label: c.label, Provider: FeeProviderShown,
			Units: units.String(), NgnMinor: ngn.Quo(ngn, stableUnitsPerToken()).String()})
	}
	p.net = left
	return p
}

// fundingCost is what the channel her naira arrives through costs: the
// central account's wallet-funding fee, or her virtual account's collection
// fee. Both are Flutterwave's, passed on at cost.
// An unknown or empty channel has no known cost, so it is refused rather than
// priced at either one.
func (s *ITService) fundingCost(ch PayInChannel) (passThroughCost, error) {
	var bps int64
	switch ch {
	case PayInCentral:
		bps = s.rampTerms().fundingFeeBps
	case PayInVA:
		bps = s.rampTerms().vaFundingFeeBps
	default:
		return passThroughCost{}, fmt.Errorf("%w: no funding cost for pay-in channel %q", ErrStableQuoteUnavailable, ch)
	}
	return passThroughCost{kind: FeeKindFunding, bps: bps,
		label: fmt.Sprintf("Funding fee (%s%%)", formatBps(bps))}, nil
}

// payInChannelNow is the channel a buy opened now is paid through — the same
// switch payIntoForStableBuy takes, so a quote and its order agree.
func (s *ITService) payInChannelNow() PayInChannel {
	if _, ok := s.ActiveCentralAccount(); ok {
		return PayInCentral
	}
	return PayInVA
}

// paidThrough is the channel intent's naira actually came through: central
// when its payment is a central-account credit (payment_ref is the credit's
// NIBSS session id), a virtual account otherwise. Before any payment it is
// the channel she would pay through now.
func (s *ITService) paidThrough(ctx context.Context, intent *models.ITIntent) (PayInChannel, error) {
	ref := strings.TrimSpace(intent.PaymentRef)
	if ref == "" {
		return s.payInChannelNow(), nil
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&models.InboundCredit{}).Where("provider_ref = ?", ref).Count(&n).Error; err != nil {
		return "", fmt.Errorf("it stable buy: which channel paid intent %s: %w", intent.ID, err)
	}
	if n > 0 {
		return PayInCentral, nil
	}
	return PayInVA, nil
}

// ── Flutterwave ────────────────────────────────────────────────────────────

// routedPrice is Flutterwave's price: its rate plus the take, less funding,
// its conversion fee and its send fee.
func (s *ITService) routedPrice(fiatMinor *big.Int, fiatPerUnit *big.Rat, ch PayInChannel) (inHousePrice, error) {
	lim := s.routedLimits()
	funding, err := s.fundingCost(ch)
	if err != nil {
		return inHousePrice{}, err
	}
	return priceInHouseBuy(fiatMinor, fiatPerUnit, s.rampTerms().takeBps, []passThroughCost{
		funding,
		{kind: FeeKindConversion, bps: lim.conversionFeeBps,
			label: fmt.Sprintf("Provider conversion fee (%s%%)", formatBps(lim.conversionFeeBps))},
		{kind: FeeKindNetwork, label: "Network fee", flat: lim.sendFeeUnits},
	}), nil
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
	return s.routedQuoteAt(asset, currency, fiat, fiatPerUnit, true, s.payInChannelNow())
}

// routedQuoteAt prices on Flutterwave's routed terms, for naira paid through
// ch. enforceMax is true for a quote she is shown, false for live pricing of
// money already received (owner 2026-10-04 #1: a minimum, never a maximum).
func (s *ITService) routedQuoteAt(asset, currency string, fiatMinor *big.Int, fiatPerUnit *big.Rat, enforceMax bool, ch PayInChannel) (*StableBuyQuote, error) {
	lim := s.routedLimits()
	p, err := s.routedPrice(fiatMinor, fiatPerUnit, ch)
	if err != nil {
		return nil, err
	}
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
		SendFeeUnits:     new(big.Int).Set(lim.sendFeeUnits),
		RatePerUnitMinor: ratePerDeliveredUnit(fiatMinor, p.net),
		DisplayRateMinor: p.displayRate,
		Fees:             p.fees,
		PayIn:            ch,
		ExpiresAt:        time.Now().Add(s.stableLockTTL()),
	}, nil
}

// quoteRoutedLive prices fiatMinor, paid through ch, at Flutterwave's live
// rate on routed terms (Decision D3). Never applies the maximum: the money
// has already arrived.
func (s *ITService) quoteRoutedLive(ctx context.Context, asset string, fiatMinor *big.Int, currency string, ch PayInChannel) (*StableBuyQuote, error) {
	rate, err := s.StableBuyFiatPerUnit(ctx, currency, asset)
	if err != nil {
		return nil, err
	}
	return s.routedQuoteAt(strings.ToUpper(asset), strings.ToUpper(currency), fiatMinor, rate, false, ch)
}

// ── company wallet ─────────────────────────────────────────────────────────

// QuoteTreasuryBuy prices fiatMajor naira at the market rate plus the
// company take, less the funding fee (spec §4.1, owner ruling 2026-10-07). Pure.
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
	return s.treasuryQuoteAt(asset, currency, fiat, marketRate, true, s.payInChannelNow())
}

// treasuryQuoteAt prices on company-wallet terms, for naira paid through ch.
// enforceMax is false only for an operator's "deliver anyway" (§13 Q9).
func (s *ITService) treasuryQuoteAt(asset, currency string, fiatMinor *big.Int, marketRate *big.Rat, enforceMax bool, ch PayInChannel) (*StableBuyQuote, error) {
	lim := s.treasuryLimits()
	if fiatMinor.Cmp(lim.minKobo) < 0 {
		return nil, fmt.Errorf("%w: minimum is %s %s", ErrStableBuyBelowMinimum, FormatMinor(lim.minKobo.String()), currency)
	}
	p, err := s.treasuryPrice(fiatMinor, marketRate, ch)
	if err != nil {
		return nil, err
	}
	if p.net.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount buys nothing", ErrStableBuyBelowMinimum)
	}
	if enforceMax && p.net.Cmp(lim.maxUnits) > 0 {
		return nil, fmt.Errorf("%w: maximum is %g %s", ErrStableBuyAboveMaximum, StableFromBaseUnits(lim.maxUnits), asset)
	}
	return &StableBuyQuote{
		Asset: asset, Currency: currency,
		FiatMinor: new(big.Int).Set(fiatMinor), GrossAssetUnits: p.gross, AssetUnits: p.net,
		RatePerUnitMinor: ratePerDeliveredUnit(fiatMinor, p.net), // Decision D42
		DisplayRateMinor: p.displayRate, Fees: p.fees, PayIn: ch,
		ExpiresAt: time.Now().Add(s.stableLockTTL()),
	}, nil
}

// treasuryPrice is the company wallet's price: the market plus the take, less
// the funding fee of the channel ch.
func (s *ITService) treasuryPrice(fiatMinor *big.Int, marketRate *big.Rat, ch PayInChannel) (inHousePrice, error) {
	funding, err := s.fundingCost(ch)
	if err != nil {
		return inHousePrice{}, err
	}
	return priceInHouseBuy(fiatMinor, marketRate, s.rampTerms().takeBps, []passThroughCost{funding}), nil
}

// TreasuryMinOrderUnits is what the company wallet's minimum naira buys at
// marketRate — the "smallest order" a token's free stock must cover to be
// offered at all (spec §5.3, closed leg).
func (s *ITService) TreasuryMinOrderUnits(marketRate *big.Rat) *big.Int {
	lim := s.treasuryLimits()
	if marketRate == nil || marketRate.Sign() <= 0 {
		return new(big.Int)
	}
	if p, err := s.treasuryPrice(lim.minKobo, marketRate, s.payInChannelNow()); err == nil && p.net.Sign() > 0 {
		return p.net
	}
	return new(big.Int)
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

// ── routed open seams (spec §4.2) ──────────────────────────────────────────

// IsRoutedStableBuy: a stablecoin buy the ramp router opened. Only that path
// sets RateExpiresAt on a buy (Decision D1).
func IsRoutedStableBuy(i *models.ITIntent) bool {
	return i != nil && i.Direction == models.ITDirectionBuy && IsStableAsset(i.Asset) && i.RateExpiresAt != nil
}

// StableBuyHonourUntil is when a routed buy's quote stops being honoured for
// money arriving; nil for every other intent.
func StableBuyHonourUntil(i *models.ITIntent) *time.Time {
	if !IsRoutedStableBuy(i) {
		return nil
	}
	t := *i.RateExpiresAt
	return &t
}

// StableBuyRateUpdated: a routed buy priced at a live rate instead of its
// quote. On an old-path buy Repriced means "priced at settlement", so it never
// reads as an update there.
func StableBuyRateUpdated(i *models.ITIntent) bool { return IsRoutedStableBuy(i) && i.Repriced }

// marshalFees is the stored form of a quote's fee lines; nil when there are none.
func marshalFees(fees []StableBuyFee) datatypes.JSON {
	if len(fees) == 0 {
		return nil
	}
	b, _ := json.Marshal(fees) // plain strings: cannot fail
	return datatypes.JSON(b)
}

// StableBuyFees decodes an intent's stored fee lines (spec §14). Unreadable
// JSON reads as none — the amounts on the intent stay authoritative. A line
// stored before review I-4 named the vendor; it reads as FeeProviderShown.
func StableBuyFees(i *models.ITIntent) []StableBuyFee {
	if i == nil || len(i.Fees) == 0 {
		return nil
	}
	var out []StableBuyFee
	if err := json.Unmarshal(i.Fees, &out); err != nil {
		return nil
	}
	for k := range out {
		out[k].Provider = FeeProviderShown
	}
	return out
}

// StableBuyDisplayRate is the rate shown beside an intent's fee lines, in
// minor units: the stored display rate, else the all-in rate.
func StableBuyDisplayRate(i *models.ITIntent) string {
	if i == nil {
		return ""
	}
	if d, ok := new(big.Int).SetString(strings.TrimSpace(i.DisplayRate), 10); ok && d.Sign() > 0 {
		return d.String()
	}
	return i.LockedRate
}

// RoutedDirectChain is the only chain Flutterwave's routed venue delivers to
// (spec 2.3 #6).
const RoutedDirectChain = "POLYGON"

// OpenRoutedStableBuy opens a purchase the ramp router chose Flutterwave for:
// no currency-routing check, the IT agreement, delivered to the user's own
// address on Polygon — never an address from the request.
func (s *ITService) OpenRoutedStableBuy(ctx context.Context, userID uuid.UUID, q *StableBuyQuote, destChain string) (*models.ITIntent, *StableBuyInstructions, error) {
	if !strings.EqualFold(strings.TrimSpace(destChain), RoutedDirectChain) {
		return nil, nil, fmt.Errorf("%w: this buy delivers on Polygon only, not %s",
			ErrUnsupportedChainAsset, strings.ToUpper(strings.TrimSpace(destChain)))
	}
	return s.openRoutedStableBuy(ctx, userID, q, routedDelivery{
		address:  func() (string, error) { return s.UserDeliveryAddress(ctx, userID, destChain) },
		validate: ValidateBuyDelivery,
	})
}

// routedDelivery is what differs between Flutterwave's routed opens (direct
// and converted): where it delivers — resolved after the shared checks — and
// how the row is written (nil: a plain insert).
type routedDelivery struct {
	address  func() (string, error)
	validate func(chain, asset, address string) error
	insert   func(ctx context.Context, intent *models.ITIntent) error
}

// openRoutedStableBuy is every routed Flutterwave open: the switch, the
// prechecks (KYC, the IT agreement, one open buy), the routed limits, on
// Polygon.
func (s *ITService) openRoutedStableBuy(ctx context.Context, userID uuid.UUID, q *StableBuyQuote, d routedDelivery) (*models.ITIntent, *StableBuyInstructions, error) {
	if !s.HasStableBuy() {
		return nil, nil, fmt.Errorf("%w: stablecoin purchases are not enabled", ErrStableQuoteUnavailable)
	}
	if err := s.stableBuyPrechecks(ctx, userID, q, true); err != nil {
		return nil, nil, err
	}
	addr, err := d.address()
	if err != nil {
		return nil, nil, err
	}
	lim := s.routedLimits()
	return s.openStableBuy(ctx, userID, q, RoutedDirectChain, addr, stableBuyOpen{
		provider: models.ITProviderFlutterwave, routed: true,
		minUnits: lim.minUnits, maxUnits: lim.maxUnits,
		validate: d.validate, insert: d.insert,
	})
}

// OpenStableBuy is the user's purchase awaiting payment, of any provider, or
// nil. Abandoned ones past the claim window are retired first, so a stale row
// never blocks the router's pre-flight.
func (s *ITService) OpenStableBuy(ctx context.Context, userID uuid.UUID) (*models.ITIntent, error) {
	if err := s.expireStaleStableBuys(ctx, userID); err != nil {
		return nil, err
	}
	return s.findOpenStableBuy(ctx, userID)
}

// CountAbandonedRoutedStableBuys counts the user's routed buys — either
// provider — cancelled or expired since `since` (spec §7, shared cap D14).
func (s *ITService) CountAbandonedRoutedStableBuys(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("user_id = ? AND direction = ? AND asset IN ? AND status = ? AND rate_expires_at IS NOT NULL AND updated_at >= ?",
			userID, models.ITDirectionBuy, []string{models.ITAssetUSDC, models.ITAssetUSDT},
			models.ITStatusCancelled, since).
		Count(&n).Error
	return n, err
}

// GetStableBuyForUser is the user's own stablecoin buy of `provider`. Another
// user's intent, another provider's, a sell or an MCGP order are all
// ErrStableIntentNotFound (spec §4.2: a foreign or other-provider id is a 404).
func (s *ITService) GetStableBuyForUser(ctx context.Context, userID, intentID uuid.UUID, provider string) (*models.ITIntent, error) {
	i, err := s.getIntentForUser(ctx, userID, intentID)
	if err != nil {
		return nil, err
	}
	if i.Direction != models.ITDirectionBuy || !IsStableAsset(i.Asset) || !intentIsProvider(i, provider) {
		return nil, ErrStableIntentNotFound
	}
	return i, nil
}

// StableBuyPayInto is where to pay for intent while it awaits the transfer;
// nil afterwards, so no screen invites a second transfer.
func (s *ITService) StableBuyPayInto(ctx context.Context, intent *models.ITIntent) (*StableBuyInstructions, error) {
	if !IsBuyAwaitingTransfer(intent) || !IsStableAsset(intent.Asset) {
		return nil, nil
	}
	amount, ok := new(big.Int).SetString(strings.TrimSpace(intent.AmountMinor), 10)
	if !ok || amount.Sign() <= 0 {
		return nil, fmt.Errorf("it stable buy: intent %s has no amount", intent.ID)
	}
	payInto, _, _, err := s.payIntoForStableBuy(ctx, intent.UserID, intent.Currency, amount)
	return payInto, err
}

// ── routed settlement (spec §5.2) ──────────────────────────────────────────

// rateUpdatePublisher is the slice of *events.Bus settlement uses.
type rateUpdatePublisher interface{ Publish(events.Event) }

// SetEventBus wires the "Rate updated" notice. nil disables it.
func (s *ITService) SetEventBus(b *events.Bus) {
	if b == nil {
		s.rateNotifier = nil
		return
	}
	s.rateNotifier = b
}

// routedArrival is how a routed buy's money is priced when it lands.
type routedArrival struct {
	units, rate *big.Int
	quote       *StableBuyQuote // the live quote; nil when honoured
	honoured    bool
	repriced    bool
	needsStock  bool // company wallet, priced live: win the stock under the lock
	// needsPoolCoverage: a pool buy, honoured or priced live — claimed only by
	// claimPool, under the pool lock, over the slices that still back it (S18,
	// review C-2)
	needsPoolCoverage bool
}

// setQuoteColumns writes a price onto a claim or re-price. display is nil on
// the old endpoint, which carries no breakdown.
func setQuoteColumns(cols map[string]any, units, rate, display *big.Int, fees []StableBuyFee) {
	cols["asset_amount"] = units.String()
	cols["locked_rate"] = rate.String()
	cols["rate_minor"] = rate.String()
	cols["rate_kobo"] = rate.String()
	if display != nil {
		cols["display_rate"] = display.String()
		cols["fees"] = marshalFees(fees)
	}
}

func (a *routedArrival) writeTo(claim map[string]any) {
	if a.quote != nil {
		setQuoteColumns(claim, a.units, a.rate, a.quote.DisplayRateMinor, a.quote.Fees)
	} else {
		setQuoteColumns(claim, a.units, a.rate, nil, nil) // honoured: the stored breakdown stands
	}
	// false when honoured: Repriced means "priced at a live rate" (D2).
	claim["repriced"] = a.repriced
}

// parkClaim turns a claim into a park: the money is recorded, the quote is left
// as it was, and the reason goes on the row (it is in the stuck list).
func parkClaim(claim map[string]any, reason string) {
	for _, k := range []string{"asset_amount", "locked_rate", "rate_minor", "rate_kobo", "display_rate", "fees", "repriced"} {
		delete(claim, k)
	}
	claim["status"] = models.ITStatusBuyAwaitingRate
	claim["failure_reason"] = truncateRunes(reason, 256)
}

// priceRoutedArrival applies D33: only the exact naira, arrived inside the
// window and processed inside the grace, is honoured. Everything else is
// priced live on the intent's own provider terms (D3).
func (s *ITService) priceRoutedArrival(ctx context.Context, intent *models.ITIntent, amountMinor string, arrivedAt time.Time, ch PayInChannel) (*routedArrival, error) {
	// S45 / P4: a provider without terms is never claimed, not even honoured.
	terms, err := providerTerms(intent)
	if err != nil {
		return nil, err
	}
	paid, ok := new(big.Int).SetString(strings.TrimSpace(amountMinor), 10)
	if !ok || paid.Sign() <= 0 {
		return nil, fmt.Errorf("%w: unusable amount %q", ErrStableBuyBelowMinimum, amountMinor)
	}
	expected, okE := new(big.Int).SetString(strings.TrimSpace(intent.AmountMinor), 10)
	expiry := *intent.RateExpiresAt
	onTime := !arrivedAt.After(expiry) && !time.Now().After(expiry.Add(s.rampTerms().honourGrace))
	if okE && expected.Cmp(paid) == 0 && onTime {
		units, okU := new(big.Int).SetString(strings.TrimSpace(intent.AssetAmount), 10)
		locked, okR := new(big.Int).SetString(strings.TrimSpace(intent.LockedRate), 10)
		if !okU || units.Sign() <= 0 || !okR || locked.Sign() <= 0 {
			return nil, fmt.Errorf("it stable buy: routed intent %s has no usable quote", intent.ID)
		}
		return &routedArrival{units: units, rate: locked, honoured: true, needsPoolCoverage: terms.pool}, nil
	}
	if terms.pool {
		return &routedArrival{repriced: true, needsPoolCoverage: true}, nil // priced under the lock
	}
	q, err := s.quoteRoutedLiveFor(ctx, intent, paid, ch)
	if err != nil {
		return nil, err
	}
	if err := s.convertedCeiling(ctx, intent, q.AssetUnits); err != nil {
		return nil, err
	}
	return &routedArrival{
		units: q.AssetUnits, rate: q.RatePerUnitMinor, quote: q, repriced: true,
		needsStock: terms.companyWallet,
	}, nil
}

// quoteRoutedLiveFor prices paid, which came through ch, live on the intent's
// provider terms: the company wallet always within its maximum. The one
// exception, an operator's "deliver anyway", calls quoteTreasuryLive itself.
func (s *ITService) quoteRoutedLiveFor(ctx context.Context, intent *models.ITIntent, paid *big.Int, ch PayInChannel) (*StableBuyQuote, error) {
	terms, err := providerTerms(intent)
	switch {
	case err != nil:
		return nil, err
	case terms.companyWallet:
		return s.quoteTreasuryLive(ctx, intent.Asset, paid, intent.Currency, true, ch)
	case terms.pool:
		return nil, errPoolPricedUnderLock
	case terms.flutterwaveFulfils:
		return s.quoteRoutedLive(ctx, intent.Asset, paid, intent.Currency, ch)
	default:
		return nil, fmt.Errorf("%w: %s has no live price", ErrUnknownITProvider, terms.provider)
	}
}

// quoteTreasuryLive prices fiatMinor, paid through ch, at the company
// wallet's current market rate on its in-house terms (take plus funding fee).
func (s *ITService) quoteTreasuryLive(ctx context.Context, asset string, fiatMinor *big.Int, currency string, enforceMax bool, ch PayInChannel) (*StableBuyQuote, error) {
	if s.treasuryRates == nil {
		return nil, fmt.Errorf("%w: no company-wallet market rate", ErrStableQuoteUnavailable)
	}
	market, err := s.treasuryRates.BuyRate(ctx, currency)
	if err != nil {
		return nil, err
	}
	return s.treasuryQuoteAt(strings.ToUpper(asset), strings.ToUpper(currency), fiatMinor, market, enforceMax, ch)
}

// creditArrival is when a central-account credit ARRIVED (D33): the statement's
// transaction time when it carries a time of day — but never later than when
// we first saw the credit, since a statement time is zone-less and parsed as
// UTC — else when we first saw it, else now. A date-only statement time reads
// as midnight.
func creditArrival(credit *models.InboundCredit, now time.Time) time.Time {
	if credit == nil {
		return now
	}
	t := credit.TransactionDate
	if !t.IsZero() && (t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 || t.Nanosecond() != 0) {
		if !credit.SeenAt.IsZero() && t.After(credit.SeenAt) {
			return credit.SeenAt
		}
		return t
	}
	if !credit.SeenAt.IsZero() {
		return credit.SeenAt
	}
	return now
}

// isDeterministicPricingError: the same money can never price, so retrying is
// pointless — park it on every path (D32).
func isDeterministicPricingError(err error) bool {
	return errors.Is(err, ErrStableBuyBelowMinimum) || errors.Is(err, ErrStableNotAnAsset) ||
		errors.Is(err, ErrStableBuyAboveMaximum) || errors.Is(err, ErrStableRateImplausible) ||
		errors.Is(err, ErrConvertedOverpaid) || errors.Is(err, ErrUnknownITProvider) ||
		errors.Is(err, errPoolNotRouted)
}

// parkReasonOf names a park for the ramp_treasury_parked metric.
func parkReasonOf(err error) string {
	switch {
	case errors.Is(err, errPoolNotRouted):
		return "pool_unrouted"
	case errors.Is(err, ErrUnknownITProvider):
		return "unknown_provider"
	case errors.Is(err, ErrConvertedOverpaid):
		return "converted_overpaid"
	case errors.Is(err, ErrStableBuyAboveMaximum):
		return "maximum"
	case errors.Is(err, ErrStableBuyBelowMinimum):
		return "minimum"
	case errors.Is(err, ErrStableNotAnAsset):
		return "not_an_asset"
	case errors.Is(err, ErrStableRateImplausible):
		return "rate_implausible"
	default:
		return "rate_unavailable"
	}
}

// routedMetric is the provider stem of the routed metric lines; "unknown" for
// a provider without terms, so nothing is misattributed to Flutterwave.
func routedMetric(i *models.ITIntent) string {
	t, err := providerTerms(i)
	if err != nil {
		return "unknown"
	}
	return t.metric
}

// stableBuyPriced: a routed buy is priced from the moment it opens, so
// Repriced=false on it is not the "claimed but never priced" state (D2).
func stableBuyPriced(i *models.ITIntent) bool { return i.Repriced || IsRoutedStableBuy(i) }

// quoteForIntent prices fiatMinor at the live rate on the terms the intent
// was opened under (D3). The company wallet is never re-priced here: its
// re-price must win stock under the lock (repriceTreasuryParked).
func (s *ITService) quoteForIntent(ctx context.Context, intent *models.ITIntent, fiatMinor string) (*StableBuyQuote, error) {
	// Review M1: the provider first, so an unknown one is never priced at
	// Flutterwave's old endpoint either.
	terms, err := providerTerms(intent)
	if err != nil {
		return nil, err
	}
	if terms.pool {
		return nil, errPoolPricedUnderLock // never Flutterwave's old endpoint, routed or not
	}
	if !IsRoutedStableBuy(intent) {
		return s.QuoteStableBuy(ctx, intent.Asset, fiatMinor, intent.Currency)
	}
	if terms.companyWallet {
		return nil, fmt.Errorf("%w: a company-wallet buy is re-priced only under its stock lock", ErrITBadState)
	}
	if !terms.flutterwaveFulfils {
		return nil, fmt.Errorf("%w: %s has no live price", ErrUnknownITProvider, terms.provider)
	}
	paid, ok := new(big.Int).SetString(strings.TrimSpace(fiatMinor), 10)
	if !ok || paid.Sign() <= 0 {
		return nil, fmt.Errorf("%w: unusable amount %q", ErrStableBuyBelowMinimum, fiatMinor)
	}
	ch, err := s.paidThrough(ctx, intent)
	if err != nil {
		return nil, err
	}
	q, err := s.quoteRoutedLive(ctx, intent.Asset, paid, intent.Currency, ch)
	if err != nil {
		return nil, err
	}
	if err := s.convertedCeiling(ctx, intent, q.AssetUnits); err != nil {
		return nil, err
	}
	return q, nil
}

// routedDeliveryAmounts is what a Flutterwave delivery sends and what the
// wallet must hold (D31). Every other buy sends and holds units.
func (s *ITService) routedDeliveryAmounts(intent *models.ITIntent, units *big.Int) (send, hold *big.Int) {
	send, hold = new(big.Int).Set(units), new(big.Int).Set(units)
	if !IsRoutedStableBuy(intent) || !isFlutterwaveBuy(intent) {
		return send, hold
	}
	lim := s.routedLimits()
	hold.Add(hold, lim.sendFeeUnits)
	if lim.sendFeeFromAmount {
		send.Add(send, lim.sendFeeUnits)
	}
	return send, hold
}

// logHonoured records what honouring cost: drift_bps > 0 means the live rate
// would have delivered less; for Flutterwave, shortfall_units is how much less
// (what the float covers). Best effort, after the claim.
func (s *ITService) logHonoured(ctx context.Context, intent *models.ITIntent, paidMinor string, honoured, locked *big.Int, ch PayInChannel) {
	drift, shortfall := "unknown", "unknown"
	if paid, ok := new(big.Int).SetString(strings.TrimSpace(paidMinor), 10); ok && locked.Sign() > 0 {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if q, err := s.quoteRoutedLiveFor(rctx, intent, paid, ch); err == nil && q.AssetUnits.Sign() > 0 {
			diff := new(big.Int).Sub(q.RatePerUnitMinor, locked)
			drift = diff.Mul(diff, big.NewInt(10000)).Quo(diff, locked).String()
			short := new(big.Int).Sub(honoured, q.AssetUnits)
			if short.Sign() < 0 {
				short.SetInt64(0)
			}
			shortfall = short.String()
		}
	}
	switch routedMetric(intent) {
	case "treasury":
		log.Printf("[it stable buy] METRIC ramp_treasury_honoured intent=%s drift_bps=%s", intent.ID, drift)
	case "fw":
		log.Printf("[it stable buy] METRIC ramp_fw_honoured intent=%s drift_bps=%s shortfall_units=%s", intent.ID, drift, shortfall)
	default:
		log.Printf("[it stable buy] METRIC ramp_unknown_honoured intent=%s provider=%q", intent.ID, intent.Provider)
	}
}

// notifyRateUpdated tells her a payment was priced at the live rate.
func (s *ITService) notifyRateUpdated(intent *models.ITIntent, units *big.Int) {
	if s.rateNotifier == nil {
		return
	}
	// A converted buy delivers T of her requested token, not the USDC leg D.
	asset, amount := intent.Asset, units
	if rec, err := s.ConversionFor(context.Background(), intent); err != nil {
		log.Printf("[it stable buy] rate notice intent=%s conversion: %s", intent.ID, RedactAlertText(err.Error()))
	} else if rec != nil && rec.RequestedToken != "" {
		asset, amount = rec.RequestedToken, ConversionOwed(units, rec.MarkupBps)
	}
	s.rateNotifier.Publish(events.Event{
		Type:    events.RampRateUpdated,
		UserID:  intent.UserID,
		Title:   "Rate updated",
		Message: fmt.Sprintf("Rate updated: you'll receive %s %s", FormatStableUnits(amount.String()), asset),
		Data: map[string]interface{}{
			"intentId": intent.ID.String(), "asset": asset, "assetAmount": amount.String(),
		},
	})
}
