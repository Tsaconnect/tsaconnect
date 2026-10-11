package ramp

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fixedMid struct{ rate *big.Rat }

func (f fixedMid) BuyRate(context.Context, string) (*big.Rat, error) { return f.rate, nil }

// stubPoolIT is the ITService pool surface, scripted; the shared IT reads
// come from the company-wallet adapter's fake (read only).
type stubPoolIT struct {
	*fakeTreasuryIT
	open, canFill bool
	settings      services.PoolSettings
	quote         *services.PoolPrice
	quoteErr      error
	openFn        func(ctx context.Context) (*models.ITIntent, error)
	opened        int
	convErr       error // OpenPoolConvertedBuy's refusal; nil keeps it out of the test
}

func (s *stubPoolIT) PoolLegOpen(context.Context) bool       { return s.open }
func (s *stubPoolIT) PoolVenue() (string, string)            { return "bsc", "USDT" }
func (s *stubPoolIT) PoolBandBps() int64                     { return 50 }
func (s *stubPoolIT) PoolSettingsNow() services.PoolSettings { return s.settings }
func (s *stubPoolIT) PoolCanFill(context.Context, *big.Int, services.PoolSettings) bool {
	return s.canFill
}
func (s *stubPoolIT) QuotePoolBuy(string, string, string, *big.Rat, services.PoolSettings) (*services.PoolPrice, error) {
	return s.quote, s.quoteErr
}
func (s *stubPoolIT) CheckPoolDailyCap(context.Context, uuid.UUID, *big.Int, services.PoolSettings) error {
	return nil
}
func (s *stubPoolIT) OpenPoolBuy(ctx context.Context, _ uuid.UUID, _ *services.PoolPrice, _ string) (*models.ITIntent, *services.StableBuyInstructions, error) {
	s.opened++
	i, err := s.openFn(ctx)
	return i, &services.StableBuyInstructions{AccountNumber: "1234567890"}, err
}
func (s *stubPoolIT) OpenPoolConvertedBuy(context.Context, uuid.UUID, *services.PoolPrice, services.ConvertedOpen) (*models.ITIntent, *services.StableBuyInstructions, *models.PaycrestOrderRecord, error) {
	if s.convErr != nil {
		return nil, nil, nil, s.convErr
	}
	return nil, nil, nil, errors.New("not in this test")
}

func poolQuote() *services.PoolPrice {
	return &services.PoolPrice{Quote: &services.StableBuyQuote{Asset: "USDT", Currency: "NGN", FiatMinor: big.NewInt(10_000_000),
		AssetUnits: big.NewInt(72_419_593), RatePerUnitMinor: big.NewInt(138085), DisplayRateMinor: big.NewInt(136151)},
		LPRateMinor: big.NewInt(135474), MidMinor: big.NewInt(134800)}
}

func livePool(t *testing.T) *stubPoolIT {
	return &stubPoolIT{fakeTreasuryIT: newFakeTreasuryIT(t), open: true, canFill: true, quote: poolQuote(),
		openFn: func(context.Context) (*models.ITIntent, error) {
			return &models.ITIntent{ID: uuid.New(), Provider: models.ITProviderPool}, nil
		}}
}

func TestPoolProvider_QuoteIsDirectUSDTOnThePoolChain(t *testing.T) {
	p := newPoolProvider(livePool(t), fixedMid{big.NewRat(1348, 1)}, time.Second)
	q, err := p.Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "100000"})
	if err != nil || len(q.Venues) != 1 {
		t.Fatalf("quote = %v %v", q, err)
	}
	v := q.Venues[0]
	name, k, _ := DecodeHandle(v.Handle)
	if name != ProviderPool || k.ChainKey != "bsc" || k.Token != "USDT" || v.Kind != models.RouteKindDirect || v.EffectiveRate != "1380.85" || v.Rate != "1361.51" {
		t.Fatalf("venue = %+v", v)
	}
	if p.BandBps() != 50 {
		t.Fatal("band")
	}
}

// Re-review N2: a converted pool venue never names LiFi (or anyone) to her.
// fakePricer and optVenue are the converted-venue fixtures in
// treasury_converted_test.go (same package, read only).
func TestPoolProvider_ConvertedVenueNamesNoProvider(t *testing.T) {
	p := newPoolProvider(livePool(t), fixedMid{big.NewRat(1348, 1)}, time.Second)
	WithConversion(p, &fakePricer{venue: optVenue()})
	venues, err := p.QuoteConverted(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "100000", UserID: uuid.New(),
		Want: &services.RequestedAsset{Chain: "optimism", Token: "USDT", ChainID: 10, Address: "0xOptUSDT"}})
	if err != nil || len(venues) != 1 || len(venues[0].Fees) == 0 {
		t.Fatalf("venues = %+v err = %v", venues, err)
	}
	for _, f := range venues[0].Fees {
		if f.Provider != services.FeeProviderShown {
			t.Fatalf("fee line %q tagged %q, want %q", f.Label, f.Provider, services.FeeProviderShown)
		}
	}
}

func TestPoolProvider_LegClosedWhenStoppedClosedOrShort(t *testing.T) {
	for name, it := range map[string]*stubPoolIT{
		"gates closed": func() *stubPoolIT { s := livePool(t); s.open = false; return s }(),
		"stopped":      func() *stubPoolIT { s := livePool(t); s.settings.OrdersStopped = true; return s }(),
		"VA channel":   func() *stubPoolIT { s := livePool(t); s.quoteErr = services.ErrPoolCentralOnly; return s }(),
	} {
		_, err := newPoolProvider(it, fixedMid{big.NewRat(1348, 1)}, time.Second).Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "100000"})
		if !errors.Is(err, ErrLegClosed) {
			t.Fatalf("%s: %v, want ErrLegClosed", name, err)
		}
	}
	short := livePool(t)
	short.canFill = false
	if _, err := newPoolProvider(short, fixedMid{big.NewRat(1348, 1)}, time.Second).Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "100000"}); !errors.Is(err, ErrAboveMaximum) {
		t.Fatalf("short stock = %v, want above-maximum without naming the stock", err)
	}
}

// S12: the pool attempt is capped; a cap hit is "nothing opened" (failover).
func TestPoolProvider_OpenCappedAtItsBudget(t *testing.T) {
	it := livePool(t)
	it.openFn = func(ctx context.Context) (*models.ITIntent, error) {
		<-ctx.Done()
		return nil, services.ErrPoolCannotFill
	}
	p := newPoolProvider(it, fixedMid{big.NewRat(1348, 1)}, 80*time.Millisecond)
	start := time.Now()
	_, err := p.Open(context.Background(), VenueKey{"bsc", "USDT"}, buyReq("USDT", "bsc"), OrderInput{UserID: uuid.New(), WalletAddress: "0xabc", LimitRate: "1400"})
	if !errors.Is(err, ErrVenueUnavailable) || !errors.Is(err, services.ErrPoolCannotFill) || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("open = %v after %s", err, time.Since(start))
	}
}

// Roadmap risk 3: the pool fails at its cap and Paycrest still opens inside the
// router's budget (8 s of 20 s, scaled to 120 ms of 300 ms).
func TestRouter_PoolFailsAt8sPaycrestOpensWithinBudget(t *testing.T) {
	it := livePool(t)
	it.openFn = func(ctx context.Context) (*models.ITIntent, error) {
		<-ctx.Done()
		return nil, services.ErrPoolCannotFill
	}
	pool := newPoolProvider(it, fixedMid{big.NewRat(1348, 1)}, 120*time.Millisecond)
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "bsc", "USDT", "1380.00"))
	pc.openResults = []openResult{{order: okOrder}}
	r := NewRouter(Config{OpenBudget: 300 * time.Millisecond, AttemptTimeout: 250 * time.Millisecond}, pool, pc)
	req := openReq("", "1400.00")
	req.Request.Token, req.Request.ChainKey = "USDT", "bsc"
	start := time.Now()
	o, err := r.Open(context.Background(), req)
	if err != nil || o == nil || it.opened != 1 || len(pc.opened) != 1 || time.Since(start) > 300*time.Millisecond {
		t.Fatalf("open = %v %v pool=%d paycrest=%d after %s", o, err, it.opened, len(pc.opened), time.Since(start))
	}
}

func convertingPool(it *stubPoolIT) *PoolProvider {
	p := newPoolProvider(it, fixedMid{big.NewRat(1348, 1)}, time.Second)
	WithConversion(p, &fakePricer{venue: optVenue()})
	return p
}

// A pool refusal on a converted open left nothing open, so it fails over to
// the next venue for her target — as the direct open does (classifyPoolOpen),
// never as an outcome-unknown stop.
func TestPoolProvider_ConvertedOpenRefusalFailsOver(t *testing.T) {
	for _, cause := range []error{services.ErrPoolCannotFill, services.ErrPoolClosed, services.ErrTreasuryNonceStuck,
		services.ErrPoolOrdersStopped, services.ErrPoolCentralOnly, services.ErrPoolDailyCapReached} {
		it := livePool(t)
		it.convErr = fmt.Errorf("%w (unproven_or_short)", cause)
		_, err := convertingPool(it).OpenConverted(context.Background(), convertedTreasuryReq("100000"),
			OrderInput{UserID: uuid.New(), WalletAddress: "0xabc", LimitRate: "2000"})
		if !errors.Is(err, ErrVenueUnavailable) || errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, cause) {
			t.Fatalf("%v: open = %v, want venue-unavailable carrying the cause", cause, err)
		}
	}
}

// The pool's leg closing between her quote and her open (stopped, gates shut,
// stock gone) is "nothing opened here": the router must fail over, not stop
// on a quote-time class.
func TestPoolProvider_ConvertedOpenLegGoneFailsOver(t *testing.T) {
	for name, it := range map[string]*stubPoolIT{
		"stopped":     func() *stubPoolIT { s := livePool(t); s.settings.OrdersStopped = true; return s }(),
		"gates":       func() *stubPoolIT { s := livePool(t); s.open = false; return s }(),
		"short stock": func() *stubPoolIT { s := livePool(t); s.canFill = false; return s }(),
	} {
		_, err := convertingPool(it).OpenConverted(context.Background(), convertedTreasuryReq("100000"),
			OrderInput{UserID: uuid.New(), WalletAddress: "0xabc", LimitRate: "2000"})
		if !errors.Is(err, ErrVenueUnavailable) {
			t.Fatalf("%s: open = %v, want venue-unavailable", name, err)
		}
	}
}
