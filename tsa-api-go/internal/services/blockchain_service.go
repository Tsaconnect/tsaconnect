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
	clients map[string]*blockchain.EVMClient // keyed by "network:chain" e.g. "mainnet:sonic"
	cfg     *config.Config
}

// NewBlockchainService creates a BlockchainService connected to all configured chains
// across both mainnet and testnet networks.
func NewBlockchainService(cfg *config.Config) *BlockchainService {
	bs := &BlockchainService{
		clients: make(map[string]*blockchain.EVMClient),
		cfg:     cfg,
	}

	for network, netCfg := range cfg.Networks {
		for name, chain := range netCfg.Chains {
			key := network + ":" + name
			client, err := blockchain.NewEVMClient(chain.RPCURL, chain.ChainID)
			if err != nil {
				log.Printf("Warning: failed to connect to %s/%s (chain ID %d): %v", network, name, chain.ChainID, err)
				continue
			}
			bs.clients[key] = client
			log.Printf("%s/%s blockchain client connected (chain ID: %d, RPC: %s)", network, name, chain.ChainID, chain.RPCURL)
		}
	}

	return bs
}

// ClientForNetwork returns the EVMClient for a specific network and chain.
func (bs *BlockchainService) ClientForNetwork(network, chain string) *blockchain.EVMClient {
	if bs.clients == nil {
		return nil
	}
	return bs.clients[network+":"+chain]
}

// ClientForChain returns the EVMClient for the given chain name on mainnet (backward compat).
func (bs *BlockchainService) ClientForChain(chain string) *blockchain.EVMClient {
	return bs.ClientForNetwork("mainnet", chain)
}

// chainLocation searches every configured network for the chain with the
// given numeric chain ID, returning which network it lives on alongside its
// configured name. The one loop both ClientForChainID and ChainNameForID
// need, so a chain ID is only ever searched for once per call.
func (bs *BlockchainService) chainLocation(id int64) (network, name string, ok bool) {
	if bs.cfg == nil {
		return "", "", false
	}
	for network, netCfg := range bs.cfg.Networks {
		for name, chain := range netCfg.Chains {
			if chain.ChainID == id {
				return network, name, true
			}
		}
	}
	return "", "", false
}

// ClientForChainID returns the EVMClient matching the given numeric chain ID,
// searching across all networks.
func (bs *BlockchainService) ClientForChainID(chainID int64) (*blockchain.EVMClient, string) {
	network, name, ok := bs.chainLocation(chainID)
	if !ok {
		return nil, ""
	}
	return bs.clients[network+":"+name], name
}

// ChainNameForID resolves a numeric chain ID (e.g. a LiFi route's own
// chainId, or one a client sent) to the configured EVM chain key for it on
// the active network — the mirror of ChainIDForName. The server must never
// trust a client-supplied chain name; this is how it derives one instead.
func (bs *BlockchainService) ChainNameForID(id int64) (string, bool) {
	_, name, ok := bs.chainLocation(id)
	return name, ok
}

// Client returns the mainnet Sonic client for backward compatibility.
func (bs *BlockchainService) Client() *blockchain.EVMClient {
	return bs.ClientForChain("sonic")
}

// TokenAddressForNetwork returns the contract address for a token on a specific network/chain.
func (bs *BlockchainService) TokenAddressForNetwork(network, chain, symbol string) string {
	if bs.cfg == nil {
		return ""
	}
	if net, ok := bs.cfg.Networks[network]; ok {
		return net.TokenAddresses[chain+":"+symbol]
	}
	return ""
}

// TokenAddress returns the contract address for a token on mainnet (backward compat).
func (bs *BlockchainService) TokenAddress(chain, symbol string) string {
	return bs.TokenAddressForNetwork("mainnet", chain, symbol)
}

// TokenAddressForChainID returns the configured token address for a symbol on
// the chain with the given numeric chainID, searching across all networks.
func (bs *BlockchainService) TokenAddressForChainID(chainID int64, symbol string) string {
	if bs.cfg == nil {
		return ""
	}
	for _, netCfg := range bs.cfg.Networks {
		for name, chain := range netCfg.Chains {
			if chain.ChainID == chainID {
				return netCfg.TokenAddresses[name+":"+symbol]
			}
		}
	}
	return ""
}

// ChainIDForName resolves a chain NAME (e.g. "ethereum", "polygon", "sonic")
// to its numeric chain ID, preferring mainnet then any other configured
// network. Returns (id, true) on a hit. Used to thread a chainID into the
// per-chain fee lookup (configurable-FeeManager design §6) when a call site
// only has the human chain name (e.g. withdrawal's body.Network).
func (bs *BlockchainService) ChainIDForName(name string) (int64, bool) {
	if bs.cfg == nil || name == "" {
		return 0, false
	}
	if net, ok := bs.cfg.Networks["mainnet"]; ok {
		if chain, ok := net.Chains[name]; ok {
			return chain.ChainID, true
		}
	}
	for network, netCfg := range bs.cfg.Networks {
		if network == "mainnet" {
			continue
		}
		if chain, ok := netCfg.Chains[name]; ok {
			return chain.ChainID, true
		}
	}
	return 0, false
}

// NetworkForChainID returns the network name ("mainnet"/"testnet") and chain key for a chain ID.
func (bs *BlockchainService) NetworkForChainID(chainID int64) (network, chainKey string, ok bool) {
	if bs.cfg == nil {
		return "", "", false
	}
	for net, netCfg := range bs.cfg.Networks {
		for name, chain := range netCfg.Chains {
			if chain.ChainID == chainID {
				return net, name, true
			}
		}
	}
	return "", "", false
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

	// Raw: this figure is reported to a caller as what the chain charges, so it
	// must not carry the headroom that transaction building adds.
	gasPrice, err := client.SuggestGasPriceRaw()
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
