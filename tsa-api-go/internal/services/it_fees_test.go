package services

import (
	"context"
	"math/big"
	"testing"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// liveFeeQuoteConfig mirrors the deployed defaults: MCGP carries no platform
// fee, the provider falls back to 2%, tax applies to the provider fee, and a
// withdrawal pays both swap legs (2% + 3%).
func liveFeeQuoteConfig() config.FeeQuoteConfig {
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

func feeQuoterForTest() *FeeQuoteService {
	return NewFeeQuoteService(liveFeeQuoteConfig(), nil)
}

// The PM's own test sale: 5 MCGP at ₦27.57/MCGP → ₦137.85 gross. The screenshot
// showed ₦2.75 provider, ₦0.21 tax, ₦6.89 swap, ₦9.85 total, ₦128.00 net. That
// arithmetic is now what settles, not just what is displayed.
func TestQuoteITFees_SellMatchesTheQuotedReceipt(t *testing.T) {
	s := newITTestService(t)
	s.SetFeeQuoter(feeQuoterForTest())

	gross := big.NewInt(13785) // ₦137.85
	b, err := s.quoteITFees(context.Background(), FeeFlowWithdraw, gross)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if b.Fee.String() != "985" {
		t.Fatalf("fee = %s kobo, want 985 (₦9.85 as shown to the PM)", b.Fee)
	}
	if b.Net.String() != "12800" {
		t.Fatalf("net = %s kobo, want 12800 (₦128.00)", b.Net)
	}
	// Cashback/TP fund from what the platform keeps — the ₦6.89 swap fee only.
	// The ₦2.75 provider fee leaves for Flutterwave and the ₦0.21 tax is
	// remitted; distributing on either would pay out revenue never received.
	if b.PlatformKept.String() != "689" {
		t.Fatalf("platformKept = %s kobo, want 689 (swap fee only)", b.PlatformKept)
	}
	// Gross must survive untouched — it is the valuation of the MCGP leg.
	if b.Gross.String() != "13785" {
		t.Fatalf("gross mutated to %s", b.Gross)
	}
	if sum := new(big.Int).Add(b.Fee, b.Net); sum.Cmp(gross) != 0 {
		t.Fatalf("fee + net = %s, must equal gross %s", sum, gross)
	}
}

// A deposit has no swap leg, so the user keeps materially more of it.
func TestQuoteITFees_DepositHasNoSwapLeg(t *testing.T) {
	s := newITTestService(t)
	s.SetFeeQuoter(feeQuoterForTest())

	b, err := s.quoteITFees(context.Background(), FeeFlowDeposit, big.NewInt(50000)) // ₦500
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	// provider 2% = ₦10.00; tax 8% of that = ₦0.80; no swap fee on MCGP in.
	if b.Fee.String() != "1080" {
		t.Fatalf("fee = %s kobo, want 1080 (provider + tax, no swap leg)", b.Fee)
	}
	if b.Net.String() != "48920" {
		t.Fatalf("net = %s kobo, want 48920", b.Net)
	}
	if b.PlatformKept.Sign() != 0 {
		t.Fatalf("platformKept = %s, want 0 — an MCGP deposit keeps nothing beyond the pass-through", b.PlatformKept)
	}
}

func TestQuoteITFees_RefusesWhenUnpriceable(t *testing.T) {
	s := newITTestService(t)
	s.feeQuoter = nil // an unwired deployment

	if _, err := s.quoteITFees(context.Background(), FeeFlowWithdraw, big.NewInt(13785)); err != ErrITFeeQuoteUnavailable {
		t.Fatalf("err = %v, want ErrITFeeQuoteUnavailable — an unpriceable transfer must be refused, not settled gross", err)
	}
}

// Dust: fees swallow the whole amount, so nothing would reach the user.
func TestQuoteITFees_RejectsAmountBelowFees(t *testing.T) {
	s := newITTestService(t)
	// A punitive config where the swap leg alone exceeds the amount.
	cfg := liveFeeQuoteConfig()
	cfg.McgpSwapBps = 9000
	cfg.LifiRouteBps = 2000
	s.SetFeeQuoter(NewFeeQuoteService(cfg, nil))

	if _, err := s.quoteITFees(context.Background(), FeeFlowWithdraw, big.NewInt(1000)); err != ErrITAmountBelowFees {
		t.Fatalf("err = %v, want ErrITAmountBelowFees", err)
	}
}

func TestCreditableMcgp_PricesTheNetNotTheGross(t *testing.T) {
	s := newITTestService(t)
	s.SetFeeQuoter(feeQuoterForTest())

	paid := big.NewInt(50000) // ₦500 landed
	rate := big.NewInt(2757)  // ₦27.57 per MCGP
	mcgp, fees, err := s.creditableMcgp(context.Background(), paid, rate)
	if err != nil {
		t.Fatalf("creditableMcgp: %v", err)
	}
	// The user is credited the net's worth — the withheld naira's MCGP is never
	// minted, so it stays with the contract float.
	want := mcgpForNgn(fees.Net, rate)
	if mcgp.Cmp(want) != 0 {
		t.Fatalf("credited %s, want %s (net-priced)", mcgp, want)
	}
	if gross := mcgpForNgn(paid, rate); mcgp.Cmp(gross) >= 0 {
		t.Fatalf("credited %s is not less than the gross-priced %s — fees were not applied", mcgp, gross)
	}
}

// payoutKobo is what the gateway is handed. Deducted intents pay the net;
// intents created before deduction shipped keep settling at their gross terms.
func TestPayoutKobo_NetWhenPresentGrossWhenLegacy(t *testing.T) {
	deducted := &models.ITIntent{NgnKobo: "13785", NetNgnKobo: "12800"}
	got, err := payoutKobo(deducted)
	if err != nil || got.String() != "12800" {
		t.Fatalf("deducted intent paid %v (err %v), want 12800", got, err)
	}

	legacy := &models.ITIntent{NgnKobo: "13785"}
	got, err = payoutKobo(legacy)
	if err != nil || got.String() != "13785" {
		t.Fatalf("legacy intent paid %v (err %v), want its gross 13785", got, err)
	}

	if _, err := payoutKobo(&models.ITIntent{}); err == nil {
		t.Fatal("an intent with no amount at all must error, not disburse zero")
	}
}

func TestKoboToNairaDecimal(t *testing.T) {
	cases := map[string]string{
		"13785": "137.85",
		"12800": "128.00",
		"5":     "0.05",
		"100":   "1.00",
	}
	for kobo, want := range cases {
		v, _ := new(big.Int).SetString(kobo, 10)
		if got := koboToNairaDecimal(v); got != want {
			t.Fatalf("koboToNairaDecimal(%s) = %q, want %q", kobo, got, want)
		}
	}
}

func TestKoboToUSD(t *testing.T) {
	// ₦6.89 at ₦1,600/$ ≈ $0.0043.
	usd := koboToUSD(big.NewInt(689), 1600)
	if usd < 0.0042 || usd > 0.0044 {
		t.Fatalf("koboToUSD = %v, want ~0.0043", usd)
	}
	if koboToUSD(big.NewInt(689), 0) != 0 {
		t.Fatal("a non-positive FX rate must value the fee at zero, not divide by it")
	}
}

// The carve that funds TP must never fire for a fee that was not collected.
func TestSettleITFee_SkipsWhenNothingWasKept(t *testing.T) {
	s := newITTestService(t)
	// No distributor and no kept amount — must be a silent no-op, not a panic.
	s.settleITFee(context.Background(), &models.ITIntent{ID: uuid.New()})
	s.settleITFee(context.Background(), &models.ITIntent{ID: uuid.New(), PlatformKeptKobo: "0"})
}
