package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ─── Errors (handler-mapped to HTTP) ────────────────────────────────────────

var (
	ErrAdNotFound            = errors.New("p2p: ad not found")
	ErrTradeNotFound         = errors.New("p2p: trade not found")
	ErrPaymentMethodMissing  = errors.New("p2p: payment method not found")
	ErrAdNotActive           = errors.New("p2p: ad is not active")
	ErrAmountOutOfRange      = errors.New("p2p: amount outside ad min/max limits")
	ErrInsufficientRemaining = errors.New("p2p: trade amount exceeds ad remaining")
	ErrSelfTrade             = errors.New("p2p: seller cannot open a trade on their own ad")
	ErrInvalidTransition     = errors.New("p2p: invalid state transition")
	ErrForbidden             = errors.New("p2p: not authorized for this action")
	ErrRateStale             = errors.New("p2p: rate source is stale; cannot quote")
	ErrAmountOverflow        = errors.New("p2p: amount exceeds uint96 maximum supported by contract")
	ErrInvalidParams         = errors.New("p2p: invalid parameters")
	ErrUnsupportedToken      = errors.New("p2p: token not supported on this chain")
	ErrAdHasOpenTrades       = errors.New("p2p: ad has open trades; close them first")
	ErrDisputeAlreadyOpen    = errors.New("p2p: trade already has an open dispute")
	ErrDisputeNotFound       = errors.New("p2p: dispute not found")
	ErrNotApplicableForBuyAd = errors.New("p2p: action not applicable for buy-ads")
	ErrActiveTradeExists     = errors.New("p2p: you already have an active trade on this ad")
	// ErrSellerPoolInsufficient: the maker's on-chain available balance can no
	// longer cover this trade (they withdrew, or another trade drained the
	// pool). Pre-checked at open so the buyer gets a clear "try another ad"
	// (409) instead of an opaque "estimate gas (would revert)" 500.
	ErrSellerPoolInsufficient = errors.New("p2p: seller's available balance can no longer cover this trade")
	// ErrTradePrepareReverted: building the on-chain tx failed because it would
	// revert (e.g. bad voucher signature, voucher expired, pool drained between
	// the pre-check and the estimate). The client can't fix our infra but the
	// request as-formed can't be fulfilled → 422 with the revert reason.
	ErrTradePrepareReverted = errors.New("p2p: the trade could not be prepared on-chain")
	// ErrChainUnavailable: a transient RPC/transport failure while preparing the
	// tx (nonce / gas-price lookup) — retryable → 503.
	ErrChainUnavailable = errors.New("p2p: blockchain temporarily unavailable, please retry")
)

// isChainRevert reports whether a prepare error is an on-chain revert (vs a
// transport/RPC failure), so the two map to different HTTP statuses.
func isChainRevert(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "revert")
}

// uint96Max is the largest amount the contract can hold for a single ad/trade
// (`uint96` field in P2PEscrow.sol). Computed once.
var uint96Max = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 96), big.NewInt(1))

// ─── Dependencies (interfaces so tests can mock) ────────────────────────────

// P2PEscrowAdapter is the contract-binding surface the trade service depends
// on. P2PEscrowService satisfies it; tests inject a fake.
//
// The pool model splits ad lifecycle into off-chain voucher signing (no
// adapter call) and on-chain settlement. Methods that move funds — deposit,
// withdraw, openTrade(voucher), markPaid, release, cancelByTimeout,
// raiseDispute, adminResolve — are prepared as unsigned txs the caller signs
// and broadcasts via their wallet.
type P2PEscrowAdapter interface {
	ContractAddress() string

	// Pool capital movement.
	PrepareApprove(seller, token string, amount *big.Int) ([]byte, error)
	ReadAllowance(seller, token string) (*big.Int, error)
	PrepareDeposit(seller, token string, amount *big.Int) ([]byte, error)
	PrepareWithdraw(seller, token string, amount *big.Int) ([]byte, error)

	// Voucher / trade lifecycle.
	PrepareCancelVoucher(seller string, voucher AdVoucher) ([]byte, error)
	PrepareOpenTradeWithVoucher(tradeId [32]byte, voucher AdVoucher, sig []byte, buyer string, amount *big.Int) ([]byte, error)
	// PrepareOpenTradeWithDeposit is the v2 buy-ad / JIT-funded path: the user
	// deposits crypto at open time. `from` is the depositing user (on-chain
	// seller), `merchant` is the ad poster (on-chain buyer/recipient).
	PrepareOpenTradeWithDeposit(tradeId [32]byte, token string, amount *big.Int, merchant, from string) ([]byte, error)
	PrepareMarkPaid(tradeId [32]byte, buyer string) ([]byte, error)
	PrepareRelease(tradeId [32]byte, seller string) ([]byte, error)
	PrepareCancelByTimeout(tradeId [32]byte, caller string) ([]byte, error)
	PrepareRaiseDispute(tradeId [32]byte, by string) ([]byte, error)
	PrepareAdminResolve(tradeId [32]byte, refundBuyer bool, admin string) ([]byte, error)
	// PrepareAdminRelease builds the unsigned adminRelease tx sent by the
	// platform admin EOA — the system-driven (PM-automation) release path.
	PrepareAdminRelease(tradeId [32]byte, admin string) ([]byte, error)

	// Receipt verification.
	VerifyDeposited(txHash string, expectedSeller, expectedToken common.Address) (*P2PDepositedEvent, error)
	VerifyWithdrawn(txHash string, expectedSeller, expectedToken common.Address) (*P2PWithdrawnEvent, error)
	VerifyVoucherCancelled(txHash string, expectedVoucherHash [32]byte) (*P2PVoucherCancelledEvent, error)
	VerifyTradeOpened(txHash string, expectedTradeId [32]byte) (*P2PTradeOpenedEvent, error)
	VerifyTradeOpenedWithDeposit(txHash string, expectedTradeId [32]byte) (*P2PTradeOpenedWithDepositEvent, error)
	VerifyTradeReleased(txHash string, expectedTradeId [32]byte) (*P2PTradeReleasedEvent, error)
	VerifyTradeCancelled(txHash string, expectedTradeId [32]byte) (*P2PTradeCancelledEvent, error)
	VerifyTradeDisputed(txHash string, expectedTradeId [32]byte) (*P2PTradeDisputedEvent, error)
	VerifyTradeResolved(txHash string, expectedTradeId [32]byte) (*P2PTradeResolvedEvent, error)

	// Views — used by reconciler + voucher-state checks.
	ReadTrade(tradeId [32]byte) (*OnChainP2PTrade, error)
	// ReadTradePlatformFeeBps reads the V3-snapshotted platform-fee bps for a
	// trade (design §8). Returns 0/false on a pre-V3 escrow or a V2-opened trade
	// (no snapshot) — the documented no-op signal for accrueP2PReleaseFee.
	ReadTradePlatformFeeBps(tradeId [32]byte) (platformBps uint16, found bool)
	ReadBalance(seller, token string) (*OnChainBalance, error)
	ReadVoucherFilled(voucherHash [32]byte) (*big.Int, error)
	ReadVoucherCancelled(voucherHash [32]byte) (bool, error)
	HashVoucher(v AdVoucher) ([32]byte, error)
}

// RateProvider gives the trade service a freshness signal so it can refuse
// to create ads when upstream rate data is stale.
type RateProvider interface {
	GetSnapshot() (*RatesSnapshot, error)
}

// EventPublisher is the event-bus seam.
type EventPublisher interface {
	Publish(events.Event)
}

// Clock makes time injectable for tests (expires_at calculations need to be
// deterministic).
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// SystemClock is the default clock; tests pass a fakeClock.
var SystemClock Clock = realClock{}

// ─── Service ───────────────────────────────────────────────────────────────

type P2PTradeService struct {
	db *gorm.DB
	// escrows resolves the per-chain escrow adapter + EIP-712 domain (chainId,
	// version) from an entity's ChainID. There is deliberately NO single
	// escrow/chainID/version scalar: every escrow-touching method resolves from
	// the entity it loads (trade.ChainID / ad.ChainID), so a missed site is a
	// compile error, not a silent Sonic fallback (Option B, spec §5.2.4).
	escrows *P2PEscrowRegistry
	// defaultChainID is used only for ops that have no entity chain yet —
	// CreateAd (new ads default to this chain) and the pool/seller ops
	// (approve/deposit/withdraw/allowance/balance). Sonic in production.
	defaultChainID int64
	rates          RateProvider
	bus            EventPublisher
	clock          Clock
	tokenResolver  func(chainID int64, symbol string) string
	// messages is optional. When set, every state transition also writes a
	// "system" chat message so the trade thread is self-documenting. Nil-safe
	// — internal calls go through `s.systemMsg` which guards.
	messages *P2PMessageService
	// floatOffsetMaxBps caps the absolute basis-point offset a seller can
	// publish on a float-rate ad. 0 (or unset) means use defaultFloatOffsetMaxBps.
	floatOffsetMaxBps int

	// fees is the fee/TP engine (SP3). Optional/nil-safe: wired via
	// SetFeeService from main.go. Every release transition (ConfirmRelease,
	// reconciler auto-release, adminResolve→buyer) accrues the realized on-chain
	// fee through accrueP2PReleaseFee. nil fees → no-op.
	fees *FeeDistributionService
}

// defaultFloatOffsetMaxBps is the platform-wide cap applied when the service
// is constructed without an explicit value. 1000 bps = ±10%, matching the
// typical Binance P2P deviation cap. Production should always set this from
// config (P2P_FLOAT_OFFSET_MAX_BPS); the default is a safety net for tests
// and dev environments.
const defaultFloatOffsetMaxBps = 1000

func NewP2PTradeService(db *gorm.DB, escrows *P2PEscrowRegistry, rates RateProvider, bus EventPublisher, defaultChainID int64) *P2PTradeService {
	return &P2PTradeService{
		db:             db,
		escrows:        escrows,
		rates:          rates,
		bus:            bus,
		clock:          SystemClock,
		defaultChainID: defaultChainID,
	}
}

// resolve returns the escrow adapter + EIP-712 domain (chainId, version) for a
// chain. Fail-loud — callers MUST propagate the error, never fall back to
// another chain (an unconfigured-chain entity must surface, not route funds to
// Sonic).
func (s *P2PTradeService) resolve(chainID int64) (P2PEscrowAdapter, int64, string, error) {
	return s.escrows.Resolve(chainID)
}

// EnabledChains returns the P2P-enabled chains for the marketplace/create-ad UI.
func (s *P2PTradeService) EnabledChains() []P2PChainInfo {
	if s.escrows == nil {
		return nil
	}
	return s.escrows.Chains()
}

// WithClock returns a copy of the service using the supplied clock — tests
// only. Avoids exposing a mutable field on the type.
func (s *P2PTradeService) WithClock(c Clock) *P2PTradeService {
	cp := *s
	cp.clock = c
	return &cp
}

// SetTokenResolver wires a (chainID, symbol) → address function. Called at
// service construction time so the service stays free of config-shape coupling.
func (s *P2PTradeService) SetTokenResolver(f func(chainID int64, symbol string) string) {
	s.tokenResolver = f
}

// SetMessageService wires the chat thread so transitions emit system
// messages. Optional — without it the trade still works, just no chat
// transcript.
func (s *P2PTradeService) SetMessageService(m *P2PMessageService) {
	s.messages = m
}

// SetFloatOffsetMaxBps sets the platform-wide cap on the absolute basis-point
// offset accepted on float-rate ads. Values ≤ 0 fall back to
// defaultFloatOffsetMaxBps. Called once at service construction from config.
func (s *P2PTradeService) SetFloatOffsetMaxBps(bps int) {
	if bps <= 0 {
		s.floatOffsetMaxBps = defaultFloatOffsetMaxBps
		return
	}
	s.floatOffsetMaxBps = bps
}

// floatOffsetCap returns the effective bps cap, falling back to the default
// when SetFloatOffsetMaxBps was never called.
func (s *P2PTradeService) floatOffsetCap() int {
	if s.floatOffsetMaxBps <= 0 {
		return defaultFloatOffsetMaxBps
	}
	return s.floatOffsetMaxBps
}

// SetFeeService wires the fee/TP engine (SP3). Called once at construction from
// main.go. A nil engine makes accrueP2PReleaseFee a no-op.
func (s *P2PTradeService) SetFeeService(svc *FeeDistributionService) {
	s.fees = svc
}

// accrueP2PReleaseFee is the SINGLE seam through which all three P2P release
// transitions (ConfirmRelease, reconciler auto-release, adminResolve→buyer)
// accrue the realized on-chain platform fee into the fee/TP v2 engine. Routing
// every release through one helper closes the auto-release / admin-resolve gap
// the design §3 calls out (those paths have no user confirm and previously did
// no TP accounting at all).
//
// Idempotency: OnFeeSettled is keyed on (SourceType="p2p_release", SourceID=
// trade.ID), so a confirm + a later reconciler pass for the SAME trade accrue
// exactly once — the second is a guard-row no-op.
//
// Durability caveat (SP3 audit): on the LIVE user-confirm paths (ConfirmRelease,
// ConfirmAdminResolveDispute) the trade is committed terminal BEFORE this fires,
// and ReconcileTrade early-returns on terminal trades — so the reconciler will
// NOT re-drive a dropped accrual here. EnqueueFeeSettled's bounded retry +
// alert (fee_v2_settle_failed_total) is the current backstop against a transient
// failure; a true durable outbox is the Phase-2 fix. The reconciler-triggered
// release paths (reconcileToReleased/reconcileToResolved) ARE self-healing
// because the trade stays non-terminal until the reconcile commits.
//
// Fee→USD: feeBaseUnits is the on-chain fee in the settlement token's base
// units; we divide by 10^decimals (per-chain via tokenDecimalsFor) and treat
// the settlement token (USDT/USDC) as ~$1, mirroring the handlers' established
// weiToUSDFloat convention. Run async (EnqueueFeeSettled) so it never blocks
// the release confirm or a reconciler tick.
//
// gen0 / buyer-cashback recipient is trade.BuyerID — the party that receives
// crypto and bears the fee (the buyer on a sell-ad; the receiving merchant on a
// buy-ad, which is exactly who trade.BuyerID labels in the release emits).
//
// ── P2P 1% PLATFORM accrual from the V3 snapshot (design §8, B7) ──
//
// The on-chain GAS fee (the legacy feeBps / feeBaseUnits passed in) is buyer gas
// — 100% system, NO TP/cashback — so it is NOT what gets accrued here. P2P's
// TP/cashback comes from the SEPARATE 1% PLATFORM fee, which P2PEscrowV3
// SNAPSHOTS into the trade as platformFeeBpsSnapshot at openTrade.
//
// This reads that snapshot from the escrow (ReadTradePlatformFeeBps), computes
// the platform-fee USD from the trade's token amount, and settles it under
// FeatureP2PPlatform (the carved {0.50,0.15,0.15,0.00,0.20} split) for the buyer
// (gen0 — the party that receives crypto and bears the fee). The on-chain gas
// fee (feeBaseUnits) is deliberately NOT accrued.
//
// SAFE TO MERGE BEFORE V3: until V3 is deployed, ReadTradePlatformFeeBps returns
// 0 (pre-V3 escrow / V2-opened trade) → this is a documented no-op, exactly as
// before. The flag check + the wiring fire real accrual the moment V3 lands.
//
// Gating: no-op unless a wired fee engine. Idempotency:
// OnFeeSettled is keyed (SourceType="p2p_release", SourceID=trade.ID), so a
// confirm + a later reconciler pass for the SAME trade accrue exactly once — the
// second is a guard-row no-op, so the chain reconciler does NOT re-accrue. Run
// async (EnqueueFeeSettled) so it never blocks the release confirm / reconcile.
func (s *P2PTradeService) accrueP2PReleaseFee(trade *models.P2PTrade, feeBaseUnits *big.Int) {
	_ = feeBaseUnits // the on-chain gas fee is system-only; not accrued here.

	if s.fees == nil || trade == nil {
		return
	}

	// Resolve the escrow for the trade's chain (fail-loud — never Sonic fallback).
	esc, _, _, err := s.resolve(trade.ChainID)
	if err != nil || esc == nil {
		log.Printf("[p2p accrue] trade=%s: resolve chain %d: %v", trade.ID, trade.ChainID, err)
		return
	}

	// Read the V3-snapshotted platform-fee bps. 0 ⇒ pre-V3 / V2 trade ⇒ no-op.
	platformBps, found := esc.ReadTradePlatformFeeBps(HashID(trade.ID))
	if !found || platformBps == 0 {
		return
	}

	// platform fee (base units) = amount · platformBps / 10000.
	amount, ok := new(big.Int).SetString(trade.TokenAmount, 10)
	if !ok || amount.Sign() <= 0 {
		return
	}
	platformFee := new(big.Int).Mul(amount, big.NewInt(int64(platformBps)))
	platformFee.Quo(platformFee, big.NewInt(10000))

	feeUSD := baseUnitsToUSD(platformFee, tokenDecimalsFor(trade.ChainID, trade.Token))
	if feeUSD <= 0 {
		return
	}

	// gen0 / buyer-cashback recipient is trade.BuyerID — the party that receives
	// crypto and bears the fee (buyer on a sell-ad; receiving merchant on a buy-ad).
	s.fees.EnqueueFeeSettled(FeeEvent{
		Feature:    models.FeatureP2PPlatform,
		Token:      trade.Token,
		FeeUSD:     feeUSD,
		UserID:     trade.BuyerID,
		SourceType: "p2p_release",
		SourceID:   trade.ID,
	})
}

// onChainTradeFee derives the platform fee (base units) from a view-read trade:
// fee = Amount · FeeBps / 10000. The reconciler has no TradeReleased event to
// read FeeToSystem from, so it reconstructs the fee from the on-chain trade
// fields. Returns nil when the trade is absent or carries no amount/fee.
func onChainTradeFee(t *OnChainP2PTrade) *big.Int {
	if t == nil || t.Amount == nil || t.FeeBps == 0 {
		return nil
	}
	fee := new(big.Int).Mul(t.Amount, big.NewInt(int64(t.FeeBps)))
	return fee.Quo(fee, big.NewInt(10000))
}

// baseUnitsToUSD converts an on-chain token amount (base units) to a USD float,
// treating the settlement token as a ~$1 stablecoin (the P2P settlement tokens
// are USDT/USDC). Mirrors handlers.weiToUSDFloat. amount/10^decimals.
func baseUnitsToUSD(amount *big.Int, decimals int) float64 {
	if amount == nil || amount.Sign() <= 0 {
		return 0
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	fAmount := new(big.Float).SetInt(amount)
	fDiv := new(big.Float).SetInt(divisor)
	result, _ := new(big.Float).Quo(fAmount, fDiv).Float64()
	return result
}

// systemMsg is the nil-safe gateway for system message writes. Failures here
// must never break the live transition (the user has already moved money);
// log and continue. The reconciler can re-fire any missed audit, but a
// missing chat message is purely cosmetic loss.
func (s *P2PTradeService) systemMsg(ctx context.Context, tradeID uuid.UUID, body string) {
	if s.messages == nil {
		return
	}
	if err := s.messages.PostSystem(ctx, tradeID, body); err != nil {
		log.Printf("[p2p systemMsg] trade=%s: %v", tradeID, err)
	}
}

// ─── Parameter structs ──────────────────────────────────────────────────────

type CreateAdParams struct {
	Side             string // "" (defaults to "sell"), "sell", or "buy"
	Token            string
	TokenAmount      string // base units; e.g. "1000000000" = 1000 USDT
	FiatCurrency     string
	Rate             string // fiat per 1 token base unit; required when RateType=='fixed', ignored for 'float'
	RateType         string // '' (defaults to 'fixed'), 'fixed', or 'float'
	FloatOffsetBps   *int   // required when RateType=='float'; basis points, e.g. 50 = +0.5%
	FloatAnchor      string // '' (defaults to 'bybit_sell'); reference feed key for float ads
	MinLimit         string // fiat
	MaxLimit         string // fiat
	PaymentMethodIDs []uuid.UUID
	Terms            string
	// ChainID selects the P2P chain. When 0 (omitted by current app) the
	// service defaults to s.defaultChainID (Sonic 146) for back-compat.
	ChainID int64
}

type OpenTradeParams struct {
	FiatAmount      string
	PaymentMethodID uuid.UUID
	// TokenAmount (base units) is the authoritative crypto amount for the
	// buy-ad / JIT sell path: the user specifies exactly how much crypto to
	// sell, the app approves that amount, and the contract escrows it. When
	// set (buy-ads), fiat is derived as tokenAmount × rate; when empty
	// (sell-ads, or a fiat-first buy-ad), the amount is derived from fiat.
	TokenAmount string
}

// CreateAdResult carries the persisted ad plus the EIP-712 typed-data
// payload the seller must sign off-chain. The signature is submitted back
// via SignAd; no on-chain transaction is needed to publish an ad.
type CreateAdResult struct {
	Ad        *models.P2PAd
	TypedData *VoucherTypedData
}

// VoucherTypedData is an EIP-712 typed-data payload in the shape wagmi /
// ethers / RainbowKit consume via signTypedData. Fields mirror the contract's
// AdVoucher tuple; values are stringified for JSON safety on amounts /
// timestamps.
type VoucherTypedData struct {
	Types       map[string][]VoucherTypedField `json:"types"`
	PrimaryType string                         `json:"primaryType"`
	Domain      VoucherTypedDomain             `json:"domain"`
	Message     VoucherTypedMessage            `json:"message"`
}

type VoucherTypedField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type VoucherTypedDomain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           int64  `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

type VoucherTypedMessage struct {
	Seller      string `json:"seller"`
	Token       string `json:"token"`
	MaxAmount   string `json:"maxAmount"`
	MinPerTrade string `json:"minPerTrade"`
	MaxPerTrade string `json:"maxPerTrade"`
	Deadline    string `json:"deadline"`
	Nonce       string `json:"nonce"` // hex with 0x prefix
}

type ReleaseResult struct {
	UnsignedTx []byte
}

// ─── Ad lifecycle ───────────────────────────────────────────────────────────

// Default voucher TTL — sellers can re-publish after this expires.
const defaultVoucherTTL = 30 * 24 * time.Hour

// CreateAd builds an AdVoucher, persists the ad in `pending` (awaiting
// signature) state, and returns the EIP-712 typed-data payload for the
// seller to sign off-chain. No on-chain transaction is required to publish.
//
// The seller must call SignAd next, presenting their signature over the
// returned typed data. Until then the ad is invisible to buyers.
func (s *P2PTradeService) CreateAd(ctx context.Context, sellerID uuid.UUID, sellerAddress string, params CreateAdParams) (*CreateAdResult, error) {
	// Buy-ads (merchant offers to BUY crypto from users) take a wholly
	// separate path: no on-chain pool, no voucher, no signature. The user
	// deposits crypto JIT at trade-open instead. Branch before the sell-ad's
	// pool-balance precheck so a merchant doesn't need a deposited balance to
	// post a buy-ad.
	if strings.EqualFold(strings.TrimSpace(params.Side), models.P2PAdSideBuy) {
		return s.createBuyAd(ctx, sellerID, sellerAddress, params)
	}

	if err := s.validateCreateAd(params); err != nil {
		return nil, err
	}
	if !common.IsHexAddress(sellerAddress) {
		return nil, fmt.Errorf("%w: seller address", ErrInvalidParams)
	}

	amount, _ := new(big.Int).SetString(params.TokenAmount, 10)
	if amount == nil {
		return nil, fmt.Errorf("%w: token_amount", ErrInvalidParams)
	}
	if amount.Sign() <= 0 {
		return nil, fmt.Errorf("%w: token_amount must be > 0", ErrInvalidParams)
	}
	if amount.Cmp(uint96Max) > 0 {
		return nil, ErrAmountOverflow
	}

	if err := s.requireFreshRate(params.FiatCurrency); err != nil {
		return nil, err
	}

	if err := s.validatePaymentMethodsOwned(sellerID, params.PaymentMethodIDs, params.FiatCurrency); err != nil {
		return nil, err
	}

	rateType := strings.ToLower(strings.TrimSpace(params.RateType))
	if rateType == "" {
		rateType = models.P2PAdRateTypeFixed
	}
	anchor := strings.TrimSpace(params.FloatAnchor)
	if anchor == "" {
		anchor = models.P2PAdFloatAnchorBybitSell
	}

	// Resolve the target chain: default to Sonic when omitted (back-compat).
	chainID := params.ChainID
	if chainID == 0 {
		chainID = s.defaultChainID
	}
	// Chain must be P2P-enabled (escrow configured) and the token allowed on it.
	if _, _, _, err := s.resolve(chainID); err != nil {
		return nil, fmt.Errorf("%w: chain %d not available for P2P", ErrInvalidParams, chainID)
	}
	if !s.escrows.AssetAllowed(chainID, params.Token) {
		return nil, fmt.Errorf("%w: %s is not tradeable on chain %d", ErrInvalidParams, params.Token, chainID)
	}

	tokenAddr, err := s.resolveTokenAddress(chainID, strings.ToUpper(params.Token))
	if err != nil {
		return nil, err
	}

	// Resolve the escrow + EIP-712 domain for the chosen chain.
	escrow, domainChainID, version, err := s.resolve(chainID)
	if err != nil {
		return nil, err
	}

	// Verify the seller actually holds enough deposited capital before we
	// publish a voucher promising it. The contract is still the authoritative
	// gate (openTrade reverts if available < amount), but failing here means
	// the seller doesn't sign a voucher they can't honour — protecting their
	// reputation and preventing the off-chain ad/voucher accounting from
	// diverging from the on-chain pool balance.
	bal, err := escrow.ReadBalance(sellerAddress, tokenAddr)
	if err != nil {
		// Don't silently proceed: a stuck RPC during publish would let a
		// seller stamp out unbacked vouchers (they sign, post, buyers later
		// hit the on-chain "Seller balance insufficient" revert).
		log.Printf("[p2p CreateAd] ReadBalance failed for seller=%s token=%s: %v", sellerAddress, tokenAddr, err)
		return nil, fmt.Errorf("%w: could not verify pool balance, retry", ErrInvalidParams)
	}
	if bal.Available.Cmp(amount) < 0 {
		return nil, fmt.Errorf("%w: insufficient available balance (have %s, need %s)",
			ErrInvalidParams, bal.Available, amount)
	}

	ad := &models.P2PAd{
		ID:               uuid.New(),
		SellerID:         sellerID,
		Side:             models.P2PAdSideSell,
		Token:            strings.ToUpper(params.Token),
		ChainID:          chainID,
		Amount:           params.TokenAmount,
		Remaining:        params.TokenAmount,
		Reserved:         "0",
		FiatCurrency:     strings.ToUpper(params.FiatCurrency),
		Rate:             params.Rate,
		RateType:         rateType,
		FloatOffsetBps:   params.FloatOffsetBps,
		FloatAnchor:      anchor,
		MinLimit:         models.OptionalNumeric(params.MinLimit),
		MaxLimit:         models.OptionalNumeric(params.MaxLimit),
		PaymentMethodIDs: stringifyUUIDs(params.PaymentMethodIDs),
		Terms:            params.Terms,
		Status:           models.P2PAdStatusPending,
	}

	if rateType == models.P2PAdRateTypeFloat {
		effective, err := s.ComputeEffectiveRate(ad)
		if err != nil {
			return nil, err
		}
		ad.Rate = effective
	}

	// Build the voucher. Nonce is server-generated; doubles as the ad's
	// off-chain id for indexing. Deadline defaults to 30 days; sellers
	// re-publish after that to keep the rate fresh.
	nonce := HashID(uuid.New())
	deadline := s.clock.Now().Add(defaultVoucherTTL)

	// Per-trade limits in the voucher are TOKEN amounts (the contract checks
	// `amount` against them, where amount is in token base units). MinLimit /
	// MaxLimit on the ad are FIAT amounts though, so we don't push them into
	// the voucher — the fiat-side floor/ceiling is enforced off-chain in
	// OpenTrade via checkFiatLimits. The voucher carries only the total cap.
	voucher := AdVoucher{
		Seller:      common.HexToAddress(sellerAddress),
		Token:       common.HexToAddress(tokenAddr),
		MaxAmount:   new(big.Int).Set(amount),
		MinPerTrade: big.NewInt(0),
		MaxPerTrade: big.NewInt(0),
		Deadline:    big.NewInt(deadline.Unix()),
		Nonce:       nonce,
	}

	// Ask the contract for the canonical EIP-712 digest. We could compute
	// it locally but delegating to the contract guarantees parity with the
	// on-chain check and surfaces any RPC misconfiguration early.
	hash, err := escrow.HashVoucher(voucher)
	if err != nil {
		return nil, fmt.Errorf("hash voucher: %w", err)
	}

	ad.SellerAddress = common.HexToAddress(sellerAddress).Hex() // checksummed
	ad.VoucherNonce = "0x" + common.Bytes2Hex(nonce[:])
	dt := deadline
	ad.VoucherDeadline = &dt
	ad.VoucherHash = "0x" + common.Bytes2Hex(hash[:])

	if err := s.db.WithContext(ctx).Create(ad).Error; err != nil {
		return nil, fmt.Errorf("persist ad: %w", err)
	}

	typedData := s.buildVoucherTypedData(voucher, escrow.ContractAddress(), domainChainID, version)

	s.audit(ctx, models.P2PAuditAdCreated, &sellerID, &ad.ID, nil, map[string]any{
		"token":       ad.Token,
		"amount":      ad.Amount,
		"rate":        ad.Rate,
		"voucherHash": ad.VoucherHash,
	})
	s.emit(events.P2PAdCreated, sellerID, "P2P ad created", "", map[string]any{"adId": ad.ID, "status": ad.Status})

	return &CreateAdResult{Ad: ad, TypedData: typedData}, nil
}

// createBuyAd persists a merchant's buy-ad (offer to BUY crypto from users).
// Unlike a sell-ad it has no on-chain pool, no voucher, and no signature step
// — it is created `active` immediately. Its Remaining/Reserved are pure
// off-chain capacity counters: Remaining is decremented (Reserved incremented)
// when a user opens a JIT-funded trade, and restored on cancel/refund. The
// crypto itself is deposited by the user at openTradeWithDeposit, so the
// merchant never deposits a pool.
//
// SellerAddress on a buy-ad stores the MERCHANT's wallet — the cryptoRecipient
// who receives crypto on release. It's captured here so OpenTrade can pass it
// as the `merchant` arg without a User lookup.
func (s *P2PTradeService) createBuyAd(ctx context.Context, merchantID uuid.UUID, merchantAddress string, params CreateAdParams) (*CreateAdResult, error) {
	if err := s.validateCreateAd(params); err != nil {
		return nil, err
	}
	if !common.IsHexAddress(merchantAddress) {
		return nil, fmt.Errorf("%w: merchant address", ErrInvalidParams)
	}

	amount, _ := new(big.Int).SetString(params.TokenAmount, 10)
	if amount == nil {
		return nil, fmt.Errorf("%w: token_amount", ErrInvalidParams)
	}
	if amount.Sign() <= 0 {
		return nil, fmt.Errorf("%w: token_amount must be > 0", ErrInvalidParams)
	}
	if amount.Cmp(uint96Max) > 0 {
		return nil, ErrAmountOverflow
	}

	if err := s.requireFreshRate(params.FiatCurrency); err != nil {
		return nil, err
	}
	// The PMs on a buy-ad are the MERCHANT's accounts — where they'll send
	// fiat to the user once the user deposits crypto. Same ownership +
	// currency check as a sell-ad.
	if err := s.validatePaymentMethodsOwned(merchantID, params.PaymentMethodIDs, params.FiatCurrency); err != nil {
		return nil, err
	}

	rateType := strings.ToLower(strings.TrimSpace(params.RateType))
	if rateType == "" {
		rateType = models.P2PAdRateTypeFixed
	}
	anchor := strings.TrimSpace(params.FloatAnchor)
	if anchor == "" {
		anchor = models.P2PAdFloatAnchorBybitSell
	}
	// Resolve the target chain: default to Sonic when omitted (back-compat).
	chainID := params.ChainID
	if chainID == 0 {
		chainID = s.defaultChainID
	}
	// Chain must be P2P-enabled (escrow configured) and the token allowed on it.
	if _, _, _, err := s.resolve(chainID); err != nil {
		return nil, fmt.Errorf("%w: chain %d not available for P2P", ErrInvalidParams, chainID)
	}
	if !s.escrows.AssetAllowed(chainID, params.Token) {
		return nil, fmt.Errorf("%w: %s is not tradeable on chain %d", ErrInvalidParams, params.Token, chainID)
	}

	// Validate the token is supported so OpenTrade can resolve it to an
	// address later (buy-ads skip the voucher but still settle on-chain).
	if _, err := s.resolveTokenAddress(chainID, strings.ToUpper(params.Token)); err != nil {
		return nil, err
	}

	ad := &models.P2PAd{
		ID:               uuid.New(),
		SellerID:         merchantID,
		Side:             models.P2PAdSideBuy,
		Token:            strings.ToUpper(params.Token),
		ChainID:          chainID,
		Amount:           params.TokenAmount,
		Remaining:        params.TokenAmount, // off-chain capacity
		Reserved:         "0",
		FiatCurrency:     strings.ToUpper(params.FiatCurrency),
		Rate:             params.Rate,
		RateType:         rateType,
		FloatOffsetBps:   params.FloatOffsetBps,
		FloatAnchor:      anchor,
		MinLimit:         models.OptionalNumeric(params.MinLimit),
		MaxLimit:         models.OptionalNumeric(params.MaxLimit),
		PaymentMethodIDs: stringifyUUIDs(params.PaymentMethodIDs),
		Terms:            params.Terms,
		// Buy-ads are live immediately — no voucher to sign.
		Status:        models.P2PAdStatusActive,
		SellerAddress: common.HexToAddress(merchantAddress).Hex(), // merchant = cryptoRecipient
	}

	if rateType == models.P2PAdRateTypeFloat {
		effective, err := s.ComputeEffectiveRate(ad)
		if err != nil {
			return nil, err
		}
		ad.Rate = effective
	}

	if err := s.db.WithContext(ctx).Create(ad).Error; err != nil {
		return nil, fmt.Errorf("persist buy ad: %w", err)
	}

	s.audit(ctx, models.P2PAuditAdCreated, &merchantID, &ad.ID, nil, map[string]any{
		"side":   ad.Side,
		"token":  ad.Token,
		"amount": ad.Amount,
		"rate":   ad.Rate,
	})
	s.emit(events.P2PAdCreated, merchantID, "P2P buy ad live", "", map[string]any{"adId": ad.ID, "status": ad.Status})

	// No typed data — buy-ads have no voucher to sign.
	return &CreateAdResult{Ad: ad, TypedData: nil}, nil
}

// SignAd stores the seller's EIP-712 signature for a pending ad and flips
// it to active. The signature is verified against the canonical voucher
// hash recovered from the contract — a wrong-domain, wrong-key, or
// wrong-fields sig is rejected here, before any buyer sees the ad.
func (s *P2PTradeService) SignAd(ctx context.Context, adID, sellerID uuid.UUID, sellerAddress string, signature []byte) (*models.P2PAd, error) {
	if !common.IsHexAddress(sellerAddress) {
		return nil, fmt.Errorf("%w: seller address", ErrInvalidParams)
	}
	if len(signature) != 65 {
		return nil, fmt.Errorf("%w: signature must be 65 bytes", ErrInvalidParams)
	}

	var ad models.P2PAd
	if err := s.db.WithContext(ctx).First(&ad, "id = ?", adID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAdNotFound
		}
		return nil, err
	}
	if ad.SellerID != sellerID {
		return nil, ErrForbidden
	}
	// Buy-ads have no voucher to sign — they're created active directly.
	if ad.Side == models.P2PAdSideBuy {
		return nil, ErrNotApplicableForBuyAd
	}
	// Idempotent replay: identical sig on an already-active ad returns OK.
	sigHex := "0x" + common.Bytes2Hex(signature)
	if ad.Status == models.P2PAdStatusActive && ad.VoucherSignature == sigHex {
		return &ad, nil
	}
	if ad.Status != models.P2PAdStatusPending {
		return nil, fmt.Errorf("%w: ad status=%s", ErrInvalidTransition, ad.Status)
	}

	voucher, err := s.adVoucher(&ad, sellerAddress)
	if err != nil {
		return nil, err
	}

	// Resolve the ad's chain so the voucher hash (and the mismatch-log domain)
	// match the contract the seller signed against — NOT a default chain.
	escrow, domainChainID, _, err := s.resolve(ad.ChainID)
	if err != nil {
		return nil, err
	}
	hash, err := escrow.HashVoucher(voucher)
	if err != nil {
		return nil, fmt.Errorf("hash voucher: %w", err)
	}
	if recovered, err := recoverEIP712Signer(hash, signature); err != nil {
		return nil, fmt.Errorf("%w: invalid signature (%v)", ErrInvalidParams, err)
	} else if !strings.EqualFold(recovered.Hex(), sellerAddress) {
		// Recovery mismatches are almost always a domain-separator drift
		// (chainID or verifyingContract on the BE != what the contract
		// hashed with) or a voucher-field drift between CreateAd and SignAd
		// (token resolver changed, deadline/nonce corruption). Logging the
		// inputs the BE used to recompute the hash gives the operator
		// everything needed to spot which side disagrees — without this,
		// the only signal is the recovered (fake) address which carries no
		// useful information on its own.
		log.Printf(
			"[p2p SignAd] signature recovery mismatch: recovered=%s expected=%s "+
				"adID=%s voucherHash=0x%x chainID=%d verifyingContract=%s "+
				"seller=%s token=%s maxAmount=%s deadline=%d nonce=0x%x",
			recovered.Hex(), sellerAddress, ad.ID, hash, domainChainID, escrow.ContractAddress(),
			voucher.Seller.Hex(), voucher.Token.Hex(), voucher.MaxAmount.String(),
			voucher.Deadline.Int64(), voucher.Nonce,
		)
		return nil, fmt.Errorf("%w: signature recovered to %s, expected %s",
			ErrInvalidParams, recovered.Hex(), sellerAddress)
	}

	res := s.db.WithContext(ctx).Model(&models.P2PAd{}).
		Where("id = ? AND status = ?", ad.ID, models.P2PAdStatusPending).
		Updates(map[string]any{
			"status":            models.P2PAdStatusActive,
			"voucher_signature": sigHex,
			"updated_at":        s.clock.Now(),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrInvalidTransition
	}
	ad.Status = models.P2PAdStatusActive
	ad.VoucherSignature = sigHex

	s.audit(ctx, models.P2PAuditAdSigned, &sellerID, &ad.ID, nil, map[string]any{
		"voucherHash": ad.VoucherHash,
	})
	s.emit(events.P2PAdCreated, sellerID, "P2P ad live", "", map[string]any{"adId": ad.ID, "status": "active"})
	return &ad, nil
}

// PrepareCancelAd returns the unsigned cancelVoucher tx for an active or paused ad.
// Cancellation is on-chain because the contract is the source of truth for
// voucher revocation — a DB-only flag would still leave the signature valid
// for buyers who fetched it before the flag was set.
func (s *P2PTradeService) PrepareCancelAd(ctx context.Context, adID, sellerID uuid.UUID, sellerAddress string) ([]byte, error) {
	if !common.IsHexAddress(sellerAddress) {
		return nil, fmt.Errorf("%w: seller address", ErrInvalidParams)
	}
	var ad models.P2PAd
	if err := s.db.WithContext(ctx).First(&ad, "id = ?", adID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAdNotFound
		}
		return nil, err
	}
	if ad.SellerID != sellerID {
		return nil, ErrForbidden
	}
	// Buy-ads have no on-chain voucher to revoke — cancellation is a DB-only
	// status flip via CancelBuyAd. Surface a clear error so a stale client
	// doesn't try to broadcast a meaningless tx.
	if ad.Side == models.P2PAdSideBuy {
		return nil, ErrNotApplicableForBuyAd
	}
	if ad.Status != models.P2PAdStatusActive && ad.Status != models.P2PAdStatusPaused {
		return nil, fmt.Errorf("%w: ad status=%s", ErrInvalidTransition, ad.Status)
	}
	var openCount int64
	s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("ad_id = ? AND status IN ?", ad.ID,
			[]string{models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid,
				models.P2PTradeStatusReleasing, models.P2PTradeStatusDisputed}).
		Count(&openCount)
	if openCount > 0 {
		return nil, ErrAdHasOpenTrades
	}
	voucher, err := s.adVoucher(&ad, sellerAddress)
	if err != nil {
		return nil, err
	}
	escrow, _, _, err := s.resolve(ad.ChainID)
	if err != nil {
		return nil, err
	}
	return escrow.PrepareCancelVoucher(sellerAddress, voucher)
}

// ConfirmCancelAd verifies the on-chain VoucherCancelled receipt and marks
// the ad cancelled in DB.
func (s *P2PTradeService) ConfirmCancelAd(ctx context.Context, adID, sellerID uuid.UUID, txHash string) (*models.P2PAd, error) {
	var ad models.P2PAd
	if err := s.db.WithContext(ctx).First(&ad, "id = ?", adID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAdNotFound
		}
		return nil, err
	}
	if ad.SellerID != sellerID {
		return nil, ErrForbidden
	}
	if ad.Status == models.P2PAdStatusCancelled {
		return &ad, nil
	}
	// Buy-ads have no on-chain VoucherCancelled receipt to confirm — they
	// cancel via CancelBuyAd (DB-only). Reject a stale on-chain confirm.
	if ad.Side == models.P2PAdSideBuy {
		return nil, ErrNotApplicableForBuyAd
	}
	// Status pre-check before the RPC verify call. The DB UPDATE below also
	// gates on (active|paused) but RPC has cost (and a 500 status response
	// can mask the real reason); fail-fast cheaply.
	if ad.Status != models.P2PAdStatusActive && ad.Status != models.P2PAdStatusPaused {
		return nil, fmt.Errorf("%w: ad status=%s", ErrInvalidTransition, ad.Status)
	}
	if ad.VoucherHash == "" {
		return nil, fmt.Errorf("%w: ad has no voucher hash", ErrInvalidTransition)
	}
	hash, err := decodeHex32(ad.VoucherHash)
	if err != nil {
		return nil, fmt.Errorf("%w: stored voucher hash is corrupt (%v)", ErrInvalidTransition, err)
	}

	escrow, _, _, err := s.resolve(ad.ChainID)
	if err != nil {
		return nil, err
	}
	if _, err := escrow.VerifyVoucherCancelled(txHash, hash); err != nil {
		return nil, fmt.Errorf("verify on-chain cancel: %w", err)
	}

	res := s.db.WithContext(ctx).Model(&models.P2PAd{}).
		Where("id = ? AND status IN ?", ad.ID,
			[]string{models.P2PAdStatusActive, models.P2PAdStatusPaused}).
		Updates(map[string]any{
			"status":     models.P2PAdStatusCancelled,
			"remaining":  "0",
			"reserved":   "0", // belt-and-braces: cancelVoucher on chain only succeeds when no open trades, so reserved should already be 0; zero explicitly so a future bug here doesn't leave the row with a non-zero reserved on a cancelled ad.
			"updated_at": s.clock.Now(),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrInvalidTransition
	}
	ad.Status = models.P2PAdStatusCancelled
	ad.Remaining = "0"
	s.audit(ctx, models.P2PAuditAdClosed, &sellerID, &ad.ID, nil, map[string]any{"txHash": txHash})
	s.emit(events.P2PAdClosed, sellerID, "P2P ad closed", "", map[string]any{"adId": ad.ID})
	return &ad, nil
}

// CancelBuyAd cancels a merchant's buy-ad. Buy-ads have no on-chain voucher,
// so cancellation is a pure DB status flip (no tx to prepare/confirm). Refuses
// if the ad has open trades — those depositors must settle first. Idempotent
// on an already-cancelled ad.
func (s *P2PTradeService) CancelBuyAd(ctx context.Context, adID, merchantID uuid.UUID) (*models.P2PAd, error) {
	var ad models.P2PAd
	if err := s.db.WithContext(ctx).First(&ad, "id = ?", adID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAdNotFound
		}
		return nil, err
	}
	if ad.SellerID != merchantID {
		return nil, ErrForbidden
	}
	if ad.Side != models.P2PAdSideBuy {
		// Sell-ads must cancel on-chain via cancelVoucher (PrepareCancelAd).
		return nil, fmt.Errorf("%w: sell-ads cancel on-chain", ErrInvalidTransition)
	}
	if ad.Status == models.P2PAdStatusCancelled {
		return &ad, nil
	}
	if ad.Status != models.P2PAdStatusActive && ad.Status != models.P2PAdStatusPaused {
		return nil, fmt.Errorf("%w: ad status=%s", ErrInvalidTransition, ad.Status)
	}
	var openCount int64
	s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("ad_id = ? AND status IN ?", ad.ID,
			[]string{models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid,
				models.P2PTradeStatusReleasing, models.P2PTradeStatusDisputed}).
		Count(&openCount)
	if openCount > 0 {
		return nil, ErrAdHasOpenTrades
	}

	res := s.db.WithContext(ctx).Model(&models.P2PAd{}).
		Where("id = ? AND status IN ?", ad.ID,
			[]string{models.P2PAdStatusActive, models.P2PAdStatusPaused}).
		Updates(map[string]any{
			"status":     models.P2PAdStatusCancelled,
			"remaining":  "0",
			"reserved":   "0",
			"updated_at": s.clock.Now(),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrInvalidTransition
	}
	ad.Status = models.P2PAdStatusCancelled
	ad.Remaining = "0"
	s.audit(ctx, models.P2PAuditAdClosed, &merchantID, &ad.ID, nil, map[string]any{"side": "buy"})
	s.emit(events.P2PAdClosed, merchantID, "P2P buy ad cancelled", "", map[string]any{"adId": ad.ID})
	return &ad, nil
}

func (s *P2PTradeService) PauseAd(ctx context.Context, adID, sellerID uuid.UUID) error {
	return s.flipAdStatus(ctx, adID, sellerID, models.P2PAdStatusActive, models.P2PAdStatusPaused,
		models.P2PAuditAdPaused, events.P2PAdPaused)
}

func (s *P2PTradeService) ResumeAd(ctx context.Context, adID, sellerID uuid.UUID) error {
	return s.flipAdStatus(ctx, adID, sellerID, models.P2PAdStatusPaused, models.P2PAdStatusActive,
		models.P2PAuditAdResumed, events.P2PAdResumed)
}

func (s *P2PTradeService) flipAdStatus(ctx context.Context, adID, sellerID uuid.UUID, from, to, action, event string) error {
	var ad models.P2PAd
	if err := s.db.WithContext(ctx).First(&ad, "id = ?", adID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrAdNotFound
		}
		return err
	}
	if ad.SellerID != sellerID {
		return ErrForbidden
	}
	res := s.db.WithContext(ctx).Model(&models.P2PAd{}).
		Where("id = ? AND status = ?", adID, from).
		Updates(map[string]any{"status": to, "updated_at": s.clock.Now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: ad status=%s", ErrInvalidTransition, ad.Status)
	}
	s.audit(ctx, action, &sellerID, &ad.ID, nil, nil)
	s.emit(event, sellerID, "", "", map[string]any{"adId": ad.ID})
	return nil
}

// ─── Trade lifecycle ────────────────────────────────────────────────────────

func (s *P2PTradeService) OpenTrade(ctx context.Context, adID, buyerID uuid.UUID, buyerAddress string, params OpenTradeParams) (*models.P2PTrade, []byte, error) {
	var ad models.P2PAd
	if err := s.db.WithContext(ctx).First(&ad, "id = ?", adID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrAdNotFound
		}
		return nil, nil, err
	}
	if ad.Status != models.P2PAdStatusActive {
		return nil, nil, ErrAdNotActive
	}
	if ad.SellerID == buyerID {
		return nil, nil, ErrSelfTrade
	}

	// Buy-ad → JIT-funded path. The "buyer" param here is the OPENER (the user
	// selling crypto); they deposit on-chain at open. buyerAddress carries the
	// opener's wallet (the depositor / on-chain `from`).
	if ad.Side == models.P2PAdSideBuy {
		return s.openTradeWithDeposit(ctx, &ad, buyerID, buyerAddress, params)
	}

	// Effective rate is recomputed live for float ads (snapshotted onto the
	// trade row below), and returned verbatim for fixed ads. ErrRateStale on
	// float ads is surfaced unchanged so the handler maps it to 503.
	effectiveRate, err := s.ComputeEffectiveRate(&ad)
	if err != nil {
		return nil, nil, err
	}

	tokenAmount, fiatAmount, err := computeTokenFromFiat(params.FiatAmount, effectiveRate, tokenDecimalsFor(ad.ChainID, ad.Token))
	if err != nil {
		return nil, nil, err
	}
	if err := checkFiatLimits(fiatAmount, string(ad.MinLimit), string(ad.MaxLimit)); err != nil {
		return nil, nil, err
	}
	remaining, _ := new(big.Int).SetString(ad.Remaining, 10)
	if remaining == nil || tokenAmount.Cmp(remaining) > 0 {
		return nil, nil, ErrInsufficientRemaining
	}
	if tokenAmount.Sign() <= 0 {
		return nil, nil, fmt.Errorf("%w: derived token_amount=0", ErrInvalidParams)
	}

	if err := s.validateAdPaymentMethod(&ad, params.PaymentMethodID); err != nil {
		return nil, nil, err
	}

	trade := &models.P2PTrade{
		ID:              uuid.New(),
		AdID:            ad.ID,
		BuyerID:         buyerID,
		SellerID:        ad.SellerID,
		OpenerID:        &buyerID, // sell-ad: the buyer opens
		Token:           ad.Token,
		ChainID:         ad.ChainID,
		TokenAmount:     tokenAmount.String(),
		FiatAmount:      fiatAmount.String(),
		FiatCurrency:    ad.FiatCurrency,
		Rate:            effectiveRate,
		PaymentMethodID: params.PaymentMethodID,
		Status:          models.P2PTradeStatusPendingPayment,
		ExpiresAt:       s.clock.Now().Add(p2pPaymentWindow),
	}
	trade.OnChainTradeID = HashIDHex(trade.ID)
	onChainTradeID := HashID(trade.ID)

	// Build the voucher from the ad's stored fields + the seller wallet
	// implied by the on-chain voucher signature. The voucher hash recomputed
	// here must match ad.VoucherHash — a mismatch means the DB row drifted
	// from the signed payload, and openTrade would revert with
	// "Bad voucher signature" on chain. Catch it loud here instead.
	if ad.VoucherSignature == "" || ad.SellerAddress == "" {
		return nil, nil, fmt.Errorf("%w: ad not yet signed", ErrInvalidTransition)
	}
	// Refuse to forward an expired voucher to the contract — the on-chain
	// call would revert with "Voucher expired", but the DB transaction below
	// has already debited ad.remaining / credited ad.reserved. Catch it here.
	if ad.VoucherDeadline != nil && !s.clock.Now().Before(*ad.VoucherDeadline) {
		return nil, nil, fmt.Errorf("%w: voucher expired (deadline %s)", ErrInvalidTransition, ad.VoucherDeadline)
	}
	voucher, err := s.adVoucher(&ad, ad.SellerAddress)
	if err != nil {
		return nil, nil, err
	}
	sig, err := decodeHexSig(ad.VoucherSignature)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: stored voucher signature is corrupt (%v)", ErrInvalidTransition, err)
	}
	escrow, _, _, err := s.resolve(ad.ChainID)
	if err != nil {
		return nil, nil, err
	}

	// Pre-check the maker's on-chain pool BEFORE the gas estimate. openTrade
	// debits available[seller][token]; if the seller has since withdrawn or
	// another trade drained it, the estimate reverts with "Seller balance
	// insufficient" and surfaces as an opaque 500. Catch it here and return a
	// clear 409 ("try another ad") instead. Best-effort: if the balance read
	// itself fails we fall through to the prep (its revert mapping below is the
	// backstop) rather than block a buyer on a flaky RPC.
	if tokenAddr, terr := s.resolveTokenAddress(ad.ChainID, ad.Token); terr == nil {
		if bal, berr := escrow.ReadBalance(ad.SellerAddress, tokenAddr); berr == nil {
			if bal.Available.Cmp(tokenAmount) < 0 {
				return nil, nil, fmt.Errorf("%w (maker available %s, trade needs %s base units)",
					ErrSellerPoolInsufficient, bal.Available, tokenAmount)
			}
		} else {
			log.Printf("[p2p OpenTrade] seller pool pre-check read failed (proceeding to prep): seller=%s token=%s: %v",
				ad.SellerAddress, tokenAddr, berr)
		}
	}

	unsignedTx, err := escrow.PrepareOpenTradeWithVoucher(onChainTradeID, voucher, sig, buyerAddress, tokenAmount)
	if err != nil {
		// Don't let an opaque prep failure surface as a generic 500. A revert
		// means the on-chain openTrade would fail (bad voucher signature, or
		// the pool drained between the pre-check and the estimate) → 422 with
		// the reason; anything else (nonce / gas-price RPC) is transient → 503.
		// Log with context for triage either way.
		log.Printf("[p2p OpenTrade] prepare failed: ad=%s seller=%s token=%s amount=%s err=%v",
			ad.ID, ad.SellerAddress, ad.Token, tokenAmount, err)
		if isChainRevert(err) {
			return nil, nil, fmt.Errorf("%w: %v", ErrTradePrepareReverted, err)
		}
		return nil, nil, fmt.Errorf("%w: %v", ErrChainUnavailable, err)
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// One active trade per (ad, buyer): a repeated/double-tapped open would
		// otherwise double-debit ad.remaining and strand reserved capacity on a
		// second phantom trade. (Sell-ad opener is the buyer.)
		if active, e := s.countActiveTradesForOpener(tx, ad.ID, "buyer_id", buyerID); e != nil {
			return e
		} else if active > 0 {
			return ErrActiveTradeExists
		}
		res := tx.Model(&models.P2PAd{}).
			Where("id = ? AND status = ? AND CAST(remaining AS NUMERIC) >= CAST(? AS NUMERIC)",
				ad.ID, models.P2PAdStatusActive, tokenAmount.String()).
			Updates(map[string]any{
				"remaining":  gorm.Expr("CAST(remaining AS NUMERIC) - CAST(? AS NUMERIC)", tokenAmount.String()),
				"reserved":   gorm.Expr("CAST(reserved AS NUMERIC) + CAST(? AS NUMERIC)", tokenAmount.String()),
				"updated_at": s.clock.Now(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrInsufficientRemaining
		}
		return tx.Create(trade).Error
	})
	if err != nil {
		return nil, nil, err
	}

	// Post-commit cache refresh for float ads: best-effort update so the
	// browse list shows the just-quoted rate on subsequent reads. The trade
	// already has the snapshotted rate; this is purely a display cache. A
	// crash between commit and this update leaves ad.rate stale until the
	// next successful trade open — acceptable since it's not a price source.
	if ad.RateType == models.P2PAdRateTypeFloat {
		if dbErr := s.db.WithContext(ctx).Model(&models.P2PAd{}).
			Where("id = ?", ad.ID).
			Update("rate", effectiveRate).Error; dbErr != nil {
			log.Printf("[p2p OpenTrade] cache refresh failed for ad=%s: %v", ad.ID, dbErr)
		}
	}

	s.audit(ctx, models.P2PAuditTradeOpened, &buyerID, &ad.ID, &trade.ID, map[string]any{
		"tokenAmount": trade.TokenAmount,
		"fiatAmount":  trade.FiatAmount,
	})
	s.emit(events.P2PTradeOpened, buyerID, "P2P trade opened", "", map[string]any{
		"tradeId": trade.ID, "adId": ad.ID,
	})
	s.emit(events.P2PTradeOpened, ad.SellerID, "Buyer opened a trade on your ad", "", map[string]any{
		"tradeId": trade.ID, "adId": ad.ID,
	})
	s.systemMsg(ctx, trade.ID, fmt.Sprintf(
		"Trade opened. Buyer to send %s %s to seller's payment method within 30 minutes.",
		trade.FiatAmount, trade.FiatCurrency))
	return trade, unsignedTx, nil
}

// openTradeWithDeposit handles the buy-ad / JIT-funded open. The opener
// (`openerID`/`openerAddress`) is the USER selling crypto — on a buy-ad the
// lifecycle roles invert relative to a sell-ad:
//
//	SellerID = openerUserID (depositor / crypto provider / releaser)
//	BuyerID  = ad.SellerID  (the merchant ad-poster / crypto recipient)
//	Funding  = DepositedByTaker
//
// No voucher, no signature, no on-chain pool: the user deposits crypto JIT via
// openTradeWithDeposit. ad.Remaining/Reserved are off-chain capacity counters
// (decrement on open). Self-take (merchant opening their own buy-ad) is the
// `ad.SellerID == openerID` case already rejected by OpenTrade's guard before
// we get here.
func (s *P2PTradeService) openTradeWithDeposit(ctx context.Context, ad *models.P2PAd, openerID uuid.UUID, openerAddress string, params OpenTradeParams) (*models.P2PTrade, []byte, error) {
	if !common.IsHexAddress(openerAddress) {
		return nil, nil, fmt.Errorf("%w: opener address", ErrInvalidParams)
	}
	if ad.SellerAddress == "" || !common.IsHexAddress(ad.SellerAddress) {
		return nil, nil, fmt.Errorf("%w: buy-ad missing merchant wallet", ErrInvalidTransition)
	}

	effectiveRate, err := s.ComputeEffectiveRate(ad)
	if err != nil {
		return nil, nil, err
	}
	// Authoritative-amount selection: on the sell side the user specifies the
	// exact crypto amount to sell (TokenAmount) so the app can approve that
	// precise allowance before the deposit. Fall back to fiat-derived when a
	// caller only supplies fiat (keeps the path usable fiat-first).
	var tokenAmount, fiatAmount *big.Int
	if strings.TrimSpace(params.TokenAmount) != "" {
		tokenAmount, fiatAmount, err = computeFiatFromToken(params.TokenAmount, effectiveRate, tokenDecimalsFor(ad.ChainID, ad.Token))
	} else {
		tokenAmount, fiatAmount, err = computeTokenFromFiat(params.FiatAmount, effectiveRate, tokenDecimalsFor(ad.ChainID, ad.Token))
	}
	if err != nil {
		return nil, nil, err
	}
	if err := checkFiatLimits(fiatAmount, string(ad.MinLimit), string(ad.MaxLimit)); err != nil {
		return nil, nil, err
	}
	remaining, _ := new(big.Int).SetString(ad.Remaining, 10)
	if remaining == nil || tokenAmount.Cmp(remaining) > 0 {
		return nil, nil, ErrInsufficientRemaining
	}
	if tokenAmount.Sign() <= 0 {
		return nil, nil, fmt.Errorf("%w: derived token_amount=0", ErrInvalidParams)
	}
	if tokenAmount.Cmp(uint96Max) > 0 {
		return nil, nil, ErrAmountOverflow
	}
	// Buy-ad: the MERCHANT pays fiat TO the taker, so the payment method must be
	// the TAKER's OWN account (where they receive fiat) — NOT the ad's allowlist
	// (those are the merchant's). Validate it's owned by the opener (the depositing
	// user/crypto-seller) and matches the trade currency.
	if err := s.validatePaymentMethodsOwned(openerID, []uuid.UUID{params.PaymentMethodID}, ad.FiatCurrency); err != nil {
		return nil, nil, err
	}

	tokenAddr, err := s.resolveTokenAddress(ad.ChainID, ad.Token)
	if err != nil {
		return nil, nil, err
	}

	trade := &models.P2PTrade{
		ID:              uuid.New(),
		AdID:            ad.ID,
		SellerID:        openerID,    // user deposits + releases crypto
		BuyerID:         ad.SellerID, // merchant receives crypto
		OpenerID:        &openerID,   // buy-ad: the depositing user opens
		Token:           ad.Token,
		ChainID:         ad.ChainID,
		Funding:         models.P2PTradeFundingDepositedByTaker,
		TokenAmount:     tokenAmount.String(),
		FiatAmount:      fiatAmount.String(),
		FiatCurrency:    ad.FiatCurrency,
		Rate:            effectiveRate,
		PaymentMethodID: params.PaymentMethodID,
		Status:          models.P2PTradeStatusPendingPayment,
		ExpiresAt:       s.clock.Now().Add(p2pPaymentWindow),
	}
	trade.OnChainTradeID = HashIDHex(trade.ID)
	onChainTradeID := HashID(trade.ID)

	// from = opener wallet (depositor / on-chain seller); merchant = ad poster
	// wallet (on-chain buyer / cryptoRecipient).
	escrow, _, _, err := s.resolve(ad.ChainID)
	if err != nil {
		return nil, nil, err
	}
	unsignedTx, err := escrow.PrepareOpenTradeWithDeposit(onChainTradeID, tokenAddr, tokenAmount, ad.SellerAddress, openerAddress)
	if err != nil {
		// Map the opaque prep failure like the sell-ad path: a revert (e.g. the
		// taker's wallet can't cover the deposit, token not accepted) → 422 with
		// the reason; a transport/RPC error → 503. Avoids a generic 500.
		log.Printf("[p2p openTradeWithDeposit] prepare failed: ad=%s opener=%s token=%s amount=%s err=%v",
			ad.ID, openerAddress, ad.Token, tokenAmount, err)
		if isChainRevert(err) {
			return nil, nil, fmt.Errorf("%w: %v", ErrTradePrepareReverted, err)
		}
		return nil, nil, fmt.Errorf("%w: %v", ErrChainUnavailable, err)
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// One active trade per (ad, opener): on a buy-ad the opener (crypto seller)
		// is stored as SellerID. Blocks a double-open from double-debiting capacity.
		if active, e := s.countActiveTradesForOpener(tx, ad.ID, "seller_id", openerID); e != nil {
			return e
		} else if active > 0 {
			return ErrActiveTradeExists
		}
		// Decrement off-chain capacity (not an on-chain pool).
		res := tx.Model(&models.P2PAd{}).
			Where("id = ? AND status = ? AND CAST(remaining AS NUMERIC) >= CAST(? AS NUMERIC)",
				ad.ID, models.P2PAdStatusActive, tokenAmount.String()).
			Updates(map[string]any{
				"remaining":  gorm.Expr("CAST(remaining AS NUMERIC) - CAST(? AS NUMERIC)", tokenAmount.String()),
				"reserved":   gorm.Expr("CAST(reserved AS NUMERIC) + CAST(? AS NUMERIC)", tokenAmount.String()),
				"updated_at": s.clock.Now(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrInsufficientRemaining
		}
		return tx.Create(trade).Error
	})
	if err != nil {
		return nil, nil, err
	}

	if ad.RateType == models.P2PAdRateTypeFloat {
		if dbErr := s.db.WithContext(ctx).Model(&models.P2PAd{}).
			Where("id = ?", ad.ID).
			Update("rate", effectiveRate).Error; dbErr != nil {
			log.Printf("[p2p openTradeWithDeposit] cache refresh failed for ad=%s: %v", ad.ID, dbErr)
		}
	}

	s.audit(ctx, models.P2PAuditTradeOpened, &openerID, &ad.ID, &trade.ID, map[string]any{
		"side":        "buy",
		"tokenAmount": trade.TokenAmount,
		"fiatAmount":  trade.FiatAmount,
	})
	s.emit(events.P2PTradeOpened, openerID, "P2P trade opened", "", map[string]any{
		"tradeId": trade.ID, "adId": ad.ID, "side": "buy",
	})
	s.emit(events.P2PTradeOpened, ad.SellerID, "A user opened a trade on your buy ad", "", map[string]any{
		"tradeId": trade.ID, "adId": ad.ID, "side": "buy",
	})
	s.systemMsg(ctx, trade.ID, fmt.Sprintf(
		"Trade opened. Seller deposited %s base units of %s; merchant to send %s %s within 30 minutes.",
		trade.TokenAmount, trade.Token, trade.FiatAmount, trade.FiatCurrency))
	return trade, unsignedTx, nil
}

// ConfirmOpenTrade is called by the OPENER (the user who broadcast the open
// tx). For a sell-ad the opener is the BuyerID; for a buy-ad (JIT deposit) the
// opener is the SellerID (the depositing user). `openerAddress` is their
// wallet. The on-chain event differs by funding: TradeOpened (voucher) vs
// TradeOpenedWithDeposit (JIT).
func (s *P2PTradeService) ConfirmOpenTrade(ctx context.Context, tradeID, openerID uuid.UUID, openerAddress, txHash string) (*models.P2PTrade, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	// The opener is the party who funds + broadcasts the open: buyer on a
	// pooled sell-ad, seller (depositor) on a JIT buy-ad.
	opener := trade.BuyerID
	if trade.Funding == models.P2PTradeFundingDepositedByTaker {
		opener = trade.SellerID
	}
	if opener != openerID {
		return nil, ErrForbidden
	}
	if trade.OpenTxHash == txHash && trade.OpenTxHash != "" {
		return &trade, nil
	}
	if trade.Status != models.P2PTradeStatusPendingPayment {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}

	expected, _ := new(big.Int).SetString(trade.TokenAmount, 10)

	// Resolve the trade's chain so the open receipt is verified against the
	// escrow the trade was actually opened on — never a default chain.
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}

	if trade.Funding == models.P2PTradeFundingDepositedByTaker {
		ev, err := escrow.VerifyTradeOpenedWithDeposit(txHash, HashID(trade.ID))
		if err != nil {
			return nil, fmt.Errorf("verify on-chain open (deposit): %w", err)
		}
		if ev.Amount.Cmp(expected) != 0 {
			return nil, fmt.Errorf("on-chain amount %s does not match db %s", ev.Amount, trade.TokenAmount)
		}
		// On a buy-ad the on-chain seller is the depositing user (opener) and
		// the on-chain buyer is the merchant (trade.BuyerID). Cross-check the
		// opener's wallet against the on-chain seller.
		if openerAddress != "" && !strings.EqualFold(ev.Seller.Hex(), openerAddress) {
			return nil, fmt.Errorf("on-chain seller %s does not match opener %s", ev.Seller.Hex(), openerAddress)
		}
	} else {
		ev, err := escrow.VerifyTradeOpened(txHash, HashID(trade.ID))
		if err != nil {
			return nil, fmt.Errorf("verify on-chain open: %w", err)
		}
		if ev.Amount.Cmp(expected) != 0 {
			return nil, fmt.Errorf("on-chain amount %s does not match db %s", ev.Amount, trade.TokenAmount)
		}
		// Cross-check the on-chain buyer matches the caller's wallet. tradeId is
		// server-owned (keccak of UUID); this guard catches a future bug where
		// the verify-receipt path misses a topic-shift.
		if openerAddress != "" && !strings.EqualFold(ev.Buyer.Hex(), openerAddress) {
			return nil, fmt.Errorf("on-chain buyer %s does not match expected %s", ev.Buyer.Hex(), openerAddress)
		}
	}

	openUpdates := map[string]any{"open_tx_hash": txHash, "updated_at": s.clock.Now()}
	// Re-anchor the off-chain deadline to the ON-CHAIN createdAt: the contract
	// enforces cancelByTimeout at createdAt+paymentWindow, but expires_at was set
	// at intent time and drifts earlier if the open mined late — which would nudge
	// a timeout-cancel the contract reverts. Best-effort: keep intent-time on read failure.
	if onchain, rerr := escrow.ReadTrade(HashID(trade.ID)); rerr == nil && onchain.CreatedAt != nil && onchain.CreatedAt.Sign() > 0 {
		openUpdates["expires_at"] = time.Unix(onchain.CreatedAt.Int64(), 0).UTC().Add(p2pPaymentWindow)
	}
	if err := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ?", trade.ID).
		Updates(openUpdates).Error; err != nil {
		return nil, err
	}
	trade.OpenTxHash = txHash
	return &trade, nil
}

func (s *P2PTradeService) MarkPaid(ctx context.Context, tradeID, buyerID uuid.UUID, proofURL string) (*models.P2PTrade, error) {
	if strings.TrimSpace(proofURL) == "" {
		return nil, fmt.Errorf("%w: proof URL required", ErrInvalidParams)
	}
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.BuyerID != buyerID {
		return nil, ErrForbidden
	}
	if trade.Status != models.P2PTradeStatusPendingPayment {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}
	// The escrow is only funded once the open is confirmed on-chain (OpenTxHash is
	// set solely by ConfirmOpenTrade). Refuse mark-paid on a never-broadcast open
	// so a phantom trade can't be flipped to `paid` — a state the watchdog and
	// reconciler can't clear and which blocks the seller from cancelling the ad.
	if strings.TrimSpace(trade.OpenTxHash) == "" {
		return nil, fmt.Errorf("%w: trade not yet opened on-chain", ErrInvalidTransition)
	}
	now := s.clock.Now()
	res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ? AND status = ?", trade.ID, models.P2PTradeStatusPendingPayment).
		Updates(map[string]any{
			"status":          models.P2PTradeStatusPaid,
			"marked_paid_at":  now,
			"proof_image_url": proofURL,
			"updated_at":      now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrInvalidTransition
	}
	trade.Status = models.P2PTradeStatusPaid
	trade.MarkedPaidAt = &now
	trade.ProofImageURL = proofURL
	s.audit(ctx, models.P2PAuditTradeMarkedPaid, &buyerID, &trade.AdID, &trade.ID, map[string]any{"proofUrl": proofURL})
	s.emit(events.P2PTradePaid, buyerID, "Buyer marked payment sent", "", map[string]any{"tradeId": trade.ID})
	s.emit(events.P2PTradePaid, trade.SellerID, "Buyer marked payment sent — please confirm", "", map[string]any{"tradeId": trade.ID})
	s.systemMsg(ctx, trade.ID, "Buyer marked payment as sent. Seller — verify your bank and release.")
	return &trade, nil
}

func (s *P2PTradeService) PrepareRelease(ctx context.Context, tradeID, sellerID uuid.UUID, sellerAddress string) (*ReleaseResult, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.SellerID != sellerID {
		return nil, ErrForbidden
	}
	if !common.IsHexAddress(sellerAddress) {
		return nil, fmt.Errorf("%w: invalid sellerAddress", ErrInvalidParams)
	}
	// Allow Release from pending_payment OR paid OR releasing (retry).
	// Symmetric with the contract relax: seller's signature is the gate,
	// not the buyer's (unverifiable) off-chain markPaid claim.
	if trade.Status != models.P2PTradeStatusPendingPayment &&
		trade.Status != models.P2PTradeStatusPaid &&
		trade.Status != models.P2PTradeStatusReleasing {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}

	// Resolve the trade's chain BEFORE the status flip so an unconfigured chain
	// fails loud without stranding the trade in `releasing`.
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}

	// Transition (Pending|Paid) → Releasing FIRST (atomically), then prepare
	// the unsigned tx. This closes the race with RaiseDispute: a concurrent
	// dispute will win the optimistic lock here, and the seller will get
	// ErrInvalidTransition instead of obtaining a signable release-tx that
	// could later land on-chain after dispute. If already Releasing (retry),
	// just bump updated_at so the reconciler's stale-release window resets.
	if trade.Status == models.P2PTradeStatusPaid || trade.Status == models.P2PTradeStatusPendingPayment {
		res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
			Where("id = ? AND status IN ?", trade.ID,
				[]string{models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid}).
			Updates(map[string]any{"status": models.P2PTradeStatusReleasing, "updated_at": s.clock.Now()})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			// Lost the race to a concurrent dispute or a duplicate retry.
			return nil, ErrInvalidTransition
		}
		s.audit(ctx, models.P2PAuditTradeReleasing, &sellerID, &trade.AdID, &trade.ID, nil)
		s.emit(events.P2PTradeReleasing, sellerID, "Releasing USDT…", "", map[string]any{"tradeId": trade.ID})
		// Notify the buyer too — they should know their funds are on the way as
		// soon as the seller starts releasing, not only once it confirms. (The
		// other lifecycle steps already notify both parties; release-start was
		// the one seller-only emit.) Message is side-aware: on a buy-ad the
		// on-chain buyer is the merchant receiving the depositor's crypto.
		buyerReleasingMsg := "The seller is releasing your USDT — it should arrive shortly."
		if trade.Funding == models.P2PTradeFundingDepositedByTaker {
			buyerReleasingMsg = "The seller is releasing the crypto — it should arrive shortly."
		}
		s.emit(events.P2PTradeReleasing, trade.BuyerID, "Release in progress", buyerReleasingMsg, map[string]any{"tradeId": trade.ID})
	} else {
		// Retry path: trade was already Releasing. Bump updated_at so the
		// chain reconciler's stale-release rollback window resets — protects
		// the seller mid-signing from a false-positive rollback.
		s.db.WithContext(ctx).Model(&models.P2PTrade{}).
			Where("id = ?", trade.ID).
			Update("updated_at", s.clock.Now())
	}

	unsignedTx, err := escrow.PrepareRelease(HashID(trade.ID), sellerAddress)
	if err != nil {
		// We flipped (Pending|Paid) → Releasing above; roll it back so a failed
		// prepare doesn't strand the trade in `releasing` for the reconciler's
		// 10-min stale window. Guarded on status=releasing so we never clobber a
		// trade that legitimately advanced in the meantime.
		if trade.Status == models.P2PTradeStatusPaid || trade.Status == models.P2PTradeStatusPendingPayment {
			s.db.WithContext(ctx).Model(&models.P2PTrade{}).
				Where("id = ? AND status = ?", trade.ID, models.P2PTradeStatusReleasing).
				Update("status", trade.Status)
		}
		return nil, fmt.Errorf("prepare release: %w", err)
	}
	return &ReleaseResult{UnsignedTx: unsignedTx}, nil
}

func (s *P2PTradeService) ConfirmRelease(ctx context.Context, tradeID, sellerID uuid.UUID, txHash string) (*models.P2PTrade, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.SellerID != sellerID {
		return nil, ErrForbidden
	}
	if trade.Status == models.P2PTradeStatusReleased && trade.ReleaseTxHash == txHash {
		return &trade, nil
	}
	if trade.Status != models.P2PTradeStatusPaid && trade.Status != models.P2PTradeStatusReleasing {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}
	ev, err := escrow.VerifyTradeReleased(txHash, HashID(trade.ID))
	if err != nil {
		return nil, fmt.Errorf("verify on-chain release: %w", err)
	}
	return s.confirmReleaseCore(ctx, &trade, txHash, &sellerID, ev)
}

// confirmReleaseCore is the post-verify settlement body shared by the seller
// path (ConfirmRelease) and the system-driven path (AutoRelease): money-invariant
// check, the DB transition (paid|releasing → released) with ad-reserved drain +
// exhausted-flip, fee accrual, audit, emits and the system chat message. The
// caller has already verified the on-chain release event (`ev`). `auditActor` is
// the actor recorded in the audit log — &sellerID for the seller path, nil for
// the system path.
func (s *P2PTradeService) confirmReleaseCore(ctx context.Context, trade *models.P2PTrade, txHash string, auditActor *uuid.UUID, ev *P2PTradeReleasedEvent) (*models.P2PTrade, error) {
	// Money-invariant check: AmountToBuyer + FeeToSystem must equal the
	// trade amount the seller locked. If the on-chain emit reports anything
	// else, refuse to mark the trade settled — the reconciler will pick it
	// up. This is the only check between the chain event and the DB
	// declaring funds delivered.
	expected, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	total := new(big.Int).Add(ev.AmountToBuyer, ev.FeeToSystem)
	if expected != nil && total.Cmp(expected) != 0 {
		return nil, fmt.Errorf("on-chain release total %s does not match trade amount %s", total, trade.TokenAmount)
	}

	now := s.clock.Now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.P2PTrade{}).
			Where("id = ? AND status IN ?", trade.ID,
				[]string{models.P2PTradeStatusPaid, models.P2PTradeStatusReleasing}).
			Updates(map[string]any{
				"status":          models.P2PTradeStatusReleased,
				"release_tx_hash": txHash,
				"released_at":     now,
				"updated_at":      now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrInvalidTransition
		}
		if err := tx.Model(&models.P2PAd{}).
			Where("id = ?", trade.AdID).
			Updates(map[string]any{
				"reserved":   gorm.Expr("CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)", trade.TokenAmount),
				"updated_at": now,
			}).Error; err != nil {
			return err
		}
		// H4: auto-flip the ad to Exhausted when its capacity is fully
		// drained. After this update the ad is no longer surfaced in the
		// active-only marketplace listing and a future closeAd is a no-op.
		return tx.Model(&models.P2PAd{}).
			Where("id = ? AND status = ? AND CAST(remaining AS NUMERIC) = 0 AND CAST(reserved AS NUMERIC) = 0",
				trade.AdID, models.P2PAdStatusActive).
			Update("status", models.P2PAdStatusExhausted).Error
	})
	if err != nil {
		return nil, err
	}
	// NOTE: release accounting is identical for both funding types — the
	// reserved capacity (pool-locked for sell-ads, off-chain-counter for
	// buy-ads) is consumed permanently and `remaining` is never re-credited.
	// Only the recipient labelling differs (cosmetic).
	trade.Status = models.P2PTradeStatusReleased
	trade.ReleaseTxHash = txHash
	trade.ReleasedAt = &now
	// Accrue the realized on-chain fee into the fee/TP v2 engine (flag-gated,
	// async, idempotent on p2p_release/trade.ID). The event carries the exact
	// fee that hit-chain.
	s.accrueP2PReleaseFee(trade, ev.FeeToSystem)
	s.audit(ctx, models.P2PAuditTradeReleased, auditActor, &trade.AdID, &trade.ID, map[string]any{
		"txHash":        txHash,
		"funding":       trade.Funding,
		"amountToBuyer": ev.AmountToBuyer.String(),
		"feeToSystem":   ev.FeeToSystem.String(),
	})
	s.emit(events.P2PTradeReleased, trade.SellerID, "Trade released", "", map[string]any{"tradeId": trade.ID})
	if trade.Funding == models.P2PTradeFundingDepositedByTaker {
		s.emit(events.P2PTradeReleased, trade.BuyerID, "Crypto received in your wallet", "", map[string]any{"tradeId": trade.ID})
		s.systemMsg(ctx, trade.ID, fmt.Sprintf("Trade released. %s base units sent to merchant; %s in platform fee.",
			ev.AmountToBuyer, ev.FeeToSystem))
	} else {
		s.emit(events.P2PTradeReleased, trade.BuyerID, "USDT received in your wallet", "", map[string]any{"tradeId": trade.ID})
		s.systemMsg(ctx, trade.ID, fmt.Sprintf("Trade released. %s base units sent to buyer; %s in platform fee.",
			ev.AmountToBuyer, ev.FeeToSystem))
	}
	return trade, nil
}

// SystemMarkPaid flips a PM trade pending_payment → paid on a verified inbound
// fiat credit (no proof image, no buyer identity gate — the Nomba match IS the
// proof). ref is a provenance marker, e.g. "nomba:<eventID>".
func (s *P2PTradeService) SystemMarkPaid(ctx context.Context, tradeID uuid.UUID, ref string) (*models.P2PTrade, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.Status != models.P2PTradeStatusPendingPayment {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}
	if strings.TrimSpace(trade.OpenTxHash) == "" {
		return nil, fmt.Errorf("%w: trade not yet opened on-chain", ErrInvalidTransition)
	}
	now := s.clock.Now()
	res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ? AND status = ?", trade.ID, models.P2PTradeStatusPendingPayment).
		Updates(map[string]any{
			"status":          models.P2PTradeStatusPaid,
			"marked_paid_at":  now,
			"proof_image_url": ref,
			"updated_at":      now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrInvalidTransition
	}
	trade.Status = models.P2PTradeStatusPaid
	trade.MarkedPaidAt = &now
	trade.ProofImageURL = ref
	s.audit(ctx, models.P2PAuditTradeMarkedPaid, nil, &trade.AdID, &trade.ID, map[string]any{"system": ref})
	s.systemMsg(ctx, trade.ID, "Payment received and verified automatically. Releasing crypto.")
	return &trade, nil
}

// AutoRelease settles a PM trade via the platform admin EOA: flip → releasing
// (system-driven), build the unsigned adminRelease tx, sign+broadcast through
// the ReleaseSigner, verify the on-chain event, and confirm via confirmReleaseCore.
func (s *P2PTradeService) AutoRelease(ctx context.Context, tradeID uuid.UUID, signer ReleaseSigner) (*models.P2PTrade, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.Status == models.P2PTradeStatusReleased {
		return &trade, nil // idempotent
	}
	if trade.Status != models.P2PTradeStatusPendingPayment &&
		trade.Status != models.P2PTradeStatusPaid &&
		trade.Status != models.P2PTradeStatusReleasing {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}
	// Capture the pre-flip status so a post-flip failure can roll it back.
	priorStatus := trade.Status
	if trade.Status != models.P2PTradeStatusReleasing {
		res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
			Where("id = ? AND status IN ?", trade.ID,
				[]string{models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid}).
			Updates(map[string]any{"status": models.P2PTradeStatusReleasing, "updated_at": s.clock.Now()})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			return nil, ErrInvalidTransition
		}
	}
	// rollback reverts the (Pending|Paid) → Releasing flip this call made, so a
	// transient prepare/broadcast/verify error doesn't strand the trade in
	// `releasing` until the reconciler's stale window (~10 min). Guarded two
	// ways: (1) only if WE owned the flip (priorStatus was Pending|Paid; if the
	// trade entered AutoRelease already Releasing we never flipped, so we don't
	// touch it — mirrors PrepareRelease), and (2) only while the row is still
	// Releasing, so we never clobber a trade that legitimately advanced.
	//
	// Safe to call on the verify-error path too: if the release actually landed
	// on-chain, reverting to priorStatus (paid|pending_payment) is harmless —
	// the chain reconciler walks all non-terminal trades and re-settles any whose
	// on-chain status is Released to `released` regardless of DB status (see
	// ReconcileTrade → reconcileToReleased, which gates on status NOT IN
	// terminal, not on `releasing`). So a rolled-back-but-actually-released trade
	// converges to `released` on the next reconcile pass.
	rollback := func() {
		if priorStatus == models.P2PTradeStatusPendingPayment || priorStatus == models.P2PTradeStatusPaid {
			s.db.WithContext(ctx).Model(&models.P2PTrade{}).
				Where("id = ? AND status = ?", trade.ID, models.P2PTradeStatusReleasing).
				Update("status", priorStatus)
		}
	}
	unsignedTx, err := escrow.PrepareAdminRelease(HashID(trade.ID), signer.Address())
	if err != nil {
		rollback()
		return nil, fmt.Errorf("prepare admin release: %w", err)
	}
	txHash, err := signer.SignAndBroadcast(ctx, unsignedTx)
	if err != nil {
		rollback()
		return nil, fmt.Errorf("sign+broadcast admin release: %w", err)
	}
	ev, err := escrow.VerifyTradeReleased(txHash, HashID(trade.ID))
	if err != nil {
		rollback()
		return nil, fmt.Errorf("verify on-chain release: %w", err)
	}
	return s.confirmReleaseCore(ctx, &trade, txHash, nil, ev)
}

// PrepareCancel prepares an unsigned `cancelByTimeout` tx for a trade whose
// pending-payment window has elapsed. The contract makes this permissionless
// on-chain (anyone may call after 30 minutes), but the service restricts the
// API to participants — seller or buyer — so the surface stays predictable.
// Funds return to ad.remaining on confirm.
func (s *P2PTradeService) PrepareCancel(ctx context.Context, tradeID, userID uuid.UUID, callerAddress string) ([]byte, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.BuyerID != userID && trade.SellerID != userID {
		return nil, ErrForbidden
	}
	if trade.Status != models.P2PTradeStatusPendingPayment {
		return nil, fmt.Errorf("%w: trade status=%s (only pending_payment can be timed out)",
			ErrInvalidTransition, trade.Status)
	}
	// Contract enforces `block.timestamp >= createdAt + TIMEOUT`. We
	// pre-check off-chain so callers get a clear error instead of a chain
	// revert ("Timeout not elapsed") at gas-estimation time.
	if !s.clock.Now().After(trade.ExpiresAt) {
		return nil, fmt.Errorf("%w: trade has not yet expired (expires_at=%s)",
			ErrInvalidTransition, trade.ExpiresAt.Format(time.RFC3339))
	}
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}
	return escrow.PrepareCancelByTimeout(HashID(trade.ID), callerAddress)
}

// ConfirmCancel verifies the on-chain TradeCancelled event and applies the
// DB transition: trade → cancelled, ad.remaining += amount, ad.reserved
// -= amount. Idempotent on tx hash.
func (s *P2PTradeService) ConfirmCancel(ctx context.Context, tradeID, userID uuid.UUID, txHash string) (*models.P2PTrade, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.BuyerID != userID && trade.SellerID != userID {
		return nil, ErrForbidden
	}
	if trade.Status == models.P2PTradeStatusCancelled {
		return &trade, nil
	}
	if trade.Status != models.P2PTradeStatusPendingPayment {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}

	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}
	ev, err := escrow.VerifyTradeCancelled(txHash, HashID(trade.ID))
	if err != nil {
		return nil, fmt.Errorf("verify on-chain cancel: %w", err)
	}
	// Sanity check: on-chain cancelled amount matches our recorded amount.
	// (v2 TradeCancelled carries `Amount` + a `ReturnedToPool` flag — pooled
	// sell-ads return funds to the seller's `available` pool in-contract;
	// deposited-by-taker buy-ads transfer the crypto back to the user's wallet.)
	expected, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	if expected != nil && ev.Amount.Cmp(expected) != 0 {
		return nil, fmt.Errorf("on-chain cancelled amount %s does not match trade amount %s",
			ev.Amount, trade.TokenAmount)
	}
	// Cross-check the on-chain pool-return flag matches our funding model. A
	// mismatch means the trade's DB funding diverged from chain truth — refuse
	// rather than restore capacity down the wrong branch.
	wantReturnedToPool := trade.Funding == models.P2PTradeFundingPooled
	if ev.ReturnedToPool != wantReturnedToPool {
		return nil, fmt.Errorf("on-chain returnedToPool=%v disagrees with funding=%d",
			ev.ReturnedToPool, trade.Funding)
	}

	now := s.clock.Now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.P2PTrade{}).
			Where("id = ? AND status = ?", trade.ID, models.P2PTradeStatusPendingPayment).
			Updates(map[string]any{
				"status":        models.P2PTradeStatusCancelled,
				"cancel_reason": "timed_out",
				"updated_at":    now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrInvalidTransition
		}
		// Restore ad capacity: reserved -= amount, remaining += amount. For a
		// pooled sell-ad this mirrors the on-chain pool refund; for a buy-ad
		// the crypto already returned to the user's wallet on-chain and this
		// only restores the off-chain capacity counter so the ad can be re-taken.
		return tx.Model(&models.P2PAd{}).
			Where("id = ?", trade.AdID).
			Updates(map[string]any{
				"reserved":   gorm.Expr("CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)", trade.TokenAmount),
				"remaining":  gorm.Expr("CAST(remaining AS NUMERIC) + CAST(? AS NUMERIC)", trade.TokenAmount),
				"updated_at": now,
			}).Error
	})
	if err != nil {
		return nil, err
	}
	trade.Status = models.P2PTradeStatusCancelled
	trade.CancelReason = "timed_out"

	s.audit(ctx, models.P2PAuditTradeCancelled, &userID, &trade.AdID, &trade.ID, map[string]any{
		"txHash":         txHash,
		"funding":        trade.Funding,
		"amount":         ev.Amount.String(),
		"returnedToPool": ev.ReturnedToPool,
	})
	// Notify both participants — wording differs by funding (sell-ad funds
	// return to the merchant's pool; buy-ad crypto returns to the user's wallet).
	if trade.Funding == models.P2PTradeFundingDepositedByTaker {
		// On a buy-ad the SellerID is the depositing user (crypto returned to
		// their wallet); the BuyerID is the merchant.
		s.emit(events.P2PTradeCancelled, trade.SellerID, "Trade cancelled — crypto returned to your wallet",
			"", map[string]any{"tradeId": trade.ID})
		if trade.BuyerID != trade.SellerID {
			s.emit(events.P2PTradeCancelled, trade.BuyerID, "Trade cancelled (timed out)",
				"", map[string]any{"tradeId": trade.ID})
		}
		s.systemMsg(ctx, trade.ID, "Trade cancelled after 30-minute payment window expired. Crypto returned to the seller's wallet.")
	} else {
		s.emit(events.P2PTradeCancelled, trade.SellerID, "Trade cancelled — funds returned to your ad",
			"", map[string]any{"tradeId": trade.ID})
		if trade.BuyerID != trade.SellerID {
			s.emit(events.P2PTradeCancelled, trade.BuyerID, "Trade cancelled (timed out)",
				"", map[string]any{"tradeId": trade.ID})
		}
		s.systemMsg(ctx, trade.ID, "Trade cancelled after 30-minute payment window expired. Funds returned to seller's ad.")
	}
	return &trade, nil
}

// ─── Dispute lifecycle (Step 8) ─────────────────────────────────────────────

// PrepareRaiseDispute prepares an on-chain raiseDispute tx and persists a dispute
// row tied to the trade. The trade itself isn't transitioned until
// ConfirmRaiseDispute verifies the receipt — the dispute row sits in `open`
// state in the interim so the UI can surface "dispute pending broadcast".
// Either buyer or seller (in Paid state only) may dispute.
func (s *P2PTradeService) PrepareRaiseDispute(ctx context.Context, tradeID, userID uuid.UUID, callerAddress, reason string, evidenceURLs []string) ([]byte, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason required", ErrInvalidParams)
	}
	// Evidence is optional: image URLs uploaded client-side (Cloudinary). Cap the
	// count and require http(s) URLs; store as JSON on the dispute row.
	evidenceJSON := ""
	if len(evidenceURLs) > 0 {
		if len(evidenceURLs) > 6 {
			return nil, fmt.Errorf("%w: at most 6 evidence images", ErrInvalidParams)
		}
		for _, u := range evidenceURLs {
			if t := strings.TrimSpace(u); t == "" || !strings.HasPrefix(t, "http") {
				return nil, fmt.Errorf("%w: evidence must be http(s) image URLs", ErrInvalidParams)
			}
		}
		b, _ := json.Marshal(evidenceURLs)
		evidenceJSON = string(b)
	}
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	var role string
	switch userID {
	case trade.BuyerID:
		role = models.P2PDisputeRoleBuyer
	case trade.SellerID:
		role = models.P2PDisputeRoleSeller
	default:
		return nil, ErrForbidden
	}
	// Disputes are allowed from pending_payment OR paid — symmetric with
	// the contract (P2PEscrow.sol relax). A buyer who actually paid fiat but
	// didn't call markPaid still needs a dispute path; rejecting them here
	// would force them to call raiseDispute on chain directly and rely on
	// the reconciler back-fill, defeating the API design.
	if trade.Status != models.P2PTradeStatusPaid && trade.Status != models.P2PTradeStatusPendingPayment {
		return nil, fmt.Errorf("%w: trade status=%s (only pending or paid trades can dispute)",
			ErrInvalidTransition, trade.Status)
	}
	// A never-opened-on-chain trade has no escrowed funds to dispute; refuse so a
	// phantom trade can't be escalated to `disputed` and pull in an admin.
	if strings.TrimSpace(trade.OpenTxHash) == "" {
		return nil, fmt.Errorf("%w: trade not yet opened on-chain", ErrInvalidTransition)
	}
	// Reject duplicate open dispute. Unique constraint on TradeID makes the
	// race unlikely; this is the friendly-error path.
	var existing models.P2PDispute
	err := s.db.WithContext(ctx).
		Where("trade_id = ? AND status = ?", trade.ID, models.P2PDisputeStatusOpen).
		First(&existing).Error
	if err == nil {
		return nil, ErrDisputeAlreadyOpen
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Resolve the trade's chain BEFORE persisting the dispute row so an
	// unconfigured chain fails loud without leaving an orphan dispute.
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}

	dispute := &models.P2PDispute{
		ID:           uuid.New(),
		TradeID:      trade.ID,
		RaisedBy:     userID,
		RaisedRole:   role,
		Reason:       reason,
		EvidenceURLs: evidenceJSON,
		Status:       models.P2PDisputeStatusOpen,
	}
	if err := s.db.WithContext(ctx).Create(dispute).Error; err != nil {
		return nil, fmt.Errorf("persist dispute: %w", err)
	}

	unsignedTx, err := escrow.PrepareRaiseDispute(HashID(trade.ID), callerAddress)
	if err != nil {
		// Best-effort rollback so a failed prep doesn't leave a phantom
		// dispute row blocking future retries.
		_ = s.db.WithContext(ctx).Delete(&models.P2PDispute{}, "id = ?", dispute.ID).Error
		return nil, fmt.Errorf("prepare raiseDispute: %w", err)
	}

	s.audit(ctx, models.P2PAuditTradeDisputed, &userID, &trade.AdID, &trade.ID, map[string]any{
		"role":   role,
		"reason": reason,
	})
	// Notify only the raiser at prepare time. The counter-party notice and the
	// "funds frozen" chat message fire in ConfirmRaiseDispute — after the dispute
	// is actually on chain — so a never-broadcast prepare can't signal the
	// counter-party or freeze the trade chat.
	s.emit(events.P2PTradeDisputed, userID, "Dispute prepared — sign to submit", "", map[string]any{
		"tradeId": trade.ID, "role": role,
	})

	return unsignedTx, nil
}

// ConfirmRaiseDispute verifies the on-chain TradeDisputed event and
// transitions the trade to Disputed. Idempotent across retries.
func (s *P2PTradeService) ConfirmRaiseDispute(ctx context.Context, tradeID, userID uuid.UUID, txHash string) (*models.P2PTrade, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.BuyerID != userID && trade.SellerID != userID {
		return nil, ErrForbidden
	}
	if trade.Status == models.P2PTradeStatusDisputed {
		return &trade, nil
	}
	if trade.Status != models.P2PTradeStatusPaid && trade.Status != models.P2PTradeStatusPendingPayment {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}

	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}
	if _, err := escrow.VerifyTradeDisputed(txHash, HashID(trade.ID)); err != nil {
		return nil, fmt.Errorf("verify on-chain dispute: %w", err)
	}

	res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ? AND status IN ?", trade.ID,
			[]string{models.P2PTradeStatusPaid, models.P2PTradeStatusPendingPayment}).
		Updates(map[string]any{
			"status":     models.P2PTradeStatusDisputed,
			"updated_at": s.clock.Now(),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrInvalidTransition
	}
	trade.Status = models.P2PTradeStatusDisputed

	// Symmetric with the other Confirm* paths: emit an audit row + bus event
	// once the on-chain transition is verified. Without this the operator
	// timeline only shows the intent (RaiseDispute) and not the chain-side
	// settle, making it hard to investigate disputes after the fact.
	s.audit(ctx, models.P2PAuditTradeDisputeConfirmed, &userID, &trade.AdID, &trade.ID,
		map[string]any{"txHash": txHash})
	s.emit(events.P2PTradeDisputed, userID, "Dispute confirmed on chain", "",
		map[string]any{"tradeId": trade.ID})
	// Now that the dispute is on chain, notify the counter-party and freeze the
	// trade chat (deferred from PrepareRaiseDispute).
	other := trade.SellerID
	if userID == trade.SellerID {
		other = trade.BuyerID
	}
	s.emit(events.P2PTradeDisputed, other, "Counter-party opened a dispute on this trade", "",
		map[string]any{"tradeId": trade.ID})
	s.systemMsg(ctx, trade.ID, "A dispute has been opened. Funds are frozen pending admin review.")
	return &trade, nil
}

// PrepareAdminResolveDispute prepares the on-chain adminResolve tx. Admin can side
// with the buyer (release fee-split to buyer) or seller (refund to ad pool).
// The off-chain note is persisted with the dispute row on confirm.
func (s *P2PTradeService) PrepareAdminResolveDispute(ctx context.Context, tradeID, adminID uuid.UUID, adminAddress string, refundBuyer bool, note string) ([]byte, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.Status != models.P2PTradeStatusDisputed {
		return nil, fmt.Errorf("%w: trade status=%s (only disputed can be resolved)",
			ErrInvalidTransition, trade.Status)
	}
	var dispute models.P2PDispute
	if err := s.db.WithContext(ctx).
		Where("trade_id = ? AND status = ?", trade.ID, models.P2PDisputeStatusOpen).
		First(&dispute).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDisputeNotFound
		}
		return nil, err
	}

	// Admin resolution MUST route by the disputed trade's chain, never a
	// default — resolving on the wrong escrow would move the wrong funds.
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}
	unsignedTx, err := escrow.PrepareAdminResolve(HashID(trade.ID), refundBuyer, adminAddress)
	if err != nil {
		return nil, fmt.Errorf("prepare adminResolve: %w", err)
	}

	// Stash the resolution note + resolver as intent. Deliberately do NOT persist
	// refunded_to_buyer here: it is the authoritative refund branch and is written
	// ONLY by ConfirmAdminResolveDispute from the on-chain TradeResolved event. If
	// it were set pre-broadcast, the reconciler (reconcileToResolved keys ad-capacity
	// on RefundedToBuyer != nil) would treat a stale/never-broadcast intent as
	// settled truth and could credit the wrong branch.
	updates := map[string]any{
		"resolved_by": adminID,
		"resolution":  note,
	}
	if err := s.db.WithContext(ctx).Model(&dispute).
		Where("id = ? AND status = ?", dispute.ID, models.P2PDisputeStatusOpen).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	// Pre-broadcast audit so concurrent admins racing this endpoint leave
	// a traceable record of intent — the confirm step writes the final
	// outcome separately.
	s.audit(ctx, models.P2PAuditTradeResolvePrepared, &adminID, &trade.AdID, &trade.ID, map[string]any{
		"refundBuyer": refundBuyer,
		"note":        note,
		"disputeId":   dispute.ID,
	})
	return unsignedTx, nil
}

// ConfirmAdminResolveDispute verifies the on-chain TradeResolved event and
// applies DB state: trade → resolved; dispute → resolved (with resolved_at);
// ad accounting depends on the chain outcome (refundBuyer => reserved--,
// !refundBuyer => reserved-- + remaining++).
func (s *P2PTradeService) ConfirmAdminResolveDispute(ctx context.Context, tradeID, adminID uuid.UUID, txHash string) (*models.P2PTrade, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.Status == models.P2PTradeStatusResolved {
		return &trade, nil
	}
	if trade.Status != models.P2PTradeStatusDisputed {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}

	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}
	ev, err := escrow.VerifyTradeResolved(txHash, HashID(trade.ID))
	if err != nil {
		return nil, fmt.Errorf("verify on-chain resolve: %w", err)
	}

	// Cross-check on-chain amount semantics:
	//   ReleasedToBuyer=true  → AmountMoved + FeeToSystem == trade.amount
	//   ReleasedToBuyer=false → AmountMoved == 0, FeeToSystem == 0
	expected, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	if ev.ReleasedToBuyer {
		total := new(big.Int).Add(ev.AmountMoved, ev.FeeToSystem)
		if expected != nil && total.Cmp(expected) != 0 {
			return nil, fmt.Errorf("on-chain resolve total %s does not match trade amount %s",
				total, trade.TokenAmount)
		}
	} else {
		if ev.AmountMoved.Sign() != 0 || ev.FeeToSystem.Sign() != 0 {
			return nil, fmt.Errorf("on-chain resolve(false) reported non-zero movement: amount=%s fee=%s",
				ev.AmountMoved, ev.FeeToSystem)
		}
	}

	now := s.clock.Now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Update trade.
		res := tx.Model(&models.P2PTrade{}).
			Where("id = ? AND status = ?", trade.ID, models.P2PTradeStatusDisputed).
			Updates(map[string]any{
				"status":            models.P2PTradeStatusResolved,
				"resolved_by_admin": adminID,
				"release_tx_hash":   txHash,
				"updated_at":        now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrInvalidTransition
		}
		// Finalise dispute row using the AUTHORITATIVE chain outcome — not
		// the admin's pre-broadcast intent. If the admin signed a different
		// resolution than they declared in AdminResolveDispute (or some
		// chain-side actor settled it), this overwrites the stale intent.
		if err := tx.Model(&models.P2PDispute{}).
			Where("trade_id = ? AND status = ?", trade.ID, models.P2PDisputeStatusOpen).
			Updates(map[string]any{
				"status":            models.P2PDisputeStatusResolved,
				"resolved_at":       now,
				"resolve_tx_hash":   txHash,
				"refunded_to_buyer": ev.ReleasedToBuyer,
			}).Error; err != nil {
			return err
		}
		// Ad accounting: reserved -= amount in both branches; remaining +=
		// amount only when funds DON'T go to the buyer (refundBuyer=false).
		// This is funding-agnostic at the DB level: for a buy-ad the on-chain
		// refund (refundBuyer=false) transfers crypto back to the user's wallet
		// and this only restores the off-chain capacity counter — no on-chain
		// pool is touched. For a sell-ad it mirrors the on-chain pool credit.
		updates := map[string]any{
			"reserved":   gorm.Expr("CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)", trade.TokenAmount),
			"updated_at": now,
		}
		if !ev.ReleasedToBuyer {
			updates["remaining"] = gorm.Expr(
				"CAST(remaining AS NUMERIC) + CAST(? AS NUMERIC)",
				trade.TokenAmount)
		}
		return tx.Model(&models.P2PAd{}).Where("id = ?", trade.AdID).Updates(updates).Error
	})
	if err != nil {
		return nil, err
	}
	trade.Status = models.P2PTradeStatusResolved
	trade.ResolvedByAdmin = &adminID

	// adminResolve that RELEASES to the buyer is a release transition: the fee
	// was charged on chain (ev.FeeToSystem). Accrue it through the same seam as
	// ConfirmRelease (flag-gated, async, idempotent). The refund-to-seller
	// branch charges no fee (FeeToSystem == 0) so the helper no-ops; gate on
	// ReleasedToBuyer for clarity.
	if ev.ReleasedToBuyer {
		s.accrueP2PReleaseFee(&trade, ev.FeeToSystem)
	}

	s.audit(ctx, models.P2PAuditTradeResolved, &adminID, &trade.AdID, &trade.ID, map[string]any{
		"txHash":          txHash,
		"funding":         trade.Funding,
		"releasedToBuyer": ev.ReleasedToBuyer,
		"amountMoved":     ev.AmountMoved.String(),
		"feeToSystem":     ev.FeeToSystem.String(),
	})
	s.emit(events.P2PTradeResolved, trade.BuyerID, "Dispute resolved", "",
		map[string]any{"tradeId": trade.ID, "releasedToBuyer": ev.ReleasedToBuyer})
	s.emit(events.P2PTradeResolved, trade.SellerID, "Dispute resolved", "",
		map[string]any{"tradeId": trade.ID, "releasedToBuyer": ev.ReleasedToBuyer})
	buyAd := trade.Funding == models.P2PTradeFundingDepositedByTaker
	switch {
	case ev.ReleasedToBuyer && buyAd:
		s.systemMsg(ctx, trade.ID, "Admin resolved dispute in the merchant's favour. Crypto sent to the merchant.")
	case ev.ReleasedToBuyer:
		s.systemMsg(ctx, trade.ID, "Admin resolved dispute in buyer's favour. USDT sent to buyer.")
	case buyAd:
		s.systemMsg(ctx, trade.ID, "Admin resolved dispute in the seller's favour. Crypto returned to the seller's wallet.")
	default:
		s.systemMsg(ctx, trade.ID, "Admin resolved dispute in seller's favour. Funds returned to seller's ad.")
	}

	return &trade, nil
}

// ListDisputes returns disputes filtered by status. Admin-facing.
func (s *P2PTradeService) ListDisputes(ctx context.Context, status string, limit, offset int) ([]models.P2PDispute, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	q := s.db.WithContext(ctx).Model(&models.P2PDispute{}).
		Order("created_at DESC").Limit(limit).Offset(offset)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var out []models.P2PDispute
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// GetDispute returns the dispute for a given trade. Visible to trade
// participants and admins; the handler checks the admin path separately.
func (s *P2PTradeService) GetDispute(ctx context.Context, tradeID, requestor uuid.UUID, isAdmin bool) (*models.P2PDispute, error) {
	var dispute models.P2PDispute
	if err := s.db.WithContext(ctx).
		Where("trade_id = ?", tradeID).
		Order("created_at DESC").
		First(&dispute).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDisputeNotFound
		}
		return nil, err
	}
	if !isAdmin {
		var trade models.P2PTrade
		if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
			return nil, err
		}
		if trade.BuyerID != requestor && trade.SellerID != requestor {
			return nil, ErrForbidden
		}
	}
	return &dispute, nil
}

// ─── Step 9: chain reconciliation ──────────────────────────────────────────

// ReconcileResult is the outcome of one trade's reconciliation pass.
type ReconcileResult struct {
	Changed    bool
	FromStatus string
	ToStatus   string
	Reason     string
}

// ReconcileTrade detects and fixes drift between this trade's DB row and the
// on-chain Trade struct. Idempotent — running it repeatedly on a converged
// trade is a no-op. The reconciler is a SAFETY NET for the prepare/confirm
// path: if the frontend drops the response to a Confirm* call, the DB stays
// stale until the user retries (idempotent path) OR this method runs.
//
// Drift rules (chain is authoritative for funds movement):
//
//	chain Released  & DB !released  → DB → released; apply release accounting
//	chain Cancelled & DB !cancelled → DB → cancelled; restore ad.remaining
//	chain Resolved  & DB !resolved  → DB → resolved; accounting depends on dispute outcome
//	chain Disputed  & DB !disputed  → DB → disputed; back-fill dispute row if missing
//	chain Paid      & DB pending    → DB → paid (someone called markPaid on chain)
//	chain Pending   & DB releasing for > releasingStaleAfter → DB → paid (release tx lost)
//
// Non-actionable cases (no DB change):
//   - chain Pending & DB in (pending_payment, paid, releasing<stale): wait
//   - chain Paid    & DB in (paid, releasing): OK
//   - chain matches DB exactly: nothing to do
//
// The "reconciled" txHash and `cancel_reason="reconciled"` markers mean
// "DB was repaired by the watchdog, not by a confirm-call". Auditors can
// still find the on-chain settlement by querying the contract.
func (s *P2PTradeService) ReconcileTrade(ctx context.Context, tradeID uuid.UUID) (ReconcileResult, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ReconcileResult{}, ErrTradeNotFound
		}
		return ReconcileResult{}, err
	}
	// Terminal states never need reconciliation — even chain drift cannot
	// rescue a Released/Cancelled/Resolved row.
	if isTerminalTradeStatus(trade.Status) {
		return ReconcileResult{}, nil
	}

	// Reconcile against the trade's own chain — fail-loud if it's unconfigured
	// (§8.5: a wrong-chain ReadTrade would compare against the wrong escrow and
	// could mis-correct DB state).
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("resolve escrow for trade chain %d: %w", trade.ChainID, err)
	}
	onchain, err := escrow.ReadTrade(HashID(trade.ID))
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("read on-chain trade: %w", err)
	}
	// Trade not on chain. Either the open tx is still in flight (just wait), or
	// it's a stale orphan: a pending_payment trade whose open was never confirmed
	// on-chain (OpenTxHash empty) and whose payment window has elapsed will never
	// appear on chain. Such a row permanently holds reserved/remaining capacity,
	// so reclaim it — cancel + restore the ad's off-chain counters.
	if !onchain.Exists {
		if trade.Status == models.P2PTradeStatusPendingPayment &&
			trade.OpenTxHash == "" &&
			s.clock.Now().After(trade.ExpiresAt) {
			return s.reconcileOrphanOpen(ctx, &trade)
		}
		return ReconcileResult{}, nil
	}

	// Use the ON-CHAIN funding as the source of truth for the refund branch —
	// the DB column could be wrong (e.g. a buggy open path), and a wrong-branch
	// refund either double-credits a pool or skips a wallet return. The DB
	// accounting below restores only off-chain capacity counters (it never
	// credits an on-chain pool — that movement happens on chain), so it's safe
	// for both fundings, but recording the authoritative value matters for
	// auditing and side-aware messaging.
	funding := onchain.Funding

	// Realized on-chain fee for the release/resolve paths. The reconciler has no
	// TradeReleased event to read FeeToSystem from, so derive it from the view:
	// fee = Amount · FeeBps / 10000. Passed into the release/resolve transitions
	// so they accrue the SAME fee the live confirm paths would (closing the
	// auto-release / admin-resolve gap, design §3). Idempotent on trade.ID.
	onChainFee := onChainTradeFee(onchain)

	switch onchain.Status {
	case OnChainP2PStatusReleased:
		return s.reconcileToReleased(ctx, &trade, funding, onChainFee)
	case OnChainP2PStatusCancelled:
		return s.reconcileToCancelled(ctx, &trade, funding)
	case OnChainP2PStatusResolved:
		return s.reconcileToResolved(ctx, &trade, funding, onChainFee)
	case OnChainP2PStatusDisputed:
		return s.reconcileToDisputed(ctx, &trade)
	case OnChainP2PStatusPaid:
		// Chain Paid + DB pending_payment → someone called on-chain markPaid
		// outside our normal off-chain flow. Surface it.
		if trade.Status == models.P2PTradeStatusPendingPayment {
			now := s.clock.Now()
			res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
				Where("id = ? AND status = ?", trade.ID, models.P2PTradeStatusPendingPayment).
				Updates(map[string]any{
					"status":         models.P2PTradeStatusPaid,
					"marked_paid_at": now,
					"updated_at":     now,
				})
			if res.Error != nil {
				return ReconcileResult{}, res.Error
			}
			if res.RowsAffected > 0 {
				s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
					"from": "pending_payment", "to": "paid",
					"reason": "chain status Paid",
				})
				return ReconcileResult{Changed: true, FromStatus: trade.Status,
					ToStatus: models.P2PTradeStatusPaid, Reason: "chain Paid"}, nil
			}
		}
		return ReconcileResult{}, nil
	case OnChainP2PStatusPending:
		// Release tx lost — DB stuck in `releasing` for too long. Revert
		// to `paid` so the seller can retry. We assume the prepared-but-
		// never-broadcast window is bounded by the user's session; older
		// than that and we give up waiting.
		if trade.Status == models.P2PTradeStatusReleasing &&
			s.clock.Now().Sub(trade.UpdatedAt) > releasingStaleAfter {
			res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
				Where("id = ? AND status = ?", trade.ID, models.P2PTradeStatusReleasing).
				Updates(map[string]any{
					"status":     models.P2PTradeStatusPaid,
					"updated_at": s.clock.Now(),
				})
			if res.Error != nil {
				return ReconcileResult{}, res.Error
			}
			if res.RowsAffected > 0 {
				s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
					"from": "releasing", "to": "paid",
					"reason": "release tx lost; chain still Pending after grace window",
				})
				return ReconcileResult{Changed: true, FromStatus: "releasing",
					ToStatus: models.P2PTradeStatusPaid, Reason: "release tx lost"}, nil
			}
		}
		return ReconcileResult{}, nil
	}
	return ReconcileResult{}, nil
}

// releasingStaleAfter is the grace window before we conclude a release tx is
// lost and roll the DB back to "paid". The seller can then retry. 10 minutes
// gives the user time to sign + chain time to confirm before we intervene.
const releasingStaleAfter = 10 * time.Minute

// p2pPaymentWindow is the buyer's payment deadline. MUST match the contract's
// paymentWindow (30m) — the contract enforces cancelByTimeout at createdAt+window,
// so the DB expires_at is re-anchored to on-chain createdAt in ConfirmOpenTrade.
const p2pPaymentWindow = 30 * time.Minute

func isTerminalTradeStatus(s string) bool {
	return s == models.P2PTradeStatusReleased ||
		s == models.P2PTradeStatusCancelled ||
		s == models.P2PTradeStatusResolved
}

// reconcileToReleased applies a chain Released → DB released drift fix. The ad
// accounting (reserved -= amount; never re-credit remaining) is identical for
// both fundings: on a pooled sell-ad the locked pool moved to the buyer; on a
// deposited-by-taker buy-ad the JIT deposit moved to the merchant. `funding`
// is the on-chain truth, used here only for side-aware messaging/audit — never
// to credit an on-chain pool (that movement already happened on chain).
func (s *P2PTradeService) reconcileToReleased(ctx context.Context, trade *models.P2PTrade, funding uint8, onChainFee *big.Int) (ReconcileResult, error) {
	now := s.clock.Now()
	from := trade.Status
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.P2PTrade{}).
			Where("id = ? AND status NOT IN ?", trade.ID,
				[]string{
					models.P2PTradeStatusReleased,
					models.P2PTradeStatusCancelled,
					models.P2PTradeStatusResolved,
				}).
			Updates(map[string]any{
				"status":          models.P2PTradeStatusReleased,
				"release_tx_hash": "reconciled",
				"released_at":     now,
				"updated_at":      now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		if err := tx.Model(&models.P2PAd{}).
			Where("id = ?", trade.AdID).
			Updates(map[string]any{
				"reserved":   gorm.Expr("CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)", trade.TokenAmount),
				"updated_at": now,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&models.P2PAd{}).
			Where("id = ? AND status = ? AND CAST(remaining AS NUMERIC) = 0 AND CAST(reserved AS NUMERIC) = 0",
				trade.AdID, models.P2PAdStatusActive).
			Update("status", models.P2PAdStatusExhausted).Error
	})
	if err != nil {
		return ReconcileResult{}, err
	}
	// Accrue the realized on-chain fee — same seam as the live ConfirmRelease,
	// closing the auto-release accounting gap (design §3). Idempotent on
	// p2p_release/trade.ID, so a later live confirm (or a repeat reconcile pass)
	// for the same trade is a no-op.
	s.accrueP2PReleaseFee(trade, onChainFee)
	s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
		"from": from, "to": models.P2PTradeStatusReleased,
		"funding": funding,
		"reason":  "chain Released",
	})
	s.emit(events.P2PTradeReleased, trade.SellerID, "Trade released (reconciled)", "", map[string]any{"tradeId": trade.ID})
	if funding == models.P2PTradeFundingDepositedByTaker {
		s.emit(events.P2PTradeReleased, trade.BuyerID, "Crypto received (reconciled)", "", map[string]any{"tradeId": trade.ID})
	} else {
		s.emit(events.P2PTradeReleased, trade.BuyerID, "USDT received (reconciled)", "", map[string]any{"tradeId": trade.ID})
	}
	return ReconcileResult{Changed: true, FromStatus: from, ToStatus: models.P2PTradeStatusReleased,
		Reason: "chain Released"}, nil
}

// reconcileToCancelled applies a chain Cancelled → DB cancelled drift fix.
// Restoring off-chain capacity (reserved -= amount; remaining += amount) is the
// same for both fundings — for a pooled sell-ad the on-chain pool refund is
// mirrored; for a buy-ad the crypto already returned to the user's wallet on
// chain and this only restores the off-chain capacity counter. `funding`
// drives side-aware messaging only.
func (s *P2PTradeService) reconcileToCancelled(ctx context.Context, trade *models.P2PTrade, funding uint8) (ReconcileResult, error) {
	now := s.clock.Now()
	from := trade.Status
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.P2PTrade{}).
			Where("id = ? AND status NOT IN ?", trade.ID,
				[]string{
					models.P2PTradeStatusReleased,
					models.P2PTradeStatusCancelled,
					models.P2PTradeStatusResolved,
				}).
			Updates(map[string]any{
				"status":        models.P2PTradeStatusCancelled,
				"cancel_reason": "reconciled",
				"updated_at":    now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		return tx.Model(&models.P2PAd{}).
			Where("id = ?", trade.AdID).
			Updates(map[string]any{
				"reserved":   gorm.Expr("CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)", trade.TokenAmount),
				"remaining":  gorm.Expr("CAST(remaining AS NUMERIC) + CAST(? AS NUMERIC)", trade.TokenAmount),
				"updated_at": now,
			}).Error
	})
	if err != nil {
		return ReconcileResult{}, err
	}
	s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
		"from": from, "to": models.P2PTradeStatusCancelled,
		"funding": funding,
		"reason":  "chain Cancelled",
	})
	if funding == models.P2PTradeFundingDepositedByTaker {
		// SellerID is the depositing user — crypto returned to their wallet.
		s.emit(events.P2PTradeCancelled, trade.SellerID, "Trade cancelled (reconciled) — crypto returned to your wallet", "", map[string]any{"tradeId": trade.ID})
	} else {
		s.emit(events.P2PTradeCancelled, trade.SellerID, "Trade cancelled (reconciled)", "", map[string]any{"tradeId": trade.ID})
	}
	return ReconcileResult{Changed: true, FromStatus: from, ToStatus: models.P2PTradeStatusCancelled,
		Reason: "chain Cancelled"}, nil
}

// reconcileOrphanOpen reclaims a pending_payment trade whose on-chain open never
// landed (OpenTxHash empty, not on chain, past its payment window). Such a row
// permanently held the ad's reserved/remaining capacity; this cancels it and
// restores the off-chain counters. There is nothing on chain to settle.
func (s *P2PTradeService) reconcileOrphanOpen(ctx context.Context, trade *models.P2PTrade) (ReconcileResult, error) {
	now := s.clock.Now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Guard on status + empty open_tx_hash: if ConfirmOpenTrade landed between
		// our read and here, RowsAffected==0 and we restore nothing (no double-credit).
		res := tx.Model(&models.P2PTrade{}).
			Where("id = ? AND status = ? AND open_tx_hash = ''", trade.ID, models.P2PTradeStatusPendingPayment).
			Updates(map[string]any{
				"status":        models.P2PTradeStatusCancelled,
				"cancel_reason": "reconciled_orphan",
				"updated_at":    now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		return tx.Model(&models.P2PAd{}).
			Where("id = ?", trade.AdID).
			Updates(map[string]any{
				"reserved":   gorm.Expr("CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)", trade.TokenAmount),
				"remaining":  gorm.Expr("CAST(remaining AS NUMERIC) + CAST(? AS NUMERIC)", trade.TokenAmount),
				"updated_at": now,
			}).Error
	})
	if err != nil {
		return ReconcileResult{}, err
	}
	s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
		"from": models.P2PTradeStatusPendingPayment, "to": models.P2PTradeStatusCancelled,
		"reason": "orphan open never confirmed on-chain past expiry",
	})
	return ReconcileResult{Changed: true, FromStatus: models.P2PTradeStatusPendingPayment,
		ToStatus: models.P2PTradeStatusCancelled, Reason: "orphan open reclaimed"}, nil
}

func (s *P2PTradeService) reconcileToDisputed(ctx context.Context, trade *models.P2PTrade) (ReconcileResult, error) {
	if trade.Status == models.P2PTradeStatusDisputed {
		return ReconcileResult{}, nil
	}
	from := trade.Status
	now := s.clock.Now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.P2PTrade{}).
			Where("id = ? AND status NOT IN ?", trade.ID,
				[]string{
					models.P2PTradeStatusReleased,
					models.P2PTradeStatusCancelled,
					models.P2PTradeStatusResolved,
					models.P2PTradeStatusDisputed,
				}).
			Updates(map[string]any{
				"status":     models.P2PTradeStatusDisputed,
				"updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		// Back-fill a dispute row if none exists yet. FirstOrCreate avoids
		// the unique-index race when a phantom row from a failed RaiseDispute
		// already sits in the table (best-effort delete can fail silently).
		// Raised-by defaults to seller as a placeholder; the audit row +
		// reconciler-drift action let operators identify back-filled rows.
		placeholder := models.P2PDispute{
			ID:         uuid.New(),
			TradeID:    trade.ID,
			RaisedBy:   trade.SellerID,
			RaisedRole: models.P2PDisputeRoleSeller,
			Reason:     "back-filled by reconciler (on-chain dispute without local raise)",
			Status:     models.P2PDisputeStatusOpen,
			CreatedAt:  now,
		}
		if err := tx.Where(models.P2PDispute{TradeID: trade.ID}).
			Attrs(placeholder).
			FirstOrCreate(&placeholder).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return ReconcileResult{}, err
	}
	s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
		"from": from, "to": models.P2PTradeStatusDisputed,
		"reason": "chain Disputed",
	})
	return ReconcileResult{Changed: true, FromStatus: from, ToStatus: models.P2PTradeStatusDisputed,
		Reason: "chain Disputed"}, nil
}

// reconcileToResolved applies a chain Resolved → DB resolved drift fix. The ad
// accounting (reserved -= amount always; remaining += amount only when funds
// did NOT go to the buyer) is funding-agnostic at the DB level — it restores
// only off-chain capacity, never an on-chain pool. `funding` is the on-chain
// truth, recorded for audit.
func (s *P2PTradeService) reconcileToResolved(ctx context.Context, trade *models.P2PTrade, funding uint8, onChainFee *big.Int) (ReconcileResult, error) {
	// Resolved depends on the admin's refundBuyer choice. The off-chain
	// dispute row records the admin's intent.
	//
	// CRITICAL: if the dispute row is missing OR RefundedToBuyer is nil
	// (e.g. dispute was raised on chain bypassing the API, or the resolve
	// happened before ConfirmAdminResolveDispute ran), we cannot tell which
	// branch the chain settled. Defaulting to refundBuyer=false would
	// silently credit ad.remaining even when funds actually went to the
	// buyer — a double-credit. Refuse to touch ad accounting and flag for
	// manual review; the next reconciler pass after the dispute row is
	// populated will resolve cleanly.
	var dispute models.P2PDispute
	err := s.db.WithContext(ctx).Where("trade_id = ?", trade.ID).First(&dispute).Error
	if err != nil || dispute.RefundedToBuyer == nil {
		log.Printf("[p2p reconciler] trade=%s resolved on chain but dispute outcome unknown (dispute=%v, refundedToBuyer=%v); skipping ad accounting",
			trade.ID, err == nil, dispute.RefundedToBuyer)
		s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
			"from": trade.Status, "to": models.P2PTradeStatusResolved,
			"funding": funding,
			"reason":  "chain Resolved with unknown refund target — manual review",
		})
		return ReconcileResult{Changed: false}, nil
	}
	refundBuyer := *dispute.RefundedToBuyer

	now := s.clock.Now()
	from := trade.Status
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.P2PTrade{}).
			Where("id = ? AND status NOT IN ?", trade.ID,
				[]string{
					models.P2PTradeStatusReleased,
					models.P2PTradeStatusCancelled,
					models.P2PTradeStatusResolved,
				}).
			Updates(map[string]any{
				"status":          models.P2PTradeStatusResolved,
				"release_tx_hash": "reconciled",
				"updated_at":      now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		// Close the dispute row if still open.
		if dispute.ID != uuid.Nil {
			tx.Model(&models.P2PDispute{}).
				Where("id = ? AND status = ?", dispute.ID, models.P2PDisputeStatusOpen).
				Updates(map[string]any{
					"status":      models.P2PDisputeStatusResolved,
					"resolved_at": now,
				})
		}
		updates := map[string]any{
			"reserved":   gorm.Expr("CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)", trade.TokenAmount),
			"updated_at": now,
		}
		if !refundBuyer {
			updates["remaining"] = gorm.Expr(
				"CAST(remaining AS NUMERIC) + CAST(? AS NUMERIC)",
				trade.TokenAmount)
		}
		return tx.Model(&models.P2PAd{}).Where("id = ?", trade.AdID).Updates(updates).Error
	})
	if err != nil {
		return ReconcileResult{}, err
	}
	// A resolve that RELEASED to the buyer (refundBuyer here == ReleasedToBuyer
	// on chain) charged a fee — accrue it via the same seam as the live
	// adminResolve path. The refund-to-seller branch charged no fee. Idempotent
	// on p2p_release/trade.ID.
	if refundBuyer {
		s.accrueP2PReleaseFee(trade, onChainFee)
	}
	s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
		"from": from, "to": models.P2PTradeStatusResolved,
		"reason":      "chain Resolved",
		"funding":     funding,
		"refundBuyer": refundBuyer,
	})
	return ReconcileResult{Changed: true, FromStatus: from, ToStatus: models.P2PTradeStatusResolved,
		Reason: "chain Resolved"}, nil
}

// ListNonTerminalTrades returns trades whose DB status is not in a terminal
// state, capped at `limit`. The chain reconciler iterates over these and
// calls ReconcileTrade on each.
func (s *P2PTradeService) ListNonTerminalTrades(ctx context.Context, limit int) ([]models.P2PTrade, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var out []models.P2PTrade
	if err := s.db.WithContext(ctx).
		Where("status NOT IN ?", []string{
			models.P2PTradeStatusReleased,
			models.P2PTradeStatusCancelled,
			models.P2PTradeStatusResolved,
		}).
		Order("updated_at ASC"). // oldest first; gives stuck rows priority
		Limit(limit).
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ClaimExpiredPendingTrades returns expired pending_payment trades that have
// not yet had an expiry notification emitted, and atomically marks them as
// notified so subsequent watchdog ticks ignore them. This dedups
// notifications — each expired trade fires exactly one P2PTradeExpired
// event regardless of how many watchdog passes run before the seller acts.
//
// Capped at `limit` per pass to keep the worker bounded; the watchdog
// re-runs every minute so a backlog drains quickly.
//
// The "claim" pattern (UPDATE … WHERE notified_at IS NULL RETURNING) keeps
// the worker idempotent under concurrent instances — only one worker wins
// each claim. SQLite tests use a SELECT-then-UPDATE fallback below.
func (s *P2PTradeService) ClaimExpiredPendingTrades(ctx context.Context, limit int) ([]models.P2PTrade, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	now := s.clock.Now()
	// SELECT first (SQLite-friendly), then UPDATE with optimistic predicate.
	// Postgres production would do the same in one RETURNING clause, but
	// the per-row UPDATE in the loop is bounded by `limit` so the round-trip
	// cost is acceptable for Phase 1 single-instance.
	var candidates []models.P2PTrade
	if err := s.db.WithContext(ctx).
		Where("status = ? AND expires_at < ? AND expired_notified_at IS NULL",
			models.P2PTradeStatusPendingPayment, now).
		Order("expires_at ASC").
		Limit(limit).
		Find(&candidates).Error; err != nil {
		return nil, err
	}
	claimed := make([]models.P2PTrade, 0, len(candidates))
	for i := range candidates {
		res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
			Where("id = ? AND expired_notified_at IS NULL", candidates[i].ID).
			Update("expired_notified_at", now)
		if res.Error != nil {
			log.Printf("[p2p watchdog claim] trade=%s: %v", candidates[i].ID, res.Error)
			continue
		}
		if res.RowsAffected == 1 {
			candidates[i].ExpiredNotifiedAt = &now
			claimed = append(claimed, candidates[i])
		}
	}
	return claimed, nil
}

// ListExpiredPendingTrades is kept for backward compatibility — same as
// ClaimExpiredPendingTrades minus the side-effect. Used by tests that want
// to inspect the queue without consuming it.
func (s *P2PTradeService) ListExpiredPendingTrades(ctx context.Context, limit int) ([]models.P2PTrade, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var out []models.P2PTrade
	if err := s.db.WithContext(ctx).
		Where("status = ? AND expires_at < ?",
			models.P2PTradeStatusPendingPayment, s.clock.Now()).
		Order("expires_at ASC").
		Limit(limit).
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ─── Reads ──────────────────────────────────────────────────────────────────

type AdFilter struct {
	Side         string
	Token        string
	FiatCurrency string
	Status       string
	SellerID     uuid.UUID
	Limit        int
	Offset       int
	ChainID      int64
	// PM filters by automated PaymentMerchant ads: true = only PM ads (pm_id set),
	// false = only normal ads (pm_id null), nil = both.
	PM *bool
}

func (s *P2PTradeService) GetAd(ctx context.Context, adID uuid.UUID) (*models.P2PAd, error) {
	var ad models.P2PAd
	if err := s.db.WithContext(ctx).First(&ad, "id = ?", adID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAdNotFound
		}
		return nil, err
	}
	return &ad, nil
}

func (s *P2PTradeService) ListAds(ctx context.Context, f AdFilter) ([]models.P2PAd, error) {
	q := s.db.WithContext(ctx).Model(&models.P2PAd{})
	if f.Side != "" {
		q = q.Where("side = ?", f.Side)
	}
	if f.Token != "" {
		q = q.Where("token = ?", strings.ToUpper(f.Token))
	}
	if f.FiatCurrency != "" {
		q = q.Where("fiat_currency = ?", strings.ToUpper(f.FiatCurrency))
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.SellerID != uuid.Nil {
		q = q.Where("seller_id = ?", f.SellerID)
	}
	if f.ChainID != 0 {
		q = q.Where("chain_id = ?", f.ChainID)
	}
	if f.PM != nil {
		if *f.PM {
			q = q.Where("pm_id IS NOT NULL")
		} else {
			q = q.Where("pm_id IS NULL")
		}
	}
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	q = q.Order("created_at DESC").Limit(limit).Offset(f.Offset)
	var out []models.P2PAd
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ExpressMatchParams is a one-shot best-match request (the Express buyer flow).
type ExpressMatchParams struct {
	Side         string // 'buy' = user buys crypto (match sell-ads); 'sell' = user sells (match buy-ads)
	Token        string
	FiatCurrency string
	Amount       string // token base units the user wants to trade
}

// ExpressMatch returns active ads matching the request, ranked best-first: best
// price for the user (lowest rate when buying crypto, highest when selling). Only
// ads with enough remaining capacity whose implied fiat is within the ad's limits
// are returned. MVP ranks on price; the caller decorates seller stats for display
// (reputation as a tiebreak is a planned follow-on). Opening reuses OpenTrade.
func (s *P2PTradeService) ExpressMatch(ctx context.Context, p ExpressMatchParams) ([]models.P2PAd, error) {
	side := strings.ToLower(strings.TrimSpace(p.Side))
	var adSide string
	switch side {
	case "buy":
		adSide = models.P2PAdSideSell // user buys crypto → match sell-ads
	case "sell":
		adSide = models.P2PAdSideBuy // user sells crypto → match buy-ads
	default:
		return nil, fmt.Errorf("%w: side must be 'buy' or 'sell'", ErrInvalidParams)
	}
	amount, ok := new(big.Int).SetString(strings.TrimSpace(p.Amount), 10)
	if !ok || amount.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount must be a positive integer (base units)", ErrInvalidParams)
	}

	cands, err := s.ListAds(ctx, AdFilter{
		Side: adSide, Token: p.Token, FiatCurrency: p.FiatCurrency,
		Status: models.P2PAdStatusActive, Limit: 100,
	})
	if err != nil {
		return nil, err
	}

	eligible := make([]models.P2PAd, 0, len(cands))
	for _, ad := range cands {
		rem, okR := new(big.Int).SetString(ad.Remaining, 10)
		if !okR || rem.Cmp(amount) < 0 {
			continue // not enough capacity
		}
		_, fiat, err := computeFiatFromToken(p.Amount, ad.Rate, tokenDecimalsFor(ad.ChainID, ad.Token))
		if err != nil {
			continue
		}
		if checkFiatLimits(fiat, string(ad.MinLimit), string(ad.MaxLimit)) != nil {
			continue // implied fiat outside the ad's per-trade limits
		}
		eligible = append(eligible, ad)
	}
	rankExpressAds(eligible, side)
	return eligible, nil
}

// rankExpressAds sorts ads best-first for the user (stable): buying crypto → lowest
// rate; selling crypto → highest rate. Unparseable rates sink to the end.
func rankExpressAds(ads []models.P2PAd, side string) {
	sort.SliceStable(ads, func(i, j int) bool {
		ri, ei := scaleDecimal(ads[i].Rate, 10)
		rj, ej := scaleDecimal(ads[j].Rate, 10)
		if ei != nil {
			return false
		}
		if ej != nil {
			return true
		}
		if side == "sell" { // user sells crypto → wants the highest rate
			return ri.Cmp(rj) > 0
		}
		return ri.Cmp(rj) < 0 // user buys crypto → wants the lowest rate
	})
}

// LoadAdPaymentMethods returns the seller's payment-method rows that this
// ad accepts. Joining ad.payment_method_ids ⨝ p2p_payment_methods so the
// buyer can see WHERE to send NGN — they otherwise can't know without
// opening a trade. Scoped to the ad's seller_id + non-deleted rows so a
// stale or malicious PM id in the ad's array can't surface someone else's
// account.
func (s *P2PTradeService) LoadAdPaymentMethods(ctx context.Context, ad *models.P2PAd) ([]models.P2PPaymentMethod, error) {
	if ad == nil || len(ad.PaymentMethodIDs) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(ad.PaymentMethodIDs))
	for _, raw := range ad.PaymentMethodIDs {
		if id, err := uuid.Parse(raw); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []models.P2PPaymentMethod
	if err := s.db.WithContext(ctx).
		Where("id IN ? AND user_id = ? AND deleted_at IS NULL", ids, ad.SellerID).
		Order("is_default DESC, created_at ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// LoadProvidersByMethodIDs returns a map payment-method-id → provider slug for
// the given ids in a single query. Used by the ad-list decorator to build the
// privacy-safe payment summary without an N+1 per-ad load. Deleted rows are
// excluded. Account fields are deliberately NOT selected.
func (s *P2PTradeService) LoadProvidersByMethodIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID       uuid.UUID
		Provider string
	}
	if err := s.db.WithContext(ctx).
		Model(&models.P2PPaymentMethod{}).
		Select("id, provider").
		Where("id IN ? AND deleted_at IS NULL", ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.Provider
	}
	return out, nil
}

// LookupUsernames returns a map seller-id → username for the given IDs in a
// single query. Used by the ad-list/detail handlers to decorate the wire
// response with a display name without an N+1 lookup. Missing IDs are
// silently omitted from the map; callers fall back to a truncated UUID.
func (s *P2PTradeService) LookupUsernames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	// Dedup so the IN list stays small even when many ads share a seller.
	seen := make(map[uuid.UUID]struct{}, len(ids))
	uniq := ids[:0:0]
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return out, nil
	}
	type row struct {
		ID       uuid.UUID
		Username string
	}
	var rows []row
	if err := s.db.WithContext(ctx).Table("users").
		Select("id, username").
		Where("id IN ?", uniq).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.Username
	}
	return out, nil
}

type TradeFilter struct {
	Role   string // "buyer", "seller", or empty for either
	UserID uuid.UUID
	Status string
	Limit  int
	Offset int
}

func (s *P2PTradeService) GetTrade(ctx context.Context, tradeID, requestor uuid.UUID) (*models.P2PTrade, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.BuyerID != requestor && trade.SellerID != requestor {
		return nil, ErrForbidden
	}
	return &trade, nil
}

func (s *P2PTradeService) ListTrades(ctx context.Context, f TradeFilter) ([]models.P2PTrade, error) {
	q := s.db.WithContext(ctx).Model(&models.P2PTrade{})
	switch f.Role {
	case "buyer":
		q = q.Where("buyer_id = ?", f.UserID)
	case "seller":
		q = q.Where("seller_id = ?", f.UserID)
	default:
		q = q.Where("buyer_id = ? OR seller_id = ?", f.UserID, f.UserID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	q = q.Order("created_at DESC").Limit(limit).Offset(f.Offset)
	var out []models.P2PTrade
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ─── Validation helpers ─────────────────────────────────────────────────────

// countActiveTradesForOpener counts non-terminal trades the given opener already
// holds on an ad, so a repeated open can be rejected (ErrActiveTradeExists) before
// it double-debits capacity. openerCol is a fixed internal literal ("buyer_id" for
// sell-ads, "seller_id" for buy-ads) — never user input.
func (s *P2PTradeService) countActiveTradesForOpener(tx *gorm.DB, adID uuid.UUID, openerCol string, openerID uuid.UUID) (int64, error) {
	var n int64
	err := tx.Model(&models.P2PTrade{}).
		Where("ad_id = ? AND "+openerCol+" = ? AND status IN ?", adID, openerID,
			[]string{
				models.P2PTradeStatusPendingPayment,
				models.P2PTradeStatusPaid,
				models.P2PTradeStatusReleasing,
				models.P2PTradeStatusDisputed,
			}).
		Count(&n).Error
	return n, err
}

func (s *P2PTradeService) validateCreateAd(p CreateAdParams) error {
	if strings.TrimSpace(p.Token) == "" {
		return fmt.Errorf("%w: token", ErrInvalidParams)
	}
	if strings.TrimSpace(p.TokenAmount) == "" {
		return fmt.Errorf("%w: token_amount", ErrInvalidParams)
	}
	if strings.TrimSpace(p.FiatCurrency) == "" {
		return fmt.Errorf("%w: fiat_currency", ErrInvalidParams)
	}
	if len(p.PaymentMethodIDs) == 0 {
		return fmt.Errorf("%w: at least one payment_method_id required", ErrInvalidParams)
	}

	// TokenAmount is a numeric(78,0) base-unit integer. Reject non-numeric here so
	// a bad string is a clean 400, not a Postgres INSERT-time 500 (numeric column).
	if n, ok := new(big.Int).SetString(strings.TrimSpace(p.TokenAmount), 10); !ok || n.Sign() <= 0 {
		return fmt.Errorf("%w: token_amount must be a positive integer (base units)", ErrInvalidParams)
	}

	// MinLimit/MaxLimit are optional whole-unit fiat amounts (numeric(78,0)).
	var minL, maxL *big.Int
	if t := strings.TrimSpace(p.MinLimit); t != "" {
		n, ok := new(big.Int).SetString(t, 10)
		if !ok || n.Sign() < 0 {
			return fmt.Errorf("%w: min_limit must be a non-negative whole number", ErrInvalidParams)
		}
		minL = n
	}
	if t := strings.TrimSpace(p.MaxLimit); t != "" {
		n, ok := new(big.Int).SetString(t, 10)
		if !ok || n.Sign() < 0 {
			return fmt.Errorf("%w: max_limit must be a non-negative whole number", ErrInvalidParams)
		}
		maxL = n
	}
	if minL != nil && maxL != nil && minL.Cmp(maxL) > 0 {
		return fmt.Errorf("%w: min_limit must be <= max_limit", ErrInvalidParams)
	}

	// Rate-type validation: fixed ads need a literal rate; float ads need an
	// offset within the configured cap. Anchor key must be in the allowlist.
	rateType := strings.ToLower(strings.TrimSpace(p.RateType))
	if rateType == "" {
		rateType = models.P2PAdRateTypeFixed
	}
	switch rateType {
	case models.P2PAdRateTypeFixed:
		if strings.TrimSpace(p.Rate) == "" {
			return fmt.Errorf("%w: rate", ErrInvalidParams)
		}
		// Rate is written to a numeric column; reject non-numeric / non-positive
		// here so it can't 500 the INSERT on Postgres.
		if v, err := scaleDecimal(strings.TrimSpace(p.Rate), 10); err != nil || v.Sign() <= 0 {
			return fmt.Errorf("%w: rate must be a positive decimal", ErrInvalidParams)
		}
		if p.FloatOffsetBps != nil {
			return fmt.Errorf("%w: float_offset_bps must not be set for fixed-rate ads", ErrInvalidParams)
		}
	case models.P2PAdRateTypeFloat:
		if p.FloatOffsetBps == nil {
			return fmt.Errorf("%w: float_offset_bps required for float-rate ads", ErrInvalidParams)
		}
		cap := s.floatOffsetCap()
		if *p.FloatOffsetBps > cap || *p.FloatOffsetBps < -cap {
			return fmt.Errorf("%w: float_offset_bps must be within ±%d", ErrInvalidParams, cap)
		}
		anchor := strings.TrimSpace(p.FloatAnchor)
		if anchor != "" && anchor != models.P2PAdFloatAnchorBybitSell {
			return fmt.Errorf("%w: unknown float_anchor %q", ErrInvalidParams, anchor)
		}
	default:
		return fmt.Errorf("%w: rate_type must be 'fixed' or 'float'", ErrInvalidParams)
	}
	return nil
}

func (s *P2PTradeService) requireFreshRate(currency string) error {
	if s.rates == nil {
		return nil
	}
	snap, err := s.rates.GetSnapshot()
	if err != nil {
		return fmt.Errorf("rate snapshot: %w", err)
	}
	row, ok := snap.Rates[strings.ToUpper(currency)]
	if !ok {
		return fmt.Errorf("%w: currency %s not in rate snapshot", ErrInvalidParams, currency)
	}
	if row.Stale || snap.IsStale() {
		return ErrRateStale
	}
	return nil
}

// ComputeEffectiveRate returns the NGN-per-token rate to quote against for a
// given ad. For fixed ads it is the literal stored rate. For float ads it
// pulls the latest feed value for ad.FiatCurrency via the configured anchor,
// validates freshness, applies the seller's bps offset, and formats the
// result to 10 decimal places (matching the numeric(38,10) Rate column).
//
// Returns ErrRateStale when the underlying feed is missing or stale —
// mapServiceError in the HTTP layer maps this to HTTP 503 so callers in the
// browse-list or trade-open path can surface a clean "Live rate unavailable"
// state to users.
func (s *P2PTradeService) ComputeEffectiveRate(ad *models.P2PAd) (string, error) {
	if ad == nil {
		return "", fmt.Errorf("%w: nil ad", ErrInvalidParams)
	}
	if ad.RateType != models.P2PAdRateTypeFloat {
		return ad.Rate, nil
	}
	if ad.FloatOffsetBps == nil {
		// Shouldn't happen — the CHECK constraint and validator both reject
		// this — but defend against bad data on read rather than crash.
		return "", fmt.Errorf("%w: float ad missing float_offset_bps", ErrInvalidParams)
	}
	if s.rates == nil {
		return "", ErrRateStale
	}
	snap, err := s.rates.GetSnapshot()
	if err != nil {
		return "", ErrRateStale
	}
	row, ok := snap.Rates[strings.ToUpper(ad.FiatCurrency)]
	if !ok || row.Stale || snap.IsStale() {
		return "", ErrRateStale
	}

	anchor := ad.FloatAnchor
	if anchor == "" {
		anchor = models.P2PAdFloatAnchorBybitSell
	}
	var anchorRate float64
	switch anchor {
	case models.P2PAdFloatAnchorBybitSell:
		anchorRate = row.BinanceSell
	default:
		return "", fmt.Errorf("%w: unknown float_anchor %q", ErrInvalidParams, anchor)
	}
	if anchorRate <= 0 {
		return "", ErrRateStale
	}

	// effective = anchor * (10000 + bps) / 10000
	// All in big.Rat to avoid float drift on the seller's quote.
	base := new(big.Rat).SetFloat64(anchorRate)
	if base == nil {
		return "", ErrRateStale
	}
	numerator := big.NewInt(int64(10000 + *ad.FloatOffsetBps))
	denominator := big.NewInt(10000)
	offset := new(big.Rat).SetFrac(numerator, denominator)
	effective := new(big.Rat).Mul(base, offset)
	return effective.FloatString(10), nil
}

func (s *P2PTradeService) validatePaymentMethodsOwned(userID uuid.UUID, ids []uuid.UUID, currency string) error {
	if len(ids) == 0 {
		return nil
	}
	var rows []models.P2PPaymentMethod
	if err := s.db.Where("id IN ? AND user_id = ? AND deleted_at IS NULL", ids, userID).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(ids) {
		return ErrPaymentMethodMissing
	}
	wantCur := strings.ToUpper(currency)
	for _, r := range rows {
		if strings.ToUpper(r.Currency) != wantCur {
			return fmt.Errorf("%w: payment method %s is %s, not %s", ErrInvalidParams, r.ID, r.Currency, wantCur)
		}
	}
	return nil
}

func (s *P2PTradeService) validateAdPaymentMethod(ad *models.P2PAd, pmID uuid.UUID) error {
	for _, idStr := range []string(ad.PaymentMethodIDs) {
		if idStr == pmID.String() {
			var pm models.P2PPaymentMethod
			if err := s.db.First(&pm, "id = ? AND user_id = ? AND deleted_at IS NULL", pmID, ad.SellerID).Error; err != nil {
				return ErrPaymentMethodMissing
			}
			return nil
		}
	}
	return fmt.Errorf("%w: payment method not in ad allowlist", ErrInvalidParams)
}

func (s *P2PTradeService) resolveTokenAddress(chainID int64, symbol string) (string, error) {
	sym := strings.ToUpper(symbol)
	if strings.HasPrefix(sym, "0X") && len(sym) == 42 {
		return symbol, nil
	}
	if s.tokenResolver != nil {
		if addr := s.tokenResolver(chainID, sym); addr != "" {
			return addr, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrUnsupportedToken, symbol)
}

// ─── Deposit / withdraw ─────────────────────────────────────────────────────

// PrepareApprove builds the unsigned ERC-20 approve tx so the seller can
// grant the escrow contract permission to pull `amount` of `token` on
// their behalf. Step 1 of deposit (when current allowance < amount);
// step 2 is PrepareDeposit.
func (s *P2PTradeService) PrepareApprove(ctx context.Context, sellerID uuid.UUID, sellerAddress, tokenSymbol, amount string, chainID int64) ([]byte, error) {
	if chainID == 0 {
		chainID = s.defaultChainID
	}
	if !common.IsHexAddress(sellerAddress) {
		return nil, fmt.Errorf("%w: seller address", ErrInvalidParams)
	}
	amt, ok := new(big.Int).SetString(amount, 10)
	if !ok || amt.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount", ErrInvalidParams)
	}
	tokenAddr, err := s.resolveTokenAddress(chainID, strings.ToUpper(tokenSymbol))
	if err != nil {
		return nil, err
	}
	escrow, _, _, err := s.resolve(chainID)
	if err != nil {
		return nil, err
	}
	return escrow.PrepareApprove(sellerAddress, tokenAddr, amt)
}

// GetAllowance returns the seller's current ERC-20 allowance to the escrow
// for `token`. Frontend uses it to skip the approve step when the existing
// allowance already covers the intended deposit.
func (s *P2PTradeService) GetAllowance(ctx context.Context, sellerAddress, tokenSymbol string, chainID int64) (*big.Int, error) {
	if chainID == 0 {
		chainID = s.defaultChainID
	}
	if !common.IsHexAddress(sellerAddress) {
		return nil, fmt.Errorf("%w: seller address", ErrInvalidParams)
	}
	tokenAddr, err := s.resolveTokenAddress(chainID, strings.ToUpper(tokenSymbol))
	if err != nil {
		return nil, err
	}
	escrow, _, _, err := s.resolve(chainID)
	if err != nil {
		return nil, err
	}
	return escrow.ReadAllowance(sellerAddress, tokenAddr)
}

// PrepareDeposit builds the unsigned tx for `deposit(token, amount)`. The
// seller is expected to ERC20-approve the escrow contract for `amount`
// before broadcasting; without the approval the on-chain transferFrom
// reverts and gas estimation fails.
//
// Server-side accounting is post-broadcast: balances are read on-chain via
// ReadBalance rather than tracked separately, so a confirm step exists only
// to verify the receipt + emit an audit log.
func (s *P2PTradeService) PrepareDeposit(ctx context.Context, sellerID uuid.UUID, sellerAddress, tokenSymbol, amount string, chainID int64) ([]byte, error) {
	if chainID == 0 {
		chainID = s.defaultChainID
	}
	if !common.IsHexAddress(sellerAddress) {
		return nil, fmt.Errorf("%w: seller address", ErrInvalidParams)
	}
	amt, ok := new(big.Int).SetString(amount, 10)
	if !ok || amt.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount", ErrInvalidParams)
	}
	tokenAddr, err := s.resolveTokenAddress(chainID, strings.ToUpper(tokenSymbol))
	if err != nil {
		return nil, err
	}
	escrow, _, _, err := s.resolve(chainID)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, models.P2PAuditDepositPrepared, &sellerID, nil, nil, map[string]any{
		"token":  strings.ToUpper(tokenSymbol),
		"amount": amount,
	})
	return escrow.PrepareDeposit(sellerAddress, tokenAddr, amt)
}

// ConfirmDeposit verifies the Deposited event for the supplied tx hash AND
// asserts the on-chain amount matches the caller's expected amount. Without
// the amount check, an attacker who learns another seller's pending deposit
// txHash could submit it under their own (smaller) amount and the audit row
// + emitted event would silently record a number the caller never intended.
func (s *P2PTradeService) ConfirmDeposit(ctx context.Context, sellerID uuid.UUID, sellerAddress, tokenSymbol, expectedAmount, txHash string, chainID int64) (*P2PDepositedEvent, error) {
	if chainID == 0 {
		chainID = s.defaultChainID
	}
	if !common.IsHexAddress(sellerAddress) {
		return nil, fmt.Errorf("%w: seller address", ErrInvalidParams)
	}
	expected, ok := new(big.Int).SetString(expectedAmount, 10)
	if !ok || expected.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount", ErrInvalidParams)
	}
	tokenAddr, err := s.resolveTokenAddress(chainID, strings.ToUpper(tokenSymbol))
	if err != nil {
		return nil, err
	}
	escrow, _, _, err := s.resolve(chainID)
	if err != nil {
		return nil, err
	}
	ev, err := escrow.VerifyDeposited(txHash,
		common.HexToAddress(sellerAddress),
		common.HexToAddress(tokenAddr))
	if err != nil {
		return nil, fmt.Errorf("verify deposit: %w", err)
	}
	if ev.Amount.Cmp(expected) != 0 {
		return nil, fmt.Errorf("%w: on-chain deposit amount %s does not match expected %s",
			ErrInvalidParams, ev.Amount, expected)
	}
	s.audit(ctx, models.P2PAuditDepositConfirmed, &sellerID, nil, nil, map[string]any{
		"token":  strings.ToUpper(tokenSymbol),
		"amount": ev.Amount.String(),
		"txHash": txHash,
	})
	s.emit(events.P2PDepositConfirmed, sellerID, "Deposit confirmed", "",
		map[string]any{"token": strings.ToUpper(tokenSymbol), "amount": ev.Amount.String()})
	return ev, nil
}

// PrepareWithdraw builds the unsigned tx for `withdraw(token, amount)`.
// Reverts on chain if amount exceeds the seller's available balance.
func (s *P2PTradeService) PrepareWithdraw(ctx context.Context, sellerID uuid.UUID, sellerAddress, tokenSymbol, amount string, chainID int64) ([]byte, error) {
	if chainID == 0 {
		chainID = s.defaultChainID
	}
	if !common.IsHexAddress(sellerAddress) {
		return nil, fmt.Errorf("%w: seller address", ErrInvalidParams)
	}
	amt, ok := new(big.Int).SetString(amount, 10)
	if !ok || amt.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount", ErrInvalidParams)
	}
	tokenAddr, err := s.resolveTokenAddress(chainID, strings.ToUpper(tokenSymbol))
	if err != nil {
		return nil, err
	}
	escrow, _, _, err := s.resolve(chainID)
	if err != nil {
		return nil, err
	}
	// Soft pre-check — fast-fail with a useful message if the seller is
	// trying to withdraw more than they have. The contract will reject
	// regardless; this just turns a tx revert into a friendly 400.
	if bal, err := escrow.ReadBalance(sellerAddress, tokenAddr); err == nil {
		if bal.Available.Cmp(amt) < 0 {
			return nil, fmt.Errorf("%w: insufficient available balance (have %s)", ErrInvalidParams, bal.Available)
		}
	}
	s.audit(ctx, models.P2PAuditWithdrawPrepared, &sellerID, nil, nil, map[string]any{
		"token":  strings.ToUpper(tokenSymbol),
		"amount": amount,
	})
	return escrow.PrepareWithdraw(sellerAddress, tokenAddr, amt)
}

// ConfirmWithdraw verifies the Withdrawn event for the supplied tx hash AND
// asserts the on-chain amount matches the caller's expected amount (same
// rationale as ConfirmDeposit).
func (s *P2PTradeService) ConfirmWithdraw(ctx context.Context, sellerID uuid.UUID, sellerAddress, tokenSymbol, expectedAmount, txHash string, chainID int64) (*P2PWithdrawnEvent, error) {
	if chainID == 0 {
		chainID = s.defaultChainID
	}
	if !common.IsHexAddress(sellerAddress) {
		return nil, fmt.Errorf("%w: seller address", ErrInvalidParams)
	}
	expected, ok := new(big.Int).SetString(expectedAmount, 10)
	if !ok || expected.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount", ErrInvalidParams)
	}
	tokenAddr, err := s.resolveTokenAddress(chainID, strings.ToUpper(tokenSymbol))
	if err != nil {
		return nil, err
	}
	escrow, _, _, err := s.resolve(chainID)
	if err != nil {
		return nil, err
	}
	ev, err := escrow.VerifyWithdrawn(txHash,
		common.HexToAddress(sellerAddress),
		common.HexToAddress(tokenAddr))
	if err != nil {
		return nil, fmt.Errorf("verify withdraw: %w", err)
	}
	if ev.Amount.Cmp(expected) != 0 {
		return nil, fmt.Errorf("%w: on-chain withdrawal amount %s does not match expected %s",
			ErrInvalidParams, ev.Amount, expected)
	}
	s.audit(ctx, models.P2PAuditWithdrawConfirmed, &sellerID, nil, nil, map[string]any{
		"token":  strings.ToUpper(tokenSymbol),
		"amount": ev.Amount.String(),
		"txHash": txHash,
	})
	s.emit(events.P2PWithdrawConfirmed, sellerID, "Withdrawal confirmed", "",
		map[string]any{"token": strings.ToUpper(tokenSymbol), "amount": ev.Amount.String()})
	return ev, nil
}

// GetPoolBalance returns the (available, locked) balance for a (seller, token).
// Read straight from chain — server doesn't maintain a shadow copy of the
// per-(seller, token) ledger since the contract's accountedBalance is the
// source of truth.
func (s *P2PTradeService) GetPoolBalance(ctx context.Context, sellerAddress, tokenSymbol string, chainID int64) (*OnChainBalance, error) {
	if chainID == 0 {
		chainID = s.defaultChainID
	}
	if !common.IsHexAddress(sellerAddress) {
		return nil, fmt.Errorf("%w: seller address", ErrInvalidParams)
	}
	tokenAddr, err := s.resolveTokenAddress(chainID, strings.ToUpper(tokenSymbol))
	if err != nil {
		return nil, err
	}
	escrow, _, _, err := s.resolve(chainID)
	if err != nil {
		return nil, err
	}
	return escrow.ReadBalance(sellerAddress, tokenAddr)
}

// ─── Voucher helpers ────────────────────────────────────────────────────────

// adVoucher reconstructs the EIP-712 voucher from an ad row. The ad must
// have non-empty SellerAddress + VoucherNonce + VoucherDeadline (set during
// CreateAd). `sellerAddrOverride`, when non-empty, replaces the stored
// SellerAddress — used by SignAd to bind the voucher to the caller's wallet
// in case the ad's SellerAddress wasn't pre-populated (legacy rows).
func (s *P2PTradeService) adVoucher(ad *models.P2PAd, sellerAddrOverride string) (AdVoucher, error) {
	if ad.VoucherNonce == "" || ad.VoucherDeadline == nil {
		return AdVoucher{}, fmt.Errorf("%w: ad has no voucher data", ErrInvalidTransition)
	}
	sellerAddr := ad.SellerAddress
	if sellerAddrOverride != "" {
		sellerAddr = sellerAddrOverride
	}
	if !common.IsHexAddress(sellerAddr) {
		return AdVoucher{}, fmt.Errorf("%w: ad seller address", ErrInvalidParams)
	}
	tokenAddr, err := s.resolveTokenAddress(ad.ChainID, ad.Token)
	if err != nil {
		return AdVoucher{}, err
	}
	amount, _ := new(big.Int).SetString(ad.Amount, 10)
	if amount == nil {
		return AdVoucher{}, fmt.Errorf("%w: ad amount", ErrInvalidParams)
	}
	nonce, err := decodeHex32(ad.VoucherNonce)
	if err != nil {
		return AdVoucher{}, fmt.Errorf("%w: stored voucher nonce is corrupt (%v)", ErrInvalidTransition, err)
	}
	return AdVoucher{
		Seller:      common.HexToAddress(sellerAddr),
		Token:       common.HexToAddress(tokenAddr),
		MaxAmount:   amount,
		MinPerTrade: big.NewInt(0),
		MaxPerTrade: big.NewInt(0),
		Deadline:    big.NewInt(ad.VoucherDeadline.Unix()),
		Nonce:       nonce,
	}, nil
}

// buildVoucherTypedData formats the voucher as an EIP-712 typed-data payload
// the seller's wallet (wagmi/ethers/RainbowKit) signs via signTypedData.
// Field order MUST match AD_VOUCHER_TYPEHASH in the contract — otherwise the
// digest will differ and the contract will reject the signature. The domain
// (verifyingContract, chainId, version) is resolved per the ad's chain by the
// caller and passed in — there is no single-chain fallback.
func (s *P2PTradeService) buildVoucherTypedData(v AdVoucher, contractAddress string, domainChainID int64, version string) *VoucherTypedData {
	return &VoucherTypedData{
		Types: map[string][]VoucherTypedField{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"AdVoucher": {
				{Name: "seller", Type: "address"},
				{Name: "token", Type: "address"},
				{Name: "maxAmount", Type: "uint256"},
				{Name: "minPerTrade", Type: "uint256"},
				{Name: "maxPerTrade", Type: "uint256"},
				{Name: "deadline", Type: "uint256"},
				{Name: "nonce", Type: "bytes32"},
			},
		},
		PrimaryType: "AdVoucher",
		Domain: VoucherTypedDomain{
			Name:              "TSAConnectP2PEscrow",
			Version:           version,
			ChainID:           domainChainID,
			VerifyingContract: contractAddress,
		},
		Message: VoucherTypedMessage{
			Seller:      v.Seller.Hex(),
			Token:       v.Token.Hex(),
			MaxAmount:   v.MaxAmount.String(),
			MinPerTrade: v.MinPerTrade.String(),
			MaxPerTrade: v.MaxPerTrade.String(),
			Deadline:    v.Deadline.String(),
			Nonce:       "0x" + common.Bytes2Hex(v.Nonce[:]),
		},
	}
}

// capitalize uppercases the first byte of s. Replacement for strings.Title
// (deprecated in Go 1.18+) at sites where input is known to be lowercase
// ASCII — buyer/seller role labels in the dispute system message.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	if c := s[0]; c >= 'a' && c <= 'z' {
		return string(c-32) + s[1:]
	}
	return s
}

// decodeHex32 strict-decodes a 0x-prefixed 32-byte hex string. `common.FromHex`
// returns nil/empty on malformed input rather than erroring, so a corrupted
// or truncated DB column would silently become a zero hash and the contract
// would then look up the wrong slot. Anywhere we read a voucher hash / nonce
// out of a row must go through this guard.
func decodeHex32(s string) ([32]byte, error) {
	var out [32]byte
	b := common.FromHex(s)
	if len(b) != 32 {
		return out, fmt.Errorf("expected 32-byte hex, got %d bytes (input %q)", len(b), s)
	}
	copy(out[:], b)
	return out, nil
}

// decodeHexSig strict-decodes a 0x-prefixed 65-byte ECDSA signature. Same
// rationale as decodeHex32: protects against silently propagating empty
// signature bytes that would revert on chain with an opaque message.
func decodeHexSig(s string) ([]byte, error) {
	b := common.FromHex(s)
	if len(b) != 65 {
		return nil, fmt.Errorf("expected 65-byte signature, got %d bytes", len(b))
	}
	return b, nil
}

// recoverEIP712Signer recovers the signer address from a 65-byte signature
// over an EIP-712 digest. Accepts both 0/1 and 27/28 conventions for v.
// Normalises high-S signatures by rejecting them — go-ethereum's
// SigToPub does not reject malleable sigs, so we mirror the on-chain
// ECDSA.recover behavior here.
func recoverEIP712Signer(digest [32]byte, sig []byte) (common.Address, error) {
	if len(sig) != 65 {
		return common.Address{}, fmt.Errorf("signature must be 65 bytes, got %d", len(sig))
	}
	// secp256k1n / 2 — sigs with s > this are rejected on chain.
	halfN := new(big.Int).Rsh(crypto.S256().Params().N, 1)
	s := new(big.Int).SetBytes(sig[32:64])
	if s.Cmp(halfN) > 0 {
		return common.Address{}, fmt.Errorf("non-canonical signature s value")
	}
	// Normalise v from 27/28 → 0/1 for crypto.SigToPub.
	v := sig[64]
	if v >= 27 {
		v -= 27
	}
	if v != 0 && v != 1 {
		return common.Address{}, fmt.Errorf("invalid v: %d", sig[64])
	}
	normalised := make([]byte, 65)
	copy(normalised, sig[:64])
	normalised[64] = v
	pub, err := crypto.SigToPub(digest[:], normalised)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// ─── Audit + events helpers ─────────────────────────────────────────────────

func (s *P2PTradeService) audit(ctx context.Context, action string, actorID *uuid.UUID, adID, tradeID *uuid.UUID, metadata map[string]any) {
	var meta datatypes.JSON
	if metadata != nil {
		if b, err := json.Marshal(metadata); err == nil {
			meta = b
		}
	}
	row := models.P2PAuditLog{
		ID:       uuid.New(),
		ActorID:  actorID,
		AdID:     adID,
		TradeID:  tradeID,
		Action:   action,
		Metadata: meta,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		// Audit-log failures must NOT crash the live transition (the user's
		// money/state has already moved). Surface to the standard logger so
		// the loss shows up in observability.
		log.Printf("[p2p audit] failed to persist action=%s actorId=%v adId=%v tradeId=%v: %v",
			action, actorID, adID, tradeID, err)
	}
}

func (s *P2PTradeService) emit(eventType string, userID uuid.UUID, title, message string, data map[string]any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{
		Type:    eventType,
		UserID:  userID,
		Title:   title,
		Message: message,
		Data:    data,
	})
}

// ─── Pure helpers (no service state) ────────────────────────────────────────

func stringifyUUIDs(ids []uuid.UUID) datatypes.JSONSlice[string] {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return datatypes.JSONSlice[string](out)
}

// tokenDecimalsFor returns the on-chain ERC-20 decimals for a token on a given
// chain. Stablecoins differ by chain family: Sonic USDT/USDC are 6 decimals,
// the Binance-Peg BSC USDT/USDC are 18. MCGP (Sonic-only) is 18. Unknown chains
// fall back to the Sonic-style assumption (6 for stables) — the only configured
// P2P chains are Sonic (146/14601) and BSC (56/97).
func tokenDecimalsFor(chainID int64, symbol string) int {
	sym := strings.ToUpper(symbol)
	switch chainID {
	case 56, 97: // BSC mainnet / testnet
		switch sym {
		case "USDT", "USDC":
			return 18
		}
	}
	switch sym {
	case "USDT", "USDC":
		return 6
	case "MCGP":
		return 18
	default:
		return 18
	}
}

// computeTokenFromFiat converts a fiat amount into the matching token amount
// in base units, given the ad's rate.
//
// Semantics (matches what users type in the UI):
//
//	rate     = fiat per WHOLE token (e.g. "1500" = ₦1500 per 1 USDT)
//	fiat     = integer fiat amount (Phase 1: NGN has no fractional unit)
//	decimals = token's on-chain decimals (6 for USDT/USDC, 18 for MCGP)
//
// Formula:
//
//	whole_tokens = fiat / rate
//	base_units   = whole_tokens * 10^decimals
//
// Implemented in integer math via:
//
//	base_units = fiat * 10^decimals * 10^rateScale / scaledRate
//
// where scaledRate = rate * 10^rateScale to absorb up to `rateScale`
// fractional digits on the rate. Truncates remainder (round-down).
func computeTokenFromFiat(fiatStr, rateStr string, decimals int) (token, fiat *big.Int, err error) {
	const rateScale = 10
	fiat, ok := new(big.Int).SetString(fiatStr, 10)
	if !ok {
		return nil, nil, fmt.Errorf("%w: fiat_amount", ErrInvalidParams)
	}
	if fiat.Sign() <= 0 {
		return nil, nil, fmt.Errorf("%w: fiat_amount must be > 0", ErrInvalidParams)
	}
	scaledRate, err := scaleDecimal(rateStr, rateScale)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: rate", ErrInvalidParams)
	}
	if scaledRate.Sign() <= 0 {
		return nil, nil, fmt.Errorf("%w: rate must be > 0", ErrInvalidParams)
	}
	scale := big.NewInt(int64(decimals + rateScale))
	numerator := new(big.Int).Mul(fiat, new(big.Int).Exp(big.NewInt(10), scale, nil))
	token = new(big.Int).Quo(numerator, scaledRate)
	return token, fiat, nil
}

// computeFiatFromToken is the inverse of computeTokenFromFiat: given an exact
// crypto amount (base units), it returns that same token amount plus the fiat
// it's worth at `rate` (floored to whole fiat units). Used by the buy-ad / JIT
// sell path so the on-chain escrowed amount is exactly what the user chose to
// sell (and exactly what they approved), with fiat derived for display/record.
func computeFiatFromToken(tokenStr, rateStr string, decimals int) (token, fiat *big.Int, err error) {
	const rateScale = 10
	token, ok := new(big.Int).SetString(strings.TrimSpace(tokenStr), 10)
	if !ok {
		return nil, nil, fmt.Errorf("%w: token_amount", ErrInvalidParams)
	}
	if token.Sign() <= 0 {
		return nil, nil, fmt.Errorf("%w: token_amount must be > 0", ErrInvalidParams)
	}
	scaledRate, err := scaleDecimal(rateStr, rateScale)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: rate", ErrInvalidParams)
	}
	if scaledRate.Sign() <= 0 {
		return nil, nil, fmt.Errorf("%w: rate must be > 0", ErrInvalidParams)
	}
	// fiat = token * scaledRate / 10^(decimals+rateScale)
	scale := big.NewInt(int64(decimals + rateScale))
	denom := new(big.Int).Exp(big.NewInt(10), scale, nil)
	num := new(big.Int).Mul(token, scaledRate)
	fiat = new(big.Int).Quo(num, denom)
	if fiat.Sign() <= 0 {
		return nil, nil, fmt.Errorf("%w: token_amount too small for rate (fiat=0)", ErrInvalidParams)
	}
	return token, fiat, nil
}

func scaleDecimal(s string, scale int) (*big.Int, error) {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac := s[:i], s[i+1:]
		if len(frac) > scale {
			frac = frac[:scale]
		} else {
			frac += strings.Repeat("0", scale-len(frac))
		}
		s = intPart + frac
	} else {
		s += strings.Repeat("0", scale)
	}
	out, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("not a decimal: %q", s)
	}
	return out, nil
}

func checkFiatLimits(fiat *big.Int, minStr, maxStr string) error {
	if minStr != "" {
		if mn, ok := new(big.Int).SetString(minStr, 10); ok && mn.Sign() > 0 && fiat.Cmp(mn) < 0 {
			return fmt.Errorf("%w: below ad min %s", ErrAmountOutOfRange, minStr)
		}
	}
	if maxStr != "" {
		if mx, ok := new(big.Int).SetString(maxStr, 10); ok && mx.Sign() > 0 && fiat.Cmp(mx) > 0 {
			return fmt.Errorf("%w: above ad max %s", ErrAmountOutOfRange, maxStr)
		}
	}
	return nil
}
