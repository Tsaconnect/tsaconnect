package services

import (
	"fmt"
	"log"
	"math/big"
	"strings"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// RateBandError is returned when a fixed-rate ad is priced outside the
// allowed band around the market reference. It is an ErrInvalidParams so any
// caller that does not know it still maps to 400.
type RateBandError struct {
	ReferenceRate, MinRate, MaxRate string
	BandBps                         int
}

func (e *RateBandError) Error() string {
	return fmt.Sprintf("p2p: rate must be between %s and %s (within %s%% of the market rate %s)",
		e.MinRate, e.MaxRate, ratDecimal(big.NewRat(int64(e.BandBps), 100)), e.ReferenceRate)
}

func (e *RateBandError) Unwrap() error { return ErrInvalidParams }

// SetFixedRateBandBps sets the allowed distance of a fixed-rate ad from the
// market reference (cfg.P2PFixedRateBandBps).
func (s *P2PTradeService) SetFixedRateBandBps(bps int) {
	s.fixedRateBandBps = bps
}

// fixedRateBand is the band in force: an unset or non-positive value is the
// config default, never "off".
func (s *P2PTradeService) fixedRateBand() int {
	if s.fixedRateBandBps <= 0 {
		return config.DefaultP2PFixedRateBandBps
	}
	return s.fixedRateBandBps
}

// checkFixedRateBand refuses a fixed-rate stable-asset ad priced outside
// ±band of the market reference (a 15-for-1500 typo would sell at a 99% loss).
// It passes when there is nothing to compare against: float ads (already
// capped by floatOffsetCap), non-stable assets, no rate provider, or no usable
// reference for the currency. The last case is logged, not blocked, so
// currencies without an anchor keep working. Callers skip it for PM ads.
func (s *P2PTradeService) checkFixedRateBand(p CreateAdParams) error {
	rt := strings.ToLower(strings.TrimSpace(p.RateType))
	if (rt != "" && rt != models.P2PAdRateTypeFixed) || !IsStableAsset(p.Token) || s.rates == nil {
		return nil
	}
	snap, err := s.rates.GetSnapshot()
	if err != nil {
		return nil // requireFreshRate already ran and owns snapshot failures
	}
	refF := 0.0
	if row, ok := snap.Rates[strings.ToUpper(strings.TrimSpace(p.FiatCurrency))]; ok && !row.Stale {
		refF = row.MidRate
		if refF <= 0 {
			refF = row.BinanceSell
		}
	}
	if refF <= 0 {
		log.Printf("[p2p rate band] no usable reference for %s; fixed-rate ad allowed unchecked", p.FiatCurrency)
		return nil
	}
	rate, ok := new(big.Rat).SetString(strings.TrimSpace(p.Rate))
	if !ok {
		return nil // validateCreateAd already rejects non-numeric rates
	}
	ref := new(big.Rat).SetFloat64(refF)
	if ref == nil {
		return nil
	}
	bps := s.fixedRateBand()
	band := big.NewRat(int64(bps), 10000)
	lo := new(big.Rat).Mul(ref, new(big.Rat).Sub(big.NewRat(1, 1), band))
	hi := new(big.Rat).Mul(ref, new(big.Rat).Add(big.NewRat(1, 1), band))
	if rate.Cmp(lo) >= 0 && rate.Cmp(hi) <= 0 {
		return nil
	}
	return &RateBandError{
		ReferenceRate: ratDecimal(ref), MinRate: ratDecimal(lo), MaxRate: ratDecimal(hi), BandBps: bps,
	}
}

func ratDecimal(r *big.Rat) string {
	return strings.TrimRight(strings.TrimRight(r.FloatString(2), "0"), ".")
}
