package services

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
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

func TestP2PEscrowABI_HasThePoolViews(t *testing.T) {
	for _, m := range []string{"getTrade", "paymentWindow", "releaserOf", "balanceOf", "hashVoucher"} {
		if _, ok := parsedP2PEscrowABI.Methods[m]; !ok {
			t.Fatalf("p2pEscrowABI lacks %s", m)
		}
	}
}
