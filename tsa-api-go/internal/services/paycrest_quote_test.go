package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubQuoter models both halves of the real thing: a fast order book that the
// picker is built from, and the slow per-order rate call that binds a price.
type stubQuoter struct {
	mu     sync.Mutex
	market float64
	// rates keyed "network|TOKEN"; a missing key means no provider.
	rates        map[string]*PaycrestRates
	rateCalls    int
	marketCall   int
	marketErr    error
	transient    map[string]bool // venues that fail with a transport error
	delay        time.Duration   // simulated upstream latency
	marketsCalls int
	marketsErr   error
	bookOverride []PaycrestBookEntry
}

// entries turns the rates fixture into a book, so existing tests keep
// expressing "these venues quote at these prices" without restating it twice.
func (s *stubQuoter) Markets(context.Context) (*PaycrestMarkets, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marketsCalls++
	if s.marketsErr != nil {
		return nil, s.marketsErr
	}
	if s.bookOverride != nil {
		return &PaycrestMarkets{Book: s.bookOverride}, nil
	}
	var book []PaycrestBookEntry
	for k, r := range s.rates {
		parts := strings.SplitN(k, "|", 2)
		network, token := parts[0], parts[1]
		if r.Buy != nil {
			book = append(book, PaycrestBookEntry{
				ProviderID: firstProvider(r.Buy), Side: "buy", Token: token, Fiat: "NGN",
				Network: network, Rate: r.Buy.Rate, Min: "0.5", Max: "100000",
			})
		}
		if r.Sell != nil {
			book = append(book, PaycrestBookEntry{
				ProviderID: firstProvider(r.Sell), Side: "sell", Token: token, Fiat: "NGN",
				Network: network, Rate: r.Sell.Rate, Min: "0.5", Max: "100000",
			})
		}
	}
	return &PaycrestMarkets{Book: book}, nil
}

func firstProvider(q *PaycrestQuote) string {
	if len(q.ProviderIDs) > 0 {
		return q.ProviderIDs[0]
	}
	return "stub"
}

func (s *stubQuoter) Rates(_ context.Context, network, token string, _ float64, _ string) (*PaycrestRates, error) {
	if s.delay > 0 {
		// Outside the lock, or the "concurrent" test would serialise on it and
		// pass even when the caller is sequential.
		time.Sleep(s.delay)
	}
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
	// Spread is what the app shows as the real cost: on a buy this small,
	// Paycrest's network fee included.
	if want := (800/res.Best.TokenAmount/1377.91 - 1) * 100; res.Best.SpreadPercent < want-0.01 || res.Best.SpreadPercent > want+0.01 {
		t.Errorf("spread %.3f%%, want %.3f%% (all-in cost against mid 1377.91)", res.Best.SpreadPercent, want)
	}
	// The user gets the token amount at the quoted rate, not at mid — less
	// Paycrest's network fee, which with no take of ours is always charged.
	if want := 800/1381.22 - paycrestOnrampNetworkFee; res.Best.TokenAmount < want-1e-9 || res.Best.TokenAmount > want+1e-9 {
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
	// Chains Paycrest does not serve must not surface as venues at all. Asking
	// anyway would come back "no provider" and read as a liquidity problem
	// rather than the configuration fact it is.
	if len(res.Venues) != 1 || res.Venues[0].ChainKey != "base" {
		t.Fatalf("venues = %#v, want only base", res.Venues)
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
	// Quoting must not touch the throttled per-order endpoint at all.
	if q.rateCalls != 0 {
		t.Errorf("rate calls = %d, want 0 from quoting alone", q.rateCalls)
	}
}

// The reason the picker reads the book instead of fanning out: measured from
// production, one /v2/rates call takes 1.8-5.4s and ten fired concurrently take
// 28-35s EACH — Paycrest throttles them. /v2/markets returns the whole book in
// about 0.2s. A quote must therefore cost ONE upstream call regardless of how
// many venues it covers.
func TestPaycrestQuote_CostsOneUpstreamCallRegardlessOfVenueCount(t *testing.T) {
	rates := map[string]*PaycrestRates{}
	for _, tok := range []string{"USDC", "USDT"} {
		for _, net := range []string{"base", "polygon", "bnb-smart-chain", "arbitrum-one", "ethereum"} {
			rates[net+"|"+tok] = buy("1390.64")
		}
	}
	q := &stubQuoter{market: 1377.91, rates: rates}
	s := NewPaycrestQuoteService(q, []string{"USDC", "USDT"},
		[]string{"base", "polygon", "bsc", "arbitrum", "ethereum"})

	res, err := s.Quote(context.Background(), "buy", "NGN", "800")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if len(res.Venues) != 10 {
		t.Fatalf("want 10 venues, got %d", len(res.Venues))
	}
	if q.marketsCalls != 1 {
		t.Errorf("book fetched %d times, want 1", q.marketsCalls)
	}
	if q.rateCalls != 0 {
		t.Errorf("quote spent %d rate calls; the picker must cost none", q.rateCalls)
	}
}

// A venue whose size band excludes this order must never reach the picker —
// otherwise the user selects a route the order then refuses.
func TestPaycrestQuote_ExcludesVenuesOutsideTheirSizeBand(t *testing.T) {
	q := &stubQuoter{market: 1377.91, bookOverride: []PaycrestBookEntry{
		{ProviderID: "small", Side: "buy", Token: "USDC", Fiat: "NGN", Network: "base",
			Rate: "1390.64", Min: "0.5", Max: "2"},
		{ProviderID: "big", Side: "buy", Token: "USDC", Fiat: "NGN", Network: "polygon",
			Rate: "1381.22", Min: "100", Max: "2000"},
	}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base", "polygon"})

	// NGN 800 is about 0.58 tokens: inside base's band, far below polygon's.
	res, err := s.Quote(context.Background(), "buy", "NGN", "800")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if len(res.Venues) != 1 || res.Venues[0].ChainKey != "base" {
		t.Fatalf("venues = %#v, want only base", res.Venues)
	}
}

// A stale book beats no quote at all, and the order re-prices anyway.
func TestPaycrestQuote_ServesAStaleBookWhenTheFetchFails(t *testing.T) {
	q := &stubQuoter{market: 1377.91, rates: map[string]*PaycrestRates{"base|USDC": buy("1390.64")}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})
	if _, err := s.Quote(context.Background(), "buy", "NGN", "800"); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	s.bookAt = time.Now().Add(-time.Hour) // force a refetch
	q.marketsErr = errors.New("upstream down")

	res, err := s.Quote(context.Background(), "buy", "NGN", "800")
	if err != nil {
		t.Fatalf("a failed refresh should fall back to the cached book, got %v", err)
	}
	if len(res.Venues) != 1 {
		t.Fatalf("venues = %#v", res.Venues)
	}
}

// Providers routinely quote one rate across every network. Without a tie-break
// the concurrent fan-out would reorder the picker between keystrokes.
func TestPaycrestQuote_OrderIsStableWhenRatesTie(t *testing.T) {
	rates := map[string]*PaycrestRates{}
	for _, net := range []string{"base", "polygon", "bnb-smart-chain"} {
		rates[net+"|USDC"] = buy("1390.64")
		rates[net+"|USDT"] = buy("1390.64")
	}
	q := &stubQuoter{market: 1377.91, rates: rates}
	s := NewPaycrestQuoteService(q, []string{"USDC", "USDT"}, []string{"base", "polygon", "bsc"})

	var first []string
	for i := 0; i < 8; i++ {
		res, err := s.Quote(context.Background(), "buy", "NGN", "800")
		if err != nil {
			t.Fatalf("Quote: %v", err)
		}
		got := make([]string, len(res.Venues))
		for j, v := range res.Venues {
			got[j] = v.Token + ":" + v.ChainKey
		}
		if first == nil {
			first = got
			continue
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d reordered: %v vs %v", i, got, first)
			}
		}
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
	// Exactly one authoritative call, spent on the chosen venue only.
	if q.rateCalls != 1 {
		t.Errorf("rate calls = %d, want exactly 1", q.rateCalls)
	}
	// The user sat on a picker showing USDT/polygon; it is no longer fillable.
	if _, err := s.VenueFor(context.Background(), "buy", "NGN", "800", "polygon", "USDT"); !errors.Is(err, ErrPaycrestNoVenue) {
		t.Fatalf("want ErrPaycrestNoVenue for a vanished venue, got %v", err)
	}
}
