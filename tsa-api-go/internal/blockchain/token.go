package blockchain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// UnsignedTx represents an unsigned transaction ready for client-side signing.
type UnsignedTx struct {
	Nonce    uint64 `json:"nonce"`
	GasPrice string `json:"gasPrice"`
	GasLimit uint64 `json:"gasLimit"`
	To       string `json:"to"`
	Value    string `json:"value"`
	Data     string `json:"data"`
	ChainID  string `json:"chainId"`
}

// Standard ERC-20 ABI (subset we need)
const erc20ABI = `[
	{"constant":true,"inputs":[{"name":"account","type":"address"}],"name":"balanceOf","outputs":[{"name":"","type":"uint256"}],"type":"function"},
	{"constant":false,"inputs":[{"name":"to","type":"address"},{"name":"amount","type":"uint256"}],"name":"transfer","outputs":[{"name":"","type":"bool"}],"type":"function"},
	{"constant":false,"inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"name":"approve","outputs":[{"name":"","type":"bool"}],"type":"function"},
	{"constant":true,"inputs":[{"name":"owner","type":"address"},{"name":"spender","type":"address"}],"name":"allowance","outputs":[{"name":"","type":"uint256"}],"type":"function"},
	{"constant":true,"inputs":[],"name":"decimals","outputs":[{"name":"","type":"uint8"}],"type":"function"}
]`

var parsedERC20ABI abi.ABI

func init() {
	var err error
	parsedERC20ABI, err = abi.JSON(strings.NewReader(erc20ABI))
	if err != nil {
		panic(fmt.Sprintf("failed to parse ERC-20 ABI: %v", err))
	}
}

// GetTokenBalance returns the ERC-20 token balance of a wallet address.
func (c *EVMClient) GetTokenBalance(tokenAddress, walletAddress string) (*big.Int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tokenAddr := common.HexToAddress(tokenAddress)
	walletAddr := common.HexToAddress(walletAddress)

	data, err := parsedERC20ABI.Pack("balanceOf", walletAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to pack balanceOf call: %w", err)
	}

	result, err := c.Client.CallContract(ctx, ethereum.CallMsg{
		To:   &tokenAddr,
		Data: data,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to call balanceOf on %s: %w", tokenAddress, err)
	}

	if len(result) == 0 {
		return big.NewInt(0), nil
	}

	outputs, err := parsedERC20ABI.Unpack("balanceOf", result)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack balanceOf result: %w", err)
	}

	balance, ok := outputs[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("unexpected balanceOf return type")
	}

	return balance, nil
}

// HasContractCode reports whether an address holds contract code on this chain.
// An eth_call to an address with none returns empty data, which GetTokenBalance
// reads as a zero balance: only a contract can honestly answer "empty".
func (c *EVMClient) HasContractCode(address string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, err := c.Client.CodeAt(ctx, common.HexToAddress(address), nil)
	if err != nil {
		return false, fmt.Errorf("failed to read code at %s: %w", address, err)
	}
	return len(code) > 0, nil
}

// GetTokenDecimals returns the on-chain decimals() value for an ERC-20 token,
// memoised per contract address. This is the only safe way to handle the same
// symbol with different decimals on different chains (e.g. USDT is 6 on
// Ethereum/Tron/Polygon but 18 on BSC's Binance-Peg contract).
func (c *EVMClient) GetTokenDecimals(tokenAddress string) (int, error) {
	key := strings.ToLower(tokenAddress)
	if v, ok := c.decimalsCache.Load(key); ok {
		return v.(int), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tokenAddr := common.HexToAddress(tokenAddress)
	data, err := parsedERC20ABI.Pack("decimals")
	if err != nil {
		return 0, fmt.Errorf("pack decimals call: %w", err)
	}

	result, err := c.Client.CallContract(ctx, ethereum.CallMsg{
		To:   &tokenAddr,
		Data: data,
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("call decimals on %s: %w", tokenAddress, err)
	}
	if len(result) == 0 {
		return 0, fmt.Errorf("decimals() returned empty data for %s", tokenAddress)
	}

	outputs, err := parsedERC20ABI.Unpack("decimals", result)
	if err != nil {
		return 0, fmt.Errorf("unpack decimals result: %w", err)
	}
	d, ok := outputs[0].(uint8)
	if !ok {
		return 0, fmt.Errorf("unexpected decimals return type")
	}
	c.decimalsCache.Store(key, int(d))
	return int(d), nil
}

// GetTokenAllowance returns the approved allowance that owner has granted to spender.
func (c *EVMClient) GetTokenAllowance(tokenAddress, owner, spender string) (*big.Int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tokenAddr := common.HexToAddress(tokenAddress)
	ownerAddr := common.HexToAddress(owner)
	spenderAddr := common.HexToAddress(spender)

	data, err := parsedERC20ABI.Pack("allowance", ownerAddr, spenderAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to pack allowance call: %w", err)
	}

	result, err := c.Client.CallContract(ctx, ethereum.CallMsg{
		To:   &tokenAddr,
		Data: data,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to call allowance on %s: %w", tokenAddress, err)
	}

	if len(result) == 0 {
		return big.NewInt(0), nil
	}

	outputs, err := parsedERC20ABI.Unpack("allowance", result)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack allowance result: %w", err)
	}

	allowance, ok := outputs[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("unexpected allowance return type")
	}

	return allowance, nil
}

// ErrEstimateGas marks a PrepareContractCall whose gas estimate failed (with
// no fallback): the call would revert, or the node could not say. The node's
// error stays in the chain, so a caller can still decode its revert data.
var ErrEstimateGas = errors.New("estimate gas (would revert)")

// PrepareContractCall is the one unsigned-transaction builder: nonce (a hint —
// a KeyLane chooses its own), gas price with headroom (SuggestGasPrice), the
// gas estimate of the call, and the UnsignedTx JSON every signer and the app
// take. to is written as given; a nil value is a zero-value call. A failed
// estimate uses fallbackGas when it is non-zero, else returns ErrEstimateGas
// wrapping the node's error.
func (c *EVMClient) PrepareContractCall(from, to string, data []byte, value *big.Int, fallbackGas uint64) ([]byte, error) {
	toAddr := common.HexToAddress(to)

	nonce, err := c.GetNonce(from)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}

	gasPrice, err := c.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get gas price: %w", err)
	}

	gasLimit, err := c.EstimateGas(ethereum.CallMsg{From: common.HexToAddress(from), To: &toAddr, Value: value, Data: data})
	if err != nil {
		if fallbackGas == 0 {
			return nil, fmt.Errorf("%w: %w", ErrEstimateGas, err)
		}
		gasLimit = fallbackGas
	}

	txValue := "0"
	if value != nil {
		txValue = value.String()
	}
	return json.Marshal(UnsignedTx{
		Nonce:    nonce,
		GasPrice: gasPrice.String(),
		GasLimit: gasLimit,
		To:       to,
		Value:    txValue,
		Data:     common.Bytes2Hex(data),
		ChainID:  c.chainID.String(),
	})
}

// PrepareNativeTransfer builds an unsigned native token transfer transaction and returns it as JSON bytes.
func (c *EVMClient) PrepareNativeTransfer(from, to string, amountWei *big.Int) ([]byte, error) {
	return c.PrepareContractCall(from, to, nil, amountWei, 21000)
}

// PrepareERC20Transfer builds an unsigned ERC-20 transfer transaction and returns it as JSON bytes.
func (c *EVMClient) PrepareERC20Transfer(tokenAddress, from, to string, amount *big.Int) ([]byte, error) {
	data, err := ERC20TransferData(common.HexToAddress(to), amount)
	if err != nil {
		return nil, fmt.Errorf("failed to pack transfer call: %w", err)
	}
	// A failed estimate takes a reasonable default.
	return c.PrepareContractCall(from, tokenAddress, data, nil, 100000)
}

// PrepareERC20Approve builds an unsigned ERC-20 approve transaction and returns it as JSON bytes.
func (c *EVMClient) PrepareERC20Approve(tokenAddress, owner, spender string, amount *big.Int) ([]byte, error) {
	data, err := parsedERC20ABI.Pack("approve", common.HexToAddress(spender), amount)
	if err != nil {
		return nil, fmt.Errorf("failed to pack approve call: %w", err)
	}
	return c.PrepareContractCall(owner, tokenAddress, data, nil, 80000)
}
