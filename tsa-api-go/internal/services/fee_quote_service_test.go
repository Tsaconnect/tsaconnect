package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

// feeTestConfig mirrors the PM-spec defaults loaded by config.Load.
func feeTestConfig() config.FeeQuoteConfig {
	return config.FeeQuoteConfig{
		PlatformStableUSD:    2,
		PlatformNgnBps:       50,
		PlatformFxBps:        200,
		ProviderFallbackBps:  200,
		TaxBasis:             "fee",
		VATBps:               750,
		PlatformTaxBps:       50,
		McgpSwapBps:          200,
		LifiRouteBps:         300,
		StableDepositConvBps: 200,
	}
}

// fixedProviderFee is a stub live quoter returning a constant fee.
type fixedProviderFee struct {
	fee *big.Int
	ok  bool
}

func (f fixedProviderFee) QuoteProviderFee(_ context.Context, _ FeeFlow, _ *big.Int, _ string) (*big.Int, bool) {
	return f.fee, f.ok
}

// The PM's real receipt: ₦500 deposit, provider fee ₦10 (= the 200bps
// fallback), VAT ₦0.75 — i.e. 7.5% OF THE PROVIDER FEE, the evidence behind
// the "fee" tax-basis default.
func TestFeeQuote_PMReceipt(t *testing.T) {
	s := NewFeeQuoteService(feeTestConfig(), nil)
	q, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN",
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	assertMinor(t, "amount", q.Amount, "50000")
	assertMinor(t, "providerFee", q.ProviderFee.Fee, "1000") // ₦10.00
	if q.ProviderFee.Source != "fallback" || q.ProviderFee.FallbackBps != 200 {
		t.Fatalf("provider provenance: %+v", q.ProviderFee)
	}
	if q.Tax.Basis != "fee" {
		t.Fatalf("tax basis = %q, want fee", q.Tax.Basis)
	}
	assertMinor(t, "tax base", q.Tax.Base, "1000")
	assertMinor(t, "VAT", q.Tax.VAT, "75") // ₦0.75 — matches the receipt
	if q.Tax.VAT.Display != "0.75" {
		t.Fatalf("VAT display = %q, want 0.75", q.Tax.VAT.Display)
	}
	assertMinor(t, "platformTax", q.Tax.PlatformTax, "5")
	// The receipt predates the 28/07 platform fee; the ₦2.50 (0.5% NGN tier)
	// is charged on top of it now, and is never denominated in USD.
	assertMinor(t, "platformFee", q.PlatformFee.InCurrency, "250")
	if q.PlatformFee.USDCents != 0 {
		t.Fatalf("platform USD cents = %d, want 0 — MCGP is rate-based, not flat USD", q.PlatformFee.USDCents)
	}
	assertMinor(t, "swapFee", q.SwapFee.Fee, "0") // MCGP deposit ⇒ no swap leg
	assertMinor(t, "totalFees", q.TotalFees, "1330")
	assertMinor(t, "netAmount", q.NetAmount, "48670")
	if !q.Viable {
		t.Fatal("expected viable")
	}
	if q.AssetOutEstimate != nil {
		t.Fatal("assetOutEstimate must stay nil (hook only)")
	}
}

// Table over all four flows × both tax bases, plus the chain-dependent
// stablecoin-withdraw split. fx (when set) is minor units per USD.
func TestFeeQuote_FlowsAndTaxBases(t *testing.T) {
	fx1500 := big.NewInt(150000) // ₦1500/USD in kobo

	cases := []struct {
		name     string
		basis    string
		in       FeeQuoteInput
		platform string
		provider string
		vat      string
		ptax     string
		swapBps  int64
		swap     string
		legs     string
		total    string
		net      string
		viable   bool
	}{
		{
			name:     "deposit fiat→MCGP NGN basis=fee",
			basis:    "fee",
			in:       FeeQuoteInput{Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN"},
			platform: "250", provider: "1000", vat: "75", ptax: "5", // platform = 0.5% NGN tier
			swapBps: 0, swap: "0", legs: "",
			total: "1330", net: "48670", viable: true,
		},
		{
			name:     "deposit fiat→MCGP NGN basis=amount",
			basis:    "amount",
			in:       FeeQuoteInput{Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN"},
			platform: "250", provider: "1000", vat: "3750", ptax: "250",
			swapBps: 0, swap: "0", legs: "",
			total: "5250", net: "44750", viable: true,
		},
		{
			name:     "deposit fiat→USDT NGN basis=fee",
			basis:    "fee",
			in:       FeeQuoteInput{Flow: FeeFlowDeposit, Asset: FeeAssetUSDT, Amount: "1000000", Currency: "NGN", FxMinorPerUSD: fx1500},
			platform: "300000", // $2 at ₦1500
			provider: "2000000", vat: "150000", ptax: "10000",
			swapBps: 200, swap: "2000000", legs: "conversion",
			total: "4460000", net: "95540000", viable: true,
		},
		{
			name:     "deposit fiat→USDC USD basis=amount",
			basis:    "amount",
			in:       FeeQuoteInput{Flow: FeeFlowDeposit, Asset: FeeAssetUSDC, Amount: "100", Currency: "USD"},
			platform: "200", provider: "200", vat: "750", ptax: "50",
			swapBps: 200, swap: "200", legs: "conversion",
			total: "1400", net: "8600", viable: true,
		},
		{
			name:     "withdraw MCGP→fiat USD basis=fee",
			basis:    "fee",
			in:       FeeQuoteInput{Flow: FeeFlowWithdraw, Asset: FeeAssetMCGP, Amount: "100", Currency: "USD"},
			platform: "200", provider: "200", vat: "15", ptax: "1", // platform = 2% non-NGN tier
			swapBps: 500, swap: "500", legs: "mcgpSwap+lifiRoute",
			total: "916", net: "9084", viable: true,
		},
		{
			name:     "withdraw MCGP→fiat NGN basis=amount",
			basis:    "amount",
			in:       FeeQuoteInput{Flow: FeeFlowWithdraw, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN"},
			platform: "250", provider: "1000", vat: "3750", ptax: "250",
			swapBps: 500, swap: "2500", legs: "mcgpSwap+lifiRoute",
			total: "7750", net: "42250", viable: true,
		},
		{
			name:     "withdraw USDT on polygon → LiFi leg only (3%)",
			basis:    "fee",
			in:       FeeQuoteInput{Flow: FeeFlowWithdraw, Asset: FeeAssetUSDT, Chain: "polygon", Amount: "100", Currency: "USD"},
			platform: "200", provider: "200", vat: "15", ptax: "1",
			swapBps: 300, swap: "300", legs: "lifiRoute",
			total: "716", net: "9284", viable: true,
		},
		{
			name:     "withdraw USDC off-polygon → both legs (5%)",
			basis:    "fee",
			in:       FeeQuoteInput{Flow: FeeFlowWithdraw, Asset: FeeAssetUSDC, Chain: "sonic", Amount: "100", Currency: "USD"},
			platform: "200", provider: "200", vat: "15", ptax: "1",
			swapBps: 500, swap: "500", legs: "mcgpSwap+lifiRoute",
			total: "916", net: "9084", viable: true,
		},
		{
			name:     "withdraw USDT off-polygon basis=amount",
			basis:    "amount",
			in:       FeeQuoteInput{Flow: FeeFlowWithdraw, Asset: FeeAssetUSDT, Chain: "bsc", Amount: "100", Currency: "USD"},
			platform: "200", provider: "200", vat: "750", ptax: "50",
			swapBps: 500, swap: "500", legs: "mcgpSwap+lifiRoute",
			total: "1700", net: "8300", viable: true,
		},
		{
			name:     "dust: fees swallow a tiny stable deposit",
			basis:    "fee",
			in:       FeeQuoteInput{Flow: FeeFlowDeposit, Asset: FeeAssetUSDT, Amount: "500", Currency: "NGN", FxMinorPerUSD: fx1500},
			platform: "300000", provider: "1000", vat: "75", ptax: "5",
			swapBps: 200, swap: "1000", legs: "conversion",
			total: "302080", net: "0", viable: false,
		},
		{
			name:     "dust: 1 kobo MCGP deposit floors every fee to zero",
			basis:    "fee",
			in:       FeeQuoteInput{Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "0.01", Currency: "NGN"},
			platform: "0", provider: "0", vat: "0", ptax: "0",
			swapBps: 0, swap: "0", legs: "",
			total: "0", net: "1", viable: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := feeTestConfig()
			cfg.TaxBasis = tc.basis
			q, err := NewFeeQuoteService(cfg, nil).Quote(context.Background(), tc.in)
			if err != nil {
				t.Fatalf("quote: %v", err)
			}
			assertMinor(t, "platformFee", q.PlatformFee.InCurrency, tc.platform)
			assertMinor(t, "providerFee", q.ProviderFee.Fee, tc.provider)
			assertMinor(t, "VAT", q.Tax.VAT, tc.vat)
			assertMinor(t, "platformTax", q.Tax.PlatformTax, tc.ptax)
			if q.Tax.Basis != tc.basis {
				t.Fatalf("tax basis = %q, want %q", q.Tax.Basis, tc.basis)
			}
			if q.SwapFee.Bps != tc.swapBps {
				t.Fatalf("swap bps = %d, want %d", q.SwapFee.Bps, tc.swapBps)
			}
			assertMinor(t, "swapFee", q.SwapFee.Fee, tc.swap)
			if got := strings.Join(q.SwapFee.Legs, "+"); got != tc.legs {
				t.Fatalf("swap legs = %q, want %q", got, tc.legs)
			}
			assertMinor(t, "totalFees", q.TotalFees, tc.total)
			assertMinor(t, "netAmount", q.NetAmount, tc.net)
			if q.Viable != tc.viable {
				t.Fatalf("viable = %v, want %v", q.Viable, tc.viable)
			}
		})
	}
}

func TestFeeQuote_LiveProviderQuote(t *testing.T) {
	// Live quote ₦12.34; tax basis "fee" must follow the LIVE fee, not the
	// fallback percentage.
	s := NewFeeQuoteService(feeTestConfig(), fixedProviderFee{fee: big.NewInt(1234), ok: true})
	q, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN",
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	assertMinor(t, "providerFee", q.ProviderFee.Fee, "1234")
	if q.ProviderFee.Source != "live" || q.ProviderFee.FallbackBps != 0 {
		t.Fatalf("provider provenance: %+v", q.ProviderFee)
	}
	assertMinor(t, "tax base", q.Tax.Base, "1234")
	assertMinor(t, "VAT", q.Tax.VAT, "92")                // floor(1234·750/10000)
	assertMinor(t, "platformTax", q.Tax.PlatformTax, "6") // floor(1234·50/10000)

	// A declining quoter falls back to the config percentage.
	s = NewFeeQuoteService(feeTestConfig(), fixedProviderFee{ok: false})
	q, err = s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN",
	})
	if err != nil {
		t.Fatalf("fallback quote: %v", err)
	}
	assertMinor(t, "providerFee", q.ProviderFee.Fee, "1000")
	if q.ProviderFee.Source != "fallback" {
		t.Fatalf("source = %q, want fallback", q.ProviderFee.Source)
	}
}

func TestFeeQuote_InputValidation(t *testing.T) {
	s := NewFeeQuoteService(feeTestConfig(), nil)
	base := FeeQuoteInput{Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN"}

	cases := []struct {
		name string
		mut  func(*FeeQuoteInput)
		want error
	}{
		{"bad flow", func(in *FeeQuoteInput) { in.Flow = "transfer" }, ErrFeeQuoteBadFlow},
		{"bad asset", func(in *FeeQuoteInput) { in.Asset = "BTC" }, ErrFeeQuoteBadAsset},
		// GBP and the rest of the gateway's currencies are quotable since the
		// multi-currency tiering landed; only a code off the catalogue fails.
		{"bad currency", func(in *FeeQuoteInput) { in.Currency = "ZZZ" }, ErrFeeQuoteBadCurrency},
		{"zero amount", func(in *FeeQuoteInput) { in.Amount = "0" }, ErrFeeQuoteBadAmount},
		{"negative amount", func(in *FeeQuoteInput) { in.Amount = "-5" }, ErrFeeQuoteBadAmount},
		{"3dp amount", func(in *FeeQuoteInput) { in.Amount = "1.234" }, ErrFeeQuoteBadAmount},
		{"empty amount", func(in *FeeQuoteInput) { in.Amount = "" }, ErrFeeQuoteBadAmount},
		{"garbage amount", func(in *FeeQuoteInput) { in.Amount = "12x" }, ErrFeeQuoteBadAmount},
		{"stable withdraw needs chain", func(in *FeeQuoteInput) {
			in.Flow, in.Asset, in.Currency, in.Chain = FeeFlowWithdraw, FeeAssetUSDT, "USD", ""
		}, ErrFeeQuoteNeedChain},
		{"stable NGN needs fx", func(in *FeeQuoteInput) {
			in.Asset, in.FxMinorPerUSD = FeeAssetUSDC, nil
		}, ErrFeeQuoteNeedFx},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mut(&in)
			if _, err := s.Quote(context.Background(), in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestFeeQuote_NormalizesInputCase(t *testing.T) {
	s := NewFeeQuoteService(feeTestConfig(), nil)
	q, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: "Withdraw", Asset: "usdt", Chain: " Polygon ", Amount: "100", Currency: "usd",
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if q.Flow != FeeFlowWithdraw || q.Asset != FeeAssetUSDT || q.Chain != "polygon" || q.Currency != "USD" {
		t.Fatalf("normalization: %+v", q)
	}
	if q.SwapFee.Bps != 300 { // normalized chain must hit the Polygon branch
		t.Fatalf("swap bps = %d, want 300", q.SwapFee.Bps)
	}
	// USD needs no explicit fx: $2 platform fee folds in at 100 cents/USD.
	assertMinor(t, "platformFee", q.PlatformFee.InCurrency, "200")
}

// An unknown FEE_TAX_BASIS value must degrade to the receipt-backed "fee"
// default, never break quoting.
func TestFeeQuote_UnknownTaxBasisDefaultsToFee(t *testing.T) {
	cfg := feeTestConfig()
	cfg.TaxBasis = "gross"
	q, err := NewFeeQuoteService(cfg, nil).Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN",
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if q.Tax.Basis != "fee" {
		t.Fatalf("tax basis = %q, want fee", q.Tax.Basis)
	}
	assertMinor(t, "VAT", q.Tax.VAT, "75")
}

func TestMinorPerUSDFromRate(t *testing.T) {
	for _, c := range []struct {
		rate float64
		want string
	}{
		{1500, "150000"},
		{1500.256, "150026"}, // half-up
		{1499.999, "150000"},
		{1, "100"},
	} {
		got, err := MinorPerUSDFromRate(c.rate)
		if err != nil {
			t.Fatalf("rate %v: %v", c.rate, err)
		}
		if got.String() != c.want {
			t.Fatalf("rate %v = %s, want %s", c.rate, got, c.want)
		}
	}
	for _, bad := range []float64{0, -1} {
		if _, err := MinorPerUSDFromRate(bad); err == nil {
			t.Fatalf("rate %v accepted", bad)
		}
	}
}

// Minor units must serialize as STRINGS, and the named-field layout must keep
// sibling keys (a promoted MarshalJSON would have swallowed them).
func TestFeeQuote_JSONShape(t *testing.T) {
	s := NewFeeQuoteService(feeTestConfig(), nil)
	q, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN",
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	amount, _ := m["amount"].(map[string]any)
	if amount["minor"] != "50000" || amount["display"] != "500.00" {
		t.Fatalf("amount JSON = %v", m["amount"])
	}
	provider, _ := m["providerFee"].(map[string]any)
	if provider["source"] != "fallback" {
		t.Fatalf("providerFee lost sibling keys: %v", m["providerFee"])
	}
	if fee, _ := provider["fee"].(map[string]any); fee["minor"] != "1000" {
		t.Fatalf("providerFee.fee JSON = %v", provider["fee"])
	}
	if _, present := m["assetOutEstimate"]; !present || m["assetOutEstimate"] != nil {
		t.Fatalf("assetOutEstimate = %v, want explicit null", m["assetOutEstimate"])
	}
}

func assertMinor(t *testing.T, label string, line FeeLine, want string) {
	t.Helper()
	if line.Minor == nil || line.Minor.String() != want {
		t.Fatalf("%s minor = %v, want %s", label, line.Minor, want)
	}
}

// ─────────────── platform fee: currency-tiered (PM spec, 28/07) ──────────────
// The PM reversed the "no company charge on deposits" call: every fiat ramp now
// carries a platform fee, tiered by the FIAT currency — 0.5% on NGN, 2% on
// every other currency (USD/EUR/GBP/AED/…). It is the only line the platform
// actually keeps; provider is passthrough and tax is remitted.

func TestFeeQuote_NgnMcgpDepositChargesHalfPercentPlatformFee(t *testing.T) {
	s := NewFeeQuoteService(feeTestConfig(), nil)
	q, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN",
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	// 0.5% of ₦500 = ₦2.50.
	assertMinor(t, "platformFee", q.PlatformFee.InCurrency, "250")
	if q.PlatformFee.Bps != 50 {
		t.Fatalf("platformFee.bps = %d, want 50", q.PlatformFee.Bps)
	}
	// provider ₦10 + tax ₦0.80 + platform ₦2.50, no swap leg on an MCGP deposit.
	assertMinor(t, "totalFees", q.TotalFees, "1330")
	assertMinor(t, "netAmount", q.NetAmount, "48670")
}

func TestFeeQuote_NonNgnMcgpDepositChargesTwoPercentPlatformFee(t *testing.T) {
	s := NewFeeQuoteService(feeTestConfig(), nil)
	for _, currency := range []string{"USD", "GBP", "EUR", "AED"} {
		q, err := s.Quote(context.Background(), FeeQuoteInput{
			Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: currency,
		})
		if err != nil {
			t.Fatalf("%s quote: %v", currency, err)
		}
		// 2% of 500.00 = 10.00 in the quote currency.
		assertMinor(t, currency+" platformFee", q.PlatformFee.InCurrency, "1000")
		if q.PlatformFee.Bps != 200 {
			t.Fatalf("%s platformFee.bps = %d, want 200", currency, q.PlatformFee.Bps)
		}
	}
}

// "Same fees apply" — the withdrawal leg carries the platform fee too, on top
// of the existing swap legs.
func TestFeeQuote_McgpWithdrawalCarriesThePlatformFee(t *testing.T) {
	s := NewFeeQuoteService(feeTestConfig(), nil)
	// The PM's own sell: 6 MCGP valued at ₦165.66.
	q, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowWithdraw, Asset: FeeAssetMCGP, Amount: "165.66", Currency: "NGN",
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	assertMinor(t, "platformFee", q.PlatformFee.InCurrency, "82") // 0.5%
	assertMinor(t, "providerFee", q.ProviderFee.Fee, "331")       // 2%
	assertMinor(t, "tax", q.Tax.Total, "25")                      // 8% of provider
	assertMinor(t, "swapFee", q.SwapFee.Fee, "828")               // 5%
	assertMinor(t, "totalFees", q.TotalFees, "1266")
	assertMinor(t, "netAmount", q.NetAmount, "15300") // ₦153.00 reaches the bank
}

// Multi-currency rails (PM batch 9): the engine must price every payment
// currency the gateway supports, not just NGN and USD.
func TestFeeQuote_AcceptsEveryPaymentCurrency(t *testing.T) {
	s := NewFeeQuoteService(feeTestConfig(), nil)
	for _, currency := range []string{"NGN", "USD", "GBP", "EUR", "AED", "GHS", "KES", "ZAR", "UGX", "TZS", "XOF", "XAF", "RWF", "MWK", "ZMW"} {
		if _, err := s.Quote(context.Background(), FeeQuoteInput{
			Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: currency,
		}); err != nil {
			t.Fatalf("%s must be quotable: %v", currency, err)
		}
	}
	if _, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: "ZZZ",
	}); !errors.Is(err, ErrFeeQuoteBadCurrency) {
		t.Fatalf("unknown currency err = %v, want ErrFeeQuoteBadCurrency", err)
	}
}

// The stablecoin flat-USD platform fee is a separate, untouched lever — the
// currency tier applies to the MCGP (Instant Transfer) ramps only until the PM
// rules on the stable rails.
func TestFeeQuote_StablecoinKeepsTheFlatUsdPlatformFee(t *testing.T) {
	s := NewFeeQuoteService(feeTestConfig(), nil)
	q, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowDeposit, Asset: FeeAssetUSDC, Amount: "500", Currency: "USD",
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	assertMinor(t, "platformFee", q.PlatformFee.InCurrency, "200") // $2.00 flat
	if q.PlatformFee.Bps != 0 {
		t.Fatalf("platformFee.bps = %d, want 0 (flat fee, not a percentage)", q.PlatformFee.Bps)
	}
}

// A cross-currency payout is VALUED in naira (the IT sell rate prices MCGP in
// NGN and Flutterwave converts at settlement) but the user is PAID in the
// destination currency. The platform tier follows what the user pays/receives,
// not what the amount happens to be denominated in — otherwise every foreign
// rail would quietly bill at the cheap local rate.
func TestFeeQuote_PaymentCurrencySelectsTheTier(t *testing.T) {
	s := NewFeeQuoteService(feeTestConfig(), nil)
	q, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowWithdraw, Asset: FeeAssetMCGP,
		Amount: "500", Currency: "NGN", PaymentCurrency: "GHS",
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if q.PlatformFee.Bps != 200 {
		t.Fatalf("platformFee.bps = %d, want 200 (FX tier — the user is paid in GHS)", q.PlatformFee.Bps)
	}
	assertMinor(t, "platformFee", q.PlatformFee.InCurrency, "1000") // 2% of ₦500
}

func TestFeeQuote_PaymentCurrencyDefaultsToTheQuoteCurrency(t *testing.T) {
	s := NewFeeQuoteService(feeTestConfig(), nil)
	q, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowWithdraw, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN",
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if q.PlatformFee.Bps != 50 {
		t.Fatalf("platformFee.bps = %d, want 50 — an unset payment currency means the quote currency", q.PlatformFee.Bps)
	}
}

// An unknown payment currency must not silently fall through to the cheap NGN
// tier; it is a bad request like any other unsupported currency.
func TestFeeQuote_RejectsUnknownPaymentCurrency(t *testing.T) {
	s := NewFeeQuoteService(feeTestConfig(), nil)
	if _, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowWithdraw, Asset: FeeAssetMCGP,
		Amount: "500", Currency: "NGN", PaymentCurrency: "ZZZ",
	}); !errors.Is(err, ErrFeeQuoteBadCurrency) {
		t.Fatalf("err = %v, want ErrFeeQuoteBadCurrency", err)
	}
}

func TestSwapLegs_BrokeredStablecoinFlowsChargeNoBps(t *testing.T) {
	// On a brokered ramp the company margin is the RATE spread, and the user
	// pays the bridge directly out of what arrives. Charging the swap/routing
	// bps as well would bill the same cost twice.
	s := newBrokeredFeeService(t, true)

	for _, asset := range []FeeAsset{FeeAssetUSDC, FeeAssetUSDT} {
		for _, chain := range []string{"polygon", "ethereum", ""} {
			bps, legs := s.swapLegs(FeeFlowWithdraw, asset, chain)
			if bps != 0 {
				t.Errorf("withdraw %s on %q: bps = %d, want 0", asset, chain, bps)
			}
			if len(legs) != 0 {
				t.Errorf("withdraw %s on %q: legs = %v, want none", asset, chain, legs)
			}
		}
		// The DEPOSIT direction keeps its conversion leg. This assertion was
		// inverted when it was written: the flag describes the sell ramp, and
		// the deposit side is the unimplemented buy direction whose 2%
		// conversion charge is already quoted on GET /api/it/quote. Pinned
		// separately in TestSwapLegs_BrokeredFlagDoesNotTouchTheDepositLeg.
		bps, legs := s.swapLegs(FeeFlowDeposit, asset, "polygon")
		if bps != s.cfg.StableDepositConvBps || len(legs) != 1 {
			t.Errorf("deposit %s: bps=%d legs=%v, want the conversion leg intact", asset, bps, legs)
		}
	}
}

func TestSwapLegs_MCGPIsUnaffectedByTheBrokeredFlag(t *testing.T) {
	// MCGP is NOT brokered — we are its counterparty, and its swap + routing
	// legs are real costs we bear. The flag must not touch them.
	s := newBrokeredFeeService(t, true)

	bps, legs := s.swapLegs(FeeFlowWithdraw, FeeAssetMCGP, "polygon")
	if bps == 0 {
		t.Fatal("MCGP withdraw lost its swap legs")
	}
	if len(legs) != 2 {
		t.Fatalf("MCGP withdraw legs = %v, want mcgpSwap + lifiRoute", legs)
	}
}

func TestSwapLegs_DefaultBehaviourIsUnchanged(t *testing.T) {
	// Without the flag the existing pricing must be byte-for-byte what it was.
	s := newBrokeredFeeService(t, false)

	bps, legs := s.swapLegs(FeeFlowWithdraw, FeeAssetUSDC, "polygon")
	if len(legs) != 1 || legs[0] != "lifiRoute" {
		t.Fatalf("legs = %v, want [lifiRoute]", legs)
	}
	if bps == 0 {
		t.Fatal("bps = 0, want the configured lifiRoute bps")
	}
}

// newBrokeredFeeService builds a FeeQuoteService with the brokered flag set.
// feeTestConfig (above) is the fixture the existing fee-quote tests already
// use — its McgpSwapBps (200) and LifiRouteBps (300) are both non-zero, so
// the assertions above are not vacuous.
func newBrokeredFeeService(t *testing.T, brokered bool) *FeeQuoteService {
	t.Helper()
	s := NewFeeQuoteService(feeTestConfig(), nil)
	s.SetBrokeredStableFlows(brokered)
	return s
}

func TestSwapLegs_BrokeredFlagDoesNotTouchTheDepositLeg(t *testing.T) {
	// The brokered flag describes the SELL ramp — a stablecoin withdrawal whose
	// margin is the rate spread. The deposit direction is the buy side (Phase
	// B, not implemented) and its conversion leg is a real, separate charge
	// already exposed on GET /api/it/quote. Zeroing it because the asset
	// happens to be stable discounts a quote nobody decided to discount.
	s := newBrokeredFeeService(t, true)

	bps, legs := s.swapLegs(FeeFlowDeposit, FeeAssetUSDC, feeChainPolygon)
	if len(legs) != 1 || legs[0] != "conversion" {
		t.Fatalf("legs = %v, want [conversion]", legs)
	}
	if bps != feeTestConfig().StableDepositConvBps {
		t.Fatalf("bps = %d, want the configured StableDepositConvBps %d", bps, feeTestConfig().StableDepositConvBps)
	}
}

func TestSwapLegs_BrokeredFlagStillZeroesTheWithdrawLeg(t *testing.T) {
	// The narrowing must not undo what the flag is for: a brokered stablecoin
	// WITHDRAWAL carries no bps legs, or the user is billed the spread twice.
	s := newBrokeredFeeService(t, true)

	bps, legs := s.swapLegs(FeeFlowWithdraw, FeeAssetUSDC, feeChainPolygon)
	if bps != 0 || len(legs) != 0 {
		t.Fatalf("brokered stablecoin withdraw = %d bps %v, want 0 and no legs", bps, legs)
	}
}
