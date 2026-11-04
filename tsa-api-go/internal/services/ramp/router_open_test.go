package ramp

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func openReq(handle, quoted string) OpenRequest {
	return OpenRequest{
		Request:    Request{Side: Buy, FiatCode: "NGN", FiatAmount: "1000", Token: "USDC", ChainKey: "base"},
		Handle:     handle,
		QuotedRate: quoted,
		Input:      OrderInput{UserID: uuid.New(), WalletAddress: "0xabc"},
	}
}

var okOrder = &Order{Provider: "a", Paycrest: &models.PaycrestOrderRecord{}}

func TestRouterOpen_OpensAtTheHandleWithTheLimit(t *testing.T) {
	a := &fakeProvider{name: "a", openResults: []openResult{{order: okOrder}}}
	o, err := testRouter(a).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
	if err != nil || o != okOrder {
		t.Fatalf("%v %v", o, err)
	}
	if a.opened[0] != (VenueKey{"base", "USDC"}) || a.openInputs[0].LimitRate != "1407.00000000" ||
		a.openInputs[0].Guard != services.PriceGuardEnforce {
		t.Fatalf("open input = %+v %+v", a.opened, a.openInputs)
	}
}

func TestRouterOpen_RejectsAMissingOrBadQuote(t *testing.T) {
	r := testRouter(&fakeProvider{name: "a"})
	if _, err := r.Open(context.Background(), openReq("", "")); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("no quoted rate: %v", err)
	}
	if _, err := r.Open(context.Background(), openReq("!!!", "1400")); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("bad handle: %v", err)
	}
	other := openReq(EncodeHandle("a", VenueKey{"polygon", "USDT"}), "1400")
	if _, err := r.Open(context.Background(), other); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("handle for another pair: %v", err)
	}
	unpinned := openReq("", "1400")
	unpinned.Request.Token = ""
	if _, err := r.Open(context.Background(), unpinned); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("unpinned open: %v", err)
	}
}

func TestRouterOpen_PreflightRefusesWhenAnyProviderHasAnOpenOrder(t *testing.T) {
	a := &fakeProvider{name: "a", openResults: []openResult{{order: okOrder}}}
	b := &fakeProvider{name: "b", hasOpen: []bool{true}}
	_, err := testRouter(a, b).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
	if !errors.Is(err, ErrRefusedForUser) || !errors.Is(err, services.ErrPaycrestOrderOpen) || len(a.opened) != 0 {
		t.Fatalf("err = %v opened = %v", err, a.opened)
	}
}

func TestRouterOpen_RefusedForUserStopsImmediately(t *testing.T) {
	refusal := WrapClass(ErrRefusedForUser, services.ErrKYCVerificationRequired)
	a := &fakeProvider{name: "a", openResults: []openResult{{err: refusal}}}
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1390")}},
		openResults: []openResult{{order: okOrder}}}
	_, err := testRouter(a, b).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
	if !errors.Is(err, services.ErrKYCVerificationRequired) || len(b.opened) != 0 {
		t.Fatalf("err = %v, b opened %v — a user refusal must never fail over", err, b.opened)
	}
}

func TestRouterOpen_FailsOverOnPriceMovedAndUnavailable(t *testing.T) {
	for name, firstErr := range map[string]error{
		"price moved": services.ErrPriceMovedPastQuote,
		"unavailable": WrapClass(ErrVenueUnavailable, errors.New("502")),
	} {
		t.Run(name, func(t *testing.T) {
			a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1400")}},
				openResults: []openResult{{err: firstErr}}}
			b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1405")}},
				openResults: []openResult{{order: okOrder}}}
			o, err := testRouter(a, b).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
			if err != nil || o != okOrder || len(a.opened) != 1 || len(b.opened) != 1 {
				t.Fatalf("o=%v err=%v a=%v b=%v", o, err, a.opened, b.opened)
			}
		})
	}
}

func TestRouterOpen_UnknownOutcomeWithOpenOrderRefuses(t *testing.T) {
	a := &fakeProvider{name: "a", hasOpen: []bool{false, true}, // pre-flight clean, then the row turned out committed
		openResults: []openResult{{err: WrapClass(ErrOutcomeUnknown, errors.New("commit lost"))}}}
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1390")}},
		openResults: []openResult{{order: okOrder}}}
	_, err := testRouter(a, b).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
	if !errors.Is(err, ErrRefusedForUser) || len(b.opened) != 0 {
		t.Fatalf("err = %v, b opened %v — an order may exist, so no second one", err, b.opened)
	}
}

func TestRouterOpen_UnknownOutcomeWithoutOpenOrderFailsOver(t *testing.T) {
	a := &fakeProvider{name: "a", openResults: []openResult{{err: WrapClass(ErrOutcomeUnknown, errors.New("timeout"))}}}
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1390")}},
		openResults: []openResult{{order: okOrder}}}
	if o, err := testRouter(a, b).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400")); err != nil || o != okOrder {
		t.Fatalf("%v %v", o, err)
	}
}

func TestRouterOpen_InfrastructureErrorStops(t *testing.T) {
	dbDown := errors.New("db connection refused")
	a := &fakeProvider{name: "a", openResults: []openResult{{err: dbDown}}}
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1390")}},
		openResults: []openResult{{order: okOrder}}}
	if _, err := testRouter(a, b).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400")); !errors.Is(err, dbDown) || len(b.opened) != 0 {
		t.Fatalf("err = %v b = %v", err, b.opened)
	}
}

func TestRouterOpen_NoHandleStartsFromTheBestVenueWithinTolerance(t *testing.T) {
	a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1420")}}} // outside 1407
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1402")}},
		openResults: []openResult{{order: okOrder}}}
	o, err := testRouter(a, b).Open(context.Background(), openReq("", "1400"))
	if err != nil || o != okOrder || len(a.opened) != 0 || len(b.opened) != 1 {
		t.Fatalf("o=%v err=%v a=%v b=%v", o, err, a.opened, b.opened)
	}
}

func TestRouterOpen_Outcomes(t *testing.T) {
	t.Run("price moved carries the fresh quote", func(t *testing.T) {
		a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1500")}}}
		_, err := testRouter(a).Open(context.Background(), openReq("", "1400"))
		var pm *PriceMovedError
		if !errors.As(err, &pm) || pm.Quote == nil || pm.Quote.Best.Rate != "1500" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("guard refusal at the only venue is price moved", func(t *testing.T) {
		a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1400")}},
			openResults: []openResult{{err: services.ErrPriceMovedPastQuote}}}
		_, err := testRouter(a).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
		var pm *PriceMovedError
		if !errors.As(err, &pm) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("nothing quotes: no venue / leg closed", func(t *testing.T) {
		a := &fakeProvider{name: "a", quoteErr: ErrLegClosed}
		if _, err := testRouter(a).Open(context.Background(), openReq("", "1400")); !errors.Is(err, ErrLegClosed) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the only provider is down: unavailable, not price moved", func(t *testing.T) {
		a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1400")}},
			openResults: []openResult{{err: WrapClass(ErrVenueUnavailable, errors.New("502"))}}}
		_, err := testRouter(a).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRouterOpen_BudgetStopsAttempts(t *testing.T) {
	a := &fakeProvider{name: "a", quoteDelay: 50 * time.Millisecond,
		quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1400")}}}
	r := NewRouter(Config{ToleranceBps: 50, Guard: services.PriceGuardEnforce,
		ProviderTimeout: time.Second, AttemptTimeout: time.Second, OpenBudget: 10 * time.Millisecond}, a)
	start := time.Now()
	_, err := r.Open(context.Background(), openReq("", "1400"))
	if err == nil || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

// The rollout default: log mode must never refuse — it records and opens.
func TestRouterOpen_LogModeOpensOutsideToleranceAndRecordsIt(t *testing.T) {
	buf := captureRampLog(t)
	a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1500")}},
		openResults: []openResult{{order: okOrder}}}
	r := NewRouter(Config{ToleranceBps: 50, Guard: services.PriceGuardLog,
		ProviderTimeout: time.Second, AttemptTimeout: time.Second, OpenBudget: 2 * time.Second}, a)
	o, err := r.Open(context.Background(), openReq("", "1400"))
	if err != nil || o != okOrder {
		t.Fatalf("log mode must open: %v %v", o, err)
	}
	if !strings.Contains(buf.String(), "ramp_price_guard_would_refuse") {
		t.Fatalf("log mode must record the would-refuse: %q", buf.String())
	}
}

// A provider refusing for a reason the user can act on (a payout bank it
// cannot pay) reaches her with its own wording, not a generic outage.
func TestRouterOpen_UnavailableCarriesTheProvidersReason(t *testing.T) {
	a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1400")}},
		openResults: []openResult{{err: WrapClass(ErrVenueUnavailable, services.ErrPaycrestBankUnsupported)}}}
	_, err := testRouter(a).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, services.ErrPaycrestBankUnsupported) ||
		err.Error() != services.ErrPaycrestBankUnsupported.Error() {
		t.Fatalf("err = %v", err)
	}
}

func TestRouterOpen_PriceMovedNeverSendsNullVenues(t *testing.T) {
	a := &fakeProvider{name: "a", quoteErr: errors.New("down"),
		openResults: []openResult{{err: services.ErrPriceMovedPastQuote}}}
	_, err := testRouter(a).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
	var pm *PriceMovedError
	if !errors.As(err, &pm) || pm.Quote == nil || pm.Quote.Venues == nil {
		t.Fatalf("err = %v", err)
	}
}

// Enforce refuses on the rate the order binds at (Paycrest: /v2/rates), not
// the book rate the quote shows. The re-quote must show the bound rate, or
// she confirms the book rate and is refused again, forever.
func TestRouterOpen_PriceMovedShowsTheBoundRate(t *testing.T) {
	handle := EncodeHandle("a", VenueKey{"base", "USDC"})
	a := &fakeProvider{name: "a", bindRate: "1450",
		quote:       &ProviderQuote{MarketRate: 1380, Venues: []Venue{venue("a", "base", "USDC", "1400")}},
		openResults: []openResult{{order: okOrder}}}
	r := testRouter(a)
	_, err := r.Open(context.Background(), openReq(handle, "1400"))
	var pm *PriceMovedError
	if !errors.As(err, &pm) || pm.Quote == nil || pm.Quote.Best == nil || len(pm.Quote.Venues) != 1 {
		t.Fatalf("err = %v", err)
	}
	v := pm.Quote.Best
	if v.Rate != "1450" || v.Handle != handle || v.Provider != "a" {
		t.Fatalf("venue = %+v, want the bound rate 1450 at a", v)
	}
	if want := 1000 / 1450.0; v.TokenAmount < want-1e-9 || v.TokenAmount > want+1e-9 {
		t.Fatalf("tokenAmount = %v, want %v (repriced at the bound rate)", v.TokenAmount, want)
	}
	if want := (1450.0/1380 - 1) * 100; v.SpreadPercent < want-1e-9 || v.SpreadPercent > want+1e-9 {
		t.Fatalf("spreadPercent = %v, want %v", v.SpreadPercent, want)
	}

	// Confirming what she was shown gives a limit the same bind passes.
	o, err := r.Open(context.Background(), openReq(v.Handle, v.Rate))
	if err != nil || o != okOrder {
		t.Fatalf("re-confirm at the bound rate: o=%v err=%v (limit %s)", o, err, a.openInputs[len(a.openInputs)-1].LimitRate)
	}
}

// A refusal whose re-quote failed still shows the bound venue to confirm.
func TestRouterOpen_PriceMovedShowsTheBoundRateWithoutARequote(t *testing.T) {
	a := &fakeProvider{name: "a", bindRate: "1450", quoteErr: errors.New("down")}
	_, err := testRouter(a).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
	var pm *PriceMovedError
	if !errors.As(err, &pm) || pm.Quote == nil || len(pm.Quote.Venues) != 1 || pm.Quote.Best == nil {
		t.Fatalf("err = %v", err)
	}
	if v := pm.Quote.Best; v.Rate != "1450" || v.ChainKey != "base" || v.Token != "USDC" ||
		v.Handle != EncodeHandle("a", VenueKey{"base", "USDC"}) || v.Kind != models.RouteKindDirect {
		t.Fatalf("venue = %+v", v)
	}
}

// Only stage=bind predicts enforce refusals; the quote-stage line says so.
func TestRouterOpen_QuoteStageWouldRefuseIsTagged(t *testing.T) {
	buf := captureRampLog(t)
	a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1500")}},
		openResults: []openResult{{order: okOrder}}}
	r := NewRouter(Config{ToleranceBps: 50, Guard: services.PriceGuardLog,
		ProviderTimeout: time.Second, AttemptTimeout: time.Second, OpenBudget: 2 * time.Second}, a)
	if _, err := r.Open(context.Background(), openReq("", "1400")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "ramp_price_guard_would_refuse stage=quote provider=a side=buy") ||
		!strings.Contains(buf.String(), "tol_bps=50") {
		t.Fatalf("log = %q", buf.String())
	}
}

func captureRampLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &b
}

func TestRouterOpen_BuyerIDFailsOverToAVenueThatNeedsNone(t *testing.T) {
	a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1400")}},
		openResults: []openResult{{err: WrapClass(ErrVenueUnavailable, services.ErrBuyerIDRequired)}}}
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1405")}},
		openResults: []openResult{{order: okOrder}}}
	o, err := testRouter(a, b).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
	if err != nil || o != okOrder || len(b.opened) != 1 {
		t.Fatalf("o=%v err=%v b=%v", o, err, b.opened)
	}
}

// When nothing opened, she is asked for what would have let a venue fill it,
// even when a later venue failed for an unrelated reason.
func TestRouterOpen_NoneOpenedAsksForTheBuyerID(t *testing.T) {
	a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1400")}},
		openResults: []openResult{{err: WrapClass(ErrVenueUnavailable, services.ErrBuyerIDRequired)}}}
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1405")}},
		openResults: []openResult{{err: WrapClass(ErrVenueUnavailable, errors.New("502"))}}}
	_, err := testRouter(a, b).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, services.ErrBuyerIDRequired) || len(b.opened) != 1 {
		t.Fatalf("err = %v, b opened %v", err, b.opened)
	}
}

// Asking for the ID wins over price_moved: the re-quote would show the venue
// that needs it again, and she would confirm into the same refusal forever.
func TestRouterOpen_BuyerIDOutranksPriceMoved(t *testing.T) {
	a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1400")}},
		openResults: []openResult{{err: WrapClass(ErrVenueUnavailable, services.ErrBuyerIDRequired)}}}
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1405")}},
		openResults: []openResult{{err: services.ErrPriceMovedPastQuote}}}
	_, err := testRouter(a, b).Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
	var moved *PriceMovedError
	if errors.As(err, &moved) || !errors.Is(err, services.ErrBuyerIDRequired) || len(b.opened) != 1 {
		t.Fatalf("err = %v, b opened %v", err, b.opened)
	}
}

// Live 2026-11-04: Paycrest quoted 1400, needed the ID, and the pool filled at
// 1509 because the guard only logs. A buyer-ID failover never fills outside
// tolerance; she is asked for the ID instead.
func TestRouterOpen_BuyerIDFailoverStaysWithinTolerance(t *testing.T) {
	a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1400")}},
		openResults: []openResult{{err: WrapClass(ErrVenueUnavailable, services.ErrBuyerIDRequired)}}}
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1509")}},
		openResults: []openResult{{order: okOrder}}}
	r := testRouter(a, b)
	r.cfg.Guard = services.PriceGuardLog
	_, err := r.Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
	if !errors.Is(err, services.ErrBuyerIDRequired) || len(b.opened) != 0 {
		t.Fatalf("err = %v, b opened %v", err, b.opened)
	}
}

// Within tolerance it still fails over, and the venue binds under enforce.
func TestRouterOpen_BuyerIDFailoverBindsUnderEnforce(t *testing.T) {
	a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1400")}},
		openResults: []openResult{{err: WrapClass(ErrVenueUnavailable, services.ErrBuyerIDRequired)}}}
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1403")}},
		openResults: []openResult{{order: okOrder}}}
	r := testRouter(a, b)
	r.cfg.Guard = services.PriceGuardLog
	o, err := r.Open(context.Background(), openReq(EncodeHandle("a", VenueKey{"base", "USDC"}), "1400"))
	if err != nil || o != okOrder || len(b.openInputs) != 1 || b.openInputs[0].Guard != services.PriceGuardEnforce {
		t.Fatalf("o=%v err=%v inputs=%v", o, err, b.openInputs)
	}
}

// No handle: Paycrest is tried best-first inside the failover loop, and the
// worse venue after it is held to the shown rate all the same.
func TestRouterOpen_BuyerIDMidLoopStaysWithinTolerance(t *testing.T) {
	a := &fakeProvider{name: "a", quote: &ProviderQuote{Venues: []Venue{venue("a", "base", "USDC", "1400")}},
		openResults: []openResult{{err: WrapClass(ErrVenueUnavailable, services.ErrBuyerIDRequired)}}}
	b := &fakeProvider{name: "b", quote: &ProviderQuote{Venues: []Venue{venue("b", "base", "USDC", "1509")}},
		openResults: []openResult{{order: okOrder}}}
	r := testRouter(a, b)
	r.cfg.Guard = services.PriceGuardLog
	_, err := r.Open(context.Background(), openReq("", "1400"))
	if !errors.Is(err, services.ErrBuyerIDRequired) || len(a.opened) != 1 || len(b.opened) != 0 {
		t.Fatalf("err = %v, a opened %v, b opened %v", err, a.opened, b.opened)
	}
}
