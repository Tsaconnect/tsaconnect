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
		total.Add(total, new(big.Int).SetBytes(lg.Data))
		found = true
	}
	if !found {
		return nil, fmt.Errorf("%w: token=%s to=%s tx=%s", ErrTransferNotFound, token, to, txHash)
	}
	return total, nil
}
