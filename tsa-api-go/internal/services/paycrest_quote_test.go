package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// stubQuoter answers per (network, token) so a test can model a real book:
// some venues quote, some have no provider, some are one-sided.
type stubQuoter struct {
	mu     sync.Mutex
	market float64
	// rates keyed "network|TOKEN"; a missing key means no provider.
	rates      map[string]*PaycrestRates
	rateCalls  int
	marketCall int
	marketErr  error
	transient  map[string]bool // venues that fail with a transport error
}

func (s *stubQuoter) Rates(_ context.Context, network, token string, _ float64, _ string) (*PaycrestRates, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rateCalls++
	key := network + "|" + strings.ToUpper(token)
	if s.transient[key] {
		return nil, errors.New("upstream timeout")
	}
	r, ok := s.rates[key]
	if !ok {
		return nil, fmt.Errorf("%w (%s)", ErrPaycrestNoProvider, key)
	}
	return r, nil
}

func (s *stubQuoter) MarketRate(context.Context, string, string) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marketCall++
	if s.marketErr != nil {
		return 0, s.marketErr
	}
	return s.market, nil
}

func buy(rate string, providers ...string) *PaycrestRates {
	return &PaycrestRates{Buy: &PaycrestQuote{Rate: rate, ProviderIDs: providers}}
}
func sell(rate string) *PaycrestRates {
	return &PaycrestRates{Sell: &PaycrestQuote{Rate: rate}}
}

func TestPaycrestQuote_BelowMinimumIsRefusedBeforeAnyRateCall(t *testing.T) {
	q := &stubQuoter{market: 1377.91, rates: map[string]*PaycrestRates{}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})

	// NGN 500 is about $0.36 — under the $0.50 protocol floor.
	_, err := s.Quote(context.Background(), "buy", "NGN", "500")
	if !errors.Is(err, ErrPaycrestBelowMinimum) {
		t.Fatalf("want ErrPaycrestBelowMinimum, got %v", err)
	}
	if q.rateCalls != 0 {
		t.Errorf("spent %d rate calls on an amount nobody would quote", q.rateCalls)
	}
}

func TestPaycrestQuote_BuyRanksCheapestFirst(t *testing.T) {
	q := &stubQuoter{market: 1377.91, rates: map[string]*PaycrestRates{
		"base|USDC":            buy("1390.64", "FZvfAEyg"),
		"polygon|USDC":         buy("1381.22"),
		"bnb-smart-chain|USDC": buy("1393.23"),
	}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base", "polygon", "bsc"})

	res, err := s.Quote(context.Background(), "buy", "NGN", "800")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if len(res.Venues) != 3 {
		t.Fatalf("want 3 venues, got %d", len(res.Venues))
	}
	// On a buy, fewer naira per token is better for the user.
	if res.Best.ChainKey != "polygon" || res.Best.Rate != "1381.22" {
		t.Fatalf("best = %s@%s, want polygon@1381.22", res.Best.ChainKey, res.Best.Rate)
	}
	if res.Venues[2].Rate != "1393.23" {
		t.Errorf("worst venue should sort last, got %s", res.Venues[2].Rate)
	}
	// Spread is what the app shows as the real cost.
	if got := res.Best.SpreadPercent; got < 0.2 || got > 0.3 {
		t.Errorf("spread %.3f%% does not match 1381.22 against mid 1377.91", got)
	}
	// The user gets the token amount at the quoted rate, not at mid.
	if want := 800 / 1381.22; res.Best.TokenAmount < want-1e-9 || res.Best.TokenAmount > want+1e-9 {
		t.Errorf("tokenAmount %.6f, want %.6f", res.Best.TokenAmount, want)
	}
}

func TestPaycrestQuote_SellRanksHighestFirst(t *testing.T) {
	q := &stubQuoter{market: 1377.98, rates: map[string]*PaycrestRates{
		"base|USDC":    sell("1365.65"),
		"polygon|USDC": sell("1370.15"),
	}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base", "polygon"})

	res, err := s.Quote(context.Background(), "sell", "NGN", "50000")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	// On a sell, MORE naira per token is better for the user.
	if res.Best.Rate != "1370.15" {
		t.Fatalf("best sell rate = %s, want 1370.15", res.Best.Rate)
	}
	if res.Best.SpreadPercent > 0 {
		t.Errorf("a sell below mid should read as a negative spread, got %.3f", res.Best.SpreadPercent)
	}
}

// The failure mode measured on 2026-09-04: USDT/base had no buy provider while
// USDC/base did. A venue that cannot fill must vanish from the picker, not
// fail the whole quote.
func TestPaycrestQuote_SkipsVenuesWithNoProvider(t *testing.T) {
	q := &stubQuoter{market: 1377.91, rates: map[string]*PaycrestRates{
		"base|USDC": buy("1390.64"),
		// USDT/base intentionally absent -> ErrPaycrestNoProvider
	}}
	s := NewPaycrestQuoteService(q, []string{"USDC", "USDT"}, []string{"base"})

	res, err := s.Quote(context.Background(), "buy", "NGN", "800")
	if err != nil {
		t.Fatalf("one dead venue should not fail the quote: %v", err)
	}
	if len(res.Venues) != 1 || res.Venues[0].Token != "USDC" {
		t.Fatalf("venues = %#v, want only USDC", res.Venues)
	}
}

// A one-sided book (sell quoted, buy absent) is how "onramp unavailable here"
// presents, and must be treated the same as no provider.
func TestPaycrestQuote_SkipsOneSidedBook(t *testing.T) {
	q := &stubQuoter{market: 1377.91, rates: map[string]*PaycrestRates{
		"base|USDT":    sell("1365.65"), // sell only
		"polygon|USDT": buy("1381.22"),
	}}
	s := NewPaycrestQuoteService(q, []string{"USDT"}, []string{"base", "polygon"})

	res, err := s.Quote(context.Background(), "buy", "NGN", "800")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if len(res.Venues) != 1 || res.Venues[0].ChainKey != "polygon" {
		t.Fatalf("venues = %#v, want only polygon", res.Venues)
	}
}

func TestPaycrestQuote_TransportFailureOnOneVenueDoesNotDenyTheRest(t *testing.T) {
	q := &stubQuoter{
		market:    1377.91,
		rates:     map[string]*PaycrestRates{"polygon|USDC": buy("1381.22")},
		transient: map[string]bool{"base|USDC": true},
	}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base", "polygon"})

	res, err := s.Quote(context.Background(), "buy", "NGN", "800")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if len(res.Venues) != 1 || res.Venues[0].ChainKey != "polygon" {
		t.Fatalf("venues = %#v, want polygon to survive base failing", res.Venues)
	}
}

func TestPaycrestQuote_NoVenueIsItsOwnSentinel(t *testing.T) {
	q := &stubQuoter{market: 1377.91, rates: map[string]*PaycrestRates{}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})

	_, err := s.Quote(context.Background(), "buy", "NGN", "800")
	if !errors.Is(err, ErrPaycrestNoVenue) {
		t.Fatalf("want ErrPaycrestNoVenue so the app can say 'try another amount', got %v", err)
	}
}

// Sonic is ours and Paycrest does not serve it; asking anyway would be quoted
// as "no provider" and read as a liquidity problem.
func TestPaycrestQuote_NeverAsksAboutUnsupportedChains(t *testing.T) {
	q := &stubQuoter{market: 1377.91, rates: map[string]*PaycrestRates{"base|USDC": buy("1390.64")}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"sonic", "optimism", "base"})

	res, err := s.Quote(context.Background(), "buy", "NGN", "800")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if q.rateCalls != 1 {
		t.Errorf("made %d rate calls, want 1 — sonic and optimism should never be asked", q.rateCalls)
	}
	if len(res.Venues) != 1 {
		t.Fatalf("venues = %#v", res.Venues)
	}
}

func TestPaycrestQuote_MarketRateIsCachedAcrossQuotes(t *testing.T) {
	q := &stubQuoter{market: 1377.91, rates: map[string]*PaycrestRates{"base|USDC": buy("1390.64")}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})

	for i := 0; i < 4; i++ {
		if _, err := s.Quote(context.Background(), "buy", "NGN", "800"); err != nil {
			t.Fatalf("Quote %d: %v", i, err)
		}
	}
	if q.marketCall != 1 {
		t.Errorf("market rate fetched %d times, want 1 (cached)", q.marketCall)
	}
	// The per-order rate must NOT be cached — it is what binds the order.
	if q.rateCalls != 4 {
		t.Errorf("rate calls = %d, want 4: a quote must re-price every time", q.rateCalls)
	}
}

func TestPaycrestQuote_RejectsBadInput(t *testing.T) {
	q := &stubQuoter{market: 1377.91, rates: map[string]*PaycrestRates{}}
	s := NewPaycrestQuoteService(q, nil, nil)

	for _, tc := range []struct{ side, amount string }{
		{"sideways", "1000"},
		{"buy", "0"},
		{"buy", "-100"},
		{"buy", "abc"},
		{"buy", ""},
	} {
		if _, err := s.Quote(context.Background(), tc.side, "NGN", tc.amount); err == nil {
			t.Errorf("Quote(side=%q amount=%q) should have failed", tc.side, tc.amount)
		}
	}
	var nilSvc *PaycrestQuoteService
	if _, err := nilSvc.Quote(context.Background(), "buy", "NGN", "1000"); !errors.Is(err, ErrPaycrestDisabled) {
		t.Errorf("nil service should report disabled, got %v", err)
	}
}

// VenueFor re-prices immediately before the order is created, so a stale
// picker selection cannot bind a rate the order will not carry.
func TestPaycrestVenueFor_RefusesAVenueThatHasGoneAway(t *testing.T) {
	q := &stubQuoter{market: 1377.91, rates: map[string]*PaycrestRates{"base|USDC": buy("1390.64")}}
	s := NewPaycrestQuoteService(q, []string{"USDC", "USDT"}, []string{"base", "polygon"})

	v, err := s.VenueFor(context.Background(), "buy", "NGN", "800", "base", "USDC")
	if err != nil {
		t.Fatalf("VenueFor: %v", err)
	}
	if v.Rate != "1390.64" {
		t.Errorf("rate = %s, want the freshly re-quoted 1390.64", v.Rate)
	}
	// The user sat on a picker showing USDT/polygon; it is no longer fillable.
	if _, err := s.VenueFor(context.Background(), "buy", "NGN", "800", "polygon", "USDT"); !errors.Is(err, ErrPaycrestNoVenue) {
		t.Fatalf("want ErrPaycrestNoVenue for a vanished venue, got %v", err)
	}
}
