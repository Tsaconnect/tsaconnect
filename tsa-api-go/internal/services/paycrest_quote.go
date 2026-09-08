package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// paycrest_quote.go — venue selection.
//
// Paycrest liquidity is not a fixed property of a pair. It belongs to
// individual providers who join and leave the book without notice, and who
// tier their pricing by order size. Measured over two consecutive days:
//
//	2026-09-03  USDT/base quoted +0.24% flat from $0.50 to $2,000
//	2026-09-04  USDT/base had NO buy provider at all; USDC/base quoted +1.12%
//	            at $0.58 but +0.25% at $364
//
// So a static "we support X on Y" list is a promise we cannot keep, and a
// cached rate is a price we cannot honour. Every quote therefore asks the API
// what is actually fillable AT THIS SIZE, and the order is created against
// that same answer.
//
// The user-visible consequence: the network picker only ever shows networks
// that can really fill the amount typed, so "no provider" is never discovered
// after the user has committed.

// PaycrestVenue is one fillable (token, network) with its live price.
type PaycrestVenue struct {
	// ChainKey is OUR key ("bsc"), not Paycrest's ("bnb-smart-chain") — the
	// app and blockchainService both speak this one.
	ChainKey string `json:"chain"`
	Network  string `json:"-"` // Paycrest's name, for the order payload
	Token    string `json:"token"`
	// Rate is fiat per token, as quoted.
	Rate string `json:"rate"`
	// SpreadPercent is Rate against the protocol's market reference. Positive
	// means the user pays above mid on a buy; negative means they receive
	// below mid on a sell. Surfaced so the app can show a real cost.
	SpreadPercent float64  `json:"spreadPercent"`
	ProviderIDs   []string `json:"-"`
	// TokenAmount is what the user receives (buy) or must send (sell).
	TokenAmount float64 `json:"tokenAmount"`
}

// PaycrestQuoteResult is the answer to "what can this user actually do with
// this amount right now".
type PaycrestQuoteResult struct {
	FiatCode   string          `json:"currency"`
	FiatAmount string          `json:"amount"`
	Side       string          `json:"side"` // "buy" (onramp) or "sell" (offramp)
	MarketRate float64         `json:"marketRate"`
	Venues     []PaycrestVenue `json:"venues"`
	// Best is Venues[0] when any exist — cheapest for the user.
	Best     *PaycrestVenue `json:"best"`
	QuotedAt time.Time      `json:"quotedAt"`
}

// ErrPaycrestNoVenue means nothing on the book can fill this size right now.
// Distinct from a transport failure: the user should be told to try a
// different amount, not that something broke.
var ErrPaycrestNoVenue = errors.New("paycrest: no network can fill this amount right now")

// ErrPaycrestBelowMinimum is the protocol floor, checked before we spend any
// API calls on an amount nobody will quote.
var ErrPaycrestBelowMinimum = errors.New("paycrest: amount is below the minimum order size")

// ErrPaycrestBookEmpty means the order book parsed successfully and contained
// nothing we can use. It is deliberately an ERROR rather than "no currency
// trades", because those two are indistinguishable in the data and wildly
// different in likelihood: every provider on the protocol going dark at once
// has never happened, while a renamed JSON field or an upstream blip produces
// exactly this and produces it silently.
//
// Callers that would otherwise read the empty result as "nothing trades" must
// treat this as "we could not find out" — see ITHandler.Currencies.
var ErrPaycrestBookEmpty = errors.New("paycrest: order book has no usable entries")

// PaycrestQuoter is the rate surface the quote service needs.
type PaycrestQuoter interface {
	// Markets is the whole order book in one fast call — the venue list comes
	// from here.
	Markets(ctx context.Context) (*PaycrestMarkets, error)
	// Rates is the authoritative per-order price. Slow and
	// concurrency-throttled upstream, so it is spent on ONE venue at order
	// time, never fanned out.
	Rates(ctx context.Context, network, token string, amount float64, fiat string) (*PaycrestRates, error)
	MarketRate(ctx context.Context, fiat, side string) (float64, error)
}

// PaycrestQuoteService prices and selects venues.
type PaycrestQuoteService struct {
	pc PaycrestQuoter
	// tokens we are willing to route, in preference order. USDC first: it has
	// consistently carried far deeper onramp liquidity than USDT on the NGN
	// corridor ($15,544 vs $0 on Base when last measured).
	tokens []string
	// chainKeys we will offer, in OUR vocabulary.
	chainKeys []string

	marketMu   sync.Mutex
	marketRate map[string]marketRateEntry

	bookMu sync.Mutex
	book   *PaycrestMarkets
	bookAt time.Time
}

type marketRateEntry struct {
	rate float64
	at   time.Time
}

// paycrestMarketRateTTL keeps the reference mid fresh enough to size a naira
// amount into token units without hammering the endpoint. It is NOT used to
// price an order — only the per-order Rates() call does that.
const paycrestMarketRateTTL = 60 * time.Second

// paycrestQuoteFanoutTimeout bounds the whole venue sweep. Generous enough for
// a slow upstream, short enough that a user is never left waiting on one
// unresponsive provider — the venues that answered are still a usable quote.
// paycrestBookTTL keeps the order book fresh enough to reflect a provider
// leaving without re-fetching it on every keystroke. Short, because the whole
// reason this service re-quotes constantly is that the book moves.
const paycrestBookTTL = 20 * time.Second

// NewPaycrestQuoteService returns nil when the client is unavailable so
// callers gate on `!= nil`, consistent with the rest of the package.
func NewPaycrestQuoteService(pc PaycrestQuoter, tokens, chainKeys []string) *PaycrestQuoteService {
	if pc == nil {
		return nil
	}
	if len(tokens) == 0 {
		tokens = []string{"USDC", "USDT"}
	}
	if len(chainKeys) == 0 {
		chainKeys = []string{"base", "polygon", "bsc", "arbitrum", "ethereum"}
	}
	return &PaycrestQuoteService{
		pc:         pc,
		tokens:     tokens,
		chainKeys:  chainKeys,
		marketRate: map[string]marketRateEntry{},
	}
}

// Quote asks every configured venue what it will do with this fiat amount and
// returns only the ones that answered, cheapest first.
//
// side is "buy" for an onramp (user spends fiat) or "sell" for an offramp
// (user receives fiat). fiatAmount is in major units ("1000" naira).
func (s *PaycrestQuoteService) Quote(ctx context.Context, side, fiatCode, fiatAmount string) (*PaycrestQuoteResult, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	side = strings.ToLower(strings.TrimSpace(side))
	if side != "buy" && side != "sell" {
		return nil, fmt.Errorf("paycrest quote: side must be buy or sell, got %q", side)
	}
	if fiatCode == "" {
		fiatCode = "NGN"
	}
	amt, err := strconv.ParseFloat(strings.TrimSpace(fiatAmount), 64)
	if err != nil || amt <= 0 {
		return nil, fmt.Errorf("paycrest quote: unusable amount %q", fiatAmount)
	}

	mkt, err := s.cachedMarketRate(ctx, fiatCode, side)
	if err != nil {
		return nil, err
	}

	// Rates() takes TOKEN units, so size the request off the reference mid.
	// This is an estimate used only to land in the right pricing tier; the
	// rate that binds is whatever comes back.
	tokenAmt := amt / mkt
	if tokenAmt < PaycrestMinOrderUSD {
		return nil, fmt.Errorf("%w: %.2f %s is about %.4f, minimum is %.2f",
			ErrPaycrestBelowMinimum, amt, fiatCode, tokenAmt, PaycrestMinOrderUSD)
	}

	res := &PaycrestQuoteResult{
		FiatCode:   strings.ToUpper(fiatCode),
		FiatAmount: fiatAmount,
		Side:       side,
		MarketRate: mkt,
		QuotedAt:   time.Now(),
	}

	book, err := s.cachedBook(ctx)
	if err != nil {
		return nil, err
	}

	// Which of OUR chains Paycrest serves, so the book can be filtered without
	// asking about chains that could never appear.
	networkFor := map[string]string{}
	for _, chainKey := range s.chainKeys {
		if network, ok := PaycrestNetwork(chainKey); ok {
			networkFor[network] = chainKey
		}
	}
	wanted := map[string]bool{}
	for _, t := range s.tokens {
		wanted[strings.ToUpper(t)] = true
	}

	// Best entry per (token, network). A venue can have several providers
	// quoting different bands; the user only cares about the best one that
	// covers their size.
	type key struct{ token, chainKey string }
	best := map[key]PaycrestBookEntry{}
	for _, e := range book.Book {
		if !strings.EqualFold(e.Fiat, fiatCode) || !strings.EqualFold(e.Side, side) {
			continue
		}
		token := strings.ToUpper(e.Token)
		if !wanted[token] {
			continue
		}
		chainKey, ok := networkFor[strings.ToLower(e.Network)]
		if !ok {
			continue
		}
		// The size band is the whole point of reading the book: an entry that
		// cannot fill this amount must never reach the picker, or the user
		// selects a route the order then refuses.
		min, err1 := strconv.ParseFloat(e.Min, 64)
		max, err2 := strconv.ParseFloat(e.Max, 64)
		if err1 != nil || err2 != nil || tokenAmt < min || tokenAmt > max {
			continue
		}
		rate, err := strconv.ParseFloat(e.Rate, 64)
		if err != nil || rate <= 0 {
			continue
		}
		k := key{token, chainKey}
		cur, seen := best[k]
		if !seen {
			best[k] = e
			continue
		}
		curRate, _ := strconv.ParseFloat(cur.Rate, 64)
		if (side == "buy" && rate < curRate) || (side == "sell" && rate > curRate) {
			best[k] = e
		}
	}

	for k, e := range best {
		rate, _ := strconv.ParseFloat(e.Rate, 64)
		network, _ := PaycrestNetwork(k.chainKey)
		res.Venues = append(res.Venues, PaycrestVenue{
			ChainKey:      k.chainKey,
			Network:       network,
			Token:         k.token,
			Rate:          e.Rate,
			SpreadPercent: (rate/mkt - 1) * 100,
			ProviderIDs:   []string{e.ProviderID},
			TokenAmount:   amt / rate,
		})
	}

	if len(res.Venues) == 0 {
		return nil, fmt.Errorf("%w (%s %s %s)", ErrPaycrestNoVenue, fiatAmount, res.FiatCode, side)
	}

	// Cheapest for the user first: on a buy that is the lowest fiat-per-token;
	// on a sell it is the highest.
	sort.SliceStable(res.Venues, func(i, j int) bool {
		a, _ := strconv.ParseFloat(res.Venues[i].Rate, 64)
		b, _ := strconv.ParseFloat(res.Venues[j].Rate, 64)
		if a != b {
			if side == "buy" {
				return a < b // fewer naira per token is better on a buy
			}
			return a > b // more naira per token is better on a sell
		}
		// Providers routinely quote one rate across every network, so without a
		// tie-break the concurrent fan-out would reorder the picker on each
		// keystroke. Token then chain keeps it stable.
		if res.Venues[i].Token != res.Venues[j].Token {
			return res.Venues[i].Token < res.Venues[j].Token
		}
		return res.Venues[i].ChainKey < res.Venues[j].ChainKey
	})
	res.Best = &res.Venues[0]
	return res, nil
}

// ─── Ramp legs ─────────────────────────────────────────────────────────────

// RampLegs says which directions a currency can actually trade in right now.
// Both false means the currency has a provider on paper but nothing on the
// book — the app must offer neither button.
type RampLegs struct {
	Buy  bool `json:"buy"`
	Sell bool `json:"sell"`
}

// RampLegs reports, per fiat, which sides have live liquidity on a venue we
// route.
//
// This is derived rather than declared on purpose. Direction is a property of
// provider liquidity, not of our product decisions: UGX and TZS have offramp
// providers only today, and the day someone starts quoting a UGX onramp we
// should offer it without shipping a migration. The registry stays the
// authority on WHO serves a currency; the book answers WHICH WAY.
//
// It rides the same 20s-TTL book the venue picker uses, so the currency list
// costs nothing extra: one shared /v2/markets call (~0.2s) serves every caller,
// and a later fetch failure serves the previous book rather than erroring. An
// error here therefore means the book has NEVER loaded — see the caller for
// what gets served then.
func (s *PaycrestQuoteService) RampLegs(ctx context.Context) (map[string]RampLegs, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	book, err := s.cachedBook(ctx)
	if err != nil {
		return nil, err
	}

	wanted := make(map[string]bool, len(s.tokens))
	for _, t := range s.tokens {
		wanted[strings.ToUpper(t)] = true
	}
	networkFor := make(map[string]string, len(s.chainKeys))
	for _, chainKey := range s.chainKeys {
		if network, ok := PaycrestNetwork(chainKey); ok {
			networkFor[network] = chainKey
		}
	}
	legs := legsByFiat(book, wanted, networkFor)
	if len(legs) == 0 {
		// Not "nothing trades anywhere" — that is not a state this protocol
		// reaches. An empty map here means the book was empty or shaped in a
		// way we no longer parse, and reporting it as fact would take every
		// currency (NGN included) dark on a single bad response.
		return nil, ErrPaycrestBookEmpty
	}
	return legs, nil
}

// legsByFiat is the pure core of RampLegs: group the book by fiat and side,
// counting only entries on a token and network we would actually route.
//
// Order-size bands are deliberately ignored. The currency list renders before
// the user has typed an amount, so applying a band here would mean inventing
// one — and a currency whose only liquidity is large would vanish from the
// picker even though it trades fine at that size. Quote() applies the band when
// there is a real amount to apply it to.
func legsByFiat(book *PaycrestMarkets, wantToken map[string]bool, networkFor map[string]string) map[string]RampLegs {
	out := map[string]RampLegs{}
	if book == nil {
		return out
	}
	for _, e := range book.Book {
		fiat := strings.ToUpper(strings.TrimSpace(e.Fiat))
		if fiat == "" {
			continue
		}
		if !wantToken[strings.ToUpper(strings.TrimSpace(e.Token))] {
			continue
		}
		if _, ok := networkFor[strings.ToLower(strings.TrimSpace(e.Network))]; !ok {
			continue
		}
		legs := out[fiat]
		switch strings.ToLower(strings.TrimSpace(e.Side)) {
		case "buy":
			legs.Buy = true
		case "sell":
			legs.Sell = true
		default:
			continue
		}
		out[fiat] = legs
	}
	return out
}

// VenueFor binds the price for one venue immediately before an order is
// created. This is where the single authoritative /v2/rates call is spent.
//
// The picker is built from the order book, which is fast but is a standing
// offer rather than a quote. Before money moves we ask the provider what it
// will actually do with THIS amount — the book can be seconds stale, and a
// venue that has since left must fail here rather than at Paycrest with the
// user already committed.
func (s *PaycrestQuoteService) VenueFor(ctx context.Context, side, fiatCode, fiatAmount, chainKey, token string) (*PaycrestVenue, error) {
	res, err := s.Quote(ctx, side, fiatCode, fiatAmount)
	if err != nil {
		return nil, err
	}
	var venue *PaycrestVenue
	for i := range res.Venues {
		if strings.EqualFold(res.Venues[i].ChainKey, chainKey) &&
			strings.EqualFold(res.Venues[i].Token, token) {
			venue = &res.Venues[i]
			break
		}
	}
	if venue == nil {
		return nil, fmt.Errorf("%w: %s on %s is no longer fillable at this size",
			ErrPaycrestNoVenue, strings.ToUpper(token), chainKey)
	}

	amt, err := strconv.ParseFloat(strings.TrimSpace(fiatAmount), 64)
	if err != nil || amt <= 0 {
		return nil, fmt.Errorf("paycrest venue: unusable amount %q", fiatAmount)
	}
	rates, err := s.pc.Rates(ctx, venue.Network, venue.Token, venue.TokenAmount, fiatCode)
	if err != nil {
		// The book said this venue was live and the provider now disagrees.
		// Surfacing it as "no venue" is right: the user should pick again
		// rather than be told something broke.
		if errors.Is(err, ErrPaycrestNoProvider) {
			return nil, fmt.Errorf("%w: %s on %s stopped quoting",
				ErrPaycrestNoVenue, strings.ToUpper(token), chainKey)
		}
		return nil, err
	}
	var q *PaycrestQuote
	if side == "buy" {
		q = rates.Buy
	} else {
		q = rates.Sell
	}
	if q == nil || strings.TrimSpace(q.Rate) == "" {
		return nil, fmt.Errorf("%w: %s on %s has no %s side right now",
			ErrPaycrestNoVenue, strings.ToUpper(token), chainKey, side)
	}
	rate, err := strconv.ParseFloat(q.Rate, 64)
	if err != nil || rate <= 0 {
		return nil, fmt.Errorf("paycrest venue: unusable rate %q", q.Rate)
	}

	// Bind to what the provider just said, not to the book.
	bound := *venue
	bound.Rate = q.Rate
	bound.TokenAmount = amt / rate
	bound.SpreadPercent = (rate/res.MarketRate - 1) * 100
	if len(q.ProviderIDs) > 0 {
		bound.ProviderIDs = q.ProviderIDs
	}
	return &bound, nil
}

func (s *PaycrestQuoteService) cachedMarketRate(ctx context.Context, fiat, side string) (float64, error) {
	key := strings.ToUpper(fiat) + ":" + side
	s.marketMu.Lock()
	if e, ok := s.marketRate[key]; ok && time.Since(e.at) < paycrestMarketRateTTL {
		s.marketMu.Unlock()
		return e.rate, nil
	}
	s.marketMu.Unlock()

	rate, err := s.pc.MarketRate(ctx, fiat, side)
	if err != nil {
		return 0, err
	}
	s.marketMu.Lock()
	s.marketRate[key] = marketRateEntry{rate: rate, at: time.Now()}
	s.marketMu.Unlock()
	return rate, nil
}

// cachedBook fetches the order book at most once per TTL. The book is public
// and identical for every caller, so one fetch serves every concurrent quote.
func (s *PaycrestQuoteService) cachedBook(ctx context.Context) (*PaycrestMarkets, error) {
	s.bookMu.Lock()
	if s.book != nil && time.Since(s.bookAt) < paycrestBookTTL {
		b := s.book
		s.bookMu.Unlock()
		return b, nil
	}
	s.bookMu.Unlock()

	b, err := s.pc.Markets(ctx)
	if err != nil {
		// Serve a stale book rather than denying every quote: a book from a
		// minute ago is far more useful than an error, and the order still
		// re-prices against /v2/rates before anything is created.
		s.bookMu.Lock()
		stale := s.book
		s.bookMu.Unlock()
		if stale != nil {
			return stale, nil
		}
		return nil, err
	}
	s.bookMu.Lock()
	s.book, s.bookAt = b, time.Now()
	s.bookMu.Unlock()
	return b, nil
}
