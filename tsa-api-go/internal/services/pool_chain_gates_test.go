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
	gateAdmin    = "0x00000000000000000000000000000000000AD000"
	gateUSDT     = "0x55d398326f99059fF775485246999027B3197955"
	gateVault    = "0x00000000000000000000000000000000000BA017"
	gateTreasury = "0x7E5a000000000000000000000000000000000001"
)

// fakePoolChain answers the vault's views (token, operators, recipient,
// paused), bytecode and the operator's balance.
type fakePoolChain struct {
	token      common.Address
	operators  map[common.Address]bool
	recipients map[common.Address]bool
	paused     bool
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

func (f *fakePoolChain) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	m := parsedLiquidityVaultABI.Methods
	sel := call.Data[:4]
	arg := func(name string) common.Address {
		args, _ := m[name].Inputs.Unpack(call.Data[4:])
		return args[0].(common.Address)
	}
	switch {
	case bytes.Equal(sel, m["token"].ID):
		return m["token"].Outputs.Pack(f.token)
	case bytes.Equal(sel, m["operators"].ID):
		return m["operators"].Outputs.Pack(f.operators[arg("operators")])
	case bytes.Equal(sel, m["recipient"].ID):
		return m["recipient"].Outputs.Pack(f.recipients[arg("recipient")])
	case bytes.Equal(sel, m["paused"].ID):
		return m["paused"].Outputs.Pack(f.paused)
	}
	return nil, errors.New("fakePoolChain: unexpected call")
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
	return &fakePoolChain{token: common.HexToAddress(gateUSDT),
		operators:  map[common.Address]bool{common.HexToAddress(gateAdmin): true},
		recipients: map[common.Address]bool{common.HexToAddress(gateTreasury): true},
		noCode:     map[common.Address]bool{}, balance: big.NewInt(5e16)}
}

type gateSigner struct{}

func (gateSigner) Address() string { return gateTreasury }
func (gateSigner) SignAndBroadcast(context.Context, []byte) (string, error) {
	return "", errors.New("unused")
}

func healthyGates(chain *fakePoolChain) PoolChainGatesConfig {
	withTx := map[string]map[string]any{"latest": {"number": "0x10", "transactions": []string{"0xaa"}}}
	return PoolChainGatesConfig{
		ChainID: 56, ChainKey: "bsc", Network: "mainnet", Vault: gateVault,
		Tokens: map[string]string{"USDT": gateUSDT}, Decimals: map[string]int{"USDT": 18},
		ReleaseAdmin: gateAdmin, ReleaseGasFloorWei: big.NewInt(1e16), Chain: chain,
		Receipts: &fakeRawRPC{blocks: withTx, receipt: `{"status":"0x1"}`},
		TreasuryLane: &TreasuryLane{SponsorLane: &SponsorLane{ChainKey: "bsc", Signer: gateSigner{},
			Tokens: map[string]string{"USDT": gateUSDT}}, ChainID: 56},
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
	if !g.ReleaseGasOK(context.Background(), 4, false) {
		t.Fatal("below the floor but able to pay its releases: the pool must stay open")
	}
	c.gasPrice = big.NewInt(5e9)           // 4 × 300k × 5 gwei × 2 = 0.012 > 0.005
	g = NewPoolChainGates(healthyGates(c)) // a fresh reading (they are reused for a few seconds)
	if g.ReleaseGasOK(context.Background(), 4, false) {
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
	if g := NewPoolChainGates(cfg); g.ReleaseGasOK(context.Background(), 1, false) {
		t.Fatal("no chain client: release gas must not read as OK")
	}

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
		if gate.Enabled() || gate.ReleaseGasOK(ctx, 1, false) {
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

func TestPoolGates_VaultWiring(t *testing.T) {
	c := healthyPoolChain()
	c.noCode[common.HexToAddress(gateVault)] = true
	assertClosedBy(t, healthyGates(c), "bytecode")

	c = healthyPoolChain()
	c.token = common.HexToAddress("0x00000000000000000000000000000000000000ee")
	assertClosedBy(t, healthyGates(c), "vault")

	c = healthyPoolChain()
	c.recipients = map[common.Address]bool{}
	assertClosedBy(t, healthyGates(c), "vault")

	c = healthyPoolChain()
	c.paused = true
	assertClosedBy(t, healthyGates(c), "vault")
}

// The operator key must be one of the vault's operators.
func TestPoolGates_OperatorGate(t *testing.T) {
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
	cfg.TreasuryLane.SponsorLane.Tokens = map[string]string{}
	assertClosedBy(t, cfg, "treasury_lane")
}
