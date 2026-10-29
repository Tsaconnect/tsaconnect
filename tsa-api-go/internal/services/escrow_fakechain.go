package services

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// FakeEscrowChain is an in-memory OrderEscrow (and its FeeConfig) for tests of code that reads
// orders, fees and receipts through EscrowService (see NewEscrowServiceWithBackend). It answers
// getOrder(key), FeeConfig's rawRate/limitsFor, and transaction receipts.
type FakeEscrowChain struct {
	orders   map[[32]byte]OnChainOrder
	receipts map[string]*types.Receipt
	// Rate and Limits are FeeConfig's order row and limits for every token.
	Rate   FeeConfigRate
	Limits *FeeConfigLimits
	// Err, when set, makes every read fail like an unreachable node.
	Err error
}

func NewFakeEscrowChain() *FakeEscrowChain {
	return &FakeEscrowChain{orders: map[[32]byte]OnChainOrder{}, receipts: map[string]*types.Receipt{},
		Rate:   FeeConfigRate{BPS: 200, FlatFee: big.NewInt(100_000), Active: true},
		Limits: &FeeConfigLimits{MaxPerOrder: big.NewInt(1_000_000_000), MaxOwed: big.NewInt(20_000_000_000)}}
}

// SetOrder stores o as the escrow's record of key.
func (f *FakeEscrowChain) SetOrder(key [32]byte, o OnChainOrder) { f.orders[key] = o }

// MineEvent records a successful transaction that emitted event for key on escrowAddr; data is
// the event's non-indexed payload.
func (f *FakeEscrowChain) MineEvent(txHash, escrowAddr, event string, key [32]byte, data []byte) {
	f.receipts[strings.ToLower(txHash)] = &types.Receipt{Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{{
		Address: common.HexToAddress(escrowAddr),
		Topics:  []common.Hash{parsedOrderEscrowABI.Events[event].ID, common.Hash(key)},
		Data:    data,
	}}}
}

// MineLog records a successful transaction carrying l.
func (f *FakeEscrowChain) MineLog(txHash string, l *types.Log) {
	f.receipts[strings.ToLower(txHash)] = &types.Receipt{Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{l}}
}

// MineFailed records a transaction that mined and reverted.
func (f *FakeEscrowChain) MineFailed(txHash string) {
	f.receipts[strings.ToLower(txHash)] = &types.Receipt{Status: types.ReceiptStatusFailed}
}

// OrderResolvedData is the payload of an OrderResolved log.
func OrderResolvedData(toBuyer, toSeller, fee *big.Int) []byte {
	return mustPackEvent("OrderResolved", toBuyer, toSeller, fee)
}

// OrderDeadlineData is the payload of an OrderDelivered (confirmBy) or OrderDisputed (resolveBy) log.
func OrderDeadlineData(event string, at time.Time) []byte {
	return mustPackEvent(event, big.NewInt(at.Unix()))
}

// OrderRefundedData is the payload of an OrderRefunded log.
func OrderRefundedData(amount *big.Int, reason uint8) []byte {
	return mustPackEvent("OrderRefunded", amount, reason)
}

// OrderReleasedData is the payload of an OrderReleased log.
func OrderReleasedData(sellerAmount, fee *big.Int, feeRecipient common.Address) []byte {
	return mustPackEvent("OrderReleased", sellerAmount, fee, feeRecipient)
}

// OrderCreatedLog is the OrderCreated log createOrder emits on escrowAddr.
func OrderCreatedLog(escrowAddr string, orderID [32]byte, buyer, seller, token common.Address,
	product, shipping, fee, flat *big.Int, deliverBy time.Time) *types.Log {
	key := OrderKey(buyer.Hex(), orderID)
	return &types.Log{Address: common.HexToAddress(escrowAddr),
		Topics: []common.Hash{parsedOrderEscrowABI.Events["OrderCreated"].ID, key, orderID, common.BytesToHash(buyer.Bytes())},
		Data:   mustPackEvent("OrderCreated", seller, token, product, shipping, fee, flat, big.NewInt(deliverBy.Unix()))}
}

func mustPackEvent(event string, args ...any) []byte {
	data, err := parsedOrderEscrowABI.Events[event].Inputs.NonIndexed().Pack(args...)
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

func (f *FakeEscrowChain) TransactionReceipt(_ context.Context, h common.Hash) (*types.Receipt, error) {
	return f.GetTransactionReceipt(h.Hex())
}

func (f *FakeEscrowChain) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	sel := call.Data[:4]
	fc := parsedFeeConfigABI.Methods
	switch {
	case bytes.Equal(sel, fc["rawRate"].ID):
		flat := f.Rate.FlatFee
		if flat == nil {
			flat = new(big.Int)
		}
		return fc["rawRate"].Outputs.Pack(feeConfigRateTuple{Bps: f.Rate.BPS, FlatFee: flat, Active: f.Rate.Active})
	case bytes.Equal(sel, fc["limitsFor"].ID):
		if f.Limits == nil {
			return nil, fmt.Errorf("execution reverted: LimitsNotConfigured")
		}
		return fc["limitsFor"].Outputs.Pack(feeConfigLimitsTuple{MaxPerOrder: f.Limits.MaxPerOrder, MaxOwed: f.Limits.MaxOwed})
	}
	m := parsedOrderEscrowABI.Methods["getOrder"]
	if !bytes.Equal(sel, m.ID) {
		return nil, fmt.Errorf("FakeEscrowChain: unexpected call %x", sel)
	}
	args, err := m.Inputs.Unpack(call.Data[4:])
	if err != nil {
		return nil, err
	}
	o := f.orders[args[0].([32]byte)] // zero value: no order, as the contract answers
	n := func(x *big.Int) *big.Int {
		if x == nil {
			return new(big.Int)
		}
		return x
	}
	t := func(at time.Time) *big.Int {
		if at.IsZero() {
			return new(big.Int)
		}
		return big.NewInt(at.Unix())
	}
	return m.Outputs.Pack(orderTuple{Buyer: o.Buyer, CreatedAt: t(o.CreatedAt), DeliverBy: t(o.DeliverBy), State: uint8(o.State),
		Seller: o.Seller, ConfirmBy: t(o.ConfirmBy), ResolveBy: t(o.ResolveBy), Token: o.Token, MerchantFee: n(o.MerchantFee),
		ProductAmount: n(o.ProductAmount), ShippingAmount: n(o.ShippingAmount)})
}
