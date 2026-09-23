package services

import (
	"errors"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/chains/solana"
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
	got, err := resolveRequestedAsset(chains, nil, RequestedAsset{ChainID: 56, Address: evmNativeAddress})
	if err != nil || got.ChainKey != "bsc" || got.Stable {
		t.Fatalf("BNB: %+v err=%v", got, err)
	}
	for name, bad := range map[string]RequestedAsset{
		"unknown chain id":  {ChainID: 424242, Address: evmNativeAddress},
		"malformed address": {ChainID: 56, Address: "0xnothex"},
		"empty":             {},
	} {
		if _, err := resolveRequestedAsset(chains, nil, bad); !errors.Is(err, ErrBadRequestedAsset) {
			t.Errorf("%s: err = %v, want ErrBadRequestedAsset", name, err)
		}
	}
	// The client's chain name is ignored: chainId 56 is BSC whatever it says.
	spoof, err := resolveRequestedAsset(chains, nil, RequestedAsset{Chain: "polygon", ChainID: 56, Address: evmNativeAddress})
	if err != nil || spoof.ChainKey != "bsc" {
		t.Fatalf("spoofed chain name: %+v err=%v", spoof, err)
	}
	// A legacy symbol request still resolves through the config map.
	legacy, err := resolveRequestedAsset(chains, nil, RequestedAsset{Chain: "optimism", Token: "USDT"})
	if err != nil || !legacy.Stable || legacy.ChainID != 10 {
		t.Fatalf("legacy: %+v err=%v", legacy, err)
	}
}

// A Solana destination is named the only way Solana can be named — a base58
// mint (or the System Program id for native SOL) and a base58 recipient — and
// neither is trusted: both go through the chain client's own validator, which
// is the same code the wallet validates a send address with.
func TestResolveRequestedAsset_Solana(t *testing.T) {
	chains := testChainRegistry()
	validate := solana.ValidateAddress
	const (
		recipient = "5tzFkiKscXHK5ZXCGbXZxdw7gTjjD1mBwuoFbhUvuAi9"
		usdcMint  = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	)

	sol, err := resolveRequestedAsset(chains, validate, RequestedAsset{
		ChainID: solanaLiFiChainID, Address: solanaNativeToken, Destination: recipient,
	})
	if err != nil || sol.ChainKey != "solana" || sol.Stable || sol.Address != solanaNativeToken || sol.Destination != recipient {
		t.Fatalf("native SOL: %+v err=%v", sol, err)
	}
	// A Solana stablecoin is still NOT a "stable destination": that flag means
	// the treasury holds a float it can top up from, and it holds none here.
	spl, err := resolveRequestedAsset(chains, validate, RequestedAsset{
		ChainID: solanaLiFiChainID, Address: usdcMint, Destination: recipient,
	})
	if err != nil || spl.Stable || spl.Address != usdcMint {
		t.Fatalf("USDC on Solana: %+v err=%v", spl, err)
	}

	for name, bad := range map[string]RequestedAsset{
		"an EVM address as the mint": {ChainID: solanaLiFiChainID, Address: evmNativeAddress, Destination: recipient},
		"a mint that is not base58":  {ChainID: solanaLiFiChainID, Address: "not a mint", Destination: recipient},
		"no recipient":               {ChainID: solanaLiFiChainID, Address: solanaNativeToken},
		"a recipient that is not base58": {
			ChainID: solanaLiFiChainID, Address: solanaNativeToken, Destination: "0x00000000000000000000000000000000000000ff",
		},
	} {
		if _, err := resolveRequestedAsset(chains, validate, bad); !errors.Is(err, ErrBadRequestedAsset) {
			t.Errorf("%s: err = %v, want ErrBadRequestedAsset", name, err)
		}
	}

	// No Solana chain client wired: the destination cannot be validated, so it
	// is refused rather than accepted on the client's say-so.
	if _, err := resolveRequestedAsset(chains, nil, RequestedAsset{
		ChainID: solanaLiFiChainID, Address: solanaNativeToken, Destination: recipient,
	}); !errors.Is(err, ErrBadRequestedAsset) {
		t.Errorf("dormant Solana: err = %v, want ErrBadRequestedAsset", err)
	}
}
