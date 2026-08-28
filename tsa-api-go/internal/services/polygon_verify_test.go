package services

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	testToken  = "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359" // USDC on Polygon
	testFWAddr = "0x742d35Cc6634C0532925a3b844Bc454e4438f44e"
	testOther  = "0x1111111111111111111111111111111111111111"
)

type stubLogReader struct {
	receipt *types.Receipt
	err     error
}

func (s *stubLogReader) TransactionReceipt(_ context.Context, _ common.Hash) (*types.Receipt, error) {
	return s.receipt, s.err
}

func transferLog(token, from, to string, amount *big.Int) *types.Log {
	return &types.Log{
		Address: common.HexToAddress(token),
		Topics: []common.Hash{
			common.HexToHash(erc20TransferTopic),
			common.HexToHash(from),
			common.HexToHash(to),
		},
		Data: common.LeftPadBytes(amount.Bytes(), 32),
	}
}

func TestVerifyTransfer_ReturnsTheAmountCreditedToFlutterwave(t *testing.T) {
	r := &stubLogReader{receipt: &types.Receipt{
		Status: types.ReceiptStatusSuccessful,
		Logs:   []*types.Log{transferLog(testToken, testOther, testFWAddr, big.NewInt(99884217))},
	}}
	got, err := NewLogTransferVerifier(r).VerifyTransfer(context.Background(), "0xdest", testToken, testFWAddr)
	if err != nil {
		t.Fatalf("VerifyTransfer: %v", err)
	}
	if got.String() != "99884217" {
		t.Fatalf("amount = %s, want 99884217", got)
	}
}

func TestVerifyTransfer_SumsMultipleTransfersToTheSameRecipient(t *testing.T) {
	r := &stubLogReader{receipt: &types.Receipt{
		Status: types.ReceiptStatusSuccessful,
		Logs: []*types.Log{
			transferLog(testToken, testOther, testFWAddr, big.NewInt(1000000)),
			transferLog(testToken, testOther, testFWAddr, big.NewInt(500000)),
		},
	}}
	got, err := NewLogTransferVerifier(r).VerifyTransfer(context.Background(), "0xdest", testToken, testFWAddr)
	if err != nil {
		t.Fatalf("VerifyTransfer: %v", err)
	}
	if got.String() != "1500000" {
		t.Fatalf("amount = %s, want 1500000", got)
	}
}

func TestVerifyTransfer_IgnoresTransfersToOtherAddresses(t *testing.T) {
	// A transaction can move the same token to several recipients. Only what
	// reached Flutterwave may fund a payout.
	r := &stubLogReader{receipt: &types.Receipt{
		Status: types.ReceiptStatusSuccessful,
		Logs: []*types.Log{
			transferLog(testToken, testOther, testOther, big.NewInt(5000000)),
			transferLog(testToken, testOther, testFWAddr, big.NewInt(1000000)),
		},
	}}
	got, err := NewLogTransferVerifier(r).VerifyTransfer(context.Background(), "0xdest", testToken, testFWAddr)
	if err != nil {
		t.Fatalf("VerifyTransfer: %v", err)
	}
	if got.String() != "1000000" {
		t.Fatalf("amount = %s, want 1000000 (other recipients excluded)", got)
	}
}

func TestVerifyTransfer_IgnoresOtherTokens(t *testing.T) {
	// A USDT log must never satisfy a USDC intent.
	r := &stubLogReader{receipt: &types.Receipt{
		Status: types.ReceiptStatusSuccessful,
		Logs:   []*types.Log{transferLog(testOther, testOther, testFWAddr, big.NewInt(9999999))},
	}}
	if _, err := NewLogTransferVerifier(r).VerifyTransfer(context.Background(), "0xdest", testToken, testFWAddr); err == nil {
		t.Fatal("a different token satisfied the verification")
	}
}

func TestVerifyTransfer_RejectsARevertedTransaction(t *testing.T) {
	r := &stubLogReader{receipt: &types.Receipt{Status: types.ReceiptStatusFailed}}
	if _, err := NewLogTransferVerifier(r).VerifyTransfer(context.Background(), "0xdest", testToken, testFWAddr); err == nil {
		t.Fatal("reverted transaction accepted")
	}
}

func TestVerifyTransfer_RejectsWhenNoMatchingLogExists(t *testing.T) {
	r := &stubLogReader{receipt: &types.Receipt{Status: types.ReceiptStatusSuccessful}}
	if _, err := NewLogTransferVerifier(r).VerifyTransfer(context.Background(), "0xdest", testToken, testFWAddr); err == nil {
		t.Fatal("empty receipt accepted")
	}
}
