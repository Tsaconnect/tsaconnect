package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PayRefund statuses (spec 2026-10-08 §5.2). requested → submitted → confirmed
// is the happy path; a requested refund may be cancelled. A submitted one ends
// failed only when the chain proves it paid nothing, and needs_review when it
// paid part.
const (
	PayRefundRequested   = "requested"
	PayRefundSubmitted   = "submitted"
	PayRefundConfirmed   = "confirmed"
	PayRefundFailed      = "failed"
	PayRefundNeedsReview = "needs_review"
	PayRefundCancelled   = "cancelled"

	// Where the refund was asked for: the web (the owner then approves it in
	// the app, and is told so) or the app itself.
	PayRefundViaWeb = "web"
	PayRefundViaApp = "app"
)

// IsPayRefundStatus reports whether s is one of the refund statuses.
func IsPayRefundStatus(s string) bool {
	switch s {
	case PayRefundRequested, PayRefundSubmitted, PayRefundConfirmed, PayRefundFailed, PayRefundNeedsReview, PayRefundCancelled:
		return true
	}
	return false
}

// PayRefund is a merchant paying a customer back, in USDT or USDC, from the
// merchant owner's own proven TSA wallet: TSA never holds TSA Pay money. Amounts
// are micro-USD (1 USDT = 1 USDC = 1,000,000).
//
// At most one refund per payment is open (requested, submitted or
// needs_review): the partial unique index idx_pay_refunds_one_open. Its WHERE
// uses OR, never IN (…, …) — GORM splits index options on commas. CapMicroUSD
// is the payment's refundable cap as it stood when this refund was asked for.
// TxHash and RawTx are recorded before the transfer is broadcast, so a crash
// after the write still has the hash; the hash is unique so one transfer can
// never settle two refunds. ChainID, TokenSymbol and TokenAddress are derived
// by the server at submit, never taken from the client.
type PayRefund struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	PaymentID        uuid.UUID  `gorm:"type:uuid;not null;index;uniqueIndex:idx_pay_refunds_one_open,where:status = 'requested' OR status = 'submitted' OR status = 'needs_review'" json:"paymentId"`
	MerchantID       uuid.UUID  `gorm:"type:uuid;not null;index:idx_pay_refunds_merchant_mode_created,priority:1;index:idx_pay_refunds_merchant_mode_confirmed,priority:1" json:"merchantId"`
	Mode             string     `gorm:"size:8;not null;index:idx_pay_refunds_merchant_mode_created,priority:2;index:idx_pay_refunds_merchant_mode_confirmed,priority:2" json:"mode"`
	AmountMicroUSD   int64      `gorm:"not null" json:"amountMicroUsd"`
	CapMicroUSD      int64      `gorm:"not null" json:"capMicroUsd"`
	CreditedMicroUSD int64      `gorm:"not null;default:0" json:"creditedMicroUsd"`
	Reason           string     `gorm:"size:255" json:"reason"`
	ToAddress        string     `gorm:"size:128;not null" json:"toAddress"`
	FromAddress      string     `gorm:"size:128" json:"fromAddress"`
	ChainID          int64      `gorm:"not null;default:0" json:"chainId"`
	TokenSymbol      string     `gorm:"size:16" json:"tokenSymbol"`
	TokenAddress     string     `gorm:"size:128" json:"tokenAddress"`
	TxHash           *string    `gorm:"size:128;uniqueIndex" json:"txHash,omitempty"`
	RawTx            string     `gorm:"type:text" json:"-"`
	Status           string     `gorm:"size:16;not null;index" json:"status"`
	FailureReason    string     `gorm:"size:255" json:"failureReason"`
	RequestedVia     string     `gorm:"size:8;not null" json:"requestedVia"`
	CreatedAt        time.Time  `gorm:"index:idx_pay_refunds_merchant_mode_created,priority:3" json:"createdAt"`
	SubmittedAt      *time.Time `json:"submittedAt,omitempty"`
	ConfirmedAt      *time.Time `gorm:"index:idx_pay_refunds_merchant_mode_confirmed,priority:3" json:"confirmedAt,omitempty"`
	// LastBroadcastError is why the network last refused this refund's signed
	// transfer, in words safe to show the owner ("" once a broadcast went
	// through); LastBroadcastAt when that last try was. The refund stays
	// submitted and the confirmer keeps sending it (review I6).
	LastBroadcastError string     `gorm:"size:255" json:"lastBroadcastError"`
	LastBroadcastAt    *time.Time `json:"lastBroadcastAt,omitempty"`
	// CheckedAt is when the confirmer last looked at this refund: it checks the
	// least recently looked-at first, so refunds on a chain it can't read never
	// starve the rest (review I5).
	CheckedAt *time.Time `json:"-"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

func (r *PayRefund) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}
