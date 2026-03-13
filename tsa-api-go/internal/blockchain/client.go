package blockchain

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// SonicClient wraps an Ethereum-compatible JSON-RPC client configured for the Sonic network.
type SonicClient struct {
	Client  *ethclient.Client
	Config  *config.Config
	chainID *big.Int
}

// NewSonicClient connects to the Sonic RPC endpoint and returns a ready client.
func NewSonicClient(cfg *config.Config) (*SonicClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := ethclient.DialContext(ctx, cfg.SonicRPCURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Sonic RPC at %s: %w", cfg.SonicRPCURL, err)
	}

	return &SonicClient{
		Client:  client,
		Config:  cfg,
		chainID: big.NewInt(cfg.SonicChainID),
	}, nil
}

// ChainID returns the configured chain ID.
func (sc *SonicClient) ChainID() *big.Int {
	return sc.chainID
}

// GetBalance returns the native S token balance for the given address in wei.
func (sc *SonicClient) GetBalance(address string) (*big.Int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	addr := common.HexToAddress(address)
	balance, err := sc.Client.BalanceAt(ctx, addr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance for %s: %w", address, err)
	}
	return balance, nil
}

// GetNonce returns the next nonce for the given address.
func (sc *SonicClient) GetNonce(address string) (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	addr := common.HexToAddress(address)
	nonce, err := sc.Client.PendingNonceAt(ctx, addr)
	if err != nil {
		return 0, fmt.Errorf("failed to get nonce for %s: %w", address, err)
	}
	return nonce, nil
}

// EstimateGas estimates the gas needed for the given call message.
func (sc *SonicClient) EstimateGas(msg ethereum.CallMsg) (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	gas, err := sc.Client.EstimateGas(ctx, msg)
	if err != nil {
		return 0, fmt.Errorf("failed to estimate gas: %w", err)
	}
	return gas, nil
}

// SuggestGasPrice returns the currently suggested gas price.
func (sc *SonicClient) SuggestGasPrice() (*big.Int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	price, err := sc.Client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to suggest gas price: %w", err)
	}
	return price, nil
}

// SendRawTransaction submits an RLP-encoded signed transaction and returns the tx hash.
func (sc *SonicClient) SendRawTransaction(signedTx []byte) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tx := new(types.Transaction)
	if err := rlp.DecodeBytes(signedTx, tx); err != nil {
		return "", fmt.Errorf("failed to decode signed transaction: %w", err)
	}

	if err := sc.Client.SendTransaction(ctx, tx); err != nil {
		return "", fmt.Errorf("failed to send transaction: %w", err)
	}

	return tx.Hash().Hex(), nil
}

// GetTransactionReceipt retrieves the receipt for a mined transaction.
func (sc *SonicClient) GetTransactionReceipt(txHash string) (*types.Receipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hash := common.HexToHash(txHash)
	receipt, err := sc.Client.TransactionReceipt(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipt for %s: %w", txHash, err)
	}
	return receipt, nil
}
