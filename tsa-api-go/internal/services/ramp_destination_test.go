package services

import (
	"errors"
	"strings"
	"testing"
)

// ramp_destination_test.go — resolving what the user asked to be delivered.
//
// Every field of a requested asset arrives from a client, so none of it is
// trusted: the chain key comes from the chain id, stable-ness and decimals
// from our own config, and a chain name the client sent is ignored entirely.

func TestIsStableDestination(t *testing.T) {
	chains := testChainRegistry()
	usdt := chains.TokenAddressForChainID(56, "USDT")
	if ok, _, sym := isStableDestination(chains, 56, strings.ToUpper(usdt)); !ok || sym != "USDT" {
		t.Fatal("configured USDT on BSC (any case) is a stablecoin destination")
	}
	if ok, _, _ := isStableDestination(chains, 56, evmNativeAddress); ok {
		t.Fatal("native BNB is not a stablecoin destination")
	}
	if ok, _, _ := isStableDestination(nil, 56, usdt); ok {
		t.Fatal("no registry: nothing is a stablecoin")
	}
}

func TestResolveRequestedAsset(t *testing.T) {
	chains := testChainRegistry()
	got, err := resolveRequestedAsset(chains, RequestedAsset{ChainID: 56, Address: evmNativeAddress})
	if err != nil || got.ChainKey != "bsc" || got.Stable {
		t.Fatalf("BNB: %+v err=%v", got, err)
	}
	for name, bad := range map[string]RequestedAsset{
		"unknown chain id":  {ChainID: 424242, Address: evmNativeAddress},
		"malformed address": {ChainID: 56, Address: "0xnothex"},
		"empty":             {},
	} {
		if _, err := resolveRequestedAsset(chains, bad); !errors.Is(err, ErrBadRequestedAsset) {
			t.Errorf("%s: err = %v, want ErrBadRequestedAsset", name, err)
		}
	}
	// The client's chain name is ignored: chainId 56 is BSC whatever it says.
	spoof, err := resolveRequestedAsset(chains, RequestedAsset{Chain: "polygon", ChainID: 56, Address: evmNativeAddress})
	if err != nil || spoof.ChainKey != "bsc" {
		t.Fatalf("spoofed chain name: %+v err=%v", spoof, err)
	}
	// A legacy symbol request still resolves through the config map.
	legacy, err := resolveRequestedAsset(chains, RequestedAsset{Chain: "optimism", Token: "USDT"})
	if err != nil || !legacy.Stable || legacy.ChainID != 10 {
		t.Fatalf("legacy: %+v err=%v", legacy, err)
	}
}
