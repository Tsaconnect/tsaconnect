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
)

// ProductEscrow ABI — subset of functions and events we interact with
const productEscrowABI = `[
	{"inputs":[{"name":"orderId","type":"bytes32"},{"name":"seller","type":"address"},{"name":"token","type":"address"},{"name":"productAmount","type":"uint256"},{"name":"shippingAmount","type":"uint256"},{"name":"buyerUpline","type":"address"}],"name":"createOrder","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"orderId","type":"bytes32"}],"name":"markDelivered","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"orderId","type":"bytes32"}],"name":"confirmReceipt","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"orderId","type":"bytes32"}],"name":"requestRefund","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"orderId","type":"bytes32"}],"name":"cancelOrder","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"orderId","type":"bytes32"},{"name":"refundBuyer","type":"bool"}],"name":"adminResolve","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[],"name":"owner","outputs":[{"type":"address"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"account","type":"address"}],"name":"isAdmin","outputs":[{"type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"feeManager","outputs":[{"type":"address"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"token","type":"address"}],"name":"tokenSymbol","outputs":[{"type":"string"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"orderId","type":"bytes32"}],"name":"orders","outputs":[{"name":"buyer","type":"address"},{"name":"seller","type":"address"},{"name":"token","type":"address"},{"name":"productAmount","type":"uint256"},{"name":"shippingAmount","type":"uint256"},{"name":"platformFee","type":"uint256"},{"name":"buyerUpline","type":"address"},{"name":"createdAt","type":"uint256"},{"name":"sellerDelivered","type":"bool"},{"name":"buyerConfirmed","type":"bool"},{"name":"refundRequested","type":"bool"},{"name":"resolved","type":"bool"}],"stateMutability":"view","type":"function"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"orderId","type":"bytes32"},{"indexed":true,"name":"buyer","type":"address"},{"indexed":true,"name":"seller","type":"address"},{"indexed":false,"name":"token","type":"address"},{"indexed":false,"name":"productAmount","type":"uint256"},{"indexed":false,"name":"shippingAmount","type":"uint256"},{"indexed":false,"name":"platformFee","type":"uint256"}],"name":"OrderCreated","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"orderId","type":"bytes32"}],"name":"ReceiptConfirmed","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"orderId","type":"bytes32"}],"name":"OrderCancelled","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"orderId","type":"bytes32"}],"name":"DeliveryMarked","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"orderId","type":"bytes32"},{"indexed":false,"name":"refundedToBuyer","type":"bool"}],"name":"DisputeResolved","type":"event"}
]`

var parsedEscrowABI abi.ABI

func init() {
	var err error
	parsedEscrowABI, err = abi.JSON(strings.NewReader(productEscrowABI))
	if err != nil {
		panic(fmt.Sprintf("failed to parse ProductEscrow ABI: %v", err))
	}
}

// EscrowService prepares unsigned transactions for the ProductEscrow contract.
type EscrowService struct {
	client        *blockchain.EVMClient
	escrowAddress string
	cfg           *config.Config
	// rates caches escrow fee reads per feature + token address → rateEntry.
	rates sync.Map
	// caller serves the read-only fee lookups; nil without an RPC client.
	caller contractCaller
}

// contractCaller is the eth_call surface the fee lookups need.
type contractCaller interface {
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

// Constants of ProductEscrowV2 that the backend must mirror exactly.
const (
	// DefaultEscrowPlatformFeeBPS is PLATFORM_FEE_BPS: the fee when the escrow
	// has no FeeManager or no active product_merchant row for the token.
	DefaultEscrowPlatformFeeBPS = 200
	// MaxEscrowPlatformFeeBPS is MAX_PLATFORM_FEE_BPS: above it createOrder
	// reverts ("Platform fee too high").
	MaxEscrowPlatformFeeBPS = 300
	// DefaultEscrowGasFee is GAS_FEE ($0.10 in 6-decimal base units): the flat
	// gas fee when there is no FeeManager or no active product row.
	DefaultEscrowGasFee = 100000
)

// ErrEscrowFeeAboveMax means FeeManager holds a rate the escrow refuses, so
// every createOrder would revert. Pricing must stop rather than quote it.
var ErrEscrowFeeAboveMax = fmt.Errorf("escrow platform fee is above the contract maximum of %d bps", MaxEscrowPlatformFeeBPS)

const (
	// escrowRateTTL bounds how long a read rate is trusted. An admin fee change
	// confirmed on-chain reaches checkout within this window.
	escrowRateTTL = time.Minute
	// escrowRateMaxStale bounds how long a previous read may stand in for a
	// failed one. Past it the caller falls back to the fee_config replica.
	escrowRateMaxStale = 10 * time.Minute
)

// escrowRate is one FeeManager row as ProductEscrow resolves it.
type escrowRate struct {
	noFeeManager bool
	bps          int
	flatUSD      uint64 // 1e6 units, which the escrow uses as token base units
	exempt       bool
	active       bool
	feeManager   common.Address
	symbol       string // the escrow's own tokenSymbol for the token, which rateFor is keyed by
}

type rateEntry struct {
	rate   escrowRate
	readAt time.Time
}

// NewEscrowService creates a new EscrowService.
func NewEscrowService(client *blockchain.EVMClient, cfg *config.Config) *EscrowService {
	es := &EscrowService{
		client:        client,
		escrowAddress: cfg.ProductEscrowAddress,
		cfg:           cfg,
	}
	if client != nil {
		es.caller = client.Client
	}
	return es
}

// GenerateOrderID creates a deterministic bytes32 from a UUID using keccak256.
func GenerateOrderID(dbOrderID uuid.UUID) [32]byte {
	return crypto.Keccak256Hash(dbOrderID[:])
}

// PrepareCreateOrder encodes the createOrder call and builds an UnsignedTx.
// Must be called AFTER the buyer has broadcast the approve tx, so gas estimation works.
// Always targets the currently-configured escrow contract — new orders go to the latest deployment.
func (s *EscrowService) PrepareCreateOrder(orderId [32]byte, buyer, seller, token string, productAmount, shippingAmount *big.Int, upline string) ([]byte, error) {
	sellerAddr := common.HexToAddress(seller)
	tokenAddr := common.HexToAddress(token)
	uplineAddr := common.HexToAddress(upline)

	data, err := parsedEscrowABI.Pack("createOrder", orderId, sellerAddr, tokenAddr, productAmount, shippingAmount, uplineAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to pack createOrder: %w", err)
	}

	return s.buildUnsignedTx(buyer, s.escrowAddress, data)
}

// CurrentEscrowAddress returns the contract address from current config.
// Used by handlers to stamp new orders at submit-escrow time.
func (s *EscrowService) CurrentEscrowAddress() string {
	return s.escrowAddress
}

// PrepareConfirmReceipt encodes the confirmReceipt call against the order's escrow contract.
func (s *EscrowService) PrepareConfirmReceipt(orderId [32]byte, buyer, escrowAddr string) ([]byte, error) {
	data, err := parsedEscrowABI.Pack("confirmReceipt", orderId)
	if err != nil {
		return nil, fmt.Errorf("failed to pack confirmReceipt: %w", err)
	}
	return s.buildUnsignedTx(buyer, escrowAddr, data)
}

// PrepareRequestRefund encodes the requestRefund call against the order's escrow contract.
func (s *EscrowService) PrepareRequestRefund(orderId [32]byte, buyer, escrowAddr string) ([]byte, error) {
	data, err := parsedEscrowABI.Pack("requestRefund", orderId)
	if err != nil {
		return nil, fmt.Errorf("failed to pack requestRefund: %w", err)
	}
	return s.buildUnsignedTx(buyer, escrowAddr, data)
}

// PrepareMarkDelivered encodes the markDelivered call against the order's escrow contract.
// Until it lands on-chain the buyer's requestRefund is an instant full refund.
func (s *EscrowService) PrepareMarkDelivered(orderId [32]byte, seller, escrowAddr string) ([]byte, error) {
	data, err := parsedEscrowABI.Pack("markDelivered", orderId)
	if err != nil {
		return nil, fmt.Errorf("failed to pack markDelivered: %w", err)
	}
	return s.buildUnsignedTx(seller, escrowAddr, data)
}

// PrepareCancelOrder encodes the cancelOrder call against the order's escrow contract.
func (s *EscrowService) PrepareCancelOrder(orderId [32]byte, seller, escrowAddr string) ([]byte, error) {
	data, err := parsedEscrowABI.Pack("cancelOrder", orderId)
	if err != nil {
		return nil, fmt.Errorf("failed to pack cancelOrder: %w", err)
	}
	return s.buildUnsignedTx(seller, escrowAddr, data)
}

// Owner reads the escrow contract's owner() via eth_call. Used on startup to
// log the active deployment's owner. Admin-role authorization for adminResolve
// is checked per-request via IsAdmin — owner isn't queried for that path.
func (s *EscrowService) Owner() (string, error) {
	if s.client == nil {
		return "", fmt.Errorf("blockchain client not available")
	}
	data, err := parsedEscrowABI.Pack("owner")
	if err != nil {
		return "", fmt.Errorf("failed to pack owner(): %w", err)
	}
	escrowAddr := common.HexToAddress(s.escrowAddress)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := s.client.Client.CallContract(ctx, ethereum.CallMsg{
		To:   &escrowAddr,
		Data: data,
	}, nil)
	if err != nil {
		return "", fmt.Errorf("owner() call failed: %w", err)
	}
	unpacked, err := parsedEscrowABI.Unpack("owner", out)
	if err != nil || len(unpacked) == 0 {
		return "", fmt.Errorf("failed to decode owner(): %w", err)
	}
	addr, ok := unpacked[0].(common.Address)
	if !ok {
		return "", fmt.Errorf("owner() returned unexpected type %T", unpacked[0])
	}
	return addr.Hex(), nil
}

// IsAdmin reads the escrow contract's isAdmin(account) view via eth_call against
// the supplied contract address. Pass the order's escrow address when checking
// admin authorization for that specific order — admin sets can differ between
// deployments after a contract migration.
func (s *EscrowService) IsAdmin(account, contractAddr string) (bool, error) {
	if s.client == nil {
		return false, fmt.Errorf("blockchain client not available")
	}
	data, err := parsedEscrowABI.Pack("isAdmin", common.HexToAddress(account))
	if err != nil {
		return false, fmt.Errorf("failed to pack isAdmin: %w", err)
	}
	escrowAddr := common.HexToAddress(contractAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := s.client.Client.CallContract(ctx, ethereum.CallMsg{
		To:   &escrowAddr,
		Data: data,
	}, nil)
	if err != nil {
		return false, fmt.Errorf("isAdmin() call failed: %w", err)
	}
	unpacked, err := parsedEscrowABI.Unpack("isAdmin", out)
	if err != nil || len(unpacked) == 0 {
		return false, fmt.Errorf("failed to decode isAdmin: %w", err)
	}
	ok, okCast := unpacked[0].(bool)
	if !okCast {
		return false, fmt.Errorf("isAdmin returned unexpected type %T", unpacked[0])
	}
	return ok, nil
}

// PrepareAdminResolve encodes the adminResolve call against the order's escrow contract.
func (s *EscrowService) PrepareAdminResolve(orderId [32]byte, refundBuyer bool, admin, escrowAddr string) ([]byte, error) {
	data, err := parsedEscrowABI.Pack("adminResolve", orderId, refundBuyer)
	if err != nil {
		return nil, fmt.Errorf("failed to pack adminResolve: %w", err)
	}
	return s.buildUnsignedTx(admin, escrowAddr, data)
}

// EscrowCreated is the OrderCreated event for one order, as the escrow
// recorded it — the amounts it actually holds.
type EscrowCreated struct {
	OrderID        [32]byte
	Buyer          common.Address
	ProductAmount  *big.Int
	ShippingAmount *big.Int
	PlatformFee    *big.Int
}

// VerifyEscrowCreated checks that txHash succeeded and emitted OrderCreated on
// the configured escrow FOR expectedOrderID, and returns what the escrow
// recorded. Matching the id matters: without it, any successful escrow tx —
// another, cheaper order's — would mark this order as paid.
func (s *EscrowService) VerifyEscrowCreated(txHash string, expectedOrderID [32]byte) (*EscrowCreated, error) {
	if s.client == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	receipt, err := s.client.GetTransactionReceipt(txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipt: %w", err)
	}
	if receipt.Status != 1 {
		return nil, fmt.Errorf("transaction failed")
	}
	return findOrderCreated(receipt.Logs, common.HexToAddress(s.escrowAddress), expectedOrderID)
}

// ErrOrderEventNotFound: the transaction is mined but is not the escrow action
// claimed for the order. Any other VerifyOrderEvent error is an unreadable
// chain, which a caller may retry.
var ErrOrderEventNotFound = errors.New("the transaction does not do this to the order")

// VerifyOrderEvent checks that txHash succeeded and emitted event (an escrow
// event whose first indexed argument is the order id) on escrowAddr FOR orderID,
// and returns that log. A mined transaction proves nothing about an order by
// itself: without the event, any successful transaction would complete, refund
// or resolve it. escrowAddr is the order's own escrow, which may be an older
// deployment than the configured one.
func (s *EscrowService) VerifyOrderEvent(txHash, escrowAddr, event string, orderID [32]byte) (*types.Log, error) {
	if s.client == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	receipt, err := s.client.GetTransactionReceipt(txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipt: %w", err)
	}
	if receipt.Status != 1 {
		return nil, fmt.Errorf("%w: transaction failed", ErrOrderEventNotFound)
	}
	return findOrderEvent(receipt.Logs, common.HexToAddress(escrowAddr), event, orderID)
}

// findOrderEvent returns the log of event emitted by escrowAddr for orderID.
func findOrderEvent(logs []*types.Log, escrowAddr common.Address, event string, orderID [32]byte) (*types.Log, error) {
	ev, ok := parsedEscrowABI.Events[event]
	if !ok {
		return nil, fmt.Errorf("unknown escrow event %q", event)
	}
	for _, l := range logs {
		if l.Address == escrowAddr && len(l.Topics) >= 2 && l.Topics[0] == ev.ID && l.Topics[1] == common.Hash(orderID) {
			return l, nil
		}
	}
	return nil, fmt.Errorf("%w: no %s for this order in the receipt", ErrOrderEventNotFound, event)
}

// DisputeResolvedRefund reports whether a DisputeResolved log refunded the
// buyer. The chain's answer is the only one: an admin's request body is not.
func DisputeResolvedRefund(l *types.Log) (bool, error) {
	vals, err := parsedEscrowABI.Events["DisputeResolved"].Inputs.NonIndexed().Unpack(l.Data)
	if err != nil || len(vals) != 1 {
		return false, fmt.Errorf("decode DisputeResolved: %v", err)
	}
	refunded, ok := vals[0].(bool)
	if !ok {
		return false, fmt.Errorf("decode DisputeResolved: unexpected value type")
	}
	return refunded, nil
}

// OrderMatchesEscrow reports whether the escrow's record of an order is this
// order, funded as it was priced: unresolved (the money is still held), this
// buyer paying this seller, in the order's token, the amounts it was priced at.
// The order id is derivable from the order's UUID, so anyone could escrow under
// it, and the buyer signs createOrder on their own device — the seller and
// token in it are theirs to choose until this is checked.
func OrderMatchesEscrow(o *models.Order, sellerWallet, buyerWallet, tokenAddr string, onChain *OnChainOrder) bool {
	product, okP := new(big.Int).SetString(o.ProductAmount, 10)
	shipping, okS := new(big.Int).SetString(o.ShippingAmount, 10)
	return !(!okP || !okS || tokenAddr == "" || sellerWallet == "" || buyerWallet == "" || onChain == nil || onChain.Resolved ||
		!strings.EqualFold(onChain.Buyer.Hex(), common.HexToAddress(buyerWallet).Hex()) ||
		!strings.EqualFold(onChain.Seller.Hex(), common.HexToAddress(sellerWallet).Hex()) ||
		!strings.EqualFold(onChain.Token.Hex(), common.HexToAddress(tokenAddr).Hex()) ||
		onChain.ProductAmount.Cmp(product) != 0 || onChain.ShippingAmount.Cmp(shipping) != 0)
}

// OnChainOrder is an order as the escrow's orders(id) getter records it.
type OnChainOrder struct {
	Buyer, Seller, Token                       common.Address
	ProductAmount, ShippingAmount, PlatformFee *big.Int
	CreatedAt                                  time.Time
	// Resolved: the escrow has paid it out (refund or release); it holds nothing.
	Resolved bool
}

// SellerDelivered reads whether the seller's markDelivered is on-chain for
// orderId at escrowAddr (the order's own escrow). Only a successful read of an
// existing order answers; an RPC failure is an error, never "not delivered".
func (s *EscrowService) SellerDelivered(ctx context.Context, escrowAddr string, orderId [32]byte) (bool, error) {
	if s.caller == nil {
		return false, fmt.Errorf("blockchain client not available")
	}
	data, err := parsedEscrowABI.Pack("orders", orderId)
	if err != nil {
		return false, fmt.Errorf("pack orders: %w", err)
	}
	escrow := common.HexToAddress(escrowAddr)
	out, err := s.caller.CallContract(ctx, ethereum.CallMsg{To: &escrow, Data: data}, nil)
	if err != nil {
		return false, fmt.Errorf("orders call: %w", err)
	}
	vals, err := parsedEscrowABI.Unpack("orders", out)
	if err != nil || len(vals) < 12 {
		return false, fmt.Errorf("decode orders: %v (%d values)", err, len(vals))
	}
	buyer, ok1 := vals[0].(common.Address)
	delivered, ok2 := vals[8].(bool)
	if !ok1 || !ok2 || buyer == (common.Address{}) {
		return false, fmt.Errorf("decode orders: no escrowed order")
	}
	return delivered, nil
}

// EscrowedOrder reads the configured escrow's record of orderId, or nil when
// it holds none (createOrder records the buyer, so a zero buyer means nothing
// was ever escrowed). Only a successful read is an answer — an RPC failure is
// an error, never "not escrowed", because callers release stock on nil.
func (s *EscrowService) EscrowedOrder(ctx context.Context, orderId [32]byte) (*OnChainOrder, error) {
	if s.caller == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	data, err := parsedEscrowABI.Pack("orders", orderId)
	if err != nil {
		return nil, fmt.Errorf("pack orders: %w", err)
	}
	escrow := common.HexToAddress(s.escrowAddress)
	out, err := s.caller.CallContract(ctx, ethereum.CallMsg{To: &escrow, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("orders call: %w", err)
	}
	vals, err := parsedEscrowABI.Unpack("orders", out)
	if err != nil || len(vals) < 12 {
		return nil, fmt.Errorf("decode orders: %v (%d values)", err, len(vals))
	}
	buyer, ok1 := vals[0].(common.Address)
	seller, ok2 := vals[1].(common.Address)
	token, ok3 := vals[2].(common.Address)
	product, ok4 := vals[3].(*big.Int)
	shipping, ok5 := vals[4].(*big.Int)
	fee, ok6 := vals[5].(*big.Int)
	created, ok7 := vals[7].(*big.Int)
	resolved, ok8 := vals[11].(bool)
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7 && ok8) {
		return nil, fmt.Errorf("decode orders: unexpected value types")
	}
	if buyer == (common.Address{}) {
		return nil, nil
	}
	return &OnChainOrder{
		Buyer: buyer, Seller: seller, Token: token,
		ProductAmount: product, ShippingAmount: shipping, PlatformFee: fee,
		CreatedAt: time.Unix(created.Int64(), 0),
		Resolved:  resolved,
	}, nil
}

func findOrderCreated(logs []*types.Log, escrowAddr common.Address, expectedOrderID [32]byte) (*EscrowCreated, error) {
	event := parsedEscrowABI.Events["OrderCreated"]
	foundEscrowLog := false
	for _, l := range logs {
		if l.Address != escrowAddr {
			continue
		}
		foundEscrowLog = true
		if len(l.Topics) < 4 || l.Topics[0] != event.ID || l.Topics[1] != common.Hash(expectedOrderID) {
			continue
		}
		vals, err := event.Inputs.NonIndexed().Unpack(l.Data)
		if err != nil || len(vals) < 4 {
			return nil, fmt.Errorf("decode OrderCreated: %v", err)
		}
		product, _ := vals[1].(*big.Int)
		shipping, _ := vals[2].(*big.Int)
		fee, _ := vals[3].(*big.Int)
		if product == nil || shipping == nil || fee == nil {
			return nil, fmt.Errorf("decode OrderCreated: unexpected amount types")
		}
		return &EscrowCreated{
			OrderID:        expectedOrderID,
			Buyer:          common.BytesToAddress(l.Topics[2].Bytes()),
			ProductAmount:  product,
			ShippingAmount: shipping,
			PlatformFee:    fee,
		}, nil
	}
	if !foundEscrowLog {
		return nil, fmt.Errorf("no logs found for configured escrow contract %s in receipt", escrowAddr.Hex())
	}
	return nil, fmt.Errorf("OrderCreated for this order not found in receipt")
}

// buildUnsignedTx creates an UnsignedTx JSON targeting the supplied escrow contract,
// with real gas estimation when a chain client is available.
func (s *EscrowService) buildUnsignedTx(from, contractAddr string, callData []byte) ([]byte, error) {
	if contractAddr == "" {
		return nil, fmt.Errorf("escrow contract address is empty")
	}

	if s.client == nil {
		if s.cfg.IsProduction() {
			return nil, fmt.Errorf("blockchain client not available")
		}
		tx := blockchain.UnsignedTx{
			To:    contractAddr,
			Value: "0",
			Data:  common.Bytes2Hex(callData),
		}
		return json.Marshal(tx)
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

	gasLimit, err := s.client.EstimateGas(ethereum.CallMsg{
		From: fromAddr,
		To:   &escrowAddr,
		Data: callData,
	})
	if err != nil {
		return nil, fmt.Errorf("gas estimation failed (transaction would likely revert): %w", err)
	}

	tx := blockchain.UnsignedTx{
		Nonce:    nonce,
		GasPrice: gasPrice.String(),
		GasLimit: gasLimit,
		To:       contractAddr,
		Value:    "0",
		Data:     common.Bytes2Hex(callData),
		ChainID:  s.client.ChainID().String(),
	}

	return json.Marshal(tx)
}

// PlatformFeeBPS is the fee rate ProductEscrow will apply at createOrder for
// tokenAddr, resolved the way its _calculatePlatformFee does: no FeeManager →
// the built-in 200; the FeeManager's product_merchant row for the escrow's OWN
// symbol for the token (exact key, no wildcard) inactive → 200; exempt → 0;
// otherwise its bps, which above MaxEscrowPlatformFeeBPS makes the escrow
// revert (ErrEscrowFeeAboveMax). The caller handles MCGP, which the contract
// zeroes first.
//
// Reading the contract rather than fee_config keeps the recorded fee and the
// approve amount equal to what createOrder pulls, whichever admin tool last
// changed a rate.
func (s *EscrowService) PlatformFeeBPS(ctx context.Context, tokenAddr string) (int, error) {
	r, err := s.escrowRateFor(ctx, "product_merchant", tokenAddr)
	if err != nil {
		return 0, err
	}
	switch {
	case r.noFeeManager || !r.active:
		return DefaultEscrowPlatformFeeBPS, nil
	case r.exempt:
		return 0, nil
	case r.bps > MaxEscrowPlatformFeeBPS:
		return 0, ErrEscrowFeeAboveMax
	default:
		return r.bps, nil
	}
}

// GasFeeWei is the flat gas fee ProductEscrow adds at createOrder for tokenAddr,
// in the token's base units, resolved the way its _gasFee does: no FeeManager
// or an inactive product row → GAS_FEE; exempt → 0; otherwise the row's
// flatUSD. The caller handles MCGP, which the contract zeroes first.
func (s *EscrowService) GasFeeWei(ctx context.Context, tokenAddr string) (*big.Int, error) {
	r, err := s.escrowRateFor(ctx, "product", tokenAddr)
	if err != nil {
		return nil, err
	}
	switch {
	case r.noFeeManager || !r.active:
		return big.NewInt(DefaultEscrowGasFee), nil
	case r.exempt:
		return big.NewInt(0), nil
	default:
		return new(big.Int).SetUint64(r.flatUSD), nil
	}
}

// escrowRateFor returns the cached FeeManager row for feature/token, re-reading
// it past escrowRateTTL. When a re-read fails, a previous read younger than
// escrowRateMaxStale is returned without error (logged), so an RPC blip does
// not change prices mid-checkout.
func (s *EscrowService) escrowRateFor(ctx context.Context, feature, tokenAddr string) (escrowRate, error) {
	key := feature + "|" + strings.ToLower(tokenAddr)
	cached, hasCached := s.rates.Load(key)
	if hasCached && time.Since(cached.(rateEntry).readAt) < escrowRateTTL {
		return cached.(rateEntry).rate, nil
	}
	r, err := s.readEscrowRate(ctx, feature, tokenAddr)
	if err != nil {
		if hasCached && time.Since(cached.(rateEntry).readAt) < escrowRateMaxStale {
			log.Printf("[escrow] %s rate read for %s failed (%v); using the read from %s", feature, tokenAddr, err, cached.(rateEntry).readAt.Format(time.RFC3339))
			return cached.(rateEntry).rate, nil
		}
		return escrowRate{}, err
	}
	s.rates.Store(key, rateEntry{rate: r, readAt: time.Now()})
	return r, nil
}

func (s *EscrowService) readEscrowRate(ctx context.Context, feature, tokenAddr string) (escrowRate, error) {
	return readEscrowFeeRow(ctx, s.caller, s.escrowAddress, tokenAddr, feature)
}
