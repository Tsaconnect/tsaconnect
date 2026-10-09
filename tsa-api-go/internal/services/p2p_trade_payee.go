package services

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// PublicPaymentMethod is the ad-page view of a payment method: enough to pick
// one and recognise it, never enough to pay it. The full account lives only in
// the trade's payee, which only the two trade parties can read. The account
// holder name is deliberately absent.
type PublicPaymentMethod struct {
	ID                 uuid.UUID `json:"id"`
	Type               string    `json:"type"`
	Provider           string    `json:"provider"`
	BankName           string    `json:"bankName"`
	AccountNumber      string    `json:"accountNumber"` // masked "******7890"
	AccountNumberLast4 string    `json:"accountNumberLast4"`
}

// maskAccountNumber keeps the last 4 digits. Numbers of 4 or fewer characters
// are masked entirely and expose no last4.
func maskAccountNumber(n string) (masked, last4 string) {
	if len(n) <= 4 {
		return strings.Repeat("*", len(n)), ""
	}
	last4 = n[len(n)-4:]
	return strings.Repeat("*", len(n)-4) + last4, last4
}

func PublicPaymentMethods(rows []models.P2PPaymentMethod) []PublicPaymentMethod {
	out := make([]PublicPaymentMethod, 0, len(rows))
	for _, r := range rows {
		masked, last4 := maskAccountNumber(r.AccountNumber)
		out = append(out, PublicPaymentMethod{
			ID: r.ID, Type: r.Type, Provider: r.Provider, BankName: r.BankName,
			AccountNumber: masked, AccountNumberLast4: last4,
		})
	}
	return out
}

// payeeVisible: the payee is shown from the moment the open is recorded on
// chain (any non-empty open_tx_hash, including the "reconciled" sentinel)
// until the fiat leg is over.
func payeeVisible(t *models.P2PTrade) bool {
	switch t.Status {
	case models.P2PTradeStatusPendingPayment:
		return t.OpenTxHash != ""
	case models.P2PTradeStatusPaid, models.P2PTradeStatusReleasing, models.P2PTradeStatusDisputed:
		return true
	}
	return false
}

// TradePayee returns the payee only while it is visible; nil otherwise.
func (s *P2PTradeService) TradePayee(ctx context.Context, t *models.P2PTrade) (*models.P2PPayee, error) {
	if t == nil || !payeeVisible(t) {
		return nil, nil
	}
	return s.loadPayee(ctx, t)
}

// loadPayee is the ungated lookup (admin reuses it). PaymentMethodID always
// belongs to trade.SellerID for both ad sides. Soft-deleted rows are included
// on purpose: deleting the method mid-trade must not hide where the payer was
// told to send money.
func (s *P2PTradeService) loadPayee(ctx context.Context, t *models.P2PTrade) (*models.P2PPayee, error) {
	var pm models.P2PPaymentMethod
	err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", t.PaymentMethodID, t.SellerID).First(&pm).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &models.P2PPayee{Provider: pm.Provider, BankName: pm.BankName, AccountNumber: pm.AccountNumber, AccountName: pm.AccountName}, nil
}
