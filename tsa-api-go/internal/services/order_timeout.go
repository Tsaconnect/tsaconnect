package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// order_timeout.go — the deadline sweep for OrderEscrow orders (contracts v2 slice 7). Every
// non-terminal escrow state ends without an admin: past deliverBy the buyer is refunded, past
// confirmBy the seller is paid, past resolveBy a dispute is refunded — but only once someone calls
// claimTimeout. This sweep is that someone (the release key; claimTimeout is permissionless), and it
// records orders the escrow already paid out whose submit never reached the API.

// OrderSettler records an order the escrow has paid out (handlers.CheckoutHandler).
type OrderSettler interface {
	SettleOrderFromChain(ctx context.Context, order *models.Order, state OrderState) (bool, error)
}

// OrderTimeoutClaimer sweeps open escrow orders whose deadline has passed.
type OrderTimeoutClaimer struct {
	db      *gorm.DB
	escrow  *EscrowService
	settler OrderSettler
	// signer sends claimTimeout; nil leaves the sweep recording only (someone else must claim).
	signer  ChainSender
	chainID int64
	now     func() time.Time
}

// orderTimeoutBatch bounds one sweep (each order is one chain read, a due one a send).
const orderTimeoutBatch = 100

// OrderOpenStatuses are the statuses an escrowed order holds until the escrow pays it out (the
// statuses an on-chain payout can still move).
var OrderOpenStatuses = []string{
	models.OrderStatusEscrowed, models.OrderStatusShipped, models.OrderStatusDelivered,
	models.OrderStatusRefundRequested, models.OrderStatusCancelRequested,
}

func NewOrderTimeoutClaimer(db *gorm.DB, escrow *EscrowService, settler OrderSettler, signer ChainSender, chainID int64) *OrderTimeoutClaimer {
	return &OrderTimeoutClaimer{db: db, escrow: escrow, settler: settler, signer: signer, chainID: chainID, now: time.Now}
}

// Sweep reads every open escrow order with a passed deadline: one the escrow already paid out is
// recorded; one still held is settled with claimTimeout. Errors on one order do not stop the rest;
// the last is returned so a sweep that keeps failing is alerted.
func (c *OrderTimeoutClaimer) Sweep(ctx context.Context) (claimed, settled int, err error) {
	now := c.now()
	// Due in its current phase: the latest deadline the order has recorded has passed (a recorded
	// confirm_by supersedes deliver_by, a resolve_by both). Least recently swept first, and every
	// order read is touched, so one that is not settled yet never starves the ones behind it.
	var orders []models.Order
	if err := c.db.WithContext(ctx).
		Where("escrow_order_key <> '' AND status IN ?", OrderOpenStatuses).
		Where(`(resolve_by IS NOT NULL AND resolve_by < ?) OR
			(resolve_by IS NULL AND confirm_by IS NOT NULL AND confirm_by < ?) OR
			(resolve_by IS NULL AND confirm_by IS NULL AND deliver_by < ?)`, now, now, now).
		Order("updated_at").Limit(orderTimeoutBatch).Find(&orders).Error; err != nil {
		return 0, 0, err
	}
	var lastErr error
	for i := range orders {
		if ctx.Err() != nil {
			return claimed, settled, ctx.Err()
		}
		o := &orders[i]
		c.db.WithContext(ctx).Model(&models.Order{}).Where("id = ?", o.ID).UpdateColumn("updated_at", now)
		escrowAddr := firstNonEmpty(o.EscrowContractAddress, c.escrow.CurrentEscrowAddress())
		oc, err := c.escrow.ReadOrder(ctx, escrowAddr, OrderKeyOf(o))
		if err != nil {
			lastErr = fmt.Errorf("read order %s: %w", o.ID, err)
			continue
		}
		if oc == nil {
			log.Printf("[order-timeout] ALERT order_escrow_missing=1 order=%s key=%s: recorded escrowed but the escrow holds nothing", o.ID, o.EscrowOrderKey)
			continue
		}
		if oc.State.Settled() {
			c.dropUnprovenClaimHash(ctx, o, escrowAddr, oc.State)
			moved, err := c.settler.SettleOrderFromChain(ctx, o, oc.State)
			if err != nil {
				lastErr = fmt.Errorf("record order %s: %w", o.ID, err)
			} else if moved {
				settled++
			}
			continue
		}
		c.syncDeadlines(ctx, o, oc)
		if !timeoutDue(oc, now) || c.signer == nil {
			continue
		}
		key := OrderKeyOf(o)
		// One operation per order and phase: a retry answers the same allocation, never a new nonce.
		sendCtx := WithOperationRef(ctx, fmt.Sprintf("order_timeout:%s:%d", o.ID, oc.State))
		hash, err := c.signer.Send(sendCtx, c.chainID, func() ([]byte, error) {
			return c.escrow.PrepareClaimTimeout(key, c.signer.Address(), escrowAddr)
		})
		if err != nil && !errors.Is(err, ErrPendingBroadcast) {
			lastErr = fmt.Errorf("claimTimeout %s: %s", o.ID, RedactAlertText(err.Error()))
			continue
		}
		// The claim is the order's payout transaction: a payout order's bank leg proves the release
		// from it once the sweep records the order completed.
		if hash != "" {
			c.db.WithContext(ctx).Model(&models.Order{}).Where("id = ? AND COALESCE(release_tx_hash, '') = ''", o.ID).
				UpdateColumn("release_tx_hash", hash)
		}
		claimed++
		log.Printf("[order-timeout] order=%s state=%d claimTimeout sent tx=%s", o.ID, oc.State, hash)
	}
	return claimed, settled, lastErr
}

// settledEvent is the OrderEscrow event a settled state was reached by.
var settledEvent = map[OrderState]string{
	OrderStateReleased: "OrderReleased", OrderStateRefunded: "OrderRefunded", OrderStateResolved: "OrderResolved",
}

// dropUnprovenClaimHash clears the release_tx_hash the sweep recorded when its claimTimeout was
// sent, unless that tx is the one that settled the order: a claim that lost to another settlement
// (a resolve, the seller's cancel, another claimant) mined and reverted, and must not stand as the
// order's payout proof — a payout order's bank leg reads it. A receipt that cannot be read yet
// keeps the hash for the next tick.
func (c *OrderTimeoutClaimer) dropUnprovenClaimHash(ctx context.Context, o *models.Order, escrowAddr string, state OrderState) {
	if o.ReleaseTxHash == "" {
		return
	}
	_, err := c.escrow.VerifyOrderEvent(o.ReleaseTxHash, escrowAddr, settledEvent[state], OrderKeyOf(o))
	if err == nil || !errors.Is(err, ErrOrderEventNotFound) {
		return
	}
	res := c.db.WithContext(ctx).Model(&models.Order{}).Where("id = ? AND release_tx_hash = ?", o.ID, o.ReleaseTxHash).
		UpdateColumn("release_tx_hash", "")
	if res.Error == nil && res.RowsAffected == 1 {
		log.Printf("[order-timeout] order=%s: claim %s did not settle it (%v); hash cleared", o.ID, o.ReleaseTxHash, err)
		o.ReleaseTxHash = ""
	}
}

// timeoutDue reports whether claimTimeout settles oc now.
func timeoutDue(oc *OnChainOrder, now time.Time) bool {
	switch oc.State {
	case OrderStateFunded:
		return now.After(oc.DeliverBy)
	case OrderStateDelivered:
		return now.After(oc.ConfirmBy)
	case OrderStateDisputed:
		return now.After(oc.ResolveBy)
	}
	return false
}

// syncDeadlines copies the escrow's confirm and resolve deadlines onto the order when they started
// on-chain without the API recording them.
func (c *OrderTimeoutClaimer) syncDeadlines(ctx context.Context, o *models.Order, oc *OnChainOrder) {
	updates := map[string]interface{}{}
	if !oc.ConfirmBy.IsZero() && (o.ConfirmBy == nil || !o.ConfirmBy.Equal(oc.ConfirmBy)) {
		updates["confirm_by"] = oc.ConfirmBy
	}
	if !oc.ResolveBy.IsZero() && (o.ResolveBy == nil || !o.ResolveBy.Equal(oc.ResolveBy)) {
		updates["resolve_by"] = oc.ResolveBy
	}
	if len(updates) == 0 {
		return
	}
	if err := c.db.WithContext(ctx).Model(&models.Order{}).Where("id = ?", o.ID).Updates(updates).Error; err != nil {
		log.Printf("[order-timeout] sync deadlines for order %s: %v", o.ID, err)
	}
}

// LegacyEscrowOrders lists open orders escrowed in ProductEscrowV2 (no OrderEscrow key, paid
// on-chain). The hard switch speaks only OrderEscrow, so these have no in-app exit: they must be
// settled on V2 before the release, and boot alerts while any remain.
func LegacyEscrowOrders(db *gorm.DB) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := db.Model(&models.Order{}).
		Where("COALESCE(escrow_order_key, '') = '' AND COALESCE(contract_order_id, '') <> '' AND COALESCE(payment_source, '') = '' AND status IN ?", OrderOpenStatuses).
		Pluck("id", &ids).Error
	return ids, err
}
