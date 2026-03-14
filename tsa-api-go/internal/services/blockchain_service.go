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

// BlockchainService provides blockchain interaction methods via EVM RPC clients.
type BlockchainService struct {
	clients map[string]*blockchain.EVMClient // keyed by chain name: "sonic", "bsc"
	cfg     *config.Config
}

// NewBlockchainService creates a BlockchainService connected to all configured chains.
func NewBlockchainService(cfg *config.Config) *BlockchainService {
	bs := &BlockchainService{
		clients: make(map[string]*blockchain.EVMClient),
		cfg:     cfg,
	}

	for name, chain := range cfg.Chains {
		client, err := blockchain.NewEVMClient(chain.RPCURL, chain.ChainID)
		if err != nil {
			log.Printf("Warning: failed to connect to %s (chain ID %d): %v", name, chain.ChainID, err)
			continue
		}
		bs.clients[name] = client
		log.Printf("%s blockchain client connected (chain ID: %d, RPC: %s)", name, chain.ChainID, chain.RPCURL)
	}

	return bs
}

// ClientForChain returns the EVMClient for the given chain name.
func (bs *BlockchainService) ClientForChain(chain string) *blockchain.EVMClient {
	if bs.clients == nil {
		return nil
	}
	return bs.clients[chain]
}

// ClientForChainID returns the EVMClient matching the given numeric chain ID.
func (bs *BlockchainService) ClientForChainID(chainID int64) (*blockchain.EVMClient, string) {
	if bs.cfg == nil {
		return nil, ""
	}
	for name, chain := range bs.cfg.Chains {
		if chain.ChainID == chainID {
			if bs.clients == nil {
				return nil, name
			}
			return bs.clients[name], name
		}
	}
	return nil, ""
}

// Client returns the Sonic client for backward compatibility.
func (bs *BlockchainService) Client() *blockchain.EVMClient {
	return bs.ClientForChain("sonic")
}

// TokenAddress returns the contract address for a token on a chain.
func (bs *BlockchainService) TokenAddress(chain, symbol string) string {
	if bs.cfg == nil {
		return ""
	}
	return bs.cfg.TokenAddresses[chain+":"+symbol]
}

// GetTransactionReceipt fetches a transaction receipt from the specified chain.
func (bs *BlockchainService) GetTransactionReceipt(txHash, chain string) *TransactionReceipt {
	client := bs.ClientForChain(chain)
	if client == nil {
		return &TransactionReceipt{
			TxHash:     txHash,
			Blockchain: chain,
			Status:     "error",
			Timestamp:  time.Now().Unix(),
		}
	}

	receipt, err := client.GetTransactionReceipt(txHash)
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

// GetGasPrices returns current gas prices from the specified chain.
func (bs *BlockchainService) GetGasPrices(chain string) *GasPrices {
	client := bs.ClientForChain(chain)
	if client == nil {
		return &GasPrices{Chain: chain, Slow: 10, Average: 20, Fast: 40}
	}

	gasPrice, err := client.SuggestGasPrice()
	if err != nil {
		return &GasPrices{Chain: chain, Slow: 10, Average: 20, Fast: 40}
	}

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

// VerifyTransaction verifies a transaction on the specified chain.
func (bs *BlockchainService) VerifyTransaction(txHash, chain string) *TransactionVerification {
	client := bs.ClientForChain(chain)
	if client == nil {
		return &TransactionVerification{
			TxHash:  txHash,
			Chain:   chain,
			Status:  "error",
			Message: "blockchain client not available",
		}
	}

	receipt, err := client.GetTransactionReceipt(txHash)
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
