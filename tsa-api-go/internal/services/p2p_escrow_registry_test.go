package services

import (
	"testing"
)

func TestNewP2PEscrowServiceForChain_UsesExplicitAddress(t *testing.T) {
	s := NewP2PEscrowServiceForChain(nil, nil, "0xBSCescrow")
	if s.ContractAddress() != "0xBSCescrow" {
		t.Errorf("ContractAddress() = %q, want 0xBSCescrow", s.ContractAddress())
	}
}

func TestP2PEscrowRegistry_For_HitAndFailLoudMiss(t *testing.T) {
	reg := &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{
		146: {service: NewP2PEscrowServiceForChain(nil, nil, "0xSonic"), eip712Version: "1"},
	}}

	got, err := reg.For(146)
	if err != nil {
		t.Fatalf("For(146) error = %v, want nil", err)
	}
	if got.ContractAddress() != "0xSonic" {
		t.Errorf("For(146) escrow = %q, want 0xSonic", got.ContractAddress())
	}

	// Fail-loud: an unconfigured chain must ERROR, never default to another chain.
	if _, err := reg.For(56); err == nil {
		t.Errorf("For(56) on unconfigured chain returned nil error — must fail loud")
	}
}

func TestP2PEscrowRegistry_EIP712VersionFor(t *testing.T) {
	reg := &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{
		146: {service: NewP2PEscrowServiceForChain(nil, nil, "0xSonic"), eip712Version: "1"},
	}}
	v, err := reg.EIP712VersionFor(146)
	if err != nil || v != "1" {
		t.Errorf("EIP712VersionFor(146) = (%q,%v), want (\"1\",nil)", v, err)
	}
	if _, err := reg.EIP712VersionFor(56); err == nil {
		t.Errorf("EIP712VersionFor(56) must fail loud on unconfigured chain")
	}
}

func TestP2PEscrowRegistry_Resolve(t *testing.T) {
	esc := NewP2PEscrowServiceForChain(nil, nil, "0xSonic") // a P2PEscrowAdapter
	reg := &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{
		146: {service: esc, eip712Version: "2", domainChainID: 146},
	}}
	got, domainID, ver, err := reg.Resolve(146)
	if err != nil {
		t.Fatalf("Resolve(146) err=%v", err)
	}
	if got.ContractAddress() != "0xSonic" || domainID != 146 || ver != "2" {
		t.Errorf("Resolve(146) = (%q,%d,%q), want (0xSonic,146,2)", got.ContractAddress(), domainID, ver)
	}
	if _, _, _, err := reg.Resolve(56); err == nil {
		t.Errorf("Resolve(56) on unconfigured chain must fail loud")
	}
}

// TestP2PEscrowRegistryFromMap_StampsDomainChainID verifies the test/ops seam
// builds entries with domainChainID == chainID and resolves them.
func TestP2PEscrowRegistryFromMap_StampsDomainChainID(t *testing.T) {
	reg := NewP2PEscrowRegistryFromMap(map[int64]P2PEscrowAdapter{
		146: NewP2PEscrowServiceForChain(nil, nil, "0xSonic"),
	}, "1")
	got, domainID, ver, err := reg.Resolve(146)
	if err != nil {
		t.Fatalf("Resolve(146) err=%v", err)
	}
	if got.ContractAddress() != "0xSonic" || domainID != 146 || ver != "1" {
		t.Errorf("Resolve(146) = (%q,%d,%q), want (0xSonic,146,1)", got.ContractAddress(), domainID, ver)
	}
	if _, _, _, err := reg.Resolve(56); err == nil {
		t.Errorf("Resolve(56) on unconfigured chain must fail loud")
	}
}

func TestP2PEscrowRegistry_NilSafety(t *testing.T) {
	var r *P2PEscrowRegistry
	if _, err := r.For(146); err == nil {
		t.Errorf("nil registry For must error, not panic/nil")
	}
	if ids := r.ChainIDs(); ids != nil {
		t.Errorf("nil registry ChainIDs = %v, want nil", ids)
	}
	// nil bs/cfg must not panic and yields an empty (usable) registry.
	reg := NewP2PEscrowRegistry(nil, nil, nil)
	if reg == nil {
		t.Fatalf("NewP2PEscrowRegistry(nil, nil, nil) returned nil")
	}
	if _, err := reg.For(146); err == nil {
		t.Errorf("empty registry For must fail loud")
	}
}

func TestP2PEscrowRegistry_Chains(t *testing.T) {
	reg := &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{
		146: {service: NewP2PEscrowServiceForChain(nil, nil, "0xSonic"), eip712Version: "1", domainChainID: 146, assets: []string{"MCGP"}},
		56:  {service: NewP2PEscrowServiceForChain(nil, nil, "0xBSC"), eip712Version: "2", domainChainID: 56, assets: []string{"USDT", "USDC"}},
	}}

	chains := reg.Chains()
	if len(chains) != 2 {
		t.Fatalf("Chains() len = %d, want 2", len(chains))
	}
	// Sorted by chainId ascending: 56 first, 146 second.
	if chains[0].ChainID != 56 {
		t.Errorf("chains[0].ChainID = %d, want 56", chains[0].ChainID)
	}
	if chains[1].ChainID != 146 {
		t.Errorf("chains[1].ChainID = %d, want 146", chains[1].ChainID)
	}
	// Assets are passed through unmodified.
	if len(chains[0].Assets) != 2 {
		t.Errorf("chains[0] (BSC) assets = %v, want [USDT USDC]", chains[0].Assets)
	}
	if len(chains[1].Assets) != 1 || chains[1].Assets[0] != "MCGP" {
		t.Errorf("chains[1] (Sonic) assets = %v, want [MCGP]", chains[1].Assets)
	}

	// nil registry must return nil (not panic).
	var r *P2PEscrowRegistry
	if r.Chains() != nil {
		t.Errorf("nil registry Chains() must return nil")
	}
}

func TestP2PEscrowRegistry_AssetAllowed(t *testing.T) {
	reg := &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{
		146: {service: NewP2PEscrowServiceForChain(nil, nil, "0xSonic"), eip712Version: "1", domainChainID: 146, assets: []string{"USDT", "USDC", "MCGP"}},
		56:  {service: NewP2PEscrowServiceForChain(nil, nil, "0xBSC"), eip712Version: "2", domainChainID: 56, assets: []string{"USDT", "USDC"}},
	}}
	if !reg.AssetAllowed(146, "MCGP") {
		t.Errorf("MCGP must be allowed on Sonic")
	}
	if reg.AssetAllowed(56, "MCGP") {
		t.Errorf("MCGP must NOT be allowed on BSC")
	}
	if !reg.AssetAllowed(56, "usdt") { // case-insensitive
		t.Errorf("USDT must be allowed on BSC (case-insensitive)")
	}
	if reg.AssetAllowed(999, "USDT") { // unconfigured chain
		t.Errorf("no chain → not allowed")
	}
}
