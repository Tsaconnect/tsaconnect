package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// UnpaidOrderTTL is how long a marketplace order may sit without payment
// activity before it is cancelled and its reserved stock released. It runs
// from the order's updated_at, which prepare-approve, prepare-escrow and
// submit-escrow refresh (TouchUnpaidOrder): an order whose buyer is paying
// right now — including a retry long after the order was placed — is never
// old enough.
const UnpaidOrderTTL = time.Hour

// EscrowWindow is how long a buyer's payment stays in escrow before it can be
// reclaimed, as recorded on the order.
const EscrowWindow = 30 * 24 * time.Hour

// unpaidOrderExpiryFrom bounds the sweep to orders placed since it shipped.
// Older abandoned orders took stock that merchants may have corrected by hand
// since, and some may have been prepared against an earlier escrow contract,
// which the escrow read here does not cover.
var unpaidOrderExpiryFrom = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)

// unpaidOrderBatch is one page of the sweep (a var so tests can page).
var unpaidOrderBatch = 200

// ErrOrderNotPayable means the order is no longer awaiting payment.
var ErrOrderNotPayable = errors.New("order is not awaiting payment")

// escrowOrderReader reads the escrow's record of an order. *EscrowService
// fills it.
type escrowOrderReader interface {
	EscrowedOrder(ctx context.Context, key [32]byte) (*OnChainOrder, error)
	CurrentEscrowAddress() string
}

// tokenAddresser resolves a pay token's address. *BlockchainService fills it.
type tokenAddresser interface {
	TokenAddress(chain, symbol string) string
}

// TouchUnpaidOrder records payment activity on an order still awaiting
// payment, restarting its UnpaidOrderTTL.
func TouchUnpaidOrder(db *gorm.DB, id uuid.UUID) error {
	return db.Model(&models.Order{}).
		Where("id = ? AND status = ?", id, models.OrderStatusPendingPayment).
		Update("updated_at", time.Now()).Error
}

// TouchUnpaidGroup is TouchUnpaidOrder for every order of one buyer's checkout
// group still awaiting payment, in one UPDATE (a funding swap in flight pays
// the whole group).
func TouchUnpaidGroup(db *gorm.DB, groupID, buyerID uuid.UUID) error {
	return db.Model(&models.Order{}).
		Where("checkout_group_id = ? AND buyer_id = ? AND status = ?", groupID, buyerID, models.OrderStatusPendingPayment).
		Update("updated_at", time.Now()).Error
}

// RestoreOrderStock returns an order's units to its listing, inside tx.
// Services hold no inventory, so there is nothing to restore.
func RestoreOrderStock(tx *gorm.DB, order *models.Order) error {
	if order.Kind == models.OrderKindService {
		return nil
	}
	if err := tx.Model(&models.Product{}).
		Where("id = ?", order.ProductID).
		UpdateColumn("stock", gorm.Expr("stock + ?", order.Quantity)).Error; err != nil {
		return fmt.Errorf("restore stock: %w", err)
	}
	return nil
}

// CancelUnpaidOrder cancels an order that is still awaiting payment and
// returns its reserved stock, inside tx. It reports false, changing nothing,
// when the order has left pending_payment — a payment that landed first wins.
func CancelUnpaidOrder(tx *gorm.DB, order *models.Order) (bool, error) {
	return cancelPending(tx, order, nil)
}

// cancelPending is CancelUnpaidOrder, optionally also requiring no payment
// activity since idleBefore — checked in the same UPDATE, so a buyer who
// starts paying after the sweep picked the order still keeps it.
func cancelPending(tx *gorm.DB, order *models.Order, idleBefore *time.Time) (bool, error) {
	// A Spendable MCGP debit in flight may still land: cancelling under it
	// would take a payment for an order that no longer exists. The payment
	// reconciler resolves it first (a failed debit leaves the order
	// cancellable; one that lands after a cancel is returned).
	q := tx.Model(&models.Order{}).
		Where("id = ? AND status = ?", order.ID, models.OrderStatusPendingPayment).
		Where("NOT EXISTS (SELECT 1 FROM order_mcgp_payments p WHERE p.order_id = orders.id AND p.status IN ?)",
			[]string{models.OrderMcgpDebiting, models.OrderMcgpHeld})
	if idleBefore != nil {
		q = q.Where("updated_at < ?", *idleBefore)
	}
	res := q.Updates(map[string]interface{}{
		"status":     models.OrderStatusCancelled,
		"updated_at": time.Now(),
	})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}
	if err := RestoreOrderStock(tx, order); err != nil {
		return false, err
	}
	return true, nil
}

// EscrowRecord is what a verified OrderEscrow holds for an order.
type EscrowRecord struct {
	OrderID, Key [32]byte
	Buyer        common.Address
	// Product and shipping are what the buyer escrowed; MerchantFee is what the seller pays at
	// release. FlatFee is what createOrder charged the buyer on top (nil when read back from
	// getOrder, which does not store it: the order then keeps the flat fee it was priced at).
	ProductAmount, ShippingAmount, MerchantFee, FlatFee *big.Int
	DeliverBy                                           time.Time
	EscrowAddress                                       string
}

// EscrowRecordFromCreated is the record an OrderCreated event proves.
func EscrowRecordFromCreated(c *EscrowCreated, escrowAddress string) EscrowRecord {
	return EscrowRecord{OrderID: c.OrderID, Key: c.Key, Buyer: c.Buyer, ProductAmount: c.ProductAmount,
		ShippingAmount: c.ShippingAmount, MerchantFee: c.MerchantFee, FlatFee: c.FlatFee,
		DeliverBy: c.DeliverBy, EscrowAddress: escrowAddress}
}

// EscrowedOrderUpdates is the order record of a verified escrow: the amounts the escrow actually
// holds (fee settlement and every later screen read these, not the ones we asked for), its key
// and buyer, where it holds them, and its delivery deadline (escrow_expires_at, which the screens
// read, is that deadline: past it the buyer's refund is claimable).
func EscrowedOrderUpdates(r EscrowRecord) map[string]interface{} {
	updates := map[string]interface{}{
		"status":                  models.OrderStatusEscrowed,
		"contract_order_id":       fmt.Sprintf("0x%x", r.OrderID),
		"escrow_order_key":        common.Hash(r.Key).Hex(),
		"escrow_buyer_wallet":     r.Buyer.Hex(),
		"product_amount":          r.ProductAmount.String(),
		"shipping_amount":         r.ShippingAmount.String(),
		"platform_fee":            r.MerchantFee.String(),
		"merchant_fee":            r.MerchantFee.String(),
		"total_amount":            new(big.Int).Add(r.ProductAmount, r.ShippingAmount).String(),
		"escrow_contract_address": r.EscrowAddress,
		"deliver_by":              r.DeliverBy,
		"escrow_expires_at":       r.DeliverBy,
	}
	if r.FlatFee != nil {
		updates["flat_fee"] = r.FlatFee.String()
	}
	return updates
}

// MarkOrderEscrowed applies updates (EscrowedOrderUpdates) to an order still
// awaiting payment, inside tx, guarded on the status: the expiry or a cancel
// may have run while the payment was being verified. Any other state is
// ErrOrderNotPayable.
func MarkOrderEscrowed(tx *gorm.DB, id uuid.UUID, updates map[string]interface{}) error {
	res := tx.Model(&models.Order{}).
		Where("id = ? AND status = ?", id, models.OrderStatusPendingPayment).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrOrderNotPayable
	}
	return nil
}

// NotifyOrderEscrowed tells the buyer their payment is held and the seller to
// fulfil. bus may be nil.
func NotifyOrderEscrowed(bus *events.Bus, order *models.Order, expires time.Time) {
	if bus == nil {
		return
	}
	bus.Publish(events.Event{
		Type:    events.OrderEscrowed,
		UserID:  order.BuyerID,
		Title:   "Order Escrowed",
		Message: fmt.Sprintf("Your payment for order %s has been escrowed", order.ID),
		Data: map[string]interface{}{
			"orderId":         order.ID.String(),
			"status":          models.OrderStatusEscrowed,
			"escrowExpiresAt": expires,
		},
	})
	sellerTitle := "Payment Received — Ship Order"
	sellerMsg := fmt.Sprintf("Payment for order #%s has been escrowed. Please ship the item to the buyer.", order.ID.String()[:8])
	if order.Kind == models.OrderKindService {
		sellerTitle = "Payment Received — Provide Service"
		sellerMsg = fmt.Sprintf("Payment for service order #%s has been escrowed. Provide the service, then mark it rendered.", order.ID.String()[:8])
	}
	bus.Publish(events.Event{
		Type:    events.OrderEscrowed,
		UserID:  order.SellerID,
		Title:   sellerTitle,
		Message: sellerMsg,
		Data: map[string]interface{}{
			"orderId":         order.ID.String(),
			"status":          models.OrderStatusEscrowed,
			"escrowExpiresAt": expires,
			"role":            "seller",
		},
	})
}

// UnpaidOrderExpiryService settles product orders nobody finished paying for.
//
// Placing an order takes its stock before the buyer pays, and until now only
// an explicit cancel gave it back — so a payment that failed or was abandoned
// kept a 1-in-stock listing unbuyable for everyone else, indefinitely. And a
// buyer whose app closed between the escrow tx and reporting it left a paid
// order pending, the seller never told.
type UnpaidOrderExpiryService struct {
	db     *gorm.DB
	escrow escrowOrderReader
	tokens tokenAddresser
	bus    *events.Bus
	ttl    time.Duration
}

// NewUnpaidOrderExpiryService builds the sweep. bus may be nil (no notices).
func NewUnpaidOrderExpiryService(db *gorm.DB, escrow escrowOrderReader, tokens tokenAddresser, bus *events.Bus) *UnpaidOrderExpiryService {
	return &UnpaidOrderExpiryService{db: db, escrow: escrow, tokens: tokens, bus: bus, ttl: UnpaidOrderTTL}
}

// staleOrder is a pending order with the wallets its escrow must be between.
type staleOrder struct {
	models.Order
	SellerWallet string
	BuyerWallet  string
}

// ExpireUnpaidOrders visits every product order with no payment activity for
// the TTL and settles it against the escrow: one the escrow holds, as this
// order, is marked escrowed; one it does not hold is cancelled and its stock
// returned. It returns how many it cancelled. Service orders are left alone:
// they hold no stock, and one may back a converted booking request.
//
// An unreadable escrow skips the order until a later tick and is returned as
// an error, so a persistent RPC failure is visible.
func (s *UnpaidOrderExpiryService) ExpireUnpaidOrders(ctx context.Context) (int64, error) {
	cutoff := time.Now().Add(-s.ttl)
	var expired int64
	var readErr error
	after := uuid.Nil
	for {
		var page []staleOrder
		if err := s.db.WithContext(ctx).
			Table("orders").
			Select("orders.*, COALESCE(NULLIF(orders.escrow_seller_wallet, ''), sellers.wallet_address) AS seller_wallet, buyers.wallet_address AS buyer_wallet").
			Joins("LEFT JOIN users AS sellers ON sellers.id = orders.seller_id").
			Joins("LEFT JOIN users AS buyers ON buyers.id = orders.buyer_id").
			Where("orders.status = ? AND orders.kind <> ? AND orders.created_at >= ? AND orders.updated_at < ? AND orders.id > ?",
				models.OrderStatusPendingPayment, models.OrderKindService, unpaidOrderExpiryFrom, cutoff, after).
			Order("orders.id").
			Limit(unpaidOrderBatch).
			Scan(&page).Error; err != nil {
			return expired, err
		}
		for i := range page {
			if err := ctx.Err(); err != nil {
				return expired, err
			}
			cancelled, err := s.settle(ctx, &page[i], cutoff)
			if err != nil {
				readErr = err
				continue
			}
			if cancelled {
				expired++
			}
		}
		if len(page) < unpaidOrderBatch {
			return expired, readErr
		}
		after = page[len(page)-1].ID
	}
}

// settle reconciles one stale order with the escrow, reporting whether it was
// cancelled.
func (s *UnpaidOrderExpiryService) settle(ctx context.Context, o *staleOrder, cutoff time.Time) (bool, error) {
	// A Spendable MCGP order is never funded through the escrow, so there is
	// nothing there to reconcile — and an escrow record under its id is not a
	// payment we accept for it.
	if !o.IsSpendableMCGP() {
		// The key is the funding wallet's: the one recorded at prepare-escrow, else the buyer's own.
		onChain, err := s.escrow.EscrowedOrder(ctx, OrderKey(firstNonEmpty(o.EscrowBuyerWallet, o.BuyerWallet), GenerateOrderID(o.ID)))
		if err != nil {
			return false, fmt.Errorf("escrow read for order %s: %w", o.ID, err)
		}
		if onChain != nil {
			return false, s.reconcilePaid(ctx, o, onChain)
		}
	}

	var cancelled bool
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		cancelled, err = cancelPending(tx, &o.Order, &cutoff)
		return err
	}); err != nil {
		return false, fmt.Errorf("cancel order %s: %w", o.ID, err)
	}
	if cancelled {
		log.Printf("[unpaid-order-expiry] cancelled unpaid order %s (qty=%d)", o.ID, o.Quantity)
		s.notifyExpired(&o.Order)
	}
	return cancelled, nil
}

// reconcilePaid marks escrowed a pending order the escrow holds — paid, but
// never reported. The orderId is derivable from the order's UUID, so anyone
// could escrow under it: the record is trusted only when it is unresolved (the
// money is still held) and is this order's buyer paying its seller, in its
// token, the amounts it was priced at. Anything else is left pending and
// alerted.
func (s *UnpaidOrderExpiryService) reconcilePaid(ctx context.Context, o *staleOrder, onChain *OnChainOrder) error {
	tokenAddr := ""
	if s.tokens != nil {
		tokenAddr = s.tokens.TokenAddress("sonic", o.Token)
	}
	handlingDays, err := OrderHandlingDays(s.db.WithContext(ctx), &o.Order, onChain.CreatedAt)
	if err != nil {
		return fmt.Errorf("handling days for order %s: %w", o.ID, err)
	}
	if !OrderMatchesEscrow(&o.Order, o.SellerWallet, firstNonEmpty(o.EscrowBuyerWallet, o.BuyerWallet), tokenAddr, handlingDays, onChain) {
		log.Printf("[unpaid-order-expiry] ALERT unpaid_order_escrow_mismatch=1 order=%s: escrow holds an order under its id that does not match it", o.ID)
		return nil
	}

	expires := onChain.DeliverBy
	id := GenerateOrderID(o.ID)
	updates := EscrowedOrderUpdates(EscrowRecord{OrderID: id, Key: OrderKey(onChain.Buyer.Hex(), id), Buyer: onChain.Buyer,
		ProductAmount: onChain.ProductAmount, ShippingAmount: onChain.ShippingAmount, MerchantFee: onChain.MerchantFee,
		DeliverBy: onChain.DeliverBy, EscrowAddress: s.escrow.CurrentEscrowAddress()})
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return MarkOrderEscrowed(tx, o.ID, updates)
	})
	if errors.Is(err, ErrOrderNotPayable) {
		return nil // reported meanwhile
	}
	if err != nil {
		return fmt.Errorf("mark order %s escrowed: %w", o.ID, err)
	}
	log.Printf("[unpaid-order-expiry] reconciled paid order %s: escrowed on-chain, never reported", o.ID)
	NotifyOrderEscrowed(s.bus, &o.Order, expires)
	return nil
}

// notifyExpired tells both sides, as a buyer's own cancel does.
func (s *UnpaidOrderExpiryService) notifyExpired(order *models.Order) {
	if s.bus == nil {
		return
	}
	short := order.ID.String()[:8]
	data := map[string]interface{}{
		"orderId":     order.ID.String(),
		"status":      models.OrderStatusCancelled,
		"cancelledBy": "expiry",
	}
	s.bus.Publish(events.Event{
		Type:    events.OrderRefunded,
		UserID:  order.BuyerID,
		Title:   "Order Cancelled",
		Message: fmt.Sprintf("Order #%s was cancelled because it was not paid within an hour. Nothing was charged; place it again to buy.", short),
		Data:    data,
	})
	s.bus.Publish(events.Event{
		Type:    events.OrderRefunded,
		UserID:  order.SellerID,
		Title:   "Order Cancelled",
		Message: fmt.Sprintf("Order #%s was cancelled because the buyer did not pay within an hour. Its stock is back on sale.", short),
		Data:    data,
	})
}
