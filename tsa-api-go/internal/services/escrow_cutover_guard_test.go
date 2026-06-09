package services

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// stubEscrowCutoverReader is an in-memory EscrowCutoverReader. states maps
// "chainID|address" → the on-chain getter values; readErr forces a read failure.
type stubEscrowCutoverReader struct {
	states  map[string]EscrowCutoverState
	readErr error
}

func newStubEscrowReader() *stubEscrowCutoverReader {
	return &stubEscrowCutoverReader{states: map[string]EscrowCutoverState{}}
}

func (s *stubEscrowCutoverReader) set(chainID int64, addr string, st EscrowCutoverState) {
	s.states[fmt.Sprintf("%d|%s", chainID, strings.ToLower(addr))] = st
}

func (s *stubEscrowCutoverReader) ReadCutoverState(_ context.Context, chainID int64, addr string) (EscrowCutoverState, error) {
	if s.readErr != nil {
		return EscrowCutoverState{}, s.readErr
	}
	st, ok := s.states[fmt.Sprintf("%d|%s", chainID, strings.ToLower(addr))]
	if !ok {
		return EscrowCutoverState{}, fmt.Errorf("no stub state for chain %d addr %s", chainID, addr)
	}
	return st, nil
}

const (
	fmAddr  = "0x00000000000000000000000000000000000feeAA"
	escrowA = "0x000000000000000000000000000000000Escrow1"
	zeroFM  = "0x0000000000000000000000000000000000000000"
)

// flag-ON + legacySplitEnabled==true → error (the escrow still splits on-chain).
func TestVerifyEscrowCutoverReady_FlagOn_StillSplitting_Errors(t *testing.T) {
	reader := newStubEscrowReader()
	reader.set(146, escrowA, EscrowCutoverState{LegacySplitEnabled: true, FeeManager: fmAddr})
	escrows := []EscrowRef{{Label: "p2p", ChainID: 146, Address: escrowA}}

	err := VerifyEscrowCutoverReady(context.Background(), reader, escrows, true)
	if err == nil {
		t.Fatalf("expected error when legacySplitEnabled==true with flag ON")
	}
	if !strings.Contains(err.Error(), "legacySplitEnabled") {
		t.Errorf("error = %q, want it to name legacySplitEnabled", err)
	}
}

// flag-ON + legacySplitEnabled==false + feeManager set → ok (cutover-ready).
func TestVerifyEscrowCutoverReady_FlagOn_CutoverReady_OK(t *testing.T) {
	reader := newStubEscrowReader()
	reader.set(146, escrowA, EscrowCutoverState{LegacySplitEnabled: false, FeeManager: fmAddr})
	escrows := []EscrowRef{{Label: "p2p", ChainID: 146, Address: escrowA}}

	if err := VerifyEscrowCutoverReady(context.Background(), reader, escrows, true); err != nil {
		t.Fatalf("cutover-ready escrow should pass, got: %v", err)
	}
}

// flag-ON + legacySplitEnabled==false but feeManager==0x0 → error (not wired).
func TestVerifyEscrowCutoverReady_FlagOn_FeeManagerUnset_Errors(t *testing.T) {
	reader := newStubEscrowReader()
	reader.set(146, escrowA, EscrowCutoverState{LegacySplitEnabled: false, FeeManager: zeroFM})
	escrows := []EscrowRef{{Label: "product", ChainID: 146, Address: escrowA}}

	err := VerifyEscrowCutoverReady(context.Background(), reader, escrows, true)
	if err == nil {
		t.Fatalf("expected error when feeManager==0x0 with flag ON")
	}
	if !strings.Contains(err.Error(), "feeManager==0x0") {
		t.Errorf("error = %q, want it to name feeManager==0x0", err)
	}
}

// flag-OFF → ok REGARDLESS of escrow state (even a still-splitting escrow). The
// guard must be a no-op so flag-OFF behavior is byte-for-byte unchanged.
func TestVerifyEscrowCutoverReady_FlagOff_NoOp(t *testing.T) {
	reader := newStubEscrowReader()
	reader.set(146, escrowA, EscrowCutoverState{LegacySplitEnabled: true, FeeManager: zeroFM})
	escrows := []EscrowRef{{Label: "p2p", ChainID: 146, Address: escrowA}}

	if err := VerifyEscrowCutoverReady(context.Background(), reader, escrows, false); err != nil {
		t.Fatalf("flag-OFF guard must be a no-op, got: %v", err)
	}
	// flag-OFF with a nil reader must also be a no-op.
	if err := VerifyEscrowCutoverReady(context.Background(), nil, escrows, false); err != nil {
		t.Fatalf("flag-OFF guard with nil reader must be a no-op, got: %v", err)
	}
}

// no escrows configured (dormant default) → ok regardless of flag.
func TestVerifyEscrowCutoverReady_NoEscrows_OK(t *testing.T) {
	reader := newStubEscrowReader()
	if err := VerifyEscrowCutoverReady(context.Background(), reader, nil, true); err != nil {
		t.Fatalf("no escrows + flag ON must pass, got: %v", err)
	}
	if err := VerifyEscrowCutoverReady(context.Background(), reader, []EscrowRef{}, true); err != nil {
		t.Fatalf("empty escrows + flag ON must pass, got: %v", err)
	}
	// An escrow with an EMPTY address is an unconfigured slot — skipped.
	if err := VerifyEscrowCutoverReady(context.Background(), reader, []EscrowRef{{Label: "p2p", ChainID: 146, Address: ""}}, true); err != nil {
		t.Fatalf("escrow with empty address (unconfigured slot) must be skipped, got: %v", err)
	}
}

// flag-ON + escrows configured + reader==nil → error (cannot prove they stopped
// splitting → fail safe).
func TestVerifyEscrowCutoverReady_FlagOn_NilReader_FailsSafe(t *testing.T) {
	escrows := []EscrowRef{{Label: "p2p", ChainID: 146, Address: escrowA}}
	err := VerifyEscrowCutoverReady(context.Background(), nil, escrows, true)
	if err == nil {
		t.Fatalf("flag ON + escrows configured + nil reader must fail safe")
	}
	if !strings.Contains(err.Error(), "no on-chain reader") {
		t.Errorf("error = %q, want a fail-safe no-reader message", err)
	}
}

// flag-ON + a read error → error (cannot prove the escrow stopped splitting).
func TestVerifyEscrowCutoverReady_FlagOn_ReadError_FailsSafe(t *testing.T) {
	reader := newStubEscrowReader()
	reader.readErr = fmt.Errorf("rpc down")
	escrows := []EscrowRef{{Label: "service-contact", ChainID: 146, Address: escrowA}}

	err := VerifyEscrowCutoverReady(context.Background(), reader, escrows, true)
	if err == nil {
		t.Fatalf("flag ON + read error must fail safe")
	}
	if !strings.Contains(err.Error(), "could not read") {
		t.Errorf("error = %q, want a could-not-read message", err)
	}
}

// Multiple escrows: the error aggregates every offending escrow so the operator
// sees the full picture in one boot-failure line.
func TestVerifyEscrowCutoverReady_AggregatesMultiple(t *testing.T) {
	reader := newStubEscrowReader()
	reader.set(146, escrowA, EscrowCutoverState{LegacySplitEnabled: true, FeeManager: fmAddr})                                     // still splitting
	reader.set(56, "0x00000000000000000000000000000000000Esc2", EscrowCutoverState{LegacySplitEnabled: false, FeeManager: zeroFM}) // unwired
	escrows := []EscrowRef{
		{Label: "p2p-sonic", ChainID: 146, Address: escrowA},
		{Label: "p2p-bsc", ChainID: 56, Address: "0x00000000000000000000000000000000000Esc2"},
	}
	err := VerifyEscrowCutoverReady(context.Background(), reader, escrows, true)
	if err == nil {
		t.Fatalf("expected an aggregated error")
	}
	if !strings.Contains(err.Error(), "p2p-sonic") || !strings.Contains(err.Error(), "p2p-bsc") {
		t.Errorf("error should name BOTH escrows, got: %q", err)
	}
}

// isZeroOrEmptyAddress edge cases.
func TestIsZeroOrEmptyAddress(t *testing.T) {
	zero := []string{"", "   ", zeroFM, "0x0"}
	for _, a := range zero {
		if !isZeroOrEmptyAddress(a) {
			t.Errorf("isZeroOrEmptyAddress(%q) = false, want true", a)
		}
	}
	if isZeroOrEmptyAddress(fmAddr) {
		t.Errorf("isZeroOrEmptyAddress(%q) = true, want false", fmAddr)
	}
}
