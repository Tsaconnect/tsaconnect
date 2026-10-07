package ramp

import (
	"context"
	"errors"
	"math"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fwOpenCall struct {
	user  uuid.UUID
	q     *services.StableBuyQuote
	chain string
}

// fakeFWIT embeds a real ITService for the pure parts (QuoteRoutedStableBuy,
// RoutedAbandonCap) and scripts everything that would touch the network or
// the database. A method defined here shadows the embedded one.
type fakeFWIT struct {
	*services.ITService
	rate      *big.Rat
	rateErr   error
	rateCalls atomic.Int32
	gate      chan struct{}
	opened    []fwOpenCall
	openErr   error
	intent    *models.ITIntent
	payInto   *services.StableBuyInstructions
	pending   *models.ITIntent
	abandoned int64
	cancelErr error
	afterOpen func()

	conversion *models.PaycrestOrderRecord // what ConversionFor reads
	convOpens  []services.ConvertedOpen    // OpenFlutterwaveConvertedBuy calls
}

func newFakeFWIT(t *testing.T) *fakeFWIT {
	t.Helper()
	return &fakeFWIT{ITService: services.NewITService(nil, nil, nil), rate: big.NewRat(165025, 100)}
}

func (f *fakeFWIT) HasStableBuy() bool { return true }
func (f *fakeFWIT) StableBuyFiatPerUnit(context.Context, string, string) (*big.Rat, error) {
	f.rateCalls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	return f.rate, f.rateErr
}
func (f *fakeFWIT) OpenRoutedStableBuy(_ context.Context, user uuid.UUID, q *services.StableBuyQuote, chain string) (*models.ITIntent, *services.StableBuyInstructions, error) {
	f.opened = append(f.opened, fwOpenCall{user, q, chain})
	if f.afterOpen != nil {
		f.afterOpen()
	}
	if f.openErr != nil {
		return nil, nil, f.openErr
	}
	return f.intent, f.payInto, nil
}
func (f *fakeFWIT) OpenFlutterwaveConvertedBuy(_ context.Context, user uuid.UUID, q *services.StableBuyQuote, in services.ConvertedOpen) (*models.ITIntent, *services.StableBuyInstructions, *models.PaycrestOrderRecord, error) {
	f.opened = append(f.opened, fwOpenCall{user, q, services.RoutedDirectChain})
	f.convOpens = append(f.convOpens, in)
	if f.openErr != nil {
		return nil, nil, nil, f.openErr
	}
	return f.intent, f.payInto, &models.PaycrestOrderRecord{Holder: models.HolderFlutterwave}, nil
}
func (f *fakeFWIT) ConversionFor(context.Context, *models.ITIntent) (*models.PaycrestOrderRecord, error) {
	return f.conversion, nil
}
func (f *fakeFWIT) OpenStableBuy(context.Context, uuid.UUID) (*models.ITIntent, error) {
	return f.pending, nil
}
func (f *fakeFWIT) CountAbandonedRoutedStableBuys(context.Context, uuid.UUID, time.Time) (int64, error) {
	return f.abandoned, nil
}
func (f *fakeFWIT) GetStableBuyForUser(_ context.Context, _ uuid.UUID, id uuid.UUID, provider string) (*models.ITIntent, error) {
	if f.intent == nil || f.intent.ID != id || provider != models.ITIntentProvider(f.intent) {
		return nil, services.ErrStableIntentNotFound
	}
	return f.intent, nil
}
func (f *fakeFWIT) StableBuyPayInto(context.Context, *models.ITIntent) (*services.StableBuyInstructions, error) {
	return f.payInto, nil
}
func (f *fakeFWIT) CancelBuy(context.Context, uuid.UUID, uuid.UUID) (*models.ITIntent, error) {
	if f.cancelErr != nil {
		return nil, f.cancelErr
	}
	c := *f.intent
	c.Status = models.ITStatusCancelled
	return &c, nil
}

func fwBuy(amount string) Request { return Request{Side: Buy, FiatCode: "NGN", FiatAmount: amount} }

func fwInput(limit string, guard services.PriceGuardMode) OrderInput {
	return OrderInput{UserID: uuid.New(), LimitRate: limit, Guard: guard}
}

func TestNewFlutterwaveProvider_NilUnlessEnabledAndWired(t *testing.T) {
	if p := NewFlutterwaveProvider(nil, true); p != nil {
		t.Fatal("nil service must give a true nil provider")
	}
	if p := NewFlutterwaveProvider(services.NewITService(nil, nil, nil), true); p != nil {
		t.Fatal("a dormant buy rail must not register")
	}
	if p := NewFlutterwaveProvider(services.NewITService(nil, nil, nil), false); p != nil {
		t.Fatal("RAMP_FLUTTERWAVE_BUY=false must not register")
	}
	if r := NewRouter(Config{}, NewFlutterwaveProvider(nil, true)); r.Enabled() {
		t.Fatal("the router counted a nil provider as live")
	}
}

func TestFlutterwaveLegs_BuyOnly(t *testing.T) {
	legs, err := newFlutterwaveProvider(newFakeFWIT(t)).RampLegs(context.Background())
	if err != nil || legs["NGN"] != (services.RampLegs{Buy: true}) {
		t.Fatalf("legs = %+v %v", legs, err)
	}
}

// The worked example (owner ruling 2026-10-07): ₦165,025 at ₦1650.25 →
// 95.137885 delivered; display ₦1658.50 (the take only), effective ₦1734.59,
// three fee lines at cost (spec §14).
func TestFlutterwaveQuote_DisplayRateEffectiveRateAndFees(t *testing.T) {
	q, err := newFlutterwaveProvider(newFakeFWIT(t)).Quote(context.Background(), fwBuy("165025"))
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if len(q.Venues) != 2 { // USDC and USDT, Polygon only
		t.Fatalf("venues = %+v", q.Venues)
	}
	var usdc *Venue
	for i, v := range q.Venues {
		if v.Provider != ProviderFlutterwave || v.Kind != models.RouteKindDirect || v.ChainKey != "polygon" {
			t.Fatalf("venue %+v — Polygon only", v)
		}
		if v.Token == "USDC" {
			usdc = &q.Venues[i]
		}
	}
	if usdc == nil || usdc.Rate != "1658.50" || usdc.EffectiveRate != "1734.59" || usdc.TokenAmount != 95.137885 {
		t.Fatalf("polygon USDC = %+v", usdc)
	}
	if len(usdc.Fees) != 3 ||
		usdc.Fees[0] != (VenueFee{Kind: "funding", Label: "Funding fee (1.4%)", Provider: "Flutterwave", Token: "1.393035", Ngn: "2310.34"}) ||
		usdc.Fees[1] != (VenueFee{Kind: "conversion", Label: "Flutterwave conversion fee (1.5%)", Provider: "Flutterwave", Token: "1.471642", Ngn: "2440.71"}) ||
		usdc.Fees[2] != (VenueFee{Kind: "network", Label: "Network fee", Provider: "Flutterwave", Token: "1.5", Ngn: "2487.75"}) {
		t.Fatalf("fees = %+v", usdc.Fees)
	}
	if math.Abs(usdc.SpreadPercent-5.1107) > 0.01 {
		t.Fatalf("spread%% = %v, want ~5.11 all-in against the raw rate", usdc.SpreadPercent)
	}
	name, k, err := DecodeHandle(usdc.Handle)
	if err != nil || name != ProviderFlutterwave || k != (VenueKey{"polygon", "USDC"}) {
		t.Fatalf("handle → %q %+v %v", name, k, err)
	}
	if q.MarketRate != 1650.25 {
		t.Fatalf("market = %v", q.MarketRate)
	}
}

// Review Focus 5: on the JSON the user sees, amount ÷ rate − Σfees = tokenAmount
// within one base unit.
func TestFlutterwaveQuote_FeeLinesAddUp(t *testing.T) {
	p := newFlutterwaveProvider(newFakeFWIT(t))
	for _, amount := range []string{"20000", "165025", "999999.99", "3000000"} {
		q, err := p.Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: amount, Token: "USDC"})
		if err != nil {
			if errors.Is(err, ErrAboveMaximum) {
				continue
			}
			t.Fatalf("₦%s: %v", amount, err)
		}
		v := q.Venues[0]
		units := func(dec string) *big.Rat {
			r, _ := new(big.Rat).SetString(dec)
			return r.Mul(r, big.NewRat(1_000_000, 1))
		}
		naira, _ := new(big.Rat).SetString(amount)
		rate, _ := new(big.Rat).SetString(v.Rate)
		viaRate := new(big.Rat).Quo(naira, rate)
		viaRate.Mul(viaRate, big.NewRat(1_000_000, 1))
		floor := new(big.Int).Quo(viaRate.Num(), viaRate.Denom())
		sum := new(big.Rat)
		for _, f := range v.Fees {
			sum.Add(sum, units(f.Token))
		}
		got := new(big.Rat).Sub(new(big.Rat).SetInt(floor), sum)
		want := units(new(big.Float).SetFloat64(v.TokenAmount).Text('f', 6))
		diff := new(big.Rat).Sub(got, want)
		if diff.Abs(diff).Cmp(big.NewRat(1, 1)) > 0 {
			t.Fatalf("₦%s: amount÷rate−fees = %s units, tokenAmount = %s", amount, got.FloatString(0), want.FloatString(0))
		}
	}
}

func TestFlutterwaveQuote_ClosedAndOutOfRange(t *testing.T) {
	p := newFlutterwaveProvider(newFakeFWIT(t))
	ctx := context.Background()
	if _, err := p.Quote(ctx, Request{Side: Sell, FiatCode: "NGN", FiatAmount: "165025"}); !errors.Is(err, ErrLegClosed) {
		t.Fatalf("sell: %v", err)
	}
	if _, err := p.Quote(ctx, Request{Side: Buy, FiatCode: "KES", FiatAmount: "165025"}); !errors.Is(err, ErrLegClosed) {
		t.Fatalf("KES: %v", err)
	}
	if _, err := p.Quote(ctx, fwBuy("17000")); !errors.Is(err, ErrBelowMinimum) {
		t.Fatalf("below 10 tokens: %v", err)
	}
	if _, err := p.Quote(ctx, fwBuy("4000000")); !errors.Is(err, ErrAboveMaximum) {
		t.Fatalf("above $2000: %v", err)
	}
	for _, chain := range []string{"base", "ethereum", "solana"} {
		r := fwBuy("165025")
		r.Token, r.ChainKey = "USDC", chain
		if _, err := p.Quote(ctx, r); !errors.Is(err, ErrNoVenue) {
			t.Fatalf("USDC on %s: %v, want no venue (Polygon only)", chain, err)
		}
	}
}

func TestFlutterwaveQuote_CachesARateFailureBriefly(t *testing.T) {
	f := newFakeFWIT(t)
	f.rateErr = errors.New("flutterwave 503")
	p := newFlutterwaveProvider(f)
	start := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	clock := start
	p.now = func() time.Time { return clock }
	req := fwBuy("165025")
	req.Token = "USDC"
	for i := 0; i < 3; i++ {
		if _, err := p.Quote(context.Background(), req); err == nil {
			t.Fatal("quoted through an outage")
		}
	}
	if n := f.rateCalls.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1 within the failure window", n)
	}
	clock = start.Add(6 * time.Second)
	f.rateErr = nil
	if _, err := p.Quote(context.Background(), req); err != nil || f.rateCalls.Load() != 2 {
		t.Fatalf("after the window: fetches = %d err = %v", f.rateCalls.Load(), err)
	}
}

// Spec §7: the public quote reaches Flutterwave only through a 30s cache, and
// concurrent misses share one fetch.
func TestFlutterwaveQuote_CachesTheRateAndSingleFlightsMisses(t *testing.T) {
	f := newFakeFWIT(t)
	f.gate = make(chan struct{})
	p := newFlutterwaveProvider(f)
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return clock }
	req := fwBuy("165025")
	req.Token = "USDC"
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Quote(context.Background(), req); err != nil {
				t.Error(err)
			}
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.rateCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the other four join the flight
	close(f.gate)
	wg.Wait()
	if n := f.rateCalls.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1 shared by five concurrent quotes", n)
	}
	if _, err := p.Quote(context.Background(), req); err != nil || f.rateCalls.Load() != 1 {
		t.Fatalf("within 30s: fetches = %d err = %v", f.rateCalls.Load(), err)
	}
	clock = clock.Add(31 * time.Second)
	if _, err := p.Quote(context.Background(), req); err != nil || f.rateCalls.Load() != 2 {
		t.Fatalf("after 30s: fetches = %d err = %v", f.rateCalls.Load(), err)
	}
}

func TestFlutterwaveOpen_RequotesFreshAndOpensWithTheServerQuote(t *testing.T) {
	f := newFakeFWIT(t)
	f.intent = &models.ITIntent{ID: uuid.New(), Provider: models.ITProviderFlutterwave}
	f.payInto = &services.StableBuyInstructions{AccountNumber: "0123456789"}
	p := newFlutterwaveProvider(f)
	ctx := context.Background()
	if _, err := p.Quote(ctx, Request{Side: Buy, FiatCode: "NGN", FiatAmount: "165025", Token: "USDC"}); err != nil {
		t.Fatal(err)
	}
	in := fwInput("1734.59", services.PriceGuardEnforce)
	o, err := p.Open(ctx, VenueKey{"polygon", "USDC"}, fwBuy("165025"), in)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if f.rateCalls.Load() != 2 {
		t.Fatalf("fetches = %d, want a fresh rate at open (D7)", f.rateCalls.Load())
	}
	if len(f.opened) != 1 || f.opened[0].chain != "POLYGON" || f.opened[0].user != in.UserID ||
		f.opened[0].q.AssetUnits.Cmp(big.NewInt(95_137_885)) != 0 {
		t.Fatalf("opened = %+v", f.opened)
	}
	if o.Provider != ProviderFlutterwave || o.IT != f.intent || o.PayInto != f.payInto {
		t.Fatalf("order = %+v", o)
	}
}

// D40: the guard binds on the effective rate.
func TestFlutterwaveOpen_PriceGuard(t *testing.T) {
	f := newFakeFWIT(t)
	f.intent = &models.ITIntent{ID: uuid.New()}
	p := newFlutterwaveProvider(f)
	ctx := context.Background()
	_, err := p.Open(ctx, VenueKey{"polygon", "USDC"}, fwBuy("165025"), fwInput("1700.00", services.PriceGuardEnforce))
	var moved *services.PriceMovedPastQuoteError
	if !errors.As(err, &moved) || moved.BoundRate != "1734.59" || len(f.opened) != 0 {
		t.Fatalf("enforce: err = %v opened = %d", err, len(f.opened))
	}
	logs := captureRampLog(t)
	if _, err := p.Open(ctx, VenueKey{"polygon", "USDC"}, fwBuy("165025"), fwInput("1700.00", services.PriceGuardLog)); err != nil {
		t.Fatalf("log mode must open: %v", err)
	}
	if !containsAny(logs.String(), "stage=bind provider=flutterwave") {
		t.Fatalf("log = %q", logs.String())
	}
}

func TestFlutterwaveOpen_SharedAbandonCap(t *testing.T) {
	f := newFakeFWIT(t)
	f.intent = &models.ITIntent{ID: uuid.New()}
	f.abandoned = int64(services.DefaultRampDailyAbandonCap)
	p := newFlutterwaveProvider(f)
	logs := captureRampLog(t)
	_, err := p.Open(context.Background(), VenueKey{"polygon", "USDC"}, fwBuy("165025"), fwInput("1754.34", services.PriceGuardEnforce))
	if !errors.Is(err, ErrVenueUnavailable) || !errors.Is(err, ErrAbandonCapReached) || len(f.opened) != 0 {
		t.Fatalf("err = %v opened = %d", err, len(f.opened))
	}
	if !containsAny(logs.String(), "ramp_abandon_cap_hit provider=flutterwave") {
		t.Fatalf("log = %q", logs.String())
	}
	f.abandoned--
	if _, err := p.Open(context.Background(), VenueKey{"polygon", "USDC"}, fwBuy("165025"), fwInput("1754.34", services.PriceGuardEnforce)); err != nil {
		t.Fatalf("under the cap: %v", err)
	}
}

func TestFlutterwaveOpen_RefusalClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		class error
	}{
		{"kyc", services.ErrKYCVerificationRequired, ErrRefusedForUser},
		{"agreement", services.ErrITAgreementRequired, ErrRefusedForUser},
		{"already open", &services.StableBuyOpenError{IntentID: uuid.New()}, ErrRefusedForUser},
		{"below minimum", services.ErrStableBuyBelowMinimum, ErrBelowMinimum},
		{"above maximum", services.ErrStableBuyAboveMaximum, ErrAboveMaximum},
		{"no address", services.ErrBadDeliveryAddress, ErrNoVenue},
		{"uncertain write", errors.New("db: connection reset"), ErrOutcomeUnknown},
		{"no virtual account", services.ErrStableBuyNoVA, ErrVenueUnavailable},
		{"rail dormant", services.ErrStableQuoteUnavailable, ErrVenueUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeFWIT(t)
			f.openErr = tc.cause
			_, err := newFlutterwaveProvider(f).Open(context.Background(), VenueKey{"polygon", "USDC"}, fwBuy("165025"),
				fwInput("1754.34", services.PriceGuardEnforce))
			if !errors.Is(err, tc.class) || !errors.Is(err, tc.cause) {
				t.Fatalf("err = %v, want %v carrying %v", err, tc.class, tc.cause)
			}
		})
	}
	for _, chain := range []string{"bsc", "ethereum", "base"} {
		f := newFakeFWIT(t)
		if _, err := newFlutterwaveProvider(f).Open(context.Background(), VenueKey{chain, "USDC"}, fwBuy("165025"),
			fwInput("1754.34", services.PriceGuardEnforce)); !errors.Is(err, ErrNoVenue) || f.rateCalls.Load() != 0 {
			t.Fatalf("%s: %v, want no venue before any rate call", chain, err)
		}
	}
}

// D34: an error after the write started may follow a committed row; the
// router re-runs its pre-flight, which finds the purchase.
func TestFlutterwaveOpen_UncertainWriteRechecksBeforeFailover(t *testing.T) {
	f := newFakeFWIT(t)
	written := &models.ITIntent{ID: uuid.New()}
	f.openErr = errors.New("db: connection reset")
	f.afterOpen = func() { f.pending = written }
	fw := newFlutterwaveProvider(f)
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "polygon", "USDC", "1700"))
	pc.openResults = []openResult{{order: okOrder}}
	req := openReq(EncodeHandle(ProviderFlutterwave, VenueKey{"polygon", "USDC"}), "1734.59")
	req.Request.ChainKey = "polygon"
	req.Request.FiatAmount = "165025"
	_, err := testRouter(pc, fw).Open(context.Background(), req)
	var open *services.StableBuyOpenError
	if !errors.As(err, &open) || open.IntentID != written.ID || len(pc.opened) != 0 {
		t.Fatalf("err = %v, Paycrest opened %v", err, pc.opened)
	}
}

func TestITBuy_CheckNoOpenOrderCarriesTheIntentID(t *testing.T) {
	f := newFakeFWIT(t)
	p := newFlutterwaveProvider(f)
	ctx := context.Background()
	if err := p.CheckNoOpenOrder(ctx, uuid.New(), Buy); err != nil {
		t.Fatalf("nothing open: %v", err)
	}
	f.pending = &models.ITIntent{ID: uuid.New(), Provider: models.ITProviderTreasury} // any provider's
	err := p.CheckNoOpenOrder(ctx, uuid.New(), Buy)
	var open *services.StableBuyOpenError
	if !errors.Is(err, ErrRefusedForUser) || !errors.As(err, &open) || open.IntentID != f.pending.ID {
		t.Fatalf("err = %v", err)
	}
	if err := p.CheckNoOpenOrder(ctx, uuid.New(), Sell); err != nil {
		t.Fatalf("a buy must not block a sell: %v", err)
	}
}

// §4.2: reads are scoped to the user and to this provider; foreign is a 404.
func TestITBuy_ReadsScopedAndForeignIs404(t *testing.T) {
	f := newFakeFWIT(t)
	f.intent = &models.ITIntent{ID: uuid.New(), Provider: models.ITProviderFlutterwave, Direction: models.ITDirectionBuy,
		Asset: "USDC", Status: models.ITStatusPendingPayment}
	f.payInto = &services.StableBuyInstructions{AccountNumber: "0123456789"}
	p := newFlutterwaveProvider(f)
	ctx := context.Background()
	o, err := p.Get(ctx, uuid.New(), f.intent.ID.String())
	if err != nil || o.IT != f.intent || o.PayInto != f.payInto {
		t.Fatalf("get: %+v %v", o, err)
	}
	if o, err := p.ClaimPayment(ctx, uuid.New(), f.intent.ID.String()); err != nil || o.IT != f.intent {
		t.Fatalf("claim: %+v %v", o, err)
	}
	for _, id := range []string{uuid.NewString(), "not-a-uuid"} {
		if _, err := p.Get(ctx, uuid.New(), id); !errors.Is(err, ErrOrderNotFound) {
			t.Fatalf("get %q: %v", id, err)
		}
		if _, err := p.Cancel(ctx, uuid.New(), id); !errors.Is(err, ErrOrderNotFound) {
			t.Fatalf("cancel %q: %v", id, err)
		}
	}
	f.intent.Provider = models.ITProviderTreasury // the company wallet's order is not Flutterwave's
	if _, err := p.Get(ctx, uuid.New(), f.intent.ID.String()); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("other provider: %v", err)
	}
	f.intent.Provider = models.ITProviderFlutterwave
	c, err := p.Cancel(ctx, uuid.New(), f.intent.ID.String())
	if err != nil || c.IT.Status != models.ITStatusCancelled {
		t.Fatalf("cancel: %+v %v", c, err)
	}
	f.cancelErr = services.ErrITBuyPaymentArrived
	if _, err := p.Cancel(ctx, uuid.New(), f.intent.ID.String()); !errors.Is(err, services.ErrITBuyPaymentArrived) {
		t.Fatalf("payment arrived: %v", err)
	}
	if list, err := p.List(ctx, uuid.New(), 25); err != nil || len(list) != 0 {
		t.Fatalf("list = %v %v, want empty (D9)", list, err)
	}
}
