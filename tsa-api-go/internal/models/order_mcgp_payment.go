package models

import (
	"time"

	"github.com/google/uuid"
)

// Lifecycle of the Spendable MCGP that funds a marketplace order.
//
// The on-chain machine is the entitlement contract's operator debit, the same
// one bills use: debitSpendable moves the buyer's premium into pendingSettle,
// and exactly one of settleDebit or reverseDebit resolves it. The pending debit
// IS the escrow hold — nothing moves to the seller until the order resolves.
//
//	debiting ─► held ─► releasing ─► released   (seller paid: settle + credit)
//	    │         └───► reversing ─► reversed   (buyer refunded: reverse)
//	    └─► failed                              (debit never landed; retryable)
//
// `releasing` and `reversing` are claimed in the same database transaction
// that records the order's outcome, before any chain call, so a crash between
// a chain call and the next write only ever leaves a row that the reconciler
// drives forward from chain state. Nothing is decided twice.
const (
	OrderMcgpDebiting  = "debiting"  // debit written, broadcast outcome not yet confirmed
	OrderMcgpHeld      = "held"      // debit Pending on-chain: the buyer's MCGP is held
	OrderMcgpReleasing = "releasing" // release decided; settle + credits in progress
	OrderMcgpReleased  = "released"  // debit settled and every credit confirmed
	OrderMcgpReversing = "reversing" // refund decided; reverse in progress
	OrderMcgpReversed  = "reversed"  // debit reversed: buyer restored bucket for bucket
	OrderMcgpFailed    = "failed"    // debit never landed; the order may be paid again
)

// Why a release happened, recorded for support and the audit trail.
const (
	OrderMcgpReleaseBuyerConfirmed = "buyer_confirmed"
	OrderMcgpReleaseAdminResolved  = "admin_resolved"
	OrderMcgpReleaseEscrowExpired  = "escrow_expired"
)

// OrderMcgpPayment is the Spendable MCGP leg of one marketplace order. It holds
// only money movement; what was bought belongs to the order.
type OrderMcgpPayment struct {
	ID      uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	OrderID uuid.UUID `gorm:"type:uuid;uniqueIndex;not null" json:"orderId"`
	BuyerID uuid.UUID `gorm:"type:uuid;index;not null" json:"buyerId"`

	// BuyerWallet is pinned at the first attempt: the debit ref is per order,
	// so every retry, reverse and late-debit check must use this same wallet
	// even if the account's wallet address changes afterwards.
	BuyerWallet string `gorm:"type:varchar(42);not null" json:"buyerWallet"`
	// McgpAmount is the debited amount in base units (18dp), as text: it does
	// not fit an int64 and must not round.
	McgpAmount string `gorm:"type:varchar(78);not null" json:"mcgpAmount"`
	// FromBought is how much of the debit came out of the buyer's self-exitable
	// (USDC-bought) bucket. A reversal restores it there; a release does not —
	// the seller is credited spend-only premium.
	FromBought string `gorm:"type:varchar(78)" json:"fromBought,omitempty"`
	// DebitRef is the on-chain idempotency key, derived from OrderID.
	DebitRef string `gorm:"type:varchar(66);uniqueIndex;not null" json:"debitRef"`

	Status      string `gorm:"type:varchar(16);index;not null" json:"status"`
	DebitTxHash string `gorm:"type:varchar(66)" json:"debitTxHash,omitempty"`
	// ResolveTxHash is the settleDebit or reverseDebit of the order's debit.
	ResolveTxHash string `gorm:"type:varchar(66)" json:"resolveTxHash,omitempty"`

	// The release plan, fixed when the release is claimed so a retry pays
	// exactly what was decided. SellerAmount + BuyerAmount never exceed
	// McgpAmount; any remainder stays in the contract as platform excess.
	ReleaseReason      string `gorm:"type:varchar(24)" json:"releaseReason,omitempty"`
	SellerWallet       string `gorm:"type:varchar(42)" json:"sellerWallet,omitempty"`
	SellerAmount       string `gorm:"type:varchar(78)" json:"sellerAmount,omitempty"`
	SellerCreditTxHash string `gorm:"type:varchar(66)" json:"sellerCreditTxHash,omitempty"`
	// BuyerAmount is the share a split leaves with the buyer. It is never
	// credited (that would turn self-exitable MCGP into spend-only): the whole
	// hold is reversed, and the non-buyer share is collected back with a
	// second debit (CollectTxHash), settled (CollectSettleTxHash).
	BuyerAmount         string `gorm:"type:varchar(78)" json:"buyerAmount,omitempty"`
	CollectTxHash       string `gorm:"type:varchar(66)" json:"collectTxHash,omitempty"`
	CollectSettleTxHash string `gorm:"type:varchar(66)" json:"collectSettleTxHash,omitempty"`

	// DecidedAt is when the release or refund was claimed; stuck alerts
	// measure from it.
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
	// DebitedAt is when the latest debit attempt was sent. Checkout's share
	// of the contract's debit window is measured from it (rows written before
	// the column existed fall back to CreatedAt).
	DebitedAt *time.Time `gorm:"index" json:"debitedAt,omitempty"`

	FailureReason string `gorm:"type:varchar(256)" json:"failureReason,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (OrderMcgpPayment) TableName() string { return "order_mcgp_payments" }

// IsSplit reports a release that leaves part of the hold with the buyer.
func (p *OrderMcgpPayment) IsSplit() bool {
	return p.BuyerAmount != "" && p.BuyerAmount != "0"
}
