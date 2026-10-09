package ramp

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// poolIT is the slice of *services.ITService the pool adapter uses.
type poolIT interface {
	itBuyReads
	PoolLegOpen(ctx context.Context) bool
	PoolVenue() (chain, token string)
	PoolBandBps() int64
	PoolSettingsNow() services.PoolSettings
	PoolCanFill(ctx context.Context, units *big.Int, set services.PoolSettings) bool
	QuotePoolBuy(asset, fiatMajor, currency string, mid *big.Rat, set services.PoolSettings) (*services.PoolPrice, error)
	CheckPoolDailyCap(ctx context.Context, userID uuid.UUID, units *big.Int, set services.PoolSettings) error
	OpenPoolBuy(ctx context.Context, userID uuid.UUID, p *services.PoolPrice, destAddress string) (*models.ITIntent, *services.StableBuyInstructions, error)
	OpenPoolConvertedBuy(ctx context.Context, userID uuid.UUID, p *services.PoolPrice, in services.ConvertedOpen) (*models.ITIntent, *services.StableBuyInstructions, *models.PaycrestOrderRecord, error)
}

const poolFiat = "NGN"

// PoolProvider is TSA Liquidity (spec 2026-10-08 §11.1), on the company
// wallet's template: the shared itBuyProvider reads and itConverted flow.
type PoolProvider struct {
	itBuyProvider
	it      poolIT
	market  services.TreasuryRateSource
	conv    ConvertedPricer
	openCap time.Duration
}

// NewPoolProvider is a true nil unless the pool desk is wired (decision 17:
// the configuration is the switch; the typed-nil lesson).
func NewPoolProvider(it *services.ITService, market services.TreasuryRateSource) Provider {
	if tm, typed := market.(*services.TreasuryMarketRate); it == nil || market == nil || (typed && tm == nil) || !it.HasPool() {
		return nil
	}
	return newPoolProvider(it, market, services.PoolOpenCap)
}

func newPoolProvider(it poolIT, market services.TreasuryRateSource, openCap time.Duration) *PoolProvider {
	return &PoolProvider{itBuyProvider: itBuyProvider{name: ProviderPool, reads: it, now: time.Now}, it: it, market: market, openCap: openCap}
}

func (p *PoolProvider) Name() string   { return ProviderPool }
func (p *PoolProvider) BandBps() int64 { return p.it.PoolBandBps() }

func (p *PoolProvider) RampLegs(ctx context.Context) (map[string]services.RampLegs, error) {
	set := p.it.PoolSettingsNow()
	open := p.it.PoolLegOpen(ctx) && p.it.PoolCanFill(ctx, set.MinSliceUnits, set)
	if open {
		if _, err := p.market.BuyRate(ctx, poolFiat); err != nil {
			open = false
		}
	}
	return map[string]services.RampLegs{poolFiat: {Buy: open}}, nil
}

// price is one pool quote at the current mid, or the class a router expects.
func (p *PoolProvider) price(ctx context.Context, r Request) (*services.PoolPrice, float64, error) {
	if r.Side != Buy || !strings.EqualFold(strings.TrimSpace(r.FiatCode), poolFiat) {
		return nil, 0, ErrLegClosed
	}
	set := p.it.PoolSettingsNow()
	if set.OrdersStopped || !p.it.PoolLegOpen(ctx) {
		return nil, 0, ErrLegClosed
	}
	mid, err := p.market.BuyRate(ctx, poolFiat)
	if err != nil {
		return nil, 0, WrapClass(ErrVenueUnavailable, err)
	}
	_, token := p.it.PoolVenue()
	q, err := p.it.QuotePoolBuy(token, r.FiatAmount, poolFiat, mid, set)
	switch {
	case errors.Is(err, services.ErrPoolCentralOnly), errors.Is(err, services.ErrPoolOrdersStopped):
		return nil, 0, ErrLegClosed
	case err != nil:
		return nil, 0, classifyTreasuryQuote(err)
	}
	if !p.it.PoolCanFill(ctx, q.Quote.AssetUnits, set) {
		return nil, 0, WrapClass(ErrAboveMaximum, errStockDoesNotCover) // never names the stock (§23)
	}
	f, _ := mid.Float64()
	return q, f, nil
}

func (p *PoolProvider) Quote(ctx context.Context, r Request) (*ProviderQuote, error) {
	chain, token := p.it.PoolVenue()
	if (r.Token != "" && !strings.EqualFold(r.Token, token)) || (r.ChainKey != "" && !strings.EqualFold(r.ChainKey, chain)) {
		return nil, ErrLegClosed
	}
	q, mid, err := p.price(ctx, r)
	if err != nil {
		return nil, err
	}
	return &ProviderQuote{MarketRate: mid, Venues: []Venue{itVenue(ProviderPool, chain, token, q.Quote, mid)}}, nil
}

// Open is §12.2 behind the company wallet's checks, capped at 8 s (S12) so the
// router keeps ≥ 12 s of its 20 s budget for the re-quote (≤ 8 s) and the
// next venue's open (review M8).
func (p *PoolProvider) Open(ctx context.Context, k VenueKey, r Request, in OrderInput) (*Order, error) {
	ctx, cancel := context.WithTimeout(ctx, p.openCap)
	defer cancel()
	chain, token := p.it.PoolVenue()
	if !strings.EqualFold(k.ChainKey, chain) || !strings.EqualFold(k.Token, token) {
		return nil, WrapClass(ErrVenueUnavailable, ErrLegClosed)
	}
	q, _, err := p.price(ctx, r)
	if err != nil {
		return nil, WrapClass(ErrVenueUnavailable, err)
	}
	bound := services.FormatMinor(q.Quote.RatePerUnitMinor.String())
	if err := services.CheckPriceGuard(ProviderPool, string(Buy), bound, in.LimitRate, in.Guard); err != nil {
		return nil, err
	}
	if err := p.it.CheckPoolDailyCap(ctx, in.UserID, q.Quote.AssetUnits, q.Settings); err != nil {
		return nil, classifyPoolOpen(err)
	}
	if err := p.checkAbandonCap(ctx, in.UserID); err != nil {
		return nil, err
	}
	intent, payInto, err := p.it.OpenPoolBuy(ctx, in.UserID, q, in.WalletAddress)
	if err != nil {
		return nil, classifyPoolOpen(err)
	}
	return &Order{Provider: ProviderPool, IT: intent, PayInto: payInto}, nil
}

// classifyPoolOpen: every pool refusal leaves no open order (the failed open
// is cancelled in the service), so the router fails over; anything else is
// the shared IT classification (a DB error mid-write stays outcome-unknown).
func classifyPoolOpen(err error) error {
	if poolRefused(err) {
		return WrapClass(ErrVenueUnavailable, err)
	}
	return classifyITOpen(err, true)
}

// poolRefused: the pool said no and nothing stayed open.
func poolRefused(err error) bool {
	for _, s := range []error{services.ErrPoolCannotFill, services.ErrPoolClosed, services.ErrTreasuryNonceStuck,
		services.ErrPoolOrdersStopped, services.ErrPoolDailyCapReached, services.ErrPoolCentralOnly} {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

func (p *PoolProvider) converts(r Request) bool {
	return p.conv != nil && r.Want != nil && r.Side == Buy && strings.EqualFold(strings.TrimSpace(r.FiatCode), poolFiat)
}

func (p *PoolProvider) converted() itConverted {
	return itConverted{
		conv: p.conv,
		leg: func(ctx context.Context, r Request, open bool) (*itConvertedLeg, error) {
			q, mid, err := p.price(ctx, r)
			if err != nil {
				if open {
					// Closed, stopped or short since her quote: nothing opened
					// here, so the router fails over (as the direct Open does).
					return nil, WrapClass(ErrVenueUnavailable, err)
				}
				return nil, err
			}
			if !open && r.UserID != uuid.Nil {
				if err := p.it.CheckPoolDailyCap(ctx, r.UserID, q.Quote.AssetUnits, q.Settings); errors.Is(err, services.ErrPoolDailyCapReached) {
					return nil, err
				}
			}
			chain, token := p.it.PoolVenue()
			return &itConvertedLeg{q: q.Quote, chain: chain, token: token, mid: mid, pool: q}, nil
		},
		admit: func(ctx context.Context, userID uuid.UUID, leg *itConvertedLeg) error {
			if err := p.it.CheckPoolDailyCap(ctx, userID, leg.q.AssetUnits, leg.pool.Settings); err != nil {
				return classifyPoolOpen(err)
			}
			return nil
		},
		open: func(ctx context.Context, userID uuid.UUID, leg *itConvertedLeg, in services.ConvertedOpen) (*models.ITIntent, *services.StableBuyInstructions, *models.PaycrestOrderRecord, error) {
			return p.it.OpenPoolConvertedBuy(ctx, userID, leg.pool, in)
		},
	}
}

func (p *PoolProvider) QuoteConverted(ctx context.Context, r Request) ([]Venue, error) {
	if !p.converts(r) {
		return nil, nil
	}
	return p.quoteConverted(ctx, p.converted(), r), nil // every fee line is tagged at its source (review I-4)
}

func (p *PoolProvider) OpenConverted(ctx context.Context, r Request, in OrderInput) (*Order, error) {
	if !p.converts(r) {
		return nil, WrapClass(ErrVenueUnavailable, errConversionGone)
	}
	ctx, cancel := context.WithTimeout(ctx, p.openCap)
	defer cancel()
	o, err := p.openConverted(ctx, p.converted(), r, in)
	if err != nil && poolRefused(err) {
		// The shared flow classes an unnamed service error as outcome-unknown;
		// a pool refusal left nothing open, so it is the direct open's class.
		var c classed
		if errors.As(err, &c) {
			err = c.cause
		}
		return nil, classifyPoolOpen(err)
	}
	return o, err
}

var (
	_ Provider        = (*PoolProvider)(nil)
	_ ConvertedQuoter = (*PoolProvider)(nil)
	_ PoolBander      = (*PoolProvider)(nil)
)
