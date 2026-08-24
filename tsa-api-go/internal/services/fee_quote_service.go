package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

// Fee-quote engine (PM fee spec, batch 11) — the "user sees total fees before
// confirming" layer for the four fiat ramps:
//
//	deposit  fiat → MCGP          platform 0.5%/2%       no swap fee
//	deposit  fiat → USDC/USDT     platform $X (config)   conversion bps
//	withdraw MCGP → fiat          platform 0.5%/2%       mcgpSwap + lifiRoute bps
//	withdraw USDC/USDT → fiat     platform $X (config)   lifiRoute bps (already on
//	                                                     Polygon) else both legs
//
// The MCGP platform fee is tiered by PAYMENT CURRENCY — 0.5% on NGN, 2% on
// every other currency (PM spec, 28/07) — and is the only line the platform
// retains. On top of every flow: the provider (Flutterwave) fee — passthrough,
// quoted live via GetFees when the client is up, else a config fallback
// percentage — and the tax line (VAT + platform tax) whose BASE is config:
// "fee" (the provider fee, matching the PM's receipt) or "amount".
//
// This engine only PRICES. The Instant Transfer legs deduct from it (see
// it_fees.go) so the quote the user is shown is the one the money follows.
//
// All money math is big.Int over minor units (kobo/cents) — float64 never
// touches an amount. Percentages are basis points over bpsDenom (it_service.go).

// FeeFlow is the money direction being quoted.
type FeeFlow string

const (
	FeeFlowDeposit  FeeFlow = "deposit"
	FeeFlowWithdraw FeeFlow = "withdraw"
)

// FeeAsset is the crypto side of the ramp.
type FeeAsset string

const (
	FeeAssetMCGP FeeAsset = "MCGP"
	FeeAssetUSDC FeeAsset = "USDC"
	FeeAssetUSDT FeeAsset = "USDT"
)

// IsStable reports whether the asset is a platform-fee-bearing stablecoin.
func (a FeeAsset) IsStable() bool { return a == FeeAssetUSDC || a == FeeAssetUSDT }

// feeChainPolygon is the chain key (config.Networks vocabulary, lowercase)
// where stablecoin withdrawals skip the transfer leg and pay only LiFi routing.
const feeChainPolygon = "polygon"

var (
	ErrFeeQuoteBadFlow     = errors.New("fee quote: flow must be deposit or withdraw")
	ErrFeeQuoteBadAsset    = errors.New("fee quote: asset must be MCGP, USDC or USDT")
	ErrFeeQuoteBadAmount   = errors.New("fee quote: amount must be a positive 2dp decimal")
	ErrFeeQuoteBadCurrency = errors.New("fee quote: unsupported currency")
	ErrFeeQuoteNeedChain   = errors.New("fee quote: chain is required for stablecoin withdrawals")
	ErrFeeQuoteNeedFx      = errors.New("fee quote: USD fx rate required for stablecoin flows in this currency")
)

// ProviderFeeQuoter quotes the provider's own fee for a prospective
// collection/payout, in minor units of currency. ok=false means no live quote
// is available (dormant client, network error, unusable response) — the
// calculator then falls back to the configured percentage. Implementations
// must never panic on a nil receiver-less call path; the service treats a nil
// quoter as "always fall back".
type ProviderFeeQuoter interface {
	QuoteProviderFee(ctx context.Context, flow FeeFlow, amountMinor *big.Int, currency string) (fee *big.Int, ok bool)
}

// FeeQuoteInput is one quote request. Amount is a decimal string in Currency
// (max 2dp, e.g. "500" / "500.50"); Chain uses the wallet chain-key vocabulary
// ("polygon", "sonic", …) and is only consulted for stablecoin withdrawals.
//
// FxMinorPerUSD is how many minor units of Currency equal 1 USD (kobo per
// dollar for NGN, 100 for USD) — needed only to price the flat USD platform
// fee on stablecoin flows. Nil is fine for USD (implied 100) and for all MCGP
// flows, whose platform fee is a percentage of the amount already in Currency.
type FeeQuoteInput struct {
	Flow     FeeFlow
	Asset    FeeAsset
	Chain    string
	Amount   string
	Currency string
	// PaymentCurrency is the currency the user actually pays in or is paid out
	// in, which selects the platform-fee tier. It differs from Currency only on
	// a cross-currency rail: an IT sell to a Ghanaian bank is VALUED in naira
	// (the sell rate prices MCGP in NGN and the gateway converts at settlement)
	// but PAID in GHS, so it owes the 2% FX tier, not the 0.5% local one.
	// Empty means "same as Currency".
	PaymentCurrency string
	FxMinorPerUSD   *big.Int
}

// FeeLine is one itemized fee: minor units plus the 2dp display string. It
// marshals Minor as a STRING (repo convention for kobo/cents — big values
// must survive JS clients intact).
type FeeLine struct {
	Minor   *big.Int `json:"-"`
	Display string   `json:"display"`
}

// MarshalJSON renders {"minor":"<decimal string>","display":"…"}. Do NOT embed
// FeeLine in another struct — the promoted method would hijack the outer
// type's marshaling; hold it as a named field instead.
func (l FeeLine) MarshalJSON() ([]byte, error) {
	minor := "0"
	if l.Minor != nil {
		minor = l.Minor.String()
	}
	return json.Marshal(map[string]string{"minor": minor, "display": l.Display})
}

// FeeTaxQuote is the tax line: VAT + platform tax over the recorded basis.
type FeeTaxQuote struct {
	// Basis is what the percentages were applied to: "fee" (provider fee) or
	// "amount" (gross). Recorded so receipts stay explainable after a config
	// flip.
	Basis          string  `json:"basis"`
	Base           FeeLine `json:"base"`
	VATBps         int64   `json:"vatBps"`
	VAT            FeeLine `json:"vat"`
	PlatformTaxBps int64   `json:"platformTaxBps"`
	PlatformTax    FeeLine `json:"platformTax"`
	Total          FeeLine `json:"total"`
}

// FeeProviderQuote is the provider-fee line with its provenance.
type FeeProviderQuote struct {
	Fee FeeLine `json:"fee"`
	// Source is "live" (provider GetFees) or "fallback" (config percentage).
	Source string `json:"source"`
	// FallbackBps is the percentage used when Source is "fallback"; 0 on live.
	FallbackBps int64 `json:"fallbackBps,omitempty"`
}

// FeePlatformQuote is the platform's own fee — the only line the platform
// actually keeps. It is priced one of two ways:
//
//	MCGP ramps      a percentage of the amount, tiered by fiat currency
//	                (Bps set, USDCents 0)
//	stablecoin ramps a flat USD amount converted into the quote currency
//	                (USDCents set, Bps 0)
type FeePlatformQuote struct {
	USDCents int64  `json:"usdCents"`
	USD      string `json:"usd"`
	// Bps is the percentage charged when the fee is rate-based; 0 for the flat
	// USD stablecoin fee. Recorded so a receipt stays explainable after a
	// config change.
	Bps int64 `json:"bps,omitempty"`
	// InCurrency is the fee in the quote currency — the authoritative amount
	// however it was derived.
	InCurrency FeeLine `json:"inCurrency"`
}

// FeeSwapQuote is the swap/conversion line.
type FeeSwapQuote struct {
	Fee FeeLine `json:"fee"`
	Bps int64   `json:"bps"`
	// Legs names the bps components ("mcgpSwap", "lifiRoute", "conversion")
	// so the app can explain 3% vs 5% withdrawals.
	Legs []string `json:"legs,omitempty"`
}

// FeeQuote is the itemized result. All money is minor units of Currency.
type FeeQuote struct {
	Flow     FeeFlow  `json:"flow"`
	Asset    FeeAsset `json:"asset"`
	Chain    string   `json:"chain,omitempty"`
	Currency string   `json:"currency"`
	Amount   FeeLine  `json:"amount"`

	PlatformFee FeePlatformQuote `json:"platformFee"`
	ProviderFee FeeProviderQuote `json:"providerFee"`
	Tax         FeeTaxQuote      `json:"tax"`
	SwapFee     FeeSwapQuote     `json:"swapFee"`

	TotalFees FeeLine `json:"totalFees"`
	// NetAmount is Amount − TotalFees, clamped at zero — every fee in this
	// spec is deducted from the deposited/withdrawn amount, never added on
	// top. Viable is false when fees meet or exceed the amount (dust).
	NetAmount FeeLine `json:"netAmount"`
	Viable    bool    `json:"viable"`

	// AssetOutEstimate is how much MCGP/USDC/USDT (deposit) or fiat (withdraw)
	// the user would receive for NetAmount. Left nil until the conversion-rate
	// wiring lands (needs the IT/OTC rate feeds; out of quote-engine scope).
	AssetOutEstimate *string `json:"assetOutEstimate"`
}

// FeeQuoteService is a pure calculator over the injected config; the only
// side-effectful collaborator is the optional live provider-fee quoter.
type FeeQuoteService struct {
	cfg      config.FeeQuoteConfig
	provider ProviderFeeQuoter
}

// NewFeeQuoteService builds the calculator. provider may be nil — every quote
// then prices the provider fee from the fallback percentage, which keeps the
// endpoint dormant-safe (no Flutterwave credentials required).
func NewFeeQuoteService(cfg config.FeeQuoteConfig, provider ProviderFeeQuoter) *FeeQuoteService {
	if cfg.TaxBasis != "amount" {
		// Unknown basis degrades to the receipt-backed default, never an error
		// — a typo'd env var must not take quoting down.
		cfg.TaxBasis = "fee"
	}
	return &FeeQuoteService{cfg: cfg, provider: provider}
}

// Quote computes the itemized fee breakdown for one prospective deposit or
// withdrawal.
func (s *FeeQuoteService) Quote(ctx context.Context, in FeeQuoteInput) (*FeeQuote, error) {
	flow := FeeFlow(strings.ToLower(strings.TrimSpace(string(in.Flow))))
	if flow != FeeFlowDeposit && flow != FeeFlowWithdraw {
		return nil, ErrFeeQuoteBadFlow
	}
	asset := FeeAsset(strings.ToUpper(strings.TrimSpace(string(in.Asset))))
	if asset != FeeAssetMCGP && asset != FeeAssetUSDC && asset != FeeAssetUSDT {
		return nil, ErrFeeQuoteBadAsset
	}
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if !isKnownCurrency(currency) {
		return nil, fmt.Errorf("%w: %q", ErrFeeQuoteBadCurrency, in.Currency)
	}
	payCurrency := strings.ToUpper(strings.TrimSpace(in.PaymentCurrency))
	if payCurrency == "" {
		payCurrency = currency
	} else if !isKnownCurrency(payCurrency) {
		// Never fall through to the cheap local tier on a typo — an unpriceable
		// rail must fail loudly, not bill at 0.5%.
		return nil, fmt.Errorf("%w: payment currency %q", ErrFeeQuoteBadCurrency, in.PaymentCurrency)
	}
	chain := strings.ToLower(strings.TrimSpace(in.Chain))
	if flow == FeeFlowWithdraw && asset.IsStable() && chain == "" {
		// The 3%-vs-5% split hinges on where the funds sit — refuse to guess.
		return nil, ErrFeeQuoteNeedChain
	}

	amount, err := nairaToKobo(in.Amount) // generic 2dp decimal → minor units
	if err != nil || amount.Sign() <= 0 {
		return nil, ErrFeeQuoteBadAmount
	}

	// Platform fee — the platform's own cut, priced two different ways:
	//   MCGP ramps       a percentage tiered by payment currency (0.5% NGN,
	//                    2% every other currency), on BOTH legs
	//   stablecoin ramps the legacy flat USD amount, converted via fx
	// The stablecoin rails are deliberately untouched by the 28/07 currency
	// tiering — the PM specified the MCGP fiat ramps and has not ruled on the
	// stable ones; charging both would silently double the PM-side fee.
	platformCents := int64(0)
	platformBps := int64(0)
	platformMinor := big.NewInt(0)
	if asset.IsStable() {
		platformCents = s.cfg.PlatformStableUSD * 100
		fx := in.FxMinorPerUSD
		if fx == nil && currency == "USD" {
			fx = big.NewInt(100) // 100 cents per USD
		}
		if platformCents > 0 {
			if fx == nil || fx.Sign() <= 0 {
				return nil, ErrFeeQuoteNeedFx
			}
			// cents × (minor per USD) / 100.
			platformMinor = new(big.Int).Mul(big.NewInt(platformCents), fx)
			platformMinor.Div(platformMinor, big.NewInt(100))
		}
	} else {
		platformBps = s.platformBps(payCurrency)
		platformMinor = bpsShare(amount, platformBps)
	}

	// Provider fee — live passthrough when a quoter is wired and answers,
	// else the config fallback percentage of the gross amount.
	provider := FeeProviderQuote{Source: "fallback", FallbackBps: s.cfg.ProviderFallbackBps}
	providerMinor := bpsShare(amount, s.cfg.ProviderFallbackBps)
	if s.provider != nil {
		if live, ok := s.provider.QuoteProviderFee(ctx, flow, amount, currency); ok && live != nil && live.Sign() >= 0 {
			providerMinor = live
			provider.Source = "live"
			provider.FallbackBps = 0
		}
	}
	provider.Fee = feeLine(providerMinor)

	// Tax — VAT + platform tax over the configured basis.
	taxBase := providerMinor
	if s.cfg.TaxBasis == "amount" {
		taxBase = amount
	}
	vat := bpsShare(taxBase, s.cfg.VATBps)
	platformTax := bpsShare(taxBase, s.cfg.PlatformTaxBps)
	tax := FeeTaxQuote{
		Basis:          s.cfg.TaxBasis,
		Base:           feeLine(taxBase),
		VATBps:         s.cfg.VATBps,
		VAT:            feeLine(vat),
		PlatformTaxBps: s.cfg.PlatformTaxBps,
		PlatformTax:    feeLine(platformTax),
		Total:          feeLine(new(big.Int).Add(vat, platformTax)),
	}

	// Swap/conversion fee — withdrawals + stablecoin deposits only.
	swapBps, legs := s.swapLegs(flow, asset, chain)
	swapMinor := bpsShare(amount, swapBps)
	swap := FeeSwapQuote{Fee: feeLine(swapMinor), Bps: swapBps, Legs: legs}

	total := new(big.Int).Add(platformMinor, providerMinor)
	total.Add(total, tax.Total.Minor)
	total.Add(total, swapMinor)

	net := new(big.Int).Sub(amount, total)
	viable := net.Sign() > 0
	if !viable {
		net = big.NewInt(0)
	}

	return &FeeQuote{
		Flow:     flow,
		Asset:    asset,
		Chain:    chain,
		Currency: currency,
		Amount:   feeLine(amount),
		PlatformFee: FeePlatformQuote{
			USDCents:   platformCents,
			USD:        fmt.Sprintf("%d.%02d", platformCents/100, platformCents%100),
			Bps:        platformBps,
			InCurrency: feeLine(platformMinor),
		},
		ProviderFee: provider,
		Tax:         tax,
		SwapFee:     swap,
		TotalFees:   feeLine(total),
		NetAmount:   feeLine(net),
		Viable:      viable,
	}, nil
}

// platformBps is the platform's cut for a payment currency: the NGN tier for
// naira, the FX tier for everything else. Naira is cheaper because the local
// rail costs the platform less to operate than the cross-border ones.
func (s *FeeQuoteService) platformBps(payCurrency string) int64 {
	if payCurrency == "NGN" {
		return s.cfg.PlatformNgnBps
	}
	return s.cfg.PlatformFxBps
}

// swapLegs resolves the swap/conversion bps + its named components:
//
//	MCGP withdraw            mcgpSwap + lifiRoute        (default 2%+3% = 5%)
//	stable withdraw          lifiRoute on Polygon (3%),  mcgpSwap acts as the
//	                         transfer leg elsewhere      (default 2%+3% = 5%)
//	stable deposit           conversion                  (default 2%)
//	MCGP deposit             none
func (s *FeeQuoteService) swapLegs(flow FeeFlow, asset FeeAsset, chain string) (int64, []string) {
	switch {
	case flow == FeeFlowWithdraw && asset == FeeAssetMCGP:
		return s.cfg.McgpSwapBps + s.cfg.LifiRouteBps, []string{"mcgpSwap", "lifiRoute"}
	case flow == FeeFlowWithdraw: // stablecoin
		if chain == feeChainPolygon {
			return s.cfg.LifiRouteBps, []string{"lifiRoute"}
		}
		return s.cfg.McgpSwapBps + s.cfg.LifiRouteBps, []string{"mcgpSwap", "lifiRoute"}
	case flow == FeeFlowDeposit && asset.IsStable():
		return s.cfg.StableDepositConvBps, []string{"conversion"}
	default: // MCGP deposit
		return 0, nil
	}
}

// bpsShare is amount × bps / 10000, floored — the same integer bps math the
// IT rate engine uses. Non-positive bps yields zero.
func bpsShare(amount *big.Int, bps int64) *big.Int {
	if amount == nil || amount.Sign() <= 0 || bps <= 0 {
		return big.NewInt(0)
	}
	n := new(big.Int).Mul(amount, big.NewInt(bps))
	return n.Div(n, big.NewInt(bpsDenom))
}

func feeLine(minor *big.Int) FeeLine {
	display, err := koboToNairaString(minor.String()) // generic minor → 2dp
	if err != nil {
		display = "0.00"
	}
	return FeeLine{Minor: minor, Display: display}
}

// MinorPerUSDFromRate converts a float FX rate (units of currency per USD,
// e.g. the P2P feed's NgnPerUsd) into minor units per USD, rounded half-up.
// This is the one sanctioned float boundary: the FX feed is float-native, and
// the value is a RATE, not an amount — every subsequent money op is big.Int.
func MinorPerUSDFromRate(rate float64) (*big.Int, error) {
	if rate <= 0 {
		return nil, fmt.Errorf("fee quote: non-positive fx rate %v", rate)
	}
	f := new(big.Float).Mul(big.NewFloat(rate), big.NewFloat(100))
	f.Add(f, big.NewFloat(0.5)) // big.Float.Int truncates; +0.5 makes it half-up
	minor, _ := f.Int(nil)
	if minor.Sign() <= 0 {
		return nil, fmt.Errorf("fee quote: fx rate %v rounds to zero", rate)
	}
	return minor, nil
}

// ─── Flutterwave live provider-fee quoter ──────────────────────────────────

// flutterwaveFeeQuoter adapts FlutterwaveClient.GetFees to ProviderFeeQuoter.
// Best-effort: any failure reads as "no live quote" and the calculator falls
// back to the config percentage, so a Flutterwave wobble can never take the
// quote endpoint down.
type flutterwaveFeeQuoter struct {
	client *FlutterwaveClient
}

// NewFlutterwaveFeeQuoter returns nil (⇒ fallback pricing) when the client is
// dormant, mirroring the other dormant-by-default Flutterwave seams.
func NewFlutterwaveFeeQuoter(client *FlutterwaveClient) ProviderFeeQuoter {
	if !client.IsEnabled() {
		return nil
	}
	return &flutterwaveFeeQuoter{client: client}
}

// fwFeeQuoteTimeout keeps the quote endpoint snappy when Flutterwave is slow.
const fwFeeQuoteTimeout = 5 * time.Second

func (q *flutterwaveFeeQuoter) QuoteProviderFee(ctx context.Context, flow FeeFlow, amountMinor *big.Int, currency string) (*big.Int, bool) {
	major, err := koboToNairaFloat(amountMinor.String())
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, fwFeeQuoteTimeout)
	defer cancel()
	// v4 /fees is collection-vocabulary; bank_transfer is our rail for both
	// directions. A method the endpoint won't price just falls back.
	// Country must match the currency's own rail. It was hardcoded "NG", which
	// asked Flutterwave to price a cedi transfer as if it were a Nigerian one —
	// a combination that does not exist, so the quote either came back wrong or
	// failed and silently fell through to the fallback percentage. The user was
	// then charged a number derived from the wrong market.
	fees, err := q.client.GetFees(ctx, major, currency, "bank_transfer", "", payoutCountryFor(currency))
	if err != nil || len(fees) == 0 {
		return nil, false
	}
	fee := fees[0]
	for _, f := range fees {
		if strings.EqualFold(f.Currency, currency) {
			fee = f
			break
		}
	}
	if fee.Fee < 0 {
		return nil, false
	}
	// Only the provider's own fee is passed through; its VAT figure is ignored
	// because the tax line computes VAT per OUR config (basis + bps) — using
	// both would double-count.
	minor, err := nairaToKobo(strconv.FormatFloat(fee.Fee, 'f', 2, 64))
	if err != nil {
		return nil, false
	}
	return minor, true
}
