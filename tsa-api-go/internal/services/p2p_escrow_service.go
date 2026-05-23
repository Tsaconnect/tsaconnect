package services

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
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// p2pEscrowABI mirrors the deposit-once / EIP-712 voucher P2PEscrow surface.
// Keep in sync with smart-contracts/contracts/P2PEscrow.sol — any function
// or event added/renamed/removed must be reflected here. The full ABI is
// emitted by Hardhat at artifacts/contracts/P2PEscrow.sol/P2PEscrow.json;
// inlining only the symbols the backend uses keeps the binding small.
const p2pEscrowABI = `[
	{"inputs":[{"name":"token","type":"address"},{"name":"amount","type":"uint256"}],"name":"deposit","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"token","type":"address"},{"name":"amount","type":"uint256"}],"name":"withdraw","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"components":[{"name":"seller","type":"address"},{"name":"token","type":"address"},{"name":"maxAmount","type":"uint256"},{"name":"minPerTrade","type":"uint256"},{"name":"maxPerTrade","type":"uint256"},{"name":"deadline","type":"uint256"},{"name":"nonce","type":"bytes32"}],"name":"v","type":"tuple"}],"name":"cancelVoucher","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"},{"components":[{"name":"seller","type":"address"},{"name":"token","type":"address"},{"name":"maxAmount","type":"uint256"},{"name":"minPerTrade","type":"uint256"},{"name":"maxPerTrade","type":"uint256"},{"name":"deadline","type":"uint256"},{"name":"nonce","type":"bytes32"}],"name":"v","type":"tuple"},{"name":"sig","type":"bytes"},{"name":"amount","type":"uint256"}],"name":"openTrade","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"}],"name":"markPaid","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"}],"name":"release","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"}],"name":"adminRelease","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"}],"name":"cancelByTimeout","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"}],"name":"raiseDispute","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"},{"name":"releaseToBuyer","type":"bool"}],"name":"adminResolve","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"seller","type":"address"},{"name":"token","type":"address"}],"name":"balanceOf","outputs":[{"name":"_available","type":"uint256"},{"name":"_locked","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"","type":"bytes32"}],"name":"voucherFilled","outputs":[{"type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"","type":"bytes32"}],"name":"voucherCancelled","outputs":[{"type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"}],"name":"getTrade","outputs":[{"name":"seller","type":"address"},{"name":"buyer","type":"address"},{"name":"token","type":"address"},{"name":"amount","type":"uint256"},{"name":"createdAt","type":"uint256"},{"name":"status","type":"uint8"},{"name":"voucherHash","type":"bytes32"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"account","type":"address"}],"name":"isAdmin","outputs":[{"type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"owner","outputs":[{"type":"address"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"components":[{"name":"seller","type":"address"},{"name":"token","type":"address"},{"name":"maxAmount","type":"uint256"},{"name":"minPerTrade","type":"uint256"},{"name":"maxPerTrade","type":"uint256"},{"name":"deadline","type":"uint256"},{"name":"nonce","type":"bytes32"}],"name":"v","type":"tuple"}],"name":"hashVoucher","outputs":[{"type":"bytes32"}],"stateMutability":"view","type":"function"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"seller","type":"address"},{"indexed":true,"name":"token","type":"address"},{"indexed":false,"name":"amount","type":"uint256"}],"name":"Deposited","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"seller","type":"address"},{"indexed":true,"name":"token","type":"address"},{"indexed":false,"name":"amount","type":"uint256"}],"name":"Withdrawn","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"voucherHash","type":"bytes32"},{"indexed":true,"name":"seller","type":"address"}],"name":"VoucherCancelled","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":true,"name":"voucherHash","type":"bytes32"},{"indexed":true,"name":"buyer","type":"address"},{"indexed":false,"name":"seller","type":"address"},{"indexed":false,"name":"token","type":"address"},{"indexed":false,"name":"amount","type":"uint256"}],"name":"TradeOpened","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"}],"name":"TradePaid","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":true,"name":"buyer","type":"address"},{"indexed":false,"name":"amountToBuyer","type":"uint256"},{"indexed":false,"name":"feeToSystem","type":"uint256"}],"name":"TradeReleased","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":false,"name":"returnedToAvailable","type":"uint256"}],"name":"TradeCancelled","type":"event"},
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

// AdVoucher mirrors the EIP-712 struct defined in P2PEscrow.sol. Built by
// the backend at ad-creation time, signed off-chain by the seller, and
// presented by the buyer on every openTrade call.
//
// `Nonce` is server-generated (random bytes32); the EIP-712 digest of the
// voucher serves as its on-chain identity. The same voucher can be filled
// up to MaxAmount across many trades (bounded per-trade by Min/MaxPerTrade
// when non-zero).
type AdVoucher struct {
	Seller       common.Address `abi:"seller"`
	Token        common.Address `abi:"token"`
	MaxAmount    *big.Int       `abi:"maxAmount"`
	MinPerTrade  *big.Int       `abi:"minPerTrade"`
	MaxPerTrade  *big.Int       `abi:"maxPerTrade"`
	Deadline     *big.Int       `abi:"deadline"`
	Nonce        [32]byte       `abi:"nonce"`
}

// OnChainP2PTrade is the decoded result of getTrade(tradeId) under the pool
// model. Trades are self-contained — no ad pointer because ads live entirely
// off-chain as signed vouchers.
type OnChainP2PTrade struct {
	Seller      common.Address
	Buyer       common.Address
	Token       common.Address
	Amount      *big.Int
	CreatedAt   *big.Int
	Status      OnChainP2PTradeStatus
	VoucherHash [32]byte
	Exists      bool // false when Status == None
}

// OnChainBalance is the decoded result of balanceOf(seller, token).
type OnChainBalance struct {
	Available *big.Int
	Locked    *big.Int
}

// P2PDepositedEvent captures the Deposited event emitted when a seller tops
// up their per-(seller, token) available balance.
type P2PDepositedEvent struct {
	Seller common.Address
	Token  common.Address
	Amount *big.Int
}

// P2PWithdrawnEvent captures the Withdrawn event from seller withdraws.
type P2PWithdrawnEvent struct {
	Seller common.Address
	Token  common.Address
	Amount *big.Int
}

// P2PVoucherCancelledEvent captures the VoucherCancelled event.
type P2PVoucherCancelledEvent struct {
	VoucherHash [32]byte
	Seller      common.Address
}

// P2PTradeOpenedEvent is parsed from openTrade receipts under the pool model
// — carries voucherHash + seller + token (no adId).
type P2PTradeOpenedEvent struct {
	TradeID     [32]byte
	VoucherHash [32]byte
	Buyer       common.Address
	Seller      common.Address
	Token       common.Address
	Amount      *big.Int
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

// P2PTradeCancelledEvent captures the TradeCancelled event payload — emitted
// by `cancelByTimeout`. Under the pool model the funds return to the seller's
// `available` pool (not to a specific ad's remaining).
type P2PTradeCancelledEvent struct {
	TradeID             [32]byte
	ReturnedToAvailable *big.Int
}

// P2PTradeDisputedEvent captures TradeDisputed — emitted by raiseDispute.
type P2PTradeDisputedEvent struct {
	TradeID [32]byte
	By      common.Address
}

// P2PTradeResolvedEvent captures TradeResolved — emitted by adminResolve.
type P2PTradeResolvedEvent struct {
	TradeID         [32]byte
	ReleasedToBuyer bool
	AmountMoved     *big.Int
	FeeToSystem     *big.Int
}

// P2PAdCreatedEvent is the legacy event from the pre-upgrade lock-on-publish
// model. Retained so older code paths still compile during the Phase 2B
// migration; new flows never emit or parse it.
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

// ErrPhase2Migration is returned by legacy lock-on-publish methods that no
// longer have a corresponding on-chain function. All adapter call sites
// were migrated in Phase 2B; the stubs persist only so the interface still
// references the legacy method names should anything outside the trade
// service have a stale binding.
var ErrPhase2Migration = errors.New("p2p: legacy method removed by pool-model upgrade")

// PrepareCreateAd is no longer supported — ads are off-chain vouchers under
// the pool model. The trade service's ad-creation path must be refactored
// to call SignVoucher / PrepareDeposit instead. Returns ErrPhase2Migration
// to surface mid-migration call sites loudly.
func (s *P2PEscrowService) PrepareCreateAd(adId [32]byte, seller, token string, amount *big.Int) ([]byte, error) {
	return nil, ErrPhase2Migration
}

// PrepareCloseAd is no longer supported — sellers revoke an ad off-chain
// by calling cancelVoucher with the signed voucher. Returns ErrPhase2Migration.
func (s *P2PEscrowService) PrepareCloseAd(adId [32]byte, seller string) ([]byte, error) {
	return nil, ErrPhase2Migration
}

// PrepareOpenTrade is the legacy (adId-based) entry point. The pool model
// requires the buyer to present the seller's signed voucher; the new flow
// is `PrepareOpenTradeWithVoucher`. Returns ErrPhase2Migration.
func (s *P2PEscrowService) PrepareOpenTrade(tradeId, adId [32]byte, buyer string, amount *big.Int) ([]byte, error) {
	return nil, ErrPhase2Migration
}

// PrepareDeposit builds the unsigned tx for `deposit(token, amount)`. The
// seller must have ERC20-approved this contract for `amount` first;
// without the approval, gas estimation reverts (which is the desired failure).
func (s *P2PEscrowService) PrepareDeposit(seller, token string, amount *big.Int) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("deposit", common.HexToAddress(token), amount)
	if err != nil {
		return nil, fmt.Errorf("pack deposit: %w", err)
	}
	return s.buildUnsignedTx(seller, data)
}

// PrepareWithdraw builds the unsigned tx for `withdraw(token, amount)`. The
// withdrawer must have `amount` available (not locked against a trade).
func (s *P2PEscrowService) PrepareWithdraw(seller, token string, amount *big.Int) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("withdraw", common.HexToAddress(token), amount)
	if err != nil {
		return nil, fmt.Errorf("pack withdraw: %w", err)
	}
	return s.buildUnsignedTx(seller, data)
}

// PrepareCancelVoucher builds the unsigned tx for `cancelVoucher(voucher)`.
// Only the voucher's seller can submit this — the on-chain check enforces it.
func (s *P2PEscrowService) PrepareCancelVoucher(seller string, voucher AdVoucher) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("cancelVoucher", voucher)
	if err != nil {
		return nil, fmt.Errorf("pack cancelVoucher: %w", err)
	}
	return s.buildUnsignedTx(seller, data)
}

// PrepareOpenTradeWithVoucher builds the unsigned tx the buyer broadcasts to
// open a trade against a seller's signed voucher. `sig` is the seller's
// 65-byte EIP-712 signature over the voucher.
func (s *P2PEscrowService) PrepareOpenTradeWithVoucher(tradeId [32]byte, voucher AdVoucher, sig []byte, buyer string, amount *big.Int) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("openTrade", tradeId, voucher, sig, amount)
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

// ReadBalance returns the (available, locked) tuple for a (seller, token) in
// the pool. Available is withdrawable; locked is reserved against open trades.
func (s *P2PEscrowService) ReadBalance(seller, token string) (*OnChainBalance, error) {
	if err := s.requireConfigured(); err != nil {
		return nil, err
	}
	data, err := parsedP2PEscrowABI.Pack("balanceOf", common.HexToAddress(seller), common.HexToAddress(token))
	if err != nil {
		return nil, fmt.Errorf("pack balanceOf: %w", err)
	}
	out, err := s.callContract(data)
	if err != nil {
		return nil, err
	}
	vals, err := parsedP2PEscrowABI.Unpack("balanceOf", out)
	if err != nil || len(vals) != 2 {
		return nil, fmt.Errorf("decode balanceOf: %w", err)
	}
	return &OnChainBalance{
		Available: vals[0].(*big.Int),
		Locked:    vals[1].(*big.Int),
	}, nil
}

// ReadVoucherFilled returns the cumulative amount filled against a voucher
// (by its EIP-712 digest). Tells the backend how much of the voucher's
// maxAmount remains available before reverting at "Exceeds voucher capacity".
func (s *P2PEscrowService) ReadVoucherFilled(voucherHash [32]byte) (*big.Int, error) {
	if err := s.requireConfigured(); err != nil {
		return nil, err
	}
	data, err := parsedP2PEscrowABI.Pack("voucherFilled", voucherHash)
	if err != nil {
		return nil, fmt.Errorf("pack voucherFilled: %w", err)
	}
	out, err := s.callContract(data)
	if err != nil {
		return nil, err
	}
	vals, err := parsedP2PEscrowABI.Unpack("voucherFilled", out)
	if err != nil || len(vals) == 0 {
		return nil, fmt.Errorf("decode voucherFilled: %w", err)
	}
	return vals[0].(*big.Int), nil
}

// ReadVoucherCancelled returns whether a voucher has been revoked by its
// seller via cancelVoucher.
func (s *P2PEscrowService) ReadVoucherCancelled(voucherHash [32]byte) (bool, error) {
	if err := s.requireConfigured(); err != nil {
		return false, err
	}
	data, err := parsedP2PEscrowABI.Pack("voucherCancelled", voucherHash)
	if err != nil {
		return false, fmt.Errorf("pack voucherCancelled: %w", err)
	}
	out, err := s.callContract(data)
	if err != nil {
		return false, err
	}
	vals, err := parsedP2PEscrowABI.Unpack("voucherCancelled", out)
	if err != nil || len(vals) == 0 {
		return false, fmt.Errorf("decode voucherCancelled: %w", err)
	}
	return vals[0].(bool), nil
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
	if err != nil || len(vals) != 7 {
		return nil, fmt.Errorf("decode getTrade: %w", err)
	}
	t := &OnChainP2PTrade{
		Seller:      vals[0].(common.Address),
		Buyer:       vals[1].(common.Address),
		Token:       vals[2].(common.Address),
		Amount:      vals[3].(*big.Int),
		CreatedAt:   vals[4].(*big.Int),
		Status:      OnChainP2PTradeStatus(vals[5].(uint8)),
		VoucherHash: vals[6].([32]byte),
	}
	t.Exists = t.Status != OnChainP2PStatusNone
	return t, nil
}

// HashVoucher delegates to the contract's `hashVoucher` view to compute the
// EIP-712 digest. Used at ad-signing time so the backend stores the canonical
// hash without re-implementing the EIP-712 domain locally.
func (s *P2PEscrowService) HashVoucher(v AdVoucher) ([32]byte, error) {
	var zero [32]byte
	if err := s.requireConfigured(); err != nil {
		return zero, err
	}
	data, err := parsedP2PEscrowABI.Pack("hashVoucher", v)
	if err != nil {
		return zero, fmt.Errorf("pack hashVoucher: %w", err)
	}
	out, err := s.callContract(data)
	if err != nil {
		return zero, err
	}
	vals, err := parsedP2PEscrowABI.Unpack("hashVoucher", out)
	if err != nil || len(vals) == 0 {
		return zero, fmt.Errorf("decode hashVoucher: %w", err)
	}
	return vals[0].([32]byte), nil
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

// VerifyAdCreated is the legacy entry point — `AdCreated` no longer exists
// on chain (ads are off-chain vouchers). Returns ErrPhase2Migration so any
// remaining call site surfaces loudly during the Phase 2B refactor.
func (s *P2PEscrowService) VerifyAdCreated(txHash string, expectedAdId [32]byte) (*P2PAdCreatedEvent, error) {
	return nil, ErrPhase2Migration
}

// VerifyDeposited parses a Deposited event for the supplied tx hash. Used by
// the deposit-confirm path to credit the seller's available balance row.
func (s *P2PEscrowService) VerifyDeposited(txHash string, expectedSeller, expectedToken common.Address) (*P2PDepositedEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseDepositedFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedSeller, expectedToken)
}

// VerifyWithdrawn mirrors VerifyDeposited for the withdraw flow.
func (s *P2PEscrowService) VerifyWithdrawn(txHash string, expectedSeller, expectedToken common.Address) (*P2PWithdrawnEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseWithdrawnFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedSeller, expectedToken)
}

// VerifyVoucherCancelled parses VoucherCancelled and returns the voucher
// hash + seller that revoked.
func (s *P2PEscrowService) VerifyVoucherCancelled(txHash string, expectedVoucherHash [32]byte) (*P2PVoucherCancelledEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseVoucherCancelledFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedVoucherHash)
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

// VerifyTradeCancelled parses the TradeCancelled event for the supplied tx
// hash. Returned amount must match the trade's TokenAmount; the confirm-cancel
// path uses it to add the funds back to ad.remaining.
func (s *P2PEscrowService) VerifyTradeCancelled(txHash string, expectedTradeId [32]byte) (*P2PTradeCancelledEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseTradeCancelledFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedTradeId)
}

// VerifyTradeDisputed parses TradeDisputed and returns the raising party.
func (s *P2PEscrowService) VerifyTradeDisputed(txHash string, expectedTradeId [32]byte) (*P2PTradeDisputedEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseTradeDisputedFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedTradeId)
}

// VerifyTradeResolved parses TradeResolved (emitted by adminResolve).
func (s *P2PEscrowService) VerifyTradeResolved(txHash string, expectedTradeId [32]byte) (*P2PTradeResolvedEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseTradeResolvedFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedTradeId)
}

// ─── Receipt parsers (testable without an RPC client) ──────────────────────

// parseAdCreatedFromReceipt scans logs for an AdCreated event matching the
// expected adId emitted from `contractAddr`. Pure function — synthetic
// receipts let us unit-test the topic/data layout without standing up a chain.
// parseDepositedFromReceipt scans logs for a Deposited event matching the
// expected (seller, token) pair emitted from `contractAddr`.
func parseDepositedFromReceipt(receipt *types.Receipt, contractAddr, expectedSeller, expectedToken common.Address) (*P2PDepositedEvent, error) {
	sigID := parsedP2PEscrowABI.Events["Deposited"].ID
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 3 || lg.Topics[0] != sigID {
			continue
		}
		seller := common.BytesToAddress(lg.Topics[1].Bytes())
		token := common.BytesToAddress(lg.Topics[2].Bytes())
		if seller != expectedSeller || token != expectedToken {
			continue
		}
		nonIndexed, err := parsedP2PEscrowABI.Events["Deposited"].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 1 {
			return nil, fmt.Errorf("decode Deposited data: %w", err)
		}
		return &P2PDepositedEvent{
			Seller: seller,
			Token:  token,
			Amount: nonIndexed[0].(*big.Int),
		}, nil
	}
	return nil, fmt.Errorf("Deposited event for seller %s token %s not found in receipt", expectedSeller.Hex(), expectedToken.Hex())
}

// parseWithdrawnFromReceipt mirrors parseDepositedFromReceipt for the
// Withdrawn event (same indexed-fields layout).
func parseWithdrawnFromReceipt(receipt *types.Receipt, contractAddr, expectedSeller, expectedToken common.Address) (*P2PWithdrawnEvent, error) {
	sigID := parsedP2PEscrowABI.Events["Withdrawn"].ID
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 3 || lg.Topics[0] != sigID {
			continue
		}
		seller := common.BytesToAddress(lg.Topics[1].Bytes())
		token := common.BytesToAddress(lg.Topics[2].Bytes())
		if seller != expectedSeller || token != expectedToken {
			continue
		}
		nonIndexed, err := parsedP2PEscrowABI.Events["Withdrawn"].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 1 {
			return nil, fmt.Errorf("decode Withdrawn data: %w", err)
		}
		return &P2PWithdrawnEvent{
			Seller: seller,
			Token:  token,
			Amount: nonIndexed[0].(*big.Int),
		}, nil
	}
	return nil, fmt.Errorf("Withdrawn event for seller %s token %s not found in receipt", expectedSeller.Hex(), expectedToken.Hex())
}

// parseVoucherCancelledFromReceipt scans for the VoucherCancelled event
// emitted by cancelVoucher. Both fields are indexed so the entire payload
// lives in topics.
func parseVoucherCancelledFromReceipt(receipt *types.Receipt, contractAddr common.Address, expectedVoucherHash [32]byte) (*P2PVoucherCancelledEvent, error) {
	sigID := parsedP2PEscrowABI.Events["VoucherCancelled"].ID
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 3 || lg.Topics[0] != sigID {
			continue
		}
		var gotHash [32]byte
		copy(gotHash[:], lg.Topics[1].Bytes())
		if gotHash != expectedVoucherHash {
			continue
		}
		seller := common.BytesToAddress(lg.Topics[2].Bytes())
		return &P2PVoucherCancelledEvent{VoucherHash: gotHash, Seller: seller}, nil
	}
	return nil, fmt.Errorf("VoucherCancelled event for voucher 0x%s not found in receipt", common.Bytes2Hex(expectedVoucherHash[:]))
}

func parseTradeOpenedFromReceipt(receipt *types.Receipt, contractAddr common.Address, expectedTradeId [32]byte) (*P2PTradeOpenedEvent, error) {
	sigID := parsedP2PEscrowABI.Events["TradeOpened"].ID
	for _, lg := range receipt.Logs {
		// 4 topics = sig + indexed(tradeId, voucherHash, buyer)
		if lg.Address != contractAddr || len(lg.Topics) < 4 || lg.Topics[0] != sigID {
			continue
		}
		var gotTrade [32]byte
		copy(gotTrade[:], lg.Topics[1].Bytes())
		if gotTrade != expectedTradeId {
			continue
		}
		var gotVoucher [32]byte
		copy(gotVoucher[:], lg.Topics[2].Bytes())
		buyer := common.BytesToAddress(lg.Topics[3].Bytes())
		// Non-indexed fields in declared order: seller, token, amount.
		nonIndexed, err := parsedP2PEscrowABI.Events["TradeOpened"].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 3 {
			return nil, fmt.Errorf("decode TradeOpened data: %w", err)
		}
		return &P2PTradeOpenedEvent{
			TradeID:     gotTrade,
			VoucherHash: gotVoucher,
			Buyer:       buyer,
			Seller:      nonIndexed[0].(common.Address),
			Token:       nonIndexed[1].(common.Address),
			Amount:      nonIndexed[2].(*big.Int),
		}, nil
	}
	return nil, fmt.Errorf("TradeOpened event for tradeId 0x%s not found in receipt", common.Bytes2Hex(expectedTradeId[:]))
}

func parseTradeDisputedFromReceipt(receipt *types.Receipt, contractAddr common.Address, expectedTradeId [32]byte) (*P2PTradeDisputedEvent, error) {
	sigID := parsedP2PEscrowABI.Events["TradeDisputed"].ID
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 3 || lg.Topics[0] != sigID {
			continue
		}
		var got [32]byte
		copy(got[:], lg.Topics[1].Bytes())
		if got != expectedTradeId {
			continue
		}
		by := common.BytesToAddress(lg.Topics[2].Bytes())
		return &P2PTradeDisputedEvent{TradeID: got, By: by}, nil
	}
	return nil, fmt.Errorf("TradeDisputed event for tradeId 0x%s not found in receipt", common.Bytes2Hex(expectedTradeId[:]))
}

func parseTradeResolvedFromReceipt(receipt *types.Receipt, contractAddr common.Address, expectedTradeId [32]byte) (*P2PTradeResolvedEvent, error) {
	sigID := parsedP2PEscrowABI.Events["TradeResolved"].ID
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 2 || lg.Topics[0] != sigID {
			continue
		}
		var got [32]byte
		copy(got[:], lg.Topics[1].Bytes())
		if got != expectedTradeId {
			continue
		}
		nonIndexed, err := parsedP2PEscrowABI.Events["TradeResolved"].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 3 {
			return nil, fmt.Errorf("decode TradeResolved data: %w", err)
		}
		return &P2PTradeResolvedEvent{
			TradeID:         got,
			ReleasedToBuyer: nonIndexed[0].(bool),
			AmountMoved:     nonIndexed[1].(*big.Int),
			FeeToSystem:     nonIndexed[2].(*big.Int),
		}, nil
	}
	return nil, fmt.Errorf("TradeResolved event for tradeId 0x%s not found in receipt", common.Bytes2Hex(expectedTradeId[:]))
}

func parseTradeCancelledFromReceipt(receipt *types.Receipt, contractAddr common.Address, expectedTradeId [32]byte) (*P2PTradeCancelledEvent, error) {
	sigID := parsedP2PEscrowABI.Events["TradeCancelled"].ID
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 2 || lg.Topics[0] != sigID {
			continue
		}
		var got [32]byte
		copy(got[:], lg.Topics[1].Bytes())
		if got != expectedTradeId {
			continue
		}
		nonIndexed, err := parsedP2PEscrowABI.Events["TradeCancelled"].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 1 {
			return nil, fmt.Errorf("decode TradeCancelled data: %w", err)
		}
		return &P2PTradeCancelledEvent{
			TradeID:             got,
			ReturnedToAvailable: nonIndexed[0].(*big.Int),
		}, nil
	}
	return nil, fmt.Errorf("TradeCancelled event for tradeId 0x%s not found in receipt", common.Bytes2Hex(expectedTradeId[:]))
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
