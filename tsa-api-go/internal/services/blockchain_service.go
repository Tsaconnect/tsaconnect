package services

import "time"

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
	TxHash    string `json:"txHash"`
	Chain     string `json:"chain"`
	Verified  bool   `json:"verified"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

// BlockchainService provides mock blockchain interaction methods.
type BlockchainService struct{}

// NewBlockchainService creates a new BlockchainService.
func NewBlockchainService() *BlockchainService {
	return &BlockchainService{}
}

// GetTransactionReceipt returns a mock transaction receipt.
func (bs *BlockchainService) GetTransactionReceipt(txHash, blockchain string) *TransactionReceipt {
	return &TransactionReceipt{
		TxHash:      txHash,
		Blockchain:  blockchain,
		Status:      "confirmed",
		BlockNumber: 1234567,
		GasUsed:     21000,
		Timestamp:   time.Now().Unix(),
	}
}

// GetGasPrices returns mock gas prices for the specified chain.
func (bs *BlockchainService) GetGasPrices(chain string) *GasPrices {
	prices := map[string]*GasPrices{
		"ethereum": {Chain: "ethereum", Slow: 20, Average: 35, Fast: 60},
		"bsc":      {Chain: "bsc", Slow: 3, Average: 5, Fast: 8},
		"polygon":  {Chain: "polygon", Slow: 30, Average: 50, Fast: 80},
		"solana":   {Chain: "solana", Slow: 0.000005, Average: 0.000005, Fast: 0.000005},
	}

	if gp, ok := prices[chain]; ok {
		return gp
	}

	return &GasPrices{
		Chain:   chain,
		Slow:    10,
		Average: 20,
		Fast:    40,
	}
}

// VerifyTransaction returns a mock transaction verification.
func (bs *BlockchainService) VerifyTransaction(txHash, chain string) *TransactionVerification {
	return &TransactionVerification{
		TxHash:   txHash,
		Chain:    chain,
		Verified: true,
		Status:   "confirmed",
		Message:  "Transaction verified successfully",
	}
}
