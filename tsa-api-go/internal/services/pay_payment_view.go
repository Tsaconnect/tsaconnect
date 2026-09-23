package services

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

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
	if payable && !youArePaying && p.Mode == models.PayModeLive && user != nil {
		// Say so up front rather than let the payer tap Pay and be refused.
		// Any other lookup error leaves payable as computed; Lock enforces it.
		if err := requireVerifiedForMoneyIn(ctx, s.db, user.ID); errors.Is(err, ErrKYCVerificationRequired) {
			payable, why = false, "Verify your identity to pay this"
		}
	}

	v := &PayCheckoutView{
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
		Payable:            payable,
		NotPayableWhy:      why,
		YouArePaying:       youArePaying,
	}
	// A return URL often carries the merchant's own order token, so it is
	// shown only once the payment has succeeded — like PublicView — not to
	// whoever merely holds the payment id.
	if p.Status == models.PayStatusSucceeded {
		v.ReturnURL = p.ReturnURL
	}
	return v, nil
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
