package ramp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
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

// Router routes quotes and orders across providers, in a fixed registration
// order (treasury, paycrest, flutterwave in production) that also breaks rate
// ties (Decision D36).
type Router struct {
	cfg       Config
	providers []Provider
	byName    map[string]Provider
}

func NewRouter(cfg Config, providers ...Provider) *Router {
	if cfg.ToleranceBps <= 0 {
		log.Printf("[ramp] RAMP_PRICE_TOLERANCE_BPS=%d is not positive; using 50", cfg.ToleranceBps)
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
// precedence. With Want set, providers that convert add their converted
// venues; per target the company wallet goes before Flutterwave and one venue
// (the best) is kept (spec §15.2, Decision C2).
func (r *Router) Quote(ctx context.Context, req Request) (*QuoteResult, error) {
	return r.quote(ctx, req, true)
}

// quote is Quote; rule=false keeps every venue (an open's re-quote, which
// fails over through all of them and applies the rule itself).
func (r *Router) quote(ctx context.Context, req Request, rule bool) (*QuoteResult, error) {
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
			if req.Want != nil {
				if cq, ok := p.(ConvertedQuoter); ok {
					conv, cerr := cq.QuoteConverted(pctx, req)
					if cerr != nil {
						log.Printf("[ramp] METRIC ramp_provider_converted_quote_failed provider=%s err=%s",
							p.Name(), services.RedactAlertText(cerr.Error()))
					}
					if len(conv) > 0 {
						merged := &ProviderQuote{}
						if err == nil {
							merged.MarketRate = q.MarketRate
							merged.Venues = append(merged.Venues, q.Venues...)
						}
						merged.Venues = append(merged.Venues, conv...)
						q, err = merged, nil
					}
				}
			}
			if err != nil {
				log.Printf("[ramp] METRIC ramp_provider_quote_failed provider=%s side=%s fiat=%q err=%v",
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
			// Uniform JSON: every venue carries both rates and a fee list (§14).
			if strings.TrimSpace(v.EffectiveRate) == "" {
				v.EffectiveRate = v.Rate
			}
			if v.Fees == nil {
				v.Fees = []VenueFee{}
			}
			if req.Token != "" && !strings.EqualFold(v.Token, req.Token) {
				continue
			}
			if req.ChainKey != "" && !strings.EqualFold(v.ChainKey, req.ChainKey) {
				continue
			}
			res.Venues = append(res.Venues, v)
		}
	}
	if rule {
		r.shown(res, req)
	} else {
		sortVenues(req.Side, res.Venues) // every venue, pools included; Open orders them itself
	}
	if len(res.Venues) == 0 {
		return nil, quoteError(errs)
	}
	res.Best = &res.Venues[0]
	return res, nil
}

// shown shapes what a quote shows her — the quote's own tail, and an open's
// "price moved" re-show (review I-3), so the two cannot drift. A pool-card
// (PoolOnly) quote lists the pool's venues best-first with PoolAvailable set;
// when the pool has none it says so and falls back to the ruled list. Any
// other quote is the ruled list.
func (r *Router) shown(res *QuoteResult, req Request) {
	if req.PoolOnly {
		pools, _ := splitPool(res.Venues)
		avail := len(pools) > 0
		res.PoolAvailable = &avail
		if avail {
			sortVenues(req.Side, pools)
			res.Venues = pools
			return
		}
	}
	res.Venues = r.ruled(req.Side, res.Venues, req.Token == "" || req.ChainKey == "")
}

// poolCardOnly reports an open from the pinned pool card that must stay the
// pool's order (decision 14, review I-3): PoolOnly, and the venue she tapped
// was the pool's (or none was named). A pool-card screen showing other
// offers because the pool could not fill lets her pick one, which opens as
// any open does.
func poolCardOnly(q Request, handleProvider string) bool {
	return q.PoolOnly && (handleProvider == "" || handleProvider == ProviderPool)
}

// bestPerPair keeps the first (best, since sorted) venue per target — what it
// delivers to her, direct or converted (Decision C2).
func bestPerPair(sorted []Venue) []Venue {
	seen := map[string]bool{}
	out := sorted[:0:0]
	for _, v := range sorted {
		t := v.Target()
		k := t.Token + "|" + t.ChainKey
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

func sortVenues(side Side, vs []Venue) {
	sort.SliceStable(vs, func(i, j int) bool { return RateBetter(side, vs[i].RankRate(), vs[j].RankRate()) })
}

// ruled is what a shown quote obeys: the company wallet first over Flutterwave,
// best-first, the pool ahead of its target within the band (S17), one venue
// per target when the pair is not pinned. Without a pool venue it is exactly
// today's companyWalletFirst → sort → bestPerPair.
func (r *Router) ruled(side Side, venues []Venue, unpinned bool) []Venue {
	pools, rest := splitPool(venues)
	third := bestThirdParty(side, rest) // read before company-wallet suppression
	rest = companyWalletFirst(rest)
	sortVenues(side, rest)
	inBand, _ := r.poolsInBand(side, pools, third)
	out := placePools(side, rest, inBand)
	if unpinned {
		out = bestPerPair(out)
	}
	return out
}

// failoverOrder is an open's re-quote order (§11.2 step 5): in-band pool
// venues first, then today's order (Flutterwave last), then out-of-band pool
// venues as the very last resort.
func (r *Router) failoverOrder(side Side, venues []Venue) []Venue {
	pools, rest := splitPool(venues)
	inBand, outBand := r.poolsInBand(side, pools, bestThirdParty(side, rest))
	out := append([]Venue(nil), inBand...)
	out = append(out, companyWalletFirstOrder(rest)...)
	return append(out, outBand...)
}

// splitPool separates pool venues, keeping both lists' order.
func splitPool(vs []Venue) (pools, rest []Venue) {
	for _, v := range vs {
		if v.Provider == ProviderPool {
			pools = append(pools, v)
		} else {
			rest = append(rest, v)
		}
	}
	return pools, rest
}

// bestThirdParty is the best RankRate per target among Paycrest and
// Flutterwave venues (S17: the company wallet is never compared).
func bestThirdParty(side Side, vs []Venue) map[VenueKey]string {
	out := map[VenueKey]string{}
	for _, v := range vs {
		if v.Provider != ProviderPaycrest && v.Provider != ProviderFlutterwave {
			continue
		}
		t := v.Target()
		if cur, ok := out[t]; !ok || RateBetter(side, v.RankRate(), cur) {
			out[t] = v.RankRate()
		}
	}
	return out
}

// poolsInBand splits pool venues by the band rule (S17): preferred when no
// third party serves the target, or when within pool_band of the best one.
// Both lists come back best-first.
func (r *Router) poolsInBand(side Side, pools []Venue, third map[VenueKey]string) (in, out []Venue) {
	var band int64
	if p, ok := r.byName[ProviderPool].(PoolBander); ok {
		band = p.BandBps()
	}
	sortVenues(side, pools)
	for _, p := range pools {
		best, has := third[p.Target()]
		preferred := !has || withinBand(side, p.RankRate(), best, band)
		gap := "n/a"
		if has {
			gap = bandGapBps(p.RankRate(), best)
		}
		if !preferred { // review M-14: only the pool priced out of its band is worth a line
			log.Printf("[ramp] METRIC ramp_pool_band target=%s/%s pool=%s best_third_party=%q gap_bps=%s preferred=false",
				p.Target().ChainKey, p.Target().Token, p.RankRate(), best, gap)
		}
		if preferred {
			in = append(in, p)
		} else {
			out = append(out, p)
		}
	}
	return in, out
}

// withinBand: pool ≤ best × (1 + band) on a buy (the pool is buy-only).
func withinBand(side Side, pool, best string, bandBps int64) bool {
	p, ok1 := new(big.Rat).SetString(strings.TrimSpace(pool))
	b, ok2 := new(big.Rat).SetString(strings.TrimSpace(best))
	if !ok1 || !ok2 || side != Buy {
		return false
	}
	limit := new(big.Rat).Mul(b, big.NewRat(10_000+bandBps, 10_000))
	return p.Cmp(limit) <= 0
}

func bandGapBps(pool, best string) string {
	p, ok1 := new(big.Rat).SetString(strings.TrimSpace(pool))
	b, ok2 := new(big.Rat).SetString(strings.TrimSpace(best))
	if !ok1 || !ok2 || b.Sign() == 0 {
		return "n/a"
	}
	g := new(big.Rat).Quo(new(big.Rat).Sub(p, b), b)
	return g.Mul(g, big.NewRat(10_000, 1)).FloatString(1)
}

// placePools puts each preferred pool venue directly ahead of the first venue
// for its target, else at its rank; every other venue keeps its position.
// pools is best-first and is placed worst-first, so when several share a
// target the best of them ends up first. The result is never nil: a re-shown
// quote must not send "venues": null.
func placePools(side Side, sorted, pools []Venue) []Venue {
	out := append(make([]Venue, 0, len(sorted)+len(pools)), sorted...)
	for j := len(pools) - 1; j >= 0; j-- {
		p := pools[j]
		at := len(out)
		for i, v := range out {
			if v.Target() == p.Target() {
				at = i
				break
			}
		}
		if at == len(out) {
			for i, v := range out {
				if RateBetter(side, p.RankRate(), v.RankRate()) {
					at = i
					break
				}
			}
		}
		out = append(out[:at], append([]Venue{p}, out[at:]...)...)
	}
	return out
}

// companyWalletFirst drops every Flutterwave venue for a target the company
// wallet has a venue for (spec §15.2). Paycrest is never dropped.
func companyWalletFirst(venues []Venue) []Venue {
	cw := map[VenueKey]bool{}
	for _, v := range venues {
		if v.Provider == ProviderTreasury {
			cw[v.Target()] = true
		}
	}
	if len(cw) == 0 {
		return venues
	}
	out := venues[:0:0]
	for _, v := range venues {
		if v.Provider == ProviderFlutterwave && cw[v.Target()] {
			continue
		}
		out = append(out, v)
	}
	return out
}

// companyWalletFirstOrder moves Flutterwave venues behind the rest (stable),
// so failover reaches the company wallet first whatever the rates.
func companyWalletFirstOrder(venues []Venue) []Venue {
	out := make([]Venue, 0, len(venues))
	var later []Venue
	for _, v := range venues {
		if v.Provider == ProviderFlutterwave {
			later = append(later, v)
			continue
		}
		out = append(out, v)
	}
	return append(out, later...)
}

// companyWalletUntried: a Flutterwave venue waits while the company wallet has
// a venue for the same target and has not yet been tried (spec §15.2).
func companyWalletUntried(v Venue, venues []Venue, st *openState) bool {
	if v.Provider != ProviderFlutterwave || st.tried[ProviderTreasury] {
		return false
	}
	for _, o := range venues {
		if o.Provider == ProviderTreasury && o.Target() == v.Target() {
			return true
		}
	}
	return false
}

// quoteError picks the user-facing reason when nothing quoted (spec §4.2, as
// amended by rev 3 §4.4: a closed leg no longer hides an amount-range answer).
func quoteError(errs []error) error {
	if len(errs) == 0 {
		return ErrNoVenue // every provider answered, with nothing for this request
	}
	for _, e := range errs {
		if errors.Is(e, ErrNoVenue) {
			return ErrNoVenue // spec rule: any "no venue for this size/pair"
		}
	}
	all := func(match func(error) bool) bool {
		for _, e := range errs {
			if !match(e) {
				return false
			}
		}
		return true
	}
	closed := func(e error) bool { return errors.Is(e, ErrLegClosed) }
	outOfRange := func(e error) bool { return errors.Is(e, ErrBelowMinimum) || errors.Is(e, ErrAboveMaximum) }
	switch {
	case all(closed):
		return ErrLegClosed
	case all(func(e error) bool { return closed(e) || outOfRange(e) }):
		// Every provider that trades this way is out of range for this amount;
		// below wins when they disagree.
		for _, e := range errs {
			if errors.Is(e, ErrBelowMinimum) {
				return ErrBelowMinimum
			}
		}
		return ErrAboveMaximum
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

// order runs fn at the provider holding id. "<provider>:<native>" goes
// straight there. A bare UUID can be any provider's, so providers are asked in
// registration order and a not-found answer moves on (D11).
func (r *Router) order(id string, fn func(p Provider, native string) (*Order, error)) (*Order, error) {
	name, native, err := ResolveID(id)
	if err != nil {
		return nil, err
	}
	if _, perr := uuid.Parse(strings.TrimSpace(id)); perr != nil {
		p := r.provider(name)
		if p == nil {
			return nil, ErrOrderNotFound
		}
		return fn(p, native)
	}
	for _, p := range r.providers {
		o, err := fn(p, native)
		if !isNotFound(err) {
			return o, err
		}
	}
	return nil, ErrOrderNotFound
}

// isNotFound is "this provider does not hold that id for this user", in any
// provider's words.
func isNotFound(err error) bool {
	return errors.Is(err, ErrOrderNotFound) ||
		errors.Is(err, services.ErrPaycrestOrderNotYours) ||
		errors.Is(err, services.ErrPaycrestOrderNotFound)
}

func (r *Router) Get(ctx context.Context, userID uuid.UUID, id string) (*Order, error) {
	return r.order(id, func(p Provider, native string) (*Order, error) { return p.Get(ctx, userID, native) })
}

func (r *Router) ClaimPayment(ctx context.Context, userID uuid.UUID, id string) (*Order, error) {
	return r.order(id, func(p Provider, native string) (*Order, error) { return p.ClaimPayment(ctx, userID, native) })
}

func (r *Router) Cancel(ctx context.Context, userID uuid.UUID, id string) (*Order, error) {
	return r.order(id, func(p Provider, native string) (*Order, error) { return p.Cancel(ctx, userID, native) })
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
	handleProvider := ""
	if strings.TrimSpace(req.Handle) != "" {
		name, k, err := DecodeHandle(req.Handle)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(k.ChainKey, pinned.ChainKey) || !strings.EqualFold(k.Token, pinned.Token) {
			return nil, ErrInvalidQuote
		}
		first = r.provider(name) // nil when that provider is gone: start at step 3
		handleProvider = name
	}
	q.PoolOnly = poolCardOnly(q, handleProvider)

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
		log.Printf("[ramp] METRIC ramp_route side=%s fiat=%q token=%q chain=%q quoted_rate=%q chosen=%s attempts=%d failovers=%s outcome=%s",
			q.Side, q.FiatCode, q.Token, q.ChainKey, req.QuotedRate, st.chosen, st.attempts, strings.Join(st.failovers, ","), outcome)
	}

	// Steps 1–2: the venue she was shown.
	if first != nil {
		o, stop, err := r.attempt(ctx, budget, first, q.Side, in.UserID, &st,
			func(actx context.Context) (*Order, error) { return first.Open(actx, pinned, q, in) })
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
	fresh, qerr := r.quote(budget, q, false)
	if fresh != nil {
		for _, v := range r.failoverOrder(q.Side, fresh.Venues) {
			if budget.Err() != nil {
				break
			}
			if q.PoolOnly && v.Provider != ProviderPool {
				continue // a pool-card open is the pool's order or none
			}
			if st.tried[v.Provider] || companyWalletUntried(v, fresh.Venues, &st) {
				continue
			}
			if !Acceptable(q.Side, v.RankRate(), limit) {
				if enforce {
					st.outsideTolerance = true
					continue
				}
				log.Printf("[ramp] METRIC ramp_price_guard_would_refuse stage=quote provider=%s side=%s quoted=%q venue_rate=%s limit=%s tol_bps=%d",
					v.Provider, q.Side, req.QuotedRate, v.RankRate(), limit, r.cfg.ToleranceBps)
			}
			p := r.provider(v.Provider)
			if p == nil {
				continue
			}
			o, stop, err := r.attempt(ctx, budget, p, q.Side, in.UserID, &st,
				func(actx context.Context) (*Order, error) { return p.Open(actx, pinned, q, in) })
			if o != nil || stop {
				logOutcome(outcomeOf(o, err))
				return o, err
			}
		}
	}

	return nil, r.noneOpened(&st, fresh, qerr, pinned, q, logOutcome)
}

// noneOpened is step 4 of an open that opened nothing (Open and OpenConverted).
func (r *Router) noneOpened(st *openState, fresh *QuoteResult, qerr error, pinned VenueKey, q Request, logOutcome func(string)) error {
	switch {
	case st.sawPriceMoved || st.outsideTolerance:
		logOutcome("price_moved")
		if fresh == nil {
			fresh = &QuoteResult{FiatCode: q.FiatCode, FiatAmount: q.FiatAmount, Side: string(q.Side),
				Venues: []Venue{}, QuotedAt: time.Now()} // never "venues": null
		}
		// What she is shown again obeys the same rules as any quote — a
		// pool-card open's is pool-only (review I-3): the re-quote kept every
		// venue for failover (rule=false).
		r.shown(fresh, q)
		fresh.Best = nil
		if len(fresh.Venues) > 0 {
			fresh.Best = &fresh.Venues[0]
		}
		showBoundRates(fresh, pinned, st.bound)
		return &PriceMovedError{Quote: fresh}
	case fresh == nil && qerr != nil && !errors.Is(qerr, ErrUnavailable):
		// The re-quote says why (leg closed, no venue, below minimum),
		// whatever earlier attempts hit.
		logOutcome("no_venue")
		return qerr
	default:
		// Carry the last provider's own reason (e.g. a payout bank it cannot
		// pay), so the handler can show it instead of a generic outage.
		logOutcome("unavailable")
		return WrapClass(ErrUnavailable, st.lastCause)
	}
}

// OpenConverted opens a buy of a requested asset (spec §15): the venue she
// was shown first, then every venue for her target best-first — company
// wallet before Flutterwave — direct or converted.
func (r *Router) OpenConverted(ctx context.Context, req OpenRequest) (*Order, error) {
	q := normalise(req.Request)
	if q.Side != Buy || q.Want == nil {
		return nil, ErrInvalidQuote
	}
	q.Token, q.ChainKey = "", "" // pinned on the target, never on a leg
	target := VenueKey{ChainKey: strings.ToLower(strings.TrimSpace(q.Want.Chain)), Token: strings.ToUpper(strings.TrimSpace(q.Want.Token))}
	limit, err := LimitRate(q.Side, req.QuotedRate, r.cfg.ToleranceBps)
	if err != nil {
		return nil, err
	}
	var first Provider
	var shown Venue // the venue the handle names, as far as a handle can say
	if h := strings.TrimSpace(req.Handle); h != "" {
		name, k, err := DecodeHandle(h)
		if err != nil {
			return nil, err
		}
		first = r.provider(name) // nil when that provider is gone: start at the re-quote
		q.PoolOnly = poolCardOnly(q, name)
		// A handle carries the pair the provider delivers: her target means a
		// direct venue, anything else the settlement leg of a conversion.
		shown = Venue{Provider: name, ChainKey: k.ChainKey, Token: k.Token, Kind: models.RouteKindDirect}
		if shown.Target() != target {
			shown.Kind = models.RouteKindConverted
		}
	}

	in := req.Input
	in.LimitRate, in.Guard = limit, r.cfg.Guard
	budget, cancel := context.WithTimeout(ctx, r.cfg.OpenBudget)
	defer cancel()

	if err := r.preflight(ctx, in.UserID, q.Side); err != nil {
		return nil, err
	}

	st := openState{tried: map[string]bool{}}
	logOutcome := func(outcome string) {
		log.Printf("[ramp] METRIC ramp_route kind=converted side=%s fiat=%q target=%s/%s quoted_rate=%q chosen=%s attempts=%d failovers=%s outcome=%s",
			q.Side, q.FiatCode, target.ChainKey, target.Token, req.QuotedRate, st.chosen, st.attempts, strings.Join(st.failovers, ","), outcome)
	}
	// openVenue is how venue v opens at p, or nil when p cannot open it.
	openVenue := func(p Provider, v Venue) func(context.Context) (*Order, error) {
		if v.Kind == models.RouteKindConverted {
			cq, ok := p.(ConvertedQuoter)
			if !ok {
				return nil
			}
			return func(actx context.Context) (*Order, error) { return cq.OpenConverted(actx, q, in) }
		}
		k := v.Target()
		direct := Request{Side: q.Side, FiatCode: q.FiatCode, FiatAmount: q.FiatAmount, Token: k.Token, ChainKey: k.ChainKey}
		return func(actx context.Context) (*Order, error) { return p.Open(actx, k, direct, in) }
	}

	// The venue she was shown.
	if first != nil {
		if open := openVenue(first, shown); open != nil {
			o, stop, err := r.attempt(ctx, budget, first, q.Side, in.UserID, &st, open)
			if o != nil || stop {
				logOutcome(outcomeOf(o, err))
				return o, err
			}
		}
	}

	// Re-quote and fail over best-first, company wallet before Flutterwave,
	// as Open does (log-only guard mode still tries a venue outside tolerance).
	enforce := r.cfg.Guard == services.PriceGuardEnforce
	fresh, qerr := r.quote(budget, q, false)
	if fresh != nil {
		for _, v := range r.failoverOrder(q.Side, fresh.Venues) {
			if budget.Err() != nil {
				break
			}
			if v.Kind != models.RouteKindConverted && v.Target() != target {
				continue // a direct venue for another pair: an alternative, not her order
			}
			if q.PoolOnly && v.Provider != ProviderPool {
				continue // a pool-card open is the pool's order or none
			}
			if st.tried[v.Provider] || companyWalletUntried(v, fresh.Venues, &st) {
				continue
			}
			if !Acceptable(q.Side, v.RankRate(), limit) {
				if enforce {
					st.outsideTolerance = true
					continue
				}
				log.Printf("[ramp] METRIC ramp_price_guard_would_refuse stage=quote kind=converted provider=%s quoted=%q venue_rate=%s limit=%s tol_bps=%d",
					v.Provider, req.QuotedRate, v.RankRate(), limit, r.cfg.ToleranceBps)
			}
			p := r.provider(v.Provider)
			if p == nil {
				continue
			}
			open := openVenue(p, v)
			if open == nil {
				continue
			}
			o, stop, err := r.attempt(ctx, budget, p, q.Side, in.UserID, &st, open)
			if o != nil || stop {
				logOutcome(outcomeOf(o, err))
				return o, err
			}
		}
	}
	// Bound rates are Paycrest's direct-guard figures; re-showing one as a
	// venue for her converted target would invent a direct venue.
	st.bound = nil
	return nil, r.noneOpened(&st, fresh, qerr, target, q, logOutcome)
}

type openState struct {
	tried            map[string]bool
	attempts         int
	failovers        []string
	chosen           string
	sawPriceMoved    bool
	outsideTolerance bool
	lastCause        error       // the last ErrVenueUnavailable, for the final message
	bound            []boundRate // what each guard refusal bound at, in attempt order
}

type boundRate struct{ provider, rate string }

// attempt opens at one provider through open. stop=true means return (o, err)
// as-is.
func (r *Router) attempt(ctx, budget context.Context, p Provider, side Side, userID uuid.UUID, st *openState, open func(context.Context) (*Order, error)) (*Order, bool, error) {
	if budget.Err() != nil {
		return nil, false, nil
	}
	st.attempts++
	st.tried[p.Name()] = true
	actx, cancel := context.WithTimeout(budget, r.cfg.AttemptTimeout)
	defer cancel()
	o, err := open(actx)
	switch {
	case err == nil && o != nil:
		st.chosen = p.Name()
		return o, true, nil
	case err == nil:
		err = fmt.Errorf("%w: provider %s returned no order", ErrOutcomeUnknown, p.Name())
		fallthrough
	case errors.Is(err, ErrOutcomeUnknown):
		// Something may have been written. Never open a second order over it.
		if perr := r.preflight(ctx, userID, side); perr != nil {
			return nil, true, perr
		}
	case errors.Is(err, ErrRefusedForUser):
		return nil, true, err
	case errors.Is(err, services.ErrPriceMovedPastQuote):
		st.sawPriceMoved = true
		var moved *services.PriceMovedPastQuoteError
		if errors.As(err, &moved) {
			st.bound = append(st.bound, boundRate{p.Name(), moved.BoundRate})
		}
	case errors.Is(err, ErrVenueUnavailable):
		st.lastCause = err
	default:
		// Infrastructure, or a provider error not classified as "nothing
		// sent" (v1 deliberately does not fail over on these — spec §4.3).
		return nil, true, err
	}
	log.Printf("[ramp] open failed over provider=%s side=%s err=%v", p.Name(), side, err)
	st.failovers = append(st.failovers, p.Name())
	return nil, false, nil
}

// showBoundRates puts the rate each refusing provider actually bound at into
// its venue for the pinned pair, priced as the provider prices a venue
// (tokenAmount = fiat / rate). The guard refuses on the bound rate (Paycrest:
// /v2/rates), not the book rate the re-quote shows; re-showing the book rate
// could be confirmed and refused forever, while a limit from the bound rate
// passes the same bind.
func showBoundRates(res *QuoteResult, k VenueKey, bound []boundRate) {
	amt, _ := strconv.ParseFloat(strings.TrimSpace(res.FiatAmount), 64)
	changed := false
	for _, b := range bound {
		rateStr := strings.TrimSpace(b.rate)
		rate, err := strconv.ParseFloat(rateStr, 64)
		if err != nil || rate <= 0 {
			continue // an unreadable bound rate: nothing honest to show
		}
		i := -1
		for j, v := range res.Venues {
			if v.Provider == b.provider && strings.EqualFold(v.ChainKey, k.ChainKey) && strings.EqualFold(v.Token, k.Token) {
				i = j
				break
			}
		}
		if i < 0 { // the re-quote failed or no longer lists it
			res.Venues = append(res.Venues, Venue{Handle: EncodeHandle(b.provider, k), Provider: b.provider,
				ChainKey: k.ChainKey, Token: k.Token, Kind: models.RouteKindDirect, Fees: []VenueFee{}})
			i = len(res.Venues) - 1
		}
		v := &res.Venues[i]
		// The bound rate is all-in. A venue with fee lines keeps its display
		// rate from the re-quote; one without has no other rate to show.
		v.EffectiveRate = rateStr
		if len(v.Fees) == 0 {
			v.Rate = rateStr
		}
		if amt > 0 {
			v.TokenAmount = amt / rate
		}
		if res.MarketRate > 0 {
			v.SpreadPercent = (rate/res.MarketRate - 1) * 100
		}
		changed = true
	}
	if !changed {
		return
	}
	sort.SliceStable(res.Venues, func(i, j int) bool {
		return RateBetter(Side(res.Side), res.Venues[i].RankRate(), res.Venues[j].RankRate())
	})
	res.Best = &res.Venues[0]
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
