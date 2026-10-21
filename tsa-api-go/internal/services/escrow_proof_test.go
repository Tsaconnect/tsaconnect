package services

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func escrowEventLog(t *testing.T, name string, orderID [32]byte, data []byte) *types.Log {
	t.Helper()
	return &types.Log{
		Address: common.HexToAddress(testEscrow),
		Topics:  []common.Hash{parsedEscrowABI.Events[name].ID, common.Hash(orderID)},
		Data:    data,
	}
}

// A buyer must not complete order A with order B's confirm, or with any other
// successful transaction.
func TestFindOrderEvent_BindsOrderContractAndEvent(t *testing.T) {
	var mine, other [32]byte
	mine[0], other[0] = 1, 2
	escrow := common.HexToAddress(testEscrow)

	logs := []*types.Log{escrowEventLog(t, "ReceiptConfirmed", other, nil)}
	if _, err := findOrderEvent(logs, escrow, "ReceiptConfirmed", mine); !errors.Is(err, ErrOrderEventNotFound) {
		t.Fatalf("another order's confirm was accepted: %v", err)
	}

	// The right order, the wrong event.
	logs = []*types.Log{escrowEventLog(t, "OrderCancelled", mine, nil)}
	if _, err := findOrderEvent(logs, escrow, "ReceiptConfirmed", mine); !errors.Is(err, ErrOrderEventNotFound) {
		t.Fatalf("a cancel was accepted as a confirm: %v", err)
	}

	// The right event from another contract.
	l := escrowEventLog(t, "ReceiptConfirmed", mine, nil)
	l.Address = common.HexToAddress("0x0000000000000000000000000000000000000bad")
	if _, err := findOrderEvent([]*types.Log{l}, escrow, "ReceiptConfirmed", mine); !errors.Is(err, ErrOrderEventNotFound) {
		t.Fatalf("another contract's confirm was accepted: %v", err)
	}

	logs = []*types.Log{escrowEventLog(t, "ReceiptConfirmed", other, nil), escrowEventLog(t, "ReceiptConfirmed", mine, nil)}
	if _, err := findOrderEvent(logs, escrow, "ReceiptConfirmed", mine); err != nil {
		t.Fatalf("this order's confirm was refused: %v", err)
	}
}

// The chain, not the admin's request, says which way a dispute went.
func TestDisputeResolvedRefund_ReadsTheChain(t *testing.T) {
	var id [32]byte
	id[0] = 1
	for _, want := range []bool{true, false} {
		data, err := parsedEscrowABI.Events["DisputeResolved"].Inputs.NonIndexed().Pack(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DisputeResolvedRefund(escrowEventLog(t, "DisputeResolved", id, data))
		if err != nil || got != want {
			t.Fatalf("refunded=%v: got %v, %v", want, got, err)
		}
	}
	if _, err := DisputeResolvedRefund(escrowEventLog(t, "DisputeResolved", id, nil)); err == nil {
		t.Fatal("an empty DisputeResolved was decoded")
	}
}

func TestOrderMatchesEscrow(t *testing.T) {
	const (
		buyer  = "0x00000000000000000000000000000000000000b1"
		seller = "0x00000000000000000000000000000000000000a1"
		mine   = "0x00000000000000000000000000000000000000c1"
	)
	order := &models.Order{ProductAmount: "1000", ShippingAmount: "50"}
	onChain := func() *OnChainOrder {
		return &OnChainOrder{
			Buyer: common.HexToAddress(buyer), Seller: common.HexToAddress(seller), Token: common.HexToAddress(testUSDC),
			ProductAmount: big.NewInt(1000), ShippingAmount: big.NewInt(50),
		}
	}
	if !OrderMatchesEscrow(order, seller, buyer, testUSDC, onChain()) {
		t.Fatal("the order as priced was refused")
	}
	cases := map[string]func(*OnChainOrder){
		// The buyer swapped the seller for a wallet of their own.
		"another seller": func(o *OnChainOrder) { o.Seller = common.HexToAddress(mine) },
		"another token":  func(o *OnChainOrder) { o.Token = common.HexToAddress(mine) },
		"cheaper":        func(o *OnChainOrder) { o.ProductAmount = big.NewInt(1) },
		"no shipping":    func(o *OnChainOrder) { o.ShippingAmount = big.NewInt(0) },
		"already paid":   func(o *OnChainOrder) { o.Resolved = true },
	}
	for name, mutate := range cases {
		o := onChain()
		mutate(o)
		if OrderMatchesEscrow(order, seller, buyer, testUSDC, o) {
			t.Errorf("%s: accepted", name)
		}
	}
	if OrderMatchesEscrow(order, "", buyer, testUSDC, onChain()) || OrderMatchesEscrow(order, seller, buyer, "", onChain()) ||
		OrderMatchesEscrow(order, seller, buyer, testUSDC, nil) {
		t.Error("an unknown seller, token or escrow record was accepted")
	}
}

// A delivery proof must be this order's DeliveryMarked, not its cancel.
func TestFindOrderEvent_DeliveryMarked(t *testing.T) {
	var mine [32]byte
	mine[0] = 1
	escrow := common.HexToAddress(testEscrow)
	if _, err := findOrderEvent([]*types.Log{escrowEventLog(t, "OrderCancelled", mine, nil)}, escrow, "DeliveryMarked", mine); !errors.Is(err, ErrOrderEventNotFound) {
		t.Fatalf("a cancel was accepted as a delivery: %v", err)
	}
	if _, err := findOrderEvent([]*types.Log{escrowEventLog(t, "DeliveryMarked", mine, nil)}, escrow, "DeliveryMarked", mine); err != nil {
		t.Fatalf("this order's delivery was refused: %v", err)
	}
}
