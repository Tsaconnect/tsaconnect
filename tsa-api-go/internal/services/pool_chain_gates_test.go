package services

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

const (
	gateAdmin = "0x00000000000000000000000000000000000AD000"
	gateUSDT  = "0x55d398326f99059fF775485246999027B3197955"
)

// fakePoolChain answers the P2P escrow views on top of fakeEscrowChain's
// feeManager/tokenSymbol/rateFor.
type fakePoolChain struct {
	fakeEscrowChain
	feeBps     uint16
	window     int64
	admins     map[common.Address]bool
	noCode     map[common.Address]bool
	balance    *big.Int
	balanceErr error
	gasPrice   *big.Int // nil = 1 gwei
}

func (f *fakePoolChain) SuggestGasPrice(context.Context) (*big.Int, error) {
	if f.gasPrice == nil {
		return big.NewInt(1e9), nil
	}
	return f.gasPrice, nil
}

func (f *fakePoolChain) CallContract(ctx context.Context, call ethereum.CallMsg, n *big.Int) ([]byte, error) {
	m := parsedP2PEscrowABI.Methods
	sel := call.Data[:4]
	switch {
	case bytes.Equal(sel, m["feeBps"].ID):
		return m["feeBps"].Outputs.Pack(f.feeBps)
	case bytes.Equal(sel, m["paymentWindow"].ID):
		return m["paymentWindow"].Outputs.Pack(big.NewInt(f.window))
	case bytes.Equal(sel, m["isAdmin"].ID):
		args, _ := m["isAdmin"].Inputs.Unpack(call.Data[4:])
		return m["isAdmin"].Outputs.Pack(f.admins[args[0].(common.Address)])
	}
	return f.fakeEscrowChain.CallContract(ctx, call, n)
}
func (f *fakePoolChain) CodeAt(_ context.Context, a common.Address, _ *big.Int) ([]byte, error) {
	if f.noCode[a] {
		return nil, nil
	}
	return []byte{0x60}, nil
}
func (f *fakePoolChain) BalanceAt(context.Context, common.Address, *big.Int) (*big.Int, error) {
	return f.balance, f.balanceErr
}

func healthyPoolChain() *fakePoolChain {
	esc := withFeeManager(map[string][4]interface{}{
		"p2p_platform": {uint16(0), uint64(0), false, true},
		"p2p":          {uint16(0), uint64(0), false, true},
	})
	esc.symbol = "USDT"
	return &fakePoolChain{fakeEscrowChain: esc, window: 3600,
		admins: map[common.Address]bool{common.HexToAddress(gateAdmin): true}, balance: big.NewInt(5e16)}
}

func healthyGates(chain *fakePoolChain) PoolChainGatesConfig {
	withTx := map[string]map[string]any{"latest": {"number": "0x10", "transactions": []string{"0xaa"}}}
	return PoolChainGatesConfig{
		ChainID: 56, ChainKey: "bsc", Network: "mainnet", Escrow: testEscrow, FeeManager: testFeeManager,
		Tokens: map[string]string{"USDT": gateUSDT}, Decimals: map[string]int{"USDT": 18},
		ReleaseAdmin: gateAdmin, ReleaseGasFloorWei: big.NewInt(1e16), Chain: chain,
		Receipts: &fakeRawRPC{blocks: withTx, receipt: `{"status":"0x1"}`},
		TreasuryLane: &TreasuryLane{SponsorLane: &SponsorLane{ChainKey: "bsc", Tokens: map[string]string{"USDT": gateUSDT}},
			ChainID: 56, EscrowCalls: []common.Address{common.HexToAddress(testEscrow)}},
		CompanyPM:           func(context.Context) error { return nil },
		MinPaymentWindowSec: 3600,
	}
}

func TestPoolGates_AllPassOpens(t *testing.T) {
	g := NewPoolChainGates(healthyGates(healthyPoolChain()))
	st := g.Check(context.Background())
	if !st.Open || len(st.Failures) != 0 || !g.Open() {
		t.Fatalf("status = %+v", st)
	}
}

func assertClosedBy(t *testing.T, cfg PoolChainGatesConfig, gate string) {
	t.Helper()
	g := NewPoolChainGates(cfg)
	st := g.Check(context.Background())
	if st.Open || g.Open() {
		t.Fatalf("pool open, want closed by %s: %+v", gate, st)
	}
	for _, f := range st.Failures {
		if strings.HasPrefix(f, gate+":") {
			return
		}
	}
	t.Fatalf("failures = %v, want one from gate %q", st.Failures, gate)
}

// Review Focus 4: an inactive row falls back to legacy feeBps on-chain (S2).
func TestPoolGates_InactiveFeeRowClosesPool(t *testing.T) {
	c := healthyPoolChain()
	c.rates["p2p"] = [4]interface{}{uint16(0), uint64(0), false, false}
	assertClosedBy(t, healthyGates(c), "fee_invariant")
}

func TestPoolGates_NonZeroLegacyFeeBpsClosesPool(t *testing.T) {
	c := healthyPoolChain()
	c.feeBps = 50
	assertClosedBy(t, healthyGates(c), "fee_invariant")
}

func TestPoolGates_NonZeroRowClosesPool(t *testing.T) {
	c := healthyPoolChain()
	c.rates["p2p_platform"] = [4]interface{}{uint16(100), uint64(0), false, true}
	assertClosedBy(t, healthyGates(c), "fee_invariant")
}

func TestPoolGates_WrongFeeManagerClosesPool(t *testing.T) {
	cfg := healthyGates(healthyPoolChain())
	cfg.FeeManager = "0x00000000000000000000000000000000000FEE99"
	assertClosedBy(t, cfg, "fee_invariant")
}

func TestPoolGates_MissingBytecodeClosesPool(t *testing.T) {
	c := healthyPoolChain()
	c.noCode = map[common.Address]bool{common.HexToAddress(testFeeManager): true}
	assertClosedBy(t, healthyGates(c), "bytecode")
}

func TestPoolGates_ShortPaymentWindowClosesPool(t *testing.T) {
	c := healthyPoolChain()
	c.window = 1800
	assertClosedBy(t, healthyGates(c), "payment_window")
}

func TestPoolGates_ReleaseKeyNotAdminClosesPool(t *testing.T) {
	cfg := healthyGates(healthyPoolChain())
	cfg.ReleaseAdmin = "0x00000000000000000000000000000000000BAD00"
	assertClosedBy(t, cfg, "release_admin")
	cfg.ReleaseAdmin = ""
	assertClosedBy(t, cfg, "release_admin")
}

func TestPoolGates_TreasuryLaneGate(t *testing.T) {
	cfg := healthyGates(healthyPoolChain())
	cfg.TreasuryLane = nil
	assertClosedBy(t, cfg, "treasury_lane")

	cfg = healthyGates(healthyPoolChain())
	cfg.TreasuryLane.EscrowCalls = nil
	assertClosedBy(t, cfg, "treasury_lane")

	cfg = healthyGates(healthyPoolChain())
	cfg.TreasuryLane.Routers = []common.Address{common.HexToAddress(testEscrow)} // a LiFi route could call it
	assertClosedBy(t, cfg, "treasury_lane")

	cfg = healthyGates(healthyPoolChain())
	cfg.TreasuryLane.SponsorLane.Tokens = map[string]string{}
	assertClosedBy(t, cfg, "treasury_lane")
}

func TestPoolGates_DecimalsGate(t *testing.T) {
	cfg := healthyGates(healthyPoolChain())
	cfg.Decimals = map[string]int{} // dropped by the registry (unreadable)
	assertClosedBy(t, cfg, "decimals")
	cfg.Decimals = map[string]int{"USDT": 4}
	assertClosedBy(t, cfg, "decimals")
}

func TestPoolGates_ReceiptsUnreadableClosesPoolAndReprobeReopens(t *testing.T) {
	cfg := healthyGates(healthyPoolChain())
	rpc := cfg.Receipts.(*fakeRawRPC)
	rpc.receiptErr = errors.New("403 Forbidden")
	g := NewPoolChainGates(cfg)
	if g.Check(context.Background()).Open {
		t.Fatal("unreadable receipts must close the pool (S38)")
	}
	rpc.receiptErr = nil
	g.ReprobeReceipts(context.Background())
	if !g.Open() {
		t.Fatalf("a passing re-probe must reopen: %+v", g.Status())
	}
}

func TestPoolGates_CompanyPMGate(t *testing.T) {
	cfg := healthyGates(healthyPoolChain())
	cfg.CompanyPM = func(context.Context) error { return errors.New("IT_PM_ID is not an active payment merchant") }
	assertClosedBy(t, cfg, "company_pm")
}

func TestPoolGates_CloseLatchesUntilRestart(t *testing.T) {
	g := NewPoolChainGates(healthyGates(healthyPoolChain()))
	g.Check(context.Background())
	g.Close("liquidity_pool_fee_invariant_broken on slice 0xabc")
	if g.Open() {
		t.Fatal("closed")
	}
	if st := g.Check(context.Background()); st.Open || st.Latched == "" {
		t.Fatalf("a re-check must not clear a latch: %+v", st)
	}
}

// Floors are alert-only (owner 2026-10-13): below the 0.01 floor a release key
// that can pay this order's releases keeps the pool open (and alerts); one
// that cannot is refused.
func TestPoolGates_ReleaseGasIsALiveFeeCheckNotTheFloor(t *testing.T) {
	c := healthyPoolChain()
	c.balance = big.NewInt(5e15) // below the 0.01 floor
	g := NewPoolChainGates(healthyGates(c))
	// 0.005 BNB at 1 gwei pays 4 releases (4 × 300k × 1 gwei × 2 = 0.0024).
	if !g.ReleaseGasOK(context.Background(), 4) {
		t.Fatal("below the floor but able to pay its releases: the pool must stay open")
	}
	c.gasPrice = big.NewInt(5e9)           // 4 × 300k × 5 gwei × 2 = 0.012 > 0.005
	g = NewPoolChainGates(healthyGates(c)) // a fresh reading (they are reused for a few seconds)
	if g.ReleaseGasOK(context.Background(), 4) {
		t.Fatal("a key that cannot pay this order's releases must refuse the open")
	}
	fl, err := g.FloatBalances(context.Background())
	if err != nil || len(fl) != 1 || fl[0].Chain != "bsc" || fl[0].FloorWei.Cmp(big.NewInt(1e16)) != 0 || fl[0].Address != gateAdmin {
		t.Fatalf("floats = %+v err = %v", fl, err)
	}
}

// A half-built config fails closed with a reason; it never panics or opens.
func TestPoolGates_MissingChainOrTokensFailsClosed(t *testing.T) {
	cfg := healthyGates(healthyPoolChain())
	cfg.Chain = nil
	assertClosedBy(t, cfg, "bytecode")
	if st := NewPoolChainGates(cfg).Check(context.Background()); len(st.Failures) != 1 {
		t.Fatalf("no client must close at once, not run the chain gates: %v", st.Failures)
	}
	if g := NewPoolChainGates(cfg); g.ReleaseGasOK(context.Background(), 1) {
		t.Fatal("no chain client: release gas must not read as OK")
	}

	cfg = healthyGates(healthyPoolChain())
	cfg.Tokens = nil
	assertClosedBy(t, cfg, "fee_invariant")

	g := NewPoolChainGates(healthyGates(healthyPoolChain()))
	g.Check(context.Background())
	g.Close("")
	if g.Open() || g.Check(context.Background()).Open {
		t.Fatal("an empty close reason must still latch")
	}
}

// Review I2: a nil *PoolChainGates — held directly or in a PoolGate — reads
// closed on every method and never panics.
func TestPoolGates_NilReceiverReadsClosed(t *testing.T) {
	var g *PoolChainGates
	var pg PoolGate = g
	ctx := context.Background()
	for name, gate := range map[string]PoolGate{"pointer": g, "interface": pg} {
		if gate.Open() {
			t.Fatalf("%s: Open", name)
		}
		if st := gate.Status(); st.Open || len(st.Failures) == 0 {
			t.Fatalf("%s: Status = %+v, want closed with a reason", name, st)
		}
		if st := gate.Check(ctx); st.Open || len(st.Failures) == 0 {
			t.Fatalf("%s: Check = %+v, want closed with a reason", name, st)
		}
		gate.Close("fee snapshot")
		gate.ReprobeReceipts(ctx)
		if gate.Enabled() || gate.ReleaseGasOK(ctx, 1) {
			t.Fatalf("%s: release float must read as off", name)
		}
		if fl, err := gate.FloatBalances(ctx); err == nil || fl != nil {
			t.Fatalf("%s: FloatBalances = %v, %v; want an error", name, fl, err)
		}
	}
}

// Review M1: an RPC error naming a keyed URL never leaves FloatBalances whole.
func TestPoolGates_FloatBalancesRedactsTheRPCError(t *testing.T) {
	c := healthyPoolChain()
	c.balanceErr = errors.New(`Post "https://bnb-mainnet.g.alchemy.com/v2/SECRETKEY123": dial tcp: i/o timeout`)
	_, err := NewPoolChainGates(healthyGates(c)).FloatBalances(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRETKEY123") {
		t.Fatalf("err = %v, want the RPC URL redacted", err)
	}
}
