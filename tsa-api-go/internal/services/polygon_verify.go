package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// erc20TransferTopic is keccak256("Transfer(address,address,uint256)").
const erc20TransferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

var (
	ErrTransferNotFound       = errors.New("verify: no matching ERC-20 transfer in that transaction")
	ErrTransferReverted       = errors.New("verify: transaction reverted")
	ErrTransferWrongRecipient = errors.New("verify: transfer did not credit the expected address")
)

// ethLogReader is the slice of an Ethereum client this verifier needs.
type ethLogReader interface {
	TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error)
}

// ERC20TransferVerifier proves on-chain that a transaction credited an address.
type ERC20TransferVerifier interface {
	VerifyTransfer(ctx context.Context, txHash, token, to string) (*big.Int, error)
}

// LogTransferVerifier reads a receipt and sums the ERC-20 Transfer logs that
// credited a given address with a given token.
//
// This is the authority for "did the user's money actually reach Flutterwave".
// LiFi's status API reports the same thing and is used to FIND the destination
// transaction, but a provider's own claim is not evidence that funds moved —
// the receipt is.
type LogTransferVerifier struct{ rpc ethLogReader }

func NewLogTransferVerifier(rpc ethLogReader) *LogTransferVerifier {
	return &LogTransferVerifier{rpc: rpc}
}

// VerifyTransfer returns the total base units of `token` credited to `to` by
// the transaction. Transfers to other recipients, and logs from other token
// contracts, are excluded — a payout may only ever be funded by what actually
// reached the destination.
func (v *LogTransferVerifier) VerifyTransfer(ctx context.Context, txHash, token, to string) (*big.Int, error) {
	return v.sumTransfers(ctx, txHash, token, "", to)
}

// ERC20SourcedTransferVerifier proves a transaction moved a token from one
// address to another — for a payment whose payer matters as much as its
// recipient (it is who a refund goes back to, and one user must not be able to
// claim another's transfer).
type ERC20SourcedTransferVerifier interface {
	VerifyTransferFrom(ctx context.Context, txHash, token, from, to string) (*big.Int, error)
}

// VerifyTransferFrom is VerifyTransfer counting only the transfers `from` made.
func (v *LogTransferVerifier) VerifyTransferFrom(ctx context.Context, txHash, token, from, to string) (*big.Int, error) {
	if !common.IsHexAddress(from) {
		return nil, fmt.Errorf("%w: invalid sender %q", ErrTransferNotFound, from)
	}
	return v.sumTransfers(ctx, txHash, token, from, to)
}

// sumTransfers adds up the Transfer logs of token to `to` in one receipt, and
// only those from `from` when it is set.
func (v *LogTransferVerifier) sumTransfers(ctx context.Context, txHash, token, from, to string) (*big.Int, error) {
	receipt, err := v.rpc.TransactionReceipt(ctx, common.HexToHash(txHash))
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	if receipt == nil {
		return nil, ErrTransferNotFound
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return nil, fmt.Errorf("%w: %s", ErrTransferReverted, txHash)
	}

	wantToken := common.HexToAddress(token)
	wantTo := common.HexToHash(strings.ToLower(to))
	var wantFrom common.Hash
	if from != "" {
		wantFrom = common.HexToHash(strings.ToLower(from))
	}
	total := new(big.Int)
	found := false

	for _, lg := range receipt.Logs {
		if lg == nil || len(lg.Topics) < 3 {
			continue
		}
		if lg.Address != wantToken {
			continue
		}
		if !strings.EqualFold(lg.Topics[0].Hex(), erc20TransferTopic) {
			continue
		}
		if lg.Topics[2] != wantTo {
			continue
		}
		if from != "" && lg.Topics[1] != wantFrom {
			continue
		}
		total.Add(total, new(big.Int).SetBytes(lg.Data))
		found = true
	}
	if !found {
		return nil, fmt.Errorf("%w: token=%s to=%s tx=%s", ErrTransferNotFound, token, to, txHash)
	}
	return total, nil
}

// TransferVerdict is what one on-chain transfer check proved.
type TransferVerdict int

const (
	// TransferProven: the transfer happened and credited a positive amount.
	// The only verdict anything may be settled on.
	TransferProven TransferVerdict = iota
	// TransferUnproven: not mined yet, not readable, or nothing credited —
	// look again later.
	TransferUnproven
	// TransferRefuted: the transaction reverted or paid someone else. That is a
	// settled question with a bad answer; waiting will never change it.
	TransferRefuted
)

// ClassifyTransfer sorts the answer of a VerifyTransfer / VerifyTransferFrom
// call into what a caller may do with it. Every caller that proves a payment
// on-chain goes through this, so none of them can drift in what "proven" means.
func ClassifyTransfer(credited *big.Int, err error) TransferVerdict {
	switch {
	case errors.Is(err, ErrTransferReverted) || errors.Is(err, ErrTransferWrongRecipient):
		return TransferRefuted
	case err != nil, credited == nil, credited.Sign() <= 0:
		return TransferUnproven
	}
	return TransferProven
}
