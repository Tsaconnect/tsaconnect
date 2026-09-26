package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// payIntents is the slice of *SellIntentService the payment's reconciler and
// the checkout starter drive: open a pay intent, end one that will never
// settle, read what became of its swap, and prove a crypto delivery on-chain.
// A nil payIntents means converted payments are unavailable (ramp sell is off).
type payIntents interface {
	Create(ctx context.Context, user *models.User, in CreateSellIntentInput) (*models.RampSellIntent, *SellSwapQuote, error)
	AbandonForPay(ctx context.Context, id uuid.UUID, swapTxHash *string) error
	SwapStatus(ctx context.Context, id uuid.UUID) (*LiFiTransferStatus, error)
	ConfirmDelivery(ctx context.Context, id uuid.UUID) (destTx string, err error)
}

const (
	payDefaultExpirySeconds = 1800
	payMinExpirySeconds     = 900
	payMaxExpirySeconds     = 86_400
	payMaxReferenceLen      = 128
	payMaxDescriptionLen    = 255

	// payMinTimeToStart is the least time a payment may have left when a
	// customer FIRST locks it — the app refuses to start with < 5 min left.
	// A payer already holding the lock may resume past this (and past
	// expires_at entirely): see Lock.
	payMinTimeToStart = 5 * time.Minute

	// payStuckTestReleaseAfter is how long a test-mode payment may sit in
	// processing with no attempt linked before the reconciler releases it.
	// A test outcome resolves in milliseconds, so anything this old was
	// stranded by a crash between the lock and the resolve.
	payStuckTestReleaseAfter = 10 * time.Minute
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
	// ErrPayNotPayable is returned by Lock/CheckoutView/SimulateTestOutcome
	// for a payment that can't be paid right now: someone else is already
	// paying it, it's finished or expired, there's too little time left to
	// start, the merchant is suspended, or live payments are disabled.
	ErrPayNotPayable = errors.New("this payment can't be paid now")
	// ErrPayInProgress is returned by LinkOrder/LinkIntent when a payment
	// already carries an attempt — a processing payment holds at most one.
	ErrPayInProgress = errors.New("you already started paying this — finish or wait for it")
	// ErrPayForbidden is returned when the caller may not act on this payment
	// (someone else's test payment, say).
	ErrPayForbidden = errors.New("you can't do this on this payment")
	// ErrPayTransition is returned when a guarded state-changing call finds
	// the payment not in a state that allows it (the guarded UPDATE matched
	// zero rows).
	ErrPayTransition = errors.New("payment is not in a state that allows this")
)

// PayPaymentService creates and reads TSA Pay merchant payments.
type PayPaymentService struct {
	db           *gorm.DB
	merchants    *PayMerchantService
	feeBps       int
	checkoutBase string
	now          func() time.Time
	intents      payIntents       // nil until SetIntents (ramp sell off: converted payments unavailable)
	deposits     payDepositReader // nil until SetDepositReader: an expired order can then never be proven empty
	skipped      atomic.Int64     // reconcile ticks skipped in a row (noteTick)
	alerted      sync.Map         // alertOnce's memory: name+"/"+attemptID -> time.Time last alerted; pruneAlerted bounds it
}

// NewPayPaymentService constructs a PayPaymentService. feeBps is the
// platform fee (PAYCREST_SENDER_FEE_PERCENT, as bps); checkoutBase is the
// public checkout page a merchant's customer is sent to.
func NewPayPaymentService(db *gorm.DB, merchants *PayMerchantService, feeBps int, checkoutBase string, now func() time.Time) *PayPaymentService {
	return &PayPaymentService{db: db, merchants: merchants, feeBps: feeBps, checkoutBase: checkoutBase, now: now}
}

// payDepositReader reads the balance of a token at an address on one of our
// chains, keyed the way an order records them (chain key, token symbol).
// *BlockchainService fills it.
type payDepositReader interface {
	DepositBalance(chainKey, token, address string) (*big.Int, error)
}

// SetDepositReader wires the on-chain read that proves an expired order's
// deposit address is empty. Unset, no expired order is ever released: it waits
// and alerts. Never called with a typed-nil.
func (s *PayPaymentService) SetDepositReader(r payDepositReader) { s.deposits = r }

// SetIntents wires the sell-intent service the reconciler reads converted
// attempts through. Never called with a typed-nil: a nil interface is how the
// reconciler knows there are no converted attempts to look after.
func (s *PayPaymentService) SetIntents(i payIntents) { s.intents = i }

// IntentsWired reports whether converted and crypto payments can be followed.
func (s *PayPaymentService) IntentsWired() bool { return s.intents != nil }

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

// PayListFilter selects payments. MerchantID nil means every merchant;
// Mode/Status "" mean every mode/status. Query matches the reference or
// description (case-insensitive substring) or an id prefix.
type PayListFilter struct {
	MerchantID          *uuid.UUID
	Mode, Status, Query string
	Page, Limit         int
}

// List is one page of payments, newest first, with the total that matched.
func (s *PayPaymentService) List(ctx context.Context, f PayListFilter) ([]models.PayPayment, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit < 1 {
		f.Limit = 20
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	filter := func(q *gorm.DB) *gorm.DB {
		if f.MerchantID != nil {
			q = q.Where("merchant_id = ?", *f.MerchantID)
		}
		if f.Mode != "" {
			q = q.Where("mode = ?", f.Mode)
		}
		if f.Status != "" {
			q = q.Where("status = ?", f.Status)
		}
		if term := strings.ToLower(strings.TrimSpace(f.Query)); term != "" {
			// What she typed is matched literally: % and _ are not wildcards.
			esc := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(term)
			// One group, so the ORs never widen the merchant/mode/status filter.
			q = q.Where(s.db.Where(`LOWER(reference) LIKE ? ESCAPE '\'`, "%"+esc+"%").
				Or(`LOWER(description) LIKE ? ESCAPE '\'`, "%"+esc+"%").
				Or(`CAST(id AS TEXT) LIKE ? ESCAPE '\'`, esc+"%"))
		}
		return q
	}

	var total int64
	if err := filter(s.db.WithContext(ctx).Model(&models.PayPayment{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []models.PayPayment
	if err := filter(s.db.WithContext(ctx).Model(&models.PayPayment{})).
		Order("created_at DESC").Offset((f.Page - 1) * f.Limit).Limit(f.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// AdminList is the admin console's payments listing: every payment,
// optionally narrowed to one merchant/mode/status, newest first.
func (s *PayPaymentService) AdminList(ctx context.Context, merchantID *uuid.UUID, mode, status string, page, limit int) ([]models.PayPayment, int64, error) {
	return s.List(ctx, PayListFilter{MerchantID: merchantID, Mode: mode, Status: status, Page: page, Limit: limit})
}

// Deliveries returns every webhook delivery owed for paymentID, newest
// first — the admin console's per-payment delivery log.
func (s *PayPaymentService) Deliveries(ctx context.Context, paymentID uuid.UUID) ([]models.PayWebhookDelivery, error) {
	var rows []models.PayWebhookDelivery
	if err := s.db.WithContext(ctx).Where("payment_id = ?", paymentID).Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
