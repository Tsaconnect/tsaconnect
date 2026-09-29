package services

import (
	"context"
	"errors"
	"fmt"
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

// A sourced check counts only what the named sender moved: one user's transfer
// must not be claimable as another's payment.
func TestVerifyTransferFrom_CountsOnlyTheSendersTransfers(t *testing.T) {
	payer := "0x2222222222222222222222222222222222222222"
	r := &stubLogReader{receipt: &types.Receipt{
		Status: types.ReceiptStatusSuccessful,
		Logs: []*types.Log{
			transferLog(testToken, payer, testFWAddr, big.NewInt(1_000_000)),
			transferLog(testToken, testOther, testFWAddr, big.NewInt(7)),
		},
	}}
	v := NewLogTransferVerifier(r)
	got, err := v.VerifyTransferFrom(context.Background(), "0xdest", testToken, payer, testFWAddr)
	if err != nil || got.String() != "1000000" {
		t.Fatalf("got %v err %v", got, err)
	}
	if _, err := v.VerifyTransferFrom(context.Background(), "0xdest", testToken, "0x3333333333333333333333333333333333333333", testFWAddr); !errors.Is(err, ErrTransferNotFound) {
		t.Fatalf("a sender who moved nothing: err = %v", err)
	}
}

func TestClassifyTransfer(t *testing.T) {
	for name, tc := range map[string]struct {
		credited *big.Int
		err      error
		want     TransferVerdict
	}{
		"proven":            {big.NewInt(1), nil, TransferProven},
		"nothing credited":  {big.NewInt(0), nil, TransferUnproven},
		"not mined yet":     {nil, ErrTransferNotFound, TransferUnproven},
		"rpc down":          {nil, errors.New("dial tcp: timeout"), TransferUnproven},
		"reverted":          {nil, fmt.Errorf("%w: 0xabc", ErrTransferReverted), TransferRefuted},
		"paid someone else": {nil, ErrTransferWrongRecipient, TransferRefuted},
	} {
		if got := ClassifyTransfer(tc.credited, tc.err); got != tc.want {
			t.Errorf("%s: got %v want %v", name, got, tc.want)
		}
	}
}

// A mined, successful transaction with no matching Transfer says so as its own
// error — still ErrTransferNotFound to every caller that waits on that, and
// still unproven (not refuted) to ClassifyTransfer: only a caller that knows
// the receipt is deep enough may treat it as final.
func TestVerifyTransferFrom_MinedWithoutAMatchIsNoMatch(t *testing.T) {
	r := &stubLogReader{receipt: &types.Receipt{
		Status: types.ReceiptStatusSuccessful,
		Logs:   []*types.Log{transferLog(testToken, testOther, testFWAddr, big.NewInt(1_000_000))},
	}}
	payer := "0x2222222222222222222222222222222222222222"
	got, err := NewLogTransferVerifier(r).VerifyTransferFrom(context.Background(), "0x1", testToken, payer, testFWAddr)
	if !errors.Is(err, ErrTransferNoMatch) || !errors.Is(err, ErrTransferNotFound) || got != nil {
		t.Fatalf("got %v err %v", got, err)
	}
	if ClassifyTransfer(got, err) != TransferUnproven {
		t.Fatal("no match must stay unproven for callers that don't know the depth")
	}
	// Not mined: plain not-found, not "no match".
	_, err = NewLogTransferVerifier(&stubLogReader{}).VerifyTransferFrom(context.Background(), "0x1", testToken, payer, testFWAddr)
	if !errors.Is(err, ErrTransferNotFound) || errors.Is(err, ErrTransferNoMatch) {
		t.Fatalf("unmined: err = %v", err)
	}
}
