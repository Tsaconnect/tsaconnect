package services

import (
	"context"
	"errors"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

const feeViewToken = "0x00000000000000000000000000000000000000a1"

// fakeP2PRates is a chain's FeeConfig as P2PFeeView reads it: one row per feature (missing =
// never set, inactive). fail makes every read an RPC error; hang blocks each read until the test
// ends; seen records the read context.
type fakeP2PRates struct {
	rows  map[string]FeeConfigRate
	fail  bool
	calls atomic.Int32
	hang  chan struct{}
	seen  *ctxSeen
}

func (f *fakeP2PRates) read(ctx context.Context, _ int64, feature string, _ common.Address) (FeeConfigRate, error) {
	f.calls.Add(1)
	if f.seen != nil {
		_, hasDeadline := ctx.Deadline()
		*f.seen = ctxSeen{err: ctx.Err(), hasDeadline: hasDeadline}
	}
	if f.hang != nil {
		<-f.hang
	}
	if f.fail {
		return FeeConfigRate{}, errors.New("rpc down")
	}
	return f.rows[feature], nil
}

// feeRows is a FeeConfig with both P2P rows active at these bps.
func feeRows(platformBps, gasBps int) *fakeP2PRates {
	return &fakeP2PRates{rows: map[string]FeeConfigRate{
		p2pFeatureP2PPlatform: {BPS: uint16(platformBps), FlatFee: big.NewInt(0), Active: true},
		p2pFeatureP2PGas:      {BPS: uint16(gasBps), FlatFee: big.NewInt(0), Active: true},
	}}
}

func newFeeViewOn(r *fakeP2PRates) *P2PFeeView {
	return NewP2PFeeView(r.read, func(int64, string) string { return feeViewToken })
}

func newFeeViewFailing(t *testing.T) *P2PFeeView {
	t.Helper()
	return newFeeViewOn(&fakeP2PRates{fail: true})
}

// ctxSeen is the read context's state at the moment of the chain call.
type ctxSeen struct {
	err         error
	hasDeadline bool
}

func TestLiveFeeBps_SumsThePlatformAndGasRows(t *testing.T) {
	for _, c := range []struct {
		name          string
		platform, gas int
		want          int
	}{
		{"BSC direct: platform only", 190, 0, 190},
		{"Sonic: gas only", 0, 50, 50},
		{"both", 150, 40, 190},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := newFeeViewOn(feeRows(c.platform, c.gas)).LiveFeeBps(context.Background(), 146, "USDT")
			if err != nil || got != c.want {
				t.Fatalf("got %d, %v; want %d", got, err, c.want)
			}
		})
	}
}

// Every row the escrow would refuse at open is an error, never a fee shown: a trade cannot open.
func TestLiveFeeBps_RowsTheEscrowRefusesAreErrors(t *testing.T) {
	for name, mutate := range map[string]func(r *fakeP2PRates){
		"platform row missing": func(r *fakeP2PRates) { delete(r.rows, p2pFeatureP2PPlatform) },
		"gas row inactive": func(r *fakeP2PRates) {
			r.rows[p2pFeatureP2PGas] = FeeConfigRate{BPS: 10, FlatFee: big.NewInt(0)}
		},
		"flat fee": func(r *fakeP2PRates) {
			r.rows[p2pFeatureP2PPlatform] = FeeConfigRate{BPS: 10, FlatFee: big.NewInt(1), Active: true}
		},
		"platform above 300": func(r *fakeP2PRates) { r.rows[p2pFeatureP2PPlatform] = FeeConfigRate{BPS: 301, Active: true} },
		"gas above 300":      func(r *fakeP2PRates) { r.rows[p2pFeatureP2PGas] = FeeConfigRate{BPS: 301, Active: true} },
		"sum above 500": func(r *fakeP2PRates) {
			r.rows[p2pFeatureP2PPlatform] = FeeConfigRate{BPS: 300, Active: true}
			r.rows[p2pFeatureP2PGas] = FeeConfigRate{BPS: 201, Active: true}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := feeRows(100, 50)
			mutate(r)
			if got, err := newFeeViewOn(r).LiveFeeBps(context.Background(), 146, "USDT"); err == nil {
				t.Fatalf("got %d, want an error", got)
			}
		})
	}
}

func TestLiveFeeBps_UnknownTokenIsAnError(t *testing.T) {
	v := NewP2PFeeView(feeRows(100, 0).read, func(int64, string) string { return "" })
	if _, err := v.LiveFeeBps(context.Background(), 146, "DOGE"); !errors.Is(err, ErrUnsupportedToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestLiveFeeBps_CachesPerChainAndToken(t *testing.T) {
	rates := feeRows(50, 0)
	v := newFeeViewOn(rates)
	now := time.Now()
	v.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if _, err := v.LiveFeeBps(context.Background(), 146, "USDT"); err != nil {
			t.Fatal(err)
		}
	}
	first := rates.calls.Load()
	if first != 2 {
		t.Fatalf("calls = %d, want one read per row", first)
	}
	now = now.Add(61 * time.Second)
	if _, err := v.LiveFeeBps(context.Background(), 146, "USDT"); err != nil {
		t.Fatal(err)
	}
	if rates.calls.Load() == first {
		t.Fatal("cache must expire after 60s so a setRates is followed")
	}
}

// A failed read is retried after ~10s, not the 60s success TTL.
func TestLiveFeeBps_FailureCachedBriefly(t *testing.T) {
	rates := feeRows(50, 0)
	rates.fail = true
	v := newFeeViewOn(rates)
	now := time.Now()
	v.now = func() time.Time { return now }
	if _, err := v.LiveFeeBps(context.Background(), 146, "USDT"); err == nil {
		t.Fatal("want error on unreadable chain")
	}
	rates.fail = false
	now = now.Add(5 * time.Second)
	if _, err := v.LiveFeeBps(context.Background(), 146, "USDT"); err == nil {
		t.Fatal("a failure is still served from cache within its short TTL")
	}
	now = now.Add(6 * time.Second) // 11s after the failure
	if bps, err := v.LiveFeeBps(context.Background(), 146, "USDT"); err != nil || bps != 50 {
		t.Fatalf("after the failure TTL the chain is read again: bps=%d err=%v", bps, err)
	}
}

// The shared read does not inherit the first caller's cancellation: a client
// that hung up must not fail (and cache a failure for) everyone else.
func TestLiveFeeBps_ReadSurvivesCallerCancel(t *testing.T) {
	var seen ctxSeen
	rates := feeRows(50, 0)
	rates.seen = &seen
	v := newFeeViewOn(rates)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if bps, err := v.LiveFeeBps(ctx, 146, "USDT"); err != nil || bps != 50 {
		t.Fatalf("bps=%d err=%v", bps, err)
	}
	if seen.err != nil {
		t.Fatalf("read ran under the caller's cancelled context: %v", seen.err)
	}
	if !seen.hasDeadline {
		t.Fatal("the detached read must still be bounded by its own timeout")
	}
}

// A chain whose core set has not passed its gates shows no fee.
func TestNewP2PFeeViewForCore_NotReadyIsAnError(t *testing.T) {
	v := NewP2PFeeViewForCore(nil, func(int64, string) string { return feeViewToken })
	if _, err := v.LiveFeeBps(context.Background(), 146, "USDT"); !errors.Is(err, ErrCoreSetNotReady) {
		t.Fatalf("err = %v", err)
	}
}

func TestSplitFee_FloorsLikeContract(t *testing.T) {
	for _, c := range []struct {
		amt      string
		bps      int
		fee, net string
	}{
		{"100000000", 50, "500000", "99500000"},
		{"199", 50, "0", "199"},
		{"333", 100, "3", "330"},
	} {
		fee, net, err := SplitFee(c.amt, c.bps)
		if err != nil || fee != c.fee || net != c.net {
			t.Fatalf("SplitFee(%s,%d)=%s,%s,%v", c.amt, c.bps, fee, net, err)
		}
	}
	if _, _, err := SplitFee("abc", 50); err == nil {
		t.Fatal("want error on a non-numeric amount")
	}
}
