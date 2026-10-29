package services

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func escrowEventLog(t *testing.T, name string, orderID [32]byte, data []byte) *types.Log {
	t.Helper()
	return &types.Log{
		Address: common.HexToAddress(testEscrow),
		Topics:  []common.Hash{parsedOrderEscrowABI.Events[name].ID, common.Hash(orderID)},
		Data:    data,
	}
}

// A buyer must not complete order A with order B's confirm, or with any other
// successful transaction.
func TestFindOrderEvent_BindsOrderContractAndEvent(t *testing.T) {
	var mine, other [32]byte
	mine[0], other[0] = 1, 2
	escrow := common.HexToAddress(testEscrow)

	logs := []*types.Log{escrowEventLog(t, "OrderReleased", other, nil)}
	if _, err := findOrderEvent(logs, escrow, "OrderReleased", mine); !errors.Is(err, ErrOrderEventNotFound) {
		t.Fatalf("another order's confirm was accepted: %v", err)
	}

	// The right order, the wrong event.
	logs = []*types.Log{escrowEventLog(t, "OrderRefunded", mine, nil)}
	if _, err := findOrderEvent(logs, escrow, "OrderReleased", mine); !errors.Is(err, ErrOrderEventNotFound) {
		t.Fatalf("a cancel was accepted as a confirm: %v", err)
	}

	// The right event from another contract.
	l := escrowEventLog(t, "OrderReleased", mine, nil)
	l.Address = common.HexToAddress("0x0000000000000000000000000000000000000bad")
	if _, err := findOrderEvent([]*types.Log{l}, escrow, "OrderReleased", mine); !errors.Is(err, ErrOrderEventNotFound) {
		t.Fatalf("another contract's confirm was accepted: %v", err)
	}

	logs = []*types.Log{escrowEventLog(t, "OrderReleased", other, nil), escrowEventLog(t, "OrderReleased", mine, nil)}
	if _, err := findOrderEvent(logs, escrow, "OrderReleased", mine); err != nil {
		t.Fatalf("this order's confirm was refused: %v", err)
	}
}

// The chain, not the admin's request, says how a dispute was split.
func TestDecodeOrderResolved_ReadsTheChain(t *testing.T) {
	var key [32]byte
	key[0] = 1
	full := escrowEventLog(t, "OrderResolved", key, OrderResolvedData(big.NewInt(80000), big.NewInt(0), big.NewInt(0)))
	r, err := DecodeOrderResolved(full)
	if err != nil || !r.RefundedToBuyer() || r.ToBuyer.Int64() != 80000 {
		t.Fatalf("full refund decoded %+v, %v", r, err)
	}
	split := escrowEventLog(t, "OrderResolved", key, OrderResolvedData(big.NewInt(30000), big.NewInt(49000), big.NewInt(1000)))
	if r, err := DecodeOrderResolved(split); err != nil || r.RefundedToBuyer() || r.ToSeller.Int64() != 49000 {
		t.Fatalf("split decoded %+v, %v", r, err)
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
			ProductAmount: big.NewInt(1000), ShippingAmount: big.NewInt(50), State: OrderStateFunded,
			CreatedAt: time.Unix(1_800_000_000, 0), DeliverBy: time.Unix(1_800_000_000, 0).Add(7 * 24 * time.Hour),
		}
	}
	if !OrderMatchesEscrow(order, seller, buyer, testUSDC, 7, onChain()) {
		t.Fatal("the order as priced was refused")
	}
	cases := map[string]func(*OnChainOrder){
		// The buyer swapped the seller for a wallet of their own.
		"another seller": func(o *OnChainOrder) { o.Seller = common.HexToAddress(mine) },
		"another token":  func(o *OnChainOrder) { o.Token = common.HexToAddress(mine) },
		"cheaper":        func(o *OnChainOrder) { o.ProductAmount = big.NewInt(1) },
		"no shipping":    func(o *OnChainOrder) { o.ShippingAmount = big.NewInt(0) },
		"already paid":   func(o *OnChainOrder) { o.State = OrderStateReleased },
		// The fixed bug: the funding wallet is compared, not read back from the chain.
		"another buyer": func(o *OnChainOrder) { o.Buyer = common.HexToAddress(mine) },
		// The buyer signs createOrder: a 1-day window would let them claim a refund on day 2.
		"a shorter delivery window": func(o *OnChainOrder) { o.DeliverBy = o.CreatedAt.Add(24 * time.Hour) },
	}
	for name, mutate := range cases {
		o := onChain()
		mutate(o)
		if OrderMatchesEscrow(order, seller, buyer, testUSDC, 7, o) {
			t.Errorf("%s: accepted", name)
		}
	}
	if OrderMatchesEscrow(order, "", buyer, testUSDC, 7, onChain()) || OrderMatchesEscrow(order, seller, buyer, "", 7, onChain()) ||
		OrderMatchesEscrow(order, seller, buyer, testUSDC, 7, nil) {
		t.Error("an unknown seller, token or escrow record was accepted")
	}
}

// A delivery proof must be this order's OrderDelivered, not its refund.
func TestFindOrderEvent_OrderDelivered(t *testing.T) {
	var mine [32]byte
	mine[0] = 1
	escrow := common.HexToAddress(testEscrow)
	if _, err := findOrderEvent([]*types.Log{escrowEventLog(t, "OrderRefunded", mine, nil)}, escrow, "OrderDelivered", mine); !errors.Is(err, ErrOrderEventNotFound) {
		t.Fatalf("a cancel was accepted as a delivery: %v", err)
	}
	if _, err := findOrderEvent([]*types.Log{escrowEventLog(t, "OrderDelivered", mine, nil)}, escrow, "OrderDelivered", mine); err != nil {
		t.Fatalf("this order's delivery was refused: %v", err)
	}
}
