package services

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// payRefundOpenFirst orders open refunds (the ones someone has to act on) first.
const payRefundOpenFirst = "CASE WHEN status IN ('requested', 'submitted', 'needs_review') THEN 0 ELSE 1 END"

// payRefundInProgressReason is why another refund can't be asked for while one is open.
const payRefundInProgressReason = "A refund is already in progress"

// payRefundCheckFailedReason is shown in place of the Refund button when a
// payment's refund capacity couldn't be read (a transient failure).
const payRefundCheckFailedReason = "Refund availability can't be checked right now — try again shortly."

// PayRefundFilter is one page of a merchant's refunds in a mode: Status ""
// is every status; From/To bound created_at ([From, To), zero is open).
type PayRefundFilter struct {
	MerchantID  uuid.UUID
	Mode        string
	Status      string
	From, To    time.Time
	Page, Limit int
}

// PayRefundListItem is a refund with the payment it is for.
type PayRefundListItem struct {
	PayRefundView
	PaymentCode  string `json:"paymentCode"`
	CustomerName string `json:"customerName"`
	Description  string `json:"description"`
}

// List is one page of the merchant's refunds, open first, then newest.
func (s *PayRefundService) List(ctx context.Context, f PayRefundFilter) ([]PayRefundListItem, int64, error) {
	f.Page, f.Limit = payPage(f.Page, f.Limit)
	scope := func() *gorm.DB {
		q := s.db.WithContext(ctx).Model(&models.PayRefund{}).Where("merchant_id = ? AND mode = ?", f.MerchantID, f.Mode)
		if f.Status != "" {
			q = q.Where("status = ?", f.Status)
		}
		return payBetween(q, "created_at", f.From, f.To)
	}
	var total int64
	if err := scope().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []models.PayRefund
	if err := scope().Order(payRefundOpenFirst).Order("created_at DESC").Order("id").Offset((f.Page - 1) * f.Limit).Limit(f.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	payments, err := s.paymentsOf(ctx, rows)
	if err != nil {
		return nil, 0, err
	}
	out := make([]PayRefundListItem, len(rows))
	for i := range rows {
		p := payments[rows[i].PaymentID]
		out[i] = PayRefundListItem{PayRefundView: s.View(&rows[i]), PaymentCode: formatStoredPayCode(p.PayCode), CustomerName: p.CustomerName, Description: p.Description}
	}
	return out, total, nil
}

// paymentsOf is the payments rows are for, in one query.
func (s *PayRefundService) paymentsOf(ctx context.Context, rows []models.PayRefund) (map[uuid.UUID]models.PayPayment, error) {
	out := map[uuid.UUID]models.PayPayment{}
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.PaymentID)
	}
	var ps []models.PayPayment
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&ps).Error; err != nil {
		return nil, err
	}
	for _, p := range ps {
		out[p.ID] = p
	}
	return out, nil
}

// PayPaymentRefunds is a payment's refunds with what is left to refund and
// whether a new refund can be asked for now (and, when not, why — the web
// shows the sentence in place of the Refund button).
type PayPaymentRefunds struct {
	Refunds             []PayRefundView `json:"refunds"`
	Cap                 string          `json:"cap"`
	Remaining           string          `json:"remaining"`
	Refundable          bool            `json:"refundable"`
	NotRefundableReason string          `json:"notRefundableReason"`
}

// ListForPayment is p's refunds, newest first, with its cap and remaining.
func (s *PayRefundService) ListForPayment(ctx context.Context, p *models.PayPayment) (*PayPaymentRefunds, error) {
	var rows []models.PayRefund
	if err := s.db.WithContext(ctx).Where("payment_id = ?", p.ID).Order("created_at DESC").Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := &PayPaymentRefunds{Refunds: make([]PayRefundView, len(rows)), Cap: FormatMicroUSD(0), Remaining: FormatMicroUSD(0)}
	var used int64
	open := false
	for i := range rows {
		out.Refunds[i] = s.View(&rows[i])
		switch rows[i].Status {
		case models.PayRefundConfirmed:
			used += rows[i].AmountMicroUSD
		case models.PayRefundRequested, models.PayRefundSubmitted, models.PayRefundNeedsReview:
			used += rows[i].AmountMicroUSD
			open = true
		}
	}
	src, _, err := s.prepare(ctx, p)
	if err != nil {
		if why := PayRefundRefusal(err); why != "" {
			out.NotRefundableReason = why
			return out, nil
		}
		// Only the capacity lookup failed: the refunds already made are still
		// worth showing, and a retry will size the rest.
		log.Printf("[tsa-pay] refund capacity for payment %s: %v", p.ID, err)
		out.NotRefundableReason = payRefundCheckFailedReason
		if open {
			out.NotRefundableReason = payRefundInProgressReason
		}
		return out, nil
	}
	remaining := max(src.capMicroUSD-used, 0)
	out.Cap, out.Remaining = FormatMicroUSD(src.capMicroUSD), FormatMicroUSD(remaining)
	switch {
	case open:
		out.NotRefundableReason = payRefundInProgressReason
	case remaining == 0:
		out.NotRefundableReason = "This payment has been fully refunded"
	default:
		out.Refundable = true
	}
	return out, nil
}

// PayRefundAdminFilter narrows the admin refund console: by status, by mode
// (live or test; "" is both), by one payment, or none of these.
type PayRefundAdminFilter struct {
	Status      string
	Mode        string
	PaymentID   *uuid.UUID
	Page, Limit int
}

// PayRefundAdminItem is a refund in the admin console, across merchants.
type PayRefundAdminItem struct {
	PayRefundView
	MerchantName string `json:"merchantName"`
	PaymentCode  string `json:"paymentCode"`
}

// AdminList is one page of every merchant's refunds, open first, then newest.
func (s *PayRefundService) AdminList(ctx context.Context, f PayRefundAdminFilter) ([]PayRefundAdminItem, int64, error) {
	f.Page, f.Limit = payPage(f.Page, f.Limit)
	scope := func() *gorm.DB {
		q := s.db.WithContext(ctx).Model(&models.PayRefund{})
		if f.Status != "" {
			q = q.Where("status = ?", f.Status)
		}
		if f.Mode != "" {
			q = q.Where("mode = ?", f.Mode)
		}
		if f.PaymentID != nil {
			q = q.Where("payment_id = ?", *f.PaymentID)
		}
		return q
	}
	var total int64
	if err := scope().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []models.PayRefund
	if err := scope().Order(payRefundOpenFirst).Order("created_at DESC").Order("id").Offset((f.Page - 1) * f.Limit).Limit(f.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	payments, err := s.paymentsOf(ctx, rows)
	if err != nil {
		return nil, 0, err
	}
	merchantIDs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		merchantIDs = append(merchantIDs, r.MerchantID)
	}
	names := map[uuid.UUID]string{}
	if len(merchantIDs) > 0 {
		var ms []models.PayMerchant
		if err := s.db.WithContext(ctx).Select("id", "business_name").Where("id IN ?", merchantIDs).Find(&ms).Error; err != nil {
			return nil, 0, err
		}
		for _, m := range ms {
			names[m.ID] = m.BusinessName
		}
	}
	out := make([]PayRefundAdminItem, len(rows))
	for i := range rows {
		out[i] = PayRefundAdminItem{PayRefundView: s.View(&rows[i]), MerchantName: names[rows[i].MerchantID], PaymentCode: formatStoredPayCode(payments[rows[i].PaymentID].PayCode)}
	}
	return out, total, nil
}

// PayMerchantRefund is what a payer reads of the merchant's refunds to her:
// partial or full once one is confirmed (Amount = the confirmed total, the
// rest from the latest confirmed refund), pending while one is only on its
// way. Not the same as PayHistoryRefunded, which is a returned payout attempt.
type PayMerchantRefund struct {
	Status      string     `json:"status"`
	Amount      string     `json:"amount"`
	TokenSymbol string     `json:"tokenSymbol,omitempty"`
	ChainID     int64      `json:"chainId,omitempty"`
	TxHash      string     `json:"txHash,omitempty"`
	ConfirmedAt *time.Time `json:"confirmedAt,omitempty"`
}

// payMerchantRefunds is the PayMerchantRefund of each of ids that has a
// confirmed or open refund, in one query.
func payMerchantRefunds(db *gorm.DB, ids []uuid.UUID) (map[uuid.UUID]*PayMerchantRefund, error) {
	out := map[uuid.UUID]*PayMerchantRefund{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []models.PayRefund
	if err := db.Where("payment_id IN ? AND status IN ?", ids, payRefundCountedStatuses).Order("created_at").Find(&rows).Error; err != nil {
		return nil, err
	}
	type tally struct{ confirmed, capMicro int64 }
	sums := map[uuid.UUID]*tally{}
	for _, r := range rows {
		mr, t := out[r.PaymentID], sums[r.PaymentID]
		if mr == nil {
			mr, t = &PayMerchantRefund{Status: "pending"}, &tally{}
			out[r.PaymentID], sums[r.PaymentID] = mr, t
		}
		t.capMicro = max(t.capMicro, r.CapMicroUSD)
		if r.Status == models.PayRefundConfirmed {
			t.confirmed += r.AmountMicroUSD
			mr.TokenSymbol, mr.ChainID, mr.ConfirmedAt, mr.TxHash = r.TokenSymbol, r.ChainID, r.ConfirmedAt, ""
			if r.TxHash != nil {
				mr.TxHash = *r.TxHash
			}
		}
	}
	for id, mr := range out {
		t := sums[id]
		mr.Amount = FormatMicroUSD(t.confirmed)
		if t.confirmed > 0 {
			mr.Status = payRefundStatus(t.confirmed, t.capMicro)
		}
	}
	return out, nil
}

// merchantRefundsFor is payMerchantRefunds when refunds are wired, else none.
func (s *PayPaymentService) merchantRefundsFor(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]*PayMerchantRefund {
	if s.refundAgg == nil {
		return nil
	}
	out, err := payMerchantRefunds(s.db.WithContext(ctx), ids)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[tsa-pay] merchant refunds for %d payments: %v", len(ids), err)
	}
	return out
}
