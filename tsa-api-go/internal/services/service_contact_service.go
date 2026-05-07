package services

import (
	"encoding/json"
	"fmt"
	"log"
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
//
// Verifies the configured contract has bytecode on the connected chain at
// startup. A misconfigured SERVICE_CONTACT_ADDRESS (e.g. a testnet address
// behind a mainnet RPC) would otherwise let the BE happily prepare TXs that
// hit an empty EOA — the call data gets ignored, no events emitted,
// transaction status=success, and users' funds are spent on what is
// effectively a no-op.
func NewServiceContactService(client *blockchain.EVMClient, cfg *config.Config) *ServiceContactService {
	if client != nil && cfg.ServiceContractAddress != "" {
		code, err := client.GetCode(cfg.ServiceContractAddress)
		if err != nil {
			log.Printf("[ServiceContact] WARNING: could not verify contract bytecode at %s: %v", cfg.ServiceContractAddress, err)
		} else if len(code) == 0 {
			log.Printf("[ServiceContact] FATAL CONFIG ERROR: SERVICE_CONTACT_ADDRESS %s has NO bytecode on chain %s. "+
				"Calls to this address would succeed with no effect (funds spent on a no-op). "+
				"Either deploy ServiceContact to this chain or point the service at the chain where it exists.",
				cfg.ServiceContractAddress, client.ChainID().String())
		} else {
			log.Printf("[ServiceContact] verified contract %s has %d bytes of bytecode on chain %s",
				cfg.ServiceContractAddress, len(code), client.ChainID().String())
		}
	}
	return &ServiceContactService{
		client:          client,
		contractAddress: cfg.ServiceContractAddress,
		cfg:             cfg,
	}
}

// GetFeeAmount returns the contact fee in the smallest token unit.
// The on-chain ServiceContact uses a single global `feeAmount = 100000`
// sized for 6-decimal stablecoins ($0.10 in USDC/USDT). 18-decimal tokens
// like MCGP are not supported by this contract design — adding them would
// transfer dust through the splits. Don't change this without a contract
// redesign that derives per-token fees from on-chain prices.
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

	for _, l := range receipt.Logs {
		if l.Address != contractAddr {
			continue
		}
		if len(l.Topics) < 4 || l.Topics[0] != contactFeePaidSig {
			continue
		}

		// Topics: [0]=sig, [1]=caller(indexed), [2]=serviceProvider(indexed), [3]=token(indexed)
		caller := common.BytesToAddress(l.Topics[1].Bytes())
		provider := common.BytesToAddress(l.Topics[2].Bytes())
		token := common.BytesToAddress(l.Topics[3].Bytes())

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

	// Diagnostic dump: when the event is missing, log every log entry we *did*
	// see so operators can spot contract-address or ABI drift. This is the
	// first thing to check when a tx succeeds on-chain but verification fails.
	log.Printf("[ContactFee] verification failed for tx=%s expected_contract=%s expected_sig=%s",
		txHash, contractAddr.Hex(), contactFeePaidSig.Hex())
	for i, l := range receipt.Logs {
		topic0 := "<none>"
		if len(l.Topics) > 0 {
			topic0 = l.Topics[0].Hex()
		}
		log.Printf("[ContactFee]   log[%d] addr=%s topic0=%s topics=%d", i, l.Address.Hex(), topic0, len(l.Topics))
	}
	return nil, fmt.Errorf("ContactFeePaid event not found in receipt (logs=%d, expected contract %s)", len(receipt.Logs), contractAddr.Hex())
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

	// Gas estimation will revert with "ERC20: transfer amount exceeds
	// allowance" because the approve TX from this same prepare call hasn't
	// been broadcast yet — that's expected. Fall back to a conservative
	// hardcoded limit; payContactFee does ~4 transfers + a few SLOADs and
	// fits comfortably under 200k. Other revert reasons (provider == caller,
	// wrong token, paused) will surface when the user actually broadcasts.
	gasLimit, err := s.client.EstimateGas(ethereum.CallMsg{
		From: fromAddr,
		To:   &contractAddr,
		Data: callData,
	})
	if err != nil {
		log.Printf("[ServiceContact] EstimateGas failed (likely pre-approve allowance check) — falling back to 250000: %v", err)
		gasLimit = 250000
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
