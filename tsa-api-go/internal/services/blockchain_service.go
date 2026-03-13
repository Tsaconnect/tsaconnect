package services

import (
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// TransactionReceipt represents a blockchain transaction receipt.
type TransactionReceipt struct {
	TxHash      string `json:"txHash"`
	Blockchain  string `json:"blockchain"`
	Status      string `json:"status"`
	BlockNumber int64  `json:"blockNumber"`
	GasUsed     int64  `json:"gasUsed"`
	Timestamp   int64  `json:"timestamp"`
}

// GasPrices represents gas price tiers for a blockchain.
type GasPrices struct {
	Chain   string  `json:"chain"`
	Slow    float64 `json:"slow"`
	Average float64 `json:"average"`
	Fast    float64 `json:"fast"`
}

// TransactionVerification represents the result of a transaction verification.
type TransactionVerification struct {
	TxHash   string `json:"txHash"`
	Chain    string `json:"chain"`
	Verified bool   `json:"verified"`
	Status   string `json:"status"`
	Message  string `json:"message"`
}

// BlockchainService provides blockchain interaction methods via the Sonic RPC client.
type BlockchainService struct {
	client *blockchain.SonicClient
}

// NewBlockchainService creates a new BlockchainService connected to the Sonic network.
func NewBlockchainService(cfg *config.Config) *BlockchainService {
	client, err := blockchain.NewSonicClient(cfg)
	if err != nil {
		log.Printf("Warning: failed to initialize Sonic blockchain client: %v", err)
		return &BlockchainService{}
	}
	log.Printf("Sonic blockchain client connected (chain ID: %s, RPC: %s)", client.ChainID().String(), cfg.SonicRPCURL)
	return &BlockchainService{client: client}
}

// Client returns the underlying SonicClient (may be nil if connection failed).
func (bs *BlockchainService) Client() *blockchain.SonicClient {
	return bs.client
}

// GetTransactionReceipt fetches a real transaction receipt from the Sonic network.
func (bs *BlockchainService) GetTransactionReceipt(txHash, chain string) *TransactionReceipt {
	if bs.client == nil {
		return &TransactionReceipt{
			TxHash:     txHash,
			Blockchain: chain,
			Status:     "error",
			Timestamp:  time.Now().Unix(),
		}
	}

	receipt, err := bs.client.GetTransactionReceipt(txHash)
	if err != nil {
		return &TransactionReceipt{
			TxHash:     txHash,
			Blockchain: chain,
			Status:     "not_found",
			Timestamp:  time.Now().Unix(),
		}
	}

	status := "failed"
	if receipt.Status == types.ReceiptStatusSuccessful {
		status = "confirmed"
	}

	return &TransactionReceipt{
		TxHash:      txHash,
		Blockchain:  chain,
		Status:      status,
		BlockNumber: receipt.BlockNumber.Int64(),
		GasUsed:     int64(receipt.GasUsed),
		Timestamp:   time.Now().Unix(),
	}
}

// GetGasPrices returns current gas prices from the Sonic network.
func (bs *BlockchainService) GetGasPrices(chain string) *GasPrices {
	if bs.client == nil {
		return &GasPrices{Chain: chain, Slow: 10, Average: 20, Fast: 40}
	}

	gasPrice, err := bs.client.SuggestGasPrice()
	if err != nil {
		return &GasPrices{Chain: chain, Slow: 10, Average: 20, Fast: 40}
	}

	// Convert wei to gwei
	gwei := new(big.Float).Quo(
		new(big.Float).SetInt(gasPrice),
		new(big.Float).SetFloat64(1e9),
	)
	avg, _ := gwei.Float64()

	return &GasPrices{
		Chain:   chain,
		Slow:    avg * 0.8,
		Average: avg,
		Fast:    avg * 1.5,
	}
}

// VerifyTransaction verifies a transaction on the Sonic network.
func (bs *BlockchainService) VerifyTransaction(txHash, chain string) *TransactionVerification {
	if bs.client == nil {
		return &TransactionVerification{
			TxHash:  txHash,
			Chain:   chain,
			Status:  "error",
			Message: "blockchain client not available",
		}
	}

	receipt, err := bs.client.GetTransactionReceipt(txHash)
	if err != nil {
		return &TransactionVerification{
			TxHash:   txHash,
			Chain:    chain,
			Verified: false,
			Status:   "not_found",
			Message:  "Transaction not found or not yet mined",
		}
	}

	verified := receipt.Status == types.ReceiptStatusSuccessful
	status := "failed"
	message := "Transaction failed"
	if verified {
		status = "confirmed"
		message = "Transaction verified successfully"
	}

	return &TransactionVerification{
		TxHash:   txHash,
		Chain:    chain,
		Verified: verified,
		Status:   status,
		Message:  message,
	}
}
