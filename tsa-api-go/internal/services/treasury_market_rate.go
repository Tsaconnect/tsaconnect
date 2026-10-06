package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// DefaultTreasuryRateTolBps is RAMP_TREASURY_RATE_TOL_BPS: how far the company
// wallet's rate sources may drift apart before its leg closes (review I2).
const DefaultTreasuryRateTolBps int64 = 400

var (
	// ErrTreasuryRateDisagree: the sources (or the one source and the FX feed)
	// are further apart than the tolerance. Transient: the leg is closed until
	// they agree again; nothing is parked on it.
	ErrTreasuryRateDisagree = fmt.Errorf("%w: the company wallet's rate sources disagree", ErrStableQuoteUnavailable)
	// ErrTreasuryRateUnverified: one source answered and the FX feed that
	// would check it is missing or stale.
	ErrTreasuryRateUnverified = fmt.Errorf("%w: the company wallet's only rate source cannot be checked", ErrStableQuoteUnavailable)
)

// TreasuryRateSource is the market rate the company wallet prices from.
type TreasuryRateSource interface {
	BuyRate(ctx context.Context, fiat string) (*big.Rat, error)
}

// SetTreasuryMarketRate wires the company wallet's market rate (Decision D44).
func (s *ITService) SetTreasuryMarketRate(src TreasuryRateSource) { s.treasuryRates = src }

// StableFxReference is the independent FX feed's fiat per USD — the source
// checkRatePlausible uses — or false when it is missing or stale.
func (s *ITService) StableFxReference(ctx context.Context, currency string) (*big.Rat, bool) {
	if s == nil {
		return nil, false
	}
	perUSD, ok := s.stablePlausibilityRateFor(ctx, strings.ToUpper(strings.TrimSpace(currency)))
	if !ok || perUSD <= 0 {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(perUSD, 'f', -1, 64))
	return r, ok
}

// StableMidFiatPerUnit is the mid of Flutterwave's buy and sell rate for one
// token — the company wallet's second rate source (§13 Q4). It needs only the
// rate quoter, not the whole Flutterwave buy rail. The adapter asks for USDC
// only: USDT stock is 0 at launch (§13 Q1), and the two trade at par.
func (s *ITService) StableMidFiatPerUnit(ctx context.Context, currency, asset string) (*big.Rat, error) {
	if s == nil || s.rateQuoter == nil {
		return nil, fmt.Errorf("%w: no Flutterwave rate source", ErrStableQuoteUnavailable)
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	asset = strings.ToUpper(strings.TrimSpace(asset))
	buyProbe, err := s.rateQuoter.QuoteRate(ctx, currency, asset, 1)
	if err != nil {
		return nil, fmt.Errorf("it stable mid: buy: %w", err)
	}
	buy, err := DeriveBuyFiatPerUnit(buyProbe)
	if err != nil {
		return nil, err
	}
	sellProbe, err := s.rateQuoter.QuoteRate(ctx, asset, currency, 1)
	if err != nil {
		return nil, fmt.Errorf("it stable mid: sell: %w", err)
	}
	sell, err := DeriveFiatPerUnit(sellProbe)
	if err != nil {
		return nil, err
	}
	mid := new(big.Rat).Add(buy, sell)
	mid.Quo(mid, big.NewRat(2, 1))
	if err := s.checkRatePlausible(ctx, mid, currency); err != nil {
		return nil, err
	}
	return mid, nil
}

type paycrestMarketRates interface {
	MarketRate(ctx context.Context, fiat, side string) (float64, error)
}

type stableMidSource interface {
	StableMidFiatPerUnit(ctx context.Context, currency, asset string) (*big.Rat, error)
	StableFxReference(ctx context.Context, currency string) (*big.Rat, bool)
}

const (
	treasuryMidFreshTTL = 60 * time.Second
	treasuryMidMaxStale = 5 * time.Minute
	treasuryMidTimeout  = 8 * time.Second
)

type midReading struct {
	rate *big.Rat
	at   time.Time
}

// TreasuryMarketRate is the company wallet's market rate (Decision D44,
// revised by review I2): it fails closed rather than trust one unchecked
// figure.
type TreasuryMarketRate struct {
	paycrest paycrestMarketRates // nil: no first source
	mid      stableMidSource     // nil: no second source and no FX check
	tolBps   int64
	now      func() time.Time

	mu     sync.Mutex
	mids   map[string]midReading
	flight singleflight.Group
}

// NewTreasuryMarketRate builds the source. Either source may be nil; tolBps
// <= 0 means DefaultTreasuryRateTolBps.
func NewTreasuryMarketRate(pc *PaycrestQuoteService, it *ITService, tolBps int64) *TreasuryMarketRate {
	var p paycrestMarketRates
	if pc != nil { // a typed nil would read as a source
		p = pc
	}
	var m stableMidSource
	if it != nil {
		m = it
	}
	return newTreasuryMarketRate(p, m, tolBps)
}

func newTreasuryMarketRate(pc paycrestMarketRates, mid stableMidSource, tolBps int64) *TreasuryMarketRate {
	if tolBps <= 0 {
		tolBps = DefaultTreasuryRateTolBps
	}
	return &TreasuryMarketRate{paycrest: pc, mid: mid, tolBps: tolBps, now: time.Now, mids: map[string]midReading{}}
}

// BuyRate is fiat per token for a company-wallet buy:
//   - both sources up: they must agree within tolBps, and the higher is used;
//   - one up: it must agree with the FX feed within tolBps;
//   - otherwise the leg is closed.
func (r *TreasuryMarketRate) BuyRate(ctx context.Context, fiat string) (*big.Rat, error) {
	fiat = strings.ToUpper(strings.TrimSpace(fiat))
	pc, pcErr := r.paycrestRate(ctx, fiat)
	mid, midErr := r.midRate(ctx, fiat)
	switch {
	case pcErr == nil && midErr == nil:
		if !withinBps(pc, mid, r.tolBps) {
			log.Printf("[ramp-treasury] ALERT ramp_treasury_rate_disagree fiat=%s paycrest=%s flutterwave_mid=%s tol_bps=%d",
				fiat, pc.FloatString(2), mid.FloatString(2), r.tolBps)
			return nil, fmt.Errorf("%w: paycrest %s vs flutterwave mid %s", ErrTreasuryRateDisagree, pc.FloatString(2), mid.FloatString(2))
		}
		// The company is the seller: of two agreeing figures, take the higher.
		if mid.Cmp(pc) > 0 {
			return mid, nil
		}
		return pc, nil
	case pcErr == nil:
		return r.checkedAgainstFX(ctx, fiat, pc, "paycrest")
	case midErr == nil:
		rate, err := r.checkedAgainstFX(ctx, fiat, mid, "flutterwave_mid")
		if err == nil {
			log.Printf("[ramp-treasury] METRIC ramp_treasury_market_fallback source=flutterwave_mid fiat=%s reason=%q", fiat, pcErr.Error())
		}
		return rate, err
	default:
		log.Printf("[ramp-treasury] ALERT ramp_treasury_rate_unavailable fiat=%s paycrest=%v flutterwave_mid=%v", fiat, pcErr, midErr)
		return nil, fmt.Errorf("%w: company wallet market rate: paycrest: %v; flutterwave mid: %v",
			ErrStableQuoteUnavailable, pcErr, midErr)
	}
}

// checkedAgainstFX accepts a lone source only when the independent FX feed is
// fresh and within the tolerance of it.
func (r *TreasuryMarketRate) checkedAgainstFX(ctx context.Context, fiat string, rate *big.Rat, source string) (*big.Rat, error) {
	var ref *big.Rat
	ok := false
	if r.mid != nil {
		ref, ok = r.mid.StableFxReference(ctx, fiat)
	}
	if !ok {
		log.Printf("[ramp-treasury] ALERT ramp_treasury_rate_unverified fiat=%s source=%s rate=%s — no fresh FX feed to check it",
			fiat, source, rate.FloatString(2))
		return nil, fmt.Errorf("%w: %s %s", ErrTreasuryRateUnverified, source, rate.FloatString(2))
	}
	if !withinBps(rate, ref, r.tolBps) {
		log.Printf("[ramp-treasury] ALERT ramp_treasury_rate_disagree fiat=%s %s=%s fx=%s tol_bps=%d",
			fiat, source, rate.FloatString(2), ref.FloatString(2), r.tolBps)
		return nil, fmt.Errorf("%w: %s %s vs fx %s", ErrTreasuryRateDisagree, source, rate.FloatString(2), ref.FloatString(2))
	}
	return rate, nil
}

// withinBps: |rate − ref| ≤ ref × bps / 10000.
func withinBps(rate, ref *big.Rat, bps int64) bool {
	diff := new(big.Rat).Sub(rate, ref)
	diff.Abs(diff)
	limit := new(big.Rat).Mul(ref, big.NewRat(bps, 10_000))
	return diff.Cmp(limit) <= 0
}

func (r *TreasuryMarketRate) paycrestRate(ctx context.Context, fiat string) (*big.Rat, error) {
	if r.paycrest == nil {
		return nil, errors.New("no Paycrest reference rate configured")
	}
	v, err := r.paycrest.MarketRate(ctx, fiat, "buy")
	if err != nil {
		return nil, err
	}
	if v <= 0 {
		return nil, fmt.Errorf("paycrest reference rate %v is not positive", v)
	}
	// Through the decimal string, so 1349.5 is exactly 13495/10.
	rate, ok := new(big.Rat).SetString(strconv.FormatFloat(v, 'f', -1, 64))
	if !ok {
		return nil, fmt.Errorf("paycrest reference rate %v is unreadable", v)
	}
	return rate, nil
}

func (r *TreasuryMarketRate) midRate(ctx context.Context, fiat string) (*big.Rat, error) {
	if r.mid == nil {
		return nil, fmt.Errorf("%w: no Flutterwave mid configured", ErrStableQuoteUnavailable)
	}
	r.mu.Lock()
	last, ok := r.mids[fiat]
	r.mu.Unlock()
	if ok && r.now().Sub(last.at) < treasuryMidFreshTTL {
		return new(big.Rat).Set(last.rate), nil
	}
	v, err, _ := r.flight.Do(fiat, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), treasuryMidTimeout)
		defer cancel()
		rate, err := r.mid.StableMidFiatPerUnit(fctx, fiat, models.ITAssetUSDC)
		if err != nil {
			return nil, err
		}
		r.mu.Lock()
		r.mids[fiat] = midReading{rate: rate, at: r.now()}
		r.mu.Unlock()
		return rate, nil
	})
	if err == nil {
		return new(big.Rat).Set(v.(*big.Rat)), nil
	}
	if ok && r.now().Sub(last.at) < treasuryMidMaxStale {
		log.Printf("[ramp-treasury] serving a %s-old Flutterwave mid for %s: %v", r.now().Sub(last.at).Round(time.Second), fiat, err)
		return new(big.Rat).Set(last.rate), nil
	}
	return nil, err
}

var _ TreasuryRateSource = (*TreasuryMarketRate)(nil)
