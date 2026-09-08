package services

import (
	"context"
	"errors"
	"testing"
)

// The shape here mirrors a real /v2/markets response measured 2026-09-08:
// NGN and KES quote both sides, UGX and TZS are offramp-only, and KES onramp
// liquidity sits on a subset of venues rather than all of them.
func rampLegsBook() *PaycrestMarkets {
	return &PaycrestMarkets{
		AsOf: "2026-09-08T22:25:07Z",
		Book: []PaycrestBookEntry{
			{Fiat: "NGN", Side: "buy", Token: "USDC", Network: "base", Rate: "1380.04", Min: "0.5", Max: "2000"},
			{Fiat: "NGN", Side: "sell", Token: "USDT", Network: "polygon", Rate: "1363.40", Min: "0.5", Max: "2000"},
			{Fiat: "KES", Side: "buy", Token: "USDC", Network: "base", Rate: "130.57", Min: "0.5", Max: "2000"},
			{Fiat: "KES", Side: "sell", Token: "USDT", Network: "polygon", Rate: "128.57", Min: "0.5", Max: "2000"},
			{Fiat: "UGX", Side: "sell", Token: "USDC", Network: "base", Rate: "3753.68", Min: "0.5", Max: "2000"},
			{Fiat: "TZS", Side: "sell", Token: "USDT", Network: "polygon", Rate: "2609.01", Min: "0.5", Max: "2000"},
		},
	}
}

func TestLegsByFiat_ReportsEachSideSeparately(t *testing.T) {
	legs := legsByFiat(rampLegsBook(),
		map[string]bool{"USDC": true, "USDT": true},
		map[string]string{"base": "base", "polygon": "polygon"})

	want := map[string]RampLegs{
		"NGN": {Buy: true, Sell: true},
		"KES": {Buy: true, Sell: true},
		"UGX": {Buy: false, Sell: true},
		"TZS": {Buy: false, Sell: true},
	}
	for fiat, w := range want {
		if got := legs[fiat]; got != w {
			t.Errorf("%s legs = %+v, want %+v", fiat, got, w)
		}
	}
	// A currency absent from the book has no legs, rather than defaulting to
	// "tradable" — that default is exactly how a dead route gets advertised.
	if got := legs["GHS"]; got.Buy || got.Sell {
		t.Errorf("GHS legs = %+v, want none — it is not on the book at all", got)
	}
}

// A leg is only real if it sits on a venue we would actually route. Otherwise
// the app offers Buy, the user types an amount, and Quote finds no venue —
// the same after-the-fact failure this whole mechanism exists to prevent.
func TestLegsByFiat_IgnoresVenuesWeDoNotRoute(t *testing.T) {
	book := &PaycrestMarkets{Book: []PaycrestBookEntry{
		// Right currency and side, but a token we do not offer.
		{Fiat: "KES", Side: "buy", Token: "DAI", Network: "base", Rate: "130", Min: "0.5", Max: "2000"},
		// Right currency and side, but a chain we do not route.
		{Fiat: "KES", Side: "sell", Token: "USDC", Network: "tron", Rate: "128", Min: "0.5", Max: "2000"},
	}}

	legs := legsByFiat(book,
		map[string]bool{"USDC": true, "USDT": true},
		map[string]string{"base": "base", "polygon": "polygon"})

	if got := legs["KES"]; got.Buy || got.Sell {
		t.Errorf("KES legs = %+v, want none — neither entry is on a venue we route", got)
	}
}

// Deliberately NOT filtered by order size: the currency list is rendered before
// the user has typed an amount, so a band check here would either need a
// made-up amount or would hide a currency that trades fine at another size.
// Quote() still enforces the band when there is a real amount.
func TestLegsByFiat_IgnoresOrderSizeBands(t *testing.T) {
	book := &PaycrestMarkets{Book: []PaycrestBookEntry{
		{Fiat: "KES", Side: "buy", Token: "USDC", Network: "base", Rate: "130", Min: "1000", Max: "2000"},
	}}

	legs := legsByFiat(book,
		map[string]bool{"USDC": true},
		map[string]string{"base": "base"})

	if !legs["KES"].Buy {
		t.Error("KES buy = false; a large-only band is still a live leg at list time")
	}
}

type legsQuoter struct {
	book *PaycrestMarkets
	err  error
	// calls counts Markets() hits so the cache can be observed.
	calls int
}

func (q *legsQuoter) Markets(context.Context) (*PaycrestMarkets, error) {
	q.calls++
	if q.err != nil {
		return nil, q.err
	}
	return q.book, nil
}
func (q *legsQuoter) Rates(context.Context, string, string, float64, string) (*PaycrestRates, error) {
	return nil, errors.New("not used")
}
func (q *legsQuoter) MarketRate(context.Context, string, string) (float64, error) {
	return 0, errors.New("not used")
}

func TestRampLegs_ReadsTheCachedBook(t *testing.T) {
	q := &legsQuoter{book: rampLegsBook()}
	s := NewPaycrestQuoteService(q, []string{"USDC", "USDT"}, []string{"base", "polygon"})

	first, err := s.RampLegs(context.Background())
	if err != nil {
		t.Fatalf("RampLegs: %v", err)
	}
	if !first["KES"].Buy || !first["UGX"].Sell || first["UGX"].Buy {
		t.Fatalf("legs = %+v, want KES buyable and UGX sell-only", first)
	}

	// Second call inside the TTL must not re-fetch: this runs on a list
	// endpoint that the app hits on every focus.
	if _, err := s.RampLegs(context.Background()); err != nil {
		t.Fatalf("RampLegs (cached): %v", err)
	}
	if q.calls != 1 {
		t.Errorf("Markets called %d times, want 1 — the book cache should absorb the second call", q.calls)
	}
}

// The only genuinely new failure mode: the book has never loaded and Paycrest
// is unreachable. RampLegs reports the error rather than inventing legs; the
// CALLER decides what to serve, and the handler's own test pins that choice.
func TestRampLegs_ErrorsWhenTheBookHasNeverLoaded(t *testing.T) {
	q := &legsQuoter{err: errors.New("paycrest unreachable")}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})

	if _, err := s.RampLegs(context.Background()); err == nil {
		t.Fatal("RampLegs = nil error; a never-loaded book must not read as 'no legs anywhere'")
	}
}

// Once a book HAS loaded, a later fetch failure serves the stale one — legs
// from a minute ago are far better than hiding every ramp in the app.
func TestRampLegs_ServesStaleBookOnLaterFailure(t *testing.T) {
	q := &legsQuoter{book: rampLegsBook()}
	s := NewPaycrestQuoteService(q, []string{"USDC", "USDT"}, []string{"base", "polygon"})

	if _, err := s.RampLegs(context.Background()); err != nil {
		t.Fatalf("warm: %v", err)
	}
	// Expire the cache, then break the upstream.
	s.bookMu.Lock()
	s.bookAt = s.bookAt.Add(-2 * paycrestBookTTL)
	s.bookMu.Unlock()
	q.err = errors.New("paycrest unreachable")

	legs, err := s.RampLegs(context.Background())
	if err != nil {
		t.Fatalf("stale: %v", err)
	}
	if !legs["KES"].Buy {
		t.Error("KES buy lost on a stale read; the previous book should still answer")
	}
}

// The regression that takes production dark: a book that parses fine and
// contains nothing. Reported as "no legs" it reads as "no currency trades",
// and every row on the IT hub — NGN included — goes to "paused".
//
// It does not need Paycrest to be down. A renamed JSON field unmarshals to an
// empty slice with no error, gets cached, and re-fetches into the same state.
func TestRampLegs_EmptyBookIsUnknownNotClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		book *PaycrestMarkets
	}{
		{"no entries", &PaycrestMarkets{Book: []PaycrestBookEntry{}}},
		{"nil slice, as a renamed field would unmarshal", &PaycrestMarkets{}},
		{"entries we cannot route", &PaycrestMarkets{Book: []PaycrestBookEntry{
			{Fiat: "NGN", Side: "buy", Token: "DAI", Network: "tron", Rate: "1", Min: "1", Max: "2"},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewPaycrestQuoteService(&legsQuoter{book: tc.book},
				[]string{"USDC", "USDT"}, []string{"base", "polygon"})

			legs, err := s.RampLegs(context.Background())
			if !errors.Is(err, ErrPaycrestBookEmpty) {
				t.Fatalf("err = %v, want ErrPaycrestBookEmpty — an empty book must read as "+
					"'we could not find out', never as 'nothing trades'", err)
			}
			if legs != nil {
				t.Errorf("legs = %+v, want nil so the caller cannot mistake it for an answer", legs)
			}
		})
	}
}

// The distinction that makes the guard above safe: one currency missing from a
// POPULATED book is a real state — its providers went offline — and must still
// report as closed rather than being swept into "unknown".
func TestRampLegs_PopulatedBookStillClosesAMissingCurrency(t *testing.T) {
	book := &PaycrestMarkets{Book: []PaycrestBookEntry{
		{Fiat: "NGN", Side: "buy", Token: "USDC", Network: "base", Rate: "1380", Min: "0.5", Max: "2000"},
	}}
	s := NewPaycrestQuoteService(&legsQuoter{book: book},
		[]string{"USDC", "USDT"}, []string{"base", "polygon"})

	legs, err := s.RampLegs(context.Background())
	if err != nil {
		t.Fatalf("RampLegs: %v", err)
	}
	if !legs["NGN"].Buy {
		t.Error("NGN buy lost")
	}
	if got := legs["KES"]; got.Buy || got.Sell {
		t.Errorf("KES legs = %+v, want none — absent from a live book means its providers left", got)
	}
}

// ─── Order-time wording ────────────────────────────────────────────────────

// "No venue" has two very different meanings and the user can only act on one
// of them. Trying a different amount fixes a thin book; nothing the user does
// will conjure a UGX onramp.
func TestExplainMissingVenue_SeparatesADeadDirectionFromAThinBook(t *testing.T) {
	quotes := NewPaycrestQuoteService(&legsQuoter{book: rampLegsBook()},
		[]string{"USDC", "USDT"}, []string{"base", "polygon"})
	svc := &PaycrestOrderService{quotes: quotes}
	ctx := context.Background()

	// UGX sells but never buys: say so plainly instead of talking about venues.
	err := svc.explainMissingVenue(ctx, "buy", "UGX", ErrPaycrestNoVenue)
	if got, want := err.Error(), "Buying is not available in UGX right now"; got != want {
		t.Errorf("UGX buy error = %q, want %q", got, want)
	}

	// KES buys fine — this really is an amount the book cannot fill, so the
	// original error (and its "try a different amount" meaning) must survive.
	err = svc.explainMissingVenue(ctx, "buy", "KES", ErrPaycrestNoVenue)
	if !errors.Is(err, ErrPaycrestNoVenue) {
		t.Errorf("KES buy error = %v, want the original ErrPaycrestNoVenue", err)
	}

	// The sell direction of a sell-only currency is likewise a size problem.
	err = svc.explainMissingVenue(ctx, "sell", "UGX", ErrPaycrestNoVenue)
	if !errors.Is(err, ErrPaycrestNoVenue) {
		t.Errorf("UGX sell error = %v, want the original ErrPaycrestNoVenue", err)
	}

	// Anything that is not a venue problem passes through untouched: a
	// transport failure must not be relabelled as a closed direction.
	boom := errors.New("paycrest: 502")
	if got := svc.explainMissingVenue(ctx, "buy", "UGX", boom); got != boom {
		t.Errorf("transport error = %v, want it returned unchanged", got)
	}
}

// With the book unreadable we cannot tell the two cases apart, so the original
// error stands rather than a guess about which direction exists.
func TestExplainMissingVenue_KeepsTheOriginalWhenLegsAreUnknown(t *testing.T) {
	quotes := NewPaycrestQuoteService(&legsQuoter{err: errors.New("unreachable")},
		[]string{"USDC"}, []string{"base"})
	svc := &PaycrestOrderService{quotes: quotes}

	err := svc.explainMissingVenue(context.Background(), "buy", "UGX", ErrPaycrestNoVenue)
	if !errors.Is(err, ErrPaycrestNoVenue) {
		t.Errorf("error = %v, want the original ErrPaycrestNoVenue", err)
	}
}
