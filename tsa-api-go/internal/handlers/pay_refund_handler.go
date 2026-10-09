package handlers

import (
	"context"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// payRefundAPI is the slice of *services.PayRefundService the refund routes
// need. Nil leaves the refund routes unmounted (RefundsEnabled).
type payRefundAPI interface {
	Request(ctx context.Context, m *models.PayMerchant, mode string, paymentID uuid.UUID, in services.PayRefundRequest) (*models.PayRefund, error)
	Cancel(ctx context.Context, merchantID, id uuid.UUID) (*models.PayRefund, error)
	Submit(ctx context.Context, m *models.PayMerchant, id uuid.UUID, ownerWallet string, in services.PayRefundSubmit) (*models.PayRefund, error)
	Detail(ctx context.Context, m *models.PayMerchant, id uuid.UUID) (*services.PayRefundDetail, error)
	List(ctx context.Context, f services.PayRefundFilter) ([]services.PayRefundListItem, int64, error)
	ListForPayment(ctx context.Context, p *models.PayPayment) (*services.PayPaymentRefunds, error)
	AdminList(ctx context.Context, f services.PayRefundAdminFilter) ([]services.PayRefundAdminItem, int64, error)
	Reevaluate(ctx context.Context, id uuid.UUID) (*models.PayRefund, error)
	View(r *models.PayRefund) services.PayRefundView
}

// WithRefunds turns the refund routes on. Pass a real service or nothing —
// never a typed-nil pointer, which would read as wired.
func (h *PayHandler) WithRefunds(r payRefundAPI) *PayHandler {
	h.refunds = r
	return h
}

// RefundsEnabled reports whether the refund routes are mounted.
func (h *PayHandler) RefundsEnabled() bool { return h != nil && h.refunds != nil }
