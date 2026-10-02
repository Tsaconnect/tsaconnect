package ramp

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// Config is the router's tolerance, guard mode and time budgets (RAMP_*).
type Config struct {
	ToleranceBps    int
	Guard           services.PriceGuardMode
	ProviderTimeout time.Duration
	AttemptTimeout  time.Duration
	OpenBudget      time.Duration
}

// Router routes quotes and orders across providers, in a fixed order
// (Paycrest first) that also breaks rate ties.
type Router struct {
	cfg       Config
	providers []Provider
	byName    map[string]Provider
}

func NewRouter(cfg Config, providers ...Provider) *Router {
	if cfg.ToleranceBps <= 0 {
		cfg.ToleranceBps = 50
	}
	if cfg.ProviderTimeout <= 0 {
		cfg.ProviderTimeout = 8 * time.Second
	}
	if cfg.AttemptTimeout <= 0 {
		cfg.AttemptTimeout = 15 * time.Second
	}
	if cfg.OpenBudget <= 0 {
		cfg.OpenBudget = 20 * time.Second
	}
	r := &Router{cfg: cfg, byName: map[string]Provider{}}
	for _, p := range providers {
		if p == nil {
			continue
		}
		r.providers = append(r.providers, p)
		r.byName[p.Name()] = p
	}
	return r
}

func (r *Router) Enabled() bool { return r != nil && len(r.providers) > 0 }

func (r *Router) provider(name string) Provider { return r.byName[name] }

func normalise(req Request) Request {
	req.FiatCode = strings.ToUpper(strings.TrimSpace(req.FiatCode))
	if req.FiatCode == "" {
		req.FiatCode = "NGN"
	}
	req.Token = strings.ToUpper(strings.TrimSpace(req.Token))
	req.ChainKey = strings.ToLower(strings.TrimSpace(req.ChainKey))
	return req
}

// Quote fans out to every provider (each under ProviderTimeout), merges and
// sorts best-first. Errors only when no venue remains, by spec §4.2's
// precedence.
func (r *Router) Quote(ctx context.Context, req Request) (*QuoteResult, error) {
	req = normalise(req)
	type answer struct {
		q   *ProviderQuote
		err error
	}
	answers := make([]answer, len(r.providers))
	var wg sync.WaitGroup
	for i, p := range r.providers {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, r.cfg.ProviderTimeout)
			defer cancel()
			q, err := p.Quote(pctx, req)
			if err == nil && q == nil {
				err = ErrNoVenue
			}
			if err != nil {
				log.Printf("[ramp] METRIC ramp_provider_quote_failed provider=%s side=%s fiat=%s err=%v",
					p.Name(), req.Side, req.FiatCode, err)
			}
			answers[i] = answer{q, err}
		}(i, p)
	}
	wg.Wait()

	res := &QuoteResult{FiatCode: req.FiatCode, FiatAmount: req.FiatAmount, Side: string(req.Side), QuotedAt: time.Now()}
	var errs []error
	for _, a := range answers { // provider order: the tie-break
		if a.err != nil {
			errs = append(errs, a.err)
			continue
		}
		if res.MarketRate == 0 {
			res.MarketRate = a.q.MarketRate
		}
		for _, v := range a.q.Venues {
			if req.Token != "" && !strings.EqualFold(v.Token, req.Token) {
				continue
			}
			if req.ChainKey != "" && !strings.EqualFold(v.ChainKey, req.ChainKey) {
				continue
			}
			res.Venues = append(res.Venues, v)
		}
	}
	sort.SliceStable(res.Venues, func(i, j int) bool {
		return RateBetter(req.Side, res.Venues[i].Rate, res.Venues[j].Rate)
	})
	if req.Token == "" || req.ChainKey == "" {
		res.Venues = bestPerPair(res.Venues)
	}
	if len(res.Venues) == 0 {
		return nil, quoteError(errs)
	}
	res.Best = &res.Venues[0]
	return res, nil
}

// bestPerPair keeps the first (best, since sorted) venue per (token, chain).
func bestPerPair(sorted []Venue) []Venue {
	seen := map[string]bool{}
	out := sorted[:0:0]
	for _, v := range sorted {
		k := strings.ToUpper(v.Token) + "|" + strings.ToLower(v.ChainKey)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

// quoteError picks the user-facing reason when nothing quoted (spec §4.2).
func quoteError(errs []error) error {
	if len(errs) == 0 {
		return ErrNoVenue // every provider answered, with nothing for this request
	}
	all := func(target error) bool {
		for _, e := range errs {
			if !errors.Is(e, target) {
				return false
			}
		}
		return true
	}
	for _, e := range errs {
		if errors.Is(e, ErrNoVenue) {
			return ErrNoVenue // spec rule: any "no venue for this size/pair"
		}
	}
	switch {
	case all(ErrLegClosed):
		return ErrLegClosed
	case all(ErrBelowMinimum):
		return ErrBelowMinimum
	default:
		return ErrUnavailable
	}
}

// RampLegs ORs the providers that answered. An error only when every
// provider failed — the IT handler then applies its own fallback, so a
// failed provider never contributes an optimistic "open".
func (r *Router) RampLegs(ctx context.Context) (map[string]services.RampLegs, error) {
	out := map[string]services.RampLegs{}
	var lastErr error
	answered := 0
	for _, p := range r.providers {
		legs, err := p.RampLegs(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		answered++
		for fiat, l := range legs {
			cur := out[fiat]
			out[fiat] = services.RampLegs{Buy: cur.Buy || l.Buy, Sell: cur.Sell || l.Sell}
		}
	}
	if answered == 0 {
		if lastErr == nil {
			lastErr = ErrUnavailable
		}
		return nil, lastErr
	}
	return out, nil
}

func (r *Router) resolve(id string) (Provider, string, error) {
	name, native, err := ResolveID(id)
	if err != nil {
		return nil, "", err
	}
	p := r.provider(name)
	if p == nil {
		return nil, "", ErrOrderNotFound
	}
	return p, native, nil
}

func (r *Router) Get(ctx context.Context, userID uuid.UUID, id string) (*Order, error) {
	p, native, err := r.resolve(id)
	if err != nil {
		return nil, err
	}
	return p.Get(ctx, userID, native)
}

func (r *Router) ClaimPayment(ctx context.Context, userID uuid.UUID, id string) (*Order, error) {
	p, native, err := r.resolve(id)
	if err != nil {
		return nil, err
	}
	return p.ClaimPayment(ctx, userID, native)
}

func (r *Router) Cancel(ctx context.Context, userID uuid.UUID, id string) (*Order, error) {
	p, native, err := r.resolve(id)
	if err != nil {
		return nil, err
	}
	return p.Cancel(ctx, userID, native)
}

// List merges every provider's orders newest first. A failing provider is
// logged and skipped unless every provider fails.
func (r *Router) List(ctx context.Context, userID uuid.UUID, limit int) ([]Order, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	var out []Order
	var lastErr error
	answered := 0
	for _, p := range r.providers {
		orders, err := p.List(ctx, userID, limit)
		if err != nil {
			log.Printf("[ramp] list failed provider=%s user=%s: %v", p.Name(), userID, err)
			lastErr = err
			continue
		}
		answered++
		out = append(out, orders...)
	}
	if answered == 0 && lastErr != nil {
		return nil, lastErr
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt().After(out[j].CreatedAt()) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
