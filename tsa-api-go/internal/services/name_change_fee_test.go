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

	logs     []types.Log
	logsErr  error
	maxSpan  uint64
	logCalls int
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

// FilterLogs serves logs the way eth_getLogs does — by range, address and
// topics — refusing any span over maxSpan (0: none), or everything on logsErr.
func (f *fakeFeeTxReader) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	f.logCalls++
	from, to := q.FromBlock.Uint64(), q.ToBlock.Uint64()
	if f.logsErr != nil {
		return nil, f.logsErr
	}
	if f.maxSpan > 0 && to-from+1 > f.maxSpan {
		return nil, errors.New("block range too large")
	}
	var out []types.Log
	for _, l := range f.logs {
		if l.BlockNumber < from || l.BlockNumber > to || (len(q.Addresses) > 0 && l.Address != q.Addresses[0]) {
			continue
		}
		match := true
		for i, want := range q.Topics {
			if len(want) > 0 && (i >= len(l.Topics) || l.Topics[i] != want[0]) {
				match = false
			}
		}
		if match {
			out = append(out, l)
		}
	}
	return out, nil
}

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

// With no refund hash to ask about, "did the refund leave?" is answered from
// the token's Transfer logs since the send was claimed: exactly the refund,
// from the treasury, to the paying wallet, in a block at or after the claim —
// scanned in chunks the RPC accepts. A scan that cannot finish, or a node
// behind the claim block, is an error, never "no transfer".
func TestTreasuryFeeRail_TransferMinedSince(t *testing.T) {
	ctx := context.Background()
	token := "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	treasury := "0x000000000000000000000000000000000000fEE5"
	payer := "0x1111111111111111111111111111111111111111"
	transfer := func(block uint64, from, to string, amount int64, removed bool) types.Log {
		return types.Log{Address: common.HexToAddress(token), BlockNumber: block, Removed: removed,
			Topics: []common.Hash{common.HexToHash(erc20TransferTopic), common.BytesToHash(common.HexToAddress(from).Bytes()),
				common.BytesToHash(common.HexToAddress(to).Bytes())},
			Data: common.LeftPadBytes(big.NewInt(amount).Bytes(), 32)}
	}
	decoys := []types.Log{
		transfer(900, treasury, payer, 1_000_000, false),     // before the claim
		transfer(1500, treasury, payer, 999_999, false),      // another amount
		transfer(2500, treasury, payer, 1_000_000, true),     // reorged out
		transfer(3000, payer, treasury, 1_000_000, false),    // the other way
		transfer(3500, treasury, treasury, 1_000_000, false), // to someone else
	}
	rail := func(r *fakeFeeTxReader) *TreasuryFeeRail {
		return NewTreasuryFeeRail(nil, nil, map[string]FeeTxReader{"base": r})
	}

	none := &fakeFeeTxReader{head: 9000, logs: decoys, maxSpan: 1000}
	if found, err := rail(none).TransferMinedSince(ctx, "base", token, treasury, payer, big.NewInt(1_000_000), 1000); err != nil || found {
		t.Fatalf("decoys only: found=%v err=%v", found, err)
	}
	if none.logCalls < 8 {
		t.Fatalf("a 1000-block RPC cap must be scanned in chunks, got %d calls", none.logCalls)
	}
	hit := &fakeFeeTxReader{head: 9000, logs: append(decoys, transfer(8999, treasury, payer, 1_000_000, false))}
	if found, err := rail(hit).TransferMinedSince(ctx, "base", token, treasury, payer, big.NewInt(1_000_000), 1000); err != nil || !found {
		t.Fatalf("the refund: found=%v err=%v", found, err)
	}
	for name, r := range map[string]*fakeFeeTxReader{
		"rpc refuses":       {head: 9000, logsErr: errors.New("rpc down")},
		"node behind claim": {head: 500},
	} {
		if found, err := rail(r).TransferMinedSince(ctx, "base", token, treasury, payer, big.NewInt(1_000_000), 1000); err == nil || found {
			t.Errorf("%s: found=%v err=%v, want an error", name, found, err)
		}
	}
	if _, err := rail(none).TransferMinedSince(ctx, "bsc", token, treasury, payer, big.NewInt(1), 1); err == nil {
		t.Fatal("a chain with no reader must be an error")
	}
}
