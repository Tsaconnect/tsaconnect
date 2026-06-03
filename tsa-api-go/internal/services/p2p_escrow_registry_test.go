package services

import "testing"

func TestNewP2PEscrowServiceForChain_UsesExplicitAddress(t *testing.T) {
	s := NewP2PEscrowServiceForChain(nil, nil, "0xBSCescrow")
	if s.ContractAddress() != "0xBSCescrow" {
		t.Errorf("ContractAddress() = %q, want 0xBSCescrow", s.ContractAddress())
	}
}

func TestP2PEscrowRegistry_For_HitAndFailLoudMiss(t *testing.T) {
	reg := &P2PEscrowRegistry{byChain: map[int64]*P2PEscrowService{
		146: NewP2PEscrowServiceForChain(nil, nil, "0xSonic"),
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
