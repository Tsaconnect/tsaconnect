package services

import (
	"context"
	"math/big"
	"testing"
	"time"

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
}

func (f *fakeFeeTxReader) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	return f.tx, false, nil
}
func (f *fakeFeeTxReader) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	return &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: new(big.Int).SetUint64(f.block)}, nil
}
func (f *fakeFeeTxReader) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	return &types.Header{Time: uint64(f.blockAt.Unix())}, nil
}
func (f *fakeFeeTxReader) BlockNumber(context.Context) (uint64, error) { return f.head, nil }

type fakeSourcedVerifier struct{ credited *big.Int }

func (f fakeSourcedVerifier) VerifyTransfer(context.Context, string, string, string) (*big.Int, error) {
	return f.credited, nil
}
func (f fakeSourcedVerifier) VerifyTransferFrom(context.Context, string, string, string, string) (*big.Int, error) {
	return f.credited, nil
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
