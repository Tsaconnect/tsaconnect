package ramp

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

const (
	// flutterwaveRateTTL bounds how stale a quoted rate may be; the public
	// quote reaches Flutterwave only through it (spec §7).
	flutterwaveRateTTL = 30 * time.Second
	// flutterwaveRateFailTTL: a failure is remembered this long.
	flutterwaveRateFailTTL = 5 * time.Second
	// flutterwaveRateTimeout bounds one shared fetch, whoever started it.
	flutterwaveRateTimeout = 8 * time.Second
)

// flutterwaveFiats are the currencies the Flutterwave buy rail prices.
var flutterwaveFiats = map[string]bool{"NGN": true}

// flutterwaveIT is the slice of *services.ITService the adapter uses.
type flutterwaveIT interface {
	itBuyReads
	HasStableBuy() bool
	StableBuyFiatPerUnit(ctx context.Context, currency, asset string) (*big.Rat, error)
	QuoteRoutedStableBuy(asset, fiatMajor, currency string, fiatPerUnit *big.Rat) (*services.StableBuyQuote, error)
	OpenRoutedStableBuy(ctx context.Context, userID uuid.UUID, q *services.StableBuyQuote, destChain string) (*models.ITIntent, *services.StableBuyInstructions, error)
}

type cachedRate struct {
	rate *big.Rat
	err  error
	at   time.Time
}

// FlutterwaveProvider adapts the brokered Flutterwave stablecoin buy
// (it_stable_buy*.go). Buy only; Polygon only (spec 2.3 #6).
type FlutterwaveProvider struct {
	itBuyProvider
	it  flutterwaveIT
	now func() time.Time

	mu     sync.Mutex
	rates  map[string]cachedRate
	flight singleflight.Group
}

// NewFlutterwaveProvider returns a true nil Provider unless
// RAMP_FLUTTERWAVE_BUY is on and the buy rail is fully wired.
func NewFlutterwaveProvider(it *services.ITService, enabled bool) Provider {
	if !enabled || it == nil || !it.HasStableBuy() {
		return nil
	}
	return newFlutterwaveProvider(it)
}

func newFlutterwaveProvider(it flutterwaveIT) *FlutterwaveProvider {
	p := &FlutterwaveProvider{it: it, now: time.Now, rates: map[string]cachedRate{}}
	p.itBuyProvider = itBuyProvider{name: ProviderFlutterwave, reads: it, now: func() time.Time { return p.now() }}
	return p
}

func (p *FlutterwaveProvider) Name() string { return ProviderFlutterwave }

func (p *FlutterwaveProvider) RampLegs(context.Context) (map[string]services.RampLegs, error) {
	out := map[string]services.RampLegs{}
	if p.it.HasStableBuy() {
		for fiat := range flutterwaveFiats {
			out[fiat] = services.RampLegs{Buy: true}
		}
	}
	return out, nil
}

// rate is Flutterwave's fiat per token. fresh skips the cache read (an open
// binds at the newest rate, D7) but refreshes the cache. Concurrent misses
// share one fetch; a caller whose context ends stops waiting.
func (p *FlutterwaveProvider) rate(ctx context.Context, fiat, token string, fresh bool) (*big.Rat, error) {
	key := fiat + "|" + token
	if !fresh {
		p.mu.Lock()
		c, ok := p.rates[key]
		p.mu.Unlock()
		switch {
		case ok && c.err != nil && p.now().Sub(c.at) < flutterwaveRateFailTTL:
			return nil, c.err
		case ok && c.err == nil && p.now().Sub(c.at) < flutterwaveRateTTL:
			return c.rate, nil
		}
	}
	ch := p.flight.DoChan(key, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flutterwaveRateTimeout)
		defer cancel()
		r, err := p.it.StableBuyFiatPerUnit(fctx, fiat, token)
		p.mu.Lock()
		p.rates[key] = cachedRate{rate: r, err: err, at: p.now()}
		p.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return r, nil
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(*big.Rat), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *FlutterwaveProvider) Quote(ctx context.Context, r Request) (*ProviderQuote, error) {
	fiat := strings.ToUpper(strings.TrimSpace(r.FiatCode))
	if r.Side != Buy || !flutterwaveFiats[fiat] {
		return nil, ErrLegClosed
	}
	chain := strings.ToLower(strings.TrimSpace(r.ChainKey))
	direct := strings.ToLower(services.RoutedDirectChain)
	if chain != "" && chain != direct {
		return nil, ErrNoVenue // Polygon only
	}
	tokens := []string{models.ITAssetUSDC, models.ITAssetUSDT}
	if t := strings.ToUpper(strings.TrimSpace(r.Token)); t != "" {
		if !services.IsStableAsset(t) {
			return nil, ErrNoVenue
		}
		tokens = []string{t}
	}
	out := &ProviderQuote{}
	var errs []error
	for _, token := range tokens {
		if !services.SupportsDelivery(services.RoutedDirectChain, token) {
			errs = append(errs, ErrNoVenue)
			continue
		}
		rate, err := p.rate(ctx, fiat, token, false)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		q, err := p.it.QuoteRoutedStableBuy(token, r.FiatAmount, fiat, rate)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		mid, _ := rate.Float64()
		if out.MarketRate == 0 {
			out.MarketRate = mid
		}
		out.Venues = append(out.Venues, itVenue(ProviderFlutterwave, direct, token, q, mid))
	}
	if len(out.Venues) > 0 {
		return out, nil
	}
	return nil, flutterwaveQuoteError(errs)
}

// flutterwaveQuoteError: the amount-range reason when there is one, then
// "no venue", else the rate source's own error (unavailable).
func flutterwaveQuoteError(errs []error) error {
	for _, e := range errs {
		if errors.Is(e, services.ErrStableBuyBelowMinimum) {
			return WrapClass(ErrBelowMinimum, e)
		}
	}
	for _, e := range errs {
		if errors.Is(e, services.ErrStableBuyAboveMaximum) {
			return WrapClass(ErrAboveMaximum, e)
		}
	}
	for _, e := range errs {
		if errors.Is(e, ErrNoVenue) {
			return ErrNoVenue
		}
	}
	if len(errs) == 0 {
		return ErrNoVenue
	}
	return errs[0]
}

// Open (spec §4.3): re-quote fresh, price guard, abandon cap, then our row.
func (p *FlutterwaveProvider) Open(ctx context.Context, k VenueKey, r Request, in OrderInput) (*Order, error) {
	fiat := strings.ToUpper(strings.TrimSpace(r.FiatCode))
	if r.Side != Buy || !flutterwaveFiats[fiat] {
		return nil, WrapClass(ErrVenueUnavailable, ErrLegClosed)
	}
	token := strings.ToUpper(k.Token)
	chain := strings.ToUpper(k.ChainKey)
	if chain != services.RoutedDirectChain || !services.SupportsDelivery(chain, token) {
		return nil, WrapClass(ErrNoVenue, fmt.Errorf("no Flutterwave venue for %s on %s (Polygon only)", token, chain))
	}
	rate, err := p.rate(ctx, fiat, token, true)
	if err != nil {
		return nil, WrapClass(ErrVenueUnavailable, err)
	}
	q, err := p.it.QuoteRoutedStableBuy(token, r.FiatAmount, fiat, rate)
	if err != nil {
		return nil, classifyITOpen(err, false)
	}
	// The guard binds on the effective rate (D40).
	bound := services.FormatMinor(q.RatePerUnitMinor.String())
	if err := services.CheckPriceGuard(ProviderFlutterwave, string(Buy), bound, in.LimitRate, in.Guard); err != nil {
		return nil, err
	}
	if err := p.checkAbandonCap(ctx, in.UserID); err != nil {
		return nil, err
	}
	intent, payInto, err := p.it.OpenRoutedStableBuy(ctx, in.UserID, q, chain)
	if err != nil {
		return nil, classifyITOpen(err, true)
	}
	return &Order{Provider: ProviderFlutterwave, IT: intent, PayInto: payInto}, nil
}

// classifyITOpen wraps an IT-backed open refusal in its outcome class; the
// cause survives for the handler's wording. writing is true once the open
// write has started: an unnamed error from there on may follow a commit (D34).
// Shared with the company wallet (treasury.go), which adds its own causes.
func classifyITOpen(err error, writing bool) error {
	switch {
	case errors.Is(err, services.ErrKYCVerificationRequired),
		errors.Is(err, services.ErrITAgreementRequired),
		errors.Is(err, services.ErrStableBuyAlreadyOpen),
		errors.Is(err, services.ErrStableBuyNoWallet):
		return WrapClass(ErrRefusedForUser, err)
	case errors.Is(err, services.ErrStableBuyBelowMinimum):
		return WrapClass(ErrBelowMinimum, err)
	case errors.Is(err, services.ErrStableBuyAboveMaximum):
		return WrapClass(ErrAboveMaximum, err)
	case errors.Is(err, services.ErrUnsupportedChainAsset),
		errors.Is(err, services.ErrUnsupportedStableChain),
		errors.Is(err, services.ErrBadDeliveryAddress):
		return WrapClass(ErrNoVenue, err)
	case !writing,
		errors.Is(err, services.ErrStableQuoteUnavailable),
		errors.Is(err, services.ErrStableBuyNoVA),
		errors.Is(err, services.ErrTreasuryUnavailable),
		errors.Is(err, services.ErrTreasuryStockShort),
		errors.Is(err, services.ErrTreasuryStockUnreadable),
		errors.Is(err, services.ErrTreasuryDailyCapReached):
		// Before the write, or a refusal made before writing: another
		// provider may try the pair.
		return WrapClass(ErrVenueUnavailable, err)
	default:
		return WrapClass(ErrOutcomeUnknown, err) // the database, mid-write
	}
}

var _ Provider = (*FlutterwaveProvider)(nil)
