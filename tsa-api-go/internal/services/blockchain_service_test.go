package services

import (
	"testing"

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
