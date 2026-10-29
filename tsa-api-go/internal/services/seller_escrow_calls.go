package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// SellerEscrowCalls makes the escrow calls only an order's on-chain seller may
// make — markDelivered and cancel — for payout orders, whose seller is
// the treasury. Each is one operation per order (a retry gets the same
// allocation and hash, never a second nonce), sent only to the allowlisted
// escrow, and refused while the treasury kill switch is on.
type SellerEscrowCalls struct {
	treasury *Treasury
	escrow   *EscrowService
}

func NewSellerEscrowCalls(t *Treasury, e *EscrowService) *SellerEscrowCalls {
	return &SellerEscrowCalls{treasury: t, escrow: e}
}

// MarkDelivered sends markDelivered for the order (its escrow key) to escrowAddr and returns the
// transaction hash. An error that still carries a hash (a broadcast whose
// outcome is unknown) means the call may land: the caller waits and asks again.
func (c *SellerEscrowCalls) MarkDelivered(ctx context.Context, orderID uuid.UUID, key [32]byte, escrowAddr string) (string, error) {
	return c.call(ctx, orderID, key, escrowAddr, "markDelivered", c.escrow.PrepareMarkDelivered)
}

// CancelOrder sends cancel for the order, refunding the buyer.
func (c *SellerEscrowCalls) CancelOrder(ctx context.Context, orderID uuid.UUID, key [32]byte, escrowAddr string) (string, error) {
	return c.call(ctx, orderID, key, escrowAddr, "cancel", c.escrow.PrepareCancelOrder)
}

func (c *SellerEscrowCalls) call(ctx context.Context, orderID uuid.UUID, key [32]byte, escrowAddr, fn string,
	prepare func(key [32]byte, seller, escrowAddr string) ([]byte, error)) (string, error) {
	if c == nil || c.treasury == nil || c.escrow == nil {
		return "", ErrSponsorNotAvailable
	}
	lane := c.treasury.Lane("sonic")
	if lane == nil {
		return "", ErrSponsorNotAvailable
	}
	ctx = WithOperationRef(ctx, fmt.Sprintf("seller_escrow_call:%s:%s", orderID, fn))
	hash, attempted, err := c.treasury.SendPrepared(ctx, lane, escrowAddr, func() ([]byte, error) {
		return prepare(key, c.treasury.Address(), escrowAddr)
	}, nil)
	if err != nil {
		if attempted && hash != "" {
			return hash, fmt.Errorf("%w: %v", ErrSellerCallUncertain, err)
		}
		return "", err
	}
	return hash, nil
}

// ErrSellerCallUncertain: the call was broadcast and may have landed; ask again
// (the same operation answers the same hash) once it has had time to mine.
var ErrSellerCallUncertain = errors.New("the escrow call was sent and its outcome is not known yet")
