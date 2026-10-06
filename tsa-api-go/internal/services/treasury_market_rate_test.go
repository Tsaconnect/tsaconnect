package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// midRateQuoter answers both directions of Flutterwave's rate probe: a buy
// (fiat → asset, "pay buy NGN for 1 token") and a sell (asset → fiat).
type midRateQuoter struct {
	buy, sell float64
	err       error
	calls     atomic.Int32
}

func (q *midRateQuoter) QuoteRate(_ context.Context, src, dst string, destAmount float64) (FWRate, error) {
	q.calls.Add(1)
	if q.err != nil {
		return FWRate{}, q.err
	}
	if IsStableAsset(dst) {
		return FWRate{Source: FWMoney{Currency: src, Amount: FWQuoteAmount(q.buy * destAmount)},
			Destination: FWMoney{Currency: dst, Amount: FWQuoteAmount(destAmount)}}, nil
	}
	return FWRate{Source: FWMoney{Currency: src, Amount: FWQuoteAmount(destAmount / q.sell)},
		Destination: FWMoney{Currency: dst, Amount: FWQuoteAmount(destAmount)}}, nil
}

type fakePaycrestMarket struct {
	rate  float64
	err   error
	calls atomic.Int32
}

func (f *fakePaycrestMarket) MarketRate(context.Context, string, string) (float64, error) {
	f.calls.Add(1)
	return f.rate, f.err
}

// rateService: the FX feed says ₦1350 per USD (fx.stale = no opinion).
func rateService(t *testing.T, q *midRateQuoter, fx stubFx) *ITService {
	t.Helper()
	s := NewITService(nil, nil, fx)
	if q != nil {
		s.SetRateQuoter(q)
	}
	return s
}

var (
	fx1350     = stubFx{ngn: 1350}
	fxStale    = stubFx{ngn: 1350, stale: true}
	usualMid   = func() *midRateQuoter { return &midRateQuoter{buy: 1360, sell: 1336} } // mid 1348
	midDown    = func() *midRateQuoter { return &midRateQuoter{err: errors.New("flutterwave down")} }
	paycrestOK = func() *fakePaycrestMarket { return &fakePaycrestMarket{rate: 1349.5} }
)

func wantRate(t *testing.T, got *big.Rat, err error, want *big.Rat) {
	t.Helper()
	if err != nil || got == nil || got.Cmp(want) != 0 {
		t.Fatalf("rate = %v err = %v, want %s", got, err, want.FloatString(4))
	}
}

func TestStableMidFiatPerUnit_AveragesBuyAndSell(t *testing.T) {
	mid, err := rateService(t, usualMid(), fx1350).StableMidFiatPerUnit(context.Background(), "ngn", "usdc")
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := mid.Float64(); f < 1347.99 || f > 1348.01 {
		t.Fatalf("mid = %s, want 1348", mid.FloatString(4))
	}
	if _, err := rateService(t, &midRateQuoter{buy: 5000, sell: 4990}, fx1350).StableMidFiatPerUnit(context.Background(), "NGN", "USDC"); !errors.Is(err, ErrStableRateImplausible) {
		t.Fatalf("implausible mid: %v", err)
	}
	if _, err := NewITService(nil, nil, nil).StableMidFiatPerUnit(context.Background(), "NGN", "USDC"); !errors.Is(err, ErrStableQuoteUnavailable) {
		t.Fatalf("no quoter: %v", err)
	}
}

func TestStableFxReference_MissingOrStaleIsNoReference(t *testing.T) {
	ctx := context.Background()
	if r, ok := rateService(t, nil, fx1350).StableFxReference(ctx, "ngn"); !ok || r.Cmp(big.NewRat(1350, 1)) != 0 {
		t.Fatalf("fresh feed = %v %v", r, ok)
	}
	if _, ok := rateService(t, nil, fxStale).StableFxReference(ctx, "NGN"); ok {
		t.Fatal("a stale feed is not a reference")
	}
	if _, ok := NewITService(nil, nil, nil).StableFxReference(ctx, "NGN"); ok {
		t.Fatal("no feed is not a reference")
	}
}

// Both sources up and within 400 bps: Paycrest's exact figure.
func TestTreasuryMarketRate_BothAgreeUsesPaycrest(t *testing.T) {
	r := newTreasuryMarketRate(paycrestOK(), rateService(t, usualMid(), fx1350), 400)
	got, err := r.BuyRate(context.Background(), "ngn")
	wantRate(t, got, err, big.NewRat(134950, 100))
}

// Review I2: two sources that disagree close the leg — they are never averaged
// or fallen back between.
func TestTreasuryMarketRate_DisagreementClosesTheLeg(t *testing.T) {
	logs := captureLog(t)
	// 1420 is 5.3% above the 1348 mid.
	r := newTreasuryMarketRate(&fakePaycrestMarket{rate: 1420}, rateService(t, usualMid(), fx1350), 400)
	_, err := r.BuyRate(context.Background(), "NGN")
	if !errors.Is(err, ErrTreasuryRateDisagree) || !errors.Is(err, ErrStableQuoteUnavailable) || errors.Is(err, ErrStableRateImplausible) {
		t.Fatalf("err = %v, want a transient disagreement", err)
	}
	if !strings.Contains(logs.String(), "ALERT ramp_treasury_rate_disagree fiat=NGN") {
		t.Fatalf("log = %q", logs.String())
	}
	// The old fallback case: Paycrest "implausible" while the mid is fine is
	// now a disagreement too.
	r = newTreasuryMarketRate(&fakePaycrestMarket{rate: 5000}, rateService(t, usualMid(), fx1350), 400)
	if _, err := r.BuyRate(context.Background(), "NGN"); !errors.Is(err, ErrTreasuryRateDisagree) {
		t.Fatalf("paycrest 5000 vs mid 1348: %v", err)
	}
}

// Review I2: one source alone must agree with the FX feed; no fresh feed, no rate.
func TestTreasuryMarketRate_OneSourceNeedsTheFXFeed(t *testing.T) {
	ctx := context.Background()
	t.Run("paycrest down, mid agrees with FX", func(t *testing.T) {
		logs := captureLog(t)
		r := newTreasuryMarketRate(&fakePaycrestMarket{err: errors.New("paycrest 503")}, rateService(t, usualMid(), fx1350), 400)
		got, err := r.BuyRate(ctx, "NGN")
		if err != nil {
			t.Fatal(err)
		}
		if f, _ := got.Float64(); f < 1347.99 || f > 1348.01 {
			t.Fatalf("rate = %s", got.FloatString(4))
		}
		if !strings.Contains(logs.String(), "ramp_treasury_market_fallback source=flutterwave_mid") {
			t.Fatalf("log = %q", logs.String())
		}
	})
	t.Run("mid down, paycrest agrees with FX", func(t *testing.T) {
		r := newTreasuryMarketRate(paycrestOK(), rateService(t, midDown(), fx1350), 400)
		got, err := r.BuyRate(ctx, "NGN")
		wantRate(t, got, err, big.NewRat(134950, 100))
	})
	t.Run("one source and a stale FX feed is closed", func(t *testing.T) {
		logs := captureLog(t)
		r := newTreasuryMarketRate(paycrestOK(), rateService(t, midDown(), fxStale), 400)
		if _, err := r.BuyRate(ctx, "NGN"); !errors.Is(err, ErrTreasuryRateUnverified) || !errors.Is(err, ErrStableQuoteUnavailable) {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(logs.String(), "ALERT ramp_treasury_rate_unverified fiat=NGN source=paycrest") {
			t.Fatalf("log = %q", logs.String())
		}
	})
	t.Run("one source off the FX feed is closed", func(t *testing.T) {
		// 1349.5 is 8% above an FX feed of 1250.
		r := newTreasuryMarketRate(paycrestOK(), rateService(t, midDown(), stubFx{ngn: 1250}), 400)
		if _, err := r.BuyRate(ctx, "NGN"); !errors.Is(err, ErrTreasuryRateDisagree) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("both down is closed", func(t *testing.T) {
		r := newTreasuryMarketRate(&fakePaycrestMarket{err: errors.New("down")}, rateService(t, midDown(), fx1350), 400)
		if _, err := r.BuyRate(ctx, "NGN"); !errors.Is(err, ErrStableQuoteUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestTreasuryMarketRate_ToleranceDefaultsTo400(t *testing.T) {
	if r := NewTreasuryMarketRate(nil, nil, 0); r.tolBps != DefaultTreasuryRateTolBps || DefaultTreasuryRateTolBps != 400 {
		t.Fatalf("tol = %d", r.tolBps)
	}
	// 1400 vs 1348 is 3.86%: inside 400 bps, outside 300.
	ctx := context.Background()
	if _, err := newTreasuryMarketRate(&fakePaycrestMarket{rate: 1400}, rateService(t, usualMid(), fx1350), 400).BuyRate(ctx, "NGN"); err != nil {
		t.Fatalf("400 bps: %v", err)
	}
	if _, err := newTreasuryMarketRate(&fakePaycrestMarket{rate: 1400}, rateService(t, usualMid(), fx1350), 300).BuyRate(ctx, "NGN"); !errors.Is(err, ErrTreasuryRateDisagree) {
		t.Fatalf("300 bps: %v", err)
	}
}

func TestTreasuryMarketRate_MidIsCachedAndServedStaleUpToFiveMinutes(t *testing.T) {
	q := usualMid()
	r := newTreasuryMarketRate(&fakePaycrestMarket{err: errors.New("down")}, rateService(t, q, fx1350), 400)
	start := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	clock := start
	r.now = func() time.Time { return clock }
	ctx := context.Background()
	if _, err := r.BuyRate(ctx, "NGN"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BuyRate(ctx, "NGN"); err != nil || q.calls.Load() != 2 {
		t.Fatalf("within 60s: calls=%d err=%v, want the cached mid (2 probes: one buy, one sell)", q.calls.Load(), err)
	}
	q.err = errors.New("flutterwave down")
	clock = start.Add(4 * time.Minute)
	if got, err := r.BuyRate(ctx, "NGN"); err != nil || got == nil {
		t.Fatalf("4 min stale: %v, want the last mid", err)
	}
	clock = start.Add(6 * time.Minute)
	if _, err := r.BuyRate(ctx, "NGN"); err == nil {
		t.Fatal("served a mid older than 5 minutes")
	}
}

// The public quote reaches /v1/currencies through a 60s cache that now also
// remembers a failure for 5s (spec §4.1 "the minor fix").
func TestPaycrestMarketRate_CachesAFailureBriefly(t *testing.T) {
	q := &stubQuoter{marketErr: errors.New("paycrest down")}
	svc := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := svc.MarketRate(ctx, "NGN", "buy"); err == nil {
			t.Fatal("no error during the outage")
		}
	}
	if q.marketCall != 1 {
		t.Fatalf("calls = %d, want 1 inside the 5s failure window", q.marketCall)
	}
	svc.expireMarketRate()
	q.marketErr, q.market = nil, 1383.45
	if v, err := svc.MarketRate(ctx, "NGN", "buy"); err != nil || v != 1383.45 || q.marketCall != 2 {
		t.Fatalf("after the window: %v %v calls=%d", v, err, q.marketCall)
	}
}

// Fix round 1: the company is the seller, so when both sources agree the
// higher figure is used.
func TestTreasuryMarketRate_BothAgreeUsesTheHigher(t *testing.T) {
	ctx := context.Background()
	// Mid is 1348; make both orderings via the quoter's buy/sell around 1380 and 1340.
	hiMid := &midRateQuoter{buy: 1390, sell: 1370} // mid 1380
	r := newTreasuryMarketRate(&fakePaycrestMarket{rate: 1340}, rateService(t, hiMid, fx1350), 400)
	got, err := r.BuyRate(ctx, "NGN")
	// The mid is derived from float quotes, so it is 1380 only approximately.
	if f, _ := got.Float64(); err != nil || f < 1379.99 || f > 1380.01 {
		t.Fatalf("paycrest 1340 vs mid 1380: %v %v", got, err)
	}

	loMid := &midRateQuoter{buy: 1350, sell: 1330} // mid 1340
	r = newTreasuryMarketRate(&fakePaycrestMarket{rate: 1380}, rateService(t, loMid, fx1350), 400)
	got, err = r.BuyRate(ctx, "NGN")
	wantRate(t, got, err, big.NewRat(1380, 1))
}

func TestTreasuryMarketRate_MidAloneIsCheckedAgainstFX(t *testing.T) {
	ctx := context.Background()
	down := func() *fakePaycrestMarket { return &fakePaycrestMarket{err: errors.New("paycrest 503")} }
	logs := captureLog(t)
	r := newTreasuryMarketRate(down(), rateService(t, usualMid(), fxStale), 400)
	if _, err := r.BuyRate(ctx, "NGN"); !errors.Is(err, ErrTreasuryRateUnverified) {
		t.Fatalf("stale fx: %v", err)
	}
	if !strings.Contains(logs.String(), "ALERT ramp_treasury_rate_unverified fiat=NGN source=flutterwave_mid") {
		t.Fatalf("log = %q", logs.String())
	}
	r = newTreasuryMarketRate(down(), rateService(t, usualMid(), stubFx{ngn: 1250}), 400)
	if _, err := r.BuyRate(ctx, "NGN"); !errors.Is(err, ErrTreasuryRateDisagree) {
		t.Fatalf("mid off fx: %v", err)
	}
}

func TestTreasuryMarketRate_ToleranceBoundaryIsExact(t *testing.T) {
	ctx := context.Background()
	down := midDown()
	// Second source = the FX feed (1350): 1350 x 1.04 = 1404 in, x 1.0401 = 1404.135 out.
	for _, tc := range []struct {
		rate float64
		ok   bool
	}{{1404, true}, {1404.135, false}} {
		r := newTreasuryMarketRate(&fakePaycrestMarket{rate: tc.rate}, rateService(t, down, fx1350), 400)
		_, err := r.BuyRate(ctx, "NGN")
		if (err == nil) != tc.ok || (!tc.ok && !errors.Is(err, ErrTreasuryRateDisagree)) {
			t.Fatalf("rate %v: err = %v, want ok=%v", tc.rate, err, tc.ok)
		}
	}
}

func TestTreasuryMarketRate_BothDownAlerts(t *testing.T) {
	logs := captureLog(t)
	r := newTreasuryMarketRate(&fakePaycrestMarket{err: errors.New("pc boom")}, rateService(t, midDown(), fx1350), 400)
	if _, err := r.BuyRate(context.Background(), "NGN"); !errors.Is(err, ErrStableQuoteUnavailable) {
		t.Fatal(err)
	}
	if l := logs.String(); !strings.Contains(l, "ALERT ramp_treasury_rate_unavailable fiat=NGN") || !strings.Contains(l, "pc boom") || !strings.Contains(l, "flutterwave down") {
		t.Fatalf("log = %q", l)
	}
}

func TestTreasuryMarketRate_CachedMidCannotBeMutatedByCallers(t *testing.T) {
	ctx := context.Background()
	r := newTreasuryMarketRate(&fakePaycrestMarket{err: errors.New("down")}, rateService(t, usualMid(), fx1350), 400)
	first, err := r.BuyRate(ctx, "NGN")
	if err != nil {
		t.Fatal(err)
	}
	want := new(big.Rat).Set(first)
	first.Add(first, big.NewRat(1000, 1))
	again, err := r.BuyRate(ctx, "NGN")
	if err != nil || again.Cmp(want) != 0 {
		t.Fatalf("cache mutated: %v %v", again, err)
	}
}
