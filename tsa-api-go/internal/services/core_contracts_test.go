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
	roles     map[common.Address]map[common.Address]bool // contract → holder → holds any role
	noCode    bool
	rpcDown   bool
}

func healthyCoreChain() *fakeCoreChain {
	return &fakeCoreChain{chainID: 146, admin: common.HexToAddress(coreAdmin), feeConfig: common.HexToAddress(coreFC),
		window: 1800, recipient: common.HexToAddress(coreFees), roles: map[common.Address]map[common.Address]bool{}}
}

func (f *fakeCoreChain) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	if f.rpcDown {
		return nil, errors.New("503 Service Unavailable")
	}
	m, fc := parsedCoreViewABI.Methods, parsedFeeConfigABI.Methods
	sel := call.Data[:4]
	switch {
	case bytes.Equal(sel, m["defaultAdmin"].ID):
		return m["defaultAdmin"].Outputs.Pack(f.admin)
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

// A nil (dormant) registry answers every call without panicking.
func TestCoreRegistry_NilIsDormant(t *testing.T) {
	var r *CoreRegistry
	r.Start()
	if _, ok := r.Ready(146); ok || r.Fees() != nil || r.SystemWallets() != nil {
		t.Fatal("a nil registry must be dormant")
	}
}

// The fee-wallet card reads each chain's feeRecipient, and says why when it cannot.
func TestCoreRegistry_SystemWallets(t *testing.T) {
	chain := healthyCoreChain()
	gates := healthyCoreGates(chain)
	r := &CoreRegistry{setName: "dev", fees: gates.Fees, chains: map[int64]*coreChain{146: {set: gates.Set, gates: gates}}}
	got := r.SystemWallets()
	if len(got) != 1 || got[0].SystemWallet != common.HexToAddress(coreFees).Hex() || got[0].Source != FeeSinkSourceFeeRecipient ||
		got[0].Contract != coreFC || !strings.Contains(got[0].Label, "dev") {
		t.Fatalf("SystemWallets = %+v", got)
	}
	if _, ok := r.Ready(146); ok {
		t.Fatal("a chain is not ready before its gates pass")
	}
	chain.rpcDown = true
	if got := r.SystemWallets(); got[0].SystemWallet != "" || got[0].Reason == "" {
		t.Fatalf("unreadable recipient = %+v, want a reason and no wallet", got[0])
	}
}
