package services

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

const (
	coreFC    = "0x00000000000000000000000000000000000FC001"
	coreOrder = "0x00000000000000000000000000000000000E5C02"
	coreP2P   = "0x00000000000000000000000000000000000E5C03"
	coreAdmin = "0xd963C3C596f692b4fD8e53bD9B60Ca1843400Da3"
	coreFees  = "0x00000000000000000000000000000000005E5701"
)

// fakeCoreChain answers the core set's views on one chain.
type fakeCoreChain struct {
	chainID   int64
	admin     common.Address
	feeConfig common.Address
	forwarder common.Address
	window    int64
	recipient common.Address
	pending   common.Address
	calls     atomic.Int64                               // the role reads run side by side
	roles     map[common.Address]map[common.Address]bool // contract → holder → holds any role
	rates     map[rateKey]feeConfigRateTuple
	limits    map[common.Address]feeConfigLimitsTuple
	noCode    bool
	rpcDown   bool
}

func healthyCoreChain() *fakeCoreChain {
	return &fakeCoreChain{chainID: 146, admin: common.HexToAddress(coreAdmin), feeConfig: common.HexToAddress(coreFC),
		window: 1800, recipient: common.HexToAddress(coreFees), roles: map[common.Address]map[common.Address]bool{},
		rates: map[rateKey]feeConfigRateTuple{}, limits: map[common.Address]feeConfigLimitsTuple{}}
}

func (f *fakeCoreChain) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	if f.rpcDown {
		return nil, errors.New("503 Service Unavailable")
	}
	f.calls.Add(1)
	m, fc := parsedCoreViewABI.Methods, parsedFeeConfigABI.Methods
	sel := call.Data[:4]
	switch {
	case bytes.Equal(sel, m["defaultAdmin"].ID):
		return m["defaultAdmin"].Outputs.Pack(f.admin)
	case bytes.Equal(sel, m["pendingDefaultAdmin"].ID):
		return m["pendingDefaultAdmin"].Outputs.Pack(f.pending, big.NewInt(0))
	case bytes.Equal(sel, m["feeConfig"].ID):
		return m["feeConfig"].Outputs.Pack(f.feeConfig)
	case bytes.Equal(sel, m["trustedForwarder"].ID):
		return m["trustedForwarder"].Outputs.Pack(f.forwarder)
	case bytes.Equal(sel, m["paymentWindow"].ID):
		return m["paymentWindow"].Outputs.Pack(big.NewInt(f.window))
	case bytes.Equal(sel, m["hasRole"].ID):
		args, _ := m["hasRole"].Inputs.Unpack(call.Data[4:])
		return m["hasRole"].Outputs.Pack(f.roles[*call.To][args[1].(common.Address)])
	case bytes.Equal(sel, fc["feeRecipient"].ID):
		return fc["feeRecipient"].Outputs.Pack(f.recipient)
	case bytes.Equal(sel, fc["rawRate"].ID):
		args, _ := fc["rawRate"].Inputs.Unpack(call.Data[4:])
		key := args[0].([32]byte)
		r := f.rates[rateKey{strings.TrimRight(string(key[:]), "\x00"), args[1].(common.Address)}]
		if r.FlatFee == nil {
			r.FlatFee = new(big.Int)
		}
		return fc["rawRate"].Outputs.Pack(r)
	case bytes.Equal(sel, fc["limitsFor"].ID):
		args, _ := fc["limitsFor"].Inputs.Unpack(call.Data[4:])
		l, ok := f.limits[args[0].(common.Address)]
		if !ok {
			return nil, errors.New("execution reverted: LimitsNotConfigured")
		}
		return fc["limitsFor"].Outputs.Pack(l)
	}
	return nil, errors.New("fakeCoreChain: unexpected call")
}
func (f *fakeCoreChain) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	return nil, errors.New("not used")
}
func (f *fakeCoreChain) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	if f.noCode {
		return nil, nil
	}
	return []byte{0x60}, nil
}
func (f *fakeCoreChain) ChainID(context.Context) (*big.Int, error) { return big.NewInt(f.chainID), nil }

func healthyCoreGates(chain *fakeCoreChain) CoreGatesConfig {
	withTx := map[string]map[string]any{"latest": {"number": "0x10", "transactions": []string{"0xaa"}}}
	return CoreGatesConfig{
		Set: config.CoreContracts{ChainID: 146, ChainKey: "sonic", FeeConfig: coreFC, OrderEscrow: coreOrder,
			P2PEscrow: coreP2P, Admin: coreAdmin},
		HotKeys: map[string]string{"operator": directOperator},
		Chain:   chain, Receipts: &fakeRawRPC{blocks: withTx, receipt: `{"status":"0x1"}`},
		Fees: NewFeeConfigReader(map[int64]FeeManagerRPCClient{146: chain}),
	}
}

// One failing case per gate; a healthy set passes them all.
func TestCoreGates_EachGate(t *testing.T) {
	if fails := CheckCoreGates(context.Background(), healthyCoreGates(healthyCoreChain())); len(fails) != 0 {
		t.Fatalf("healthy set failed: %v", fails)
	}
	operator := common.HexToAddress(directOperator)
	for _, tc := range []struct {
		gate   string
		break_ func(*CoreGatesConfig, *fakeCoreChain)
	}{
		{"bytecode", func(_ *CoreGatesConfig, f *fakeCoreChain) { f.noCode = true }},
		{"chain_id", func(_ *CoreGatesConfig, f *fakeCoreChain) { f.chainID = 14601 }},
		{"admin", func(_ *CoreGatesConfig, f *fakeCoreChain) { f.admin = operator }},
		{"admin", func(c *CoreGatesConfig, _ *fakeCoreChain) { c.HotKeys["deployer"] = coreAdmin }},
		{"admin", func(_ *CoreGatesConfig, f *fakeCoreChain) { f.pending = operator }},
		{"hot_key_role", func(_ *CoreGatesConfig, f *fakeCoreChain) {
			f.roles[common.HexToAddress(coreOrder)] = map[common.Address]bool{operator: true}
		}},
		{"hot_key_role", func(_ *CoreGatesConfig, f *fakeCoreChain) {
			f.roles[common.HexToAddress(coreFC)] = map[common.Address]bool{operator: true}
		}},
		{"fee_config", func(_ *CoreGatesConfig, f *fakeCoreChain) { f.feeConfig = common.HexToAddress(coreFees) }},
		{"trusted_forwarder", func(_ *CoreGatesConfig, f *fakeCoreChain) { f.forwarder = operator }},
		{"trusted_forwarder", func(c *CoreGatesConfig, _ *fakeCoreChain) { c.Set.Forwarder = coreFees }},
		{"payment_window", func(_ *CoreGatesConfig, f *fakeCoreChain) { f.window = 3600 }},
		{"fee_recipient", func(_ *CoreGatesConfig, f *fakeCoreChain) { f.recipient = common.Address{} }},
	} {
		chain := healthyCoreChain()
		c := healthyCoreGates(chain)
		tc.break_(&c, chain)
		fails := CheckCoreGates(context.Background(), c)
		found := false
		for _, f := range fails {
			if f.Gate == tc.gate && !f.Unread {
				found = true
			}
		}
		if !found {
			t.Errorf("gate %s: want a wrong-value failure, got %v", tc.gate, fails)
		}
	}
}

// An RPC that cannot answer is "unread", never "wrong", and the set is not ready either way.
func TestCoreGates_RPCDownIsUnread(t *testing.T) {
	chain := healthyCoreChain()
	chain.rpcDown = true
	fails := CheckCoreGates(context.Background(), healthyCoreGates(chain))
	if len(fails) == 0 {
		t.Fatal("an RPC that cannot answer must not pass the gates")
	}
	for _, f := range fails {
		if !f.Unread {
			t.Fatalf("%v: want unread", f)
		}
	}
}

// A handover pending to an address that is not a hot key passes (the PM taking over is expected).
func TestCoreGates_PendingHandoverToNonHotKeyPasses(t *testing.T) {
	chain := healthyCoreChain()
	chain.pending = common.HexToAddress(coreFees)
	if fails := CheckCoreGates(context.Background(), healthyCoreGates(chain)); len(fails) != 0 {
		t.Fatalf("fails = %v", fails)
	}
}

// One address under two labels is read once per role, not twice.
func TestCoreGates_HotKeysDeduplicated(t *testing.T) {
	once := healthyCoreChain()
	CheckCoreGates(context.Background(), healthyCoreGates(once))
	twice := healthyCoreChain()
	c := healthyCoreGates(twice)
	c.HotKeys["operator 0x38cE (production)"] = directOperator
	CheckCoreGates(context.Background(), c)
	if once.calls.Load() != twice.calls.Load() {
		t.Fatalf("calls %d with one label, %d with two for the same address", once.calls.Load(), twice.calls.Load())
	}
}

func testCoreRegistry(chain *fakeCoreChain, now *time.Time) *CoreRegistry {
	gates := healthyCoreGates(chain)
	return &CoreRegistry{setName: "dev", fees: gates.Fees, now: func() time.Time { return *now },
		chains: map[int64]*coreChain{146: {set: gates.Set, gates: gates}}}
}

// Ready follows the gates: a pass opens it, a wrong value found later closes it, an unread gate
// changes nothing, and a ready chain is re-gated only every coreGatesReadyRecheck.
func TestCoreRegistry_GateTransitions(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	chain := healthyCoreChain()
	r := testCoreRegistry(chain, &now)
	ready := func() bool { _, ok := r.Ready(146); return ok }

	chain.rpcDown = true
	if err := r.Gate(context.Background()); !errors.Is(err, ErrCoreSetNotReady) || ready() {
		t.Fatalf("unread before any pass: err=%v ready=%v, want not ready", err, ready())
	}
	chain.rpcDown = false
	if err := r.Gate(context.Background()); err != nil || !ready() {
		t.Fatalf("healthy: err=%v ready=%v", err, ready())
	}
	chain.forwarder = common.HexToAddress(directOperator)
	now = now.Add(time.Minute)
	if err := r.Gate(context.Background()); err != nil || !ready() {
		t.Fatalf("within the ready recheck window the chain is not re-gated: err=%v ready=%v", err, ready())
	}
	now = now.Add(coreGatesReadyRecheck)
	if err := r.Gate(context.Background()); !errors.Is(err, ErrCoreSetNotReady) || ready() {
		t.Fatalf("wrong value after ready: err=%v ready=%v, want out of service", err, ready())
	}
	chain.forwarder = common.Address{}
	if err := r.Gate(context.Background()); err != nil || !ready() {
		t.Fatalf("fixed on-chain: err=%v ready=%v, want ready again", err, ready())
	}
	chain.rpcDown = true
	now = now.Add(coreGatesReadyRecheck)
	if err := r.Gate(context.Background()); err != nil || !ready() {
		t.Fatalf("an RPC blip must not close a ready set: err=%v ready=%v", err, ready())
	}
}

// A nil (dormant) registry answers every call without panicking.
func TestCoreRegistry_NilIsDormant(t *testing.T) {
	var r *CoreRegistry
	if err := r.Gate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Ready(146); ok || r.Fees() != nil || r.SystemWallets() != nil {
		t.Fatal("a nil registry must be dormant")
	}
}

// The fee-wallet card reads each ready chain's feeRecipient, and says why when it cannot.
func TestCoreRegistry_SystemWallets(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	chain := healthyCoreChain()
	r := testCoreRegistry(chain, &now)
	if got := r.SystemWallets(); got[0].SystemWallet != "" || !strings.Contains(got[0].Reason, "gates") {
		t.Fatalf("before the gates pass = %+v, want unverified", got[0])
	}
	if err := r.Gate(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := r.SystemWallets()
	if len(got) != 1 || got[0].SystemWallet != common.HexToAddress(coreFees).Hex() || got[0].Source != FeeSinkSourceFeeRecipient ||
		got[0].Contract != coreFC || !strings.Contains(got[0].Label, "dev") {
		t.Fatalf("SystemWallets = %+v", got)
	}
	chain.rpcDown = true
	if got := r.SystemWallets(); got[0].SystemWallet != "" || got[0].Reason == "" {
		t.Fatalf("unreadable recipient = %+v, want a reason and no wallet", got[0])
	}
}
