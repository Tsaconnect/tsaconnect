package services

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// PayRefundRequest is a refund asked for: Amount in USD (≤ 6 dp), the
// merchant's own Reason, and Via — the web or the app (set by the handler).
type PayRefundRequest struct {
	Amount string `json:"amount"`
	Reason string `json:"reason"`
	Via    string `json:"-"`
}

// PayRefundRefusal is the owner-facing sentence for why a payment can't be
// refunded, "" when err is not such a refusal.
func PayRefundRefusal(err error) string {
	switch {
	case errors.Is(err, ErrPayRefundNotSucceeded):
		return "Only paid payments can be refunded"
	case errors.Is(err, ErrPayRefundNotRefundable):
		return "Refunds aren't available for this payment"
	case errors.Is(err, ErrPayRefundPayerDeleted):
		return "The customer's account was deleted, so this payment can't be refunded"
	case errors.Is(err, ErrPayRefundUnavailable):
		return "Refunds aren't available right now — try again later"
	case errors.Is(err, ErrPayRefundNonceUsed):
		return "Your wallet has already sent a transaction in this one's place — sign the refund again"
	case errors.Is(err, ErrPayRefundNonceFar):
		return "Your wallet can't send this transaction until earlier ones go through — sign the refund again"
	}
	return ""
}

// Request opens a refund of in.Amount on the merchant's payment (D17, D18).
// One transaction locks the payment row, then checks the one-open rule and
// the cap: confirmed + open + this ≤ cap. The partial unique index backs the
// one-open rule against any race the lock misses.
//
// A test refund moves no money: it is confirmed at once (D26). A live one
// waits in requested for the owner's app to sign it; the owner is told only
// when it was asked for on the web.
func (s *PayRefundService) Request(ctx context.Context, m *models.PayMerchant, mode string, paymentID uuid.UUID, in PayRefundRequest) (*models.PayRefund, error) {
	amount, err := ParseMicroUSD(in.Amount)
	if err != nil {
		return nil, ErrPayRefundInvalid
	}
	p, err := s.payments.GetForMerchant(ctx, m.ID, mode, paymentID)
	if err != nil {
		return nil, err
	}
	src, to, err := s.prepare(ctx, p)
	if err != nil {
		return nil, err
	}
	via := models.PayRefundViaWeb
	if in.Via == models.PayRefundViaApp {
		via = models.PayRefundViaApp
	}
	now := s.now()
	r := &models.PayRefund{ID: uuid.New(), PaymentID: p.ID, MerchantID: p.MerchantID, Mode: p.Mode, AmountMicroUSD: amount,
		CapMicroUSD: src.capMicroUSD, Reason: truncateRunes(strings.TrimSpace(in.Reason), 255), ToAddress: to,
		Status: models.PayRefundRequested, RequestedVia: via, CreatedAt: now, UpdatedAt: now}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked models.PayPayment
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").First(&locked, "id = ?", p.ID).Error; err != nil {
			return err
		}
		if locked.Status != models.PayStatusSucceeded {
			return ErrPayRefundNotSucceeded
		}
		var open int64
		if err := tx.Model(&models.PayRefund{}).Where("payment_id = ? AND status IN ?", p.ID, payRefundOpenStatuses).Count(&open).Error; err != nil {
			return err
		}
		if open > 0 {
			return ErrPayRefundOpen
		}
		var used int64
		if err := tx.Model(&models.PayRefund{}).Where("payment_id = ? AND status IN ?", p.ID, payRefundCountedStatuses).
			Select("CAST(COALESCE(SUM(amount_micro_usd), 0) AS BIGINT)").Row().Scan(&used); err != nil {
			return err
		}
		if used+amount > src.capMicroUSD {
			return &PayRefundOverCapError{RemainingMicroUSD: max(src.capMicroUSD-used, 0)}
		}
		if err := tx.Create(r).Error; err != nil {
			if isPayRefundOneOpenViolation(err) {
				return ErrPayRefundOpen
			}
			return err
		}
		if r.Mode == models.PayModeTest {
			// D26: no money moves, so it is confirmed — and its webhook queued —
			// in this same transaction. A failure leaves no half-made refund
			// behind to block the payment (review M7).
			if _, err := s.claimConfirmedIn(tx, r, []string{models.PayRefundRequested}, amount); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if r.Mode == models.PayModeTest {
		// r is the row as claimed: re-reading it could fail after the commit and
		// send the owner to retry — simulating a second refund.
		s.announce(ctx, r, events.PayRefundConfirmed) // after commit
		return r, nil
	}
	if via == models.PayRefundViaWeb {
		s.announce(ctx, r, events.PayRefundRequested)
	}
	return r, nil
}

// isPayRefundOneOpenViolation reports whether err is the one-open partial index
// refusing a second open refund — and not any other unique violation on
// pay_refunds. Postgres names the index; SQLite names its column, which no
// other unique index on the table covers.
func isPayRefundOneOpenViolation(err error) bool {
	if !isUniqueViolation(err) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "idx_pay_refunds_one_open") || strings.Contains(msg, "pay_refunds.payment_id")
}

// prepare is what a new refund on p is sized and sent by — its source (cap,
// funding wallet, default network) and its destination — or why p can't be
// refunded right now.
func (s *PayRefundService) prepare(ctx context.Context, p *models.PayPayment) (*payRefundSource, string, error) {
	if p.Status != models.PayStatusSucceeded {
		return nil, "", ErrPayRefundNotSucceeded
	}
	if p.Mode == models.PayModeLive && (s.network == nil || s.verifier == nil) {
		return nil, "", ErrPayRefundUnavailable
	}
	src, err := s.source(ctx, p)
	if err != nil {
		return nil, "", err
	}
	to, err := s.destination(ctx, p, src.fundingWallet)
	if err != nil {
		return nil, "", err
	}
	return src, to, nil
}

// destination is where a refund on p goes (D19): the payer's current wallet
// when she has proven she holds it, else the wallet that funded the payment.
// A deleted payer is refused. A test payment may have neither (no money moves).
func (s *PayRefundService) destination(ctx context.Context, p *models.PayPayment, funding string) (string, error) {
	if p.PayerUserID == nil {
		if p.Mode == models.PayModeTest {
			return "", nil
		}
		return "", ErrPayRefundNotRefundable
	}
	var u struct {
		WalletAddress sql.NullString
		AccountStatus sql.NullString
	}
	res := s.db.WithContext(ctx).Table("users").Select("wallet_address", "account_status").
		Where("id = ?", *p.PayerUserID).Limit(1).Scan(&u)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 || u.AccountStatus.String == models.AccountStatusDeleted {
		return "", ErrPayRefundPayerDeleted
	}
	if w := strings.TrimSpace(u.WalletAddress.String); common.IsHexAddress(w) {
		proven, err := WalletProven(s.db.WithContext(ctx), *p.PayerUserID, w)
		if err != nil {
			return "", err
		}
		if proven {
			return common.HexToAddress(w).Hex(), nil
		}
	}
	if common.IsHexAddress(funding) {
		return common.HexToAddress(funding).Hex(), nil
	}
	if p.Mode == models.PayModeTest {
		return "", nil
	}
	return "", ErrPayRefundNotRefundable
}

// Get is the merchant's refund id — in either mode: the row says which.
func (s *PayRefundService) Get(ctx context.Context, merchantID, id uuid.UUID) (*models.PayRefund, error) {
	var r models.PayRefund
	err := s.db.WithContext(ctx).First(&r, "id = ? AND merchant_id = ?", id, merchantID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPayRefundNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Cancel withdraws a refund nobody has sent yet (D22). Once the owner's app
// has handed us a signed transfer, it can't be withdrawn: money may be moving.
func (s *PayRefundService) Cancel(ctx context.Context, merchantID, id uuid.UUID) (*models.PayRefund, error) {
	res := s.db.WithContext(ctx).Model(&models.PayRefund{}).
		Where("id = ? AND merchant_id = ? AND status = ?", id, merchantID, models.PayRefundRequested).
		Update("status", models.PayRefundCancelled)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if _, err := s.Get(ctx, merchantID, id); err != nil {
			return nil, err
		}
		return nil, ErrPayRefundState
	}
	return s.Get(ctx, merchantID, id)
}

// claimConfirmed moves r to confirmed from one of `from`, once (D25): only the
// caller whose guarded UPDATE moved the row queues payment.refunded — in the
// same transaction — and, after commit, tells the payer and the owner. false
// with a nil error means another caller already confirmed (or closed) it.
func (s *PayRefundService) claimConfirmed(ctx context.Context, r *models.PayRefund, from []string, creditedMicroUSD int64) (bool, error) {
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		claimed, err = s.claimConfirmedIn(tx, r, from, creditedMicroUSD)
		return err
	})
	if err != nil || !claimed {
		return false, err
	}
	s.announce(ctx, r, events.PayRefundConfirmed)
	return true, nil
}

// claimConfirmedIn is claimConfirmed's write inside the caller's tx: the
// guarded UPDATE and, only when it moved the row, the payment.refunded
// delivery. It announces nothing — the caller does that after commit.
func (s *PayRefundService) claimConfirmedIn(tx *gorm.DB, r *models.PayRefund, from []string, creditedMicroUSD int64) (bool, error) {
	now := s.now()
	res := tx.Model(&models.PayRefund{}).Where("id = ? AND status IN ?", r.ID, from).Updates(map[string]any{
		"status": models.PayRefundConfirmed, "credited_micro_usd": creditedMicroUSD, "confirmed_at": &now, "failure_reason": ""})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected != 1 {
		return false, nil
	}
	r.Status, r.CreditedMicroUSD, r.ConfirmedAt, r.FailureReason = models.PayRefundConfirmed, creditedMicroUSD, &now, ""
	var p models.PayPayment
	if err := tx.First(&p, "id = ?", r.PaymentID).Error; err != nil {
		return false, err
	}
	if err := s.payments.enqueueWebhookWith(tx, &p, models.PayEventRefunded, map[string]any{"refund": payRefundWebhookObject(r)}); err != nil {
		return false, err
	}
	return true, nil
}

// payRefundWebhookObject is payment.refunded's top-level `refund`.
func payRefundWebhookObject(r *models.PayRefund) map[string]any {
	txHash := ""
	if r.TxHash != nil {
		txHash = *r.TxHash
	}
	return map[string]any{"id": r.ID.String(), "amount": FormatMicroUSD(r.AmountMicroUSD), "token": r.TokenSymbol,
		"chainId": r.ChainID, "txHash": txHash, "confirmedAt": r.ConfirmedAt}
}

// payRefundFailedFallback is what a failed refund's notice says when no
// reason was recorded.
const payRefundFailedFallback = "the transfer didn't go through"

// payerFirstName is the payer's first name as the merchant knows them, "" for
// none or a deleted account.
func (s *PayRefundService) payerFirstName(ctx context.Context, p *models.PayPayment) string {
	payer, err := s.payments.Payer(ctx, p)
	if err != nil || payer == nil || payer.Name == payerDeletedName {
		return ""
	}
	if parts := strings.Fields(payer.Name); len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// payRefundNotice is what every notice about one refund is built from.
type payRefundNotice struct {
	s      *PayRefundService
	r      *models.PayRefund
	p      *models.PayPayment
	m      *models.PayMerchant
	who    string // the payer's first name, else "your customer"
	amount string
	token  string
	prefix string // "Test: " in test mode — no money moved
}

// notice reads what a notice about r needs. Called only after the change
// committed; a failed read is logged, never returned (the refund has already
// moved). false: no bus, or the read failed.
func (s *PayRefundService) notice(ctx context.Context, r *models.PayRefund) (*payRefundNotice, bool) {
	if s.events == nil {
		return nil, false
	}
	p, err := s.payments.Get(ctx, r.PaymentID)
	if err != nil {
		log.Printf("[tsa-pay] refund %s notice: %v", r.ID, err)
		return nil, false
	}
	m, err := s.merchants.Get(ctx, r.MerchantID)
	if err != nil {
		log.Printf("[tsa-pay] refund %s notice: %v", r.ID, err)
		return nil, false
	}
	n := &payRefundNotice{s: s, r: r, p: p, m: m, who: s.payerFirstName(ctx, p), amount: FormatMicroUSD(r.AmountMicroUSD), token: r.TokenSymbol}
	if n.who == "" {
		n.who = "your customer"
	}
	if n.token == "" {
		n.token = "USD"
	}
	if r.Mode == models.PayModeTest {
		n.prefix = "Test: "
	}
	return n, true
}

func (n *payRefundNotice) publish(kind string, to uuid.UUID, title, message string) {
	n.s.events.Publish(events.Event{Type: kind, UserID: to, Title: n.prefix + title, Message: n.prefix + message,
		Data: map[string]any{"refundId": n.r.ID.String(), "paymentId": n.r.PaymentID.String()}})
}

// announce tells the people a refund concerns that it was requested,
// confirmed or failed — the owner at every step, the payer when money reached
// her. needs_review is announceReview's.
func (s *PayRefundService) announce(ctx context.Context, r *models.PayRefund, kind string) {
	n, ok := s.notice(ctx, r)
	if !ok {
		return
	}
	owner := n.m.UserID
	switch kind {
	case events.PayRefundRequested:
		n.publish(kind, owner, "Refund requested", "Refund of "+n.amount+" USD to "+n.who+" requested — approve it in TSA Connect")
	case events.PayRefundConfirmed:
		if n.p.PayerUserID != nil {
			n.publish(events.PayRefundReceived, *n.p.PayerUserID, "Refund received", "You received a "+n.amount+" "+n.token+" refund from "+n.m.BusinessName)
		}
		n.publish(kind, owner, "Refund confirmed", "Refund to "+n.who+" confirmed")
	case events.PayRefundFailed:
		why := r.FailureReason
		if why == "" {
			why = payRefundFailedFallback
		}
		n.publish(kind, owner, "Refund failed", "Refund to "+n.who+" failed: "+why+". You can request it again.")
	}
}

// announceReview tells the owner r was parked for support, worded by why.
func (s *PayRefundService) announceReview(ctx context.Context, r *models.PayRefund, review payRefundReview) {
	n, ok := s.notice(ctx, r)
	if !ok {
		return
	}
	const kind, title = events.PayRefundNeedsReview, "Refund needs review"
	switch review {
	case payRefundReviewShort:
		n.publish(kind, n.m.UserID, title, "The refund to "+n.who+" credited less than "+n.amount+" "+n.token+" — support is reviewing it")
	case payRefundReviewReplaced:
		// Replaced (review I4): another transaction from the owner's wallet took
		// its place; whether that one paid the customer is for support to check.
		n.publish(kind, n.m.UserID, title, "The refund to "+n.who+" was replaced by another transaction from your wallet — support is checking whether it paid")
	default:
		n.publish(kind, n.m.UserID, title, "The network showed something unexpected for the refund to "+n.who+" — support is checking it")
	}
}
