package services

import (
	"context"
	"errors"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// PayReceiptView is a succeeded live payment's public receipt (spec D12): what
// tsaconnectworld.com/r/<code> shows to anyone holding the code. The payer is
// named by initials only. Amount is what the payer paid (customerTotal), in kobo.
type PayReceiptView struct {
	Code                      string    `json:"code"`
	MerchantName              string    `json:"merchantName"`
	Amount                    int64     `json:"amount"`
	Currency                  string    `json:"currency"`
	Description               string    `json:"description"`
	DescriptionIsCustomerNote bool      `json:"descriptionIsCustomerNote"`
	PaidAt                    time.Time `json:"paidAt"`
	PayerInitials             string    `json:"payerInitials"`
	RefundStatus              string    `json:"refundStatus"`   // none, partial or full
	RefundedAmount            string    `json:"refundedAmount"` // USD, "12.50"
}

// IDByCode is the payment a code names, for the app's TSA Pay screen, which
// then opens the ordinary checkout by id (spec D10). Any payment with the code
// answers — an untried open-link payment too: its id is already the visitor's.
// A code that could not be one is answered exactly as an unknown one.
func (s *PayPaymentService) IDByCode(ctx context.Context, code string) (uuid.UUID, error) {
	bare, ok := NormalizePayCode(code)
	if !ok {
		return uuid.Nil, ErrPayCodeNotFound
	}
	var p models.PayPayment
	err := s.db.WithContext(ctx).Select("id").Where("pay_code = ?", bare).Take(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return uuid.Nil, ErrPayCodeNotFound
	}
	if err != nil {
		return uuid.Nil, err
	}
	return p.ID, nil
}

// PublicReceipt is the receipt of the succeeded LIVE payment with this code.
// Anything else — pending, failed, unknown, malformed, and every test payment,
// whose receipt would vouch for money that never moved — is ErrPayCodeNotFound.
// Refund status comes from the refund hook (Plan 3a); unwired, none.
func (s *PayPaymentService) PublicReceipt(ctx context.Context, code string) (PayReceiptView, error) {
	bare, ok := NormalizePayCode(code)
	if !ok {
		return PayReceiptView{}, ErrPayCodeNotFound
	}
	var p models.PayPayment
	err := s.db.WithContext(ctx).
		Where("pay_code = ? AND mode = ? AND status = ?", bare, models.PayModeLive, models.PayStatusSucceeded).
		Take(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return PayReceiptView{}, ErrPayCodeNotFound
	}
	if err != nil {
		return PayReceiptView{}, err
	}
	m, err := s.merchants.Get(ctx, p.MerchantID)
	if err != nil {
		return PayReceiptView{}, err
	}
	payer, err := s.Payer(ctx, &p)
	if err != nil {
		return PayReceiptView{}, err
	}
	initials := ""
	if payer != nil && payer.Name != payerDeletedName {
		initials = payInitials(payer.Name)
	}
	// The public page's rule: never an API payment's description, which is the
	// merchant's own (booking codes, order details).
	description, kind, err := s.payPurpose(ctx, &p)
	if err != nil {
		return PayReceiptView{}, err
	}
	if p.Source == models.PaySourceAPI {
		description, kind = "", payPurposeNone
	}
	paidAt := p.UpdatedAt
	if p.SucceededAt != nil {
		paidAt = *p.SucceededAt
	}
	v := PayReceiptView{
		Code: FormatPayCode(bare), MerchantName: m.BusinessName, Amount: p.CustomerTotalKobo, Currency: p.Currency,
		Description: description, DescriptionIsCustomerNote: kind == payPurposeCustomerNote, PaidAt: paidAt, PayerInitials: initials,
		RefundStatus: "none", RefundedAmount: FormatMicroUSD(0),
	}
	if s.refundAgg != nil {
		aggs, err := s.refundAgg.AggregateByPayment(ctx, []uuid.UUID{p.ID})
		if err != nil {
			return PayReceiptView{}, err
		}
		if a, ok := aggs[p.ID]; ok {
			if a.Status != "" {
				v.RefundStatus = a.Status
			}
			v.RefundedAmount = FormatMicroUSD(a.RefundedMicroUSD)
		}
	}
	return v, nil
}

// payInitials is a name as the initials of its first and last words — "Ada
// Okafor" is "A.O." — so a public receipt never carries a payer's name.
func payInitials(name string) string {
	first, last := payNameEnds(name)
	initial := func(word string) string {
		if word == "" {
			return ""
		}
		r, _ := utf8.DecodeRuneInString(word)
		return string(unicode.ToUpper(r)) + "."
	}
	return initial(first) + initial(last)
}
