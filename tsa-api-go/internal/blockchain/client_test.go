package blockchain

import (
	"testing"
)

const (
	testRPCURL  = "https://rpc.testnet.soniclabs.com"
	testChainID = 14601
	testMCGP    = "0x517600323e5E2938207fA2e2e915B9D80e5B2b21"
	testSystem  = "0xaF326D5D242C9A55590540f14658adDDd3586A8d"
)

func TestNewEVMClient(t *testing.T) {
	client, err := NewEVMClient(testRPCURL, testChainID)
	if err != nil {
		t.Fatalf("NewEVMClient failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.ChainID().Int64() != 14601 {
		t.Errorf("expected chain ID 14601, got %d", client.ChainID().Int64())
	}
}

func TestGetBalance(t *testing.T) {
	client, err := NewEVMClient(testRPCURL, testChainID)
	if err != nil {
		t.Fatalf("NewEVMClient failed: %v", err)
	}

	balance, err := client.GetBalance(testSystem)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}
	if balance == nil {
		t.Fatal("expected non-nil balance")
	}
	t.Logf("System wallet S balance: %s wei", balance.String())
}

func TestGetNonce(t *testing.T) {
	client, err := NewEVMClient(testRPCURL, testChainID)
	if err != nil {
		t.Fatalf("NewEVMClient failed: %v", err)
	}

	nonce, err := client.GetNonce(testSystem)
	if err != nil {
		t.Fatalf("GetNonce failed: %v", err)
	}
	t.Logf("System wallet nonce: %d", nonce)
}

func TestSuggestGasPrice(t *testing.T) {
	client, err := NewEVMClient(testRPCURL, testChainID)
	if err != nil {
		t.Fatalf("NewEVMClient failed: %v", err)
	}

	gasPrice, err := client.SuggestGasPrice()
	if err != nil {
		t.Fatalf("SuggestGasPrice failed: %v", err)
	}
	if gasPrice == nil {
		t.Fatal("expected non-nil gas price")
	}
	if gasPrice.Sign() <= 0 {
		t.Errorf("expected positive gas price, got %s", gasPrice.String())
	}
	t.Logf("Suggested gas price: %s wei", gasPrice.String())
}
