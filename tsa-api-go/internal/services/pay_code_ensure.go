package services

import (
	"context"
	"errors"
	"log"
	"net/url"

	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// EnsurePayCode returns p's code, first giving it one if it is a payment from
// before codes existed — the migration backfills nothing (spec D8). The write
// only fills an empty code, so two readers racing on one payment store one code
// between them: the one whose write found it already filled reads the winner's.
// A code another payment has is drawn again. p.PayCode is set to the result.
// It writes with UpdateColumn so a read path does not bump updated_at.
func (s *PayPaymentService) EnsurePayCode(ctx context.Context, p *models.PayPayment) (string, error) {
	if p.PayCode != nil && *p.PayCode != "" {
		return *p.PayCode, nil
	}
	var stored string
	err := payTryFresh(payCodeNext, func(code string) error {
		res := s.db.WithContext(ctx).Model(&models.PayPayment{}).
			Where("id = ? AND pay_code IS NULL", p.ID).
			UpdateColumn("pay_code", code)
		if res.Error == nil && res.RowsAffected == 1 {
			stored = code
		}
		return res.Error
	}, payCodeTaken)
	if err != nil {
		return "", err
	}
	if stored == "" {
		var row models.PayPayment
		if err := s.db.WithContext(ctx).Select("id", "pay_code").First(&row, "id = ?", p.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", ErrPayPaymentNotFound
			}
			return "", err
		}
		if row.PayCode == nil {
			return "", ErrPayPaymentNotFound
		}
		stored = *row.PayCode
	}
	p.PayCode = &stored
	return stored, nil
}

// payCodeFor is p's code as a payer sees it. A succeeded payment from before
// codes is given its code here, the first time its receipt is shown. Failing to
// store one is logged and the read goes on without it: a receipt must not fail
// for want of its number. A pending one made before codes simply has none.
func (s *PayPaymentService) payCodeFor(ctx context.Context, p *models.PayPayment) string {
	if p.PayCode == nil && p.Status == models.PayStatusSucceeded {
		if _, err := s.EnsurePayCode(ctx, p); err != nil {
			log.Printf("[tsa-pay] payment code for %s: %v", p.ID, err)
		}
	}
	return formatStoredPayCode(p.PayCode)
}

// formatStoredPayCode is FormatPayCode for a nullable column.
func formatStoredPayCode(code *string) string {
	if code == nil {
		return ""
	}
	return FormatPayCode(*code)
}

// receiptURL is the public page that verifies p's receipt (spec D12): only a
// succeeded live payment has one — a test payment's would vouch for money that
// never moved. It lives on the hosted checkout's own site, at /r/<code>.
func (s *PayPaymentService) receiptURL(p *models.PayPayment) string {
	if p.Mode != models.PayModeLive || p.Status != models.PayStatusSucceeded || p.PayCode == nil {
		return ""
	}
	u, err := url.Parse(s.checkoutBase)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/r/" + FormatPayCode(*p.PayCode)
}
