package models

import (
	"time"

	"github.com/google/uuid"
)

// Bill payment lifecycle. Mirrors the IT sell states because it is the same
// on-chain machine: debit → (off-chain leg) → settle or reverse.
const (
	BillPayStatusDebiting = "debiting" // debit broadcast, not yet confirmed
	BillPayStatusDebited  = "debited"  // MCGP held in pendingSettle; vend in flight
	BillPayStatusSettled  = "settled"  // vend succeeded, backing freed
	BillPayStatusReversed = "reversed" // vend failed, user made whole
	BillPayStatusFailed   = "failed"   // never got as far as a confirmed debit
)

// What an MCGP debit paid for. The lifecycle is the same for every one-off
// payment to the platform from spendable MCGP; the purpose keeps each owner's
// rows (and their on-chain refs) apart.
const (
	BillPayPurposeBill       = "bill"
	BillPayPurposeNameChange = "name_change"
)

// BillPayment is the MCGP leg of a utility-bill purchase.
//
// It deliberately holds ONLY the payment. What was bought — airtime, a meter
// token, a cable package — belongs to the bill order that owns this row, so the
// payment machine stays independent of any one provider. That separation is the
// point: the provider can change without touching money movement.
//
// The lifecycle is the contract's, not ours: debitSpendable moves the user's
// premium into pendingSettle, and exactly one of settleDebit (vend succeeded,
// backing freed) or reverseDebit (vend failed, user restored to the same buckets)
// resolves it. A row stuck in `debited` means the off-chain leg never reported
// back and needs reconciling — it is NOT a lost balance, since the MCGP is still
// on-chain in pendingSettle.
type BillPayment struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;index;not null" json:"userId"`

	// OrderID ties the payment to the bill order that requested it. Unique, so a
	// retried order can never open a second debit.
	OrderID uuid.UUID `gorm:"type:uuid;uniqueIndex;not null" json:"orderId"`
	// Purpose names what OrderID refers to: a bill order, or a name change.
	Purpose string `gorm:"type:varchar(24);not null;default:'bill';index" json:"purpose"`

	WalletAddress string `gorm:"type:varchar(42);not null" json:"walletAddress"`

	// McgpAmount is the debited amount in base units (18dp) as a decimal string.
	// Stored as text: it does not fit an int64 and must not round.
	McgpAmount string `gorm:"type:varchar(78);not null" json:"mcgpAmount"`
	// NgnKobo is what the bill cost in kobo, and RateKobo the ₦-per-MCGP rate the
	// quote used. Both are recorded so a dispute can be reconstructed at the rate
	// actually charged rather than today's.
	NgnKobo  string `gorm:"type:varchar(78);not null" json:"ngnKobo"`
	RateKobo string `gorm:"type:varchar(78);not null" json:"rateKobo"`

	// FromBought is how much of the debit came out of the user's self-exitable
	// balance. Recorded because a reversal must restore the same split, and
	// because support needs to see when a bill consumed bought premium.
	FromBought string `gorm:"type:varchar(78)" json:"fromBought"`

	// DebitRef is the on-chain idempotency key, derived from OrderID. The chain
	// rejects a duplicate, so this is what makes a retry safe.
	DebitRef string `gorm:"type:varchar(66);uniqueIndex;not null" json:"debitRef"`

	Status        string `gorm:"type:varchar(24);index;not null" json:"status"`
	TxHash        string `gorm:"type:varchar(66)" json:"txHash,omitempty"`
	ResolveTxHash string `gorm:"type:varchar(66)" json:"resolveTxHash,omitempty"`
	FailureReason string `gorm:"type:varchar(256)" json:"failureReason,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (BillPayment) TableName() string { return "bill_payments" }
