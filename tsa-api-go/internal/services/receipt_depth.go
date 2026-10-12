package services

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
)

// receiptDeep: hash's receipt has status want and is at least need blocks
// deep, read on the endpoint gen (extracted from the company wallet's
// saleConfirmed; Task 4 makes saleConfirmed call it). Non-blocking: one
// receipt read, one head read — never WaitForReceipt (review I-8).
func receiptDeep(ctx context.Context, probe TreasuryTxProbe, hash string, want, need, gen uint64) (bool, error) {
	receipt, err := probe.TransactionReceipt(ctx, common.HexToHash(hash))
	if err != nil || receipt == nil || receipt.BlockNumber == nil || receipt.Status != want {
		return false, nil
	}
	head, err := probe.BlockNumber(ctx)
	if err != nil || endpointSteady(probe, gen) != nil {
		return false, nil
	}
	mined := receipt.BlockNumber.Uint64()
	if need < 1 {
		need = 1
	}
	return head >= mined && head-mined+1 >= need, nil
}
