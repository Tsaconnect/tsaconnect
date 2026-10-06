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
	return s
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
	wantUnits(t, "fiat", q.FiatMinor, 16_502_500)
	wantUnits(t, "gross", q.GrossAssetUnits, 100_000_000)
	wantUnits(t, "net", q.AssetUnits, 94_537_500)
	wantUnits(t, "send fee", q.SendFeeUnits, 1_500_000)
	wantUnits(t, "effective rate", q.RatePerUnitMinor, 174561)
	wantUnits(t, "display rate", q.DisplayRateMinor, 169257)
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
	if len(q.Fees) != 2 {
		t.Fatalf("fees = %+v", q.Fees)
	}
	conv, network := q.Fees[0], q.Fees[1]
	if conv.Kind != FeeKindConversion || conv.Label != "Flutterwave conversion fee (1.5%)" || conv.Provider != "Flutterwave" ||
		conv.Units != "1462160" || conv.NgnMinor != "247480" {
		t.Errorf("conversion = %+v", conv)
	}
	if network.Kind != FeeKindNetwork || network.Label != "Network fee" || network.Provider != "Flutterwave" ||
		network.Units != "1500000" || network.NgnMinor != "253885" {
		t.Errorf("network = %+v", network)
	}
}

// Review Focus 5: tokenAmount = amount ÷ rate − Σfees, at every size, against
// the 2-dp display rate the client is shown.
func TestRoutedFees_InvariantHoldsAtEveryAmount(t *testing.T) {
	s := newRoutedQuoteService(t)
	for _, rate := range []*big.Rat{routedFWRate, big.NewRat(180137, 100), big.NewRat(1499, 1)} {
		for naira := int64(30_000); naira <= 3_000_000; naira += 7_919 {
			q, err := s.routedQuoteAt("USDC", "NGN", big.NewInt(naira*100), rate, false)
			if err != nil {
				t.Fatalf("₦%d at %s: %v", naira, rate.FloatString(2), err)
			}
			sum := new(big.Int)
			for _, f := range q.Fees {
				u, _ := new(big.Int).SetString(f.Units, 10)
				sum.Add(sum, u)
			}
			viaRate := new(big.Int).Sub(lockedUnits(q.FiatMinor, q.DisplayRateMinor), sum)
			diff := new(big.Int).Sub(viaRate, q.AssetUnits)
			if diff.CmpAbs(big.NewInt(1)) > 0 {
				t.Fatalf("₦%d at %s: amount÷rate−fees = %s, tokenAmount = %s", naira, rate.FloatString(2), viaRate, q.AssetUnits)
			}
		}
	}
}

// §14 as amended 2026-10-05: a venue with no fee lines (the company wallet,
// Paycrest) has no exact invariant — its rate is rounded to the kobo — but its
// rate is never more than one kobo from amount ÷ tokenAmount.
func TestTreasuryQuote_RateIsAmountOverTokensWithinAKobo(t *testing.T) {
	s := newRoutedQuoteService(t)
	oneKobo := big.NewRat(1, 1)
	for _, market := range []*big.Rat{treasuryMarket, big.NewRat(139733, 100), big.NewRat(1501, 1)} {
		for naira := int64(100); naira <= 270_000; naira += 1_237 {
			q, err := s.treasuryQuoteAt("USDC", "NGN", big.NewInt(naira*100), market, false)
			if err != nil {
				t.Fatalf("₦%d at %s: %v", naira, market.FloatString(2), err)
			}
			if len(q.Fees) != 0 {
				t.Fatalf("₦%d: a company-wallet quote carries fee lines %+v", naira, q.Fees)
			}
			// Kobo per whole token: fiatMinor × 10^6 ÷ base units.
			exact := new(big.Rat).SetFrac(new(big.Int).Mul(q.FiatMinor, big.NewInt(1_000_000)), q.AssetUnits)
			diff := new(big.Rat).Sub(new(big.Rat).SetInt(q.DisplayRateMinor), exact)
			if diff.Abs(diff).Cmp(oneKobo) > 0 {
				t.Fatalf("₦%d at %s: rate %s kobo, amount ÷ tokens = %s kobo", naira, market.FloatString(2),
					q.DisplayRateMinor, exact.FloatString(4))
			}
		}
	}
}

func TestQuoteRoutedStableBuy_Limits(t *testing.T) {
	s := newRoutedQuoteService(t)
	// ₦17,000 → 10.30 gross → 8.39 delivered: under 10.
	if _, err := s.QuoteRoutedStableBuy("USDC", "17000", "NGN", routedFWRate); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("17000: %v, want ErrStableBuyBelowMinimum", err)
	}
	// ₦20,000 → 10.14 delivered: just over.
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
		t.Fatalf("routed: %v, want 94.5 accepted", err)
	}
}

func TestSetRoutedStableBuyLimits_ConfiguresAndKeepsGoodValuesOnATypo(t *testing.T) {
	s := newRoutedQuoteService(t)
	if err := s.SetRoutedStableBuyLimits(RoutedStableBuyLimits{MinTokens: "5", MaxUSD: "500", SendFeeUSD: "0.5"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	// ₦9,000 → 5.45 gross → 5.24 → 4.74 after a 0.50 fee: under 5.
	if _, err := s.QuoteRoutedStableBuy("USDC", "9000", "NGN", routedFWRate); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("min 5: %v", err)
	}
	// ₦900,000 → ~523 delivered: over 500.
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

// Owner 2026-10-04 #3 and S7: the routed spread and the conversion fee are
// the routed buy's own; the old endpoint keeps 155 bps and no conversion fee.
func TestRoutedSpreadAndConversionFee_AreSeparateFromTheITSpread(t *testing.T) {
	s := newRoutedQuoteService(t)
	old, err := s.QuoteStableBuy(context.Background(), "USDC", "16502500", "NGN")
	if err != nil || old.AssetUnits.Cmp(big.NewInt(98_450_000)) != 0 {
		t.Fatalf("old path = %v %v, want 98.45 at 155 bps (unchanged)", old, err)
	}
	if old.DisplayRateMinor == nil || old.DisplayRateMinor.Cmp(old.RatePerUnitMinor) != 0 || len(old.Fees) != 0 {
		t.Fatalf("old path display = %v fees = %v, want the rate itself and no fee lines", old.DisplayRateMinor, old.Fees)
	}
	if s.RoutedSpreadBps() != DefaultRoutedSpreadBps {
		t.Fatalf("routed spread = %d", s.RoutedSpreadBps())
	}
	if err := s.SetRoutedStableBuyLimits(RoutedStableBuyLimits{SpreadBps: 300, ConversionFeeBps: 200}); err != nil {
		t.Fatal(err)
	}
	q, err := s.QuoteRoutedStableBuy("USDC", "165025", "NGN", routedFWRate)
	// 100 → 98.00 (200 bps) → 95.06 (300 bps) → 93.56 after the fee.
	if err != nil || q.AssetUnits.Cmp(big.NewInt(93_560_000)) != 0 {
		t.Fatalf("routed at 300/200 bps = %v %v", q, err)
	}
	for _, bad := range []int64{-1, 10_000} {
		if err := s.SetRoutedStableBuyLimits(RoutedStableBuyLimits{SpreadBps: bad}); err == nil {
			t.Fatalf("spread %d accepted", bad)
		}
	}
	if s.RoutedSpreadBps() != 300 {
		t.Fatalf("a bad spread changed it to %d", s.RoutedSpreadBps())
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
	if err != nil || q.AssetUnits.Cmp(big.NewInt(94_537_500)) != 0 {
		t.Fatalf("amount-mode quote = %v %v", q, err)
	}
}

// Owner 2026-10-04 #1: Flutterwave live pricing has a minimum but no maximum.
func TestRoutedQuoteAt_LivePricingHasNoMaximum(t *testing.T) {
	s := newRoutedQuoteService(t)
	if _, err := s.routedQuoteAt("USDC", "NGN", big.NewInt(400_000_000), routedFWRate, true); !errors.Is(err, ErrStableBuyAboveMaximum) {
		t.Fatalf("quote: %v, want the maximum", err)
	}
	if q, err := s.routedQuoteAt("USDC", "NGN", big.NewInt(400_000_000), routedFWRate, false); err != nil || q.AssetUnits.Sign() <= 0 {
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
	q, err := newRoutedQuoteService(t).quoteRoutedLive(context.Background(), "USDC", big.NewInt(16_502_500), "NGN")
	if err != nil || q.AssetUnits.Cmp(big.NewInt(94_537_500)) != 0 {
		t.Fatalf("q = %+v err = %v", q, err)
	}
}

// §2.3 #2/#3/#4: market + margin, ₦100 minimum on naira, $200 maximum on tokens.
func TestQuoteTreasuryBuy_MarginMinimumMaximum(t *testing.T) {
	s := newRoutedQuoteService(t)
	q, err := s.QuoteTreasuryBuy("usdc", "10000", "ngn", treasuryMarket)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	wantUnits(t, "gross", q.GrossAssetUnits, 7_418_397)
	wantUnits(t, "net", q.AssetUnits, 7_232_937)
	wantUnits(t, "effective rate", q.RatePerUnitMinor, 138257)
	wantUnits(t, "display rate", q.DisplayRateMinor, 138257)
	if len(q.Fees) != 0 || q.SendFeeUnits != nil {
		t.Fatalf("a company-wallet quote has no fee lines: %+v", q.Fees)
	}
	// Parity with the shared arithmetic (spec §4.1 extraction).
	_, net, _ := priceStableBuyAt(treasuryMarket, big.NewInt(1_000_000), DefaultTreasuryMarginBps)
	if net.Cmp(q.AssetUnits) != 0 {
		t.Fatalf("priceStableBuyAt = %s, quote = %s", net, q.AssetUnits)
	}
	if q, err := s.QuoteTreasuryBuy("USDT", "100", "NGN", treasuryMarket); err != nil || q.AssetUnits.Cmp(big.NewInt(72_328)) != 0 {
		t.Fatalf("₦100: %v %v, want 0.072328 USDT", q, err)
	}
	if _, err := s.QuoteTreasuryBuy("USDC", "99.99", "NGN", treasuryMarket); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("₦99.99: %v", err)
	}
	// ₦276,554 → 200.029784 delivered: over 200.
	if _, err := s.QuoteTreasuryBuy("USDC", "276554", "NGN", treasuryMarket); !errors.Is(err, ErrStableBuyAboveMaximum) {
		t.Fatalf("over $200: %v", err)
	}
	if _, err := s.treasuryQuoteAt("USDC", "NGN", big.NewInt(27_655_400), treasuryMarket, false); err != nil {
		t.Fatalf("operator waiver: %v, want priced", err)
	}
	if got := s.TreasuryMinOrderUnits(treasuryMarket); got.Cmp(big.NewInt(72_328)) != 0 {
		t.Fatalf("smallest order = %s, want what ₦100 buys", got)
	}
}

func TestSetTreasuryBuyLimits_ConfiguresAndKeepsGoodValuesOnATypo(t *testing.T) {
	s := newRoutedQuoteService(t)
	if c := s.treasuryLimits().confirmations; c != DefaultTreasuryConfirmations {
		t.Fatalf("default confirmations = %d, want 5", c)
	}
	if err := s.SetTreasuryBuyLimits(TreasuryBuyLimits{MarginBps: 300, MinNGN: "500", MaxUSD: "100", DailyUserMaxUSD: "300",
		SendStall: time.Hour, Confirmations: 12}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QuoteTreasuryBuy("USDC", "100", "NGN", treasuryMarket); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("min ₦500: %v", err)
	}
	q, err := s.QuoteTreasuryBuy("USDC", "10000", "NGN", treasuryMarket)
	if err != nil || q.AssetUnits.Cmp(big.NewInt(7_195_845)) != 0 {
		t.Fatalf("300 bps: %v %v", q, err)
	}
	lim := s.treasuryLimits()
	if lim.dailyUserMaxUnits.Cmp(big.NewInt(300_000_000)) != 0 || lim.sendStall != time.Hour || lim.confirmations != 12 {
		t.Fatalf("limits = %+v", lim)
	}
	if err := s.SetTreasuryBuyLimits(TreasuryBuyLimits{MinNGN: "abc", MarginBps: 10_000}); err == nil {
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
