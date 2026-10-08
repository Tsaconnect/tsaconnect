package blockchain

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

// Why DecodeSignedTx refused a signed transaction — one per step, so each
// caller can answer in its own words.
var (
	ErrSignedTxHex       = errors.New("signed transaction: not 0x-prefixed hex")
	ErrSignedTxEncoding  = errors.New("signed transaction: not a valid transaction encoding")
	ErrSignedTxSignature = errors.New("signed transaction: signature cannot be recovered")
)

// DecodeSignedTx reads a 0x-hex signed transaction (legacy or typed, in its
// canonical binary encoding) and recovers who signed it. The chain credits a
// transaction to its signer, so the signer — never an address a client states —
// is what any check of "who is spending" must use.
func DecodeSignedTx(raw string) (*types.Transaction, common.Address, error) {
	b, err := hexutil.Decode(raw)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("%w: %v", ErrSignedTxHex, err)
	}
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(b); err != nil {
		return nil, common.Address{}, fmt.Errorf("%w: %v", ErrSignedTxEncoding, err)
	}
	signer, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("%w: %v", ErrSignedTxSignature, err)
	}
	return tx, signer, nil
}

// ERC20TransferData is the calldata of an ERC-20 transfer(to, amount).
func ERC20TransferData(to common.Address, amount *big.Int) ([]byte, error) {
	return parsedERC20ABI.Pack("transfer", to, amount)
}
