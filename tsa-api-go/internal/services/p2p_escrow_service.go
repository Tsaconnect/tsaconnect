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
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// p2pEscrowABI is the subset of P2PEscrow.sol surface the backend interacts
// with. Keep this in sync with smart-contracts/contracts/P2PEscrow.sol — any
// function or event removed/renamed must be reflected here. The full ABI is
// emitted by Hardhat at artifacts/contracts/P2PEscrow.sol/P2PEscrow.json but
// inlining only what we need keeps the binding small and the cut-points
// obvious in diffs.
const p2pEscrowABI = `[
	{"inputs":[{"name":"adId","type":"bytes32"},{"name":"token","type":"address"},{"name":"amount","type":"uint256"}],"name":"createAd","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"adId","type":"bytes32"}],"name":"closeAd","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"},{"name":"adId","type":"bytes32"},{"name":"amount","type":"uint256"}],"name":"openTrade","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"}],"name":"markPaid","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"}],"name":"release","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"}],"name":"adminRelease","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"}],"name":"cancelByTimeout","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"}],"name":"raiseDispute","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"},{"name":"releaseToBuyer","type":"bool"}],"name":"adminResolve","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"adId","type":"bytes32"}],"name":"getAd","outputs":[{"name":"seller","type":"address"},{"name":"token","type":"address"},{"name":"remaining","type":"uint256"},{"name":"reserved","type":"uint256"},{"name":"createdAt","type":"uint256"},{"name":"open","type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"}],"name":"getTrade","outputs":[{"name":"adId","type":"bytes32"},{"name":"buyer","type":"address"},{"name":"amount","type":"uint256"},{"name":"createdAt","type":"uint256"},{"name":"status","type":"uint8"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"account","type":"address"}],"name":"isAdmin","outputs":[{"type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"owner","outputs":[{"type":"address"}],"stateMutability":"view","type":"function"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"adId","type":"bytes32"},{"indexed":true,"name":"seller","type":"address"},{"indexed":true,"name":"token","type":"address"},{"indexed":false,"name":"amount","type":"uint256"}],"name":"AdCreated","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":true,"name":"adId","type":"bytes32"},{"indexed":true,"name":"buyer","type":"address"},{"indexed":false,"name":"amount","type":"uint256"}],"name":"TradeOpened","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"}],"name":"TradePaid","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":true,"name":"buyer","type":"address"},{"indexed":false,"name":"amountToBuyer","type":"uint256"},{"indexed":false,"name":"feeToSystem","type":"uint256"}],"name":"TradeReleased","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":false,"name":"returnedToAd","type":"uint256"}],"name":"TradeCancelled","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":true,"name":"by","type":"address"}],"name":"TradeDisputed","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":false,"name":"releasedToBuyer","type":"bool"},{"indexed":false,"name":"amountMoved","type":"uint256"},{"indexed":false,"name":"feeToSystem","type":"uint256"}],"name":"TradeResolved","type":"event"}
]`

var parsedP2PEscrowABI abi.ABI

func init() {
	var err error
	parsedP2PEscrowABI, err = abi.JSON(strings.NewReader(p2pEscrowABI))
	if err != nil {
		panic(fmt.Sprintf("failed to parse P2PEscrow ABI: %v", err))
	}
}

// OnChainP2PTradeStatus mirrors the Status enum in P2PEscrow.sol. Stable
// numeric values — change in lock-step with the contract.
type OnChainP2PTradeStatus uint8

const (
	OnChainP2PStatusNone      OnChainP2PTradeStatus = 0
	OnChainP2PStatusPending   OnChainP2PTradeStatus = 1
	OnChainP2PStatusPaid      OnChainP2PTradeStatus = 2
	OnChainP2PStatusReleased  OnChainP2PTradeStatus = 3
	OnChainP2PStatusCancelled OnChainP2PTradeStatus = 4
	OnChainP2PStatusDisputed  OnChainP2PTradeStatus = 5
	OnChainP2PStatusResolved  OnChainP2PTradeStatus = 6
)

// OnChainP2PAd is the decoded result of getAd(adId).
type OnChainP2PAd struct {
	Seller    common.Address
	Token     common.Address
	Remaining *big.Int
	Reserved  *big.Int
	CreatedAt *big.Int
	Open      bool
	Exists    bool // false when Seller == 0x0 — i.e. ad never created
}

// OnChainP2PTrade is the decoded result of getTrade(tradeId).
type OnChainP2PTrade struct {
	AdID      [32]byte
	Buyer     common.Address
	Amount    *big.Int
	CreatedAt *big.Int
	Status    OnChainP2PTradeStatus
	Exists    bool // false when Status == None
}

// P2PTradeReleasedEvent captures the TradeReleased event payload (used by the
// chain reconciler and the release-confirm handler to surface the actual fee
// split that hit-chain, in case it differs from off-chain expectations).
type P2PTradeReleasedEvent struct {
	TradeID       [32]byte
	Buyer         common.Address
	AmountToBuyer *big.Int
	FeeToSystem   *big.Int
}

// P2PTradeOpenedEvent is parsed from openTrade receipts and confirms the
// buyer + portion that actually landed on-chain.
type P2PTradeOpenedEvent struct {
	TradeID [32]byte
	AdID    [32]byte
	Buyer   common.Address
	Amount  *big.Int
}

// P2PAdCreatedEvent is parsed from createAd receipts so the handler can
// re-verify the on-chain seller and token match the DB row before persisting.
type P2PAdCreatedEvent struct {
	AdID   [32]byte
	Seller common.Address
	Token  common.Address
	Amount *big.Int
}

// P2PEscrowService prepares unsigned transactions for the P2PEscrow contract
// and decodes its view methods + event logs. Mirrors EscrowService in shape;
// they could one day share a base via interface, but the per-contract ABI
// keeps them naturally separate.
type P2PEscrowService struct {
	client          *blockchain.EVMClient
	contractAddress string
	cfg             *config.Config
}

// NewP2PEscrowService constructs a service bound to the Sonic chain client.
// The contract address is read from Config.P2PEscrowAddress; an empty
// address is allowed (feature dormant) and surfaces as errors at call time
// rather than panicking at startup.
func NewP2PEscrowService(client *blockchain.EVMClient, cfg *config.Config) *P2PEscrowService {
	return &P2PEscrowService{
		client:          client,
		contractAddress: cfg.P2PEscrowAddress,
		cfg:             cfg,
	}
}

// ContractAddress returns the configured P2PEscrow contract address. Empty
// when the feature isn't deployed yet — handlers should treat empty as
// "P2P unavailable".
func (s *P2PEscrowService) ContractAddress() string {
	return s.contractAddress
}

// HashID converts a DB-side UUID into the bytes32 the contract uses. Stable
// and deterministic so on-chain & off-chain rows share a canonical
// identifier without storing both.
func HashID(id uuid.UUID) [32]byte {
	return crypto.Keccak256Hash(id[:])
}

// HashIDHex is the hex-with-0x form, the shape we persist in DB columns and
// pass to the frontend.
func HashIDHex(id uuid.UUID) string {
	h := HashID(id)
	return "0x" + common.Bytes2Hex(h[:])
}

// ─── Prepare* (unsigned-tx builders) ────────────────────────────────────────

// PrepareCreateAd builds the unsigned tx for createAd(adId, token, amount).
// Called AFTER the seller has approved the escrow to spend `amount` of token;
// without the approval, gas estimation reverts which is the desired failure.
func (s *P2PEscrowService) PrepareCreateAd(adId [32]byte, seller, token string, amount *big.Int) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("createAd", adId, common.HexToAddress(token), amount)
	if err != nil {
		return nil, fmt.Errorf("pack createAd: %w", err)
	}
	return s.buildUnsignedTx(seller, data)
}

func (s *P2PEscrowService) PrepareCloseAd(adId [32]byte, seller string) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("closeAd", adId)
	if err != nil {
		return nil, fmt.Errorf("pack closeAd: %w", err)
	}
	return s.buildUnsignedTx(seller, data)
}

func (s *P2PEscrowService) PrepareOpenTrade(tradeId, adId [32]byte, buyer string, amount *big.Int) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("openTrade", tradeId, adId, amount)
	if err != nil {
		return nil, fmt.Errorf("pack openTrade: %w", err)
	}
	return s.buildUnsignedTx(buyer, data)
}

func (s *P2PEscrowService) PrepareMarkPaid(tradeId [32]byte, buyer string) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("markPaid", tradeId)
	if err != nil {
		return nil, fmt.Errorf("pack markPaid: %w", err)
	}
	return s.buildUnsignedTx(buyer, data)
}

func (s *P2PEscrowService) PrepareRelease(tradeId [32]byte, seller string) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("release", tradeId)
	if err != nil {
		return nil, fmt.Errorf("pack release: %w", err)
	}
	return s.buildUnsignedTx(seller, data)
}

// PrepareAdminRelease is the webhook entry point used by Phase 2. The caller
// is the operator EOA (registered as admin via setAdmin). Phase 1 doesn't
// invoke this from a webhook, but the service surface ships now so we don't
// have to revisit ABI parity later.
func (s *P2PEscrowService) PrepareAdminRelease(tradeId [32]byte, admin string) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("adminRelease", tradeId)
	if err != nil {
		return nil, fmt.Errorf("pack adminRelease: %w", err)
	}
	return s.buildUnsignedTx(admin, data)
}

func (s *P2PEscrowService) PrepareCancelByTimeout(tradeId [32]byte, caller string) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("cancelByTimeout", tradeId)
	if err != nil {
		return nil, fmt.Errorf("pack cancelByTimeout: %w", err)
	}
	return s.buildUnsignedTx(caller, data)
}

func (s *P2PEscrowService) PrepareRaiseDispute(tradeId [32]byte, by string) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("raiseDispute", tradeId)
	if err != nil {
		return nil, fmt.Errorf("pack raiseDispute: %w", err)
	}
	return s.buildUnsignedTx(by, data)
}

func (s *P2PEscrowService) PrepareAdminResolve(tradeId [32]byte, refundBuyer bool, admin string) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("adminResolve", tradeId, refundBuyer)
	if err != nil {
		return nil, fmt.Errorf("pack adminResolve: %w", err)
	}
	return s.buildUnsignedTx(admin, data)
}

// ─── Read* (view calls) ─────────────────────────────────────────────────────

// ReadAd returns the on-chain Ad record. Exists=false when the ad slot is
// empty (seller == zero address).
func (s *P2PEscrowService) ReadAd(adId [32]byte) (*OnChainP2PAd, error) {
	if err := s.requireConfigured(); err != nil {
		return nil, err
	}
	data, err := parsedP2PEscrowABI.Pack("getAd", adId)
	if err != nil {
		return nil, fmt.Errorf("pack getAd: %w", err)
	}
	out, err := s.callContract(data)
	if err != nil {
		return nil, err
	}
	vals, err := parsedP2PEscrowABI.Unpack("getAd", out)
	if err != nil || len(vals) != 6 {
		return nil, fmt.Errorf("decode getAd: %w", err)
	}
	ad := &OnChainP2PAd{
		Seller:    vals[0].(common.Address),
		Token:     vals[1].(common.Address),
		Remaining: vals[2].(*big.Int),
		Reserved:  vals[3].(*big.Int),
		CreatedAt: vals[4].(*big.Int),
		Open:      vals[5].(bool),
	}
	ad.Exists = ad.Seller != (common.Address{})
	return ad, nil
}

// ReadTrade returns the on-chain Trade record. Exists=false when the trade
// slot is empty (Status == None).
func (s *P2PEscrowService) ReadTrade(tradeId [32]byte) (*OnChainP2PTrade, error) {
	if err := s.requireConfigured(); err != nil {
		return nil, err
	}
	data, err := parsedP2PEscrowABI.Pack("getTrade", tradeId)
	if err != nil {
		return nil, fmt.Errorf("pack getTrade: %w", err)
	}
	out, err := s.callContract(data)
	if err != nil {
		return nil, err
	}
	vals, err := parsedP2PEscrowABI.Unpack("getTrade", out)
	if err != nil || len(vals) != 5 {
		return nil, fmt.Errorf("decode getTrade: %w", err)
	}
	t := &OnChainP2PTrade{
		AdID:      vals[0].([32]byte),
		Buyer:     vals[1].(common.Address),
		Amount:    vals[2].(*big.Int),
		CreatedAt: vals[3].(*big.Int),
		Status:    OnChainP2PTradeStatus(vals[4].(uint8)),
	}
	t.Exists = t.Status != OnChainP2PStatusNone
	return t, nil
}

// IsAdmin reports whether the given account is the contract's owner or in
// the admins mapping. Useful at handler-time to verify the operator address
// before issuing adminRelease.
func (s *P2PEscrowService) IsAdmin(account string) (bool, error) {
	if err := s.requireConfigured(); err != nil {
		return false, err
	}
	data, err := parsedP2PEscrowABI.Pack("isAdmin", common.HexToAddress(account))
	if err != nil {
		return false, fmt.Errorf("pack isAdmin: %w", err)
	}
	out, err := s.callContract(data)
	if err != nil {
		return false, err
	}
	vals, err := parsedP2PEscrowABI.Unpack("isAdmin", out)
	if err != nil || len(vals) == 0 {
		return false, fmt.Errorf("decode isAdmin: %w", err)
	}
	return vals[0].(bool), nil
}

// Owner returns the contract owner — logged at startup for operator
// sanity-checking the right deployment is wired up.
func (s *P2PEscrowService) Owner() (string, error) {
	if err := s.requireConfigured(); err != nil {
		return "", err
	}
	data, err := parsedP2PEscrowABI.Pack("owner")
	if err != nil {
		return "", fmt.Errorf("pack owner: %w", err)
	}
	out, err := s.callContract(data)
	if err != nil {
		return "", err
	}
	vals, err := parsedP2PEscrowABI.Unpack("owner", out)
	if err != nil || len(vals) == 0 {
		return "", fmt.Errorf("decode owner: %w", err)
	}
	return vals[0].(common.Address).Hex(), nil
}

// ─── Verify* (parse receipts) ───────────────────────────────────────────────

// VerifyAdCreated reads the receipt for the supplied tx hash and parses the
// AdCreated event with the expected adId. Returns the seller, token, and
// amount that actually landed on-chain so the handler can sanity-check them
// against the off-chain DB row (defence in depth — under normal operation
// these always match because the adId is keccak-derived from a server-owned
// UUID, but a richer return makes a future bug observable rather than silent).
func (s *P2PEscrowService) VerifyAdCreated(txHash string, expectedAdId [32]byte) (*P2PAdCreatedEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseAdCreatedFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedAdId)
}

// VerifyTradeOpened mirrors VerifyAdCreated for openTrade flows.
func (s *P2PEscrowService) VerifyTradeOpened(txHash string, expectedTradeId [32]byte) (*P2PTradeOpenedEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseTradeOpenedFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedTradeId)
}

// VerifyTradeReleased confirms a release/adminRelease/adminResolve(true)
// receipt and returns the parsed payment split. Used by the release-confirm
// path to record the actual fee that hit the system wallet (in case fee
// policy changes mid-flight).
func (s *P2PEscrowService) VerifyTradeReleased(txHash string, expectedTradeId [32]byte) (*P2PTradeReleasedEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseTradeReleasedFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedTradeId)
}

// ─── Receipt parsers (testable without an RPC client) ──────────────────────

// parseAdCreatedFromReceipt scans logs for an AdCreated event matching the
// expected adId emitted from `contractAddr`. Pure function — synthetic
// receipts let us unit-test the topic/data layout without standing up a chain.
func parseAdCreatedFromReceipt(receipt *types.Receipt, contractAddr common.Address, expectedAdId [32]byte) (*P2PAdCreatedEvent, error) {
	sigID := parsedP2PEscrowABI.Events["AdCreated"].ID
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 4 || lg.Topics[0] != sigID {
			continue
		}
		var gotAd [32]byte
		copy(gotAd[:], lg.Topics[1].Bytes())
		if gotAd != expectedAdId {
			continue
		}
		seller := common.BytesToAddress(lg.Topics[2].Bytes())
		token := common.BytesToAddress(lg.Topics[3].Bytes())
		nonIndexed, err := parsedP2PEscrowABI.Events["AdCreated"].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 1 {
			return nil, fmt.Errorf("decode AdCreated data: %w", err)
		}
		return &P2PAdCreatedEvent{
			AdID:   gotAd,
			Seller: seller,
			Token:  token,
			Amount: nonIndexed[0].(*big.Int),
		}, nil
	}
	return nil, fmt.Errorf("AdCreated event for adId 0x%s not found in receipt", common.Bytes2Hex(expectedAdId[:]))
}

func parseTradeOpenedFromReceipt(receipt *types.Receipt, contractAddr common.Address, expectedTradeId [32]byte) (*P2PTradeOpenedEvent, error) {
	sigID := parsedP2PEscrowABI.Events["TradeOpened"].ID
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 4 || lg.Topics[0] != sigID {
			continue
		}
		var gotTrade [32]byte
		copy(gotTrade[:], lg.Topics[1].Bytes())
		if gotTrade != expectedTradeId {
			continue
		}
		var gotAd [32]byte
		copy(gotAd[:], lg.Topics[2].Bytes())
		buyer := common.BytesToAddress(lg.Topics[3].Bytes())
		nonIndexed, err := parsedP2PEscrowABI.Events["TradeOpened"].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 1 {
			return nil, fmt.Errorf("decode TradeOpened data: %w", err)
		}
		return &P2PTradeOpenedEvent{
			TradeID: gotTrade,
			AdID:    gotAd,
			Buyer:   buyer,
			Amount:  nonIndexed[0].(*big.Int),
		}, nil
	}
	return nil, fmt.Errorf("TradeOpened event for tradeId 0x%s not found in receipt", common.Bytes2Hex(expectedTradeId[:]))
}

func parseTradeReleasedFromReceipt(receipt *types.Receipt, contractAddr common.Address, expectedTradeId [32]byte) (*P2PTradeReleasedEvent, error) {
	sigID := parsedP2PEscrowABI.Events["TradeReleased"].ID
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 3 || lg.Topics[0] != sigID {
			continue
		}
		var got [32]byte
		copy(got[:], lg.Topics[1].Bytes())
		if got != expectedTradeId {
			continue
		}
		buyer := common.BytesToAddress(lg.Topics[2].Bytes())
		nonIndexed, err := parsedP2PEscrowABI.Events["TradeReleased"].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 2 {
			return nil, fmt.Errorf("decode TradeReleased data: %w", err)
		}
		return &P2PTradeReleasedEvent{
			TradeID:       got,
			Buyer:         buyer,
			AmountToBuyer: nonIndexed[0].(*big.Int),
			FeeToSystem:   nonIndexed[1].(*big.Int),
		}, nil
	}
	return nil, fmt.Errorf("TradeReleased event for tradeId 0x%s not found in receipt", common.Bytes2Hex(expectedTradeId[:]))
}

// ─── Internals ──────────────────────────────────────────────────────────────

func (s *P2PEscrowService) requireConfigured() error {
	if s.contractAddress == "" {
		return fmt.Errorf("P2PEscrow contract address not configured")
	}
	if s.client == nil {
		return fmt.Errorf("blockchain client not available")
	}
	return nil
}

func (s *P2PEscrowService) callContract(data []byte) ([]byte, error) {
	addr := common.HexToAddress(s.contractAddress)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.client.Client.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: data}, nil)
}

func (s *P2PEscrowService) fetchReceiptAndCheckOK(txHash string) (*types.Receipt, error) {
	if err := s.requireConfigured(); err != nil {
		return nil, err
	}
	receipt, err := s.client.GetTransactionReceipt(txHash)
	if err != nil {
		return nil, fmt.Errorf("get receipt: %w", err)
	}
	if receipt.Status != 1 {
		return nil, fmt.Errorf("transaction reverted on chain")
	}
	return receipt, nil
}

// buildUnsignedTx mirrors EscrowService.buildUnsignedTx — populates nonce,
// gas price, and gas limit when a chain client is available. In dev without
// a client we fall back to a partial JSON so handlers can return something
// useful to local tooling; production refuses.
func (s *P2PEscrowService) buildUnsignedTx(from string, callData []byte) ([]byte, error) {
	if s.contractAddress == "" {
		return nil, fmt.Errorf("P2PEscrow contract address not configured")
	}

	if s.client == nil {
		// Fail-closed for any env that isn't an explicit dev allowlist —
		// otherwise a misconfigured staging deploy silently emits partial
		// txs instead of erroring.
		if s.cfg == nil || s.cfg.Env != "development" {
			return nil, fmt.Errorf("blockchain client not available")
		}
		tx := blockchain.UnsignedTx{
			To:    s.contractAddress,
			Value: "0",
			Data:  common.Bytes2Hex(callData),
		}
		return json.Marshal(tx)
	}

	fromAddr := common.HexToAddress(from)
	toAddr := common.HexToAddress(s.contractAddress)

	nonce, err := s.client.GetNonce(from)
	if err != nil {
		return nil, fmt.Errorf("get nonce: %w", err)
	}
	gasPrice, err := s.client.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("suggest gas price: %w", err)
	}
	gasLimit, err := s.client.EstimateGas(ethereum.CallMsg{
		From: fromAddr,
		To:   &toAddr,
		Data: callData,
	})
	if err != nil {
		return nil, fmt.Errorf("estimate gas (would revert): %w", err)
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
