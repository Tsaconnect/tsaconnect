package services

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// selectorCaller answers eth_calls by method name; a missing entry reverts,
// and a method named in failing returns a non-revert transport error.
type selectorCaller struct {
	answers map[string]common.Address
	failing map[string]bool
}

func (s selectorCaller) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	for name := range s.failing {
		if string(call.Data[:4]) == string(parsedP2PEscrowABI.Methods[name].ID) {
			return nil, errors.New("context deadline exceeded")
		}
	}
	for name, addr := range s.answers {
		if string(call.Data[:4]) == string(parsedP2PEscrowABI.Methods[name].ID) {
			return common.LeftPadBytes(addr.Bytes(), 32), nil
		}
	}
	return nil, errors.New("execution reverted")
}

func answers(m map[string]common.Address) selectorCaller { return selectorCaller{answers: m} }

func TestFeeSinks_SystemWallets(t *testing.T) {
	fee := common.HexToAddress("0xaF326D5D242C9A55590540f14658adDDd3586A8d")
	owner := common.HexToAddress("0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96")

	tests := []struct {
		name       string
		caller     selectorCaller
		wantWallet string
		wantSource string
		wantReason bool
	}{
		{"fee wallet set", answers(map[string]common.Address{"systemWallet": fee, "owner": owner}), fee.Hex(), FeeSinkSourceSystemWallet, false},
		{"pre-upgrade contract reverts on systemWallet, pays owner", answers(map[string]common.Address{"owner": owner}), owner.Hex(), FeeSinkSourceOwner, false},
		{"upgraded but unset systemWallet pays owner", answers(map[string]common.Address{"systemWallet": {}, "owner": owner}), owner.Hex(), FeeSinkSourceOwner, false},
		{"nothing readable", answers(nil), "", "", true},
		// A timeout is not a revert: the contract may well have a fee wallet set, so
		// reporting owner() here would be a confident wrong answer.
		{"systemWallet times out, owner readable: could not check", selectorCaller{answers: map[string]common.Address{"owner": owner}, failing: map[string]bool{"systemWallet": true}}, "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NewFeeSinks(FeeSinkContract{Label: "Product escrow", ChainKey: "sonic", ChainID: 146, Address: "0x42C3c60F40c48497D023166C6088e27872D63fcb", Caller: tc.caller}).SystemWallets()
			if len(got) != 1 {
				t.Fatalf("want 1 row, got %d", len(got))
			}
			row := got[0]
			if row.SystemWallet != tc.wantWallet || row.Source != tc.wantSource || (row.Reason != "") != tc.wantReason {
				t.Fatalf("got wallet=%q source=%q reason=%q", row.SystemWallet, row.Source, row.Reason)
			}
		})
	}
}

func TestFeeSinks_UnconfiguredAddressKeepsRowWithReason(t *testing.T) {
	got := NewFeeSinks(FeeSinkContract{Label: "Service contact"}).SystemWallets()
	if len(got) != 1 || got[0].Reason == "" {
		t.Fatalf("an unconfigured contract must stay visible with a reason, got %+v", got)
	}
}

func TestCombinedFeeSinks_ConcatenatesInOrder(t *testing.T) {
	a := NewFeeSinks(FeeSinkContract{Label: "A"})
	b := NewFeeSinks(FeeSinkContract{Label: "B"})
	var nilRegistry *P2PEscrowRegistry
	got := CombinedFeeSinks{nilRegistry, a, b}.SystemWallets()
	if len(got) != 2 || got[0].Label != "A" || got[1].Label != "B" {
		t.Fatalf("got %+v", got)
	}
}

func TestFeeSinks_ReadsContractsConcurrentlyInOrder(t *testing.T) {
	owner := common.HexToAddress("0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96")
	c := answers(map[string]common.Address{"owner": owner})
	got := NewFeeSinks(
		FeeSinkContract{Label: "first", Address: "0x1", Caller: c},
		FeeSinkContract{Label: "second", Address: "0x2", Caller: c},
	).SystemWallets()
	if len(got) != 2 || got[0].Label != "first" || got[1].Label != "second" {
		t.Fatalf("rows must keep configured order, got %+v", got)
	}
}

// FeeSinks reads ProductEscrowV2 and ServiceContactV2 through the P2P ABI, so
// the selectors it packs must be the real ones for these contracts.
func TestFeeSinkSelectors(t *testing.T) {
	for method, sig := range map[string]string{"systemWallet": "systemWallet()", "owner": "owner()"} {
		want := crypto.Keccak256([]byte(sig))[:4]
		if got := parsedP2PEscrowABI.Methods[method].ID; string(got) != string(want) {
			t.Errorf("%s selector = %x, want %x", method, got, want)
		}
	}
}
