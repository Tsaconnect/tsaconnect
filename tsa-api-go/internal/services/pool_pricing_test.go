package services

import (
	"errors"
	"math/big"
	"testing"
)

// poolMid is spec §10.2's M: ₦1,348.00 per USDT.
var poolMid = big.NewRat(134800, 100)

func centralPoolService(t *testing.T) *ITService {
	t.Helper()
	s, _ := newRoutedBuyService(t)
	s.SetInboundCentral(true, testCentralAccount()) // central funding 1.40% (S15)
	return s
}

func quotePool(t *testing.T, s *ITService, naira string) *PoolPrice {
	t.Helper()
	p, err := s.QuotePoolBuy("USDT", naira, "NGN", poolMid, specPoolSettings())
	if err != nil {
		t.Fatalf("quote ₦%s: %v", naira, err)
	}
	return p
}

// Roadmap risk 10 / spec §10.2 at 50 bps (decision 19).
func TestPoolPricing_WorkedExample100k(t *testing.T) {
	p := quotePool(t, centralPoolService(t), "100000")
	q := p.Quote
	if p.MidMinor.Int64() != 134800 || p.LPRateMinor.Int64() != 135474 || q.DisplayRateMinor.Int64() != 136151 {
		t.Fatalf("M=%s L=%s D=%s, want 134800/135474/136151", p.MidMinor, p.LPRateMinor, q.DisplayRateMinor)
	}
	if q.AssetUnits.Int64() != 72_419_593 || q.RatePerUnitMinor.Int64() != 138085 {
		t.Fatalf("net=%s effective=%s, want 72.419593 at ₦1,380.85", q.AssetUnits, q.RatePerUnitMinor)
	}
	if len(q.Fees) != 1 || q.Fees[0].Kind != FeeKindFunding || q.Fees[0].Units != "1028270" || q.Fees[0].NgnMinor != "139999" {
		t.Fatalf("fees = %+v, want one funding line 1.028270 (₦1,399.99)", q.Fees)
	}
	a, b := PoolLPPayoutKobo(big.NewInt(60_000_000), p.LPRateMinor), PoolLPPayoutKobo(big.NewInt(12_419_593), p.LPRateMinor)
	if a.Int64() != 8_128_440 || b.Int64() != 1_682_531 {
		t.Fatalf("LP payouts = %s / %s kobo, want ₦81,284.40 / ₦16,825.31", a, b)
	}
	if take := PoolTakeKobo(q.FiatMinor, 50); take.Int64() != 49_752 {
		t.Fatalf("take = %s, want ₦497.52", take)
	}
	// Keep = fiat − actual funding (₦1,400.00) − Σ LP payouts − 2 × ₦10 payout fees.
	keep := 10_000_000 - 140_000 - a.Int64() - b.Int64() - 2_000
	if keep != 47_029 {
		t.Fatalf("keep = %d kobo, want ₦470.29", keep)
	}
}

func TestPoolPricing_WorkedExample5k(t *testing.T) {
	p := quotePool(t, centralPoolService(t), "5000")
	q := p.Quote
	if q.AssetUnits.Int64() != 3_620_980 || q.Fees[0].Units != "51413" || q.Fees[0].NgnMinor != "6999" || q.RatePerUnitMinor.Int64() != 138085 {
		t.Fatalf("net=%s funding=%+v effective=%s", q.AssetUnits, q.Fees, q.RatePerUnitMinor)
	}
	lp := PoolLPPayoutKobo(q.AssetUnits, p.LPRateMinor)
	if lp.Int64() != 490_548 || 500_000-7_000-lp.Int64()-1_000 != 1_452 {
		t.Fatalf("LP naira = %s (want ₦4,905.48), keep must be ₦14.52", lp)
	}
}

// §14 invariant: fiat ÷ display rate − Σ fees = net, by construction.
func TestPoolPricing_FeeLineInvariant(t *testing.T) {
	s := centralPoolService(t)
	for _, naira := range []string{"2800", "5000", "73211.37", "100000", "2500000"} {
		p := quotePool(t, s, naira)
		left := lockedUnits(p.Quote.FiatMinor, p.Quote.DisplayRateMinor)
		fee, _ := new(big.Int).SetString(p.Quote.Fees[0].Units, 10)
		if new(big.Int).Sub(left, fee).Cmp(p.Quote.AssetUnits) != 0 {
			t.Fatalf("₦%s: left %s − fee %s ≠ net %s", naira, left, fee, p.Quote.AssetUnits)
		}
	}
}

// S15: central account only.
// Review I5 / §19: the buyer never sees a provider's name. fundingCost tags its
// line "Flutterwave" for the Flutterwave and company-wallet venues; the pool's
// line says "provider".
func TestPoolQuote_FundingLineNamesNoProvider(t *testing.T) {
	p := quotePool(t, centralPoolService(t), "100000")
	if len(p.Quote.Fees) == 0 {
		t.Fatal("no fee lines")
	}
	for _, f := range p.Quote.Fees {
		if f.Provider != PoolFeeProvider && f.Provider != "" {
			t.Fatalf("fee line %q tagged %q, want %q", f.Label, f.Provider, PoolFeeProvider)
		}
	}
}

func TestPoolQuote_RefusesVAChannel(t *testing.T) {
	s, _ := newRoutedBuyService(t) // VA mode
	if _, err := s.QuotePoolBuy("USDT", "100000", "NGN", poolMid, specPoolSettings()); !errors.Is(err, ErrPoolCentralOnly) {
		t.Fatalf("VA quote = %v, want ErrPoolCentralOnly", err)
	}
}

// §10.4 range: net below the minimum slice, above the order maximum. (The
// per-user daily cap is checked at open: TestPoolOpen_DailyCapRefusesForFailover.)
func TestPoolQuote_MinMax(t *testing.T) {
	s := centralPoolService(t)
	if _, err := s.QuotePoolBuy("USDT", "2700", "NGN", poolMid, specPoolSettings()); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("₦2,700 (net 1.955 < $2) = %v", err)
	}
	if _, err := s.QuotePoolBuy("USDT", "2800", "NGN", poolMid, specPoolSettings()); err != nil {
		t.Fatalf("₦2,800 (net 2.03) = %v", err)
	}
	if _, err := s.QuotePoolBuy("USDT", "3000000", "NGN", poolMid, specPoolSettings()); !errors.Is(err, ErrStableBuyAboveMaximum) {
		t.Fatalf("₦3,000,000 (net ≈ 2,172) = %v", err)
	}
}

// S46: the incident stop closes quotes; live pricing of money that arrived
// (enforceMax=false) still works — settlement keeps running.
func TestPoolQuote_OrdersStoppedClosesQuotesNotLivePricing(t *testing.T) {
	s := centralPoolService(t)
	set := specPoolSettings()
	set.OrdersStopped, set.OrderMaxUnits = true, new(big.Int)
	if _, err := s.QuotePoolBuy("USDT", "100000", "NGN", poolMid, set); !errors.Is(err, ErrPoolOrdersStopped) {
		t.Fatalf("stopped quote = %v", err)
	}
	if p, err := s.poolQuoteAt("USDT", "NGN", big.NewInt(10_000_000), poolMid, set, PayInCentral, false); err != nil || p.Quote.AssetUnits.Int64() != 72_419_593 {
		t.Fatalf("live price while stopped = %v %v", p, err)
	}
}
