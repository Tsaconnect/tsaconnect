package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// P2PSettlementForward lifecycle. Read it as an answer to one question: where
// is the merchant's money right now?
//
//	pending   the ledger says it is theirs; nothing has been sent.
//	submitted the ledger STILL says it is theirs, and a transfer is in flight.
//	settled   the transfer landed, so the ledger entry was spent to pay for it.
//	failed    the transfer will never land; the ledger entry stands.
//
// Only `settled` ever debits available_ngn. That is the whole design: every
// other state leaves the merchant holding the ledger entry, so no state in this
// table can produce a merchant with neither the money nor the claim on it.
const (
	// P2PForwardPending — available_ngn credited, transfer not yet submitted.
	// Re-runnable: nothing has been sent, so a retry cannot double-pay.
	P2PForwardPending = "pending"
	// P2PForwardSubmitted — the provider accepted the transfer and gave us an
	// id. Deliberately NOT debited: acceptance is not disbursement.
	P2PForwardSubmitted = "submitted"
	// P2PForwardSettled — disbursement confirmed; available_ngn debited.
	P2PForwardSettled = "settled"
	// P2PForwardFailed — rejected at submission, or reported failed later. The
	// balance stays credited and an operator has to decide what happens next.
	P2PForwardFailed = "failed"
)

// P2PSettlementForward records one attempt to sweep a settled P2P trade's naira
// from the central account to the merchant's own settlement bank (decision 7 of
// the central-account inbound design: forward instantly, per trade).
//
// It exists for two reasons that a log line cannot serve:
//
//  1. ProviderRef is the idempotency claim. It is the NIBSS session id of the
//     credit that settled the trade, and the unique index on it is what stops a
//     replayed credit from crediting the merchant's ledger a second time. The
//     row is inserted BEFORE the ledger moves, so the claim is durable even if
//     the process dies mid-settle.
//
//  2. A transfer that is rejected at submission produces no provider-side
//     record and therefore no webhook — the exact hole that strands value on
//     the MCGP sell leg (it_service.go, ~line 675). Without a row of our own
//     there is nothing to alert on and nothing for an operator to find.
type P2PSettlementForward struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`

	// TradeID is unique: one trade settles once, so it forwards once.
	TradeID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"tradeId"`
	PMID    uuid.UUID `gorm:"type:uuid;not null;index" json:"pmId"`

	// ProviderRef is the inbound credit's NIBSS session id. Unique, and the
	// reason Accept is idempotent — see the type comment.
	ProviderRef string `gorm:"size:64;not null;uniqueIndex" json:"providerRef"`

	Currency string `gorm:"size:8;not null" json:"currency"`
	// Amount is the merchant's proceeds as a 2dp decimal string, matching the
	// ledger columns it is credited to and debited from.
	Amount string `gorm:"type:numeric(38,2);not null" json:"amount"`

	Status string `gorm:"size:16;not null;default:'pending';index" json:"status"`

	// Reference is what we send the provider and what comes back on the
	// disburse webhook. It is how a confirmation finds this row.
	Reference string `gorm:"size:64;not null;uniqueIndex" json:"reference"`
	PayoutID  string `gorm:"size:64;index" json:"payoutId,omitempty"`

	// Attempts counts how many times a submission has been held back rather
	// than sent — an unreadable payout balance, a short one, an absent gateway.
	// A held forward is retryable by design, but retrying without a ceiling is
	// its own silence: a permanently unpayable forward would be re-driven every
	// five minutes forever and still never reach an operator, because only
	// `failed` rows are queued. The count is what turns "retry" into "escalate".
	Attempts int `gorm:"not null;default:0" json:"attempts"`

	// FailureReason is operator-facing text, not a code. Whoever reads it is
	// deciding whether to re-fire the transfer or pay the merchant by hand.
	// Sized to match ITIntent.FailureReason so the shared truncateReason helper
	// cannot overflow it.
	FailureReason string `gorm:"size:256" json:"failureReason,omitempty"`

	// AlertedAt records that the admin alert for the CURRENT problem has
	// already been raised, so a re-run does not alert on every tick.
	AlertedAt   *time.Time `json:"alertedAt,omitempty"`
	SubmittedAt *time.Time `json:"submittedAt,omitempty"`
	SettledAt   *time.Time `json:"settledAt,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (P2PSettlementForward) TableName() string {
	// GORM pluralises p2p_* to p2_p_*. Every P2P model pins its own name.
	return "p2p_settlement_forwards"
}

// BeforeCreate mints the id in Go. Same reason as InboundCredit: the SQLite
// test rigs have no gen_random_uuid(), and a row that inserts in production but
// not under test is worse than no row at all.
func (f *P2PSettlementForward) BeforeCreate(tx *gorm.DB) error {
	if f.ID == uuid.Nil {
		f.ID = uuid.New()
	}
	return nil
}

// NeedsAttention reports whether this forward is money an operator has to look
// at: the merchant is credited but nothing reached their bank.
//
// `pending` is deliberately NOT included here. Whether a pending forward is an
// operator's problem depends on how long it has been pending, which this value
// alone cannot say — that judgement lives in
// P2PSettlementForwarder.ListNeedingAttention, which has the clock.
func (f *P2PSettlementForward) NeedsAttention() bool {
	return f != nil && f.Status == P2PForwardFailed
}
