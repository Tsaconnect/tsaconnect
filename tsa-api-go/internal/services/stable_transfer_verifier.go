package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Proving that one stablecoin transaction moved a token from one wallet to
// another, signed by the first, deep enough to count. A name-change fee (paid
// to the ramp treasury) and a TSA Pay refund (paid by a merchant to a payer)
// are proven by the same reads and held to the same policy; only the wallets
// and the chains differ.

// StablePayment is what one transaction proved.
type StablePayment struct {
	// Credited is what the Transfer logs moved from the payer to the treasury.
	Credited *big.Int
	// Sender signed the transaction. A fee must be sent by the proven wallet
	// itself, not moved out of it by some contract it once approved.
	Sender string
	// BlockTime dates the transaction: a transfer from before the change was
	// opened cannot have been made for it.
	BlockTime time.Time
	// Confirmations is how deep the transaction is.
	Confirmations uint64
}

// FeeTxReader is the slice of an Ethereum client a fee proof needs;
// *ethclient.Client satisfies it.
type FeeTxReader interface {
	TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, bool, error)
	TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	BlockNumber(ctx context.Context) (uint64, error)
}

// FeeTxReaders is a transaction reader for each chain key that has a client.
func FeeTxReaders(chains *BlockchainService, keys []string) map[string]FeeTxReader {
	out := map[string]FeeTxReader{}
	for _, k := range keys {
		if c := chains.ClientForChain(k); c != nil && c.Client != nil {
			out[strings.ToLower(k)] = c.Client
		}
	}
	return out
}

// StableTransferVerifier proves stablecoin transfers on the chains it has a
// transfer verifier and a transaction reader for. Which chains those are is
// the caller's choice: the name-change fee uses the treasury's lanes, refunds
// every mainnet chain with a client.
type StableTransferVerifier struct {
	verifiers map[string]ERC20TransferVerifier
	readers   map[string]FeeTxReader
}

// NewStableTransferVerifier wires the per-chain transfer verifiers and
// transaction readers, both keyed by chain key.
func NewStableTransferVerifier(verifiers map[string]ERC20TransferVerifier, readers map[string]FeeTxReader) *StableTransferVerifier {
	return &StableTransferVerifier{verifiers: verifiers, readers: readers}
}

// Verify is what the transaction moved of the token from `from` to `to`, and
// the facts about the transaction a payment is held to — errors as
// ERC20SourcedTransferVerifier reports them. On ErrTransferNoMatch (mined,
// nothing matched) it still returns the payment, with nothing credited, so
// the caller can tell how deep the transaction is.
func (v *StableTransferVerifier) Verify(ctx context.Context, chain, txHash, tokenAddress, from, to string) (*StablePayment, error) {
	key := strings.ToLower(strings.TrimSpace(chain))
	sv, ok := v.verifiers[key].(ERC20SourcedTransferVerifier)
	reader := v.readers[key]
	if !ok || reader == nil {
		return nil, fmt.Errorf("%w: no verifier for %s", ErrTransferNotFound, chain)
	}
	credited, verr := sv.VerifyTransferFrom(ctx, txHash, tokenAddress, from, to)
	if errors.Is(verr, ErrTransferReverted) {
		// A reverted receipt is still mined: report how deep, best effort, so a
		// caller that must not act on a shallow revert can tell. If the depth
		// cannot be read the revert is reported with no payment, as before.
		if depth, ok := receiptDepth(ctx, reader, txHash); ok {
			return &StablePayment{Credited: new(big.Int), Confirmations: depth}, verr
		}
		return nil, verr
	}
	if verr != nil && !errors.Is(verr, ErrTransferNoMatch) {
		return nil, verr
	}
	hash := common.HexToHash(txHash)
	tx, _, err := reader.TransactionByHash(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("verify: read transaction: %w", err)
	}
	sender, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
	if err != nil {
		return nil, fmt.Errorf("verify: recover sender: %w", err)
	}
	receipt, err := reader.TransactionReceipt(ctx, hash)
	if err != nil || receipt == nil || receipt.BlockNumber == nil {
		return nil, fmt.Errorf("verify: read receipt: %v", err)
	}
	header, err := reader.HeaderByNumber(ctx, receipt.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("verify: read block: %w", err)
	}
	head, err := reader.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("verify: read chain head: %w", err)
	}
	depth := depthOf(receipt.BlockNumber, head)
	p := &StablePayment{Credited: credited, Sender: sender.Hex(), BlockTime: time.Unix(int64(header.Time), 0).UTC(), Confirmations: depth}
	if verr != nil {
		p.Credited = new(big.Int)
		return p, verr
	}
	return p, nil
}

func depthOf(block *big.Int, head uint64) uint64 {
	if n := block.Uint64(); head >= n {
		return head - n + 1
	}
	return 0
}

func receiptDepth(ctx context.Context, reader FeeTxReader, txHash string) (uint64, bool) {
	receipt, err := reader.TransactionReceipt(ctx, common.HexToHash(txHash))
	if err != nil || receipt == nil || receipt.BlockNumber == nil {
		return 0, false
	}
	head, err := reader.BlockNumber(ctx)
	if err != nil {
		return 0, false
	}
	return depthOf(receipt.BlockNumber, head), true
}

// TransactionKnown reports whether the chain knows txHash at all, mined or
// pending, and who signed it (empty when that cannot be recovered).
// known=false with a nil error only on a definite "no such transaction" — a
// read that fails is an error, never "unknown".
func (v *StableTransferVerifier) TransactionKnown(ctx context.Context, chain, txHash string) (bool, string, error) {
	reader, err := v.reader(chain)
	if err != nil {
		return false, "", err
	}
	tx, _, err := reader.TransactionByHash(ctx, common.HexToHash(txHash))
	switch {
	case errors.Is(err, ethereum.NotFound):
		return false, "", nil
	case err != nil:
		return false, "", err
	}
	// The signer is readable while the transaction is still pending — which is
	// what lets a hash named by someone other than its sender be told apart
	// before it mines.
	if tx == nil {
		return true, "", nil
	}
	sender, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
	if err != nil {
		return true, "", nil
	}
	return true, sender.Hex(), nil
}

func (v *StableTransferVerifier) reader(chain string) (FeeTxReader, error) {
	reader := v.readers[strings.ToLower(strings.TrimSpace(chain))]
	if reader == nil {
		return nil, fmt.Errorf("no transaction reader for %s", chain)
	}
	return reader, nil
}

// StableTransferVerdict is what one look at a transfer settled.
type StableTransferVerdict int

const (
	StableTransferPending StableTransferVerdict = iota // not mined, not deep enough, or unreadable
	StableTransferRefuted                              // will never be the payment asked for
	StableTransferProven
)

// StableTransferReason says why, so each caller can word it its own way.
type StableTransferReason int

const (
	StableReasonNone         StableTransferReason = iota
	StableReasonUnreadable                        // no receipt yet, or the chain could not be read
	StableReasonShallow                           // mined, not deep enough yet
	StableReasonNothingMoved                      // mined deep, moved none of the token from `from` to `to`
	StableReasonReverted                          // reverted, or paid someone else
	StableReasonWrongSender                       // signed by another wallet
	StableReasonTooEarly                          // mined before it was asked for
)

// StableTransferWant is what a transfer is held to: signed by From, mined no
// earlier than NotBefore (zero: any time), at least MinConf deep.
// RevertNeedsDepth: a reverted receipt is refuted only once it is MinConf
// deep; shallower, it is not yet proven (a reorg could still replace it).
type StableTransferWant struct {
	From             string
	NotBefore        time.Time
	MinConf          uint64
	RevertNeedsDepth bool
}

// StableTransferOutcome is ProveStableTransfer's answer.
type StableTransferOutcome struct {
	Verdict StableTransferVerdict
	Reason  StableTransferReason
}

// ProveStableTransfer holds what Verify returned (p, err) to want. A mined
// transfer that matched nothing is final only once it is MinConf deep — until
// then a reorg could still change it. "Not found" is never final.
func ProveStableTransfer(p *StablePayment, err error, want StableTransferWant) StableTransferOutcome {
	if errors.Is(err, ErrTransferNoMatch) {
		if p != nil && p.Confirmations >= want.MinConf {
			return StableTransferOutcome{StableTransferRefuted, StableReasonNothingMoved}
		}
		return StableTransferOutcome{StableTransferPending, StableReasonShallow}
	}
	var credited *big.Int
	if p != nil {
		credited = p.Credited
	}
	switch ClassifyTransfer(credited, err) {
	case TransferRefuted:
		if want.RevertNeedsDepth && errors.Is(err, ErrTransferReverted) {
			if p == nil {
				return StableTransferOutcome{StableTransferPending, StableReasonUnreadable}
			}
			if p.Confirmations < want.MinConf {
				return StableTransferOutcome{StableTransferPending, StableReasonShallow}
			}
		}
		return StableTransferOutcome{StableTransferRefuted, StableReasonReverted}
	case TransferUnproven:
		return StableTransferOutcome{StableTransferPending, StableReasonUnreadable}
	}
	if !strings.EqualFold(p.Sender, want.From) {
		return StableTransferOutcome{StableTransferRefuted, StableReasonWrongSender}
	}
	if !want.NotBefore.IsZero() && p.BlockTime.Before(want.NotBefore) {
		return StableTransferOutcome{StableTransferRefuted, StableReasonTooEarly}
	}
	if p.Confirmations < want.MinConf {
		return StableTransferOutcome{StableTransferPending, StableReasonShallow}
	}
	return StableTransferOutcome{StableTransferProven, StableReasonNone}
}
