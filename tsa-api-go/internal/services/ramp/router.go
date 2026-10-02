package ramp

import (
	"context"
	"errors"
	"fmt"
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

// OpenRequest is a buy or sell the user confirmed. Request must pin Token
// and ChainKey. Handle is optional ("try this provider first"); QuotedRate
// is the venue rate she saw and is required.
type OpenRequest struct {
	Request    Request
	Handle     string
	QuotedRate string
	Input      OrderInput
}

// Open opens the user's order at the best venue within tolerance (spec §5).
func (r *Router) Open(ctx context.Context, req OpenRequest) (*Order, error) {
	q := normalise(req.Request)
	if q.Token == "" || q.ChainKey == "" || (q.Side != Buy && q.Side != Sell) {
		return nil, ErrInvalidQuote
	}
	limit, err := LimitRate(q.Side, req.QuotedRate, r.cfg.ToleranceBps)
	if err != nil {
		return nil, err
	}
	pinned := VenueKey{ChainKey: q.ChainKey, Token: q.Token}

	var first Provider
	if strings.TrimSpace(req.Handle) != "" {
		name, k, err := DecodeHandle(req.Handle)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(k.ChainKey, pinned.ChainKey) || !strings.EqualFold(k.Token, pinned.Token) {
			return nil, ErrInvalidQuote
		}
		first = r.provider(name) // nil when that provider is gone: start at step 3
	}

	in := req.Input
	in.LimitRate, in.Guard = limit, r.cfg.Guard
	budget, cancel := context.WithTimeout(ctx, r.cfg.OpenBudget)
	defer cancel()

	// Step 0: no second order anywhere while one is open.
	if err := r.preflight(ctx, in.UserID, q.Side); err != nil {
		return nil, err
	}

	st := openState{tried: map[string]bool{}}
	logOutcome := func(outcome string) {
		log.Printf("[ramp] METRIC ramp_route side=%s fiat=%s token=%s chain=%s quoted_rate=%s chosen=%s attempts=%d failovers=%s outcome=%s",
			q.Side, q.FiatCode, q.Token, q.ChainKey, req.QuotedRate, st.chosen, st.attempts, strings.Join(st.failovers, ","), outcome)
	}

	// Steps 1–2: the venue she was shown.
	if first != nil {
		o, stop, err := r.attempt(ctx, budget, first, pinned, q, in, &st)
		if o != nil || stop {
			logOutcome(outcomeOf(o, err))
			return o, err
		}
	}

	// Step 3: re-quote the pinned pair; try untried venues best-first. The
	// tolerance filter refuses only in enforce mode: in log mode (the rollout
	// default) a venue outside it is recorded and still tried, so shadow mode
	// never changes what the user gets.
	enforce := r.cfg.Guard == services.PriceGuardEnforce
	fresh, qerr := r.Quote(budget, q)
	if fresh != nil {
		for _, v := range fresh.Venues {
			if budget.Err() != nil {
				break
			}
			if st.tried[v.Provider] {
				continue
			}
			if !Acceptable(q.Side, v.Rate, limit) {
				if enforce {
					st.outsideTolerance = true
					continue
				}
				log.Printf("[ramp] METRIC ramp_price_guard_would_refuse stage=quote side=%s quoted=%s venue_rate=%s limit=%s",
					q.Side, req.QuotedRate, v.Rate, limit)
			}
			p := r.provider(v.Provider)
			if p == nil {
				continue
			}
			o, stop, err := r.attempt(ctx, budget, p, pinned, q, in, &st)
			if o != nil || stop {
				logOutcome(outcomeOf(o, err))
				return o, err
			}
		}
	}

	// Step 4: nothing opened.
	switch {
	case st.sawPriceMoved || st.outsideTolerance:
		logOutcome("price_moved")
		if fresh == nil {
			fresh = &QuoteResult{FiatCode: q.FiatCode, FiatAmount: q.FiatAmount, Side: string(q.Side),
				Venues: []Venue{}, QuotedAt: time.Now()} // never "venues": null
		}
		return nil, &PriceMovedError{Quote: fresh}
	case fresh == nil && qerr != nil && !errors.Is(qerr, ErrUnavailable):
		// The re-quote says why (leg closed, no venue, below minimum),
		// whatever earlier attempts hit.
		logOutcome("no_venue")
		return nil, qerr
	default:
		// Carry the last provider's own reason (e.g. a payout bank it cannot
		// pay), so the handler can show it instead of a generic outage.
		logOutcome("unavailable")
		return nil, WrapClass(ErrUnavailable, st.lastCause)
	}
}

type openState struct {
	tried            map[string]bool
	attempts         int
	failovers        []string
	chosen           string
	sawPriceMoved    bool
	outsideTolerance bool
	lastCause        error // the last ErrVenueUnavailable, for the final message
}

// attempt opens at one provider. stop=true means return (o, err) as-is.
func (r *Router) attempt(ctx, budget context.Context, p Provider, k VenueKey, q Request, in OrderInput, st *openState) (*Order, bool, error) {
	if budget.Err() != nil {
		return nil, false, nil
	}
	st.attempts++
	st.tried[p.Name()] = true
	actx, cancel := context.WithTimeout(budget, r.cfg.AttemptTimeout)
	defer cancel()
	o, err := p.Open(actx, k, q, in)
	switch {
	case err == nil && o != nil:
		st.chosen = p.Name()
		return o, true, nil
	case err == nil:
		err = fmt.Errorf("%w: provider %s returned no order", ErrOutcomeUnknown, p.Name())
		fallthrough
	case errors.Is(err, ErrOutcomeUnknown):
		// Something may have been written. Never open a second order over it.
		if perr := r.preflight(ctx, in.UserID, q.Side); perr != nil {
			return nil, true, perr
		}
	case errors.Is(err, ErrRefusedForUser):
		return nil, true, err
	case errors.Is(err, services.ErrPriceMovedPastQuote):
		st.sawPriceMoved = true
	case errors.Is(err, ErrVenueUnavailable):
		st.lastCause = err
	default:
		// Infrastructure, or a provider error not classified as "nothing
		// sent" (v1 deliberately does not fail over on these — spec §4.3).
		return nil, true, err
	}
	log.Printf("[ramp] open failed over provider=%s side=%s err=%v", p.Name(), q.Side, err)
	st.failovers = append(st.failovers, p.Name())
	return nil, false, nil
}

// preflight refuses when any provider holds an open order for this user and
// side. Runs on a short, detached deadline so a spent budget cannot skip it.
func (r *Router) preflight(ctx context.Context, userID uuid.UUID, side Side) error {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, p := range r.providers {
		// The provider's own refusal, wording included (a TSA Pay payment in
		// progress says so), or an infrastructure error — both stop the open.
		if err := p.CheckNoOpenOrder(pctx, userID, side); err != nil {
			return err
		}
	}
	return nil
}

func outcomeOf(o *Order, err error) string {
	switch {
	case o != nil:
		return "opened"
	case errors.Is(err, ErrRefusedForUser):
		return "refused"
	default:
		return "error"
	}
}
