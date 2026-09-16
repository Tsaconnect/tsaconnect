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

// GasFloatMonitor watches each sponsor lane's native balance. Below the floor
// the lane refuses to sponsor and users silently fall back to paying gas —
// which new users cannot do — so the alert fires at TWICE the floor, while
// there is still time to refill. Alert-only: refilling is manual in Phase 1.
type GasFloatMonitor struct {
	sponsor  sponsorFloatReader
	interval time.Duration
}

func NewGasFloatMonitor(sponsor sponsorFloatReader, interval time.Duration) *GasFloatMonitor {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &GasFloatMonitor{sponsor: sponsor, interval: interval}
}

func (w *GasFloatMonitor) Name() string            { return "gas-float-monitor" }
func (w *GasFloatMonitor) Interval() time.Duration { return w.interval }

func (w *GasFloatMonitor) Run(ctx context.Context) error {
	if w.sponsor == nil || !w.sponsor.Enabled() {
		return nil
	}
	floats, err := w.sponsor.FloatBalances(ctx)
	for _, f := range floats {
		if f.FloorWei == nil || f.BalanceWei == nil {
			continue
		}
		switch {
		case f.BalanceWei.Cmp(f.FloorWei) < 0:
			log.Printf("[gas-float-monitor] ALERT ramp_sponsor_float_empty_total=1 chain=%s address=%s balance_wei=%s floor_wei=%s — sponsorship on this chain is OFF until refilled",
				f.Chain, f.Address, f.BalanceWei, f.FloorWei)
		case f.BalanceWei.Cmp(new(big.Int).Mul(f.FloorWei, big.NewInt(2))) < 0:
			log.Printf("[gas-float-monitor] ALERT ramp_sponsor_float_low_total=1 chain=%s address=%s balance_wei=%s floor_wei=%s — refill soon",
				f.Chain, f.Address, f.BalanceWei, f.FloorWei)
		}
	}
	return err
}
