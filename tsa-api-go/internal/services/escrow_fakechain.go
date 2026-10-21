package services

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// FakeEscrowChain is an in-memory escrow for tests of code that reads orders
// and receipts through EscrowService (see NewEscrowServiceWithBackend). It
// answers the orders(id) getter and transaction receipts, nothing else.
type FakeEscrowChain struct {
	orders   map[[32]byte]OnChainOrder
	receipts map[string]*types.Receipt
	// Err, when set, makes every read fail like an unreachable node.
	Err error
}

func NewFakeEscrowChain() *FakeEscrowChain {
	return &FakeEscrowChain{orders: map[[32]byte]OnChainOrder{}, receipts: map[string]*types.Receipt{}}
}

// SetOrder stores o as the escrow's record of id.
func (f *FakeEscrowChain) SetOrder(id [32]byte, o OnChainOrder) { f.orders[id] = o }

// MineEvent records a successful transaction that emitted event for id on
// escrowAddr; data is the event's non-indexed payload.
func (f *FakeEscrowChain) MineEvent(txHash, escrowAddr, event string, id [32]byte, data []byte) {
	f.receipts[strings.ToLower(txHash)] = &types.Receipt{Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{{
		Address: common.HexToAddress(escrowAddr),
		Topics:  []common.Hash{parsedEscrowABI.Events[event].ID, common.Hash(id)},
		Data:    data,
	}}}
}

// MineFailed records a transaction that mined and reverted.
func (f *FakeEscrowChain) MineFailed(txHash string) {
	f.receipts[strings.ToLower(txHash)] = &types.Receipt{Status: types.ReceiptStatusFailed}
}

// DisputeResolvedData is the payload of a DisputeResolved log.
func DisputeResolvedData(refundedToBuyer bool) []byte {
	data, err := parsedEscrowABI.Events["DisputeResolved"].Inputs.NonIndexed().Pack(refundedToBuyer)
	if err != nil {
		panic(err)
	}
	return data
}

func (f *FakeEscrowChain) GetTransactionReceipt(txHash string) (*types.Receipt, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	if r, ok := f.receipts[strings.ToLower(txHash)]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("failed to get receipt for %s: %w", txHash, ethereum.NotFound)
}

func (f *FakeEscrowChain) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	args, err := parsedEscrowABI.Methods["orders"].Inputs.Unpack(call.Data[4:])
	if err != nil {
		return nil, err
	}
	id := args[0].([32]byte)
	o := f.orders[id] // zero value: no order, as the contract answers
	zero := big.NewInt(0)
	if o.ProductAmount == nil {
		o.ProductAmount = zero
	}
	if o.ShippingAmount == nil {
		o.ShippingAmount = zero
	}
	if o.PlatformFee == nil {
		o.PlatformFee = zero
	}
	return parsedEscrowABI.Methods["orders"].Outputs.Pack(
		o.Buyer, o.Seller, o.Token, o.ProductAmount, o.ShippingAmount, o.PlatformFee,
		common.Address{}, big.NewInt(o.CreatedAt.Unix()), o.SellerDelivered, false, false, o.Resolved)
}
