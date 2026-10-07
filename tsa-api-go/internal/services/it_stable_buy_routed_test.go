package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"
)

// routedFWRate is the Flutterwave worked example's rate: ₦1650.25 per token.
var routedFWRate = big.NewRat(165025, 100)

// treasuryMarket is the company wallet's worked example: ₦1348 per token.
var treasuryMarket = big.NewRat(1348, 1)

func newRoutedQuoteService(t *testing.T) *ITService {
	t.Helper()
	s := NewITService(nil, nil, stubFx{ngn: 1650})
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	if err := s.SetStableSpreadBps(155); err != nil {
		t.Fatal(err)
	}
	wireStableBuyPrereqs(s)
	// The pricing fixtures below were all worked at the central account's
	// 1.40% funding fee; this rig has no central account (VA mode), so the VA
	// fee is pinned to the same figure. Channel pricing itself — 1.40% central,
	// 2.15% VA — is pinned in it_stable_buy_funding_channel_test.go.
	pinFundingFee(t, s)
	return s
}

// pinFundingFee prices both pay-in channels at the central account's fee.
func pinFundingFee(t *testing.T, s *ITService) {
	t.Helper()
	if err := s.SetRampBuyTerms(RampBuyTerms{VAFundingFeeBps: DefaultRampFundingFeeBps}); err != nil {
		t.Fatal(err)
	}
}

func wantUnits(t *testing.T, name string, got *big.Int, want int64) {
	t.Helper()
	if got == nil || got.Cmp(big.NewInt(want)) != 0 {
		t.Errorf("%s = %v, want %d", name, got, want)
	}
}

func TestQuoteRoutedStableBuy_FoldsEveryFeeIntoTheEffectiveRate(t *testing.T) {
	q, err := newRoutedQuoteService(t).QuoteRoutedStableBuy("usdc", "165025", "ngn", routedFWRate)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	// Owner 2026-10-07: ₦1,650.25 × 1.005 = ₦1,658.50 buys 99.502562; less
	// funding 1.4%, conversion 1.5% and the $1.50 send, she receives 95.137885.
	wantUnits(t, "fiat", q.FiatMinor, 16_502_500)
	wantUnits(t, "gross", q.GrossAssetUnits, 100_000_000)
	wantUnits(t, "net", q.AssetUnits, 95_137_885)
	wantUnits(t, "send fee", q.SendFeeUnits, 1_500_000)
	wantUnits(t, "effective rate", q.RatePerUnitMinor, 173459)
	wantUnits(t, "display rate", q.DisplayRateMinor, 165850)
	if q.Asset != "USDC" || q.Currency != "NGN" {
		t.Fatalf("asset/currency = %q/%q", q.Asset, q.Currency)
	}
	// Decision D6: pricing the same naira at the effective rate never
	// delivers more than the quote did.
	if lockedUnits(q.FiatMinor, q.RatePerUnitMinor).Cmp(q.AssetUnits) > 0 {
		t.Fatal("the effective rate over-delivers")
	}
}

// §14: each provider fee is its own line, priced at the display rate.
func TestQuoteRoutedStableBuy_FeeLines(t *testing.T) {
	q, err := newRoutedQuoteService(t).QuoteRoutedStableBuy("USDC", "165025", "NGN", routedFWRate)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Fees) != 3 {
		t.Fatalf("fees = %+v", q.Fees)
	}
	funding, conv, network := q.Fees[0], q.Fees[1], q.Fees[2]
	if funding.Kind != FeeKindFunding || funding.Label != "Funding fee (1.4%)" || funding.Provider != "Flutterwave" ||
		funding.Units != "1393035" || funding.NgnMinor != "231034" {
		t.Errorf("funding = %+v", funding)
	}
	if conv.Kind != FeeKindConversion || conv.Label != "Flutterwave conversion fee (1.5%)" || conv.Provider != "Flutterwave" ||
		conv.Units != "1471642" || conv.NgnMinor != "244071" {
		t.Errorf("conversion = %+v", conv)
	}
	if network.Kind != FeeKindNetwork || network.Label != "Network fee" || network.Provider != "Flutterwave" ||
		network.Units != "1500000" || network.NgnMinor != "248775" {
		t.Errorf("network = %+v", network)
	}
}

// Owner ruling 2026-10-07, worked example: ₦25,000 at Flutterwave's ₦1,387.51
// and a 50 bps take is shown at ₦1,394.44 (17.928343 USDC) less three lines.
func TestQuoteRoutedStableBuy_WorkedExample(t *testing.T) {
	q, err := newRoutedQuoteService(t).QuoteRoutedStableBuy("USDC", "25000", "NGN", big.NewRat(138751, 100))
	if err != nil {
		t.Fatal(err)
	}
	wantUnits(t, "display rate", q.DisplayRateMinor, 139444)
	wantUnits(t, "net", q.AssetUnits, 15_912_187)
	wantUnits(t, "effective rate", q.RatePerUnitMinor, 157113)
	want := []StableBuyFee{
		{Kind: FeeKindFunding, Label: "Funding fee (1.4%)", Provider: "Flutterwave", Units: "250996", NgnMinor: "34999"},
		{Kind: FeeKindConversion, Label: "Flutterwave conversion fee (1.5%)", Provider: "Flutterwave", Units: "265160", NgnMinor: "36974"},
		{Kind: FeeKindNetwork, Label: "Network fee", Provider: "Flutterwave", Units: "1500000", NgnMinor: "209166"},
	}
	if len(q.Fees) != len(want) {
		t.Fatalf("fees = %+v", q.Fees)
	}
	for i := range want {
		if q.Fees[i] != want[i] {
			t.Errorf("fee %d = %+v, want %+v", i, q.Fees[i], want[i])
		}
	}
}

// Owner ruling 2026-10-07 (1): the company wallet's rate is the market
// grossed up by the take; its only fee line is the central-account funding fee.
func TestQuoteTreasuryBuy_MarketPlusTakeAndAFundingLine(t *testing.T) {
	q, err := newRoutedQuoteService(t).QuoteTreasuryBuy("USDC", "10000", "NGN", treasuryMarket)
	if err != nil {
		t.Fatal(err)
	}
	// ₦1,348 × 1.005 = ₦1,354.74 buys 7.381490; less 1.4% funding = 7.278150.
	wantUnits(t, "display rate", q.DisplayRateMinor, 135474)
	wantUnits(t, "net", q.AssetUnits, 7_278_150)
	want := StableBuyFee{Kind: FeeKindFunding, Label: "Funding fee (1.4%)", Provider: "Flutterwave", Units: "103340", NgnMinor: "13999"}
	if len(q.Fees) != 1 || q.Fees[0] != want {
		t.Fatalf("fees = %+v, want only %+v", q.Fees, want)
	}
}

// Owner ruling 2026-10-07 (3): the take moves the rate and nothing else — the
// same third-party lines, at the same percentages, come off what it buys.
func TestCompanyTake_ChangesOnlyOurMargin(t *testing.T) {
	s := newRoutedQuoteService(t)
	fw50, err := s.QuoteRoutedStableBuy("USDC", "165025", "NGN", routedFWRate)
	if err != nil {
		t.Fatal(err)
	}
	tw50, err := s.QuoteTreasuryBuy("USDC", "10000", "NGN", treasuryMarket)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRampBuyTerms(RampBuyTerms{CompanyTakeBps: 100}); err != nil {
		t.Fatal(err)
	}
	fw100, err := s.QuoteRoutedStableBuy("USDC", "165025", "NGN", routedFWRate)
	if err != nil {
		t.Fatal(err)
	}
	tw100, err := s.QuoteTreasuryBuy("USDC", "10000", "NGN", treasuryMarket)
	if err != nil {
		t.Fatal(err)
	}
	// The rate is the base × (1 + take), to the kobo: ₦1,666.75 and ₦1,361.48.
	wantUnits(t, "fw display at 100 bps", fw100.DisplayRateMinor, 166675)
	wantUnits(t, "treasury display at 100 bps", tw100.DisplayRateMinor, 136148)
	wantUnits(t, "fw net at 100 bps", fw100.AssetUnits, 94_659_551)
	wantUnits(t, "treasury net at 100 bps", tw100.AssetUnits, 7_242_119)
	for _, pair := range [][2]*StableBuyQuote{{fw50, fw100}, {tw50, tw100}} {
		a, b := pair[0], pair[1]
		if len(a.Fees) != len(b.Fees) {
			t.Fatalf("fee lines changed with the take: %+v vs %+v", a.Fees, b.Fees)
		}
		for i := range a.Fees {
			if a.Fees[i].Kind != b.Fees[i].Kind || a.Fees[i].Label != b.Fees[i].Label || a.Fees[i].Provider != b.Fees[i].Provider {
				t.Fatalf("fee line %d changed with the take: %+v vs %+v", i, a.Fees[i], b.Fees[i])
			}
			// A percentage line stays at its own share of what the rate buys:
			// in naira it does not move by more than a kobo.
			na, _ := new(big.Int).SetString(a.Fees[i].NgnMinor, 10)
			nb, _ := new(big.Int).SetString(b.Fees[i].NgnMinor, 10)
			if a.Fees[i].Kind != FeeKindNetwork && new(big.Int).Sub(na, nb).CmpAbs(big.NewInt(1)) > 0 {
				t.Errorf("%s moved from %s to %s kobo with the take", a.Fees[i].Label, na, nb)
			}
			if a.Fees[i].Kind == FeeKindNetwork && a.Fees[i].Units != b.Fees[i].Units {
				t.Errorf("the send fee moved with the take: %s → %s", a.Fees[i].Units, b.Fees[i].Units)
			}
		}
	}
}

// reconcilesExactly is §14's invariant: amount ÷ rate − Σfees = tokenAmount,
// to the base unit, at the 2-dp display rate the client is shown.
func reconcilesExactly(t *testing.T, what string, q *StableBuyQuote) {
	t.Helper()
	if len(q.Fees) == 0 {
		t.Fatalf("%s: no fee lines", what)
	}
	sum := new(big.Int)
	for _, f := range q.Fees {
		u, _ := new(big.Int).SetString(f.Units, 10)
		sum.Add(sum, u)
	}
	viaRate := new(big.Int).Sub(lockedUnits(q.FiatMinor, q.DisplayRateMinor), sum)
	if viaRate.Cmp(q.AssetUnits) != 0 {
		t.Fatalf("%s: amount÷rate−fees = %s, tokenAmount = %s", what, viaRate, q.AssetUnits)
	}
}

// Review Focus 5 / owner ruling 2026-10-07 (4): both in-house providers carry
// fee lines, and each reconciles exactly at every size and rate.
func TestInHouseFees_ReconcileExactlyForBothProviders(t *testing.T) {
	s := newRoutedQuoteService(t)
	for _, rate := range []*big.Rat{routedFWRate, big.NewRat(180137, 100), big.NewRat(1499, 1), big.NewRat(138751, 100)} {
		for naira := int64(30_000); naira <= 3_000_000; naira += 7_919 {
			q, err := s.routedQuoteAt("USDC", "NGN", big.NewInt(naira*100), rate, false, PayInCentral)
			if err != nil {
				t.Fatalf("₦%d at %s: %v", naira, rate.FloatString(2), err)
			}
			reconcilesExactly(t, "flutterwave ₦"+big.NewInt(naira).String()+" at "+rate.FloatString(2), q)
		}
	}
	for _, market := range []*big.Rat{treasuryMarket, big.NewRat(139733, 100), big.NewRat(1501, 1)} {
		for naira := int64(100); naira <= 270_000; naira += 1_237 {
			q, err := s.treasuryQuoteAt("USDC", "NGN", big.NewInt(naira*100), market, false, PayInCentral)
			if err != nil {
				t.Fatalf("₦%d at %s: %v", naira, market.FloatString(2), err)
			}
			reconcilesExactly(t, "company wallet ₦"+big.NewInt(naira).String()+" at "+market.FloatString(2), q)
		}
	}
}

func TestQuoteRoutedStableBuy_Limits(t *testing.T) {
	s := newRoutedQuoteService(t)
	// ₦17,000 → 10.30 gross → 8.46 delivered: under 10.
	if _, err := s.QuoteRoutedStableBuy("USDC", "17000", "NGN", routedFWRate); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("17000: %v, want ErrStableBuyBelowMinimum", err)
	}
	// ₦20,000 → 10.21 delivered: just over.
	if _, err := s.QuoteRoutedStableBuy("USDC", "20000", "NGN", routedFWRate); err != nil {
		t.Fatalf("20000: %v", err)
	}
	// ₦4,000,000 → ~2326 delivered: over 2000.
	if _, err := s.QuoteRoutedStableBuy("USDC", "4000000", "NGN", routedFWRate); !errors.Is(err, ErrStableBuyAboveMaximum) {
		t.Fatalf("4000000: %v, want ErrStableBuyAboveMaximum", err)
	}
	if _, err := s.QuoteRoutedStableBuy("USDC", "12,000", "NGN", routedFWRate); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("unparsable amount: %v", err)
	}
	if _, err := s.QuoteRoutedStableBuy("DAI", "165025", "NGN", routedFWRate); !errors.Is(err, ErrStableNotAnAsset) {
		t.Fatalf("DAI: %v", err)
	}
}

// Spec §9: IT_STABLE_MIN_SELL no longer bounds a routed buy.
func TestQuoteRoutedStableBuy_IgnoresTheSellFloor(t *testing.T) {
	s := newRoutedQuoteService(t)
	if err := s.SetStableMinSell("100"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QuoteStableBuy(context.Background(), "USDC", "16502500", "NGN"); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("old path: %v, want refused under the 100 floor (fixture check)", err)
	}
	if _, err := s.QuoteRoutedStableBuy("USDC", "165025", "NGN", routedFWRate); err != nil {
		t.Fatalf("routed: %v, want 95.1 accepted", err)
	}
}

func TestSetRoutedStableBuyLimits_ConfiguresAndKeepsGoodValuesOnATypo(t *testing.T) {
	s := newRoutedQuoteService(t)
	if err := s.SetRoutedStableBuyLimits(RoutedStableBuyLimits{MinTokens: "5", MaxUSD: "500", SendFeeUSD: "0.5"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	// ₦9,000 → 5.43 at the rate → 5.27 after funding and conversion → 4.77 after a 0.50 fee: under 5.
	if _, err := s.QuoteRoutedStableBuy("USDC", "9000", "NGN", routedFWRate); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("min 5: %v", err)
	}
	// ₦900,000 → ~527 delivered: over 500.
	if _, err := s.QuoteRoutedStableBuy("USDC", "900000", "NGN", routedFWRate); !errors.Is(err, ErrStableBuyAboveMaximum) {
		t.Fatalf("max 500: %v", err)
	}
	// Decision D25: a typo is loud and changes nothing.
	if err := s.SetRoutedStableBuyLimits(RoutedStableBuyLimits{MinTokens: "abc", ConversionFeeBps: -5}); err == nil {
		t.Fatal("garbage limits accepted silently")
	}
	if _, err := s.QuoteRoutedStableBuy("USDC", "9000", "NGN", routedFWRate); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("min after a typo: %v, want 5 kept", err)
	}
	if err := s.SetRoutedStableBuyLimits(RoutedStableBuyLimits{MinTokens: "600"}); err == nil {
		t.Fatal("min above max accepted")
	}
}

// Owner 2026-10-04 #3 and S7: the company take and the conversion fee are
// the routed buy's own; the old endpoint keeps 155 bps and no conversion fee.
func TestRoutedTakeAndConversionFee_AreSeparateFromTheITSpread(t *testing.T) {
	s := newRoutedQuoteService(t)
	old, err := s.QuoteStableBuy(context.Background(), "USDC", "16502500", "NGN")
	if err != nil || old.AssetUnits.Cmp(big.NewInt(98_450_000)) != 0 {
		t.Fatalf("old path = %v %v, want 98.45 at 155 bps (unchanged)", old, err)
	}
	if old.DisplayRateMinor == nil || old.DisplayRateMinor.Cmp(old.RatePerUnitMinor) != 0 || len(old.Fees) != 0 {
		t.Fatalf("old path display = %v fees = %v, want the rate itself and no fee lines", old.DisplayRateMinor, old.Fees)
	}
	if s.rampTerms().takeBps != DefaultRampCompanyTakeBps || s.rampTerms().fundingFeeBps != DefaultRampFundingFeeBps {
		t.Fatalf("take/funding = %d/%d, want 50/140", s.rampTerms().takeBps, s.rampTerms().fundingFeeBps)
	}
	if err := s.SetRoutedStableBuyLimits(RoutedStableBuyLimits{ConversionFeeBps: 200}); err != nil {
		t.Fatal(err)
	}
	q, err := s.QuoteRoutedStableBuy("USDC", "165025", "NGN", routedFWRate)
	// 99.502562 at ₦1,658.50 → less 1.4% → less 2.0% → less 1.50 = 94.647337.
	if err != nil || q.AssetUnits.Cmp(big.NewInt(94_647_337)) != 0 {
		t.Fatalf("routed at a 200 bps conversion fee = %v %v", q, err)
	}
	for _, bad := range []int64{-1, 10_000} {
		if err := s.SetRampBuyTerms(RampBuyTerms{CompanyTakeBps: bad, FundingFeeBps: bad}); err == nil {
			t.Fatalf("take/funding %d accepted", bad)
		}
	}
	if s.rampTerms().takeBps != DefaultRampCompanyTakeBps || s.rampTerms().fundingFeeBps != DefaultRampFundingFeeBps {
		t.Fatalf("a bad value changed take/funding to %d/%d", s.rampTerms().takeBps, s.rampTerms().fundingFeeBps)
	}
}

// Decision D31: the send-fee mode is configured, defaulting to "wallet"; the
// quote deducts the fee once in either mode.
func TestSendFeeMode_DefaultsToWalletAndRefusesTypos(t *testing.T) {
	s := newRoutedQuoteService(t)
	if s.routedLimits().sendFeeFromAmount {
		t.Fatal("default must be wallet")
	}
	if err := s.SetRoutedStableBuyLimits(RoutedStableBuyLimits{SendFeeMode: "amount"}); err != nil || !s.routedLimits().sendFeeFromAmount {
		t.Fatalf("amount mode: %v", err)
	}
	if err := s.SetRoutedStableBuyLimits(RoutedStableBuyLimits{SendFeeMode: "both"}); err == nil || !s.routedLimits().sendFeeFromAmount {
		t.Fatalf("typo accepted or mode lost: %v", err)
	}
	q, err := s.QuoteRoutedStableBuy("USDC", "165025", "NGN", routedFWRate)
	if err != nil || q.AssetUnits.Cmp(big.NewInt(95_137_885)) != 0 {
		t.Fatalf("amount-mode quote = %v %v", q, err)
	}
}

// Owner 2026-10-04 #1: Flutterwave live pricing has a minimum but no maximum.
func TestRoutedQuoteAt_LivePricingHasNoMaximum(t *testing.T) {
	s := newRoutedQuoteService(t)
	if _, err := s.routedQuoteAt("USDC", "NGN", big.NewInt(400_000_000), routedFWRate, true, PayInCentral); !errors.Is(err, ErrStableBuyAboveMaximum) {
		t.Fatalf("quote: %v, want the maximum", err)
	}
	if q, err := s.routedQuoteAt("USDC", "NGN", big.NewInt(400_000_000), routedFWRate, false, PayInCentral); err != nil || q.AssetUnits.Sign() <= 0 {
		t.Fatalf("live: %v %v, want priced", q, err)
	}
}

func TestStableBuyFiatPerUnit_GatesAndPlausibility(t *testing.T) {
	ctx := context.Background()
	s := newRoutedQuoteService(t)
	rate, err := s.StableBuyFiatPerUnit(ctx, "ngn", "usdc")
	if err != nil || rate.Cmp(routedFWRate) != 0 {
		t.Fatalf("rate = %v err = %v, want 1650.25", rate, err)
	}
	dormant := NewITService(nil, nil, stubFx{ngn: 1650})
	dormant.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	if _, err := dormant.StableBuyFiatPerUnit(ctx, "NGN", "USDC"); !errors.Is(err, ErrStableQuoteUnavailable) {
		t.Fatalf("unwired rail: %v", err)
	}
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 5000})
	if _, err := s.StableBuyFiatPerUnit(ctx, "NGN", "USDC"); !errors.Is(err, ErrStableRateImplausible) {
		t.Fatalf("implausible: %v", err)
	}
}

func TestQuoteRoutedLive_PricesAtTheLiveRateOnRoutedTerms(t *testing.T) {
	q, err := newRoutedQuoteService(t).quoteRoutedLive(context.Background(), "USDC", big.NewInt(16_502_500), "NGN", PayInCentral)
	if err != nil || q.AssetUnits.Cmp(big.NewInt(95_137_885)) != 0 {
		t.Fatalf("q = %+v err = %v", q, err)
	}
}

// §2.3 #2/#3/#4: market + take, ₦100 minimum on naira, $200 maximum on tokens.
func TestQuoteTreasuryBuy_TakeMinimumMaximum(t *testing.T) {
	s := newRoutedQuoteService(t)
	q, err := s.QuoteTreasuryBuy("usdc", "10000", "ngn", treasuryMarket)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	wantUnits(t, "gross", q.GrossAssetUnits, 7_418_397)
	wantUnits(t, "net", q.AssetUnits, 7_278_150)
	wantUnits(t, "effective rate", q.RatePerUnitMinor, 137398)
	wantUnits(t, "display rate", q.DisplayRateMinor, 135474)
	if len(q.Fees) != 1 || q.Fees[0].Kind != FeeKindFunding || q.SendFeeUnits != nil {
		t.Fatalf("a company-wallet quote has the funding line only: %+v", q.Fees)
	}
	if q, err := s.QuoteTreasuryBuy("USDT", "100", "NGN", treasuryMarket); err != nil || q.AssetUnits.Cmp(big.NewInt(72_781)) != 0 {
		t.Fatalf("₦100: %v %v, want 0.072781 USDT", q, err)
	}
	if _, err := s.QuoteTreasuryBuy("USDC", "99.99", "NGN", treasuryMarket); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("₦99.99: %v", err)
	}
	// ₦276,554 → ~201.3 delivered: over 200.
	if _, err := s.QuoteTreasuryBuy("USDC", "276554", "NGN", treasuryMarket); !errors.Is(err, ErrStableBuyAboveMaximum) {
		t.Fatalf("over $200: %v", err)
	}
	if _, err := s.treasuryQuoteAt("USDC", "NGN", big.NewInt(27_655_400), treasuryMarket, false, PayInCentral); err != nil {
		t.Fatalf("operator waiver: %v, want priced", err)
	}
	if got := s.TreasuryMinOrderUnits(treasuryMarket); got.Cmp(big.NewInt(72_781)) != 0 {
		t.Fatalf("smallest order = %s, want what ₦100 buys", got)
	}
}

func TestSetTreasuryBuyLimits_ConfiguresAndKeepsGoodValuesOnATypo(t *testing.T) {
	s := newRoutedQuoteService(t)
	if c := s.treasuryLimits().confirmations; c != DefaultTreasuryConfirmations {
		t.Fatalf("default confirmations = %d, want 5", c)
	}
	if err := s.SetTreasuryBuyLimits(TreasuryBuyLimits{MinNGN: "500", MaxUSD: "100", DailyUserMaxUSD: "300",
		SendStall: time.Hour, Confirmations: 12}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QuoteTreasuryBuy("USDC", "100", "NGN", treasuryMarket); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("min ₦500: %v", err)
	}
	q, err := s.QuoteTreasuryBuy("USDC", "10000", "NGN", treasuryMarket)
	if err != nil || q.AssetUnits.Cmp(big.NewInt(7_278_150)) != 0 {
		t.Fatalf("₦10,000: %v %v", q, err)
	}
	lim := s.treasuryLimits()
	if lim.dailyUserMaxUnits.Cmp(big.NewInt(300_000_000)) != 0 || lim.sendStall != time.Hour || lim.confirmations != 12 {
		t.Fatalf("limits = %+v", lim)
	}
	if err := s.SetTreasuryBuyLimits(TreasuryBuyLimits{MinNGN: "abc", MaxUSD: "-1"}); err == nil {
		t.Fatal("garbage accepted")
	}
	if _, err := s.QuoteTreasuryBuy("USDC", "100", "NGN", treasuryMarket); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatal("a typo changed the minimum")
	}
}

func TestSetRampBuyTerms_SharedCapAndGrace(t *testing.T) {
	s := newRoutedQuoteService(t)
	if s.RoutedAbandonCap() != DefaultRampDailyAbandonCap || s.rampTerms().honourGrace != 10*time.Minute {
		t.Fatalf("defaults = %d %s", s.RoutedAbandonCap(), s.rampTerms().honourGrace)
	}
	if err := s.SetRampBuyTerms(RampBuyTerms{DailyAbandonCap: 3, HonourGrace: 5 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRampBuyTerms(RampBuyTerms{DailyAbandonCap: -1, HonourGrace: -time.Second}); err == nil {
		t.Fatal("bad terms accepted")
	}
	if s.RoutedAbandonCap() != 3 || s.rampTerms().honourGrace != 5*time.Minute {
		t.Fatalf("after a typo: %d %s", s.RoutedAbandonCap(), s.rampTerms().honourGrace)
	}
}

func TestFormatHelpers(t *testing.T) {
	for in, want := range map[string]string{"174561": "1745.61", "16502500": "165025.00", "x": ""} {
		if got := FormatMinor(in); got != want {
			t.Errorf("FormatMinor(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"94537500": "94.5375", "10000000": "10", "0": "0", "1": "0.000001", "-1": ""} {
		if got := FormatStableUnits(in); got != want {
			t.Errorf("FormatStableUnits(%q) = %q, want %q", in, got, want)
		}
	}
	for bps, want := range map[int64]string{150: "1.5", 100: "1", 125: "1.25", 5: "0.05"} {
		if got := formatBps(bps); got != want {
			t.Errorf("formatBps(%d) = %q, want %q", bps, got, want)
		}
	}
}
