package services

import (
	"context"
	"math/big"
	"testing"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// liveFeeQuoteConfig mirrors the deployed defaults: MCGP carries the platform
// fee tiered by payment currency (0.5% NGN / 2% other), the provider falls back
// to 2%, tax applies to the provider fee, and a withdrawal pays both swap legs
// (2% + 3%). Keep this in step with config.Load — a stale literal here silently
// tests a fee regime that is not the one deployed.
func liveFeeQuoteConfig() config.FeeQuoteConfig {
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
	b, err := s.quoteITFees(context.Background(), FeeFlowWithdraw, gross, "NGN")
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	// ₦0.68 platform (0.5% NGN) + ₦2.75 provider + ₦0.21 tax + ₦6.89 swap.
	if b.Fee.String() != "1053" {
		t.Fatalf("fee = %s kobo, want 1053", b.Fee)
	}
	if b.Net.String() != "12732" {
		t.Fatalf("net = %s kobo, want 12732 (₦127.32)", b.Net)
	}
	// Cashback/TP fund from what the platform keeps — the ₦6.89 swap fee plus
	// the ₦0.68 platform fee. The ₦2.75 provider fee leaves for Flutterwave and
	// the ₦0.21 tax is remitted; distributing on either would pay out revenue
	// never received.
	if b.PlatformKept.String() != "757" {
		t.Fatalf("platformKept = %s kobo, want 757 (swap + platform fee)", b.PlatformKept)
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

	b, err := s.quoteITFees(context.Background(), FeeFlowDeposit, big.NewInt(50000), "NGN") // ₦500
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	// platform 0.5% = ₦2.50; provider 2% = ₦10.00; tax 8% of that = ₦0.80; no
	// swap fee on MCGP in.
	if b.Fee.String() != "1330" {
		t.Fatalf("fee = %s kobo, want 1330 (platform + provider + tax, no swap leg)", b.Fee)
	}
	if b.Net.String() != "48670" {
		t.Fatalf("net = %s kobo, want 48670", b.Net)
	}
	// The whole point of the 28/07 spec: a deposit now retains the platform fee.
	// It is withheld as MCGP that is never issued, so it stays on the contract.
	if b.PlatformKept.String() != "250" {
		t.Fatalf("platformKept = %s kobo, want 250 (the 0.5%% NGN platform fee)", b.PlatformKept)
	}
}

func TestQuoteITFees_RefusesWhenUnpriceable(t *testing.T) {
	s := newITTestService(t)
	s.feeQuoter = nil // an unwired deployment

	if _, err := s.quoteITFees(context.Background(), FeeFlowWithdraw, big.NewInt(13785), "NGN"); err != ErrITFeeQuoteUnavailable {
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

	if _, err := s.quoteITFees(context.Background(), FeeFlowWithdraw, big.NewInt(1000), "NGN"); err != ErrITAmountBelowFees {
		t.Fatalf("err = %v, want ErrITAmountBelowFees", err)
	}
}

func TestCreditableMcgp_PricesTheNetNotTheGross(t *testing.T) {
	s := newITTestService(t)
	s.SetFeeQuoter(feeQuoterForTest())

	paid := big.NewInt(50000) // ₦500 landed
	rate := big.NewInt(2757)  // ₦27.57 per MCGP
	mcgp, fees, err := s.creditableMcgp(context.Background(), paid, rate, "NGN")
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

// A sell paid out to a foreign rail owes the FX tier, not the local one. The
// gross is still naira (the sell rate prices MCGP in NGN); only the tier moves.
func TestQuoteITFees_ForeignPayoutPaysTheFxTier(t *testing.T) {
	s := newITTestService(t)
	s.SetFeeQuoter(feeQuoterForTest())

	local, err := s.quoteITFees(context.Background(), FeeFlowWithdraw, big.NewInt(50000), "NGN")
	if err != nil {
		t.Fatalf("ngn quote: %v", err)
	}
	foreign, err := s.quoteITFees(context.Background(), FeeFlowWithdraw, big.NewInt(50000), "GHS")
	if err != nil {
		t.Fatalf("ghs quote: %v", err)
	}
	// 0.5% = ₦2.50 kept locally, 2% = ₦10.00 kept on the foreign rail; the
	// swap leg (₦25.00) is identical either way, so the whole difference is
	// the platform tier.
	if local.PlatformKept.String() != "2750" {
		t.Fatalf("local platformKept = %s kobo, want 2750 (swap 2500 + 0.5%% 250)", local.PlatformKept)
	}
	if foreign.PlatformKept.String() != "3500" {
		t.Fatalf("foreign platformKept = %s kobo, want 3500 (swap 2500 + 2%% 1000)", foreign.PlatformKept)
	}
}

// An empty destination currency is the legacy NGN bank rail, not an error.
func TestQuoteITFees_EmptyPayoutCurrencyMeansNaira(t *testing.T) {
	s := newITTestService(t)
	s.SetFeeQuoter(feeQuoterForTest())

	blank, err := s.quoteITFees(context.Background(), FeeFlowWithdraw, big.NewInt(50000), "")
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if blank.PlatformKept.String() != "2750" {
		t.Fatalf("platformKept = %s kobo, want 2750 — blank must mean NGN", blank.PlatformKept)
	}
}

// ─── Fee valuation across currencies ─────────────────────────────────────────

// TestSettleITFee_ValuesFeeInTheIntentsOwnCurrency is the regression test for a
// ~100x mis-valuation.
//
// settleITFee converts the platform-kept fee to USD to carve cashback and
// TradePoints. It used to read the NGN/USD rate unconditionally, so a cedi
// intent had pesewas (~12/USD) divided by the naira rate (~1400/USD) — the
// carve came out ~117x too small and Ghanaian users would have earned
// near-zero rewards, silently and with nothing in the ledger looking wrong.
// Inverted, it mints ~100x the TradePoints, which is real distributed value.
//
// The assertion is that equal USD value produces equal FeeUSD regardless of the
// currency it was denominated in. Note this is only testable because the FX
// stub resolves per currency: a stub returning one rate for every code would
// pass with the bug present.
func TestSettleITFee_ValuesFeeInTheIntentsOwnCurrency(t *testing.T) {
	const (
		ngnPerUsd = 1400.0
		ghsPerUsd = 14.0 // exactly 100x cheaper, so equal USD value is easy to state
	)

	// $1.00 of platform-kept fee, expressed in each currency's minor units.
	keptNGN := "140000" // ₦1,400.00 = $1
	keptGHS := "1400"   // ₵14.00    = $1

	run := func(t *testing.T, currency, kept string) float64 {
		t.Helper()
		s := newITTestService(t)
		s.fx = stubFx{ngn: ngnPerUsd, rates: map[string]float64{
			"NGN": ngnPerUsd,
			"GHS": ghsPerUsd,
		}}
		fees := &recordingFeeSink{}
		s.fees = fees

		s.settleITFee(context.Background(), &models.ITIntent{
			ID: uuid.New(), UserID: uuid.New(),
			Currency:         currency,
			PlatformKeptKobo: kept,
		})
		if len(fees.events) != 1 {
			t.Fatalf("%s: expected 1 fee event, got %d", currency, len(fees.events))
		}
		return fees.events[0].FeeUSD
	}

	usdFromNGN := run(t, "NGN", keptNGN)
	usdFromGHS := run(t, "GHS", keptGHS)

	if usdFromNGN <= 0 {
		t.Fatalf("NGN fee valued at %v, want positive", usdFromNGN)
	}
	if diff := usdFromNGN - usdFromGHS; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("same USD value carved differently by currency: NGN→$%v, GHS→$%v.\n"+
			"A GHS intent must be valued at the GHS rate; using the naira rate "+
			"understates it ~100x and near-zeroes Ghanaian cashback and TP.",
			usdFromNGN, usdFromGHS)
	}
}

// TestSettleITFee_SkipsWhenCurrencyUnpriceable confirms an unresolvable rate
// skips the carve rather than booking a wrong one. The fee itself is already
// retained; what must not happen is minting rewards against a made-up value.
func TestSettleITFee_SkipsWhenCurrencyUnpriceable(t *testing.T) {
	s := newITTestService(t)
	s.fx = stubFx{ngn: 1400, rates: map[string]float64{"NGN": 1400}} // no GHS
	fees := &recordingFeeSink{}
	s.fees = fees

	s.settleITFee(context.Background(), &models.ITIntent{
		ID: uuid.New(), UserID: uuid.New(),
		Currency:         "GHS",
		PlatformKeptKobo: "1400",
	})

	if len(fees.events) != 0 {
		t.Errorf("carved a fee for an unpriceable currency: %+v — a wrong USD value "+
			"mints wrong TradePoints, which is distributed value that cannot be recalled",
			fees.events)
	}
}

// recordingFeeSink captures carve events instead of distributing them.
type recordingFeeSink struct{ events []FeeEvent }

func (r *recordingFeeSink) EnqueueFeeSettled(e FeeEvent) { r.events = append(r.events, e) }
