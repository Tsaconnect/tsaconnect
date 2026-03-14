package blockchain

import (
	"encoding/json"
	"math/big"
	"testing"
)

func TestGetTokenBalance(t *testing.T) {
	client, err := NewEVMClient(testRPCURL, testChainID)
	if err != nil {
		t.Fatalf("NewEVMClient failed: %v", err)
	}

	balance, err := client.GetTokenBalance(testMCGP, testSystem)
	if err != nil {
		t.Fatalf("GetTokenBalance failed: %v", err)
	}
	if balance == nil {
		t.Fatal("expected non-nil balance")
	}
	t.Logf("MCGP balance for system wallet: %s", balance.String())
}

func TestPrepareERC20Transfer(t *testing.T) {
	client, err := NewEVMClient(testRPCURL, testChainID)
	if err != nil {
		t.Fatalf("NewEVMClient failed: %v", err)
	}

	amount := big.NewInt(1000000000000000000) // 1 token (18 decimals)
	recipient := "0x0000000000000000000000000000000000000001"

	txBytes, err := client.PrepareERC20Transfer(testMCGP, testSystem, recipient, amount)
	if err != nil {
		t.Fatalf("PrepareERC20Transfer failed: %v", err)
	}

	var tx UnsignedTx
	if err := json.Unmarshal(txBytes, &tx); err != nil {
		t.Fatalf("failed to unmarshal unsigned tx: %v", err)
	}

	if tx.To != testMCGP {
		t.Errorf("expected To=%s, got %s", testMCGP, tx.To)
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
	client, err := NewEVMClient(testRPCURL, testChainID)
	if err != nil {
		t.Fatalf("NewEVMClient failed: %v", err)
	}

	amount := big.NewInt(1000000000000000000) // 1 token
	spender := "0x0000000000000000000000000000000000000002"

	txBytes, err := client.PrepareERC20Approve(testMCGP, testSystem, spender, amount)
	if err != nil {
		t.Fatalf("PrepareERC20Approve failed: %v", err)
	}

	var tx UnsignedTx
	if err := json.Unmarshal(txBytes, &tx); err != nil {
		t.Fatalf("failed to unmarshal unsigned tx: %v", err)
	}

	if tx.To != testMCGP {
		t.Errorf("expected To=%s, got %s", testMCGP, tx.To)
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

func TestPrepareNativeTransfer(t *testing.T) {
	client, err := NewEVMClient(testRPCURL, testChainID)
	if err != nil {
		t.Fatalf("NewEVMClient failed: %v", err)
	}

	amount := big.NewInt(1000000000000000000) // 1 S token
	recipient := "0x0000000000000000000000000000000000000001"

	txBytes, err := client.PrepareNativeTransfer(testSystem, recipient, amount)
	if err != nil {
		t.Fatalf("PrepareNativeTransfer failed: %v", err)
	}

	var tx UnsignedTx
	if err := json.Unmarshal(txBytes, &tx); err != nil {
		t.Fatalf("failed to unmarshal unsigned tx: %v", err)
	}

	if tx.To != recipient {
		t.Errorf("expected To=%s, got %s", recipient, tx.To)
	}
	if tx.Value != amount.String() {
		t.Errorf("expected Value=%s, got %s", amount.String(), tx.Value)
	}
	if tx.ChainID != "14601" {
		t.Errorf("expected ChainID=14601, got %s", tx.ChainID)
	}
	if tx.Data != "" {
		t.Errorf("expected empty Data for native transfer, got %s", tx.Data)
	}

	t.Logf("Unsigned native transfer tx: %s", string(txBytes))
}
