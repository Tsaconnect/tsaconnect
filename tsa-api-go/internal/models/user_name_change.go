package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// Name-change statuses. A verified user's name is their ID document's name, so
// changing it means re-verifying: the change is paid for, then runs one or more
// Didit sessions until one passes (applied, or held for an admin when the new
// document name does not look like the same person) or the attempts run out.
const (
	NameChangeStatusPaying        = "paying"          // fee asked for, not yet confirmed
	NameChangeStatusPaid          = "paid"            // fee confirmed, no session started
	NameChangeStatusVerifying     = "verifying"       // a Didit session is open
	NameChangeStatusDeclined      = "declined"        // the last session failed; the user may try again
	NameChangeStatusHeldForReview = "held_for_review" // passed, but the new name needs an admin
	NameChangeStatusApproved      = "approved"        // the new name is on the account
	NameChangeStatusRejected      = "rejected"        // an admin refused the held name
	NameChangeStatusLapsed        = "lapsed"          // attempts or time ran out
	NameChangeStatusCancelled     = "cancelled"       // the user stopped before any attempt, or the fee never arrived
)

// NameChangeTerminalStatuses are the statuses a change never leaves. Every other
// status is "open": a user has at most one open change (a partial unique index,
// built in AutoMigrate, holds that in the database — keep its WHERE list equal
// to this).
var NameChangeTerminalStatuses = []string{
	NameChangeStatusApproved, NameChangeStatusRejected, NameChangeStatusLapsed, NameChangeStatusCancelled,
}

// How the re-verification fee was paid.
const (
	NameChangePayMCGP       = "mcgp_spendable" // an operator debit hold on spendable MCGP
	NameChangePayStablecoin = "stablecoin"     // a USDT/USDC transfer to the treasury
	NameChangePayFree       = "free"           // the fee is configured to zero
)

// Where the fee stands. Recorded on the change so what happened to the user's
// money is readable from the row alone, whatever the change's own status.
const (
	NameChangeFeeNone     = "none"     // nothing taken (unpaid, or free)
	NameChangeFeeHeld     = "held"     // MCGP debited into pendingSettle, not yet resolved
	NameChangeFeeReceived = "received" // stablecoin in the treasury, not yet kept or returned
	NameChangeFeeSettled  = "settled"  // kept: verification was attempted
	NameChangeFeeReversed = "reversed" // MCGP hold returned to the user
	// Stablecoin refunds go through the treasury, whose broadcasts can end
	// uncertain. Sending is claimed before the broadcast, so a crash mid-send
	// is left for a human (refund_sending past the alert window) rather than
	// retried into a double refund; only a refund that provably never left
	// (refund_failed) is retried.
	NameChangeFeeRefundSending   = "refund_sending"
	NameChangeFeeRefunded        = "refunded"
	NameChangeFeeRefundFailed    = "refund_failed"
	NameChangeFeeRefundUncertain = "refund_uncertain"
)

// UserNameChange is one verified user's request to change their name by
// re-verifying their identity, with the fee that paid for it.
//
// Nothing about the user's account changes until a session passes: the account
// stays verified and payouts keep matching the old verified name meanwhile, so a
// failed or abandoned change costs the user their fee at most, never access.
type UserNameChange struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	Status string    `gorm:"type:varchar(24);not null;index" json:"status"`

	// The fee, frozen when the change was created: FeeUSD is what was quoted,
	// PayAmount the exact base units of PayToken it came to on PayChain.
	FeeUSD      string `gorm:"type:varchar(32);not null" json:"feeUsd"`
	PayMethod   string `gorm:"type:varchar(24);not null" json:"payMethod"`
	PayToken    string `gorm:"type:varchar(16)" json:"payToken"`
	PayChain    string `gorm:"type:varchar(32)" json:"payChain"`
	PayAmount   string `gorm:"type:varchar(78)" json:"payAmount"`
	PayDecimals int    `gorm:"not null;default:0" json:"payDecimals"`
	// PayTokenAddress and PayTo pin a stablecoin payment to the contract and the
	// treasury address it was quoted against.
	PayTokenAddress string `gorm:"type:varchar(66)" json:"-"`
	PayTo           string `gorm:"type:varchar(66)" json:"-"`
	// PayFrom is the wallet the user PROVED is theirs (a signed challenge) when
	// the change was opened: the only wallet a stablecoin fee is accepted from,
	// the one an MCGP hold is taken on, and the only one a refund goes to.
	PayFrom string `gorm:"type:varchar(66)" json:"-"`
	// PayRef is the MCGP debit ref, or the stablecoin transaction hash — stored
	// as soon as the user names it, before it is proven, so a payment that
	// lands after the change closed is still found and refunded. Unique, so
	// one transfer can never pay for two changes.
	PayRef *string `gorm:"type:varchar(66);uniqueIndex" json:"-"`
	// PayCredited is what the stablecoin transfer actually credited (base
	// units) — what a refund returns, which is not always the quote.
	PayCredited string `gorm:"type:varchar(78)" json:"-"`
	FeeStatus   string `gorm:"type:varchar(24);not null;default:'none';index" json:"feeStatus"`
	// FeeDecision pins, when the change closed, whether its fee is kept or
	// returned where the status alone cannot say (an unused change lapsing past
	// the user's refund allowance keeps it). Empty: derived from the status.
	FeeDecision string `gorm:"type:varchar(8)" json:"-"`
	// FeeTxHash is the settle / reverse / refund transaction that resolved the fee.
	FeeTxHash string `gorm:"type:varchar(66)" json:"-"`

	Attempts int `gorm:"not null;default:0" json:"attempts"`
	// DiditSessionID/URL are the session in flight (resumed rather than paid for
	// twice); DiditSessionIDs is every session this change ever opened. While a
	// session is being bought DiditSessionID holds a "purchasing:" claim, so two
	// requests can never both pay Didit for the same attempt.
	DiditSessionID  string                      `gorm:"type:varchar(64);index" json:"-"`
	DiditSessionURL string                      `gorm:"type:varchar(512)" json:"-"`
	DiditSessionIDs datatypes.JSONSlice[string] `gorm:"type:jsonb" json:"-"`

	// OldName and OldVerifiedName are the account as it stood when the change
	// was opened; NewVerifiedName is what the new document said.
	OldName         string `gorm:"type:varchar(255)" json:"oldName"`
	OldVerifiedName string `gorm:"type:varchar(255)" json:"-"`
	NewVerifiedName string `gorm:"type:varchar(255)" json:"newVerifiedName,omitempty"`
	MatchStrength   string `gorm:"type:varchar(16)" json:"matchStrength,omitempty"`

	// The same-person evidence, read from the ORIGINAL KYC session (the one
	// the account was first verified with) and the change's session. A name
	// alone is too weak — partial matches could be chained one change at a
	// time — so the date of birth, the document's country and (when the Didit
	// workflow runs face search) a face match against the original session
	// decide whether a change applies itself or waits for an admin. Dates of
	// birth are kept only here, for the admin reviewing the change; document
	// numbers are compared and never stored.
	OriginalDiditSessionID  string   `gorm:"type:varchar(64)" json:"-"`
	OriginalDocName         string   `gorm:"type:varchar(255)" json:"-"`
	OriginalMatchStrength   string   `gorm:"type:varchar(16)" json:"-"`
	OriginalDOB             string   `gorm:"type:varchar(16)" json:"-"`
	NewDOB                  string   `gorm:"type:varchar(16)" json:"-"`
	DOBMatch                *bool    `json:"-"`
	OriginalDocumentCountry string   `gorm:"type:varchar(8)" json:"-"`
	NewDocumentCountry      string   `gorm:"type:varchar(8)" json:"-"`
	DocumentCountryMatch    *bool    `json:"-"`
	DocumentNumberMatch     *bool    `json:"-"`
	FaceMatch               *bool    `json:"-"`
	FaceSimilarity          *float64 `json:"-"`
	// HoldReason is why a passed verification was held for an admin rather
	// than applied (admin-facing).
	HoldReason string `gorm:"type:varchar(512)" json:"-"`

	ReviewedBy *uuid.UUID `gorm:"type:uuid" json:"-"`
	ReviewedAt *time.Time `json:"-"`
	// ReviewNote is internal: an admin's note or the system's own account of
	// what happened. UserMessage is the only text the user is shown.
	ReviewNote  string `gorm:"type:varchar(512)" json:"-"`
	UserMessage string `gorm:"type:varchar(512)" json:"-"`

	ExpiresAt   time.Time  `gorm:"not null;index" json:"expiresAt"`
	CompletedAt *time.Time `gorm:"index" json:"completedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

func (UserNameChange) TableName() string { return "user_name_changes" }

// IsNameChangeTerminal reports whether a status is one a change never leaves.
func IsNameChangeTerminal(status string) bool {
	for _, s := range NameChangeTerminalStatuses {
		if s == status {
			return true
		}
	}
	return false
}
