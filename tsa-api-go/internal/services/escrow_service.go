package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// escrow_service.go — marketplace orders on the core set's OrderEscrow (contracts v2, slices 6–7;
// smart-contracts/contracts/core/OrderEscrow.sol). Owner 2026-10-29: a hard switch, so every order
// escrowed from this release on lives on OrderEscrow and ProductEscrowV2 is no longer spoken to.
//
// What changed from ProductEscrowV2, and why the backend cares:
//   - Orders are keyed by orderKey(buyer, orderId), not the bare id, so every call after
//     createOrder takes the key (orders.escrow_order_key) and the buyer is fixed by who funded it.
//   - The merchant fee is charged to the SELLER at release (bps of the product price, snapshotted
//     at createOrder); the buyer pays product + shipping + FeeConfig's flat fee, nothing else.
//   - Deadlines live on-chain: deliverBy (handlingDays), confirmBy (7 days after delivery),
//     resolveBy (14 days after a dispute). Anyone may claimTimeout past one.
//   - Fees, limits and the fee recipient come from FeeConfig, never the escrow.

const orderEscrowABIJSON = `[
	{"type":"function","name":"createOrder","stateMutability":"nonpayable","inputs":[
		{"name":"orderId","type":"bytes32"},{"name":"seller","type":"address"},{"name":"token","type":"address"},
		{"name":"productAmount","type":"uint128"},{"name":"shippingAmount","type":"uint128"},
		{"name":"handlingDays","type":"uint8"},{"name":"maxFlatFee","type":"uint256"}],
	 "outputs":[{"name":"key","type":"bytes32"}]},
	{"type":"function","name":"markDelivered","stateMutability":"nonpayable","inputs":[{"name":"key","type":"bytes32"}],"outputs":[]},
	{"type":"function","name":"confirmReceipt","stateMutability":"nonpayable","inputs":[{"name":"key","type":"bytes32"}],"outputs":[]},
	{"type":"function","name":"cancel","stateMutability":"nonpayable","inputs":[{"name":"key","type":"bytes32"}],"outputs":[]},
	{"type":"function","name":"dispute","stateMutability":"nonpayable","inputs":[{"name":"key","type":"bytes32"}],"outputs":[]},
	{"type":"function","name":"claimTimeout","stateMutability":"nonpayable","inputs":[{"name":"key","type":"bytes32"}],"outputs":[]},
	{"type":"function","name":"resolve","stateMutability":"nonpayable","inputs":[{"name":"key","type":"bytes32"},{"name":"toBuyer","type":"uint128"}],"outputs":[]},
	{"type":"function","name":"hasRole","stateMutability":"view","inputs":[{"name":"role","type":"bytes32"},{"name":"account","type":"address"}],"outputs":[{"name":"","type":"bool"}]},
	{"type":"function","name":"getOrder","stateMutability":"view","inputs":[{"name":"key","type":"bytes32"}],"outputs":[{"name":"","type":"tuple","components":[
		{"name":"buyer","type":"address"},{"name":"createdAt","type":"uint40"},{"name":"deliverBy","type":"uint40"},{"name":"state","type":"uint8"},
		{"name":"seller","type":"address"},{"name":"confirmBy","type":"uint40"},{"name":"resolveBy","type":"uint40"},
		{"name":"token","type":"address"},{"name":"merchantFee","type":"uint96"},
		{"name":"productAmount","type":"uint128"},{"name":"shippingAmount","type":"uint128"}]}]},
	{"type":"event","name":"OrderCreated","anonymous":false,"inputs":[
		{"indexed":true,"name":"key","type":"bytes32"},{"indexed":true,"name":"orderId","type":"bytes32"},{"indexed":true,"name":"buyer","type":"address"},
		{"indexed":false,"name":"seller","type":"address"},{"indexed":false,"name":"token","type":"address"},
		{"indexed":false,"name":"productAmount","type":"uint128"},{"indexed":false,"name":"shippingAmount","type":"uint128"},
		{"indexed":false,"name":"merchantFee","type":"uint96"},{"indexed":false,"name":"flatFee","type":"uint128"},
		{"indexed":false,"name":"deliverBy","type":"uint40"}]},
	{"type":"event","name":"OrderDelivered","anonymous":false,"inputs":[{"indexed":true,"name":"key","type":"bytes32"},{"indexed":false,"name":"confirmBy","type":"uint40"}]},
	{"type":"event","name":"OrderDisputed","anonymous":false,"inputs":[{"indexed":true,"name":"key","type":"bytes32"},{"indexed":false,"name":"resolveBy","type":"uint40"}]},
	{"type":"event","name":"OrderReleased","anonymous":false,"inputs":[
		{"indexed":true,"name":"key","type":"bytes32"},{"indexed":true,"name":"seller","type":"address"},
		{"indexed":false,"name":"sellerAmount","type":"uint256"},{"indexed":false,"name":"fee","type":"uint256"},{"indexed":false,"name":"feeRecipient","type":"address"}]},
	{"type":"event","name":"OrderRefunded","anonymous":false,"inputs":[
		{"indexed":true,"name":"key","type":"bytes32"},{"indexed":true,"name":"buyer","type":"address"},
		{"indexed":false,"name":"amount","type":"uint256"},{"indexed":false,"name":"reason","type":"uint8"}]},
	{"type":"event","name":"OrderResolved","anonymous":false,"inputs":[
		{"indexed":true,"name":"key","type":"bytes32"},{"indexed":true,"name":"resolver","type":"address"},
		{"indexed":false,"name":"toBuyer","type":"uint256"},{"indexed":false,"name":"toSeller","type":"uint256"},{"indexed":false,"name":"fee","type":"uint256"}]}
]`

var parsedOrderEscrowABI = func() abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(orderEscrowABIJSON))
	if err != nil {
		panic(fmt.Sprintf("failed to parse OrderEscrow ABI: %v", err))
	}
	return parsed
}()

// OrderEscrow's own limits the backend must respect before asking a buyer to sign.
const (
	MaxOrderFeeBPS       = 300 // MAX_FEE_BPS: createOrder reverts above it
	MinOrderHandlingDays = 1
	MaxOrderHandlingDays = 30
	// DefaultOrderHandlingDays is a listing's delivery window when its handling_days is 0.
	DefaultOrderHandlingDays = 7
	// orderConfirmWindow mirrors CONFIRM_WINDOW: how long a buyer has to confirm or dispute once
	// the seller marks delivery.
	orderConfirmWindow = 7 * 24 * time.Hour
)

// OrderState is OrderEscrow.State.
type OrderState uint8

const (
	OrderStateNone OrderState = iota
	OrderStateFunded
	OrderStateDelivered
	OrderStateDisputed
	OrderStateReleased
	OrderStateRefunded
	OrderStateResolved
)

// Settled reports whether the escrow has paid the order out (it holds nothing for it).
func (s OrderState) Settled() bool {
	return s == OrderStateReleased || s == OrderStateRefunded || s == OrderStateResolved
}

// OrderRefundReason is OrderEscrow.RefundReason.
const (
	OrderRefundCancelled       = 0
	OrderRefundNotDelivered    = 1
	OrderRefundDisputeBackstop = 2
)

// resolverRole is OrderEscrow.RESOLVER_ROLE.
var resolverRole = crypto.Keccak256Hash([]byte("RESOLVER_ROLE"))

// ErrEscrowFeeAboveMax means FeeConfig holds an order rate the escrow refuses, so every
// createOrder would revert. Pricing must stop rather than quote it.
var ErrEscrowFeeAboveMax = fmt.Errorf("order fee is above the escrow maximum of %d bps", MaxOrderFeeBPS)

// ErrOrderFeeNotConfigured means FeeConfig has no active order row for the token: createOrder
// would revert (FeeNotConfigured), so the token cannot be checked out in.
var ErrOrderFeeNotConfigured = errors.New("orders are not priced for this token")

// ErrOrderEscrowUnavailable means no ready core set names an OrderEscrow on the order chain.
var ErrOrderEscrowUnavailable = errors.New("order escrow not available")

// EscrowService builds and proves OrderEscrow calls for marketplace orders.
type EscrowService struct {
	client        *blockchain.EVMClient
	escrowAddress string
	feeConfig     string
	chainID       int64
	cfg           *config.Config
	// ready reports whether the core set on the order chain has passed its gates; nil = always.
	ready func() bool
	// fees reads FeeConfig's order rate and limits; nil without an RPC client.
	fees *FeeConfigReader
	// rates caches FeeConfig reads per token → rateEntry.
	rates sync.Map
	// caller serves the read-only views; nil without an RPC client.
	caller contractCaller
	// receipts proves a submitted transaction; nil without an RPC client.
	receipts escrowReceiptReader
}

// escrowReceiptReader is the receipt lookup VerifyOrderEvent needs.
type escrowReceiptReader interface {
	GetTransactionReceipt(txHash string) (*types.Receipt, error)
}

// contractCaller is the eth_call surface the views need.
type contractCaller interface {
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

// feeConfigClient is what the FeeConfig reader needs from a chain backend.
type feeConfigClient interface {
	contractCaller
	TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)
}

// orderEscrowSet finds the core set row for the order chain.
func orderEscrowSet(cfg *config.Config, chainID int64) (config.CoreContracts, bool) {
	set, ok := cfg.CoreContracts[chainID]
	return set, ok && set.OrderEscrow != ""
}

// OrderEscrowAddress is the core set's OrderEscrow on Sonic (the order chain), or "" when the
// selected set has none: the one address every order money path (approve spender, treasury call
// allowlist, payouts) names.
func OrderEscrowAddress(cfg *config.Config) string {
	found := ""
	for _, set := range cfg.CoreContracts {
		if set.ChainKey == "sonic" && set.OrderEscrow != "" {
			if found != "" && !strings.EqualFold(found, set.OrderEscrow) {
				return "" // two Sonic sets: no one answer, so none
			}
			found = set.OrderEscrow
		}
	}
	return found
}

// NewEscrowService builds the service over the Sonic client. With no core set naming an
// OrderEscrow on that chain every escrow call answers ErrOrderEscrowUnavailable.
func NewEscrowService(client *blockchain.EVMClient, cfg *config.Config) *EscrowService {
	es := &EscrowService{client: client, cfg: cfg}
	for chainID, set := range cfg.CoreContracts {
		if set.ChainKey == "sonic" && set.OrderEscrow != "" {
			es.chainID, es.escrowAddress, es.feeConfig = chainID, set.OrderEscrow, set.FeeConfig
		}
	}
	if client != nil {
		if id := client.ChainID().Int64(); es.escrowAddress != "" && id != es.chainID {
			// The set's chain is checked against the RPC at boot (the core gates); a client on
			// another chain must never be pointed at it.
			log.Printf("[escrow] ALERT order_escrow_chain_mismatch=1 client chain %d, the core set's order chain %d", id, es.chainID)
			es.escrowAddress, es.feeConfig = "", ""
		}
		es.caller, es.receipts = client.Client, client
		es.fees = NewFeeConfigReader(map[int64]FeeManagerRPCClient{client.ChainID().Int64(): client.Client})
	}
	return es
}

// NewEscrowServiceWithBackend builds an EscrowService over the given chain readers instead of an
// RPC client, so callers can be tested with a fake chain.
func NewEscrowServiceWithBackend(cfg *config.Config, chainID int64, backend feeConfigClient, receipts escrowReceiptReader) *EscrowService {
	es := &EscrowService{cfg: cfg, chainID: chainID, caller: backend, receipts: receipts,
		fees: NewFeeConfigReader(map[int64]FeeManagerRPCClient{chainID: backend})}
	if set, ok := orderEscrowSet(cfg, chainID); ok {
		es.escrowAddress, es.feeConfig = set.OrderEscrow, set.FeeConfig
	}
	return es
}

// SetReadiness makes the service refuse new orders while the core set has not passed its gates.
func (s *EscrowService) SetReadiness(ready func() bool) { s.ready = ready }

// Available reports whether new orders can be escrowed now.
func (s *EscrowService) Available() bool {
	return s != nil && s.escrowAddress != "" && (s.ready == nil || s.ready())
}

func (s *EscrowService) requireAvailable() error {
	if !s.Available() {
		return ErrOrderEscrowUnavailable
	}
	return nil
}

// GenerateOrderID creates a deterministic bytes32 from a UUID using keccak256.
func GenerateOrderID(dbOrderID uuid.UUID) [32]byte {
	return crypto.Keccak256Hash(dbOrderID[:])
}

// OrderKey is OrderEscrow.orderKey(buyer, orderId) = keccak256(abi.encode(buyer, orderId)).
func OrderKey(buyer string, orderID [32]byte) [32]byte {
	return crypto.Keccak256Hash(common.LeftPadBytes(common.HexToAddress(buyer).Bytes(), 32), orderID[:])
}

// OrderKeyOf is the key recorded on an escrowed order (orders.escrow_order_key), or the zero key.
func OrderKeyOf(o *models.Order) [32]byte {
	return common.HexToHash(o.EscrowOrderKey)
}

// OrderHandlingDays is the delivery window order must get on the escrow: its booking's slot end
// plus the grace, else its listing's handling days, as of at (the order's escrow time — the
// prepare for a new order, the escrow's createdAt when checking one).
func OrderHandlingDays(db *gorm.DB, order *models.Order, at time.Time) (uint8, error) {
	var product models.Product
	if err := db.Select("id", "handling_days").First(&product, "id = ?", order.ProductID).Error; err != nil {
		return 0, err
	}
	var slotEnd *time.Time
	var booking models.ServiceBooking
	err := db.Select("slot_end").Where("order_id = ?", order.ID).First(&booking).Error
	switch {
	case err == nil && !booking.SlotEnd.IsZero():
		slotEnd = &booking.SlotEnd
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		return 0, err
	}
	return HandlingDaysFor(product.HandlingDays, slotEnd, at), nil
}

// BookingDeliveryGrace is how long after a booking's slot ends its order may still be delivered.
const BookingDeliveryGrace = 2 * 24 * time.Hour

// HandlingDaysFor is the delivery window createOrder gets: the listing's handling days, or for a
// booking the days until its slot end plus the booking delivery grace, clamped to the contract's
// 1–30. now is the order's escrow time.
func HandlingDaysFor(handlingDays int, slotEnd *time.Time, now time.Time) uint8 {
	days := handlingDays
	if days <= 0 {
		days = DefaultOrderHandlingDays
	}
	if slotEnd != nil {
		until := slotEnd.Add(BookingDeliveryGrace).Sub(now)
		days = int((until + 24*time.Hour - 1) / (24 * time.Hour))
	}
	return uint8(min(max(days, MinOrderHandlingDays), MaxOrderHandlingDays))
}

// CurrentEscrowAddress is the OrderEscrow new orders go to (empty when none is configured).
func (s *EscrowService) CurrentEscrowAddress() string { return s.escrowAddress }

// FeeConfigAddress is the FeeConfig the order escrow reads.
func (s *EscrowService) FeeConfigAddress() string { return s.feeConfig }

// PrepareCreateOrder encodes createOrder for the buyer to sign. Must be called after the buyer's
// approve is mined, so gas estimation works. maxFlatFee is the flat fee the buyer was quoted: a fee
// raised in between makes the tx revert rather than charge more.
func (s *EscrowService) PrepareCreateOrder(orderID [32]byte, buyer, seller, token string, productAmount, shippingAmount *big.Int, handlingDays uint8, maxFlatFee *big.Int) ([]byte, error) {
	if err := s.requireAvailable(); err != nil {
		return nil, err
	}
	data, err := parsedOrderEscrowABI.Pack("createOrder", orderID, common.HexToAddress(seller), common.HexToAddress(token),
		productAmount, shippingAmount, handlingDays, maxFlatFee)
	if err != nil {
		return nil, fmt.Errorf("failed to pack createOrder: %w", err)
	}
	return s.buildUnsignedTx(buyer, s.escrowAddress, data)
}

// prepareKeyCall encodes a key-only call against the order's escrow.
func (s *EscrowService) prepareKeyCall(method string, key [32]byte, from, escrowAddr string) ([]byte, error) {
	data, err := parsedOrderEscrowABI.Pack(method, key)
	if err != nil {
		return nil, fmt.Errorf("failed to pack %s: %w", method, err)
	}
	return s.buildUnsignedTx(from, escrowAddr, data)
}

// PackKeyCall is the calldata of a key-only OrderEscrow call (for senders that sign server-side).
func PackKeyCall(method string, key [32]byte) ([]byte, error) {
	return parsedOrderEscrowABI.Pack(method, key)
}

// PrepareConfirmReceipt: the buyer releases the order to the seller (Funded or Delivered).
func (s *EscrowService) PrepareConfirmReceipt(key [32]byte, buyer, escrowAddr string) ([]byte, error) {
	return s.prepareKeyCall("confirmReceipt", key, buyer, escrowAddr)
}

// PrepareMarkDelivered: the seller marks delivery before deliverBy; the buyer then has 7 days.
func (s *EscrowService) PrepareMarkDelivered(key [32]byte, seller, escrowAddr string) ([]byte, error) {
	return s.prepareKeyCall("markDelivered", key, seller, escrowAddr)
}

// PrepareCancelOrder: the seller cancels before delivery; the buyer gets product + shipping back.
func (s *EscrowService) PrepareCancelOrder(key [32]byte, seller, escrowAddr string) ([]byte, error) {
	return s.prepareKeyCall("cancel", key, seller, escrowAddr)
}

// PrepareDispute: the buyer disputes a delivered order inside its confirm window.
func (s *EscrowService) PrepareDispute(key [32]byte, buyer, escrowAddr string) ([]byte, error) {
	return s.prepareKeyCall("dispute", key, buyer, escrowAddr)
}

// PrepareClaimTimeout: anyone settles an order whose deadline has passed (refund, release or
// backstop refund, by state).
func (s *EscrowService) PrepareClaimTimeout(key [32]byte, from, escrowAddr string) ([]byte, error) {
	return s.prepareKeyCall("claimTimeout", key, from, escrowAddr)
}

// PrepareResolve: a RESOLVER_ROLE holder splits a disputed order; toBuyer comes out of shipping
// first, then the product price.
func (s *EscrowService) PrepareResolve(key [32]byte, toBuyer *big.Int, resolver, escrowAddr string) ([]byte, error) {
	data, err := parsedOrderEscrowABI.Pack("resolve", key, toBuyer)
	if err != nil {
		return nil, fmt.Errorf("failed to pack resolve: %w", err)
	}
	return s.buildUnsignedTx(resolver, escrowAddr, data)
}

// IsResolver reads hasRole(RESOLVER_ROLE, account) on the order's escrow.
func (s *EscrowService) IsResolver(ctx context.Context, account, escrowAddr string) (bool, error) {
	vals, err := callABIView(ctx, parsedOrderEscrowABI, s.caller, escrowAddr, "hasRole", [32]byte(resolverRole), common.HexToAddress(account))
	if err != nil {
		return false, err
	}
	ok, isBool := vals[0].(bool)
	if !isBool {
		return false, fmt.Errorf("hasRole returned unexpected type %T", vals[0])
	}
	return ok, nil
}

// EscrowCreated is the OrderCreated event for one order, as the escrow recorded it.
type EscrowCreated struct {
	Key, OrderID         [32]byte
	Buyer, Seller, Token common.Address
	ProductAmount        *big.Int
	ShippingAmount       *big.Int
	MerchantFee, FlatFee *big.Int
	DeliverBy            time.Time
}

// VerifyEscrowCreated checks that txHash succeeded and emitted OrderCreated on the configured
// OrderEscrow for expectedOrderID, and returns what the escrow recorded. Matching the id matters:
// without it, any successful escrow tx — another, cheaper order's — would mark this order paid.
func (s *EscrowService) VerifyEscrowCreated(txHash string, expectedOrderID [32]byte) (*EscrowCreated, error) {
	if s.receipts == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	if s.escrowAddress == "" {
		return nil, ErrOrderEscrowUnavailable
	}
	receipt, err := s.receipts.GetTransactionReceipt(txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipt: %w", err)
	}
	if receipt.Status != 1 {
		return nil, fmt.Errorf("transaction failed")
	}
	return findOrderCreated(receipt.Logs, common.HexToAddress(s.escrowAddress), expectedOrderID)
}

// ErrOrderEventNotFound: the transaction is mined but is not the escrow action claimed for the
// order. Any other VerifyOrderEvent error is an unreadable chain, which a caller may retry.
var ErrOrderEventNotFound = errors.New("the transaction does not do this to the order")

// ErrReceiptPending means the node has no receipt for the transaction yet: retry, do not refuse.
var ErrReceiptPending = errors.New("the transaction has no receipt yet")

// VerifyOrderEvent checks that txHash succeeded and emitted event (an OrderEscrow event whose first
// indexed argument is the order key) on escrowAddr for key, and returns that log. A mined
// transaction proves nothing about an order by itself.
func (s *EscrowService) VerifyOrderEvent(txHash, escrowAddr, event string, key [32]byte) (*types.Log, error) {
	if s.receipts == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	receipt, err := s.receipts.GetTransactionReceipt(txHash)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return nil, ErrReceiptPending
		}
		return nil, fmt.Errorf("failed to get receipt: %w", err)
	}
	if receipt.Status != 1 {
		return nil, fmt.Errorf("%w: transaction failed", ErrOrderEventNotFound)
	}
	return findOrderEvent(receipt.Logs, common.HexToAddress(escrowAddr), event, key)
}

// findOrderEvent returns the log of event emitted by escrowAddr for key.
func findOrderEvent(logs []*types.Log, escrowAddr common.Address, event string, key [32]byte) (*types.Log, error) {
	ev, ok := parsedOrderEscrowABI.Events[event]
	if !ok {
		return nil, fmt.Errorf("unknown escrow event %q", event)
	}
	for _, l := range logs {
		if l.Address == escrowAddr && len(l.Topics) >= 2 && l.Topics[0] == ev.ID && l.Topics[1] == common.Hash(key) {
			return l, nil
		}
	}
	return nil, fmt.Errorf("%w: no %s for this order in the receipt", ErrOrderEventNotFound, event)
}

// OrderReleasedTo decodes an OrderReleased log: who the escrow paid as the seller and the
// sellerAmount (net of the merchant fee). The chain's own record of a release is the only thing a
// payout is funded from.
func OrderReleasedTo(l *types.Log) (seller common.Address, amount *big.Int, err error) {
	if len(l.Topics) < 3 {
		return common.Address{}, nil, fmt.Errorf("decode OrderReleased: %d topics", len(l.Topics))
	}
	vals, err := parsedOrderEscrowABI.Events["OrderReleased"].Inputs.NonIndexed().Unpack(l.Data)
	if err != nil || len(vals) != 3 {
		return common.Address{}, nil, fmt.Errorf("decode OrderReleased: %v", err)
	}
	amt, ok := vals[0].(*big.Int)
	if !ok {
		return common.Address{}, nil, fmt.Errorf("decode OrderReleased: unexpected value type")
	}
	return common.BytesToAddress(l.Topics[2].Bytes()), amt, nil
}

// OrderResolution is an OrderResolved log: how a resolver split a disputed order.
type OrderResolution struct {
	ToBuyer, ToSeller, Fee *big.Int
}

// RefundedToBuyer reports whether the buyer got everything back.
func (r OrderResolution) RefundedToBuyer() bool { return r.ToSeller.Sign() == 0 && r.Fee.Sign() == 0 }

// DecodeOrderResolved reads an OrderResolved log. The chain's answer is the only one: an admin's
// request body is not.
func DecodeOrderResolved(l *types.Log) (OrderResolution, error) {
	vals, err := parsedOrderEscrowABI.Events["OrderResolved"].Inputs.NonIndexed().Unpack(l.Data)
	if err != nil || len(vals) != 3 {
		return OrderResolution{}, fmt.Errorf("decode OrderResolved: %v", err)
	}
	toBuyer, ok1 := vals[0].(*big.Int)
	toSeller, ok2 := vals[1].(*big.Int)
	fee, ok3 := vals[2].(*big.Int)
	if !ok1 || !ok2 || !ok3 {
		return OrderResolution{}, fmt.Errorf("decode OrderResolved: unexpected value types")
	}
	return OrderResolution{ToBuyer: toBuyer, ToSeller: toSeller, Fee: fee}, nil
}

// DecodeOrderDeadline reads the new deadline an OrderDelivered (confirmBy) or OrderDisputed
// (resolveBy) log carries.
func DecodeOrderDeadline(event string, l *types.Log) (time.Time, error) {
	vals, err := parsedOrderEscrowABI.Events[event].Inputs.NonIndexed().Unpack(l.Data)
	if err != nil || len(vals) != 1 {
		return time.Time{}, fmt.Errorf("decode %s: %v", event, err)
	}
	at, ok := vals[0].(*big.Int)
	if !ok {
		return time.Time{}, fmt.Errorf("decode %s: unexpected value type", event)
	}
	return time.Unix(at.Int64(), 0).UTC(), nil
}

// OrderMatchesEscrow reports whether the escrow's record of an order is this order, funded as it
// was priced: still held, this buyer paying this seller, in the order's token, the product and
// shipping amounts it was priced at (checked apart: the buyer chooses the split, and the merchant
// fee applies to the product alone), and a delivery window no shorter than the order's
// handlingDays — the buyer signs createOrder, and a short window would make the seller miss a
// deadline the buyer then claims a refund on.
func OrderMatchesEscrow(o *models.Order, sellerWallet, buyerWallet, tokenAddr string, handlingDays uint8, onChain *OnChainOrder) bool {
	product, okP := new(big.Int).SetString(o.ProductAmount, 10)
	shipping, okS := new(big.Int).SetString(o.ShippingAmount, 10)
	return !(!okP || !okS || tokenAddr == "" || sellerWallet == "" || buyerWallet == "" || onChain == nil || onChain.State.Settled() ||
		onChain.DeliverBy.Sub(onChain.CreatedAt) < time.Duration(handlingDays)*24*time.Hour ||
		onChain.Buyer != common.HexToAddress(buyerWallet) ||
		onChain.Seller != common.HexToAddress(sellerWallet) ||
		onChain.Token != common.HexToAddress(tokenAddr) ||
		onChain.ProductAmount.Cmp(product) != 0 || onChain.ShippingAmount.Cmp(shipping) != 0)
}

// OnChainOrder is an order as OrderEscrow.getOrder(key) records it.
type OnChainOrder struct {
	Buyer, Seller, Token          common.Address
	ProductAmount, ShippingAmount *big.Int
	MerchantFee                   *big.Int
	State                         OrderState
	CreatedAt, DeliverBy          time.Time
	ConfirmBy, ResolveBy          time.Time // zero until the phase starts
}

// Resolved reports whether the escrow has paid the order out.
func (o *OnChainOrder) Resolved() bool { return o.State.Settled() }

// SellerDelivered reports whether the seller's markDelivered is on-chain.
func (o *OnChainOrder) SellerDelivered() bool {
	return o.State == OrderStateDelivered || o.State == OrderStateDisputed
}

// EscrowedOrder reads the configured escrow's record of key, or nil when it holds none. Only a
// successful read is an answer — an RPC failure is an error, never "not escrowed", because callers
// release stock on nil.
func (s *EscrowService) EscrowedOrder(ctx context.Context, key [32]byte) (*OnChainOrder, error) {
	if s.escrowAddress == "" {
		return nil, ErrOrderEscrowUnavailable
	}
	return s.ReadOrder(ctx, s.escrowAddress, key)
}

// orderTuple is getOrder's Order struct as the ABI decodes it.
type orderTuple struct {
	Buyer          common.Address
	CreatedAt      *big.Int
	DeliverBy      *big.Int
	State          uint8
	Seller         common.Address
	ConfirmBy      *big.Int
	ResolveBy      *big.Int
	Token          common.Address
	MerchantFee    *big.Int
	ProductAmount  *big.Int
	ShippingAmount *big.Int
}

// ReadOrder is EscrowedOrder against escrowAddr — an order's own escrow.
func (s *EscrowService) ReadOrder(ctx context.Context, escrowAddr string, key [32]byte) (*OnChainOrder, error) {
	vals, err := callABIView(ctx, parsedOrderEscrowABI, s.caller, escrowAddr, "getOrder", key)
	if err != nil {
		return nil, err
	}
	out, err := decodeOrderTuple(vals[0])
	if err != nil {
		return nil, err
	}
	if OrderState(out.State) == OrderStateNone {
		return nil, nil
	}
	at := func(x *big.Int) time.Time {
		if x == nil || x.Sign() == 0 {
			return time.Time{}
		}
		return time.Unix(x.Int64(), 0).UTC()
	}
	return &OnChainOrder{Buyer: out.Buyer, Seller: out.Seller, Token: out.Token,
		ProductAmount: out.ProductAmount, ShippingAmount: out.ShippingAmount, MerchantFee: out.MerchantFee,
		State: OrderState(out.State), CreatedAt: at(out.CreatedAt), DeliverBy: at(out.DeliverBy),
		ConfirmBy: at(out.ConfirmBy), ResolveBy: at(out.ResolveBy)}, nil
}

// decodeOrderTuple converts getOrder's decoded tuple; abi.ConvertType panics on a shape mismatch.
func decodeOrderTuple(v any) (out *orderTuple, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("decode getOrder: %v", r)
		}
	}()
	return abi.ConvertType(v, new(orderTuple)).(*orderTuple), nil
}

func findOrderCreated(logs []*types.Log, escrowAddr common.Address, expectedOrderID [32]byte) (*EscrowCreated, error) {
	event := parsedOrderEscrowABI.Events["OrderCreated"]
	foundEscrowLog := false
	for _, l := range logs {
		if l.Address != escrowAddr {
			continue
		}
		foundEscrowLog = true
		if len(l.Topics) < 4 || l.Topics[0] != event.ID || l.Topics[2] != common.Hash(expectedOrderID) {
			continue
		}
		vals, err := event.Inputs.NonIndexed().Unpack(l.Data)
		if err != nil || len(vals) != 7 {
			return nil, fmt.Errorf("decode OrderCreated: %v", err)
		}
		seller, _ := vals[0].(common.Address)
		token, _ := vals[1].(common.Address)
		product, _ := vals[2].(*big.Int)
		shipping, _ := vals[3].(*big.Int)
		fee, _ := vals[4].(*big.Int)
		flat, _ := vals[5].(*big.Int)
		deliverBy, _ := vals[6].(*big.Int)
		if product == nil || shipping == nil || fee == nil || flat == nil || deliverBy == nil {
			return nil, fmt.Errorf("decode OrderCreated: unexpected value types")
		}
		return &EscrowCreated{Key: l.Topics[1], OrderID: expectedOrderID,
			Buyer: common.BytesToAddress(l.Topics[3].Bytes()), Seller: seller, Token: token,
			ProductAmount: product, ShippingAmount: shipping, MerchantFee: fee, FlatFee: flat,
			DeliverBy: time.Unix(deliverBy.Int64(), 0).UTC()}, nil
	}
	if !foundEscrowLog {
		return nil, fmt.Errorf("no logs found for configured escrow contract %s in receipt", escrowAddr.Hex())
	}
	return nil, fmt.Errorf("OrderCreated for this order not found in receipt")
}

// buildUnsignedTx creates an UnsignedTx JSON targeting the supplied escrow contract, with real gas
// estimation when a chain client is available.
func (s *EscrowService) buildUnsignedTx(from, contractAddr string, callData []byte) ([]byte, error) {
	if contractAddr == "" {
		return nil, ErrOrderEscrowUnavailable
	}
	if s.client == nil {
		if s.cfg.IsProduction() {
			return nil, fmt.Errorf("blockchain client not available")
		}
		return json.Marshal(blockchain.UnsignedTx{To: contractAddr, Value: "0", Data: common.Bytes2Hex(callData)})
	}
	fromAddr := common.HexToAddress(from)
	escrowAddr := common.HexToAddress(contractAddr)
	nonce, err := s.client.GetNonce(from)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}
	gasPrice, err := s.client.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get gas price: %w", err)
	}
	gasLimit, err := s.client.EstimateGas(ethereum.CallMsg{From: fromAddr, To: &escrowAddr, Data: callData})
	if err != nil {
		return nil, fmt.Errorf("gas estimation failed (transaction would likely revert): %w", withRevertReason(err))
	}
	return json.Marshal(blockchain.UnsignedTx{Nonce: nonce, GasPrice: gasPrice.String(), GasLimit: gasLimit,
		To: contractAddr, Value: "0", Data: common.Bytes2Hex(callData), ChainID: s.client.ChainID().String()})
}

// ── Pricing: FeeConfig's order row and limits ─────────────────────────────────────────────────

const (
	// escrowRateTTL bounds how long a read rate is trusted: an admin fee change reaches checkout
	// within this window.
	escrowRateTTL = time.Minute
	// escrowRateMaxStale bounds how long a previous read may stand in for a failed one.
	escrowRateMaxStale = 10 * time.Minute
)

// orderRate is FeeConfig's order row and limits for one token.
type orderRate struct {
	rate   FeeConfigRate
	limits *FeeConfigLimits // nil: the token has no limits, so createOrder reverts
}

type rateEntry struct {
	rate   orderRate
	readAt time.Time
}

// PlatformFeeBPS is the merchant fee OrderEscrow will snapshot at createOrder for tokenAddr
// (charged to the seller at release): FeeConfig's active order row, refused above MAX_FEE_BPS.
func (s *EscrowService) PlatformFeeBPS(ctx context.Context, tokenAddr string) (int, error) {
	r, err := s.orderRateFor(ctx, tokenAddr)
	if err != nil {
		return 0, err
	}
	if int(r.rate.BPS) > MaxOrderFeeBPS {
		return 0, ErrEscrowFeeAboveMax
	}
	return int(r.rate.BPS), nil
}

// GasFeeWei is the flat fee createOrder charges the buyer for tokenAddr, in base units.
func (s *EscrowService) GasFeeWei(ctx context.Context, tokenAddr string) (*big.Int, error) {
	r, err := s.orderRateFor(ctx, tokenAddr)
	if err != nil {
		return nil, err
	}
	if r.rate.FlatFee == nil {
		return new(big.Int), nil
	}
	return new(big.Int).Set(r.rate.FlatFee), nil
}

// MaxOrderAmount is FeeConfig's maxPerOrder for tokenAddr: createOrder reverts when product +
// shipping is above it, so checkout refuses first.
func (s *EscrowService) MaxOrderAmount(ctx context.Context, tokenAddr string) (*big.Int, error) {
	r, err := s.orderRateFor(ctx, tokenAddr)
	if err != nil {
		return nil, err
	}
	if r.limits == nil {
		return nil, ErrOrderFeeNotConfigured
	}
	return new(big.Int).Set(r.limits.MaxPerOrder), nil
}

// orderRateFor returns the cached FeeConfig order row for tokenAddr, re-reading it past
// escrowRateTTL. When a re-read fails, a previous read younger than escrowRateMaxStale stands in
// (logged), so an RPC blip does not change prices mid-checkout.
func (s *EscrowService) orderRateFor(ctx context.Context, tokenAddr string) (orderRate, error) {
	if err := s.requireAvailable(); err != nil {
		return orderRate{}, err
	}
	key := strings.ToLower(tokenAddr)
	cached, hasCached := s.rates.Load(key)
	if hasCached && time.Since(cached.(rateEntry).readAt) < escrowRateTTL {
		return cached.(rateEntry).rate, nil
	}
	r, err := s.readOrderRate(ctx, tokenAddr)
	if err != nil {
		if hasCached && !errors.Is(err, ErrOrderFeeNotConfigured) && time.Since(cached.(rateEntry).readAt) < escrowRateMaxStale {
			log.Printf("[escrow] order rate read for %s failed (%v); using the read from %s", tokenAddr, err, cached.(rateEntry).readAt.Format(time.RFC3339))
			return cached.(rateEntry).rate, nil
		}
		return orderRate{}, err
	}
	s.rates.Store(key, rateEntry{rate: r, readAt: time.Now()})
	return r, nil
}

func (s *EscrowService) readOrderRate(ctx context.Context, tokenAddr string) (orderRate, error) {
	if !common.IsHexAddress(tokenAddr) {
		return orderRate{}, fmt.Errorf("token address %q not configured", tokenAddr)
	}
	token := common.HexToAddress(tokenAddr)
	rate, err := s.fees.RawRate(ctx, s.chainID, s.feeConfig, FeeConfigFeatureOrder, token)
	if err != nil {
		return orderRate{}, err
	}
	if !rate.Active {
		return orderRate{}, ErrOrderFeeNotConfigured
	}
	out := orderRate{rate: rate}
	limits, err := s.fees.LimitsFor(ctx, s.chainID, s.feeConfig, token)
	switch {
	case err == nil:
		out.limits = &limits
	case errors.Is(err, ErrFeeConfigReverted):
		return orderRate{}, ErrOrderFeeNotConfigured // no limits: createOrder would revert
	default:
		return orderRate{}, err
	}
	return out, nil
}
