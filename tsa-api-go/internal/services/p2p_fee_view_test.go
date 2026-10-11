package services

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

const (
	feeViewEscrow = "0x00000000000000000000000000000000000000e1"
	feeViewMcgp   = "0x00000000000000000000000000000000000000c9"
	feeViewToken  = "0x00000000000000000000000000000000000000a1"
)

// feeViewChain answers the reads P2PFeeView makes: the escrow's feeManager(),
// tokenSymbol(), feeBps(), mcgpToken() and the FeeManager's rateFor().
type feeViewChain struct {
	feeManager bool
	rates      map[string][4]interface{} // feature → bps, flat, exempt, active
	legacy     uint16
	fail       bool
	calls      int
}

func (f *feeViewChain) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	f.calls++
	if f.fail {
		return nil, errors.New("rpc down")
	}
	sel := call.Data[:4]
	switch {
	case bytes.Equal(sel, parsedEscrowABI.Methods["feeManager"].ID):
		fm := common.Address{}
		if f.feeManager {
			fm = common.HexToAddress(testFeeManager)
		}
		return parsedEscrowABI.Methods["feeManager"].Outputs.Pack(fm)
	case bytes.Equal(sel, parsedEscrowABI.Methods["tokenSymbol"].ID):
		return parsedEscrowABI.Methods["tokenSymbol"].Outputs.Pack("SYM")
	case bytes.Equal(sel, parsedP2PEscrowABI.Methods["feeBps"].ID):
		return parsedP2PEscrowABI.Methods["feeBps"].Outputs.Pack(f.legacy)
	case bytes.Equal(sel, parsedP2PEscrowABI.Methods["mcgpToken"].ID):
		return parsedP2PEscrowABI.Methods["mcgpToken"].Outputs.Pack(common.HexToAddress(feeViewMcgp))
	case bytes.Equal(sel, parsedFeeManagerABI.Methods["rateFor"].ID):
		args, _ := parsedFeeManagerABI.Methods["rateFor"].Inputs.Unpack(call.Data[4:])
		r, ok := f.rates[args[0].(string)]
		if !ok {
			r = [4]interface{}{uint16(0), uint64(0), false, false}
		}
		return parsedFeeManagerABI.Methods["rateFor"].Outputs.Pack(r[:]...)
	}
	return nil, errors.New("unexpected call")
}

func newFeeViewOn(chain *feeViewChain) *P2PFeeView {
	return NewP2PFeeView(
		func(int64) (contractCaller, string, error) { return chain, feeViewEscrow, nil },
		func(_ int64, sym string) string {
			if sym == "MCGP" {
				return feeViewMcgp
			}
			return feeViewToken
		},
	)
}

func newFeeViewWithFakes(t *testing.T, feeManager, pActive bool, pBps int, gActive bool, gBps int, legacy int) *P2PFeeView {
	t.Helper()
	return newFeeViewOn(&feeViewChain{
		feeManager: feeManager,
		legacy:     uint16(legacy),
		rates: map[string][4]interface{}{
			"p2p_platform": {uint16(pBps), uint64(0), false, pActive},
			"p2p":          {uint16(gBps), uint64(0), false, gActive},
		},
	})
}

func newFeeViewFailing(t *testing.T) *P2PFeeView {
	t.Helper()
	return newFeeViewOn(&feeViewChain{fail: true})
}

func TestLiveFeeBps_MirrorsContractSnapshot(t *testing.T) {
	cases := []struct {
		name       string
		feeManager bool
		pActive    bool
		pBps       int
		gActive    bool
		gBps       int
		legacy     int
		token      string
		want       int
	}{
		{"no fee manager → legacy", false, false, 0, false, 0, 50, "USDT", 50},
		{"rates unset (today) → legacy", true, false, 0, false, 0, 50, "USDT", 50},
		{"platform set, gas unset", true, true, 190, false, 0, 50, "USDC", 190},
		{"both set", true, true, 150, true, 40, 50, "USDC", 190},
		{"MCGP legacy is zero", false, false, 0, false, 0, 50, "MCGP", 0},
		{"gas set, platform unset → legacy + gas", true, false, 0, true, 40, 50, "USDT", 90},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := newFeeViewWithFakes(t, c.feeManager, c.pActive, c.pBps, c.gActive, c.gBps, c.legacy)
			got, err := v.LiveFeeBps(context.Background(), 146, c.token)
			if err != nil || got != c.want {
				t.Fatalf("got %d, %v; want %d", got, err, c.want)
			}
		})
	}
}

func TestLiveFeeBps_ExemptMcgpIsZero(t *testing.T) {
	chain := &feeViewChain{feeManager: true, legacy: 50, rates: map[string][4]interface{}{
		"p2p_platform": {uint16(190), uint64(0), true, true},
		"p2p":          {uint16(40), uint64(0), true, true},
	}}
	v := newFeeViewOn(chain)
	if got, err := v.LiveFeeBps(context.Background(), 146, "MCGP"); err != nil || got != 0 {
		t.Fatalf("exempt MCGP = %d, %v; want 0", got, err)
	}
	if got, err := v.LiveFeeBps(context.Background(), 146, "USDT"); err != nil || got != 230 {
		t.Fatalf("exempt flag must only spare MCGP: got %d, %v; want 230", got, err)
	}
}

func TestLiveFeeBps_AboveContractCeilingIsAnError(t *testing.T) {
	// The escrow reverts SnapshotFeeTooHigh above MAX_FEE_BPS (500) in total.
	v := newFeeViewWithFakes(t, true, true, 300, true, 300, 50)
	if _, err := v.LiveFeeBps(context.Background(), 146, "USDT"); err == nil {
		t.Fatal("want error: a rate the escrow would revert on is not a rate to show")
	}
}

func TestLiveFeeBps_CachesPerChainAndToken(t *testing.T) {
	chain := &feeViewChain{legacy: 50}
	v := newFeeViewOn(chain)
	now := time.Now()
	v.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if _, err := v.LiveFeeBps(context.Background(), 146, "USDT"); err != nil {
			t.Fatal(err)
		}
	}
	first := chain.calls
	if first == 0 {
		t.Fatal("expected chain reads")
	}
	now = now.Add(61 * time.Second)
	if _, err := v.LiveFeeBps(context.Background(), 146, "USDT"); err != nil {
		t.Fatal(err)
	}
	if chain.calls == first {
		t.Fatal("cache must expire after 60s so a setRates is followed")
	}
}

// A failed read is retried after ~10s, not the 60s success TTL.
func TestLiveFeeBps_FailureCachedBriefly(t *testing.T) {
	chain := &feeViewChain{legacy: 50, fail: true}
	v := newFeeViewOn(chain)
	now := time.Now()
	v.now = func() time.Time { return now }
	if _, err := v.LiveFeeBps(context.Background(), 146, "USDT"); err == nil {
		t.Fatal("want error on unreadable chain")
	}
	chain.fail = false
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
	v := newFeeViewOn(&feeViewChain{legacy: 50})
	var seen ctxSeen
	inner := v.chain
	v.chain = func(id int64) (contractCaller, string, error) {
		c, addr, err := inner(id)
		return ctxRecorder{c, &seen}, addr, err
	}
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

// ctxSeen is the read context's state at the moment of the chain call.
type ctxSeen struct {
	err         error
	hasDeadline bool
}

type ctxRecorder struct {
	contractCaller
	seen *ctxSeen
}

func (r ctxRecorder) CallContract(ctx context.Context, call ethereum.CallMsg, n *big.Int) ([]byte, error) {
	_, hasDeadline := ctx.Deadline()
	*r.seen = ctxSeen{err: ctx.Err(), hasDeadline: hasDeadline}
	return r.contractCaller.CallContract(ctx, call, n)
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

func TestLiveFeeBps_ChainErrorIsReturnedNotGuessed(t *testing.T) {
	v := newFeeViewFailing(t)
	if _, err := v.LiveFeeBps(context.Background(), 146, "USDT"); err == nil {
		t.Fatal("want error on unreadable chain")
	}
}

func TestTradeFeeSnapshotBps_SumsAndRejectsUnknown(t *testing.T) {
	if got, ok := feeBpsFromSnapshot(190, 40, true); !ok || got != 230 {
		t.Fatalf("got %d, %v", got, ok)
	}
	if _, ok := feeBpsFromSnapshot(0, 0, false); ok {
		t.Fatal("a failed read must yield no value")
	}
}
