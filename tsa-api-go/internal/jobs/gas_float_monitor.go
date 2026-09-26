package jobs

import (
	"context"
	"log"
	"math/big"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

type sponsorFloatReader interface {
	Enabled() bool
	FloatBalances(ctx context.Context) ([]services.SponsorFloat, error)
}

// GasFloatMonitor watches a native-balance float — the gas sponsor lane or,
// with a different metric prefix, the conversion treasury. Below the floor
// the lane refuses to sponsor/fund and users silently fall back (which new
// users cannot do), so the alert fires at TWICE the floor, while there is
// still time to refill. Alert-only: refilling is manual in Phase 1.
type GasFloatMonitor struct {
	sponsor  sponsorFloatReader
	interval time.Duration
	prefix   string
	// keptUSDCAlert is the USD threshold above which a float's kept USDC
	// (SponsorFloat.TokenUSD) needs sweeping. Zero (the default) means off:
	// only the treasury registration turns this on, via WithKeptUSDCAlert.
	keptUSDCAlert float64
}

// NewGasFloatMonitor watches the gas sponsor float (metric prefix "ramp_sponsor").
func NewGasFloatMonitor(sponsor sponsorFloatReader, interval time.Duration) *GasFloatMonitor {
	return NewGasFloatMonitorNamed("ramp_sponsor", sponsor, interval)
}

// NewGasFloatMonitorNamed watches any float reader (sponsor lane, treasury, …)
// under its own metric prefix — the same job type, registered under a
// different name, rather than a second monitor implementation.
func NewGasFloatMonitorNamed(prefix string, sponsor sponsorFloatReader, interval time.Duration) *GasFloatMonitor {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &GasFloatMonitor{sponsor: sponsor, interval: interval, prefix: prefix}
}

// WithKeptUSDCAlert turns on the kept-USDC check at the given USD threshold
// and returns the monitor, for chaining onto the constructor. Sweeping stays
// manual — this only alerts.
func (w *GasFloatMonitor) WithKeptUSDCAlert(usd float64) *GasFloatMonitor {
	w.keptUSDCAlert = usd
	return w
}

func (w *GasFloatMonitor) Name() string            { return "gas-float-monitor" }
func (w *GasFloatMonitor) Interval() time.Duration { return w.interval }

func (w *GasFloatMonitor) Run(ctx context.Context) error {
	if w.sponsor == nil || !w.sponsor.Enabled() {
		return nil
	}
	floats, err := w.sponsor.FloatBalances(ctx)
	for _, f := range floats {
		// Runs before the nil-native-reading continue below: a float that
		// only carries a token balance (no FloorWei/BalanceWei yet read)
		// must still get its kept-USDC check.
		if w.keptUSDCAlert > 0 && f.TokenUSD > w.keptUSDCAlert {
			log.Printf("[%s] ALERT %s_kept_usdc_high_total=1 chain=%s usd=%.0f", w.prefix, w.prefix, f.Chain, f.TokenUSD)
		}
		if f.FloorWei == nil || f.BalanceWei == nil {
			continue
		}
		switch {
		case f.BalanceWei.Cmp(f.FloorWei) < 0:
			log.Printf("[gas-float-monitor] ALERT %s_float_empty_total=1 chain=%s address=%s balance_wei=%s floor_wei=%s — sponsorship on this chain is OFF until refilled",
				w.prefix, f.Chain, f.Address, f.BalanceWei, f.FloorWei)
		case f.BalanceWei.Cmp(new(big.Int).Mul(f.FloorWei, big.NewInt(2))) < 0:
			log.Printf("[gas-float-monitor] ALERT %s_float_low_total=1 chain=%s address=%s balance_wei=%s floor_wei=%s — refill soon",
				w.prefix, f.Chain, f.Address, f.BalanceWei, f.FloorWei)
		}
	}
	return err
}
