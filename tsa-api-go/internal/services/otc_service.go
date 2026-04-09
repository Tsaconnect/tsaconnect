package services

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
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// OtcMarketplace ABI — subset of functions we interact with
const otcMarketplaceABI = `[
	{"inputs":[{"name":"mcgpAmount","type":"uint256"},{"name":"maxUsdcAmount","type":"uint256"}],"name":"buy","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"mcgpAmount","type":"uint256"},{"name":"minUsdcAmount","type":"uint256"}],"name":"sell","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"mcgpAmount","type":"uint256"}],"name":"calculateBuyPrice","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"mcgpAmount","type":"uint256"}],"name":"calculateSellPrice","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"activeBuyPhaseIndex","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"activeSellPhaseIndex","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"getBalances","outputs":[{"name":"mcgpBalance","type":"uint256"},{"name":"usdcBalance","type":"uint256"}],"stateMutability":"view","type":"function"}
]`

var parsedOTCABI abi.ABI

func init() {
	var err error
	parsedOTCABI, err = abi.JSON(strings.NewReader(otcMarketplaceABI))
	if err != nil {
		panic(fmt.Sprintf("failed to parse OtcMarketplace ABI: %v", err))
	}
}

// OTCService prepares unsigned transactions and reads state for the OtcMarketplace contract.
type OTCService struct {
	client     *blockchain.EVMClient
	otcAddress string
	cfg        *config.Config
}

// NewOTCService creates a new OTCService.
func NewOTCService(client *blockchain.EVMClient, cfg *config.Config) *OTCService {
	return &OTCService{
		client:     client,
		otcAddress: cfg.OTCMarketplaceAddress,
		cfg:        cfg,
	}
}

// GetBuyPrice calls calculateBuyPrice on the contract to get the USDC cost for a given MCGP amount.
func (s *OTCService) GetBuyPrice(mcgpAmount *big.Int) (*big.Int, error) {
	return s.callView("calculateBuyPrice", mcgpAmount)
}

// GetSellPrice calls calculateSellPrice on the contract to get the USDC proceeds for a given MCGP amount.
func (s *OTCService) GetSellPrice(mcgpAmount *big.Int) (*big.Int, error) {
	return s.callView("calculateSellPrice", mcgpAmount)
}

// GetBuyPricePerToken returns the buy price for exactly 1 MCGP (1e18 wei).
func (s *OTCService) GetBuyPricePerToken() (*big.Int, error) {
	one := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	return s.GetBuyPrice(one)
}

// GetSellPricePerToken returns the sell price for exactly 1 MCGP (1e18 wei).
func (s *OTCService) GetSellPricePerToken() (*big.Int, error) {
	one := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	return s.GetSellPrice(one)
}

// PrepareBuy encodes the buy call and builds an UnsignedTx for the given buyer.
func (s *OTCService) PrepareBuy(buyer string, mcgpAmount, maxUsdcAmount *big.Int) ([]byte, error) {
	data, err := parsedOTCABI.Pack("buy", mcgpAmount, maxUsdcAmount)
	if err != nil {
		return nil, fmt.Errorf("failed to pack buy: %w", err)
	}
	return s.buildUnsignedTx(buyer, data)
}

// PrepareSell encodes the sell call and builds an UnsignedTx for the given seller.
func (s *OTCService) PrepareSell(seller string, mcgpAmount, minUsdcAmount *big.Int) ([]byte, error) {
	data, err := parsedOTCABI.Pack("sell", mcgpAmount, minUsdcAmount)
	if err != nil {
		return nil, fmt.Errorf("failed to pack sell: %w", err)
	}
	return s.buildUnsignedTx(seller, data)
}

// callView is a helper that calls a read-only (view) function on the OtcMarketplace contract
// and returns the first output as *big.Int.
func (s *OTCService) callView(method string, args ...interface{}) (*big.Int, error) {
	if s.client == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}

	data, err := parsedOTCABI.Pack(method, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to pack %s call: %w", method, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	otcAddr := common.HexToAddress(s.otcAddress)
	result, err := s.client.Client.CallContract(ctx, ethereum.CallMsg{
		To:   &otcAddr,
		Data: data,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to call %s: %w", method, err)
	}

	if len(result) == 0 {
		return big.NewInt(0), nil
	}

	outputs, err := parsedOTCABI.Unpack(method, result)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack %s result: %w", method, err)
	}

	value, ok := outputs[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("unexpected return type from %s", method)
	}

	return value, nil
}

// buildUnsignedTx creates an UnsignedTx JSON for the OtcMarketplace contract with real gas estimation.
func (s *OTCService) buildUnsignedTx(from string, callData []byte) ([]byte, error) {
	if s.client == nil {
		if s.cfg != nil && s.cfg.Env == "production" {
			return nil, fmt.Errorf("blockchain client not available")
		}
		tx := blockchain.UnsignedTx{
			To:    s.otcAddress,
			Value: "0",
			Data:  common.Bytes2Hex(callData),
		}
		return json.Marshal(tx)
	}

	fromAddr := common.HexToAddress(from)
	otcAddr := common.HexToAddress(s.otcAddress)

	nonce, err := s.client.GetNonce(from)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}

	gasPrice, err := s.client.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get gas price: %w", err)
	}

	gasLimit, err := s.client.EstimateGas(ethereum.CallMsg{
		From: fromAddr,
		To:   &otcAddr,
		Data: callData,
	})
	if err != nil {
		return nil, fmt.Errorf("gas estimation failed (transaction would likely revert): %w", err)
	}

	tx := blockchain.UnsignedTx{
		Nonce:    nonce,
		GasPrice: gasPrice.String(),
		GasLimit: gasLimit,
		To:       s.otcAddress,
		Value:    "0",
		Data:     common.Bytes2Hex(callData),
		ChainID:  s.client.ChainID().String(),
	}

	return json.Marshal(tx)
}
