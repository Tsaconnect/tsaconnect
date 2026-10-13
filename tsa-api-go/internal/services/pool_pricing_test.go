package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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

// Roadmap risk 10 / spec §10.2 at 50 bps (decision 19), with the per-order
// costs (owner 2026-10-13): one slice (no desk) → gas 0.03 + 0.01 and one
// ₦53.75 payout fee (the slice pays the LP over ₦50,000).
func TestPoolPricing_WorkedExample100k(t *testing.T) {
	p := quotePool(t, centralPoolService(t), "100000")
	q := p.Quote
	if p.MidMinor.Int64() != 134800 || p.LPRateMinor.Int64() != 135474 || q.DisplayRateMinor.Int64() != 136151 {
		t.Fatalf("M=%s L=%s D=%s, want 134800/135474/136151", p.MidMinor, p.LPRateMinor, q.DisplayRateMinor)
	}
	if q.AssetUnits.Int64() != 72_340_115 || q.RatePerUnitMinor.Int64() != 138236 {
		t.Fatalf("net=%s effective=%s, want 72.340115 at ₦1,382.36", q.AssetUnits, q.RatePerUnitMinor)
	}
	// Fee units round down (hers, priceInHouseBuy's rule): ₦53.75 is
	// 0.039478 at ₦1,361.51, shown as ₦53.74.
	wantFees(t, q.Fees,
		[3]string{FeeKindFunding, "1028270", "139999"},
		[3]string{FeeKindNetwork, "40000", "5446"},
		[3]string{FeeKindPayout, "39478", "5374"})
	if len(p.SliceUnits) != 1 || p.SliceUnits[0].Int64() != 72_419_593 {
		t.Fatalf("slices = %v, want the one after-funding slice it was priced on", p.SliceUnits)
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
	if q.AssetUnits.Int64() != 3_573_085 || q.RatePerUnitMinor.Int64() != 139936 {
		t.Fatalf("net=%s effective=%s, want 3.573085 at ₦1,399.36", q.AssetUnits, q.RatePerUnitMinor)
	}
	wantFees(t, q.Fees,
		[3]string{FeeKindFunding, "51413", "6999"},
		[3]string{FeeKindNetwork, "40000", "5446"},
		[3]string{FeeKindPayout, "7895", "1074"})
	// Keep = fiat − funding − LP − actual payout fee − the gas line (a cost).
	lp := PoolLPPayoutKobo(q.AssetUnits, p.LPRateMinor)
	if lp.Int64() != 484_060 || 500_000-7_000-lp.Int64()-1_000-5_446 != 2_494 {
		t.Fatalf("LP naira = %s (want ₦4,840.60), keep must be ₦24.94", lp)
	}
}

// The re-price path (settle, re-delivery) passes the order's actual slices:
// two slices cost two slices' gas and two tiered payout fees (₦53.75 for the
// ₦81,284.40 slice, ₦26.88 for the ₦16,825.31 one) — the open rig's numbers.
func TestPoolPricing_RepriceOnActualSlices(t *testing.T) {
	s := centralPoolService(t)
	slices := []*big.Int{big.NewInt(60_000_000), big.NewInt(12_419_593)}
	p, err := s.poolQuoteAt("USDT", "NGN", big.NewInt(10_000_000), poolMid, specPoolSettings(), PayInCentral, false, slices)
	if err != nil {
		t.Fatal(err)
	}
	if p.Quote.AssetUnits.Int64() != 72_290_372 {
		t.Fatalf("net = %s, want 72.290372", p.Quote.AssetUnits)
	}
	wantFees(t, p.Quote.Fees,
		[3]string{FeeKindFunding, "1028270", "139999"},
		[3]string{FeeKindNetwork, "70000", "9530"},
		[3]string{FeeKindPayout, "59221", "8062"})
}

// Flutterwave's NGN transfer tiers, on the slice's naira payout.
func TestPoolPayoutFeeKobo_Tiers(t *testing.T) {
	for _, tc := range []struct{ payout, fee int64 }{
		{10_000, 1_075}, {500_000, 1_075}, {500_001, 2_688},
		{5_000_000, 2_688}, {5_000_001, 5_375}, {500_000_000, 5_375},
	} {
		if got := poolPayoutFeeKobo(big.NewInt(tc.payout)); got.Int64() != tc.fee {
			t.Errorf("payout %d kobo: fee %s, want %d", tc.payout, got, tc.fee)
		}
	}
}

// Network fee = slices × pool_gas_per_slice + pool_gas_delivery; unset reads 0.
func TestPoolNetworkFeeUnits(t *testing.T) {
	set := specPoolSettings()
	for slices, want := range map[int]int64{0: 10_000, 1: 40_000, 3: 100_000} {
		if got := poolNetworkFeeUnits(slices, set); got.Int64() != want {
			t.Errorf("%d slices: %s, want %d", slices, got, want)
		}
	}
	if got := poolNetworkFeeUnits(3, PoolSettings{}); got.Sign() != 0 {
		t.Fatalf("unset gas = %s, want 0", got)
	}
	// Payout fee units round down (hers).
	if got := poolPayoutFeeUnits([]*big.Int{big.NewInt(2_000_000)}, big.NewInt(135474), big.NewInt(136151)); got.Int64() != 7_895 {
		t.Fatalf("payout units = %s, want floor(₦10.75 / ₦1,361.51) = 0.007895", got)
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
		[3]string{FeeKindNetwork, "40000", "6464"},
		[3]string{FeeKindPayout, "6652", "1074"})
	if q.AssetUnits.Int64() != 258_415 || q.AssetUnits.Cmp(set.MinSliceUnits) < 0 {
		t.Fatalf("net = %s, want 0.258415 (≥ 0.25)", q.AssetUnits)
	}
	// What she pays covers the LP's naira, the funding fee and the payout fee,
	// with the take and the gas left over.
	lp := PoolLPPayoutKobo(q.AssetUnits, p.LPRateMinor).Int64()
	if keep := 50_000 - 700 - lp - 1_075; keep <= 0 {
		t.Fatalf("keep = %d kobo, want > 0 (LP %d)", keep, lp)
	}
	if _, err := s.QuotePoolBuy(context.Background(), "USDT", "480", "NGN", mid, set); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("₦480 = %v, want below the minimum", err)
	}
}

// The effective minimum slice is the larger of pool_min_slice and the units
// whose LP payout reaches the ₦100 transfer minimum, and every planner gets
// it through p.Settings — so no slice ever pays an LP under the floor.
func TestPoolEffectiveMinSlice_TransferFloor(t *testing.T) {
	if got := poolEffectiveMinSlice(big.NewInt(50_000), big.NewInt(135474)); got.Int64() != 73_815 {
		t.Fatalf("effective = %s, want ceil(₦100 / ₦1,354.74) = 0.073815", got)
	}
	if got := poolEffectiveMinSlice(big.NewInt(250_000), big.NewInt(160800)); got.Int64() != 250_000 {
		t.Fatalf("effective = %s, want pool_min_slice when it is the larger", got)
	}
	if PoolLPPayoutKobo(big.NewInt(73_815), big.NewInt(135474)).Int64() < fwNGNTransferMinKobo {
		t.Fatal("the effective minimum slice pays under the transfer minimum")
	}
	s := centralPoolService(t)
	set := specPoolSettings()
	set.MinSliceUnits = big.NewInt(50_000)
	p, err := s.poolQuoteAt("USDT", "NGN", big.NewInt(100_000_00), poolMid, set, PayInCentral, true, nil)
	if err != nil || p.Settings.MinSliceUnits.Int64() != 73_815 {
		t.Fatalf("price = %v %v, want the effective minimum in its settings", p, err)
	}
	if _, err := s.poolQuoteAt("USDT", "NGN", big.NewInt(10_000), poolMid, set, PayInCentral, true, nil); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("₦100 (net 0.06242 under the effective minimum) = %v, want below the minimum", err)
	}
	zero := new(big.Rat)
	plan, err := allocatePool([]poolCandidate{{free: big.NewInt(10_000_000), deficit: zero}, {free: big.NewInt(10_000_000), deficit: zero}},
		big.NewInt(10_050_000), 3, p.Settings.MinSliceUnits)
	if err != nil {
		t.Fatal(err)
	}
	for _, pl := range plan {
		if PoolLPPayoutKobo(pl.Units, p.LPRateMinor).Int64() < fwNGNTransferMinKobo {
			t.Fatalf("slice %s pays under ₦100", pl.Units)
		}
	}
}

// §14 invariant: fiat ÷ display rate − Σ fees = net, by construction.
func TestPoolPricing_FeeLineInvariant(t *testing.T) {
	s := centralPoolService(t)
	for _, naira := range []string{"2900", "5000", "73211.37", "100000", "2500000"} {
		p := quotePool(t, s, naira)
		left := lockedUnits(p.Quote.FiatMinor, p.Quote.DisplayRateMinor)
		if len(p.Quote.Fees) != 3 {
			t.Fatalf("₦%s: fees = %+v, want funding, network and payout", naira, p.Quote.Fees)
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
	if p, err := s.poolQuoteAt("USDT", "NGN", big.NewInt(10_000_000), poolMid, set, PayInCentral, false, []*big.Int{big.NewInt(72_419_593)}); err != nil || p.Quote.AssetUnits.Int64() != 72_340_115 {
		t.Fatalf("live price while stopped = %v %v", p, err)
	}
}

// Owner 2026-10-13: she sees a pool buy's costs as one "Fees" line — the sum
// of the stored funding, network and payout lines, never a provider's name —
// while the stored lines keep the detail. Other routes keep their lines.
func TestUserFeeLines_PoolCostsShowAsOneLine(t *testing.T) {
	p := quotePool(t, centralPoolService(t), "100000")
	got := PoolUserFeeLines(p.Quote.Fees, p.Quote.DisplayRateMinor)
	// Units summed; naira = that sum at the display rate, one floor
	// (1.107748 × ₦1,361.51 = ₦1,508.2097), not the sum of floored lines.
	if len(got) != 1 || got[0].Kind != FeeKindFees || got[0].Label != "Fees" || got[0].Provider != FeeProviderShown ||
		got[0].Units != "1107748" || got[0].NgnMinor != "150820" {
		t.Fatalf("shown = %+v, want one Fees line 1.107748 (₦1,508.20)", got)
	}
	if len(p.Quote.Fees) != 3 {
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
		if got := UserFeeLines(i); len(got) != 3 || got[1].Kind != FeeKindNetwork {
			t.Fatalf("%s shown = %+v, want its own lines", prov, got)
		}
	}
	// ShowUserFees rewrites only a pool intent's response copy.
	pool := models.ITIntent{Provider: models.ITProviderPool, Fees: marshalFees(p.Quote.Fees), DisplayRate: p.Quote.DisplayRateMinor.String()}
	ShowUserFees(&pool)
	if f := StableBuyFees(&pool); len(f) != 1 || f[0].Units != "1107748" || f[0].NgnMinor != "150820" {
		t.Fatalf("pool intent shown = %+v", f)
	}
	other := models.ITIntent{Provider: models.ITProviderFlutterwave, Fees: marshalFees(p.Quote.Fees)}
	ShowUserFees(&other)
	if f := StableBuyFees(&other); len(f) != 3 {
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

// fitPlanPrice (review money-1): the allocation decides the stored price.
func TestFitPlanPrice_ActualAllocationDecides(t *testing.T) {
	s := centralPoolService(t)
	p := quotePool(t, s, "100000") // estimate: one slice → 72.340115
	plan := func(units ...int64) []PoolSlicePlan {
		out := make([]PoolSlicePlan, len(units))
		for i, u := range units {
			out[i] = PoolSlicePlan{Units: big.NewInt(u)}
		}
		return out
	}
	open := planFit{allowWorse: true, allowBetter: true}

	// Same slices as the estimate: the quote stands.
	fp, pl, _, err := fitPlanPrice(p, plan(72_340_115), open)
	if err != nil || fp.Quote.AssetUnits.Int64() != 72_340_115 || len(pl) != 1 {
		t.Fatalf("same plan = %v %v", fp, err)
	}

	// Two slices where one was quoted: worse. She gets the two-slice price and
	// the largest slice gives up the difference, so the slices sum to it.
	fp, pl, _, err = fitPlanPrice(p, plan(60_000_000, 12_340_115), open)
	if err != nil || fp.Quote.AssetUnits.Int64() != 72_290_372 {
		t.Fatalf("worse = %v %v, want 72.290372", fp, err)
	}
	if sumUnits(planUnits(pl)).Cmp(fp.Quote.AssetUnits) != 0 || pl[0].Units.Int64() != 59_950_257 {
		t.Fatalf("slices %v do not sum to %s", planUnits(pl), fp.Quote.AssetUnits)
	}
	wantFees(t, fp.Quote.Fees,
		[3]string{FeeKindFunding, "1028270", "139999"},
		[3]string{FeeKindNetwork, "70000", "9530"},
		[3]string{FeeKindPayout, "59221", "8062"})

	// Worse past her guard: price-moved, nothing to open.
	guarded := *p
	guarded.Guard = PoolPriceGuard{Limit: FormatMinor(p.Quote.RatePerUnitMinor.String()), Mode: PriceGuardEnforce}
	if _, _, _, err := fitPlanPrice(&guarded, plan(60_000_000, 12_340_115), open); !errors.Is(err, ErrPriceMovedPastQuote) {
		t.Fatalf("past the guard = %v, want price moved", err)
	}
	// A converted open cannot move its amount: can't fill (failover).
	if _, _, _, err := fitPlanPrice(p, plan(60_000_000, 12_340_115), planFit{}); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("converted worse = %v, want can't fill", err)
	}

	// Two slices quoted, one allocated: better. The re-plan for the larger
	// amount is one slice too, so she gets the one-slice price on it.
	two, err := s.poolQuoteAt("USDT", "NGN", big.NewInt(10_000_000), poolMid, specPoolSettings(), PayInCentral, true,
		[]*big.Int{big.NewInt(60_000_000), big.NewInt(12_419_593)})
	if err != nil {
		t.Fatal(err)
	}
	var asked *big.Int
	better := planFit{allowWorse: true, allowBetter: true, replan: func(u *big.Int) ([]PoolSlicePlan, []models.P2PAd, error) {
		asked = u
		return plan(u.Int64()), nil, nil
	}}
	fp, pl, _, err = fitPlanPrice(two, plan(72_290_372), better)
	if err != nil || fp.Quote.AssetUnits.Int64() != 72_340_115 || asked == nil || asked.Int64() != 72_340_115 || pl[0].Units.Int64() != 72_340_115 {
		t.Fatalf("better = %v %v (re-planned %v), want 72.340115 on one slice", fp, err, asked)
	}
	// Better but not allowed (converted): the accepted price on its plan.
	fp, pl, _, err = fitPlanPrice(two, plan(72_290_372), planFit{})
	if err != nil || fp.Quote.AssetUnits.Int64() != 72_290_372 || pl[0].Units.Int64() != 72_290_372 {
		t.Fatalf("converted better = %v %v, want the accepted 72.290372", fp, err)
	}
}

// Review money-2: a live re-price at settlement is costed only on the slices
// that will be paid to an LP. Here coverage keeps A (60) and drops B, so the
// price carries one slice's gas and payout fee, not two.
func TestPoolSettlePrice_CostsOnlyTheKeptSlices(t *testing.T) {
	s := centralPoolService(t)
	a := PoolPendingSlice{ID: uuid.New(), Units: big.NewInt(60_000_000), CreatedAt: time.Now().Add(-time.Minute)}
	b := PoolPendingSlice{ID: uuid.New(), Units: big.NewInt(12_290_372), CreatedAt: time.Now()}
	p, cov, err := s.poolSettlePrice("USDT", "NGN", big.NewInt(8_227_000), poolMid, PayInCentral, nil, []PoolPendingSlice{a, b})
	if err != nil || !cov.Covered || len(cov.Keep) != 1 || cov.Keep[0] != a.ID || len(cov.Drop) != 1 || cov.Drop[0] != b.ID {
		t.Fatalf("cov = %+v %v, want A kept and B dropped", cov, err)
	}
	one, _ := s.poolQuoteAt("USDT", "NGN", big.NewInt(8_227_000), poolMid, CurrentPoolSettings(), PayInCentral, false, []*big.Int{a.Units})
	if p.Quote.AssetUnits.Cmp(one.Quote.AssetUnits) != 0 || p.Quote.Fees[1].Units != "40000" {
		t.Fatalf("priced %s with %+v, want %s on A alone (one slice's gas)", p.Quote.AssetUnits, p.Quote.Fees, one.Quote.AssetUnits)
	}
	// Released slices are always costed; a live price below them keeps none.
	p, cov, err = s.poolSettlePrice("USDT", "NGN", big.NewInt(8_227_000), poolMid, PayInCentral,
		[]*big.Int{big.NewInt(60_000_000)}, []PoolPendingSlice{b})
	if err != nil || !cov.Covered || len(cov.Keep) != 0 || p.Quote.Fees[1].Units != "40000" {
		t.Fatalf("released-only = %+v %+v %v", cov, p.Quote.Fees, err)
	}
}
