package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

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
)

// uint96Max is the largest amount the contract can hold for a single ad/trade
// (`uint96` field in P2PEscrow.sol). Computed once.
var uint96Max = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 96), big.NewInt(1))

// ─── Dependencies (interfaces so tests can mock) ────────────────────────────

// P2PEscrowAdapter is the contract-binding surface the trade service depends
// on. P2PEscrowService satisfies it; tests inject a fake.
type P2PEscrowAdapter interface {
	ContractAddress() string
	PrepareCreateAd(adId [32]byte, seller, token string, amount *big.Int) ([]byte, error)
	PrepareOpenTrade(tradeId, adId [32]byte, buyer string, amount *big.Int) ([]byte, error)
	PrepareMarkPaid(tradeId [32]byte, buyer string) ([]byte, error)
	PrepareRelease(tradeId [32]byte, seller string) ([]byte, error)
	PrepareCancelByTimeout(tradeId [32]byte, caller string) ([]byte, error)
	PrepareCloseAd(adId [32]byte, seller string) ([]byte, error)
	PrepareRaiseDispute(tradeId [32]byte, by string) ([]byte, error)
	PrepareAdminResolve(tradeId [32]byte, refundBuyer bool, admin string) ([]byte, error)
	VerifyAdCreated(txHash string, expectedAdId [32]byte) (*P2PAdCreatedEvent, error)
	VerifyTradeOpened(txHash string, expectedTradeId [32]byte) (*P2PTradeOpenedEvent, error)
	VerifyTradeReleased(txHash string, expectedTradeId [32]byte) (*P2PTradeReleasedEvent, error)
	VerifyTradeCancelled(txHash string, expectedTradeId [32]byte) (*P2PTradeCancelledEvent, error)
	VerifyTradeDisputed(txHash string, expectedTradeId [32]byte) (*P2PTradeDisputedEvent, error)
	VerifyTradeResolved(txHash string, expectedTradeId [32]byte) (*P2PTradeResolvedEvent, error)
	// ReadTrade is the chain-side getter the reconciler uses to detect DB
	// drift. Status mirrors P2PEscrow.sol's Status enum (see
	// OnChainP2PTradeStatus constants).
	ReadTrade(tradeId [32]byte) (*OnChainP2PTrade, error)
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
	db            *gorm.DB
	escrow        P2PEscrowAdapter
	rates         RateProvider
	bus           EventPublisher
	clock         Clock
	chainID       int64
	tokenResolver func(symbol string) string
	// messages is optional. When set, every state transition also writes a
	// "system" chat message so the trade thread is self-documenting. Nil-safe
	// — internal calls go through `s.systemMsg` which guards.
	messages *P2PMessageService
	// floatOffsetMaxBps caps the absolute basis-point offset a seller can
	// publish on a float-rate ad. 0 (or unset) means use defaultFloatOffsetMaxBps.
	floatOffsetMaxBps int
}

// defaultFloatOffsetMaxBps is the platform-wide cap applied when the service
// is constructed without an explicit value. 1000 bps = ±10%, matching the
// typical Binance P2P deviation cap. Production should always set this from
// config (P2P_FLOAT_OFFSET_MAX_BPS); the default is a safety net for tests
// and dev environments.
const defaultFloatOffsetMaxBps = 1000

func NewP2PTradeService(db *gorm.DB, escrow P2PEscrowAdapter, rates RateProvider, bus EventPublisher, chainID int64) *P2PTradeService {
	return &P2PTradeService{
		db:      db,
		escrow:  escrow,
		rates:   rates,
		bus:     bus,
		clock:   SystemClock,
		chainID: chainID,
	}
}

// WithClock returns a copy of the service using the supplied clock — tests
// only. Avoids exposing a mutable field on the type.
func (s *P2PTradeService) WithClock(c Clock) *P2PTradeService {
	cp := *s
	cp.clock = c
	return &cp
}

// SetTokenResolver wires a symbol → address function. Called at service
// construction time so the service stays free of config-shape coupling.
func (s *P2PTradeService) SetTokenResolver(f func(symbol string) string) {
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
}

type OpenTradeParams struct {
	FiatAmount      string
	PaymentMethodID uuid.UUID
}

type CreateAdResult struct {
	Ad         *models.P2PAd
	UnsignedTx []byte
}

type ReleaseResult struct {
	UnsignedTx []byte
}

// ─── Ad lifecycle ───────────────────────────────────────────────────────────

func (s *P2PTradeService) CreateAd(ctx context.Context, sellerID uuid.UUID, sellerAddress string, params CreateAdParams) (*CreateAdResult, error) {
	if err := s.validateCreateAd(params); err != nil {
		return nil, err
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

	ad := &models.P2PAd{
		ID:               uuid.New(),
		SellerID:         sellerID,
		Side:             models.P2PAdSideSell,
		Token:            strings.ToUpper(params.Token),
		ChainID:          s.chainID,
		Amount:           params.TokenAmount,
		Remaining:        params.TokenAmount,
		Reserved:         "0",
		FiatCurrency:     strings.ToUpper(params.FiatCurrency),
		Rate:             params.Rate,
		RateType:         rateType,
		FloatOffsetBps:   params.FloatOffsetBps,
		FloatAnchor:      anchor,
		MinLimit:         params.MinLimit,
		MaxLimit:         params.MaxLimit,
		PaymentMethodIDs: stringifyUUIDs(params.PaymentMethodIDs),
		Terms:            params.Terms,
		Status:           models.P2PAdStatusPending,
	}

	// Float ads compute their initial cache value from the live feed so the
	// browse list shows a real number from the moment the ad is published.
	// requireFreshRate above already guaranteed the feed is fresh; failing
	// here means a race we can't recover from cleanly, so bubble ErrRateStale.
	if rateType == models.P2PAdRateTypeFloat {
		effective, err := s.ComputeEffectiveRate(ad)
		if err != nil {
			return nil, err
		}
		ad.Rate = effective
	}
	ad.OnChainAdID = HashIDHex(ad.ID)
	onChainAdID := HashID(ad.ID)

	tokenAddr, err := s.resolveTokenAddress(ad.Token)
	if err != nil {
		return nil, err
	}

	unsignedTx, err := s.escrow.PrepareCreateAd(onChainAdID, sellerAddress, tokenAddr, amount)
	if err != nil {
		return nil, fmt.Errorf("prepare createAd: %w", err)
	}

	if err := s.db.WithContext(ctx).Create(ad).Error; err != nil {
		return nil, fmt.Errorf("persist ad: %w", err)
	}

	s.audit(ctx, models.P2PAuditAdCreated, &sellerID, &ad.ID, nil, map[string]any{
		"token":  ad.Token,
		"amount": ad.Amount,
		"rate":   ad.Rate,
	})
	s.emit(events.P2PAdCreated, sellerID, "P2P ad created", "", map[string]any{"adId": ad.ID, "status": ad.Status})

	return &CreateAdResult{Ad: ad, UnsignedTx: unsignedTx}, nil
}

func (s *P2PTradeService) ConfirmAd(ctx context.Context, adID, sellerID uuid.UUID, sellerAddress, txHash string) (*models.P2PAd, error) {
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
	if ad.Status == models.P2PAdStatusActive && ad.EscrowTxHash == txHash {
		return &ad, nil
	}
	if ad.Status != models.P2PAdStatusPending {
		return nil, fmt.Errorf("%w: ad status=%s", ErrInvalidTransition, ad.Status)
	}

	ev, err := s.escrow.VerifyAdCreated(txHash, HashID(ad.ID))
	if err != nil {
		return nil, fmt.Errorf("verify on-chain ad: %w", err)
	}
	expectedAmount, _ := new(big.Int).SetString(ad.Amount, 10)
	if ev.Amount.Cmp(expectedAmount) != 0 {
		return nil, fmt.Errorf("on-chain amount %s does not match db amount %s", ev.Amount, ad.Amount)
	}
	// Defence-in-depth: confirm the on-chain seller matches the wallet the
	// authenticated caller is using. The adId is keccak-derived from a
	// server-owned UUID so collision is implausible, but the cross-check
	// turns a silent-attribution bug into an explicit reject.
	if sellerAddress != "" && !strings.EqualFold(ev.Seller.Hex(), sellerAddress) {
		return nil, fmt.Errorf("on-chain seller %s does not match expected %s", ev.Seller.Hex(), sellerAddress)
	}

	res := s.db.WithContext(ctx).Model(&models.P2PAd{}).
		Where("id = ? AND status = ?", ad.ID, models.P2PAdStatusPending).
		Updates(map[string]any{
			"status":         models.P2PAdStatusActive,
			"escrow_tx_hash": txHash,
			"updated_at":     s.clock.Now(),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrInvalidTransition
	}
	ad.Status = models.P2PAdStatusActive
	ad.EscrowTxHash = txHash

	s.audit(ctx, models.P2PAuditAdCreated+".confirmed", &sellerID, &ad.ID, nil, map[string]any{"txHash": txHash})
	s.emit(events.P2PAdCreated, sellerID, "P2P ad live", "", map[string]any{"adId": ad.ID, "status": "active"})
	return &ad, nil
}

func (s *P2PTradeService) CloseAd(ctx context.Context, adID, sellerID uuid.UUID, sellerAddress string) ([]byte, error) {
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
	return s.escrow.PrepareCloseAd(HashID(ad.ID), sellerAddress)
}

func (s *P2PTradeService) ConfirmCloseAd(ctx context.Context, adID, sellerID uuid.UUID, txHash string) (*models.P2PAd, error) {
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
	res := s.db.WithContext(ctx).Model(&models.P2PAd{}).
		Where("id = ? AND status IN ?", ad.ID,
			[]string{models.P2PAdStatusActive, models.P2PAdStatusPaused}).
		Updates(map[string]any{
			"status":     models.P2PAdStatusCancelled,
			"remaining":  "0",
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

	// Effective rate is recomputed live for float ads (snapshotted onto the
	// trade row below), and returned verbatim for fixed ads. ErrRateStale on
	// float ads is surfaced unchanged so the handler maps it to 503.
	effectiveRate, err := s.ComputeEffectiveRate(&ad)
	if err != nil {
		return nil, nil, err
	}

	tokenAmount, fiatAmount, err := computeTokenFromFiat(params.FiatAmount, effectiveRate, tokenDecimalsFor(ad.Token))
	if err != nil {
		return nil, nil, err
	}
	if err := checkFiatLimits(fiatAmount, ad.MinLimit, ad.MaxLimit); err != nil {
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
		Token:           ad.Token,
		ChainID:         ad.ChainID,
		TokenAmount:     tokenAmount.String(),
		FiatAmount:      fiatAmount.String(),
		FiatCurrency:    ad.FiatCurrency,
		Rate:            effectiveRate,
		PaymentMethodID: params.PaymentMethodID,
		Status:          models.P2PTradeStatusPendingPayment,
		ExpiresAt:       s.clock.Now().Add(30 * time.Minute),
	}
	trade.OnChainTradeID = HashIDHex(trade.ID)
	onChainTradeID := HashID(trade.ID)

	unsignedTx, err := s.escrow.PrepareOpenTrade(onChainTradeID, HashID(ad.ID), buyerAddress, tokenAmount)
	if err != nil {
		return nil, nil, fmt.Errorf("prepare openTrade: %w", err)
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.P2PAd{}).
			Where("id = ? AND status = ? AND CAST(remaining AS NUMERIC) >= CAST(? AS NUMERIC)",
				ad.ID, models.P2PAdStatusActive, tokenAmount.String()).
			Updates(map[string]any{
				"remaining":  gorm.Expr("CAST((CAST(remaining AS NUMERIC) - CAST(? AS NUMERIC)) AS TEXT)", tokenAmount.String()),
				"reserved":   gorm.Expr("CAST((CAST(reserved AS NUMERIC) + CAST(? AS NUMERIC)) AS TEXT)", tokenAmount.String()),
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

func (s *P2PTradeService) ConfirmOpenTrade(ctx context.Context, tradeID, buyerID uuid.UUID, buyerAddress, txHash string) (*models.P2PTrade, error) {
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
	if trade.OpenTxHash == txHash && trade.OpenTxHash != "" {
		return &trade, nil
	}
	if trade.Status != models.P2PTradeStatusPendingPayment {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}

	ev, err := s.escrow.VerifyTradeOpened(txHash, HashID(trade.ID))
	if err != nil {
		return nil, fmt.Errorf("verify on-chain open: %w", err)
	}
	expected, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	if ev.Amount.Cmp(expected) != 0 {
		return nil, fmt.Errorf("on-chain amount %s does not match db %s", ev.Amount, trade.TokenAmount)
	}
	// Cross-check the on-chain buyer matches the caller's wallet. tradeId is
	// server-owned (keccak of UUID); this guard catches a future bug where
	// the verify-receipt path misses a topic-shift.
	if buyerAddress != "" && !strings.EqualFold(ev.Buyer.Hex(), buyerAddress) {
		return nil, fmt.Errorf("on-chain buyer %s does not match expected %s", ev.Buyer.Hex(), buyerAddress)
	}
	if err := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ?", trade.ID).
		Updates(map[string]any{"open_tx_hash": txHash, "updated_at": s.clock.Now()}).Error; err != nil {
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

func (s *P2PTradeService) Release(ctx context.Context, tradeID, sellerID uuid.UUID, sellerAddress string) (*ReleaseResult, error) {
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
	// Allow Release from pending_payment OR paid OR releasing (retry).
	// Symmetric with the contract relax: seller's signature is the gate,
	// not the buyer's (unverifiable) off-chain markPaid claim.
	if trade.Status != models.P2PTradeStatusPendingPayment &&
		trade.Status != models.P2PTradeStatusPaid &&
		trade.Status != models.P2PTradeStatusReleasing {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
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
	} else {
		// Retry path: trade was already Releasing. Bump updated_at so the
		// chain reconciler's stale-release rollback window resets — protects
		// the seller mid-signing from a false-positive rollback.
		s.db.WithContext(ctx).Model(&models.P2PTrade{}).
			Where("id = ?", trade.ID).
			Update("updated_at", s.clock.Now())
	}

	unsignedTx, err := s.escrow.PrepareRelease(HashID(trade.ID), sellerAddress)
	if err != nil {
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
	ev, err := s.escrow.VerifyTradeReleased(txHash, HashID(trade.ID))
	if err != nil {
		return nil, fmt.Errorf("verify on-chain release: %w", err)
	}
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
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
				"reserved":   gorm.Expr("CAST((CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)) AS TEXT)", trade.TokenAmount),
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
	trade.Status = models.P2PTradeStatusReleased
	trade.ReleaseTxHash = txHash
	trade.ReleasedAt = &now
	s.audit(ctx, models.P2PAuditTradeReleased, &sellerID, &trade.AdID, &trade.ID, map[string]any{
		"txHash":        txHash,
		"amountToBuyer": ev.AmountToBuyer.String(),
		"feeToSystem":   ev.FeeToSystem.String(),
	})
	s.emit(events.P2PTradeReleased, sellerID, "Trade released", "", map[string]any{"tradeId": trade.ID})
	s.emit(events.P2PTradeReleased, trade.BuyerID, "USDT received in your wallet", "", map[string]any{"tradeId": trade.ID})
	s.systemMsg(ctx, trade.ID, fmt.Sprintf("Trade released. %s base units sent to buyer; %s in platform fee.",
		ev.AmountToBuyer, ev.FeeToSystem))
	return &trade, nil
}

// Cancel prepares an unsigned `cancelByTimeout` tx for a trade whose
// pending-payment window has elapsed. The contract makes this permissionless
// on-chain (anyone may call after 30 minutes), but the service restricts the
// API to participants — seller or buyer — so the surface stays predictable.
// Funds return to ad.remaining on confirm.
func (s *P2PTradeService) Cancel(ctx context.Context, tradeID, userID uuid.UUID, callerAddress string) ([]byte, error) {
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
	return s.escrow.PrepareCancelByTimeout(HashID(trade.ID), callerAddress)
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

	ev, err := s.escrow.VerifyTradeCancelled(txHash, HashID(trade.ID))
	if err != nil {
		return nil, fmt.Errorf("verify on-chain cancel: %w", err)
	}
	// Sanity check: on-chain returnedToAvailable matches our recorded amount.
	// (Field renamed from ReturnedToAd in the pool-model upgrade — under the
	// new contract the funds return to the seller's `available` pool, not to
	// a specific ad's remaining.)
	expected, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	if expected != nil && ev.ReturnedToAvailable.Cmp(expected) != 0 {
		return nil, fmt.Errorf("on-chain returnedToAvailable %s does not match trade amount %s",
			ev.ReturnedToAvailable, trade.TokenAmount)
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
		// Restore the ad pool: reserved -= amount, remaining += amount.
		return tx.Model(&models.P2PAd{}).
			Where("id = ?", trade.AdID).
			Updates(map[string]any{
				"reserved":   gorm.Expr("CAST((CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)) AS TEXT)", trade.TokenAmount),
				"remaining":  gorm.Expr("CAST((CAST(remaining AS NUMERIC) + CAST(? AS NUMERIC)) AS TEXT)", trade.TokenAmount),
				"updated_at": now,
			}).Error
	})
	if err != nil {
		return nil, err
	}
	trade.Status = models.P2PTradeStatusCancelled
	trade.CancelReason = "timed_out"

	s.audit(ctx, models.P2PAuditTradeCancelled, &userID, &trade.AdID, &trade.ID, map[string]any{
		"txHash":       txHash,
		"returnedToAvailable": ev.ReturnedToAvailable.String(),
	})
	// Notify both participants.
	s.emit(events.P2PTradeCancelled, trade.SellerID, "Trade cancelled — funds returned to your ad",
		"", map[string]any{"tradeId": trade.ID})
	if trade.BuyerID != trade.SellerID {
		s.emit(events.P2PTradeCancelled, trade.BuyerID, "Trade cancelled (timed out)",
			"", map[string]any{"tradeId": trade.ID})
	}
	s.systemMsg(ctx, trade.ID, "Trade cancelled after 30-minute payment window expired. Funds returned to seller's ad.")
	return &trade, nil
}

// ─── Dispute lifecycle (Step 8) ─────────────────────────────────────────────

// RaiseDispute prepares an on-chain raiseDispute tx and persists a dispute
// row tied to the trade. The trade itself isn't transitioned until
// ConfirmRaiseDispute verifies the receipt — the dispute row sits in `open`
// state in the interim so the UI can surface "dispute pending broadcast".
// Either buyer or seller (in Paid state only) may dispute.
func (s *P2PTradeService) RaiseDispute(ctx context.Context, tradeID, userID uuid.UUID, callerAddress, reason string) ([]byte, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason required", ErrInvalidParams)
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

	dispute := &models.P2PDispute{
		ID:         uuid.New(),
		TradeID:    trade.ID,
		RaisedBy:   userID,
		RaisedRole: role,
		Reason:     reason,
		Status:     models.P2PDisputeStatusOpen,
	}
	if err := s.db.WithContext(ctx).Create(dispute).Error; err != nil {
		return nil, fmt.Errorf("persist dispute: %w", err)
	}

	unsignedTx, err := s.escrow.PrepareRaiseDispute(HashID(trade.ID), callerAddress)
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
	// Notify both participants — the counter-party is the one who needs to
	// know they're now in a dispute window.
	s.emit(events.P2PTradeDisputed, userID, "Dispute opened", "", map[string]any{
		"tradeId": trade.ID, "role": role,
	})
	other := trade.SellerID
	if userID == trade.SellerID {
		other = trade.BuyerID
	}
	s.emit(events.P2PTradeDisputed, other, "Counter-party opened a dispute on this trade",
		"", map[string]any{"tradeId": trade.ID})
	s.systemMsg(ctx, trade.ID, fmt.Sprintf("%s opened a dispute. Funds frozen pending admin review. Reason: %s",
		strings.Title(role), reason))

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

	if _, err := s.escrow.VerifyTradeDisputed(txHash, HashID(trade.ID)); err != nil {
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
	return &trade, nil
}

// AdminResolveDispute prepares the on-chain adminResolve tx. Admin can side
// with the buyer (release fee-split to buyer) or seller (refund to ad pool).
// The off-chain note is persisted with the dispute row on confirm.
func (s *P2PTradeService) AdminResolveDispute(ctx context.Context, tradeID, adminID uuid.UUID, adminAddress string, refundBuyer bool, note string) ([]byte, error) {
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

	unsignedTx, err := s.escrow.PrepareAdminResolve(HashID(trade.ID), refundBuyer, adminAddress)
	if err != nil {
		return nil, fmt.Errorf("prepare adminResolve: %w", err)
	}

	// Stash pending resolution on the dispute row so ConfirmAdminResolve
	// can apply it consistently. Note may be empty. NB: chain is the
	// authoritative source — see ConfirmAdminResolveDispute, which reads
	// the on-chain event regardless of what's stored here.
	updates := map[string]any{
		"resolved_by":       adminID,
		"resolution":        note,
		"refunded_to_buyer": refundBuyer,
	}
	if err := s.db.WithContext(ctx).Model(&dispute).
		Where("id = ? AND status = ?", dispute.ID, models.P2PDisputeStatusOpen).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	// Pre-broadcast audit so concurrent admins racing this endpoint leave
	// a traceable record of intent — the confirm step writes the final
	// outcome separately.
	s.audit(ctx, models.P2PAuditTradeResolved+".prepared", &adminID, &trade.AdID, &trade.ID, map[string]any{
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

	ev, err := s.escrow.VerifyTradeResolved(txHash, HashID(trade.ID))
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
		// amount only when funds return to the pool (refundBuyer=false).
		updates := map[string]any{
			"reserved":   gorm.Expr("CAST((CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)) AS TEXT)", trade.TokenAmount),
			"updated_at": now,
		}
		if !ev.ReleasedToBuyer {
			updates["remaining"] = gorm.Expr(
				"CAST((CAST(remaining AS NUMERIC) + CAST(? AS NUMERIC)) AS TEXT)",
				trade.TokenAmount)
		}
		return tx.Model(&models.P2PAd{}).Where("id = ?", trade.AdID).Updates(updates).Error
	})
	if err != nil {
		return nil, err
	}
	trade.Status = models.P2PTradeStatusResolved
	trade.ResolvedByAdmin = &adminID

	s.audit(ctx, models.P2PAuditTradeResolved, &adminID, &trade.AdID, &trade.ID, map[string]any{
		"txHash":          txHash,
		"releasedToBuyer": ev.ReleasedToBuyer,
		"amountMoved":     ev.AmountMoved.String(),
		"feeToSystem":     ev.FeeToSystem.String(),
	})
	s.emit(events.P2PTradeResolved, trade.BuyerID, "Dispute resolved", "",
		map[string]any{"tradeId": trade.ID, "releasedToBuyer": ev.ReleasedToBuyer})
	s.emit(events.P2PTradeResolved, trade.SellerID, "Dispute resolved", "",
		map[string]any{"tradeId": trade.ID, "releasedToBuyer": ev.ReleasedToBuyer})
	if ev.ReleasedToBuyer {
		s.systemMsg(ctx, trade.ID, "Admin resolved dispute in buyer's favour. USDT sent to buyer.")
	} else {
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

	onchain, err := s.escrow.ReadTrade(HashID(trade.ID))
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("read on-chain trade: %w", err)
	}
	// Trade not on chain yet (still in our DB intent). No drift, just waiting.
	if !onchain.Exists {
		return ReconcileResult{}, nil
	}

	switch onchain.Status {
	case OnChainP2PStatusReleased:
		return s.reconcileToReleased(ctx, &trade)
	case OnChainP2PStatusCancelled:
		return s.reconcileToCancelled(ctx, &trade)
	case OnChainP2PStatusResolved:
		return s.reconcileToResolved(ctx, &trade)
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

func isTerminalTradeStatus(s string) bool {
	return s == models.P2PTradeStatusReleased ||
		s == models.P2PTradeStatusCancelled ||
		s == models.P2PTradeStatusResolved
}

func (s *P2PTradeService) reconcileToReleased(ctx context.Context, trade *models.P2PTrade) (ReconcileResult, error) {
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
				"reserved":   gorm.Expr("CAST((CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)) AS TEXT)", trade.TokenAmount),
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
	s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
		"from": from, "to": models.P2PTradeStatusReleased,
		"reason": "chain Released",
	})
	s.emit(events.P2PTradeReleased, trade.SellerID, "Trade released (reconciled)", "", map[string]any{"tradeId": trade.ID})
	s.emit(events.P2PTradeReleased, trade.BuyerID, "USDT received (reconciled)", "", map[string]any{"tradeId": trade.ID})
	return ReconcileResult{Changed: true, FromStatus: from, ToStatus: models.P2PTradeStatusReleased,
		Reason: "chain Released"}, nil
}

func (s *P2PTradeService) reconcileToCancelled(ctx context.Context, trade *models.P2PTrade) (ReconcileResult, error) {
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
				"reserved":   gorm.Expr("CAST((CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)) AS TEXT)", trade.TokenAmount),
				"remaining":  gorm.Expr("CAST((CAST(remaining AS NUMERIC) + CAST(? AS NUMERIC)) AS TEXT)", trade.TokenAmount),
				"updated_at": now,
			}).Error
	})
	if err != nil {
		return ReconcileResult{}, err
	}
	s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
		"from": from, "to": models.P2PTradeStatusCancelled,
		"reason": "chain Cancelled",
	})
	s.emit(events.P2PTradeCancelled, trade.SellerID, "Trade cancelled (reconciled)", "", map[string]any{"tradeId": trade.ID})
	return ReconcileResult{Changed: true, FromStatus: from, ToStatus: models.P2PTradeStatusCancelled,
		Reason: "chain Cancelled"}, nil
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

func (s *P2PTradeService) reconcileToResolved(ctx context.Context, trade *models.P2PTrade) (ReconcileResult, error) {
	// Resolved depends on the admin's refundBuyer choice. The off-chain
	// dispute row records the admin's intent. If absent, fall back to
	// "refund to ad" (conservative — doesn't move funds beyond what chain
	// already moved).
	var dispute models.P2PDispute
	refundBuyer := false
	err := s.db.WithContext(ctx).Where("trade_id = ?", trade.ID).First(&dispute).Error
	if err == nil && dispute.RefundedToBuyer != nil {
		refundBuyer = *dispute.RefundedToBuyer
	}

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
			"reserved":   gorm.Expr("CAST((CAST(reserved AS NUMERIC) - CAST(? AS NUMERIC)) AS TEXT)", trade.TokenAmount),
			"updated_at": now,
		}
		if !refundBuyer {
			updates["remaining"] = gorm.Expr(
				"CAST((CAST(remaining AS NUMERIC) + CAST(? AS NUMERIC)) AS TEXT)",
				trade.TokenAmount)
		}
		return tx.Model(&models.P2PAd{}).Where("id = ?", trade.AdID).Updates(updates).Error
	})
	if err != nil {
		return ReconcileResult{}, err
	}
	s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &trade.AdID, &trade.ID, map[string]any{
		"from": from, "to": models.P2PTradeStatusResolved,
		"reason":      "chain Resolved",
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

func (s *P2PTradeService) resolveTokenAddress(symbol string) (string, error) {
	sym := strings.ToUpper(symbol)
	if strings.HasPrefix(sym, "0X") && len(sym) == 42 {
		return symbol, nil
	}
	if s.tokenResolver != nil {
		if addr := s.tokenResolver(sym); addr != "" {
			return addr, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrUnsupportedToken, symbol)
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

// tokenDecimalsFor returns the on-chain decimals for the Phase-1 token set.
// Phase 2 will replace this with a DB-backed registry, but for the three
// tokens we ship the lookup is constant.
func tokenDecimalsFor(symbol string) int {
	switch strings.ToUpper(symbol) {
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
