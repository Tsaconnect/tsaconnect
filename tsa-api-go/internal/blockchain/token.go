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

// TokenInfo describes a supported ERC-20 token.
type TokenInfo struct {
	Address  string `json:"address"`
	Symbol   string `json:"symbol"`
	Decimals uint8  `json:"decimals"`
}

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

// GetSupportedTokens returns the list of configured tokens on the Sonic network.
func (sc *SonicClient) GetSupportedTokens() []TokenInfo {
	tokens := []TokenInfo{}

	if sc.Config.MCGPTokenAddress != "" {
		tokens = append(tokens, TokenInfo{
			Address:  sc.Config.MCGPTokenAddress,
			Symbol:   "MCGP",
			Decimals: 18,
		})
	}
	if sc.Config.USDTTokenAddress != "" {
		tokens = append(tokens, TokenInfo{
			Address:  sc.Config.USDTTokenAddress,
			Symbol:   "USDT",
			Decimals: 6,
		})
	}
	if sc.Config.USDCTokenAddress != "" {
		tokens = append(tokens, TokenInfo{
			Address:  sc.Config.USDCTokenAddress,
			Symbol:   "USDC",
			Decimals: 6,
		})
	}

	return tokens
}

// GetTokenBalance returns the ERC-20 token balance of a wallet address.
func (sc *SonicClient) GetTokenBalance(tokenAddress, walletAddress string) (*big.Int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tokenAddr := common.HexToAddress(tokenAddress)
	walletAddr := common.HexToAddress(walletAddress)

	data, err := parsedERC20ABI.Pack("balanceOf", walletAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to pack balanceOf call: %w", err)
	}

	result, err := sc.Client.CallContract(ctx, ethereum.CallMsg{
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

// GetAllBalances returns token balances for all configured tokens.
// The map keys are token symbols (e.g. "MCGP", "USDT", "USDC").
func (sc *SonicClient) GetAllBalances(walletAddress string) (map[string]*big.Int, error) {
	balances := make(map[string]*big.Int)

	for _, token := range sc.GetSupportedTokens() {
		balance, err := sc.GetTokenBalance(token.Address, walletAddress)
		if err != nil {
			// Log warning but continue — a missing token contract shouldn't break all balances
			balances[token.Symbol] = big.NewInt(0)
			continue
		}
		balances[token.Symbol] = balance
	}

	return balances, nil
}

// PrepareERC20Transfer builds an unsigned ERC-20 transfer transaction and returns it as JSON bytes.
func (sc *SonicClient) PrepareERC20Transfer(tokenAddress, from, to string, amount *big.Int) ([]byte, error) {
	toAddr := common.HexToAddress(to)
	tokenAddr := common.HexToAddress(tokenAddress)
	fromAddr := common.HexToAddress(from)

	data, err := parsedERC20ABI.Pack("transfer", toAddr, amount)
	if err != nil {
		return nil, fmt.Errorf("failed to pack transfer call: %w", err)
	}

	nonce, err := sc.GetNonce(from)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}

	gasPrice, err := sc.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get gas price: %w", err)
	}

	gasLimit, err := sc.EstimateGas(ethereum.CallMsg{
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
		ChainID:  sc.chainID.String(),
	}

	return json.Marshal(tx)
}

// PrepareERC20Approve builds an unsigned ERC-20 approve transaction and returns it as JSON bytes.
func (sc *SonicClient) PrepareERC20Approve(tokenAddress, owner, spender string, amount *big.Int) ([]byte, error) {
	spenderAddr := common.HexToAddress(spender)
	tokenAddr := common.HexToAddress(tokenAddress)
	ownerAddr := common.HexToAddress(owner)

	data, err := parsedERC20ABI.Pack("approve", spenderAddr, amount)
	if err != nil {
		return nil, fmt.Errorf("failed to pack approve call: %w", err)
	}

	nonce, err := sc.GetNonce(owner)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}

	gasPrice, err := sc.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get gas price: %w", err)
	}

	gasLimit, err := sc.EstimateGas(ethereum.CallMsg{
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
		ChainID:  sc.chainID.String(),
	}

	return json.Marshal(tx)
}
