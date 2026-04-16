package handlers

import (
	"encoding/json"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

func TestNormalizePreparedSwapTransactions_BumpsSwapNonceAfterApprove(t *testing.T) {
	approveTxBytes, err := json.Marshal(blockchain.UnsignedTx{
		Nonce:    12,
		GasPrice: "1",
		GasLimit: 80000,
		To:       "0x0000000000000000000000000000000000000001",
		Value:    "0",
		Data:     "abcd",
		ChainID:  "146",
	})
	if err != nil {
		t.Fatalf("marshal approve tx: %v", err)
	}

	swapTxBytes, err := json.Marshal(blockchain.UnsignedTx{
		Nonce:    12,
		GasPrice: "1",
		GasLimit: 250000,
		To:       "0x0000000000000000000000000000000000000002",
		Value:    "0",
		Data:     "ef01",
		ChainID:  "146",
	})
	if err != nil {
		t.Fatalf("marshal swap tx: %v", err)
	}

	approveTx, swapTx, err := normalizePreparedSwapTransactions(approveTxBytes, swapTxBytes)
	if err != nil {
		t.Fatalf("normalizePreparedSwapTransactions failed: %v", err)
	}

	if approveTx == nil {
		t.Fatalf("expected approve tx")
	}
	if swapTx == nil {
		t.Fatalf("expected swap tx")
	}
	if swapTx.Nonce != approveTx.Nonce+1 {
		t.Fatalf("expected swap nonce %d, got %d", approveTx.Nonce+1, swapTx.Nonce)
	}
}

func TestNormalizePreparedSwapTransactions_AllowsSwapWithoutApprove(t *testing.T) {
	swapTxBytes, err := json.Marshal(blockchain.UnsignedTx{
		Nonce:    4,
		GasPrice: "1",
		GasLimit: 250000,
		To:       "0x0000000000000000000000000000000000000002",
		Value:    "0",
		Data:     "ef01",
		ChainID:  "146",
	})
	if err != nil {
		t.Fatalf("marshal swap tx: %v", err)
	}

	approveTx, swapTx, err := normalizePreparedSwapTransactions(nil, swapTxBytes)
	if err != nil {
		t.Fatalf("normalizePreparedSwapTransactions failed: %v", err)
	}

	if approveTx != nil {
		t.Fatalf("expected no approve tx")
	}
	if swapTx == nil {
		t.Fatalf("expected swap tx")
	}
	if swapTx.Nonce != 4 {
		t.Fatalf("expected swap nonce 4, got %d", swapTx.Nonce)
	}
}
