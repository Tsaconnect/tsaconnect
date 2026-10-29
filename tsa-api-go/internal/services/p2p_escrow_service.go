package services

import (
	"bytes"
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
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// p2pEscrowABI is the P2PTradeEscrow surface the backend uses
// (smart-contracts/contracts/core/P2PTradeEscrow.sol; slice 8 of the contracts v2 migration).
// Trades are stored under tradeKey(opener, tradeId): every call after the open, every event and
// getTrade take that key. The trailing "error" entries are every custom error of P2PTradeEscrow
// and FeeConfig (whose reverts bubble up through the escrow's fee reads), so decodeRevertReason can
// name a revert.
const p2pEscrowABI = `[
	{"inputs":[{"name":"token","type":"address"},{"name":"amount","type":"uint256"}],"name":"deposit","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"token","type":"address"},{"name":"amount","type":"uint256"}],"name":"withdraw","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"releaser","type":"address"}],"name":"setReleaser","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"components":[{"name":"seller","type":"address"},{"name":"token","type":"address"},{"name":"maxAmount","type":"uint256"},{"name":"minPerTrade","type":"uint256"},{"name":"maxPerTrade","type":"uint256"},{"name":"deadline","type":"uint256"},{"name":"nonce","type":"bytes32"},{"name":"taker","type":"address"}],"name":"v","type":"tuple"}],"name":"cancelVoucher","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"},{"components":[{"name":"seller","type":"address"},{"name":"token","type":"address"},{"name":"maxAmount","type":"uint256"},{"name":"minPerTrade","type":"uint256"},{"name":"maxPerTrade","type":"uint256"},{"name":"deadline","type":"uint256"},{"name":"nonce","type":"bytes32"},{"name":"taker","type":"address"}],"name":"v","type":"tuple"},{"name":"sig","type":"bytes"},{"name":"amount","type":"uint256"}],"name":"openTrade","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"tradeId","type":"bytes32"},{"name":"token","type":"address"},{"name":"amount","type":"uint256"},{"name":"merchant","type":"address"},{"name":"releaser","type":"address"}],"name":"openTradeWithDeposit","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"key","type":"bytes32"}],"name":"markPaid","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"key","type":"bytes32"}],"name":"release","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"key","type":"bytes32"}],"name":"cancelByTimeout","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"key","type":"bytes32"}],"name":"raiseDispute","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"key","type":"bytes32"},{"name":"releaseToBuyer","type":"bool"}],"name":"adminResolve","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"seller","type":"address"},{"name":"token","type":"address"}],"name":"balanceOf","outputs":[{"name":"available","type":"uint256"},{"name":"locked","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"voucherHash","type":"bytes32"}],"name":"voucherFilled","outputs":[{"type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"voucherHash","type":"bytes32"}],"name":"voucherCancelled","outputs":[{"type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"seller","type":"address"}],"name":"releaserOf","outputs":[{"type":"address"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"key","type":"bytes32"}],"name":"getTrade","outputs":[{"components":[{"name":"seller","type":"address"},{"name":"amount","type":"uint96"},{"name":"buyer","type":"address"},{"name":"createdAt","type":"uint40"},{"name":"expiresAt","type":"uint40"},{"name":"status","type":"uint8"},{"name":"funding","type":"uint8"},{"name":"token","type":"address"},{"name":"autoReleaseAt","type":"uint40"},{"name":"disputedAt","type":"uint40"},{"name":"platformFeeBps","type":"uint16"},{"name":"gasFeeBps","type":"uint16"},{"name":"autoReleaseWindow","type":"uint40"},{"name":"voucherHash","type":"bytes32"},{"name":"tradeReleaser","type":"address"}],"name":"","type":"tuple"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"components":[{"name":"seller","type":"address"},{"name":"token","type":"address"},{"name":"maxAmount","type":"uint256"},{"name":"minPerTrade","type":"uint256"},{"name":"maxPerTrade","type":"uint256"},{"name":"deadline","type":"uint256"},{"name":"nonce","type":"bytes32"},{"name":"taker","type":"address"}],"name":"v","type":"tuple"}],"name":"hashVoucher","outputs":[{"type":"bytes32"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"paymentWindow","outputs":[{"type":"uint40"}],"stateMutability":"pure","type":"function"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"seller","type":"address"},{"indexed":true,"name":"token","type":"address"},{"indexed":false,"name":"amount","type":"uint256"}],"name":"Deposited","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"seller","type":"address"},{"indexed":true,"name":"token","type":"address"},{"indexed":false,"name":"amount","type":"uint256"}],"name":"Withdrawn","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"voucherHash","type":"bytes32"},{"indexed":true,"name":"seller","type":"address"}],"name":"VoucherCancelled","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":true,"name":"voucherHash","type":"bytes32"},{"indexed":true,"name":"buyer","type":"address"},{"indexed":false,"name":"seller","type":"address"},{"indexed":false,"name":"token","type":"address"},{"indexed":false,"name":"amount","type":"uint256"}],"name":"TradeOpened","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":true,"name":"seller","type":"address"},{"indexed":true,"name":"buyer","type":"address"},{"indexed":false,"name":"token","type":"address"},{"indexed":false,"name":"amount","type":"uint256"}],"name":"TradeOpenedWithDeposit","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"}],"name":"TradePaid","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":true,"name":"buyer","type":"address"},{"indexed":false,"name":"amountToBuyer","type":"uint256"},{"indexed":false,"name":"feeToSystem","type":"uint256"}],"name":"TradeReleased","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":true,"name":"buyer","type":"address"},{"indexed":false,"name":"amountToBuyer","type":"uint256"},{"indexed":false,"name":"feeToSystem","type":"uint256"}],"name":"TradeAutoReleased","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":false,"name":"amount","type":"uint256"},{"indexed":false,"name":"returnedToPool","type":"bool"}],"name":"TradeCancelled","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":true,"name":"by","type":"address"}],"name":"TradeDisputed","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"tradeId","type":"bytes32"},{"indexed":false,"name":"releasedToBuyer","type":"bool"},{"indexed":false,"name":"amountMoved","type":"uint256"},{"indexed":false,"name":"feeToSystem","type":"uint256"}],"name":"TradeResolved","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"seller","type":"address"},{"indexed":true,"name":"releaser","type":"address"}],"name":"ReleaserSet","type":"event"},
	{"inputs":[],"name":"AboveVoucherMax","type":"error"},
	{"inputs":[],"name":"AccessControlBadConfirmation","type":"error"},
	{"inputs":[{"name":"schedule","type":"uint48"}],"name":"AccessControlEnforcedDefaultAdminDelay","type":"error"},
	{"inputs":[],"name":"AccessControlEnforcedDefaultAdminRules","type":"error"},
	{"inputs":[{"name":"defaultAdmin","type":"address"}],"name":"AccessControlInvalidDefaultAdmin","type":"error"},
	{"inputs":[{"name":"account","type":"address"},{"name":"neededRole","type":"bytes32"}],"name":"AccessControlUnauthorizedAccount","type":"error"},
	{"inputs":[{"name":"target","type":"address"}],"name":"AddressEmptyCode","type":"error"},
	{"inputs":[{"name":"amount","type":"uint256"},{"name":"limit","type":"uint256"}],"name":"AmountAboveLimit","type":"error"},
	{"inputs":[{"name":"expected","type":"uint256"},{"name":"received","type":"uint256"}],"name":"AmountMismatch","type":"error"},
	{"inputs":[{"name":"at","type":"uint40"}],"name":"AutoReleaseDue","type":"error"},
	{"inputs":[],"name":"AutoReleaseNotArmed","type":"error"},
	{"inputs":[],"name":"BadAmount","type":"error"},
	{"inputs":[],"name":"BadVoucherSignature","type":"error"},
	{"inputs":[],"name":"BelowVoucherMin","type":"error"},
	{"inputs":[],"name":"ECDSAInvalidSignature","type":"error"},
	{"inputs":[{"name":"length","type":"uint256"}],"name":"ECDSAInvalidSignatureLength","type":"error"},
	{"inputs":[{"name":"s","type":"bytes32"}],"name":"ECDSAInvalidSignatureS","type":"error"},
	{"inputs":[{"name":"implementation","type":"address"}],"name":"ERC1967InvalidImplementation","type":"error"},
	{"inputs":[],"name":"ERC1967NonPayable","type":"error"},
	{"inputs":[],"name":"EnforcedPause","type":"error"},
	{"inputs":[],"name":"ExceedsVoucherCapacity","type":"error"},
	{"inputs":[],"name":"ExpectedPause","type":"error"},
	{"inputs":[],"name":"FailedCall","type":"error"},
	{"inputs":[],"name":"FeeAboveCap","type":"error"},
	{"inputs":[{"name":"feature","type":"bytes32"},{"name":"token","type":"address"}],"name":"FeeNotConfigured","type":"error"},
	{"inputs":[],"name":"FlatFeeNotSupported","type":"error"},
	{"inputs":[],"name":"InsufficientAvailable","type":"error"},
	{"inputs":[],"name":"InvalidInitialization","type":"error"},
	{"inputs":[],"name":"InvalidReleaser","type":"error"},
	{"inputs":[{"name":"tradeId","type":"bytes32"},{"name":"status","type":"uint8"}],"name":"InvalidStatus","type":"error"},
	{"inputs":[],"name":"InvalidTradeId","type":"error"},
	{"inputs":[],"name":"InvalidVoucherSeller","type":"error"},
	{"inputs":[],"name":"LengthMismatch","type":"error"},
	{"inputs":[{"name":"token","type":"address"}],"name":"LimitsNotConfigured","type":"error"},
	{"inputs":[],"name":"NotAParty","type":"error"},
	{"inputs":[],"name":"NotInitializing","type":"error"},
	{"inputs":[],"name":"NotReleaser","type":"error"},
	{"inputs":[],"name":"NotVoucherTaker","type":"error"},
	{"inputs":[{"name":"token","type":"address"}],"name":"NothingToClaim","type":"error"},
	{"inputs":[{"name":"token","type":"address"}],"name":"NothingToRescue","type":"error"},
	{"inputs":[],"name":"OnlyBuyer","type":"error"},
	{"inputs":[],"name":"OnlyVoucherSeller","type":"error"},
	{"inputs":[{"name":"token","type":"address"},{"name":"owed","type":"uint256"},{"name":"limit","type":"uint256"}],"name":"OwedLimitExceeded","type":"error"},
	{"inputs":[{"name":"at","type":"uint40"}],"name":"PaymentWindowClosed","type":"error"},
	{"inputs":[{"name":"bps","type":"uint16"}],"name":"RateTooHigh","type":"error"},
	{"inputs":[],"name":"ReentrancyGuardReentrantCall","type":"error"},
	{"inputs":[{"name":"bits","type":"uint8"},{"name":"value","type":"uint256"}],"name":"SafeCastOverflowedUintDowncast","type":"error"},
	{"inputs":[{"name":"token","type":"address"}],"name":"SafeERC20FailedOperation","type":"error"},
	{"inputs":[],"name":"SelfTrade","type":"error"},
	{"inputs":[],"name":"SellerInsufficient","type":"error"},
	{"inputs":[{"name":"at","type":"uint40"}],"name":"TimeoutNotElapsed","type":"error"},
	{"inputs":[],"name":"TradeExists","type":"error"},
	{"inputs":[],"name":"UUPSUnauthorizedCallContext","type":"error"},
	{"inputs":[{"name":"slot","type":"bytes32"}],"name":"UUPSUnsupportedProxiableUUID","type":"error"},
	{"inputs":[],"name":"VoucherBadBounds","type":"error"},
	{"inputs":[],"name":"VoucherCancelledErr","type":"error"},
	{"inputs":[],"name":"VoucherExpired","type":"error"},
	{"inputs":[],"name":"WindowOutOfBounds","type":"error"},
	{"inputs":[],"name":"ZeroAddress","type":"error"},
	{"inputs":[],"name":"ZeroLimit","type":"error"}
]`

var parsedP2PEscrowABI abi.ABI

func init() {
	var err error
	parsedP2PEscrowABI, err = abi.JSON(strings.NewReader(p2pEscrowABI))
	if err != nil {
		panic(fmt.Sprintf("failed to parse P2PTradeEscrow ABI: %v", err))
	}
}

// OnChainP2PTradeStatus mirrors P2PTradeEscrow's Status enum. Stable numeric values — change in
// lock-step with the contract.
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

// AdVoucher mirrors the EIP-712 struct in P2PTradeEscrow. Built by the backend at ad-creation
// time, signed off-chain by the seller, and presented by the buyer on every openTrade call.
//
// `Nonce` is server-generated (random bytes32); the EIP-712 digest of the voucher serves as its
// on-chain identity. The same voucher can be filled up to MaxAmount across many trades (bounded
// per-trade by Min/MaxPerTrade when non-zero). Taker restricts the voucher to one buyer; the API
// always publishes open ads (zero).
type AdVoucher struct {
	Seller      common.Address `abi:"seller"`
	Token       common.Address `abi:"token"`
	MaxAmount   *big.Int       `abi:"maxAmount"`
	MinPerTrade *big.Int       `abi:"minPerTrade"`
	MaxPerTrade *big.Int       `abi:"maxPerTrade"`
	Deadline    *big.Int       `abi:"deadline"`
	Nonce       [32]byte       `abi:"nonce"`
	Taker       common.Address `abi:"taker"`
}

// OnChainP2PTrade is the decoded getTrade(key). Trades are self-contained — no ad pointer because
// ads live entirely off-chain as signed vouchers.
type OnChainP2PTrade struct {
	Seller    common.Address
	Buyer     common.Address
	Token     common.Address
	Amount    *big.Int
	CreatedAt *big.Int
	// ExpiresAt is the payment deadline snapshotted at open (markPaid reverts after it).
	ExpiresAt *big.Int
	Status    OnChainP2PTradeStatus
	// Funding is 0=PooledFromMaker, 1=DepositedByTaker. The reconciler reads this off-chain truth
	// to pick the correct refund branch (pool-credit vs wallet-return).
	Funding uint8
	// FeeBps is PlatformFeeBps + GasFeeBps, the snapshot the release charges.
	FeeBps                    uint16
	PlatformFeeBps, GasFeeBps uint16
	AutoReleaseAt             *big.Int
	// DisputedAt is when the dispute was raised (0 if never): claimDisputeBackstop opens
	// P2PDisputeBackstop after it.
	DisputedAt  *big.Int
	VoucherHash [32]byte
	// TradeReleaser is the address the depositing user let release this buy-ad trade (zero: none).
	TradeReleaser common.Address
	Exists        bool // false when Status == None
}

// P2PDisputeBackstop mirrors P2PTradeEscrow.DISPUTE_BACKSTOP: after it anyone may close an
// unresolved dispute, which returns the crypto to the seller's side.
const P2PDisputeBackstop = 60 * 24 * time.Hour

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

// P2PReleaserSetEvent captures ReleaserSet — a seller naming who may release their trades.
type P2PReleaserSetEvent struct {
	Seller   common.Address
	Releaser common.Address
}

// P2PTradeOpenedEvent is parsed from openTrade receipts — carries voucherHash + seller + token.
// TradeID is the trade's storage key.
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

// P2PTradeCancelledEvent captures the TradeCancelled event payload — emitted by
// `cancelByTimeout` (adminResolve(false) emits TradeResolved). It carries the trade `Amount`
// plus a `ReturnedToPool` flag: true for pooled sell-ads (funds return to the seller's
// `available` pool, in-contract), false for deposited-by-taker buy-ads (funds are transferred
// back out to the user's wallet).
type P2PTradeCancelledEvent struct {
	TradeID        [32]byte
	Amount         *big.Int
	ReturnedToPool bool
}

// P2PTradeOpenedWithDepositEvent captures TradeOpenedWithDeposit, emitted by
// openTradeWithDeposit (buy-ad / JIT-funded path). Seller is the depositing user; Buyer is the
// merchant who receives crypto on release.
type P2PTradeOpenedWithDepositEvent struct {
	TradeID [32]byte
	Seller  common.Address
	Buyer   common.Address
	Token   common.Address
	Amount  *big.Int
}

// P2PTradeDisputedEvent captures TradeDisputed — emitted by raiseDispute.
type P2PTradeDisputedEvent struct {
	TradeID [32]byte
	By      common.Address
}

// P2PTradePaidEvent captures TradePaid — emitted by markPaid (buyer attests fiat sent).
type P2PTradePaidEvent struct{ TradeID [32]byte }

// P2PTradeResolvedEvent captures TradeResolved — emitted by adminResolve and claimDisputeBackstop.
type P2PTradeResolvedEvent struct {
	TradeID         [32]byte
	ReleasedToBuyer bool
	AmountMoved     *big.Int
	FeeToSystem     *big.Int
}

// P2PEscrowService prepares unsigned transactions for a P2PTradeEscrow and decodes its view
// methods + event logs.
type P2PEscrowService struct {
	client          *blockchain.EVMClient
	contractAddress string
	cfg             *config.Config
}

// NewP2PEscrowServiceForChain builds an escrow adapter bound to an explicit
// escrow contract address (one per chain). `cfg` may be nil in tests that only
// exercise address-dependent paths.
func NewP2PEscrowServiceForChain(client *blockchain.EVMClient, cfg *config.Config, escrowAddress string) *P2PEscrowService {
	return &P2PEscrowService{
		client:          client,
		contractAddress: escrowAddress,
		cfg:             cfg,
	}
}

// ContractAddress returns the escrow contract address.
func (s *P2PEscrowService) ContractAddress() string {
	return s.contractAddress
}

// HashID converts a DB-side UUID into the bytes32 trade id the opener passes to the contract.
// The contract stores the trade under TradeKey(opener, HashID(id)).
func HashID(id uuid.UUID) [32]byte {
	return crypto.Keccak256Hash(id[:])
}

// HashIDHex is the hex-with-0x form of HashID.
func HashIDHex(id uuid.UUID) string {
	h := HashID(id)
	return "0x" + common.Bytes2Hex(h[:])
}

// TradeKey is P2PTradeEscrow.tradeKey(opener, tradeId) = keccak256(abi.encode(opener, tradeId)):
// the key the escrow stores the trade under and every later call and event uses. opener is the
// buyer on openTrade and the depositing user on openTradeWithDeposit.
func TradeKey(opener common.Address, tradeID [32]byte) [32]byte {
	return crypto.Keccak256Hash(common.LeftPadBytes(opener.Bytes(), 32), tradeID[:])
}

// ─── Prepare* (unsigned-tx builders) ────────────────────────────────────────

// PrepareApprove builds the unsigned ERC-20 approve(spender=escrow, amount)
// tx so the seller can grant the escrow the right to pull `amount` of
// `token` during a subsequent deposit. The token contract is the `to` of
// this tx (not the escrow); the escrow is just the spender argument.
func (s *P2PEscrowService) PrepareApprove(seller, token string, amount *big.Int) ([]byte, error) {
	if err := s.requireConfigured(); err != nil {
		return nil, err
	}
	if s.client == nil {
		return nil, fmt.Errorf("p2p escrow: no chain client (dev fallback not supported for approve)")
	}
	return s.client.PrepareERC20Approve(token, seller, s.contractAddress, amount)
}

// ReadAllowance returns the seller's current ERC-20 allowance to the escrow
// for `token`. Used by the frontend to decide whether an additional
// approve tx is needed before a deposit can succeed.
func (s *P2PEscrowService) ReadAllowance(seller, token string) (*big.Int, error) {
	if err := s.requireConfigured(); err != nil {
		return nil, err
	}
	if s.client == nil {
		return nil, fmt.Errorf("p2p escrow: no chain client")
	}
	return s.client.GetTokenAllowance(token, seller, s.contractAddress)
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

// PrepareSetReleaser builds the seller's `setReleaser(releaser)`: the one address, besides the
// seller, that may release the seller's trades (a payment merchant names the platform release key).
func (s *P2PEscrowService) PrepareSetReleaser(seller, releaser string) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("setReleaser", common.HexToAddress(releaser))
	if err != nil {
		return nil, fmt.Errorf("pack setReleaser: %w", err)
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

// PrepareOpenTradeWithVoucher builds the unsigned tx the buyer broadcasts to open a trade against
// a seller's signed voucher. `tradeId` is HashID(trade.ID); the escrow stores the trade under
// TradeKey(buyer, tradeId). `sig` is the seller's 65-byte EIP-712 signature over the voucher.
func (s *P2PEscrowService) PrepareOpenTradeWithVoucher(tradeId [32]byte, voucher AdVoucher, sig []byte, buyer string, amount *big.Int) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("openTrade", tradeId, voucher, sig, amount)
	if err != nil {
		return nil, fmt.Errorf("pack openTrade: %w", err)
	}
	return s.buildUnsignedTx(buyer, data)
}

// PrepareOpenTradeWithDeposit builds the unsigned tx the user (crypto seller) broadcasts to open a
// buy-ad trade by depositing tokens just-in-time. `from` is the depositing user's wallet (the
// on-chain `seller`, and the opener in the trade key); `merchant` is the ad poster's wallet (the
// on-chain `buyer`/crypto recipient). `releaser` is who the user lets release this trade (the
// platform release key on a payment merchant's buy-ad, which releases after paying the user out),
// or "" for none. The user must have ERC20-approved the escrow for `amount` first.
func (s *P2PEscrowService) PrepareOpenTradeWithDeposit(tradeId [32]byte, token string, amount *big.Int, merchant, releaser, from string) ([]byte, error) {
	var rel common.Address
	if releaser != "" {
		rel = common.HexToAddress(releaser)
	}
	data, err := parsedP2PEscrowABI.Pack("openTradeWithDeposit", tradeId, common.HexToAddress(token), amount, common.HexToAddress(merchant), rel)
	if err != nil {
		return nil, fmt.Errorf("pack openTradeWithDeposit: %w", err)
	}
	return s.buildUnsignedTx(from, data)
}

// PrepareMarkPaid builds the buyer's markPaid(key).
func (s *P2PEscrowService) PrepareMarkPaid(key [32]byte, buyer string) ([]byte, error) {
	return s.prepareKeyCall("markPaid", key, buyer)
}

// PrepareRelease builds release(key) from `caller`: the seller, the seller's releaser, or the
// trade's releaser (the platform release key on payment-merchant trades).
func (s *P2PEscrowService) PrepareRelease(key [32]byte, caller string) ([]byte, error) {
	return s.prepareKeyCall("release", key, caller)
}

// PrepareCancelByTimeout builds the permissionless cancelByTimeout(key).
func (s *P2PEscrowService) PrepareCancelByTimeout(key [32]byte, caller string) ([]byte, error) {
	return s.prepareKeyCall("cancelByTimeout", key, caller)
}

// PrepareRaiseDispute builds raiseDispute(key) from a party (only from Paid).
func (s *P2PEscrowService) PrepareRaiseDispute(key [32]byte, by string) ([]byte, error) {
	return s.prepareKeyCall("raiseDispute", key, by)
}

// PrepareAdminResolve builds adminResolve(key, releaseToBuyer) from a RESOLVER_ROLE holder.
func (s *P2PEscrowService) PrepareAdminResolve(key [32]byte, releaseToBuyer bool, resolver string) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack("adminResolve", key, releaseToBuyer)
	if err != nil {
		return nil, fmt.Errorf("pack adminResolve: %w", err)
	}
	return s.buildUnsignedTx(resolver, data)
}

func (s *P2PEscrowService) prepareKeyCall(method string, key [32]byte, from string) ([]byte, error) {
	data, err := parsedP2PEscrowABI.Pack(method, key)
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", method, err)
	}
	return s.buildUnsignedTx(from, data)
}

// ─── Read* (view calls) ─────────────────────────────────────────────────────

// ReadBalance returns the (available, locked) tuple for a (seller, token) in
// the pool. Available is withdrawable; locked is reserved against open trades.
func (s *P2PEscrowService) ReadBalance(seller, token string) (*OnChainBalance, error) {
	vals, err := s.view("balanceOf", common.HexToAddress(seller), common.HexToAddress(token))
	if err != nil {
		return nil, err
	}
	if len(vals) != 2 {
		return nil, fmt.Errorf("decode balanceOf: %d values", len(vals))
	}
	return &OnChainBalance{Available: vals[0].(*big.Int), Locked: vals[1].(*big.Int)}, nil
}

// ReadVoucherFilled returns the cumulative amount filled against a voucher
// (by its EIP-712 digest). Tells the backend how much of the voucher's
// maxAmount remains available before reverting at ExceedsVoucherCapacity.
func (s *P2PEscrowService) ReadVoucherFilled(voucherHash [32]byte) (*big.Int, error) {
	vals, err := s.view("voucherFilled", voucherHash)
	if err != nil {
		return nil, err
	}
	return vals[0].(*big.Int), nil
}

// ReadVoucherCancelled returns whether a voucher has been revoked by its
// seller via cancelVoucher.
func (s *P2PEscrowService) ReadVoucherCancelled(voucherHash [32]byte) (bool, error) {
	vals, err := s.view("voucherCancelled", voucherHash)
	if err != nil {
		return false, err
	}
	return vals[0].(bool), nil
}

// ReadReleaser is releaserOf(seller): who besides the seller may release the seller's trades
// (zero: nobody).
func (s *P2PEscrowService) ReadReleaser(seller string) (common.Address, error) {
	vals, err := s.view("releaserOf", common.HexToAddress(seller))
	if err != nil {
		return common.Address{}, err
	}
	return vals[0].(common.Address), nil
}

// ReadPaymentWindow is the escrow's paymentWindow() (uint40 seconds).
func (s *P2PEscrowService) ReadPaymentWindow() (time.Duration, error) {
	vals, err := s.view("paymentWindow")
	if err != nil {
		return 0, err
	}
	return paymentWindowOf(vals)
}

// paymentWindowOf decodes paymentWindow()'s unpacked uint40.
func paymentWindowOf(vals []any) (time.Duration, error) {
	if len(vals) == 0 {
		return 0, fmt.Errorf("decode paymentWindow: empty")
	}
	switch v := vals[0].(type) {
	case *big.Int:
		if v != nil && v.IsInt64() && v.Sign() >= 0 {
			return time.Duration(v.Int64()) * time.Second, nil
		}
	case uint64:
		return time.Duration(v) * time.Second, nil
	}
	return 0, fmt.Errorf("decode paymentWindow: unexpected %T %v", vals[0], vals[0])
}

// getTradeTuple is getTrade's Trade struct, in declaration order.
type getTradeTuple struct {
	Seller            common.Address
	Amount            *big.Int
	Buyer             common.Address
	CreatedAt         *big.Int
	ExpiresAt         *big.Int
	Status            uint8
	Funding           uint8
	Token             common.Address
	AutoReleaseAt     *big.Int
	DisputedAt        *big.Int
	PlatformFeeBps    uint16
	GasFeeBps         uint16
	AutoReleaseWindow *big.Int
	VoucherHash       [32]byte
	TradeReleaser     common.Address
}

// ReadTrade returns the on-chain trade under `key`. Exists=false when the slot is empty
// (Status == None).
func (s *P2PEscrowService) ReadTrade(key [32]byte) (*OnChainP2PTrade, error) {
	vals, err := s.view("getTrade", key)
	if err != nil {
		return nil, err
	}
	return decodeGetTrade(vals)
}

// decodeGetTrade converts getTrade's unpacked tuple.
func decodeGetTrade(vals []any) (*OnChainP2PTrade, error) {
	if len(vals) != 1 {
		return nil, fmt.Errorf("decode getTrade: %d values", len(vals))
	}
	tup, ok := abi.ConvertType(vals[0], new(getTradeTuple)).(*getTradeTuple)
	if !ok {
		return nil, fmt.Errorf("decode getTrade: unexpected %T", vals[0])
	}
	t := &OnChainP2PTrade{
		Seller: tup.Seller, Buyer: tup.Buyer, Token: tup.Token, Amount: tup.Amount,
		CreatedAt: tup.CreatedAt, ExpiresAt: tup.ExpiresAt,
		Status: OnChainP2PTradeStatus(tup.Status), Funding: tup.Funding,
		PlatformFeeBps: tup.PlatformFeeBps, GasFeeBps: tup.GasFeeBps,
		FeeBps:        tup.PlatformFeeBps + tup.GasFeeBps,
		AutoReleaseAt: tup.AutoReleaseAt, DisputedAt: tup.DisputedAt,
		VoucherHash: tup.VoucherHash, TradeReleaser: tup.TradeReleaser,
	}
	t.Exists = t.Status != OnChainP2PStatusNone
	return t, nil
}

// HashVoucher delegates to the contract's `hashVoucher` view to compute the
// EIP-712 digest. Used at ad-signing time so the backend stores the canonical
// hash without re-implementing the EIP-712 domain locally.
func (s *P2PEscrowService) HashVoucher(v AdVoucher) ([32]byte, error) {
	vals, err := s.view("hashVoucher", v)
	if err != nil {
		return [32]byte{}, err
	}
	return vals[0].([32]byte), nil
}

// view packs, calls and unpacks one view of the escrow; a result with no values is an error.
func (s *P2PEscrowService) view(method string, args ...any) ([]any, error) {
	if err := s.requireConfigured(); err != nil {
		return nil, err
	}
	data, err := parsedP2PEscrowABI.Pack(method, args...)
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", method, err)
	}
	out, err := s.callContract(data)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", method, withRevertReason(err))
	}
	vals, err := parsedP2PEscrowABI.Unpack(method, out)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", method, err)
	}
	if len(vals) == 0 {
		return nil, fmt.Errorf("decode %s: empty", method)
	}
	return vals, nil
}

// ─── Verify* (parse receipts) ───────────────────────────────────────────────

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

// VerifyReleaserSet proves expectedSeller's setReleaser and returns the releaser it named.
func (s *P2PEscrowService) VerifyReleaserSet(txHash string, expectedSeller common.Address) (*P2PReleaserSetEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseReleaserSetFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedSeller)
}

// VerifyTradeOpened mirrors VerifyAdCreated for openTrade flows.
func (s *P2PEscrowService) VerifyTradeOpened(txHash string, expectedTradeId [32]byte) (*P2PTradeOpenedEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseTradeOpenedFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedTradeId)
}

// VerifyTradeOpenedWithDeposit confirms a buy-ad / JIT-funded openTradeWithDeposit
// receipt and returns the parsed event. Used by the open-confirm path to
// cross-check the on-chain seller (user) + buyer (merchant) against the DB.
func (s *P2PEscrowService) VerifyTradeOpenedWithDeposit(txHash string, expectedTradeId [32]byte) (*P2PTradeOpenedWithDepositEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseTradeOpenedWithDepositFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedTradeId)
}

// VerifyTradeReleased confirms a release or finalizeRelease
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

// VerifyTradePaid parses TradePaid for the expected trade from this escrow.
func (s *P2PEscrowService) VerifyTradePaid(txHash string, expectedTradeId [32]byte) (*P2PTradePaidEvent, error) {
	receipt, err := s.fetchReceiptAndCheckOK(txHash)
	if err != nil {
		return nil, err
	}
	return parseTradePaidFromReceipt(receipt, common.HexToAddress(s.contractAddress), expectedTradeId)
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

// parseReleaserSetFromReceipt finds ReleaserSet(seller, releaser) for expectedSeller; both fields
// are indexed. The last one wins when a receipt carries several.
func parseReleaserSetFromReceipt(receipt *types.Receipt, contractAddr, expectedSeller common.Address) (*P2PReleaserSetEvent, error) {
	sigID := parsedP2PEscrowABI.Events["ReleaserSet"].ID
	var found *P2PReleaserSetEvent
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 3 || lg.Topics[0] != sigID {
			continue
		}
		if seller := common.BytesToAddress(lg.Topics[1].Bytes()); seller == expectedSeller {
			found = &P2PReleaserSetEvent{Seller: seller, Releaser: common.BytesToAddress(lg.Topics[2].Bytes())}
		}
	}
	if found == nil {
		return nil, fmt.Errorf("ReleaserSet event for seller %s not found in receipt", expectedSeller.Hex())
	}
	return found, nil
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

func parseTradePaidFromReceipt(receipt *types.Receipt, contractAddr common.Address, expectedTradeId [32]byte) (*P2PTradePaidEvent, error) {
	sigID := parsedP2PEscrowABI.Events["TradePaid"].ID
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 2 || lg.Topics[0] != sigID {
			continue
		}
		var got [32]byte
		copy(got[:], lg.Topics[1].Bytes())
		if got == expectedTradeId {
			return &P2PTradePaidEvent{TradeID: got}, nil
		}
	}
	return nil, fmt.Errorf("TradePaid event for tradeId 0x%s not found in receipt", common.Bytes2Hex(expectedTradeId[:]))
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
		// v2 TradeCancelled non-indexed fields: amount, returnedToPool.
		nonIndexed, err := parsedP2PEscrowABI.Events["TradeCancelled"].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 2 {
			return nil, fmt.Errorf("decode TradeCancelled data: %w", err)
		}
		return &P2PTradeCancelledEvent{
			TradeID:        got,
			Amount:         nonIndexed[0].(*big.Int),
			ReturnedToPool: nonIndexed[1].(bool),
		}, nil
	}
	return nil, fmt.Errorf("TradeCancelled event for tradeId 0x%s not found in receipt", common.Bytes2Hex(expectedTradeId[:]))
}

// parseTradeOpenedWithDepositFromReceipt scans for the v2 TradeOpenedWithDeposit
// event. 3 indexed topics (tradeId, seller, buyer) + 2 non-indexed (token,
// amount).
func parseTradeOpenedWithDepositFromReceipt(receipt *types.Receipt, contractAddr common.Address, expectedTradeId [32]byte) (*P2PTradeOpenedWithDepositEvent, error) {
	sigID := parsedP2PEscrowABI.Events["TradeOpenedWithDeposit"].ID
	for _, lg := range receipt.Logs {
		// 4 topics = sig + indexed(tradeId, seller, buyer)
		if lg.Address != contractAddr || len(lg.Topics) < 4 || lg.Topics[0] != sigID {
			continue
		}
		var gotTrade [32]byte
		copy(gotTrade[:], lg.Topics[1].Bytes())
		if gotTrade != expectedTradeId {
			continue
		}
		seller := common.BytesToAddress(lg.Topics[2].Bytes())
		buyer := common.BytesToAddress(lg.Topics[3].Bytes())
		// Non-indexed fields in declared order: token, amount.
		nonIndexed, err := parsedP2PEscrowABI.Events["TradeOpenedWithDeposit"].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 2 {
			return nil, fmt.Errorf("decode TradeOpenedWithDeposit data: %w", err)
		}
		return &P2PTradeOpenedWithDepositEvent{
			TradeID: gotTrade,
			Seller:  seller,
			Buyer:   buyer,
			Token:   nonIndexed[0].(common.Address),
			Amount:  nonIndexed[1].(*big.Int),
		}, nil
	}
	return nil, fmt.Errorf("TradeOpenedWithDeposit event for tradeId 0x%s not found in receipt", common.Bytes2Hex(expectedTradeId[:]))
}

// parseTradeReleasedFromReceipt accepts either a TradeReleased (manual /
// release-key release) OR a TradeAutoReleased (finalizeRelease after the seller's window) log —
// they carry an identical field layout (tradeId indexed, buyer indexed,
// amountToBuyer + feeToSystem non-indexed). A release that settled via
// auto-release still verifies here, so the release-confirm + reconcile paths
// recognise both. (Auto-release itself is default-disabled in v2.)
func parseTradeReleasedFromReceipt(receipt *types.Receipt, contractAddr common.Address, expectedTradeId [32]byte) (*P2PTradeReleasedEvent, error) {
	if ev := tryParseReleaseLike(receipt, contractAddr, expectedTradeId, "TradeReleased"); ev != nil {
		return ev, nil
	}
	if ev := tryParseReleaseLike(receipt, contractAddr, expectedTradeId, "TradeAutoReleased"); ev != nil {
		return ev, nil
	}
	return nil, fmt.Errorf("TradeReleased/TradeAutoReleased event for tradeId 0x%s not found in receipt", common.Bytes2Hex(expectedTradeId[:]))
}

// tryParseReleaseLike scans for a release-shaped event (`eventName`) matching
// the tradeId. Returns nil when no matching log is present so the caller can
// fall through to the next candidate.
func tryParseReleaseLike(receipt *types.Receipt, contractAddr common.Address, expectedTradeId [32]byte, eventName string) *P2PTradeReleasedEvent {
	sigID := parsedP2PEscrowABI.Events[eventName].ID
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
		nonIndexed, err := parsedP2PEscrowABI.Events[eventName].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 2 {
			return nil
		}
		return &P2PTradeReleasedEvent{
			TradeID:       got,
			Buyer:         buyer,
			AmountToBuyer: nonIndexed[0].(*big.Int),
			FeeToSystem:   nonIndexed[1].(*big.Int),
		}
	}
	return nil
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

// receiptConfirmTimeout bounds how long a confirm endpoint waits for a
// just-broadcast tx to be mined before giving up (Sonic mines in ~1-2s; the
// chain reconciler backstops anything that still slips through).
const receiptConfirmTimeout = 30 * time.Second

func (s *P2PEscrowService) fetchReceiptAndCheckOK(txHash string) (*types.Receipt, error) {
	if err := s.requireConfigured(); err != nil {
		return nil, err
	}
	// The app broadcasts then immediately calls confirm, so the tx is usually
	// not mined yet — poll for the receipt instead of failing on the first
	// "not found".
	receipt, err := s.client.WaitForReceipt(txHash, receiptConfirmTimeout)
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
		return nil, fmt.Errorf("estimate gas (would revert): %w", withRevertReason(err))
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

var (
	revertErrorSelector = []byte{0x08, 0xc3, 0x79, 0xa0} // Error(string)
	revertPanicSelector = []byte{0x4e, 0x48, 0x7b, 0x71} // Panic(uint256)
)

// revertData is the revert data a node attached to err (rpc.DataError), or nil when there is none
// or it is shorter than a selector.
func revertData(err error) []byte {
	var de rpc.DataError
	if !errors.As(err, &de) {
		return nil
	}
	// geth (and BSC nodes) send the revert data as a hex string; Hardhat
	// nests it as {"message": …, "data": "0x…"}.
	raw, ok := de.ErrorData().(string)
	if !ok {
		if m, isMap := de.ErrorData().(map[string]interface{}); isMap {
			raw, ok = m["data"].(string)
		}
	}
	if !ok {
		return nil
	}
	data, err := hexutil.Decode(raw)
	if err != nil || len(data) < 4 {
		return nil
	}
	return data
}

// decodeRevertReason turns the revert data a node attaches to a failed
// eth_estimateGas / eth_call (rpc.DataError) into a human-readable reason:
// Error(string) -> the string, Panic(uint256) -> "panic 0x..", a P2PEscrowV3
// custom error -> "Name()", anything else -> the raw hex. Returns "" when the
// error carries no revert data.
func decodeRevertReason(err error) string {
	data := revertData(err)
	if data == nil {
		return ""
	}
	switch {
	case bytes.Equal(data[:4], revertErrorSelector):
		if reason, unpackErr := abi.UnpackRevert(data); unpackErr == nil {
			return reason
		}
	case bytes.Equal(data[:4], revertPanicSelector):
		return "panic 0x" + new(big.Int).SetBytes(data[4:]).Text(16)
	default:
		if abiErr, lookupErr := parsedP2PEscrowABI.ErrorByID([4]byte(data[:4])); lookupErr == nil {
			return abiErr.Sig
		}
		// The liquidity vault's custom errors (OZ Pausable's EnforcedPause).
		if abiErr, lookupErr := parsedLiquidityVaultABI.ErrorByID([4]byte(data[:4])); lookupErr == nil {
			return abiErr.Sig
		}
	}
	return hexutil.Encode(data)
}

// withRevertReason appends the decoded revert reason to err (keeping err
// wrapped, so isChainRevert and errors.As still work). Errors without revert
// data, e.g. transport failures, are returned untouched.
func withRevertReason(err error) error {
	if reason := decodeRevertReason(err); reason != "" {
		return fmt.Errorf("%w: %s", err, reason)
	}
	return err
}
