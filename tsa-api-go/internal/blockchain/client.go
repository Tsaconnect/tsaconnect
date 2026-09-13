package blockchain

import (
	"context"
	"errors"
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

// GetCode returns the contract bytecode at the given address. An empty
// result (len == 0) means the address is not a contract — calls to it will
// succeed as no-ops, which is typically a misconfiguration.
func (c *EVMClient) GetCode(address string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	addr := common.HexToAddress(address)
	code, err := c.Client.CodeAt(ctx, addr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get code for %s: %w", address, err)
	}
	return code, nil
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

// SuggestGasPriceRaw returns the node's own suggestion, unmodified.
//
// This is the price to QUOTE, not the price to SIGN. Use it only where the
// number is shown to somebody as what the chain currently charges; anything
// that builds a transaction wants SuggestGasPrice, which adds headroom.
func (c *EVMClient) SuggestGasPriceRaw() (*big.Int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	price, err := c.Client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to suggest gas price: %w", err)
	}
	return price, nil
}

// gasPriceHeadroomPercent is how far above the node's suggestion a transaction
// is priced.
//
// The suggestion is a snapshot taken when the tx is built, but these are legacy
// (type-0) transactions signed and broadcast some time later — seconds, when a
// user is reading a confirmation screen. Every chain here reprices per block,
// so a tx priced at the old base fee is rejected outright with "max fee per gas
// less than block base fee", and a signed tx cannot be repriced.
//
// 10% is deliberately modest, for two reasons. A type-0 tx pays its full gas
// price — unlike an EIP-1559 fee cap, the surplus above the base fee is NOT
// refunded — so on Ethereum mainnet at 30 gwei every 10% is real money, roughly
// $0.15 a transfer. And headroom is no longer the only defence: the app
// re-prepares and re-signs once when a broadcast is rejected this way
// (staleKind in tsa-app/services/walletApi.ts), which costs a round trip
// rather than a fee. Headroom covers the common small drift; the retry covers
// the rest. Buying more of the first with real money to avoid the second would
// be a bad trade.
//
// Because headroom is the default, it also applies where the PLATFORM pays the
// gas rather than the user — the relayer's own transactions (see
// internal/services/relayer_adapters.go). That is a 10% uplift on a cost line
// we carry, immaterial at Sonic prices but worth knowing before the percentage
// is raised or the relayer moves to a chain where gas is not free.
//
// The real fix is to stop emitting type-0 transactions: with maxFeePerGas the
// surplus comes back and the cap can be generous. That is a cross-repo change
// (UnsignedTx, every signing call site in the app) and is tracked separately.
const gasPriceHeadroomPercent = 10

// SuggestGasPrice returns the price to build a transaction with: the node's
// suggestion plus gasPriceHeadroomPercent.
//
// Headroom is the DEFAULT on purpose. Every tx builder in this codebase reaches
// for this name, and a builder that silently prices at a stale base fee fails
// in the user's hands, not in review — so the safe behaviour is the one you get
// without knowing to ask. Reach for SuggestGasPriceRaw only to display a price.
func (c *EVMClient) SuggestGasPrice() (*big.Int, error) {
	price, err := c.SuggestGasPriceRaw()
	if err != nil {
		return nil, err
	}
	if price == nil {
		// No error and no price. Nothing in go-ethereum produces this today,
		// but falling through would build a transaction priced at zero, which
		// no node accepts — an error here is the honest outcome, and it keeps
		// the failure at the point where the cause is still known.
		return nil, errors.New("failed to suggest gas price: node returned no price")
	}
	return applyGasHeadroom(price), nil
}

// applyGasHeadroom raises price by gasPriceHeadroomPercent, without mutating it.
//
// price is expected to be non-nil; its one caller rejects nil with an error
// before reaching here. The nil branch is a backstop so that a future caller
// that forgets cannot panic inside a fee calculation and take a request handler
// with it — it is not a meaningful price, and callers must not treat it as one.
func applyGasHeadroom(price *big.Int) *big.Int {
	if price == nil {
		return big.NewInt(0)
	}
	buffered := new(big.Int).Mul(price, big.NewInt(100+gasPriceHeadroomPercent))
	return buffered.Div(buffered, big.NewInt(100))
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

const receiptPollInterval = 1500 * time.Millisecond

// WaitForReceipt polls for a transaction's receipt until it's mined or `timeout`
// elapses. A just-broadcast tx isn't mined yet — eth_getTransactionReceipt
// returns "not found" — so confirm flows that re-read the receipt to verify an
// on-chain action must wait rather than fail on the first miss. Retries on any
// error (NotFound or a transient RPC blip) and returns the last error only after
// the deadline passes.
func (c *EVMClient) WaitForReceipt(txHash string, timeout time.Duration) (*types.Receipt, error) {
	hash := common.HexToHash(txHash)
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		receipt, err := c.Client.TransactionReceipt(ctx, hash)
		cancel()
		if err == nil {
			return receipt, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("failed to get receipt for %s within %s: %w", txHash, timeout, err)
		}
		time.Sleep(receiptPollInterval)
	}
}
