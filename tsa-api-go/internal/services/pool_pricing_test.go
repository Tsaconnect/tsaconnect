package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
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
	p, err := s.QuotePoolBuy(context.Background(), "USDT", naira, "NGN", poolMid, specPoolSettings())
	if err != nil {
		t.Fatalf("quote ₦%s: %v", naira, err)
	}
	return p
}

// wantFees pins a quote's fee lines: kind, units and naira, in order.
func wantFees(t *testing.T, got []StableBuyFee, want ...[3]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("fees = %+v, want %d lines", got, len(want))
	}
	for i, w := range want {
		if got[i].Kind != w[0] || got[i].Units != w[1] || got[i].NgnMinor != w[2] || got[i].Provider != FeeProviderShown {
			t.Fatalf("fee %d = %+v, want %s %s units (%s kobo)", i, got[i], w[0], w[1], w[2])
		}
	}
}

// Spec §10.2 at 50 bps, shared pool (spec 2026-10-14): one fill — gas 0.03 +
// 0.01 — and no LP payout line (LPs earn their spread in the vault), so the
// net is the slice-era 72.340115 plus its 0.039478 payout line.
func TestPoolPricing_WorkedExample100k(t *testing.T) {
	p := quotePool(t, centralPoolService(t), "100000")
	q := p.Quote
	if p.MidMinor.Int64() != 134800 || p.LPRateMinor.Int64() != 135474 || q.DisplayRateMinor.Int64() != 136151 {
		t.Fatalf("M=%s L=%s D=%s, want 134800/135474/136151", p.MidMinor, p.LPRateMinor, q.DisplayRateMinor)
	}
	if q.AssetUnits.Int64() != 72_379_593 {
		t.Fatalf("net=%s, want 72.379593", q.AssetUnits)
	}
	wantFees(t, q.Fees,
		[3]string{FeeKindFunding, "1028270", "139999"},
		[3]string{FeeKindNetwork, "40000", "5446"})
	if take := PoolTakeKobo(q.FiatMinor, 50); take.Int64() != 49_752 {
		t.Fatalf("take = %s, want ₦497.52", take)
	}
}

func TestPoolPricing_WorkedExample5k(t *testing.T) {
	q := quotePool(t, centralPoolService(t), "5000").Quote
	if q.AssetUnits.Int64() != 3_580_980 {
		t.Fatalf("net=%s, want 3.580980 (3.573085 + the old 0.007895 payout line)", q.AssetUnits)
	}
	wantFees(t, q.Fees,
		[3]string{FeeKindFunding, "51413", "6999"},
		[3]string{FeeKindNetwork, "40000", "5446"})
}

// Network fee = pool_network_fee (the fill + the delivery); unset reads 0.
func TestPoolNetworkFeeUnits(t *testing.T) {
	if got := poolNetworkFeeUnits(specPoolSettings()); got.Int64() != 40_000 {
		t.Errorf("network = %s, want 40000", got)
	}
	if got := poolNetworkFeeUnits(PoolSettings{}); got.Sign() != 0 {
		t.Fatalf("unset gas = %s, want 0", got)
	}
}

// Owner 2026-10-13: a ~₦500 order at a ₦1,600 mid with pool_min_slice 0.25
// prices with every cost and still nets above the minimum; the take is in the
// display rate and every cost comes off after it, as on the Flutterwave route.
func TestPoolPricing_SmallOrderCoversCosts(t *testing.T) {
	s := centralPoolService(t)
	set := specPoolSettings()
	set.MinSliceUnits = big.NewInt(250_000)
	mid := big.NewRat(1600, 1)
	p, err := s.QuotePoolBuy(context.Background(), "USDT", "500", "NGN", mid, set)
	if err != nil {
		t.Fatalf("₦500: %v", err)
	}
	q := p.Quote
	if q.DisplayRateMinor.Int64() != 161604 || p.LPRateMinor.Int64() != 160800 {
		t.Fatalf("D=%s L=%s, want 161604 (M × 1.005 × 1.005) and 160800", q.DisplayRateMinor, p.LPRateMinor)
	}
	wantFees(t, q.Fees,
		[3]string{FeeKindFunding, "4331", "699"},
		[3]string{FeeKindNetwork, "40000", "6464"})
	if q.AssetUnits.Int64() != 265_067 || q.AssetUnits.Cmp(set.MinSliceUnits) < 0 {
		t.Fatalf("net = %s, want 0.265067 (≥ 0.25)", q.AssetUnits)
	}
	if _, err := s.QuotePoolBuy(context.Background(), "USDT", "460", "NGN", mid, set); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("₦460 = %v, want below the minimum", err)
	}
}

// §14 invariant: fiat ÷ display rate − Σ fees = net, by construction.
func TestPoolPricing_FeeLineInvariant(t *testing.T) {
	s := centralPoolService(t)
	for _, naira := range []string{"2900", "5000", "73211.37", "100000", "2500000"} {
		p := quotePool(t, s, naira)
		left := lockedUnits(p.Quote.FiatMinor, p.Quote.DisplayRateMinor)
		if len(p.Quote.Fees) != 2 {
			t.Fatalf("₦%s: fees = %+v, want funding and network", naira, p.Quote.Fees)
		}
		for _, f := range p.Quote.Fees {
			u, _ := new(big.Int).SetString(f.Units, 10)
			left.Sub(left, u)
		}
		if left.Cmp(p.Quote.AssetUnits) != 0 {
			t.Fatalf("₦%s: left − Σ fees = %s ≠ net %s", naira, left, p.Quote.AssetUnits)
		}
	}
}

// S15: central account only.
// Review I5 / I-4 / §19: the buyer never sees a provider's name; every fee
// line says "provider" from where it is made.
func TestPoolQuote_FundingLineNamesNoProvider(t *testing.T) {
	p := quotePool(t, centralPoolService(t), "100000")
	if len(p.Quote.Fees) == 0 {
		t.Fatal("no fee lines")
	}
	for _, f := range p.Quote.Fees {
		if f.Provider != FeeProviderShown && f.Provider != "" {
			t.Fatalf("fee line %q tagged %q, want %q", f.Label, f.Provider, FeeProviderShown)
		}
	}
}

func TestPoolQuote_RefusesVAChannel(t *testing.T) {
	s, _ := newRoutedBuyService(t) // VA mode
	if _, err := s.QuotePoolBuy(context.Background(), "USDT", "100000", "NGN", poolMid, specPoolSettings()); !errors.Is(err, ErrPoolCentralOnly) {
		t.Fatalf("VA quote = %v, want ErrPoolCentralOnly", err)
	}
}

// §10.4 range: net below the minimum slice, above the order maximum. (The
// per-user daily cap is checked at open: TestPoolOpen_DailyCapRefusesForFailover.)
func TestPoolQuote_MinMax(t *testing.T) {
	s := centralPoolService(t)
	if _, err := s.QuotePoolBuy(context.Background(), "USDT", "2800", "NGN", poolMid, specPoolSettings()); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("₦2,800 (net 1.98 after costs < $2) = %v", err)
	}
	if _, err := s.QuotePoolBuy(context.Background(), "USDT", "2830", "NGN", poolMid, specPoolSettings()); err != nil {
		t.Fatalf("₦2,830 (net 2.002) = %v", err)
	}
	if _, err := s.QuotePoolBuy(context.Background(), "USDT", "3000000", "NGN", poolMid, specPoolSettings()); !errors.Is(err, ErrStableBuyAboveMaximum) {
		t.Fatalf("₦3,000,000 (net ≈ 2,172) = %v", err)
	}
}

// S46: the incident stop closes quotes; live pricing of money that arrived
// (enforceMax=false) still works — settlement keeps running.
func TestPoolQuote_OrdersStoppedClosesQuotesNotLivePricing(t *testing.T) {
	s := centralPoolService(t)
	set := specPoolSettings()
	set.OrdersStopped, set.OrderMaxUnits = true, new(big.Int)
	if _, err := s.QuotePoolBuy(context.Background(), "USDT", "100000", "NGN", poolMid, set); !errors.Is(err, ErrPoolOrdersStopped) {
		t.Fatalf("stopped quote = %v", err)
	}
	if p, err := s.poolQuoteAt("USDT", "NGN", big.NewInt(10_000_000), poolMid, set, PayInCentral, false); err != nil || p.Quote.AssetUnits.Int64() != 72_379_593 {
		t.Fatalf("live price while stopped = %v %v", p, err)
	}
}

// Owner 2026-10-13: she sees a pool buy's costs as one "Fees" line — the sum
// of the stored funding and network lines, never a provider's name —
// while the stored lines keep the detail. Other routes keep their lines.
func TestUserFeeLines_PoolCostsShowAsOneLine(t *testing.T) {
	p := quotePool(t, centralPoolService(t), "100000")
	got := PoolUserFeeLines(p.Quote.Fees, p.Quote.DisplayRateMinor)
	// Units summed; naira = that sum at the display rate, one floor
	// (1.068270 × ₦1,361.51 = ₦1,454.46), not the sum of floored lines.
	if len(got) != 1 || got[0].Kind != FeeKindFees || got[0].Label != "Fees" || got[0].Provider != FeeProviderShown ||
		got[0].Units != "1068270" || got[0].NgnMinor != "145446" {
		t.Fatalf("shown = %+v, want one Fees line 1.068270 (₦1,454.46)", got)
	}
	if len(p.Quote.Fees) != 2 {
		t.Fatalf("stored detail changed: %+v", p.Quote.Fees)
	}
	// A converted pool buy keeps the conversion line after the merged one.
	conv := append(append([]StableBuyFee{}, p.Quote.Fees...), StableBuyFee{Kind: FeeKindConversion, Label: ConversionFeeLabel, Provider: FeeProviderShown, Units: "5", NgnMinor: "1"})
	if got := PoolUserFeeLines(conv, p.Quote.DisplayRateMinor); len(got) != 2 || got[0].Kind != FeeKindFees || got[1].Kind != FeeKindConversion {
		t.Fatalf("converted shown = %+v", got)
	}
	// Flutterwave and the company wallet are shown line by line, unchanged.
	for _, prov := range []string{models.ITProviderFlutterwave, models.ITProviderTreasury} {
		i := &models.ITIntent{Provider: prov, Fees: marshalFees(p.Quote.Fees)}
		if got := UserFeeLines(i); len(got) != 2 || got[1].Kind != FeeKindNetwork {
			t.Fatalf("%s shown = %+v, want its own lines", prov, got)
		}
	}
	// ShowUserFees rewrites only a pool intent's response copy.
	pool := models.ITIntent{Provider: models.ITProviderPool, Fees: marshalFees(p.Quote.Fees), DisplayRate: p.Quote.DisplayRateMinor.String()}
	ShowUserFees(&pool)
	if f := StableBuyFees(&pool); len(f) != 1 || f[0].Units != "1068270" || f[0].NgnMinor != "145446" {
		t.Fatalf("pool intent shown = %+v", f)
	}
	other := models.ITIntent{Provider: models.ITProviderFlutterwave, Fees: marshalFees(p.Quote.Fees)}
	ShowUserFees(&other)
	if f := StableBuyFees(&other); len(f) != 2 {
		t.Fatalf("flutterwave intent shown = %+v", f)
	}
	for _, f := range got {
		for _, vendor := range []string{"flutterwave", "paycrest", "lifi"} {
			if strings.Contains(strings.ToLower(f.Label+f.Provider), vendor) {
				t.Fatalf("Fees line names a provider: %+v", f)
			}
		}
	}
}
