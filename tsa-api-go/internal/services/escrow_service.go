package services

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// ProductEscrow ABI — subset of functions and events we interact with
const productEscrowABI = `[
	{"inputs":[{"name":"orderId","type":"bytes32"},{"name":"seller","type":"address"},{"name":"token","type":"address"},{"name":"productAmount","type":"uint256"},{"name":"shippingAmount","type":"uint256"},{"name":"buyerUpline","type":"address"}],"name":"createOrder","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"orderId","type":"bytes32"}],"name":"markDelivered","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"orderId","type":"bytes32"}],"name":"confirmReceipt","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"orderId","type":"bytes32"}],"name":"requestRefund","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"orderId","type":"bytes32"}],"name":"cancelOrder","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"orderId","type":"bytes32"},{"name":"refundBuyer","type":"bool"}],"name":"adminResolve","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"orderId","type":"bytes32"},{"indexed":true,"name":"buyer","type":"address"},{"indexed":true,"name":"seller","type":"address"},{"indexed":false,"name":"token","type":"address"},{"indexed":false,"name":"productAmount","type":"uint256"},{"indexed":false,"name":"shippingAmount","type":"uint256"},{"indexed":false,"name":"platformFee","type":"uint256"}],"name":"OrderCreated","type":"event"}
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
	client          *blockchain.EVMClient
	escrowAddress   string
	cfg             *config.Config
}

// NewEscrowService creates a new EscrowService.
func NewEscrowService(client *blockchain.EVMClient, cfg *config.Config) *EscrowService {
	return &EscrowService{
		client:        client,
		escrowAddress: cfg.ProductEscrowAddress,
		cfg:           cfg,
	}
}

// GenerateOrderID creates a deterministic bytes32 from a UUID using keccak256.
func GenerateOrderID(dbOrderID uuid.UUID) [32]byte {
	return crypto.Keccak256Hash(dbOrderID[:])
}

// PrepareCreateOrder encodes the createOrder call and builds an UnsignedTx.
// Must be called AFTER the buyer has broadcast the approve tx, so gas estimation works.
func (s *EscrowService) PrepareCreateOrder(orderId [32]byte, buyer, seller, token string, productAmount, shippingAmount *big.Int, upline string) ([]byte, error) {
	sellerAddr := common.HexToAddress(seller)
	tokenAddr := common.HexToAddress(token)
	uplineAddr := common.HexToAddress(upline)

	data, err := parsedEscrowABI.Pack("createOrder", orderId, sellerAddr, tokenAddr, productAmount, shippingAmount, uplineAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to pack createOrder: %w", err)
	}

	return s.buildUnsignedTx(buyer, data)
}

// PrepareConfirmReceipt encodes the confirmReceipt call.
func (s *EscrowService) PrepareConfirmReceipt(orderId [32]byte, buyer string) ([]byte, error) {
	data, err := parsedEscrowABI.Pack("confirmReceipt", orderId)
	if err != nil {
		return nil, fmt.Errorf("failed to pack confirmReceipt: %w", err)
	}
	return s.buildUnsignedTx(buyer, data)
}

// PrepareRequestRefund encodes the requestRefund call.
func (s *EscrowService) PrepareRequestRefund(orderId [32]byte, buyer string) ([]byte, error) {
	data, err := parsedEscrowABI.Pack("requestRefund", orderId)
	if err != nil {
		return nil, fmt.Errorf("failed to pack requestRefund: %w", err)
	}
	return s.buildUnsignedTx(buyer, data)
}

// PrepareCancelOrder encodes the cancelOrder call.
func (s *EscrowService) PrepareCancelOrder(orderId [32]byte, seller string) ([]byte, error) {
	data, err := parsedEscrowABI.Pack("cancelOrder", orderId)
	if err != nil {
		return nil, fmt.Errorf("failed to pack cancelOrder: %w", err)
	}
	return s.buildUnsignedTx(seller, data)
}

// PrepareAdminResolve encodes the adminResolve call.
func (s *EscrowService) PrepareAdminResolve(orderId [32]byte, refundBuyer bool, admin string) ([]byte, error) {
	data, err := parsedEscrowABI.Pack("adminResolve", orderId, refundBuyer)
	if err != nil {
		return nil, fmt.Errorf("failed to pack adminResolve: %w", err)
	}
	return s.buildUnsignedTx(admin, data)
}

// VerifyEscrowCreated fetches the receipt and parses the OrderCreated event.
func (s *EscrowService) VerifyEscrowCreated(txHash string) ([32]byte, error) {
	var empty [32]byte

	if s.client == nil {
		return empty, fmt.Errorf("blockchain client not available")
	}

	receipt, err := s.client.GetTransactionReceipt(txHash)
	if err != nil {
		return empty, fmt.Errorf("failed to get receipt: %w", err)
	}

	if receipt.Status != 1 {
		return empty, fmt.Errorf("transaction failed")
	}

	escrowAddr := common.HexToAddress(s.escrowAddress)
	orderCreatedSig := parsedEscrowABI.Events["OrderCreated"].ID

	for _, log := range receipt.Logs {
		if log.Address != escrowAddr {
			continue
		}
		if len(log.Topics) < 1 || log.Topics[0] != orderCreatedSig {
			continue
		}
		if len(log.Topics) < 2 {
			continue
		}
		// Topics[1] is the indexed orderId
		var orderId [32]byte
		copy(orderId[:], log.Topics[1].Bytes())
		return orderId, nil
	}

	return empty, fmt.Errorf("OrderCreated event not found in receipt")
}

// buildUnsignedTx creates an UnsignedTx JSON for the escrow contract with real gas estimation.
func (s *EscrowService) buildUnsignedTx(from string, callData []byte) ([]byte, error) {
	if s.client == nil {
		if s.cfg != nil && s.cfg.Env == "production" {
			return nil, fmt.Errorf("blockchain client not available")
		}
		tx := blockchain.UnsignedTx{
			To:    s.escrowAddress,
			Value: "0",
			Data:  common.Bytes2Hex(callData),
		}
		return json.Marshal(tx)
	}

	fromAddr := common.HexToAddress(from)
	escrowAddr := common.HexToAddress(s.escrowAddress)

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
		To:       s.escrowAddress,
		Value:    "0",
		Data:     common.Bytes2Hex(callData),
		ChainID:  s.client.ChainID().String(),
	}

	return json.Marshal(tx)
}
