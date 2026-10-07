package ramp

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakeMarket struct {
	rate *big.Rat
	err  error
}

func (m *fakeMarket) BuyRate(context.Context, string) (*big.Rat, error) { return m.rate, m.err }

type openTreasuryCall struct {
	user        uuid.UUID
	q           *services.StableBuyQuote
	chain, dest string
}

// fakeTreasuryIT embeds a real ITService for the pure pricing and scripts
// stock, gas and the write.
type fakeTreasuryIT struct {
	*services.ITService
	free      map[string]*big.Int // "chain|TOKEN"
	freeErr   error
	gasOK     bool
	dailyErr  error
	opened    []openTreasuryCall
	openErr   error
	intent    *models.ITIntent
	pending   *models.ITIntent
	abandoned int64
	afterOpen func()
	// conversion is what ConversionFor reads; convOpens records every
	// converted open.
	conversion *models.PaycrestOrderRecord
	convOpens  []services.ConvertedOpen
}

func newFakeTreasuryIT(t *testing.T) *fakeTreasuryIT {
	t.Helper()
	it := services.NewITService(nil, nil, nil)
	// Fixtures are worked at the central account's 1.40% funding fee; this
	// service is in VA mode, so the VA fee is pinned to it (channel pricing is
	// pinned in the services package).
	if err := it.SetRampBuyTerms(services.RampBuyTerms{VAFundingFeeBps: services.DefaultRampFundingFeeBps}); err != nil {
		t.Fatal(err)
	}
	return &fakeTreasuryIT{ITService: it,
		free: map[string]*big.Int{"base|USDC": big.NewInt(150_000_000)}, gasOK: true,
		intent: &models.ITIntent{ID: uuid.New(), Provider: models.ITProviderTreasury}}
}

func (f *fakeTreasuryIT) HasCompanyWallet() bool   { return true }
func (f *fakeTreasuryIT) TreasuryChains() []string { return []string{"base"} }
func (f *fakeTreasuryIT) TreasuryTokens(string) []string {
	return []string{"USDC", "USDT"}
}
func (f *fakeTreasuryIT) TreasuryFreeUnits(_ context.Context, chain, token string) (*big.Int, error) {
	if f.freeErr != nil {
		return nil, f.freeErr
	}
	if v, ok := f.free[chain+"|"+token]; ok {
		return v, nil
	}
	return big.NewInt(0), nil
}
func (f *fakeTreasuryIT) TreasuryCanPayGas(context.Context, string) bool { return f.gasOK }
func (f *fakeTreasuryIT) CheckTreasuryDailyCap(context.Context, uuid.UUID, *big.Int) error {
	return f.dailyErr
}
func (f *fakeTreasuryIT) OpenTreasuryBuy(_ context.Context, user uuid.UUID, q *services.StableBuyQuote, chain, dest string) (*models.ITIntent, *services.StableBuyInstructions, error) {
	f.opened = append(f.opened, openTreasuryCall{user, q, chain, dest})
	if f.afterOpen != nil {
		f.afterOpen()
	}
	if f.openErr != nil {
		return nil, nil, f.openErr
	}
	return f.intent, &services.StableBuyInstructions{AccountNumber: "9988776655"}, nil
}
func (f *fakeTreasuryIT) OpenTreasuryConvertedBuy(_ context.Context, user uuid.UUID, q *services.StableBuyQuote, chain string, in services.ConvertedOpen) (*models.ITIntent, *services.StableBuyInstructions, *models.PaycrestOrderRecord, error) {
	f.opened = append(f.opened, openTreasuryCall{user, q, chain, in.WalletAddress})
	f.convOpens = append(f.convOpens, in)
	if f.openErr != nil {
		return nil, nil, nil, f.openErr
	}
	return f.intent, &services.StableBuyInstructions{AccountNumber: "9988776655"}, &models.PaycrestOrderRecord{Reference: "ITC-x"}, nil
}
func (f *fakeTreasuryIT) ConversionFor(context.Context, *models.ITIntent) (*models.PaycrestOrderRecord, error) {
	return f.conversion, nil
}
func (f *fakeTreasuryIT) OpenStableBuy(context.Context, uuid.UUID) (*models.ITIntent, error) {
	return f.pending, nil
}
func (f *fakeTreasuryIT) CountAbandonedRoutedStableBuys(context.Context, uuid.UUID, time.Time) (int64, error) {
	return f.abandoned, nil
}
func (f *fakeTreasuryIT) GetStableBuyForUser(_ context.Context, _ uuid.UUID, id uuid.UUID, provider string) (*models.ITIntent, error) {
	if f.intent == nil || f.intent.ID != id || provider != models.ITIntentProvider(f.intent) {
		return nil, services.ErrStableIntentNotFound
	}
	return f.intent, nil
}
func (f *fakeTreasuryIT) StableBuyPayInto(context.Context, *models.ITIntent) (*services.StableBuyInstructions, error) {
	return nil, nil
}
func (f *fakeTreasuryIT) CancelBuy(context.Context, uuid.UUID, uuid.UUID) (*models.ITIntent, error) {
	return f.intent, nil
}

func newTestTreasuryProvider(f *fakeTreasuryIT) *TreasuryProvider {
	return newTreasuryProvider(f, &fakeMarket{rate: big.NewRat(1348, 1)})
}

func trBuy(amount string) Request { return Request{Side: Buy, FiatCode: "NGN", FiatAmount: amount} }

func trInput(limit string) OrderInput {
	return OrderInput{UserID: uuid.New(), WalletAddress: "0x000000000000000000000000000000000000dEaD",
		LimitRate: limit, Guard: services.PriceGuardEnforce}
}

func TestNewTreasuryProvider_NilUnlessEnabledAndWired(t *testing.T) {
	m := &fakeMarket{rate: big.NewRat(1348, 1)}
	if p := NewTreasuryProvider(nil, m, true); p != nil {
		t.Fatal("nil service")
	}
	if p := NewTreasuryProvider(services.NewITService(nil, nil, nil), m, true); p != nil {
		t.Fatal("no company wallet must not register")
	}
	if p := NewTreasuryProvider(services.NewITService(nil, nil, nil), nil, true); p != nil {
		t.Fatal("no market rate must not register")
	}
	var typedNil *services.TreasuryMarketRate
	if p := NewTreasuryProvider(services.NewITService(nil, nil, nil), typedNil, true); p != nil {
		t.Fatal("a typed-nil market rate must be a true nil Provider")
	}
}

// §4.1 / §11 (owner ruling 2026-10-07): market + take, the funding line, Base
// venue, no stock figure.
func TestTreasuryQuote_PricesAtMarketPlusTake(t *testing.T) {
	f := newFakeTreasuryIT(t)
	q, err := newTestTreasuryProvider(f).Quote(context.Background(), trBuy("10000"))
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Venues) != 1 { // USDT has no free stock: not offered
		t.Fatalf("venues = %+v", q.Venues)
	}
	v := q.Venues[0]
	if v.Provider != ProviderTreasury || v.ChainKey != "base" || v.Token != "USDC" || v.Rate != "1354.74" ||
		v.EffectiveRate != "1373.98" || v.TokenAmount != 7.27815 || len(v.Fees) != 1 || v.Fees[0].Kind != services.FeeKindFunding || q.MarketRate != 1348 {
		t.Fatalf("venue = %+v market = %v", v, q.MarketRate)
	}
	if name, k, _ := DecodeHandle(v.Handle); name != ProviderTreasury || k != (VenueKey{"base", "USDC"}) {
		t.Fatalf("handle → %s %+v", name, k)
	}
	if q, err := newTestTreasuryProvider(f).Quote(context.Background(), trBuy("100")); err != nil || q.Venues[0].TokenAmount != 0.072781 {
		t.Fatalf("₦100: %+v %v", q, err)
	}
}

func TestTreasuryQuote_RangeClosedAndStockNeverDisclosed(t *testing.T) {
	ctx := context.Background()
	f := newFakeTreasuryIT(t)
	p := newTestTreasuryProvider(f)
	if _, err := p.Quote(ctx, trBuy("99.99")); !errors.Is(err, ErrBelowMinimum) {
		t.Fatalf("₦99.99: %v", err)
	}
	if _, err := p.Quote(ctx, trBuy("276554")); !errors.Is(err, ErrAboveMaximum) {
		t.Fatalf("over $200: %v", err)
	}
	if _, err := p.Quote(ctx, Request{Side: Sell, FiatCode: "NGN", FiatAmount: "10000"}); !errors.Is(err, ErrLegClosed) {
		t.Fatalf("sell: %v", err)
	}
	// Open, but not for this amount: above-maximum, and the stock is not named.
	f.free["base|USDC"] = big.NewInt(5_000_000)
	_, err := p.Quote(ctx, trBuy("10000"))
	if !errors.Is(err, ErrAboveMaximum) || strings.Contains(err.Error(), "5") {
		t.Fatalf("stock short: %v — must be a range answer that never states the stock", err)
	}
	// Below the smallest order on every token: closed.
	f.free["base|USDC"] = big.NewInt(70_000)
	if _, err := p.Quote(ctx, trBuy("10000")); !errors.Is(err, ErrLegClosed) {
		t.Fatalf("empty wallet: %v", err)
	}
	f.free["base|USDC"] = big.NewInt(150_000_000)
	f.gasOK = false
	if _, err := p.Quote(ctx, trBuy("10000")); !errors.Is(err, ErrVenueUnavailable) {
		t.Fatalf("no gas: %v", err)
	}
	f.gasOK = true
	p.market = &fakeMarket{err: errors.New("paycrest and flutterwave down")}
	if _, err := p.Quote(ctx, trBuy("10000")); err == nil || errors.Is(err, ErrLegClosed) || errors.Is(err, ErrBelowMinimum) {
		t.Fatalf("no market rate: %v, want a plain failure the router reads as unavailable", err)
	}
}

// D58: Buy is open while some token covers the smallest order and gas is payable.
func TestTreasuryLegs(t *testing.T) {
	f := newFakeTreasuryIT(t)
	p := newTestTreasuryProvider(f)
	ctx := context.Background()
	if legs, _ := p.RampLegs(ctx); !legs["NGN"].Buy || legs["NGN"].Sell {
		t.Fatalf("stocked: %+v", legs)
	}
	f.free["base|USDC"] = big.NewInt(70_000) // below what ₦100 buys (72,328)
	if legs, _ := p.RampLegs(ctx); legs["NGN"].Buy {
		t.Fatal("an empty wallet reported Buy open")
	}
	f.free["base|USDC"] = big.NewInt(150_000_000)
	p.market = &fakeMarket{err: errors.New("down")}
	if legs, err := p.RampLegs(ctx); err != nil || legs["NGN"].Buy {
		t.Fatalf("no market: %+v %v, want closed, not an error", legs, err)
	}
}

func TestTreasuryOpen_OpensWithTheServerQuoteToHerOwnWallet(t *testing.T) {
	f := newFakeTreasuryIT(t)
	in := trInput("1373.98")
	o, err := newTestTreasuryProvider(f).Open(context.Background(), VenueKey{"base", "USDC"}, trBuy("10000"), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.opened) != 1 || f.opened[0].chain != "base" || f.opened[0].dest != in.WalletAddress ||
		f.opened[0].q.AssetUnits.Cmp(big.NewInt(7_278_150)) != 0 || f.opened[0].user != in.UserID {
		t.Fatalf("opened = %+v", f.opened)
	}
	if o.Provider != ProviderTreasury || o.IT != f.intent || o.PayInto == nil {
		t.Fatalf("order = %+v", o)
	}
}

func TestTreasuryOpen_PriceGuardAndCapsBeforeAnyWrite(t *testing.T) {
	ctx := context.Background()
	f := newFakeTreasuryIT(t)
	p := newTestTreasuryProvider(f)
	var moved *services.PriceMovedPastQuoteError
	if _, err := p.Open(ctx, VenueKey{"base", "USDC"}, trBuy("10000"), trInput("1373.00")); !errors.As(err, &moved) || moved.BoundRate != "1373.98" {
		t.Fatalf("guard: %v", err)
	}
	f.dailyErr = services.ErrTreasuryDailyCapReached
	if _, err := p.Open(ctx, VenueKey{"base", "USDC"}, trBuy("10000"), trInput("1373.98")); !errors.Is(err, ErrVenueUnavailable) || !errors.Is(err, services.ErrTreasuryDailyCapReached) {
		t.Fatalf("daily cap: %v", err)
	}
	f.dailyErr = nil
	f.abandoned = int64(services.DefaultRampDailyAbandonCap)
	if _, err := p.Open(ctx, VenueKey{"base", "USDC"}, trBuy("10000"), trInput("1373.98")); !errors.Is(err, ErrAbandonCapReached) {
		t.Fatalf("abandon cap: %v", err)
	}
	if len(f.opened) != 0 {
		t.Fatalf("wrote %d orders past a refusal", len(f.opened))
	}
	if _, err := p.Open(ctx, VenueKey{"polygon", "USDC"}, trBuy("10000"), trInput("1373.98")); !errors.Is(err, ErrVenueUnavailable) {
		t.Fatalf("a chain without a lane: %v", err)
	}
}

// Spec §4.1 (review minor): a lane that cannot pay the transfer's gas is
// refused before anything is written, and Paycrest may take the pair.
func TestTreasuryOpen_GasCheckedBeforeTheWrite(t *testing.T) {
	f := newFakeTreasuryIT(t)
	f.gasOK = false
	_, err := newTestTreasuryProvider(f).Open(context.Background(), VenueKey{"base", "USDC"}, trBuy("10000"), trInput("1373.98"))
	if !errors.Is(err, ErrVenueUnavailable) || len(f.opened) != 0 {
		t.Fatalf("err = %v, opened = %d, want unavailable and nothing written", err, len(f.opened))
	}
}

func TestTreasuryOpen_RefusalClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		class error
	}{
		{"kyc", services.ErrKYCVerificationRequired, ErrRefusedForUser},
		{"already open", &services.StableBuyOpenError{IntentID: uuid.New()}, ErrRefusedForUser},
		{"stock short under the lock", services.ErrTreasuryStockShort, ErrVenueUnavailable},
		{"stock unreadable", services.ErrTreasuryStockUnreadable, ErrVenueUnavailable},
		{"above maximum", services.ErrStableBuyAboveMaximum, ErrAboveMaximum},
		{"uncertain write", errors.New("db: connection reset"), ErrOutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeTreasuryIT(t)
			f.openErr = tc.cause
			_, err := newTestTreasuryProvider(f).Open(context.Background(), VenueKey{"base", "USDC"}, trBuy("10000"), trInput("1373.98"))
			if !errors.Is(err, tc.class) || !errors.Is(err, tc.cause) {
				t.Fatalf("err = %v, want %v carrying %v", err, tc.class, tc.cause)
			}
		})
	}
}

// §4.4: stock lost to a concurrent open fails over to Paycrest on Base.
func TestTreasuryOpen_StockShortFailsOverToPaycrest(t *testing.T) {
	f := newFakeTreasuryIT(t)
	f.openErr = services.ErrTreasuryStockShort
	tr := newTestTreasuryProvider(f)
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "base", "USDC", "1385"))
	pc.openResults = []openResult{{order: okOrder}}
	req := openReq(EncodeHandle(ProviderTreasury, VenueKey{"base", "USDC"}), "1382.57")
	req.Request.FiatAmount = "10000"
	o, err := testRouter(tr, pc).Open(context.Background(), req)
	if err != nil || o != okOrder {
		t.Fatalf("o=%v err=%v", o, err)
	}
}

// D34 for the company wallet: an uncertain write never fails over past a committed order.
func TestTreasuryOpen_UncertainWriteRechecksBeforeFailover(t *testing.T) {
	f := newFakeTreasuryIT(t)
	written := &models.ITIntent{ID: uuid.New()}
	f.openErr = errors.New("db: connection reset")
	f.afterOpen = func() { f.pending = written }
	tr := newTestTreasuryProvider(f)
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "base", "USDC", "1385"))
	pc.openResults = []openResult{{order: okOrder}}
	req := openReq(EncodeHandle(ProviderTreasury, VenueKey{"base", "USDC"}), "1382.57")
	req.Request.FiatAmount = "10000"
	_, err := testRouter(tr, pc).Open(context.Background(), req)
	var open *services.StableBuyOpenError
	if !errors.As(err, &open) || open.IntentID != written.ID || len(pc.opened) != 0 {
		t.Fatalf("err = %v paycrest opened %v", err, pc.opened)
	}
}

// §11: a foreign or other-provider id is a 404.
func TestTreasuryReads_OtherProvidersIdIsNotFound(t *testing.T) {
	f := newFakeTreasuryIT(t)
	p := newTestTreasuryProvider(f)
	if _, err := p.Get(context.Background(), uuid.New(), f.intent.ID.String()); err != nil {
		t.Fatalf("own: %v", err)
	}
	f.intent.Provider = models.ITProviderFlutterwave
	if _, err := p.Get(context.Background(), uuid.New(), f.intent.ID.String()); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("a Flutterwave order through the company wallet: %v", err)
	}
}

// The lane quoted a moment ago is gone (receipt probe failed) at open: the
// order fails over to Paycrest on the same pair instead of being refused.
func TestTreasuryOpen_LaneGoneFailsOverToPaycrest(t *testing.T) {
	f := newFakeTreasuryIT(t)
	tr := newTestTreasuryProvider(f)
	if q, err := tr.Quote(context.Background(), trBuy("10000")); err != nil || len(q.Venues) != 1 {
		t.Fatalf("quote: %+v %v", q, err)
	}
	gone := &laneGoneIT{fakeTreasuryIT: f}
	tr = newTestTreasuryProvider(f)
	tr.it = gone
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "base", "USDC", "1385"))
	pc.openResults = []openResult{{order: okOrder}}
	req := openReq(EncodeHandle(ProviderTreasury, VenueKey{"base", "USDC"}), "1382.57")
	req.Request.FiatAmount = "10000"
	o, err := testRouter(tr, pc).Open(context.Background(), req)
	if err != nil || o != okOrder || len(f.opened) != 0 {
		t.Fatalf("o=%v err=%v opened=%d", o, err, len(f.opened))
	}
}

type laneGoneIT struct{ *fakeTreasuryIT }

func (laneGoneIT) TreasuryChains() []string { return nil }
