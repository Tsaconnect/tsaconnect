package services

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

const (
	directEscrow   = "0x00000000000000000000000000000000000E5C01"
	directFM       = "0x00000000000000000000000000000000000FEE01"
	directOwner    = "0x62bA000000000000000000000000000000001b96"
	directArbiter  = "0x00000000000000000000000000000000000A7B17"
	directOperator = "0x38cE9a4C9190707829950B7Ae4Eed1d254c2e2c9"
	directSystem   = "0x00000000000000000000000000000000005E5700"
)

// fakeDirectChain answers a direct escrow's views from the P2P escrow ABI.
type fakeDirectChain struct {
	chainID    int64
	feeManager common.Address
	accepted   map[common.Address]bool
	admins     map[common.Address]bool
	system     common.Address
	forwarder  common.Address
	owner      common.Address
	fmOwner    common.Address
	noCode     bool
	rpcDown    bool
}

func healthyDirectChain() *fakeDirectChain {
	return &fakeDirectChain{chainID: 56, feeManager: common.HexToAddress(directFM),
		accepted: map[common.Address]bool{common.HexToAddress(gateUSDT): true},
		admins:   map[common.Address]bool{common.HexToAddress(directArbiter): true},
		system:   common.HexToAddress(directSystem), owner: common.HexToAddress(directOwner),
		fmOwner: common.HexToAddress(directOwner)}
}

func (f *fakeDirectChain) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	if f.rpcDown {
		return nil, errors.New("503 Service Unavailable")
	}
	m := parsedP2PEscrowABI.Methods
	sel := call.Data[:4]
	arg := func(name string) common.Address {
		args, _ := m[name].Inputs.Unpack(call.Data[4:])
		return args[0].(common.Address)
	}
	switch {
	case bytes.Equal(sel, m["feeManager"].ID):
		return m["feeManager"].Outputs.Pack(f.feeManager)
	case bytes.Equal(sel, m["acceptedTokens"].ID):
		return m["acceptedTokens"].Outputs.Pack(f.accepted[arg("acceptedTokens")])
	case bytes.Equal(sel, m["isAdmin"].ID):
		return m["isAdmin"].Outputs.Pack(f.admins[arg("isAdmin")])
	case bytes.Equal(sel, m["systemWallet"].ID):
		return m["systemWallet"].Outputs.Pack(f.system)
	case bytes.Equal(sel, m["trustedForwarder"].ID):
		return m["trustedForwarder"].Outputs.Pack(f.forwarder)
	case bytes.Equal(sel, m["owner"].ID):
		if call.To != nil && *call.To == common.HexToAddress(directFM) {
			return m["owner"].Outputs.Pack(f.fmOwner)
		}
		return m["owner"].Outputs.Pack(f.owner)
	}
	return nil, errors.New("fakeDirectChain: unexpected call")
}
func (f *fakeDirectChain) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	if f.noCode {
		return nil, nil
	}
	return []byte{0x60}, nil
}
func (f *fakeDirectChain) ChainID(context.Context) (*big.Int, error) {
	return big.NewInt(f.chainID), nil
}

// windowEscrow is a mock escrow that reports a payment window.
type windowEscrow struct {
	*mockEscrow
	window time.Duration
}

func (w windowEscrow) ReadPaymentWindow() (time.Duration, error) { return w.window, nil }

func healthyDirectGates(chain *fakeDirectChain) DirectEscrowGatesConfig {
	withTx := map[string]map[string]any{"latest": {"number": "0x10", "transactions": []string{"0xaa"}}}
	return DirectEscrowGatesConfig{
		ChainID: 56, ChainKey: "bsc", Escrow: directEscrow, FeeManager: directFM, ExpectedOwner: directOwner,
		Tokens: map[string]string{"USDT": gateUSDT}, Decimals: map[string]int{"USDT": 18},
		ForbiddenAdmins: map[string]string{"operator": directOperator},
		Adapter:         windowEscrow{newMockEscrow(), 30 * time.Minute}, Chain: chain,
		Receipts: &fakeRawRPC{blocks: withTx, receipt: `{"status":"0x1"}`},
	}
}

// Plan API-9: one failing case per gate; a healthy escrow passes them all.
func TestDirectEscrowGates_EachGate(t *testing.T) {
	if fails := CheckDirectEscrowGates(context.Background(), healthyDirectGates(healthyDirectChain())); len(fails) != 0 {
		t.Fatalf("healthy escrow failed: %v", fails)
	}
	for _, tc := range []struct {
		gate   string
		break_ func(*DirectEscrowGatesConfig, *fakeDirectChain)
	}{
		{"bytecode", func(_ *DirectEscrowGatesConfig, f *fakeDirectChain) { f.noCode = true }},
		{"chain_id", func(_ *DirectEscrowGatesConfig, f *fakeDirectChain) { f.chainID = 97 }},
		{"payment_window", func(c *DirectEscrowGatesConfig, _ *fakeDirectChain) {
			c.Adapter = windowEscrow{newMockEscrow(), time.Hour}
		}},
		{"payment_window", func(c *DirectEscrowGatesConfig, _ *fakeDirectChain) { c.Adapter = newMockEscrow() }},
		{"fee_manager", func(_ *DirectEscrowGatesConfig, f *fakeDirectChain) { f.feeManager = common.HexToAddress(directSystem) }},
		{"fee_manager", func(c *DirectEscrowGatesConfig, _ *fakeDirectChain) { c.FeeManager = "" }},
		{"token_accepted", func(_ *DirectEscrowGatesConfig, f *fakeDirectChain) { f.accepted = nil }},
		{"decimals", func(c *DirectEscrowGatesConfig, _ *fakeDirectChain) { c.Decimals = map[string]int{"USDT": 6} }},
		{"decimals", func(c *DirectEscrowGatesConfig, _ *fakeDirectChain) { c.Decimals = nil }},
		{"forbidden_admin", func(_ *DirectEscrowGatesConfig, f *fakeDirectChain) {
			f.admins[common.HexToAddress(directOperator)] = true
		}},
		{"system_wallet", func(_ *DirectEscrowGatesConfig, f *fakeDirectChain) { f.system = common.Address{} }},
		{"trusted_forwarder", func(_ *DirectEscrowGatesConfig, f *fakeDirectChain) { f.forwarder = common.HexToAddress(directSystem) }},
		{"owner", func(_ *DirectEscrowGatesConfig, f *fakeDirectChain) { f.owner = common.HexToAddress(directOperator) }},
		{"owner", func(c *DirectEscrowGatesConfig, _ *fakeDirectChain) { c.ExpectedOwner = "" }},
		{"owner", func(c *DirectEscrowGatesConfig, f *fakeDirectChain) {
			c.ExpectedOwner, f.owner = directOperator, common.HexToAddress(directOperator) // a hot key as owner
		}},
		{"receipts", func(c *DirectEscrowGatesConfig, _ *fakeDirectChain) { c.Receipts = nil }},
		{"fee_manager_owner", func(_ *DirectEscrowGatesConfig, f *fakeDirectChain) { f.fmOwner = common.HexToAddress(directOperator) }},
	} {
		chain := healthyDirectChain()
		c := healthyDirectGates(chain)
		tc.break_(&c, chain)
		fails := CheckDirectEscrowGates(context.Background(), c)
		found := false
		for _, f := range fails {
			found = found || strings.HasPrefix(f.String(), tc.gate+":")
		}
		if !found {
			t.Errorf("%s: not caught, failures = %v", tc.gate, fails)
		}
	}
}

// A failed gate unregisters only that chain's escrow: new activity and exits
// there fail loud, every other chain is untouched.
func TestDirectEscrowGates_FailureLeavesTheChainDormant(t *testing.T) {
	reg := NewP2PEscrowRegistryFromMap(map[int64]P2PEscrowAdapter{146: newMockEscrow(), 56: newMockEscrow()}, "2")
	if reg.applyDirectGates(56, "bsc", []DirectGateFailure{{Gate: "owner", Reason: "wrong"}}) {
		t.Fatal("a failure must not pass")
	}
	if reg.HasChain(56) || !reg.HasChain(146) {
		t.Fatalf("chains = %v, want Sonic only", reg.ChainIDs())
	}
	if _, err := reg.For(56); err == nil {
		t.Fatal("the dormant chain must fail loud")
	}
}

// A chain that cannot be read is unknown, not wrong: every failure is Unread,
// and a wrong value is never reported as Unread.
func TestDirectEscrowGates_RPCDownIsUnreadOnly(t *testing.T) {
	chain := healthyDirectChain()
	chain.rpcDown = true
	fails := CheckDirectEscrowGates(context.Background(), healthyDirectGates(chain))
	if len(fails) == 0 {
		t.Fatal("an unreadable escrow must not pass")
	}
	for _, f := range fails {
		if !f.Unread {
			t.Errorf("%s: an RPC failure reported as a wrong value", f)
		}
	}
	wrong := healthyDirectChain()
	wrong.owner = common.HexToAddress(directOperator)
	for _, f := range CheckDirectEscrowGates(context.Background(), healthyDirectGates(wrong)) {
		if f.Unread {
			t.Errorf("%s: a wrong value reported as unread", f)
		}
	}
}

// Only unread gates: the escrow stays registered so live trades keep their
// exits, closed to new activity until a re-check passes. A wrong value on
// the re-check keeps it closed.
func TestDirectEscrowGates_UnreadKeepsExitsAndRecheckOpens(t *testing.T) {
	reg := NewP2PEscrowRegistryFromMap(map[int64]P2PEscrowAdapter{146: newMockEscrow(), 56: newMockEscrow()}, "2")
	unread := []DirectGateFailure{{Gate: "receipts", Reason: "403", Unread: true}}
	if reg.applyDirectGates(56, "bsc", unread) {
		t.Fatal("an unread gate must not pass")
	}
	if _, err := reg.For(56); err != nil || !reg.Unverified(56) {
		t.Fatalf("want registered and unverified, got err=%v unverified=%t", err, reg.Unverified(56))
	}
	if reg.recheckDirectGates(56, "bsc", []DirectGateFailure{{Gate: "owner", Reason: "wrong"}}) || !reg.Unverified(56) {
		t.Fatal("a wrong value on re-check must keep the escrow exits-only")
	}
	if !reg.recheckDirectGates(56, "bsc", nil) || reg.Unverified(56) {
		t.Fatal("a clean re-check must open the escrow")
	}
	if reg.Unverified(146) {
		t.Fatal("other chains are untouched")
	}
}
