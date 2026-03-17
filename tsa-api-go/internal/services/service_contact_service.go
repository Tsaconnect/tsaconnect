package services

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// ServiceContact ABI — subset of functions and events we interact with
const serviceContactABI = `[
	{"inputs":[{"name":"serviceProvider","type":"address"},{"name":"requesterUpline","type":"address"},{"name":"token","type":"address"}],"name":"payContactFee","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"caller","type":"address"},{"indexed":true,"name":"serviceProvider","type":"address"},{"indexed":true,"name":"token","type":"address"},{"indexed":false,"name":"amount","type":"uint256"},{"indexed":false,"name":"upline","type":"address"}],"name":"ContactFeePaid","type":"event"}
]`

var parsedServiceContactABI abi.ABI

func init() {
	var err error
	parsedServiceContactABI, err = abi.JSON(strings.NewReader(serviceContactABI))
	if err != nil {
		panic(fmt.Sprintf("failed to parse ServiceContact ABI: %v", err))
	}
}

// ServiceContactService prepares unsigned transactions for the ServiceContact contract.
type ServiceContactService struct {
	client          *blockchain.EVMClient
	contractAddress string
	cfg             *config.Config
}

// NewServiceContactService creates a new ServiceContactService.
func NewServiceContactService(client *blockchain.EVMClient, cfg *config.Config) *ServiceContactService {
	return &ServiceContactService{
		client:          client,
		contractAddress: cfg.ServiceContractAddress,
		cfg:             cfg,
	}
}

// GetFeeAmount returns the contact fee in the smallest token unit (100000 = $0.10 for 6-decimal tokens).
func (s *ServiceContactService) GetFeeAmount() *big.Int {
	return big.NewInt(100000)
}

// PreparePayContactFee encodes the payContactFee call and builds an UnsignedTx.
func (s *ServiceContactService) PreparePayContactFee(caller, serviceProvider, upline, token string) ([]byte, error) {
	providerAddr := common.HexToAddress(serviceProvider)
	uplineAddr := common.HexToAddress(upline)
	tokenAddr := common.HexToAddress(token)

	data, err := parsedServiceContactABI.Pack("payContactFee", providerAddr, uplineAddr, tokenAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to pack payContactFee: %w", err)
	}

	return s.buildUnsignedTx(caller, data)
}

// ContactFeeEvent holds the parsed data from a ContactFeePaid event.
type ContactFeeEvent struct {
	Caller          common.Address
	ServiceProvider common.Address
	Token           common.Address
}

// VerifyContactFeePaid fetches the receipt and validates the ContactFeePaid event
// matches the expected caller and service provider addresses.
func (s *ServiceContactService) VerifyContactFeePaid(txHash, expectedCaller, expectedProvider string) (*ContactFeeEvent, error) {
	if s.client == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}

	receipt, err := s.client.GetTransactionReceipt(txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipt: %w", err)
	}

	if receipt.Status != 1 {
		return nil, fmt.Errorf("transaction failed")
	}

	contractAddr := common.HexToAddress(s.contractAddress)
	contactFeePaidSig := parsedServiceContactABI.Events["ContactFeePaid"].ID

	for _, log := range receipt.Logs {
		if log.Address != contractAddr {
			continue
		}
		if len(log.Topics) < 4 || log.Topics[0] != contactFeePaidSig {
			continue
		}

		// Topics: [0]=sig, [1]=caller(indexed), [2]=serviceProvider(indexed), [3]=token(indexed)
		caller := common.BytesToAddress(log.Topics[1].Bytes())
		provider := common.BytesToAddress(log.Topics[2].Bytes())
		token := common.BytesToAddress(log.Topics[3].Bytes())

		// Validate that the on-chain caller/provider match the authenticated user/provider
		if caller != common.HexToAddress(expectedCaller) {
			return nil, fmt.Errorf("on-chain caller %s does not match authenticated user %s", caller.Hex(), expectedCaller)
		}
		if provider != common.HexToAddress(expectedProvider) {
			return nil, fmt.Errorf("on-chain provider %s does not match expected provider %s", provider.Hex(), expectedProvider)
		}

		return &ContactFeeEvent{
			Caller:          caller,
			ServiceProvider: provider,
			Token:           token,
		}, nil
	}

	return nil, fmt.Errorf("ContactFeePaid event not found in receipt")
}

// buildUnsignedTx creates an UnsignedTx JSON for the ServiceContact contract.
func (s *ServiceContactService) buildUnsignedTx(from string, callData []byte) ([]byte, error) {
	if s.client == nil {
		if s.cfg != nil && s.cfg.Env == "production" {
			return nil, fmt.Errorf("blockchain client not available")
		}
		// Return a minimal tx in non-production (e.g. tests) with valid ABI-encoded data
		tx := blockchain.UnsignedTx{
			To:    s.contractAddress,
			Value: "0",
			Data:  common.Bytes2Hex(callData),
		}
		return json.Marshal(tx)
	}

	fromAddr := common.HexToAddress(from)
	contractAddr := common.HexToAddress(s.contractAddress)

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
		To:   &contractAddr,
		Data: callData,
	})
	if err != nil {
		return nil, fmt.Errorf("gas estimation failed (transaction would likely revert): %w", err)
	}

	tx := blockchain.UnsignedTx{
		Nonce:    nonce,
		GasPrice: gasPrice.String(),
		GasLimit: gasLimit,
		To:       s.contractAddress,
		Value:    "0",
		Data:     common.Bytes2Hex(callData),
		ChainID:  s.client.ChainID().String(),
	}

	return json.Marshal(tx)
}
