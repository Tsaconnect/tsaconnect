package blockchain

import (
	"encoding/json"
	"math/big"
	"testing"
)

func TestGetTokenBalance(t *testing.T) {
	cfg := testConfig()
	client, err := NewSonicClient(cfg)
	if err != nil {
		t.Fatalf("NewSonicClient failed: %v", err)
	}

	balance, err := client.GetTokenBalance(cfg.MCGPTokenAddress, cfg.SystemWalletAddress)
	if err != nil {
		t.Fatalf("GetTokenBalance failed: %v", err)
	}
	if balance == nil {
		t.Fatal("expected non-nil balance")
	}
	t.Logf("MCGP balance for system wallet: %s", balance.String())
}

func TestGetAllBalances(t *testing.T) {
	cfg := testConfig()
	client, err := NewSonicClient(cfg)
	if err != nil {
		t.Fatalf("NewSonicClient failed: %v", err)
	}

	balances, err := client.GetAllBalances(cfg.SystemWalletAddress)
	if err != nil {
		t.Fatalf("GetAllBalances failed: %v", err)
	}

	// Should have at least MCGP since it's the only configured token with an address
	if _, ok := balances["MCGP"]; !ok {
		t.Error("expected MCGP key in balances map")
	}

	for symbol, bal := range balances {
		t.Logf("%s balance: %s", symbol, bal.String())
	}
}

func TestGetSupportedTokens(t *testing.T) {
	cfg := testConfig()
	client, err := NewSonicClient(cfg)
	if err != nil {
		t.Fatalf("NewSonicClient failed: %v", err)
	}

	tokens := client.GetSupportedTokens()
	if len(tokens) == 0 {
		t.Fatal("expected at least one supported token")
	}

	found := false
	for _, tok := range tokens {
		if tok.Symbol == "MCGP" {
			found = true
			if tok.Address != cfg.MCGPTokenAddress {
				t.Errorf("MCGP address mismatch: got %s, want %s", tok.Address, cfg.MCGPTokenAddress)
			}
		}
	}
	if !found {
		t.Error("expected MCGP in supported tokens")
	}
}

func TestPrepareERC20Transfer(t *testing.T) {
	cfg := testConfig()
	client, err := NewSonicClient(cfg)
	if err != nil {
		t.Fatalf("NewSonicClient failed: %v", err)
	}

	amount := big.NewInt(1000000000000000000) // 1 token (18 decimals)
	recipient := "0x0000000000000000000000000000000000000001"

	txBytes, err := client.PrepareERC20Transfer(cfg.MCGPTokenAddress, cfg.SystemWalletAddress, recipient, amount)
	if err != nil {
		t.Fatalf("PrepareERC20Transfer failed: %v", err)
	}

	var tx UnsignedTx
	if err := json.Unmarshal(txBytes, &tx); err != nil {
		t.Fatalf("failed to unmarshal unsigned tx: %v", err)
	}

	if tx.To != cfg.MCGPTokenAddress {
		t.Errorf("expected To=%s, got %s", cfg.MCGPTokenAddress, tx.To)
	}
	if tx.Value != "0" {
		t.Errorf("expected Value=0, got %s", tx.Value)
	}
	if tx.ChainID != "14601" {
		t.Errorf("expected ChainID=14601, got %s", tx.ChainID)
	}
	if tx.Data == "" {
		t.Error("expected non-empty Data")
	}
	if tx.GasLimit == 0 {
		t.Error("expected non-zero GasLimit")
	}
	if tx.GasPrice == "" {
		t.Error("expected non-empty GasPrice")
	}

	t.Logf("Unsigned transfer tx: %s", string(txBytes))
}

func TestPrepareERC20Approve(t *testing.T) {
	cfg := testConfig()
	client, err := NewSonicClient(cfg)
	if err != nil {
		t.Fatalf("NewSonicClient failed: %v", err)
	}

	amount := big.NewInt(1000000000000000000) // 1 token
	spender := "0x0000000000000000000000000000000000000002"

	txBytes, err := client.PrepareERC20Approve(cfg.MCGPTokenAddress, cfg.SystemWalletAddress, spender, amount)
	if err != nil {
		t.Fatalf("PrepareERC20Approve failed: %v", err)
	}

	var tx UnsignedTx
	if err := json.Unmarshal(txBytes, &tx); err != nil {
		t.Fatalf("failed to unmarshal unsigned tx: %v", err)
	}

	if tx.To != cfg.MCGPTokenAddress {
		t.Errorf("expected To=%s, got %s", cfg.MCGPTokenAddress, tx.To)
	}
	if tx.Value != "0" {
		t.Errorf("expected Value=0, got %s", tx.Value)
	}
	if tx.ChainID != "14601" {
		t.Errorf("expected ChainID=14601, got %s", tx.ChainID)
	}
	if tx.Data == "" {
		t.Error("expected non-empty Data")
	}

	t.Logf("Unsigned approve tx: %s", string(txBytes))
}
