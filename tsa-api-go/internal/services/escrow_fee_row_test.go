package services

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
)

func TestReadEscrowFeeRow_ReturnsTheFeeManagerAndSymbol(t *testing.T) {
	chain := withFeeManager(map[string][4]interface{}{"p2p_platform": {uint16(0), uint64(0), false, true}})
	chain.symbol = "USDT"
	row, err := readEscrowFeeRow(context.Background(), &chain, testEscrow, testUSDC, "p2p_platform")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if row.feeManager != common.HexToAddress(testFeeManager) || row.symbol != "USDT" || !row.active || row.bps != 0 || row.noFeeManager {
		t.Fatalf("row = %+v", row)
	}
}

func TestReadEscrowFeeRow_NoFeeManager(t *testing.T) {
	chain := fakeEscrowChain{} // feeManager() answers the zero address
	row, err := readEscrowFeeRow(context.Background(), &chain, testEscrow, testUSDC, "p2p")
	if err != nil || !row.noFeeManager {
		t.Fatalf("row = %+v err = %v", row, err)
	}
}

func TestDecodeTradeFeeSnapshot(t *testing.T) {
	m := parsedP2PEscrowABI.Methods["trades"]
	out, err := m.Outputs.Pack(common.Address{1}, common.Address{2}, common.Address{3}, big.NewInt(1), big.NewInt(2), big.NewInt(0),
		uint8(1), uint8(0), uint16(0), uint16(7), uint16(3), HashID(uuid.New()))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	p, g, ok := decodeTradeFeeSnapshot(out)
	if !ok || p != 7 || g != 3 {
		t.Fatalf("snapshot = (%d, %d, %v), want (7, 3, true)", p, g, ok)
	}
	if _, _, ok := decodeTradeFeeSnapshot([]byte{1, 2, 3}); ok {
		t.Fatal("garbage must not decode")
	}
}

func TestP2PEscrowABI_HasThePoolViews(t *testing.T) {
	for _, m := range []string{"feeBps", "paymentWindow", "feeManager", "tokenSymbol", "isAdmin"} {
		if _, ok := parsedP2PEscrowABI.Methods[m]; !ok {
			t.Fatalf("p2pEscrowABI lacks %s", m)
		}
	}
}
