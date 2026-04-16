package blockchain

import (
	"context"
	"encoding/json"
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
	{"constant":true,"inputs":[{"name":"owner","type":"address"},{"name":"spender","type":"address"}],"name":"allowance","outputs":[{"name":"","type":"uint256"}],"type":"function"}
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

// PrepareNativeTransfer builds an unsigned native token transfer transaction and returns it as JSON bytes.
func (c *EVMClient) PrepareNativeTransfer(from, to string, amountWei *big.Int) ([]byte, error) {
	fromAddr := common.HexToAddress(from)
	toAddr := common.HexToAddress(to)

	nonce, err := c.GetNonce(from)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}

	gasPrice, err := c.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get gas price: %w", err)
	}

	gasLimit, err := c.EstimateGas(ethereum.CallMsg{
		From:  fromAddr,
		To:    &toAddr,
		Value: amountWei,
	})
	if err != nil {
		gasLimit = 21000
	}

	tx := UnsignedTx{
		Nonce:    nonce,
		GasPrice: gasPrice.String(),
		GasLimit: gasLimit,
		To:       to,
		Value:    amountWei.String(),
		Data:     "",
		ChainID:  c.chainID.String(),
	}

	return json.Marshal(tx)
}

// PrepareERC20Transfer builds an unsigned ERC-20 transfer transaction and returns it as JSON bytes.
func (c *EVMClient) PrepareERC20Transfer(tokenAddress, from, to string, amount *big.Int) ([]byte, error) {
	toAddr := common.HexToAddress(to)
	tokenAddr := common.HexToAddress(tokenAddress)
	fromAddr := common.HexToAddress(from)

	data, err := parsedERC20ABI.Pack("transfer", toAddr, amount)
	if err != nil {
		return nil, fmt.Errorf("failed to pack transfer call: %w", err)
	}

	nonce, err := c.GetNonce(from)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}

	gasPrice, err := c.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get gas price: %w", err)
	}

	gasLimit, err := c.EstimateGas(ethereum.CallMsg{
		From: fromAddr,
		To:   &tokenAddr,
		Data: data,
	})
	if err != nil {
		// Use a reasonable default if estimation fails
		gasLimit = 100000
	}

	tx := UnsignedTx{
		Nonce:    nonce,
		GasPrice: gasPrice.String(),
		GasLimit: gasLimit,
		To:       tokenAddress,
		Value:    "0",
		Data:     common.Bytes2Hex(data),
		ChainID:  c.chainID.String(),
	}

	return json.Marshal(tx)
}

// PrepareERC20Approve builds an unsigned ERC-20 approve transaction and returns it as JSON bytes.
func (c *EVMClient) PrepareERC20Approve(tokenAddress, owner, spender string, amount *big.Int) ([]byte, error) {
	spenderAddr := common.HexToAddress(spender)
	tokenAddr := common.HexToAddress(tokenAddress)
	ownerAddr := common.HexToAddress(owner)

	data, err := parsedERC20ABI.Pack("approve", spenderAddr, amount)
	if err != nil {
		return nil, fmt.Errorf("failed to pack approve call: %w", err)
	}

	nonce, err := c.GetNonce(owner)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}

	gasPrice, err := c.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get gas price: %w", err)
	}

	gasLimit, err := c.EstimateGas(ethereum.CallMsg{
		From: ownerAddr,
		To:   &tokenAddr,
		Data: data,
	})
	if err != nil {
		gasLimit = 80000
	}

	tx := UnsignedTx{
		Nonce:    nonce,
		GasPrice: gasPrice.String(),
		GasLimit: gasLimit,
		To:       tokenAddress,
		Value:    "0",
		Data:     common.Bytes2Hex(data),
		ChainID:  c.chainID.String(),
	}

	return json.Marshal(tx)
}
