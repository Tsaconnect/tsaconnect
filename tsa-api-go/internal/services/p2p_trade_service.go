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
	PrepareCloseAd(adId [32]byte, seller string) ([]byte, error)
	VerifyAdCreated(txHash string, expectedAdId [32]byte) (*P2PAdCreatedEvent, error)
	VerifyTradeOpened(txHash string, expectedTradeId [32]byte) (*P2PTradeOpenedEvent, error)
	VerifyTradeReleased(txHash string, expectedTradeId [32]byte) (*P2PTradeReleasedEvent, error)
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
}

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

// ─── Parameter structs ──────────────────────────────────────────────────────

type CreateAdParams struct {
	Token            string
	TokenAmount      string // base units; e.g. "1000000000" = 1000 USDT
	FiatCurrency     string
	Rate             string // fiat per 1 token base unit
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
		MinLimit:         params.MinLimit,
		MaxLimit:         params.MaxLimit,
		PaymentMethodIDs: stringifyUUIDs(params.PaymentMethodIDs),
		Terms:            params.Terms,
		Status:           models.P2PAdStatusPending,
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

	tokenAmount, fiatAmount, err := computeTokenFromFiat(params.FiatAmount, ad.Rate)
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
		Rate:            ad.Rate,
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
	if trade.Status != models.P2PTradeStatusPaid && trade.Status != models.P2PTradeStatusReleasing {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}

	// Transition Paid → Releasing FIRST (atomically), then prepare the
	// unsigned tx. This closes the race with RaiseDispute (Step 8): a
	// concurrent dispute from Paid will win the optimistic lock here, and
	// the seller will get ErrInvalidTransition instead of obtaining a
	// signable release-tx that could later land on-chain after dispute.
	// If status is already Releasing (retry), skip the transition.
	if trade.Status == models.P2PTradeStatusPaid {
		res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
			Where("id = ? AND status = ?", trade.ID, models.P2PTradeStatusPaid).
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
	return &trade, nil
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
	if strings.TrimSpace(p.Rate) == "" {
		return fmt.Errorf("%w: rate", ErrInvalidParams)
	}
	if len(p.PaymentMethodIDs) == 0 {
		return fmt.Errorf("%w: at least one payment_method_id required", ErrInvalidParams)
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

// computeTokenFromFiat divides fiat by rate to get token base units.
// Rate is a fractional string (NUMERIC(38,10) in DB); we scale to integer
// arithmetic by 10^10. Truncates remainder (round-down).
func computeTokenFromFiat(fiatStr, rateStr string) (token, fiat *big.Int, err error) {
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
	numerator := new(big.Int).Mul(fiat, new(big.Int).Exp(big.NewInt(10), big.NewInt(rateScale), nil))
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
