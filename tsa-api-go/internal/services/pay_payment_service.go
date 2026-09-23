package services

import (
	"context"
	"encoding/json"
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

	// payMinTimeToStart is the least time a payment may have left when a
	// customer FIRST locks it — the app refuses to start with < 5 min left.
	// A payer already holding the lock may resume past this (and past
	// expires_at entirely): see Lock.
	payMinTimeToStart = 5 * time.Minute
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

// PayCheckoutView is what the app's checkout screen reads: the payment plus
// whether THIS viewer may pay it right now, and why not when they can't.
type PayCheckoutView struct {
	ID                 uuid.UUID `json:"id"`
	MerchantName       string    `json:"merchantName"`
	Mode               string    `json:"mode"`
	Status             string    `json:"status"`
	Amount             int64     `json:"amount"`
	CustomerTotal      int64     `json:"customerTotal"`
	Fee                int64     `json:"fee"`
	MerchantNet        int64     `json:"merchantNet"`
	Currency           string    `json:"currency"`
	Description        string    `json:"description"`
	ExpiresAt          time.Time `json:"expiresAt"`
	SettlementMode     string    `json:"settlementMode"`
	SettleTokenSymbol  string    `json:"settleTokenSymbol,omitempty"`
	SettleTokenAddress string    `json:"settleTokenAddress,omitempty"`
	SettleChainID      int64     `json:"settleChainId,omitempty"`
	ReturnURL          string    `json:"returnUrl,omitempty"`
	Payable            bool      `json:"payable"`
	NotPayableWhy      string    `json:"notPayableWhy,omitempty"`
	YouArePaying       bool      `json:"youArePaying"`
}

// PayPublicView is the unauthenticated status page a customer's browser (or
// the merchant's own site, polling) can read without a session: no
// settlement detail, and the return URL only once the payment has actually
// succeeded.
type PayPublicView struct {
	ID            uuid.UUID `json:"id"`
	MerchantName  string    `json:"merchantName"`
	Status        string    `json:"status"`
	Amount        int64     `json:"amount"`
	CustomerTotal int64     `json:"customerTotal"`
	Currency      string    `json:"currency"`
	ExpiresAt     time.Time `json:"expiresAt"`
	ReturnURL     string    `json:"returnUrl,omitempty"` // only once succeeded
	AppURL        string    `json:"appUrl"`
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

// CheckoutView builds the app checkout screen's read shape for id: the
// payment, plus whether THIS user may pay it right now. It never mutates
// state — Lock is the only thing that does.
func (s *PayPaymentService) CheckoutView(ctx context.Context, user *models.User, id uuid.UUID, liveEnabled bool) (*PayCheckoutView, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	m, err := s.merchants.Get(ctx, p.MerchantID)
	if err != nil {
		return nil, err
	}

	youArePaying := p.Status == models.PayStatusProcessing && p.PayerUserID != nil && user != nil && *p.PayerUserID == user.ID
	payable, why := s.payability(p, m, liveEnabled, youArePaying)

	return &PayCheckoutView{
		ID:                 p.ID,
		MerchantName:       m.BusinessName,
		Mode:               p.Mode,
		Status:             p.Status,
		Amount:             p.AmountKobo,
		CustomerTotal:      p.CustomerTotalKobo,
		Fee:                p.CustomerTotalKobo - p.MerchantNetKobo,
		MerchantNet:        p.MerchantNetKobo,
		Currency:           p.Currency,
		Description:        p.Description,
		ExpiresAt:          p.ExpiresAt,
		SettlementMode:     p.SettlementMode,
		SettleTokenSymbol:  p.SettleTokenSymbol,
		SettleTokenAddress: p.SettleTokenAddress,
		SettleChainID:      p.SettleChainID,
		ReturnURL:          p.ReturnURL,
		Payable:            payable,
		NotPayableWhy:      why,
		YouArePaying:       youArePaying,
	}, nil
}

// payability decides whether p may be paid right now by a viewer for whom
// youArePaying already answers "is this the payer currently holding it".
// Mirrors (without mutating) the refusals Lock enforces — see Lock's doc
// comment for the property this must track.
func (s *PayPaymentService) payability(p *models.PayPayment, m *models.PayMerchant, liveEnabled, youArePaying bool) (bool, string) {
	if youArePaying {
		return true, ""
	}
	switch p.Status {
	case models.PayStatusProcessing:
		return false, "Someone else is completing this payment"
	case models.PayStatusPending:
		// fall through to the pending-specific checks below
	default:
		return false, "This payment is no longer payable"
	}

	now := s.now()
	if !now.Before(p.ExpiresAt) {
		return false, "This payment has expired"
	}
	if now.Add(payMinTimeToStart).After(p.ExpiresAt) {
		return false, "There isn't enough time left to start this payment"
	}
	if m.Status != models.PayMerchantActive {
		return false, "This merchant can't accept payments right now"
	}
	if p.Mode == models.PayModeLive && !liveEnabled {
		return false, "Live payments are not enabled yet"
	}
	return true, ""
}

// PublicView builds the unauthenticated status page's read shape for id.
func (s *PayPaymentService) PublicView(ctx context.Context, id uuid.UUID) (*PayPublicView, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	m, err := s.merchants.Get(ctx, p.MerchantID)
	if err != nil {
		return nil, err
	}
	v := &PayPublicView{
		ID:            p.ID,
		MerchantName:  m.BusinessName,
		Status:        p.Status,
		Amount:        p.AmountKobo,
		CustomerTotal: p.CustomerTotalKobo,
		Currency:      p.Currency,
		ExpiresAt:     p.ExpiresAt,
		AppURL:        "tsaconnect://pay?id=" + p.ID.String(),
	}
	if p.Status == models.PayStatusSucceeded {
		v.ReturnURL = p.ReturnURL
	}
	return v, nil
}

// Lock moves pending → processing for user, stamping locked_at, and is the
// single entry point that decides whether a payment may be paid right now.
// fresh is false when user already held the lock (a resume — the app
// re-opened the checkout screen, or came back from a failed attempt) rather
// than a brand-new start; a resume succeeds even past expires_at, since a
// payment already in flight must never be pulled out from under its payer by
// the clock.
//
// Every OTHER caller (a fresh lock) is refused when: the payment is not
// pending (another payer already holds it, or it finished/expired), there is
// less than payMinTimeToStart left, the merchant is suspended, the mode is
// live but live payments are disabled, or (live only) the payer is not
// KYC-verified.
func (s *PayPaymentService) Lock(ctx context.Context, user *models.User, id uuid.UUID, liveEnabled bool) (*models.PayPayment, bool, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, false, err
	}

	if p.Status == models.PayStatusProcessing && p.PayerUserID != nil && *p.PayerUserID == user.ID {
		return p, false, nil
	}
	if p.Status != models.PayStatusPending {
		return nil, false, ErrPayNotPayable
	}

	now := s.now()
	if now.Add(payMinTimeToStart).After(p.ExpiresAt) {
		return nil, false, ErrPayNotPayable
	}

	m, err := s.merchants.Get(ctx, p.MerchantID)
	if err != nil {
		return nil, false, err
	}
	if m.Status != models.PayMerchantActive {
		return nil, false, ErrPayNotPayable
	}
	if p.Mode == models.PayModeLive {
		if !liveEnabled {
			return nil, false, ErrPayNotPayable
		}
		if err := requireVerifiedForMoneyIn(ctx, s.db, user.ID); err != nil {
			return nil, false, err
		}
	}

	res := s.db.WithContext(ctx).Model(&models.PayPayment{}).
		Where("id = ? AND status = ?", id, models.PayStatusPending).
		Updates(map[string]any{
			"status":        models.PayStatusProcessing,
			"payer_user_id": user.ID,
			"locked_at":     &now,
		})
	if res.Error != nil {
		return nil, false, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, false, ErrPayNotPayable
	}

	locked, err := s.Get(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return locked, true, nil
}

// Release moves processing → pending and clears the payer, locked_at and
// both attempt links in one guarded UPDATE, so another customer can pay a
// payment whose attempt died. See the Global Constraints for when a caller
// may call this.
func (s *PayPaymentService) Release(ctx context.Context, id uuid.UUID, reason string) error {
	return s.transition(ctx, id, "id = ? AND status IN ?", []any{id, []string{models.PayStatusProcessing}},
		map[string]any{
			"status":            models.PayStatusPending,
			"payer_user_id":     nil,
			"locked_at":         nil,
			"sell_intent_id":    nil,
			"paycrest_order_id": nil,
			"failure_reason":    reason,
		}, "")
}

// LinkOrder attaches a Paycrest order as the payment's one attempt.
func (s *PayPaymentService) LinkOrder(ctx context.Context, id, orderID uuid.UUID) error {
	return s.linkAttempt(ctx, id, "paycrest_order_id", orderID)
}

// LinkIntent attaches a sell intent as the payment's one attempt.
func (s *PayPaymentService) LinkIntent(ctx context.Context, id, intentID uuid.UUID) error {
	return s.linkAttempt(ctx, id, "sell_intent_id", intentID)
}

// linkAttempt guards on the payment being processing with NEITHER attempt
// column set yet — a processing payment carries at most one attempt at a
// time, so a second start by the same or another caller is refused rather
// than silently replacing the first.
func (s *PayPaymentService) linkAttempt(ctx context.Context, id uuid.UUID, column string, val uuid.UUID) error {
	res := s.db.WithContext(ctx).Model(&models.PayPayment{}).
		Where("id = ? AND status = ? AND paycrest_order_id IS NULL AND sell_intent_id IS NULL", id, models.PayStatusProcessing).
		Update(column, val)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrPayInProgress
	}
	return nil
}

// Succeed moves processing → succeeded and queues the payment.succeeded
// webhook in the same transaction as the state change.
func (s *PayPaymentService) Succeed(ctx context.Context, id uuid.UUID, settlementRef string) error {
	now := s.now()
	return s.transition(ctx, id, "id = ? AND status IN ?", []any{id, []string{models.PayStatusProcessing}},
		map[string]any{
			"status":         models.PayStatusSucceeded,
			"settlement_ref": settlementRef,
			"succeeded_at":   &now,
		}, models.PayEventSucceeded)
}

// Fail moves processing → failed and queues the payment.failed webhook in
// the same transaction as the state change.
func (s *PayPaymentService) Fail(ctx context.Context, id uuid.UUID, reason string) error {
	return s.transition(ctx, id, "id = ? AND status IN ?", []any{id, []string{models.PayStatusProcessing}},
		map[string]any{
			"status":         models.PayStatusFailed,
			"failure_reason": reason,
		}, models.PayEventFailed)
}

// Cancel moves a merchant's own pending payment straight to expired — the
// merchant changed its mind before any customer started paying — and queues
// the payment.expired webhook in the same transaction.
func (s *PayPaymentService) Cancel(ctx context.Context, merchantID uuid.UUID, mode string, id uuid.UUID) error {
	return s.transition(ctx, id, "id = ? AND merchant_id = ? AND mode = ? AND status IN ?",
		[]any{id, merchantID, mode, []string{models.PayStatusPending}},
		map[string]any{
			"status":         models.PayStatusExpired,
			"failure_reason": "cancelled by the merchant",
		}, models.PayEventExpired)
}

// ExpirePendingForMerchant expires every pending payment of merchantID — the
// suspension side effect: a suspended merchant can no longer be paid, so
// nothing should sit around pretending it still can be.
func (s *PayPaymentService) ExpirePendingForMerchant(ctx context.Context, merchantID uuid.UUID) (int, error) {
	var ids []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&models.PayPayment{}).
		Where("merchant_id = ? AND status = ?", merchantID, models.PayStatusPending).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	return s.expireEach(ctx, ids, "merchant suspended")
}

// ExpireDue expires pending payments whose expires_at has passed, oldest
// first, up to limit — the periodic expiry sweep's unit of work. Selecting
// ids then expiring each through its own guarded transition (rather than an
// UPDATE ... LIMIT) is what keeps this portable across SQLite and Postgres.
func (s *PayPaymentService) ExpireDue(ctx context.Context, limit int) (int, error) {
	var ids []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&models.PayPayment{}).
		Where("status = ? AND expires_at < ?", models.PayStatusPending, s.now()).
		Order("expires_at").
		Limit(limit).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	return s.expireEach(ctx, ids, "expired")
}

// expireEach transitions each id pending → expired through its own guarded
// UPDATE. A row already moved on by someone else between the select and now
// (ErrPayTransition) is simply skipped, not an error for the whole sweep.
func (s *PayPaymentService) expireEach(ctx context.Context, ids []uuid.UUID, reason string) (int, error) {
	n := 0
	for _, id := range ids {
		err := s.transition(ctx, id, "id = ? AND status IN ?", []any{id, []string{models.PayStatusPending}},
			map[string]any{
				"status":         models.PayStatusExpired,
				"failure_reason": reason,
			}, models.PayEventExpired)
		switch {
		case err == nil:
			n++
		case errors.Is(err, ErrPayTransition):
			// already moved on; not this sweep's problem
		default:
			return n, err
		}
	}
	return n, nil
}

// SimulateTestOutcome is the in-app "simulate" button for a test-mode
// payment: locks it for user (refusing exactly as a real attempt would —
// another payer, expiry, a suspended merchant) and then resolves it.
func (s *PayPaymentService) SimulateTestOutcome(ctx context.Context, user *models.User, id uuid.UUID, approve bool) (*models.PayPayment, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Mode != models.PayModeTest {
		return nil, ErrPayNotPayable
	}
	if _, _, err := s.Lock(ctx, user, id, false); err != nil {
		return nil, err
	}
	if approve {
		if err := s.Succeed(ctx, id, "test"); err != nil {
			return nil, err
		}
	} else {
		if err := s.Fail(ctx, id, "declined in test mode"); err != nil {
			return nil, err
		}
	}
	return s.Get(ctx, id)
}

// transition performs one guarded state change — a single
// `UPDATE ... WHERE <where>` that only ever matches the payment's current
// legal predecessor state(s) — and, when event != "", the webhook delivery
// that transition owes, in one database transaction: a failure to write the
// delivery rolls the state change back with it. where/args must include
// `status IN ?` (every caller here follows `"id = ? AND status IN ?"` /
// `[]any{id, from}`, Cancel additionally scoping to merchant_id + mode).
func (s *PayPaymentService) transition(ctx context.Context, id uuid.UUID, where string, args []any, fields map[string]any, event string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.PayPayment{}).Where(where, args...).Updates(fields)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrPayTransition
		}
		if event == "" {
			return nil
		}
		var p models.PayPayment
		if err := tx.First(&p, "id = ?", id).Error; err != nil {
			return err
		}
		return s.enqueueWebhook(tx, &p, event)
	})
}

// enqueueWebhook inserts the delivery p's transition to event owes, inside
// tx — the same transaction as the state-changing UPDATE that called it, so
// a failure here (e.g. the deliveries table is unreachable) rolls the
// transition back too. A merchant with no webhook URL configured for p's
// mode is not queued at all — WebhookURLFor returns "".
func (s *PayPaymentService) enqueueWebhook(tx *gorm.DB, p *models.PayPayment, event string) error {
	var m models.PayMerchant
	if err := tx.First(&m, "id = ?", p.MerchantID).Error; err != nil {
		return err
	}
	url := m.WebhookURLFor(p.Mode)
	if url == "" {
		return nil
	}

	now := s.now()
	deliveryID := uuid.New()
	payload, err := json.Marshal(map[string]any{
		"id":       deliveryID,
		"type":     event,
		"livemode": p.Mode == models.PayModeLive,
		"created":  now.Unix(),
		"data":     s.View(p),
	})
	if err != nil {
		return err
	}

	delivery := models.PayWebhookDelivery{
		ID:            deliveryID,
		PaymentID:     p.ID,
		MerchantID:    p.MerchantID,
		Mode:          p.Mode,
		Event:         event,
		Payload:       string(payload),
		NextAttemptAt: &now,
	}
	return tx.Create(&delivery).Error
}
