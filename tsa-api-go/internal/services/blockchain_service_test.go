package services

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// TestTokenAddressForChainID verifies that the lookup returns the correct
// per-chain token address and does not bleed Sonic addresses into BSC lookups
// (or vice-versa).
func TestTokenAddressForChainID(t *testing.T) {
	sonicUSDT := "0xSonicUSDT0000000000000000000000000000000"
	bscUSDT := "0xBscUSDT0000000000000000000000000000000000"

	cfg := &config.Config{
		Networks: map[string]config.NetworkConfig{
			"mainnet": {
				Chains: map[string]config.ChainConfig{
					"sonic": {ChainID: 146},
					"bsc":   {ChainID: 56},
				},
				TokenAddresses: map[string]string{
					"sonic:USDT": sonicUSDT,
					"bsc:USDT":   bscUSDT,
				},
			},
		},
	}

	bs := &BlockchainService{cfg: cfg}

	got146 := bs.TokenAddressForChainID(146, "USDT")
	if got146 != sonicUSDT {
		t.Errorf("chainID 146 (Sonic): want %s, got %s", sonicUSDT, got146)
	}

	got56 := bs.TokenAddressForChainID(56, "USDT")
	if got56 != bscUSDT {
		t.Errorf("chainID 56 (BSC): want %s, got %s", bscUSDT, got56)
	}

	if got146 == got56 {
		t.Errorf("Sonic and BSC USDT addresses must differ, both resolved to %s", got146)
	}

	// Unknown chain returns empty string — no panic.
	if addr := bs.TokenAddressForChainID(999, "USDT"); addr != "" {
		t.Errorf("unknown chainID 999: want empty string, got %s", addr)
	}

	// Nil cfg is safe.
	bsNil := &BlockchainService{cfg: nil}
	if addr := bsNil.TokenAddressForChainID(146, "USDT"); addr != "" {
		t.Errorf("nil cfg: want empty string, got %s", addr)
	}
}

// TestChainNameForID pins the mirror of ChainIDForName: the server must
// never trust a client-supplied chain name, so it derives one from a chain
// ID (e.g. a LiFi route's own chainId) instead.
func TestChainNameForID(t *testing.T) {
	b := testChainRegistry() // base 8453, bsc 56, optimism 10, linea 59144
	if name, ok := b.ChainNameForID(56); !ok || name != "bsc" {
		t.Fatalf("56 -> %q,%v want bsc", name, ok)
	}
	if _, ok := b.ChainNameForID(999999); ok {
		t.Fatal("an unknown chain id must not resolve")
	}
}

// testnetAwareRegistry is the real shape: a testnet reuses its mainnet's chain
// KEY ("bsc" is 56 on mainnet and 97 on testnet), and only some mainnet chains
// have a connected client.
func testnetAwareRegistry() *BlockchainService {
	return &BlockchainService{
		cfg: &config.Config{Networks: map[string]config.NetworkConfig{
			"mainnet": {Chains: map[string]config.ChainConfig{"bsc": {ChainID: 56}, "optimism": {ChainID: 10}}},
			"testnet": {Chains: map[string]config.ChainConfig{"bsc": {ChainID: 97}}},
		}},
		clients: map[string]*blockchain.EVMClient{"mainnet:bsc": {}, "testnet:bsc": {}},
	}
}

// MainnetChainNameForID is the set of chains a delivery can be confirmed on:
// mainnet, with a connected client (rampTransferVerifiers builds a verifier for
// exactly those). ChainNameForID, which scans testnets too, is not that set.
func TestMainnetChainNameForID_OnlyAMainnetChainWithAClient(t *testing.T) {
	b := testnetAwareRegistry()
	if name, ok := b.MainnetChainNameForID(56); !ok || name != "bsc" {
		t.Fatalf("56 -> %q,%v want bsc", name, ok)
	}
	if name, ok := b.ChainNameForID(97); !ok || name != "bsc" {
		t.Fatalf("the general resolver aliases the testnet onto %q,%v — the reason the pay paths must not use it", name, ok)
	}
	for name, id := range map[string]int64{"a testnet": 97, "mainnet without a client": 10, "unknown": 999999} {
		if got, ok := b.MainnetChainNameForID(id); ok {
			t.Fatalf("%s (%d) resolved to %q", name, id, got)
		}
	}
	if _, ok := (&BlockchainService{}).MainnetChainNameForID(56); ok {
		t.Fatal("a registry with no config resolved a chain")
	}
}
