package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// fakeFeeTxReader answers the reads a fee proof makes for one signed transaction.
type fakeFeeTxReader struct {
	tx      *types.Transaction
	block   uint64
	head    uint64
	blockAt time.Time
	txErr   error
}

func (f *fakeFeeTxReader) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	return f.tx, false, f.txErr
}
func (f *fakeFeeTxReader) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	return &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: new(big.Int).SetUint64(f.block)}, nil
}
func (f *fakeFeeTxReader) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	return &types.Header{Time: uint64(f.blockAt.Unix())}, nil
}
func (f *fakeFeeTxReader) BlockNumber(context.Context) (uint64, error) { return f.head, nil }

type fakeSourcedVerifier struct {
	credited *big.Int
	err      error
}

func (f fakeSourcedVerifier) VerifyTransfer(context.Context, string, string, string) (*big.Int, error) {
	return f.credited, f.err
}
func (f fakeSourcedVerifier) VerifyTransferFrom(context.Context, string, string, string, string) (*big.Int, error) {
	return f.credited, f.err
}

func signedFeeTx(t *testing.T) (*types.Transaction, string) {
	t.Helper()
	key, _ := crypto.GenerateKey()
	signer := types.LatestSignerForChainID(big.NewInt(8453))
	tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(8453), Nonce: 1, Gas: 60000,
		GasFeeCap: big.NewInt(1), GasTipCap: big.NewInt(1)}), signer, key)
	if err != nil {
		t.Fatal(err)
	}
	return tx, crypto.PubkeyToAddress(key.PublicKey).Hex()
}

// A mined transaction that moved nothing matching still reports how deep it
// is (nothing credited), so the caller can decide when "no match" is final.
func TestTreasuryFeeRail_NoMatchStillReportsDepth(t *testing.T) {
	tx, from := signedFeeTx(t)
	rail := NewTreasuryFeeRail(nil,
		map[string]ERC20TransferVerifier{"base": fakeSourcedVerifier{err: fmt.Errorf("%w: tx=0x1", ErrTransferNoMatch)}},
		map[string]FeeTxReader{"base": &fakeFeeTxReader{tx: tx, block: 100, head: 109, blockAt: time.Now()}})
	p, err := rail.VerifyPayment(context.Background(), "base", "0x1", "0xtoken", "0xfrom", "0xto")
	if !errors.Is(err, ErrTransferNoMatch) || !errors.Is(err, ErrTransferNotFound) {
		t.Fatalf("err = %v", err)
	}
	if p == nil || p.Confirmations != 10 || p.Credited == nil || p.Credited.Sign() != 0 || p.Sender != from {
		t.Fatalf("payment %+v", p)
	}
	// Any other refusal still reports nothing.
	rail = NewTreasuryFeeRail(nil,
		map[string]ERC20TransferVerifier{"base": fakeSourcedVerifier{err: ErrTransferNotFound}},
		map[string]FeeTxReader{"base": &fakeFeeTxReader{tx: tx}})
	if p, err := rail.VerifyPayment(context.Background(), "base", "0x1", "0xtoken", "0xfrom", "0xto"); p != nil || !errors.Is(err, ErrTransferNotFound) {
		t.Fatalf("unmined: %+v %v", p, err)
	}
}

// The rail reports who SIGNED the fee transaction (not just whose tokens
// moved), when it was mined and how deep it is — what a fee is held to.
func TestTreasuryFeeRail_VerifyPaymentReportsSignerTimeAndDepth(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := types.LatestSignerForChainID(big.NewInt(8453))
	tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(8453), Nonce: 1, Gas: 60000,
		GasFeeCap: big.NewInt(1), GasTipCap: big.NewInt(1)}), signer, key)
	if err != nil {
		t.Fatal(err)
	}
	minedAt := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	rail := NewTreasuryFeeRail(nil,
		map[string]ERC20TransferVerifier{"base": fakeSourcedVerifier{credited: big.NewInt(1_000_000)}},
		map[string]FeeTxReader{"base": &fakeFeeTxReader{tx: tx, block: 100, head: 104, blockAt: minedAt}})

	p, err := rail.VerifyPayment(context.Background(), "Base", "0x1", "0xtoken", "0xfrom", "0xto")
	if err != nil {
		t.Fatal(err)
	}
	if p.Sender != crypto.PubkeyToAddress(key.PublicKey).Hex() || !p.BlockTime.Equal(minedAt) || p.Confirmations != 5 ||
		p.Credited.String() != "1000000" {
		t.Fatalf("payment %+v", p)
	}
	if _, err := rail.VerifyPayment(context.Background(), "bsc", "0x1", "0xtoken", "0xfrom", "0xto"); err == nil {
		t.Fatal("a chain with no verifier must not prove anything")
	}
}

// TransactionKnown says "unknown" only on the node's definite not-found; any
// other failed read is an error, never taken as a payment that vanished.
func TestTreasuryFeeRail_TransactionKnown(t *testing.T) {
	tx, from := signedFeeTx(t)
	for name, tc := range map[string]struct {
		err     error
		known   bool
		sender  string
		wantErr bool
	}{
		// The signer is read even before the transaction mines.
		"mined or pending": {known: true, sender: from},
		"not found":        {err: ethereum.NotFound},
		"rpc failure":      {err: errors.New("timeout"), wantErr: true},
	} {
		rail := NewTreasuryFeeRail(nil, nil, map[string]FeeTxReader{"base": &fakeFeeTxReader{tx: tx, txErr: tc.err}})
		known, sender, err := rail.TransactionKnown(context.Background(), "Base", "0x1")
		if known != tc.known || sender != tc.sender || (err != nil) != tc.wantErr {
			t.Errorf("%s: known=%v sender=%s err=%v", name, known, sender, err)
		}
	}
	if _, _, err := NewTreasuryFeeRail(nil, nil, nil).TransactionKnown(context.Background(), "bsc", "0x1"); err == nil {
		t.Fatal("a chain with no reader is an error, not an unknown transaction")
	}
}
