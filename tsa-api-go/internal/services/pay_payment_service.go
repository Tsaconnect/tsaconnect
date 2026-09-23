package services

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// payIntents is a placeholder for the sell-intent lookup Part B's
// checkout/conversion flow needs (Task 13). Declared here as an empty
// interface so PayPaymentService can carry the field now; Task 13 widens it
// and wires a real implementation via a setter.
type payIntents interface{}

const (
	payDefaultExpirySeconds = 1800
	payMinExpirySeconds     = 900
	payMaxExpirySeconds     = 86_400
	payMaxReferenceLen      = 128
	payMaxDescriptionLen    = 255
)

// CreatePayPaymentInput is a merchant's request to start one checkout.
type CreatePayPaymentInput struct {
	AmountKobo       int64  `json:"amount"`
	Currency         string `json:"currency"`
	Reference        string `json:"reference"`
	Description      string `json:"description"`
	ReturnURL        string `json:"returnUrl"`
	ExpiresInSeconds int    `json:"expiresIn"`
}

// PayPaymentView is the merchant-facing read shape of a payment: amounts in
// integer kobo, plus the app/checkout URLs a merchant's page redirects to.
type PayPaymentView struct {
	ID            uuid.UUID  `json:"id"`
	Reference     string     `json:"reference"`
	Mode          string     `json:"mode"`
	Status        string     `json:"status"`
	Amount        int64      `json:"amount"`
	CustomerTotal int64      `json:"customerTotal"`
	MerchantNet   int64      `json:"merchantNet"`
	Fee           int64      `json:"fee"`
	Currency      string     `json:"currency"`
	Description   string     `json:"description"`
	ReturnURL     string     `json:"returnUrl,omitempty"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	SettlementRef string     `json:"settlementRef,omitempty"`
	FailureReason string     `json:"failureReason,omitempty"`
	SucceededAt   *time.Time `json:"succeededAt,omitempty"`
	AppURL        string     `json:"appUrl"`
	CheckoutURL   string     `json:"checkoutUrl"`
	CreatedAt     time.Time  `json:"createdAt"`
}

var (
	// ErrPayPaymentNotFound is returned when no payment matches a merchant-
	// scoped lookup (wrong id, wrong merchant, or wrong mode).
	ErrPayPaymentNotFound = errors.New("payment not found")
	// ErrPayPaymentInvalid is wrapped with the specific reason a Create
	// refused the request body against the merchant's own settings.
	ErrPayPaymentInvalid = errors.New("invalid payment")
	// ErrPayReferenceConflict means a payment already exists for this
	// (merchant, mode, reference) with a different amount/currency/
	// description/return URL than the one just requested.
	ErrPayReferenceConflict = errors.New("a payment with this reference already exists with different details")
	// ErrPayMerchantNotReady is returned when the merchant has no
	// settlement destination configured yet.
	ErrPayMerchantNotReady = errors.New("this merchant can't take payments yet — settlement isn't configured")
)

// PayPaymentService creates and reads TSA Pay merchant payments.
type PayPaymentService struct {
	db           *gorm.DB
	merchants    *PayMerchantService
	feeBps       int
	checkoutBase string
	now          func() time.Time
	intents      payIntents // Part B (Task 13); nil until SetIntents
}

// NewPayPaymentService constructs a PayPaymentService. feeBps is the
// platform fee (PAYCREST_SENDER_FEE_PERCENT, as bps); checkoutBase is the
// public checkout page a merchant's customer is sent to.
func NewPayPaymentService(db *gorm.DB, merchants *PayMerchantService, feeBps int, checkoutBase string, now func() time.Time) *PayPaymentService {
	return &PayPaymentService{db: db, merchants: merchants, feeBps: feeBps, checkoutBase: checkoutBase, now: now}
}

// Create starts (or, for a retried reference, returns) one checkout for
// merchant m. The bool return is true only when a new row was inserted.
//
// Every field is validated against what m itself allows — amount bounds,
// NGN only, a reference 1-128 chars, an https return URL on one of m's
// exact allowed hostnames, and an expiry within [15m, 24h] (default 30m).
// A repeat of the same (mode, reference) with an identical amount,
// currency, description and return URL is idempotent and returns the
// existing payment; a repeat with any of those changed is refused as
// ErrPayReferenceConflict. expiresIn does not participate in that
// comparison — it only sizes a freshly created payment's ExpiresAt.
func (s *PayPaymentService) Create(ctx context.Context, m *models.PayMerchant, mode string, in CreatePayPaymentInput) (*models.PayPayment, bool, error) {
	if m.SettlementMode == "" {
		return nil, false, ErrPayMerchantNotReady
	}

	// Normalize (never validate the FORMAT of) the fields the idempotency
	// comparison keys on, then look up any existing payment for this
	// reference BEFORE validating bounds/format. A retried reference must
	// be recognized as a retry (identical body) or a conflict (changed
	// body) regardless of whether the changed value is itself in range —
	// e.g. a retry that shrinks the amount below the merchant's minimum is
	// a conflicting retry (409), not a fresh invalid request (422).
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		currency = "NGN"
	}
	reference := strings.TrimSpace(in.Reference)
	description := strings.TrimSpace(in.Description)
	returnURL := strings.TrimSpace(in.ReturnURL)

	var existing models.PayPayment
	err := s.db.WithContext(ctx).Where("merchant_id = ? AND mode = ? AND reference = ?", m.ID, mode, reference).First(&existing).Error
	switch {
	case err == nil:
		if paySnapshotMatches(&existing, in.AmountKobo, currency, description, returnURL) {
			return &existing, false, nil
		}
		return nil, false, ErrPayReferenceConflict
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, false, err
	}

	// No existing payment for this reference: this is a fresh request, so
	// now validate it fully against what the merchant allows.
	if currency != "NGN" {
		return nil, false, fmt.Errorf("%w: currency must be NGN", ErrPayPaymentInvalid)
	}
	if l := len(reference); l < 1 || l > payMaxReferenceLen {
		return nil, false, fmt.Errorf("%w: reference must be 1-%d characters", ErrPayPaymentInvalid, payMaxReferenceLen)
	}
	if in.AmountKobo < m.MinAmountKobo || in.AmountKobo > m.MaxAmountKobo {
		return nil, false, fmt.Errorf("%w: amount must be between %d and %d kobo", ErrPayPaymentInvalid, m.MinAmountKobo, m.MaxAmountKobo)
	}
	if len(description) > payMaxDescriptionLen {
		return nil, false, fmt.Errorf("%w: description must be at most %d characters", ErrPayPaymentInvalid, payMaxDescriptionLen)
	}
	if returnURL != "" {
		if err := validatePayReturnURL(returnURL, m.ReturnDomainList()); err != nil {
			return nil, false, fmt.Errorf("%w: %v", ErrPayPaymentInvalid, err)
		}
	}

	expiresIn := in.ExpiresInSeconds
	switch {
	case expiresIn == 0:
		expiresIn = payDefaultExpirySeconds
	case expiresIn < payMinExpirySeconds || expiresIn > payMaxExpirySeconds:
		return nil, false, fmt.Errorf("%w: expiresIn must be %d-%d seconds", ErrPayPaymentInvalid, payMinExpirySeconds, payMaxExpirySeconds)
	}

	split, err := PayAmounts(in.AmountKobo, s.feeBps, m.CustomerFeeSharePercent)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrPayPaymentInvalid, err)
	}

	now := s.now()
	p := &models.PayPayment{
		MerchantID:              m.ID,
		Mode:                    mode,
		Reference:               reference,
		AmountKobo:              in.AmountKobo,
		Currency:                currency,
		Description:             description,
		ReturnURL:               returnURL,
		FeeBps:                  s.feeBps,
		CustomerFeeSharePercent: m.CustomerFeeSharePercent,
		CustomerTotalKobo:       split.CustomerTotalKobo,
		MerchantNetKobo:         split.MerchantNetKobo,
		SettlementMode:          m.SettlementMode,
		SettleBankCode:          m.BankCode,
		SettleAccountNumber:     m.AccountNumber,
		SettleAccountName:       m.AccountName,
		SettleChainID:           m.ChainID,
		SettleTokenAddress:      m.TokenAddress,
		SettleTokenSymbol:       m.TokenSymbol,
		SettleWalletAddress:     m.WalletAddress,
		ExpiresAt:               now.Add(time.Duration(expiresIn) * time.Second),
		Status:                  models.PayStatusPending,
	}

	return s.createOrExisting(ctx, p, in)
}

// createOrExisting inserts p outside any transaction. If another request won
// an insert-race for the same (merchant, mode, reference) between Create's
// pre-check and this insert, the unique index refuses the row; we re-read
// the winner and apply the same idempotency comparison Create's pre-check
// does, rather than surfacing an error for a request that in fact succeeded.
func (s *PayPaymentService) createOrExisting(ctx context.Context, p *models.PayPayment, in CreatePayPaymentInput) (*models.PayPayment, bool, error) {
	if err := s.db.WithContext(ctx).Create(p).Error; err != nil {
		if !isUniqueViolation(err) {
			return nil, false, err
		}
		var existing models.PayPayment
		if reErr := s.db.WithContext(ctx).
			Where("merchant_id = ? AND mode = ? AND reference = ?", p.MerchantID, p.Mode, p.Reference).
			First(&existing).Error; reErr != nil {
			return nil, false, reErr
		}
		if paySnapshotMatches(&existing, p.AmountKobo, p.Currency, p.Description, p.ReturnURL) {
			return &existing, false, nil
		}
		return nil, false, ErrPayReferenceConflict
	}
	return p, true, nil
}

// paySnapshotMatches reports whether an existing payment was created from
// the same request body as the one now being made for the same reference —
// the idempotency contract: same reference + same body is the same
// payment; same reference + different body is a conflict. expiresIn is
// deliberately excluded.
func paySnapshotMatches(existing *models.PayPayment, amountKobo int64, currency, description, returnURL string) bool {
	return existing.AmountKobo == amountKobo &&
		existing.Currency == currency &&
		existing.Description == description &&
		existing.ReturnURL == returnURL
}

// GetForMerchant returns a payment only when it belongs to merchantID and
// was created under mode — a test key can never read a live payment or
// another merchant's payment, and vice versa.
func (s *PayPaymentService) GetForMerchant(ctx context.Context, merchantID uuid.UUID, mode string, id uuid.UUID) (*models.PayPayment, error) {
	var p models.PayPayment
	err := s.db.WithContext(ctx).Where("id = ? AND merchant_id = ? AND mode = ?", id, merchantID, mode).First(&p).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPayPaymentNotFound
		}
		return nil, err
	}
	return &p, nil
}

// Get returns one payment by id regardless of merchant/mode, for admin and
// internal (webhook/checkout) callers that already know it's the right one.
func (s *PayPaymentService) Get(ctx context.Context, id uuid.UUID) (*models.PayPayment, error) {
	var p models.PayPayment
	if err := s.db.WithContext(ctx).First(&p, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPayPaymentNotFound
		}
		return nil, err
	}
	return &p, nil
}

// View builds the merchant-facing read shape for p.
func (s *PayPaymentService) View(p *models.PayPayment) PayPaymentView {
	return PayPaymentView{
		ID:            p.ID,
		Reference:     p.Reference,
		Mode:          p.Mode,
		Status:        p.Status,
		Amount:        p.AmountKobo,
		CustomerTotal: p.CustomerTotalKobo,
		MerchantNet:   p.MerchantNetKobo,
		Fee:           p.CustomerTotalKobo - p.MerchantNetKobo,
		Currency:      p.Currency,
		Description:   p.Description,
		ReturnURL:     p.ReturnURL,
		ExpiresAt:     p.ExpiresAt,
		SettlementRef: p.SettlementRef,
		FailureReason: p.FailureReason,
		SucceededAt:   p.SucceededAt,
		AppURL:        "tsaconnect://pay?id=" + p.ID.String(),
		CheckoutURL:   strings.TrimRight(s.checkoutBase, "/") + "/" + p.ID.String(),
		CreatedAt:     p.CreatedAt,
	}
}

// validatePayReturnURL checks a merchant-supplied return URL at checkout-
// creation time: https only, no embedded credentials, default port only,
// and a lowercase hostname equal to one of the merchant's allowed return
// domains (a lookalike or subdomain does not match — the comparison is
// exact, not a suffix check).
func validatePayReturnURL(raw string, allowed []string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid return url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("return url must use https")
	}
	if u.User != nil {
		return fmt.Errorf("return url must not contain credentials")
	}
	if port := u.Port(); port != "" && port != "443" {
		return fmt.Errorf("return url must use the default https port")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("return url must have a host")
	}
	for _, d := range allowed {
		if host == d {
			return nil
		}
	}
	return fmt.Errorf("return url host %q is not an allowed return domain", host)
}
