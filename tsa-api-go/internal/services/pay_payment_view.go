package services

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// PayPaymentView is the merchant-facing read shape of a payment: amounts in
// integer kobo, plus the app/checkout URLs a merchant's page redirects to.
// Source says whether the merchant's server made it or the owner sent it as a
// link; Cancelled tells a payment its merchant called off from one that ran
// out of time (both are expired).
type PayPaymentView struct {
	ID            uuid.UUID  `json:"id"`
	Reference     string     `json:"reference"`
	Mode          string     `json:"mode"`
	Status        string     `json:"status"`
	Cancelled     bool       `json:"cancelled,omitempty"`
	Source        string     `json:"source"`
	Amount        int64      `json:"amount"`
	CustomerTotal int64      `json:"customerTotal"`
	MerchantNet   int64      `json:"merchantNet"`
	Fee           int64      `json:"fee"`
	Currency      string     `json:"currency"`
	Description   string     `json:"description"`
	CustomerName  string     `json:"customerName,omitempty"`
	OpenLinkID    *uuid.UUID `json:"openLinkId,omitempty"`
	LinkLabel     string     `json:"linkLabel,omitempty"` // open-link payments, from Views: the link it was paid through
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
	Source             string    `json:"source"` // api, link or open: an open payment's description is the payer's own note
	Status             string    `json:"status"`
	Amount             int64     `json:"amount"`
	CustomerTotal      int64     `json:"customerTotal"`
	Fee                int64     `json:"fee"`
	MerchantNet        int64     `json:"merchantNet"`
	Currency           string    `json:"currency"`
	Description        string    `json:"description"`
	CustomerName       string    `json:"customerName,omitempty"`
	LinkLabel          string    `json:"linkLabel,omitempty"` // open-link payments: the link paid through (the note, the description, is often empty)
	ExpiresAt          time.Time `json:"expiresAt"`
	SettlementMode     string    `json:"settlementMode"`
	SettleTokenSymbol  string    `json:"settleTokenSymbol,omitempty"`
	SettleTokenAddress string    `json:"settleTokenAddress,omitempty"`
	SettleChainID      int64     `json:"settleChainId,omitempty"`
	ReturnURL          string    `json:"returnUrl,omitempty"`
	Payable            bool      `json:"payable"`
	NotPayableWhy      string    `json:"notPayableWhy,omitempty"`
	// NotPayableCode is set when the payer can fix the block themselves: one
	// of the KYC refusal codes, so the app can offer the next step.
	NotPayableCode string `json:"notPayableCode,omitempty"`
	YouArePaying   bool   `json:"youArePaying"`
	// OrderID is the payout order this payment's attempt opened: so a payer who
	// comes back to a processing payment can resume funding it, and so one whose
	// payment failed or needs chasing can report it (the report is filed on the
	// order). Only ever set for the payment's payer: nobody else may see it.
	OrderID *uuid.UUID `json:"orderId,omitempty"`
	// OrderStatus is that order's status as the Paycrest poller last recorded
	// it, so the payer's progress steps move on the checkout read the app
	// already polls — not a second, Paycrest-syncing poll of the order. Same
	// visibility as OrderID.
	OrderStatus string `json:"orderStatus,omitempty"`
	// FailureReason says why her payment failed — written for her ("support is
	// reviewing it") — and only to her.
	FailureReason string `json:"failureReason,omitempty"`
}

// PayPublicView is the unauthenticated status page a customer's browser (or
// the merchant's own site, polling) can read without a session: no
// settlement detail, and the return URL only once the payment has actually
// succeeded. A payment link's description and customer name are shown — the
// owner wrote them for the payer, who must see what they are being asked to pay
// for — and so is an open-link payment's description (the payer's own note) and
// the link's label. An API payment's are not: a merchant's server writes its
// own booking codes and order details there, never meant for whoever holds the
// id.
type PayPublicView struct {
	ID            uuid.UUID  `json:"id"`
	MerchantName  string     `json:"merchantName"`
	Mode          string     `json:"mode"`
	Source        string     `json:"source"`
	Status        string     `json:"status"`
	Cancelled     bool       `json:"cancelled,omitempty"`
	Amount        int64      `json:"amount"`
	CustomerTotal int64      `json:"customerTotal"`
	Currency      string     `json:"currency"`
	Description   string     `json:"description,omitempty"`  // links and open links only
	CustomerName  string     `json:"customerName,omitempty"` // links only
	LinkLabel     string     `json:"linkLabel,omitempty"`    // open links only: the link it was paid through
	OpenLinkURL   string     `json:"openLinkUrl,omitempty"`  // open links only, while that link takes payments
	ExpiresAt     time.Time  `json:"expiresAt"`
	SucceededAt   *time.Time `json:"succeededAt,omitempty"`
	ReturnURL     string     `json:"returnUrl,omitempty"` // only once succeeded
	AppURL        string     `json:"appUrl"`
}

// payCancelled reports whether p ended because its merchant called it off,
// rather than running out of time: both leave it expired.
func payCancelled(p *models.PayPayment) bool {
	return p.Status == models.PayStatusExpired && p.FailureReason == PayReasonMerchantCancelled
}

// Views is View for each of ps, with each open-link payment's LinkLabel filled
// in (one query) — its description is the customer's note, often empty, so the
// label is what says what it was for.
func (s *PayPaymentService) Views(ctx context.Context, ps []models.PayPayment) ([]PayPaymentView, error) {
	var ids []uuid.UUID
	for i := range ps {
		if ps[i].OpenLinkID != nil {
			ids = append(ids, *ps[i].OpenLinkID)
		}
	}
	labels, err := s.openLinkLabels(ctx, ids)
	if err != nil {
		return nil, err
	}
	views := make([]PayPaymentView, len(ps))
	for i := range ps {
		views[i] = s.View(&ps[i])
		if ps[i].OpenLinkID != nil {
			views[i].LinkLabel = labels[*ps[i].OpenLinkID]
		}
	}
	return views, nil
}

// CheckoutURL is the public checkout page for payment id.
func (s *PayPaymentService) CheckoutURL(id uuid.UUID) string {
	return strings.TrimRight(s.checkoutBase, "/") + "/" + id.String()
}

// View builds the merchant-facing read shape for p from the row alone, so it
// leaves LinkLabel empty: that needs the open link. Views fills it in for a
// page of payments with one query; a caller already holding the link sets it
// itself. The webhook payload is a View.
func (s *PayPaymentService) View(p *models.PayPayment) PayPaymentView {
	return PayPaymentView{
		ID:            p.ID,
		Reference:     p.Reference,
		Mode:          p.Mode,
		Status:        p.Status,
		Cancelled:     payCancelled(p),
		Source:        p.Source,
		Amount:        p.AmountKobo,
		CustomerTotal: p.CustomerTotalKobo,
		MerchantNet:   p.MerchantNetKobo,
		Fee:           p.CustomerTotalKobo - p.MerchantNetKobo,
		Currency:      p.Currency,
		Description:   p.Description,
		CustomerName:  p.CustomerName,
		OpenLinkID:    p.OpenLinkID,
		ReturnURL:     p.ReturnURL,
		ExpiresAt:     p.ExpiresAt,
		SettlementRef: p.SettlementRef,
		FailureReason: p.FailureReason,
		SucceededAt:   p.SucceededAt,
		AppURL:        "tsaconnect://pay?id=" + p.ID.String(),
		CheckoutURL:   s.CheckoutURL(p.ID),
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
	var whyCode string
	if payable && !youArePaying && p.Mode == models.PayModeLive && user != nil {
		// Say so up front rather than let the payer tap Pay and be refused.
		// Any other lookup error leaves payable as computed; Lock enforces it.
		whyCode = KYCRefusalCode(requireVerifiedForMoneyIn(ctx, s.db, user.ID))
		switch whyCode {
		case KYCVerificationRequiredCode:
			payable, why = false, "Verify your identity to pay this"
		case KYCLegalNameMissingCode:
			payable, why = false, "We couldn't confirm the legal name on your verified identity. Contact support to pay this."
		}
	}

	v := &PayCheckoutView{
		ID:                 p.ID,
		MerchantName:       m.BusinessName,
		Mode:               p.Mode,
		Source:             p.Source,
		Status:             p.Status,
		Amount:             p.AmountKobo,
		CustomerTotal:      p.CustomerTotalKobo,
		Fee:                p.CustomerTotalKobo - p.MerchantNetKobo,
		MerchantNet:        p.MerchantNetKobo,
		Currency:           p.Currency,
		Description:        p.Description,
		CustomerName:       p.CustomerName,
		ExpiresAt:          p.ExpiresAt,
		SettlementMode:     p.SettlementMode,
		SettleTokenSymbol:  p.SettleTokenSymbol,
		SettleTokenAddress: p.SettleTokenAddress,
		SettleChainID:      p.SettleChainID,
		Payable:            payable,
		NotPayableWhy:      why,
		NotPayableCode:     whyCode,
		YouArePaying:       youArePaying,
	}
	if p.OpenLinkID != nil {
		labels, err := s.openLinkLabels(ctx, []uuid.UUID{*p.OpenLinkID})
		if err != nil {
			return nil, err
		}
		v.LinkLabel = labels[*p.OpenLinkID]
	}
	isPayer := p.PayerUserID != nil && user != nil && *p.PayerUserID == user.ID
	if isPayer && p.Status == models.PayStatusFailed {
		v.FailureReason = p.FailureReason
	}
	if isPayer && p.PaycrestOrderID != nil {
		v.OrderID = p.PaycrestOrderID
		// Scoped to the payer as well as the id: the order is theirs, and a
		// failed read only leaves the steps where they were.
		var order models.PaycrestOrderRecord
		if err := s.db.WithContext(ctx).Select("status").
			Where("id = ? AND user_id = ?", *p.PaycrestOrderID, user.ID).
			Take(&order).Error; err == nil {
			v.OrderStatus = order.Status
		}
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
	if p.Mode == models.PayModeLive && !m.LiveAllowed(liveEnabled) {
		return false, "Live payments are not enabled yet"
	}
	return true, ""
}

// PublicView builds the unauthenticated status page's read shape for id.
// liveEnabled is the global live switch, for whether an open link still takes
// payments.
func (s *PayPaymentService) PublicView(ctx context.Context, id uuid.UUID, liveEnabled bool) (*PayPublicView, error) {
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
		Mode:          p.Mode,
		Source:        p.Source,
		Status:        p.Status,
		Cancelled:     payCancelled(p),
		Amount:        p.AmountKobo,
		CustomerTotal: p.CustomerTotalKobo,
		Currency:      p.Currency,
		ExpiresAt:     p.ExpiresAt,
		SucceededAt:   p.SucceededAt,
		AppURL:        "tsaconnect://pay?id=" + p.ID.String(),
	}
	switch p.Source {
	case models.PaySourceLink:
		v.Description, v.CustomerName = p.Description, p.CustomerName
	case models.PaySourceOpen:
		v.Description = p.Description // the customer's own note, if any
		if p.OpenLinkID != nil {
			var l models.PayOpenLink
			if err := s.db.WithContext(ctx).First(&l, "id = ?", *p.OpenLinkID).Error; err != nil {
				return nil, err
			}
			v.LinkLabel = l.Label
			// So a payer whose payment ran out can start another there.
			v.OpenLinkURL = s.openLinkURLIfTaking(p, &l, m, liveEnabled)
		}
	}
	if p.Status == models.PayStatusSucceeded {
		v.ReturnURL = p.ReturnURL
	}
	return v, nil
}

// PayPayerView is who paid (or is paying) a payment, as its merchant sees it in
// the dashboard: a name only — never an email, phone or account id. The merchant
// already knows its customer through its own reference; this tells it which TSA
// Connect account actually paid.
type PayPayerView struct {
	Name string `json:"name"`
}

// payerDeletedName stands in for an account that is being or has been deleted:
// its name — above all the one read off its ID — stops reaching merchants.
const payerDeletedName = "Deleted account"

// Payer is the payment's payer, or nil when nobody has taken it up (a payment
// back to pending has no payer) or the account no longer has a name. A verified
// payer is named as on their ID.
func (s *PayPaymentService) Payer(ctx context.Context, p *models.PayPayment) (*PayPayerView, error) {
	if p.PayerUserID == nil {
		return nil, nil
	}
	var row struct {
		Name          sql.NullString
		AccountStatus sql.NullString
	}
	res := s.db.WithContext(ctx).Table("users").Select("name", "account_status").
		Where("id = ?", *p.PayerUserID).Limit(1).Scan(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	if row.AccountStatus.String == models.AccountStatusDeleted {
		return &PayPayerView{Name: payerDeletedName}, nil
	}
	verified, legalName, err := verifiedIdentity(ctx, s.db, *p.PayerUserID)
	if err != nil {
		return nil, err
	}
	if verified && legalName != "" {
		return &PayPayerView{Name: legalName}, nil
	}
	if name := strings.TrimSpace(row.Name.String); name != "" {
		return &PayPayerView{Name: name}, nil
	}
	return nil, nil
}

// PayHistoryItem is one payment in a payer's own history: who she paid, how
// much, and where it stands. Never the payout order behind it as a sale of
// hers — except to reach a refund (OrderID), which lives on that order.
type PayHistoryItem struct {
	ID            uuid.UUID  `json:"id"`
	MerchantName  string     `json:"merchantName"`
	Description   string     `json:"description,omitempty"`
	Status        string     `json:"status"`
	CustomerTotal int64      `json:"customerTotal"`
	Currency      string     `json:"currency"`
	PaidAt        time.Time  `json:"paidAt"`
	SucceededAt   *time.Time `json:"succeededAt,omitempty"`
	// OrderID is set on a refunded past attempt: its refund transaction, and
	// any report of it, are on that order. The payment itself may have been
	// paid since, or be payable again, so it is not this row's receipt.
	OrderID *uuid.UUID `json:"orderId,omitempty"`
}

// PayHistoryRefunded is the Status of a refunded past attempt. Not a payment
// status: the payment moved on (released for another try) once it was refunded.
const PayHistoryRefunded = "refunded"

// payHistoryCap bounds the history in one read; the app lists it unpaged, like bills.
const payHistoryCap = 50

// PayerHistory is userID's live payments, newest first:
//   - the payments she is the payer of — paying, paid, or failed for support;
//   - her past attempts that were REFUNDED. A refund releases the payment (it
//     clears the payer so she, or anyone, can try again), yet her money moved
//     and came back — that must stay on her record.
//
// An attempt released with nothing sent (a lapsed order, a refused start) is
// not listed: no money of hers moved. Test payments move none either. Two
// joined queries, no lookup per row.
func (s *PayPaymentService) PayerHistory(ctx context.Context, userID uuid.UUID) ([]PayHistoryItem, error) {
	type row struct {
		ID                uuid.UUID
		OrderID           *uuid.UUID
		BusinessName      string
		Description       string
		Status            string
		CustomerTotalKobo int64
		Currency          string
		LockedAt          *time.Time
		CreatedAt         time.Time
		SucceededAt       *time.Time
	}
	// When she paid, not when the merchant raised the payment. Picked here, not
	// by COALESCE in SQL, so the time keeps its type on every driver.
	paidAt := func(r row) time.Time {
		if r.LockedAt != nil {
			return *r.LockedAt
		}
		return r.CreatedAt
	}
	var current []row
	err := s.db.WithContext(ctx).
		Table("pay_payments AS p").
		Select("p.id, m.business_name, p.description, p.status, p.customer_total_kobo, p.currency, "+
			"p.locked_at, p.created_at, p.succeeded_at").
		Joins("JOIN pay_merchants AS m ON m.id = p.merchant_id").
		Where("p.payer_user_id = ? AND p.mode = ?", userID, models.PayModeLive).
		Order("COALESCE(p.locked_at, p.created_at) DESC").
		Limit(payHistoryCap).
		Scan(&current).Error
	if err != nil {
		return nil, err
	}
	var refunded []row
	err = s.db.WithContext(ctx).
		Table("paycrest_order_records AS o").
		Select("p.id, o.id AS order_id, m.business_name, p.description, ? AS status, p.customer_total_kobo, p.currency, "+
			"o.created_at", PayHistoryRefunded).
		Joins("JOIN pay_payments AS p ON p.id = o.pay_payment_id").
		Joins("JOIN pay_merchants AS m ON m.id = p.merchant_id").
		// No longer the payment's attempt: a current one is listed above, as the payment.
		Where("o.user_id = ? AND o.status = ? AND p.mode = ? AND (p.paycrest_order_id IS NULL OR p.paycrest_order_id <> o.id)",
			userID, models.PaycrestStatusRefunded, models.PayModeLive).
		Order("o.created_at DESC").
		Limit(payHistoryCap).
		Scan(&refunded).Error
	if err != nil {
		return nil, err
	}

	rows := append(current, refunded...)
	sort.SliceStable(rows, func(i, j int) bool { return paidAt(rows[i]).After(paidAt(rows[j])) })
	if len(rows) > payHistoryCap {
		rows = rows[:payHistoryCap]
	}
	out := make([]PayHistoryItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, PayHistoryItem{
			ID: r.ID, OrderID: r.OrderID, MerchantName: r.BusinessName, Description: r.Description, Status: r.Status,
			CustomerTotal: r.CustomerTotalKobo, Currency: r.Currency, PaidAt: paidAt(r), SucceededAt: r.SucceededAt,
		})
	}
	return out, nil
}
