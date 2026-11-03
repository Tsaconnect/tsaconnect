package ramp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// fakeProvider is a scriptable Provider. openResults is consumed one per Open.
type fakeProvider struct {
	name        string
	quote       *ProviderQuote
	quoteErr    error
	quoteDelay  time.Duration
	legs        map[string]services.RampLegs
	legsErr     error
	openResults []openResult
	// bindRate, when set, is the rate Open binds at: like the Paycrest guard,
	// an enforced limit it misses is refused with that rate.
	bindRate   string
	opened     []VenueKey
	openInputs []OrderInput
	hasOpen    []bool // consumed per CheckNoOpenOrder call; last value repeats
	hasOpenErr error
	hasOpenN   int
	orders     map[string]*Order
	list       []Order
	listErr    error
}

type openResult struct {
	order *Order
	err   error
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) RampLegs(context.Context) (map[string]services.RampLegs, error) {
	return f.legs, f.legsErr
}
func (f *fakeProvider) Quote(ctx context.Context, _ Request) (*ProviderQuote, error) {
	if f.quoteDelay > 0 {
		select {
		case <-time.After(f.quoteDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.quote, f.quoteErr
}
func (f *fakeProvider) Open(_ context.Context, k VenueKey, req Request, in OrderInput) (*Order, error) {
	f.opened = append(f.opened, k)
	f.openInputs = append(f.openInputs, in)
	if f.bindRate != "" && in.Guard == services.PriceGuardEnforce && !Acceptable(req.Side, f.bindRate, in.LimitRate) {
		return nil, &services.PriceMovedPastQuoteError{BoundRate: f.bindRate}
	}
	if len(f.openResults) == 0 {
		return nil, ErrVenueUnavailable
	}
	r := f.openResults[0]
	f.openResults = f.openResults[1:]
	return r.order, r.err
}
func (f *fakeProvider) CheckNoOpenOrder(context.Context, uuid.UUID, Side) error {
	if f.hasOpenErr != nil {
		return f.hasOpenErr
	}
	if len(f.hasOpen) == 0 {
		return nil
	}
	i := f.hasOpenN
	if i >= len(f.hasOpen) {
		i = len(f.hasOpen) - 1
	}
	f.hasOpenN++
	if f.hasOpen[i] {
		return WrapClass(ErrRefusedForUser, services.ErrPaycrestOrderOpen)
	}
	return nil
}
func (f *fakeProvider) Get(_ context.Context, _ uuid.UUID, id string) (*Order, error) {
	if o, ok := f.orders[id]; ok {
		return o, nil
	}
	return nil, ErrOrderNotFound
}
func (f *fakeProvider) List(context.Context, uuid.UUID, int) ([]Order, error) {
	return f.list, f.listErr
}
func (f *fakeProvider) ClaimPayment(ctx context.Context, u uuid.UUID, id string) (*Order, error) {
	return f.Get(ctx, u, id)
}
func (f *fakeProvider) Cancel(ctx context.Context, u uuid.UUID, id string) (*Order, error) {
	return f.Get(ctx, u, id)
}

func venue(provider, chain, token, rate string) Venue {
	return Venue{Handle: EncodeHandle(provider, VenueKey{chain, token}), Provider: provider,
		ChainKey: chain, Token: token, Rate: rate, TokenAmount: 1}
}

func testRouter(ps ...Provider) *Router {
	return NewRouter(Config{ToleranceBps: 50, Guard: services.PriceGuardEnforce,
		ProviderTimeout: 200 * time.Millisecond, AttemptTimeout: time.Second, OpenBudget: 2 * time.Second}, ps...)
}

func TestRouterQuote_SortsByRatePerSide(t *testing.T) {
	a := &fakeProvider{name: "a", quote: &ProviderQuote{MarketRate: 1380, Venues: []Venue{venue("a", "base", "USDC", "1400")}}}
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1390")}}}
	r := testRouter(a, b)

	res, err := r.Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "1000", Token: "USDC", ChainKey: "base"})
	if err != nil || len(res.Venues) != 2 || res.Venues[0].Provider != "b" || res.Best.Provider != "b" {
		t.Fatalf("buy: %+v %v", res, err)
	}
	if res.MarketRate != 1380 || res.FiatCode != "NGN" || res.Side != "buy" {
		t.Fatalf("envelope: %+v", res)
	}
	res, err = r.Quote(context.Background(), Request{Side: Sell, FiatCode: "NGN", FiatAmount: "1000", Token: "USDC", ChainKey: "base"})
	if err != nil || res.Venues[0].Provider != "a" {
		t.Fatalf("sell: %+v %v", res, err)
	}
}

func TestRouterQuote_UnpinnedKeepsBestPerPair(t *testing.T) {
	a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{
		venue("a", "base", "USDC", "1400"), venue("a", "polygon", "USDT", "1395")}}}
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1390")}}}
	res, err := testRouter(a, b).Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "1000"})
	if err != nil || len(res.Venues) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if res.Venues[0].Provider != "b" || res.Venues[0].ChainKey != "base" || res.Venues[1].ChainKey != "polygon" {
		t.Fatalf("venues = %+v", res.Venues)
	}
}

func TestRouterQuote_DropsAFailingOrSlowProvider(t *testing.T) {
	ok := &fakeProvider{name: "ok", quote: &ProviderQuote{Venues: []Venue{venue("ok", "base", "USDC", "1400")}}}
	broken := &fakeProvider{name: "broken", quoteErr: errors.New("boom")}
	slow := &fakeProvider{name: "slow", quoteDelay: time.Second,
		quote: &ProviderQuote{Venues: []Venue{venue("slow", "base", "USDC", "1300")}}}
	res, err := testRouter(broken, slow, ok).Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "1000"})
	if err != nil || len(res.Venues) != 1 || res.Venues[0].Provider != "ok" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRouterQuote_ErrorPrecedence(t *testing.T) {
	q := func(errs ...error) error {
		var ps []Provider
		for i, e := range errs {
			ps = append(ps, &fakeProvider{name: string(rune('a' + i)), quoteErr: e})
		}
		_, err := testRouter(ps...).Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "1000"})
		return err
	}
	if err := q(ErrLegClosed, ErrLegClosed); !errors.Is(err, ErrLegClosed) {
		t.Errorf("all closed → %v", err)
	}
	if err := q(ErrBelowMinimum); !errors.Is(err, ErrBelowMinimum) {
		t.Errorf("all below minimum → %v", err)
	}
	if err := q(ErrLegClosed, ErrNoVenue); !errors.Is(err, ErrNoVenue) {
		t.Errorf("any no-venue → %v", err)
	}
	if err := q(ErrLegClosed, errors.New("timeout")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("mixed with an outage → %v", err)
	}
	if err := q(ErrNoVenue, errors.New("timeout")); !errors.Is(err, ErrNoVenue) {
		t.Errorf("no-venue beside an outage → %v (spec: any no-venue wins)", err)
	}
	// 2026-10-13: a below-minimum answer beats another provider's outage.
	if err := q(WrapClass(ErrBelowMinimum, services.ErrStableBuyBelowMinimum), errors.New("timeout")); !errors.Is(err, ErrBelowMinimum) || errors.Is(err, ErrUnavailable) {
		t.Errorf("below-minimum beside an outage → %v, want below minimum", err)
	}
	if err := q(errors.New("timeout"), ErrLegClosed, ErrBelowMinimum, ErrAboveMaximum); !errors.Is(err, ErrBelowMinimum) {
		t.Errorf("below-minimum among outage/closed/above → %v", err)
	}
	if err := q(ErrAboveMaximum, errors.New("timeout")); !errors.Is(err, ErrAboveMaximum) || errors.Is(err, ErrUnavailable) {
		t.Errorf("above-maximum beside an outage → %v, want above maximum (same rule as below)", err)
	}
	if err := q(errors.New("timeout"), ErrAboveMaximum, ErrBelowMinimum); !errors.Is(err, ErrBelowMinimum) {
		t.Errorf("both beside an outage → %v, want below minimum first", err)
	}
	// Decision D13 (spec rev 3 §4.4): a closed leg no longer hides the amount.
	if err := q(ErrLegClosed, ErrBelowMinimum); !errors.Is(err, ErrBelowMinimum) {
		t.Errorf("closed + below-minimum, no no-venue → %v", err)
	}
}

func TestRouterRampLegs_ORAcrossAnsweringProviders(t *testing.T) {
	a := &fakeProvider{name: "a", legs: map[string]services.RampLegs{"NGN": {Sell: true}}}
	b := &fakeProvider{name: "b", legs: map[string]services.RampLegs{"NGN": {Buy: true}, "KES": {Buy: true}}}
	down := &fakeProvider{name: "down", legsErr: errors.New("book never loaded")}
	legs, err := testRouter(a, down, b).RampLegs(context.Background())
	if err != nil || legs["NGN"] != (services.RampLegs{Buy: true, Sell: true}) || !legs["KES"].Buy {
		t.Fatalf("%+v %v", legs, err)
	}
	if _, err := testRouter(down).RampLegs(context.Background()); err == nil {
		t.Fatal("every provider failing must be an error, so the handler's own fallback applies")
	}
}

// Providers are asked at once and the caller's deadline is honoured: a slow
// provider (the pool's on-chain reads, 2026-11-03) neither starves the fast
// one nor holds the caller; it counts as not having answered.
func TestRouterRampLegs_SlowProviderDoesNotStarveOthers(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	slow := &blockingLegs{fakeProvider: fakeProvider{name: "pool", legs: map[string]services.RampLegs{"NGN": {Buy: true}}}, release: release}
	fast := &fakeProvider{name: "paycrest", legs: map[string]services.RampLegs{"NGN": {Sell: true}}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	legs, err := testRouter(slow, fast).RampLegs(ctx)
	if err != nil || legs["NGN"] != (services.RampLegs{Sell: true}) {
		t.Fatalf("%+v %v", legs, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("held the caller %s past its deadline", took)
	}
}

func TestRouterRampLegs_PanickingProviderIsAFailure(t *testing.T) {
	boom := &panicLegs{fakeProvider: fakeProvider{name: "boom"}}
	fine := &fakeProvider{name: "paycrest", legs: map[string]services.RampLegs{"NGN": {Sell: true}}}
	legs, err := testRouter(boom, fine).RampLegs(context.Background())
	if err != nil || !legs["NGN"].Sell {
		t.Fatalf("%+v %v", legs, err)
	}
}

// A provider that cannot answer counts as open only on the sides it could
// ever offer; one that answered is believed. Paycrest failing must not read
// NGN sell as closed (2026-11-03), a slow pool must not lose NGN buy, and an
// out-of-stock pool that answered keeps NGN buy closed.
func TestRouterRampLegs_FailedProviderCountsItsPossibleSides(t *testing.T) {
	poolAnswered := &fakeProvider{name: "pool", legs: map[string]services.RampLegs{"NGN": {Buy: false}}}
	paycrestDown := &cappedLegs{
		fakeProvider: fakeProvider{name: "paycrest", legsErr: errors.New("book unavailable")},
		max:          map[string]services.RampLegs{"NGN": {Sell: true}, "UGX": {Sell: true}},
	}
	legs, err := testRouter(poolAnswered, paycrestDown).RampLegs(context.Background())
	if err != nil || legs["NGN"] != (services.RampLegs{Sell: true}) || legs["UGX"] != (services.RampLegs{Sell: true}) {
		t.Fatalf("%+v %v", legs, err)
	}

	release := make(chan struct{})
	defer close(release)
	slowPool := &cappedBlocking{
		blockingLegs: blockingLegs{fakeProvider: fakeProvider{name: "pool"}, release: release},
		max:          map[string]services.RampLegs{"NGN": {Buy: true}},
	}
	paycrest := &fakeProvider{name: "paycrest", legs: map[string]services.RampLegs{"NGN": {Sell: true}}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	legs, err = testRouter(slowPool, paycrest).RampLegs(ctx)
	if err != nil || legs["NGN"] != (services.RampLegs{Buy: true, Sell: true}) {
		t.Fatalf("slow pool: %+v %v", legs, err)
	}
}

type cappedLegs struct {
	fakeProvider
	max map[string]services.RampLegs
}

func (c *cappedLegs) MaxLegs() map[string]services.RampLegs { return c.max }

type cappedBlocking struct {
	blockingLegs
	max map[string]services.RampLegs
}

func (c *cappedBlocking) MaxLegs() map[string]services.RampLegs { return c.max }

// blockingLegs ignores its context and answers only when released.
type blockingLegs struct {
	fakeProvider
	release chan struct{}
}

func (b *blockingLegs) RampLegs(context.Context) (map[string]services.RampLegs, error) {
	<-b.release
	return b.legs, nil
}

type panicLegs struct{ fakeProvider }

func (p *panicLegs) RampLegs(context.Context) (map[string]services.RampLegs, error) {
	panic("nil market")
}

func TestRouterReads_ResolveIDs(t *testing.T) {
	id := uuid.NewString()
	rec := &models.PaycrestOrderRecord{}
	pc := &fakeProvider{name: ProviderPaycrest, orders: map[string]*Order{id: {Provider: ProviderPaycrest, Paycrest: rec}}}
	r := testRouter(pc)
	if o, err := r.Get(context.Background(), uuid.New(), id); err != nil || o.Paycrest != rec {
		t.Fatalf("bare uuid: %v %v", o, err)
	}
	if _, err := r.Get(context.Background(), uuid.New(), "nope:123"); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("unknown provider: %v", err)
	}
	if _, err := r.Cancel(context.Background(), uuid.New(), "garbage"); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("malformed: %v", err)
	}
}

func TestRouterList_MergesNewestFirst(t *testing.T) {
	now := time.Now()
	mk := func(p string, ago time.Duration) Order {
		return Order{Provider: p, Paycrest: &models.PaycrestOrderRecord{CreatedAt: now.Add(-ago)}}
	}
	a := &fakeProvider{name: "a", list: []Order{mk("a", 3*time.Hour), mk("a", time.Hour)}}
	b := &fakeProvider{name: "b", list: []Order{mk("b", 2*time.Hour)}}
	broken := &fakeProvider{name: "x", listErr: errors.New("down")}
	got, err := testRouter(a, broken, b).List(context.Background(), uuid.New(), 2)
	if err != nil || len(got) != 2 || got[0].CreatedAt().Before(got[1].CreatedAt()) || got[1].Provider != "b" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := testRouter(broken).List(context.Background(), uuid.New(), 5); err == nil {
		t.Fatal("all providers failing must error")
	}
}

// A nil Paycrest service must not become a typed-nil provider the router
// counts as live.
func TestNewRouter_SkipsAProviderBuiltFromNilServices(t *testing.T) {
	if r := NewRouter(Config{}, NewPaycrestProvider(nil, nil, nil)); r.Enabled() {
		t.Fatal("a provider built from nil services must leave the router disabled")
	}
}

// RAMP_PRICE_TOLERANCE_BPS=0 falls back to 50, but never silently.
func TestNewRouter_WarnsWhenToleranceIsDefaulted(t *testing.T) {
	buf := captureRampLog(t)
	NewRouter(Config{ToleranceBps: 0})
	if !strings.Contains(buf.String(), "RAMP_PRICE_TOLERANCE_BPS") {
		t.Fatalf("log = %q", buf.String())
	}
}
