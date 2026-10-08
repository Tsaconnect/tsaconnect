package jobs

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakeFloats struct {
	enabled bool
	floats  []services.SponsorFloat
	err     error
}

func (f fakeFloats) Enabled() bool { return f.enabled }
func (f fakeFloats) FloatBalances(context.Context) ([]services.SponsorFloat, error) {
	return f.floats, f.err
}

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	fn()
	return buf.String()
}

func TestGasFloatMonitor_AlertsLowAndEmptySeparately(t *testing.T) {
	m := NewGasFloatMonitor(fakeFloats{enabled: true, floats: []services.SponsorFloat{
		{Chain: "base", Address: "0xa", BalanceWei: big.NewInt(1_500), FloorWei: big.NewInt(1_000)}, // < 2x floor
		{Chain: "bsc", Address: "0xb", BalanceWei: big.NewInt(900), FloorWei: big.NewInt(1_000)},    // < floor
		{Chain: "arbitrum", Address: "0xc", BalanceWei: big.NewInt(5_000), FloorWei: big.NewInt(1_000)},
	}}, 0)
	out := captureLog(t, func() {
		if err := m.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "ramp_sponsor_float_low_total=1 chain=base") {
		t.Fatalf("missing low alert for base:\n%s", out)
	}
	if !strings.Contains(out, "ramp_sponsor_float_empty_total=1 chain=bsc") {
		t.Fatalf("missing empty alert for bsc:\n%s", out)
	}
	if strings.Contains(out, "chain=arbitrum") {
		t.Fatalf("healthy chain alerted:\n%s", out)
	}
}

func TestGasFloatMonitor_DormantWhenDisabled(t *testing.T) {
	m := NewGasFloatMonitor(fakeFloats{enabled: false}, 0)
	if out := captureLog(t, func() { _ = m.Run(context.Background()) }); out != "" {
		t.Fatalf("disabled monitor logged:\n%s", out)
	}
}

// The SAME monitor, pointed at the treasury: one job type, two registrations.
func TestGasFloatMonitor_CarriesItsMetricPrefix(t *testing.T) {
	m := NewGasFloatMonitorNamed("ramp_treasury", fakeFloats{enabled: true, floats: []services.SponsorFloat{
		{Chain: "base", BalanceWei: big.NewInt(1), FloorWei: big.NewInt(1000)},
	}}, time.Minute)
	out := captureLog(t, func() {
		if err := m.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "ramp_treasury_float_empty_total=1 chain=base") {
		t.Fatalf("no treasury alert logged: %s", out)
	}
}

func TestGasFloatMonitor_AlertsWhenKeptUSDCNeedsSweeping(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	// FloorWei/BalanceWei left nil on purpose: the kept-USDC check must run
	// before Run's "no native reading, skip" continue.
	reader := &fakeFloats{enabled: true, floats: []services.SponsorFloat{{Chain: "base", TokenUSD: 600}}}
	m := NewGasFloatMonitorNamed("ramp_treasury", reader, time.Minute).WithKeptUSDCAlert(500)
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "ramp_treasury_kept_usdc_high_total=1 chain=base usd=600") {
		t.Fatalf("log = %q", buf.String())
	}
}

// Review M1: a read error leaves the tick redacted — the scheduler logs it as is.
func TestGasFloatMonitor_ReadErrorIsRedacted(t *testing.T) {
	m := NewGasFloatMonitorNamed("liquidity_pool_release", fakeFloats{enabled: true,
		err: errors.New(`release EOA balance: Post "https://bnb-mainnet.g.alchemy.com/v2/SECRETKEY123": EOF`)}, 0)
	err := m.Run(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRETKEY123") {
		t.Fatalf("tick error = %v, want it redacted", err)
	}
}
