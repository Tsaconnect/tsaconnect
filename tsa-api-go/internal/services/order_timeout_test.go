package services

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// timeoutSender records the claimTimeout calls the sweep sends.
type timeoutSender struct{ sent []string }

func (s *timeoutSender) Address() string { return "0x00000000000000000000000000000000000000Ee" }
func (s *timeoutSender) SignAndBroadcast(context.Context, []byte) (string, error) {
	panic("the sweep sends through the lane")
}
func (s *timeoutSender) Send(ctx context.Context, chainID int64, build func() ([]byte, error)) (string, error) {
	if _, err := build(); err != nil {
		return "", err
	}
	s.sent = append(s.sent, operationRef(ctx))
	return "0xclaim", nil
}

// timeoutSettler records the orders the sweep hands back as paid out.
type timeoutSettler struct{ settled map[uuid.UUID]OrderState }

func (s *timeoutSettler) SettleOrderFromChain(_ context.Context, o *models.Order, st OrderState) (bool, error) {
	s.settled[o.ID] = st
	return true, nil
}

// writeEscrowedOrder stores an escrowed order with an OrderEscrow key and the given deadline.
func writeEscrowedOrder(t *testing.T, db *gorm.DB, status string, deliverBy time.Time) (uuid.UUID, [32]byte) {
	t.Helper()
	id := writeOrder(t, db, writeProduct(t, db, 0), models.OrderKindProduct, status, 48*time.Hour, 48*time.Hour)
	key := testKey(id)
	if err := db.Exec(`UPDATE orders SET escrow_order_key = ?, deliver_by = ? WHERE id = ?`, common.Hash(key).Hex(), deliverBy, id).Error; err != nil {
		t.Fatal(err)
	}
	return id, key
}

// Past each deadline the sweep sends claimTimeout (once per order and phase); an order the escrow
// already paid out is recorded instead; one still inside its window is left alone.
func TestOrderTimeoutClaimer_Sweep(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	chain := NewFakeEscrowChain()
	es := &EscrowService{escrowAddress: testEscrow, caller: chain, cfg: &config.Config{}}
	sender := &timeoutSender{}
	settler := &timeoutSettler{settled: map[uuid.UUID]OrderState{}}
	now := time.Now()

	overdue, overdueKey := writeEscrowedOrder(t, db, models.OrderStatusEscrowed, now.Add(-time.Hour))
	chain.SetOrder(overdueKey, OnChainOrder{Buyer: common.HexToAddress(testBuyerWallet), State: OrderStateFunded,
		ProductAmount: big.NewInt(1), ShippingAmount: big.NewInt(0), DeliverBy: now.Add(-time.Hour), CreatedAt: now.Add(-48 * time.Hour)})

	// Delivered on-chain without the API hearing: its confirm window is still open.
	delivered, deliveredKey := writeEscrowedOrder(t, db, models.OrderStatusEscrowed, now.Add(-time.Hour))
	confirmBy := now.Add(5 * 24 * time.Hour).Truncate(time.Second)
	chain.SetOrder(deliveredKey, OnChainOrder{Buyer: common.HexToAddress(testBuyerWallet), State: OrderStateDelivered,
		ProductAmount: big.NewInt(1), ShippingAmount: big.NewInt(0), DeliverBy: now.Add(-time.Hour), ConfirmBy: confirmBy, CreatedAt: now.Add(-48 * time.Hour)})

	paidOut, paidOutKey := writeEscrowedOrder(t, db, models.OrderStatusDelivered, now.Add(-time.Hour))
	chain.SetOrder(paidOutKey, OnChainOrder{Buyer: common.HexToAddress(testBuyerWallet), State: OrderStateRefunded,
		ProductAmount: big.NewInt(1), ShippingAmount: big.NewInt(0), CreatedAt: now.Add(-48 * time.Hour)})

	writeEscrowedOrder(t, db, models.OrderStatusEscrowed, now.Add(24*time.Hour)) // inside its window: left alone

	c := NewOrderTimeoutClaimer(db, es, settler, sender, 146)
	claimed, settled, err := c.Sweep(context.Background())
	if err != nil || claimed != 1 || settled != 1 {
		t.Fatalf("claimed=%d settled=%d err=%v; want one claim, one recorded", claimed, settled, err)
	}
	if len(sender.sent) != 1 || sender.sent[0] != "order_timeout:"+overdue.String()+":1" {
		t.Fatalf("claims = %v, want the overdue order's", sender.sent)
	}
	if settler.settled[paidOut] != OrderStateRefunded || len(settler.settled) != 1 {
		t.Fatalf("settled = %v", settler.settled)
	}
	var row struct{ ConfirmBy *time.Time }
	db.Raw(`SELECT confirm_by FROM orders WHERE id = ?`, delivered).Scan(&row)
	if row.ConfirmBy == nil || !row.ConfirmBy.Equal(confirmBy) {
		t.Fatalf("confirm_by = %v, want the chain's %v", row.ConfirmBy, confirmBy)
	}

	// Without a release key the sweep still records, but claims nothing.
	sender.sent = nil
	if claimed, _, err := NewOrderTimeoutClaimer(db, es, settler, nil, 146).Sweep(context.Background()); err != nil || claimed != 0 {
		t.Fatalf("no signer: claimed=%d err=%v", claimed, err)
	}
}
