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

// PaycrestQuoter is the rate surface the quote service needs.
type PaycrestQuoter interface {
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
}

type marketRateEntry struct {
	rate float64
	at   time.Time
}

// paycrestMarketRateTTL keeps the reference mid fresh enough to size a naira
// amount into token units without hammering the endpoint. It is NOT used to
// price an order — only the per-order Rates() call does that.
const paycrestMarketRateTTL = 60 * time.Second

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

	for _, token := range s.tokens {
		for _, chainKey := range s.chainKeys {
			network, ok := PaycrestNetwork(chainKey)
			if !ok {
				// A chain we serve that Paycrest does not. Skipping silently is
				// correct: it is a configuration fact, not a live condition.
				continue
			}
			rates, err := s.pc.Rates(ctx, network, token, tokenAmt, fiatCode)
			if err != nil {
				// "No provider" is the single most common answer here and is
				// not an error worth failing the whole quote over — the point
				// of fanning out is that some venues will decline.
				if errors.Is(err, ErrPaycrestNoProvider) {
					continue
				}
				// A transport failure on one venue likewise should not deny the
				// user the venues that did answer.
				continue
			}
			var q *PaycrestQuote
			if side == "buy" {
				q = rates.Buy
			} else {
				q = rates.Sell
			}
			if q == nil || strings.TrimSpace(q.Rate) == "" {
				continue // one-sided book: this direction is unavailable here
			}
			rate, err := strconv.ParseFloat(q.Rate, 64)
			if err != nil || rate <= 0 {
				continue
			}
			v := PaycrestVenue{
				ChainKey:      chainKey,
				Network:       network,
				Token:         token,
				Rate:          q.Rate,
				SpreadPercent: (rate/mkt - 1) * 100,
				ProviderIDs:   q.ProviderIDs,
			}
			if side == "buy" {
				v.TokenAmount = amt / rate // fiat in, token out
			} else {
				v.TokenAmount = amt / rate // token needed to realise this fiat
			}
			res.Venues = append(res.Venues, v)
		}
	}

	if len(res.Venues) == 0 {
		return nil, fmt.Errorf("%w (%s %s %s)", ErrPaycrestNoVenue, fiatAmount, res.FiatCode, side)
	}

	// Cheapest for the user first: on a buy that is the lowest fiat-per-token;
	// on a sell it is the highest.
	sort.SliceStable(res.Venues, func(i, j int) bool {
		a, _ := strconv.ParseFloat(res.Venues[i].Rate, 64)
		b, _ := strconv.ParseFloat(res.Venues[j].Rate, 64)
		if side == "buy" {
			return a < b
		}
		return a > b
	})
	res.Best = &res.Venues[0]
	return res, nil
}

// VenueFor re-checks one specific venue immediately before creating an order.
// The user may have sat on a quote for minutes and providers move; binding the
// order to a re-confirmed price is what stops us promising a rate the order
// will not carry.
func (s *PaycrestQuoteService) VenueFor(ctx context.Context, side, fiatCode, fiatAmount, chainKey, token string) (*PaycrestVenue, error) {
	res, err := s.Quote(ctx, side, fiatCode, fiatAmount)
	if err != nil {
		return nil, err
	}
	for i := range res.Venues {
		if strings.EqualFold(res.Venues[i].ChainKey, chainKey) &&
			strings.EqualFold(res.Venues[i].Token, token) {
			return &res.Venues[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s on %s is no longer fillable at this size",
		ErrPaycrestNoVenue, strings.ToUpper(token), chainKey)
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
