package jobs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

type sponsorFloatReader interface {
	Enabled() bool
	FloatBalances(ctx context.Context) ([]services.SponsorFloat, error)
}

// StockLowNotifier emails a company-wallet stock or gas alert.
// *services.InboundHoldNotifier satisfies it.
type StockLowNotifier interface {
	NotifyStockLow(ctx context.Context, a services.StockLowAlert) error
}

// stockAlertEvery bounds how often one (chain, asset) is emailed.
const stockAlertEvery = time.Hour

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
	// tokenLowUSD turns on the company wallet's free-stock alert (and emails
	// a low native balance too). Zero: off. See WithTokenLowAlert.
	tokenLowUSD   float64
	stockNotifier StockLowNotifier
	lowAlerted    map[string]time.Time // chain:asset -> last email
	now           func() time.Time
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
	return &GasFloatMonitor{sponsor: sponsor, interval: interval, prefix: prefix, now: time.Now}
}

// WithKeptUSDCAlert turns on the kept-USDC check at the given USD threshold
// and returns the monitor, for chaining onto the constructor. Sweeping stays
// manual — this only alerts.
func (w *GasFloatMonitor) WithKeptUSDCAlert(usd float64) *GasFloatMonitor {
	w.keptUSDCAlert = usd
	return w
}

// WithTokenLowAlert turns on the company wallet's low-stock alert at usd of
// free stock per token, emailing through n (spec 2026-10-03 §5.3).
func (w *GasFloatMonitor) WithTokenLowAlert(usd float64, n StockLowNotifier) *GasFloatMonitor {
	w.tokenLowUSD, w.stockNotifier, w.lowAlerted = usd, n, map[string]time.Time{}
	return w
}

// lowAlert emails at most every stockAlertEvery while low, and once on recovery.
func (w *GasFloatMonitor) lowAlert(ctx context.Context, key string, low bool, a services.StockLowAlert) {
	if w.stockNotifier == nil {
		return
	}
	now := w.now()
	last, alerted := w.lowAlerted[key]
	switch {
	case low && (!alerted || now.Sub(last) >= stockAlertEvery):
		w.lowAlerted[key] = now
	case !low && alerted:
		delete(w.lowAlerted, key)
		a.Recovered = true
	default:
		return
	}
	if err := w.stockNotifier.NotifyStockLow(ctx, a); err != nil {
		log.Printf("[%s] stock alert notify failed: %s", w.prefix, services.RedactAlertText(err.Error()))
	}
}

// alertTokens runs the free-stock and native-gas alerts for one float.
func (w *GasFloatMonitor) alertTokens(ctx context.Context, f services.SponsorFloat) {
	tokens := make([]string, 0, len(f.Tokens))
	for t := range f.Tokens {
		tokens = append(tokens, t)
	}
	sort.Strings(tokens)
	for _, token := range tokens {
		free := f.Tokens[token]
		low := free < w.tokenLowUSD
		if low {
			log.Printf("[%s] ALERT %s_stock_low chain=%s token=%s free_usd=%.2f", w.prefix, w.prefix, f.Chain, token, free)
		}
		w.lowAlert(ctx, f.Chain+":"+token, low, services.StockLowAlert{
			Chain: f.Chain, Asset: token, Address: f.Address,
			Have: fmt.Sprintf("%.2f %s free", free, token), Threshold: fmt.Sprintf("$%.2f", w.tokenLowUSD),
		})
	}
	if f.FloorWei != nil && f.BalanceWei != nil {
		twice := new(big.Int).Mul(f.FloorWei, big.NewInt(2))
		w.lowAlert(ctx, f.Chain+":native", f.BalanceWei.Cmp(twice) < 0, services.StockLowAlert{
			Chain: f.Chain, Asset: "native gas", Address: f.Address,
			Have: f.BalanceWei.String() + " wei", Threshold: twice.String() + " wei",
		})
	}
}

func (w *GasFloatMonitor) Name() string            { return "gas-float-monitor" }
func (w *GasFloatMonitor) Interval() time.Duration { return w.interval }

func (w *GasFloatMonitor) Run(ctx context.Context) error {
	if w.sponsor == nil || !w.sponsor.Enabled() {
		return nil
	}
	floats, err := w.sponsor.FloatBalances(ctx)
	if err != nil && w.tokenLowUSD > 0 {
		// The company wallet's tick never fails on a read error: log it and
		// alert on whatever was readable.
		log.Printf("[%s] float read: %s", w.prefix, services.RedactAlertText(err.Error()))
		err = nil
	}
	for _, f := range floats {
		if w.tokenLowUSD > 0 {
			w.alertTokens(ctx, f)
		}
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
			log.Printf("[gas-float-monitor] ALERT %s_float_empty_total=1 chain=%s address=%s balance_wei=%s floor_wei=%s — refill now; sends continue only while the balance pays each transaction's own gas",
				w.prefix, f.Chain, f.Address, f.BalanceWei, f.FloorWei)
		case f.BalanceWei.Cmp(new(big.Int).Mul(f.FloorWei, big.NewInt(2))) < 0:
			log.Printf("[gas-float-monitor] ALERT %s_float_low_total=1 chain=%s address=%s balance_wei=%s floor_wei=%s — refill soon",
				w.prefix, f.Chain, f.Address, f.BalanceWei, f.FloorWei)
		}
	}
	if err != nil {
		// The scheduler logs a tick error as is: an RPC error can carry a keyed URL.
		return errors.New(services.RedactAlertText(err.Error()))
	}
	return nil
}
