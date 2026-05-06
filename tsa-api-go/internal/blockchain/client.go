package blockchain

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rlp"
)

// EVMClient wraps an Ethereum-compatible JSON-RPC client.
type EVMClient struct {
	Client  *ethclient.Client
	chainID *big.Int

	// decimalsCache memoises ERC-20 decimals() results keyed by lowercase
	// contract address. Decimals are immutable on-chain so we cache forever.
	decimalsCache sync.Map
}

// NewEVMClient connects to the given RPC endpoint and returns a ready client.
func NewEVMClient(rpcURL string, chainID int64) (*EVMClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RPC at %s: %w", rpcURL, err)
	}

	return &EVMClient{
		Client:  client,
		chainID: big.NewInt(chainID),
	}, nil
}

// ChainID returns the configured chain ID.
func (c *EVMClient) ChainID() *big.Int {
	return c.chainID
}

// GetBalance returns the native token balance for the given address in wei.
func (c *EVMClient) GetBalance(address string) (*big.Int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	addr := common.HexToAddress(address)
	balance, err := c.Client.BalanceAt(ctx, addr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance for %s: %w", address, err)
	}
	return balance, nil
}

// GetNonce returns the next nonce for the given address.
func (c *EVMClient) GetNonce(address string) (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	addr := common.HexToAddress(address)
	nonce, err := c.Client.PendingNonceAt(ctx, addr)
	if err != nil {
		return 0, fmt.Errorf("failed to get nonce for %s: %w", address, err)
	}
	return nonce, nil
}

// EstimateGas estimates the gas needed for the given call message.
func (c *EVMClient) EstimateGas(msg ethereum.CallMsg) (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	gas, err := c.Client.EstimateGas(ctx, msg)
	if err != nil {
		return 0, fmt.Errorf("failed to estimate gas: %w", err)
	}
	return gas, nil
}

// SuggestGasPrice returns the currently suggested gas price.
func (c *EVMClient) SuggestGasPrice() (*big.Int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	price, err := c.Client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to suggest gas price: %w", err)
	}
	return price, nil
}

// SendRawTransaction submits an RLP-encoded signed transaction and returns the tx hash.
func (c *EVMClient) SendRawTransaction(signedTx []byte) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tx := new(types.Transaction)
	if err := rlp.DecodeBytes(signedTx, tx); err != nil {
		return "", fmt.Errorf("failed to decode signed transaction: %w", err)
	}

	if err := c.Client.SendTransaction(ctx, tx); err != nil {
		return "", fmt.Errorf("failed to send transaction: %w", err)
	}

	return tx.Hash().Hex(), nil
}

// GetTransactionReceipt retrieves the receipt for a mined transaction.
func (c *EVMClient) GetTransactionReceipt(txHash string) (*types.Receipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hash := common.HexToHash(txHash)
	receipt, err := c.Client.TransactionReceipt(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipt for %s: %w", txHash, err)
	}
	return receipt, nil
}
