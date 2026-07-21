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
	assertMinor(t, "platformFee", q.PlatformFee.InCurrency, "0") // MCGP ⇒ $0
	if q.PlatformFee.USDCents != 0 {
		t.Fatalf("platform USD cents = %d, want 0", q.PlatformFee.USDCents)
	}
	assertMinor(t, "swapFee", q.SwapFee.Fee, "0") // MCGP deposit ⇒ no swap leg
	assertMinor(t, "totalFees", q.TotalFees, "1080")
	assertMinor(t, "netAmount", q.NetAmount, "48920")
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
			platform: "0", provider: "1000", vat: "75", ptax: "5",
			swapBps: 0, swap: "0", legs: "",
			total: "1080", net: "48920", viable: true,
		},
		{
			name:     "deposit fiat→MCGP NGN basis=amount",
			basis:    "amount",
			in:       FeeQuoteInput{Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN"},
			platform: "0", provider: "1000", vat: "3750", ptax: "250",
			swapBps: 0, swap: "0", legs: "",
			total: "5000", net: "45000", viable: true,
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
			platform: "0", provider: "200", vat: "15", ptax: "1",
			swapBps: 500, swap: "500", legs: "mcgpSwap+lifiRoute",
			total: "716", net: "9284", viable: true,
		},
		{
			name:     "withdraw MCGP→fiat NGN basis=amount",
			basis:    "amount",
			in:       FeeQuoteInput{Flow: FeeFlowWithdraw, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN"},
			platform: "0", provider: "1000", vat: "3750", ptax: "250",
			swapBps: 500, swap: "2500", legs: "mcgpSwap+lifiRoute",
			total: "7500", net: "42500", viable: true,
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
		{"bad currency", func(in *FeeQuoteInput) { in.Currency = "GBP" }, ErrFeeQuoteBadCurrency},
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
