package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Inbound credit lifecycle (central-account attribution design §4).
//
// A row only ever moves forward, and only `unmatched` has an automatic exit:
// `held` is terminal until a human acts (decision 9 — no auto-release, because
// an unattributed credit that silently resolves itself is exactly how money
// reaches the wrong user).
const (
	// InboundCreditUnmatched — ingested; the matcher has not reached a decision.
	InboundCreditUnmatched = "unmatched"
	// InboundCreditMatched — bound to a claim and the product settle path ran.
	InboundCreditMatched = "matched"
	// InboundCreditHeld — the matcher declined to bind. Needs an operator.
	InboundCreditHeld = "held"
	// InboundCreditRefunded — an operator returned it to the sender.
	InboundCreditRefunded = "refunded"
	// InboundCreditIgnored — not customer money (own-float top-up, test credit).
	InboundCreditIgnored = "ignored"
	// InboundCreditBinding — claimed for a bind on a buyer's payer declaration,
	// whose product settle is running. Nothing else may act on the row: an
	// admin refund or assign racing the settle would pay the same naira twice.
	// MatchedClaim* already name the claim, so a bind interrupted mid-way is
	// finished (the settle is idempotent), never handed back.
	InboundCreditBinding = "binding"

	InboundCreditProviderFlutterwave = "flutterwave"

	// Match strengths, as justified by NameMatcher.
	InboundMatchStrengthExact   = "exact"
	InboundMatchStrengthPartial = "partial"
)

// Hold reason codes — why the matcher declined to bind, said without naming
// anybody.
//
// These exist because the prose in HoldReason embeds the sender's bank account
// name, the raw statement remark and the Didit-verified legal names of every
// candidate user. That string is fine in the admin console, behind auth, where
// an operator needs exactly those details to resolve the row. It is not fine
// in the application log or in an alert email to a mailbox nobody here
// controls, and this repo's policy is that verified names stay out of logs.
//
// So the two are kept apart: the code travels to the log and the alert, the
// prose stays for the console. Anything rendering a hold outside the console
// renders the code and nothing else.
//
// They are values an operator reads cold, with no legend to hand, which is why
// they are sentences-in-a-word rather than abbreviations. They are persisted,
// so they must not be renamed once any row carries them.
const (
	// InboundHoldNoClaimAtAmount — money arrived that nobody is waiting for.
	InboundHoldNoClaimAtAmount = "no_open_claim_at_this_amount"
	// InboundHoldNoSenderName — the statement carried no name to check.
	InboundHoldNoSenderName = "statement_carried_no_sender_name"
	// InboundHoldUnverifiableRival — a rival claim's owner has no Didit name,
	// so it can be neither matched nor ruled out.
	InboundHoldUnverifiableRival = "rival_claim_owner_has_no_verified_name"
	// InboundHoldNoNameMatch — names were on file and the sender is none of them.
	InboundHoldNoNameMatch = "sender_matches_no_candidate_name"
	// InboundHoldContested — several claims match on both amount and name.
	InboundHoldContested = "several_claims_match_equally_well"
	// InboundHoldNonPositiveAmount — the row parsed to zero or less, so there
	// is no amount to match on.
	InboundHoldNonPositiveAmount = "credit_amount_is_not_positive"
	// InboundHoldCurrencyMismatch — a claim source answered with a claim in
	// the wrong currency. Not an operator's mistake to fix: a bug in us.
	InboundHoldCurrencyMismatch = "claim_currency_does_not_match_credit"
	// InboundHoldMerchantNotActive — the credit is attributed to a P2P trade
	// whose payment merchant is paused (or otherwise not live), so it is not
	// settled automatically.
	InboundHoldMerchantNotActive = "merchant_is_not_active"
	// InboundHoldOutsideMerchantCap — the trade is over the merchant's
	// per-trade or daily cap, or cannot be valued to show it is within them.
	InboundHoldOutsideMerchantCap = "trade_not_within_merchant_cap"
	// InboundHoldPayoutBelowMinimum — the merchant's proceeds are under the
	// provider's minimum transfer, so settling would release the crypto and
	// then fail the bank transfer.
	InboundHoldPayoutBelowMinimum = "payout_below_transfer_minimum"
)

// InboundCredit is an immutable record of one credit into the central account.
// Rows are never deleted; AmountKobo and ProviderRef are never updated.
//
// The table exists to separate "money seen" from "money attributed": the
// ingestor writes here and stops, so no matching bug can lose the fact that
// money arrived. Everything downstream is re-runnable against rows that are
// already durable.
type InboundCredit struct {
	ID       uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Provider string    `gorm:"size:16;not null" json:"provider"` // "flutterwave"

	// ProviderRef is the NIBSS NIP session id (30 digits) from the statement
	// row's `reference`. Unique per credit and the ONLY viable dedupe key —
	// funding rows carry no account number and no transfer sub-object.
	ProviderRef string `gorm:"size:64;not null;uniqueIndex" json:"providerRef"`

	Currency string `gorm:"size:8;not null;index" json:"currency"`

	// AmountKobo is the credit as it appears on the statement row.
	AmountKobo int64 `gorm:"not null" json:"amountKobo"`
	// FeeKobo is the paired fee row when the provider nets its charge, else 0.
	FeeKobo int64 `gorm:"not null;default:0" json:"feeKobo"`
	// GrossKobo = AmountKobo + FeeKobo. This is what claims match against, so
	// matching is correct whether the provider nets its fee or bills separately.
	GrossKobo int64 `gorm:"not null;index" json:"grossKobo"`

	// SenderRaw is the statement's free-text remark, stored verbatim for audit:
	//   "Received Money from GTBANK|ADEMU EMMANUEL O."
	SenderRaw  string `gorm:"size:255;not null" json:"senderRaw"`
	SenderBank string `gorm:"size:64" json:"senderBank,omitempty"`  // parsed left of "|"
	SenderName string `gorm:"size:128" json:"senderName,omitempty"` // parsed right of "|"

	TransactionDate time.Time `gorm:"not null;index" json:"transactionDate"`
	SeenAt          time.Time `gorm:"not null" json:"seenAt"`

	// Status: unmatched | matched | held | refunded | ignored
	Status string `gorm:"size:16;not null;default:'unmatched';index" json:"status"`
	// HoldReason is the operator-facing prose: which claims contended, whose
	// names were compared, what the sender line said. It names real people, so
	// it is for the admin console only — never a log line, never an alert.
	HoldReason string `gorm:"size:255" json:"holdReason,omitempty"`
	// HoldReasonCode is the same decision with nobody's name in it, safe to
	// log and to email. Set together with HoldReason on every hold; both are
	// cleared when the credit is finally bound.
	HoldReasonCode string `gorm:"size:48;index" json:"holdReasonCode,omitempty"`

	// HeldClaimIDs lists the claims ("type:id", comma-separated) that asked
	// this amount when the matcher held it for want of a name match — who the
	// money could have been for at the time. A payer declaration may settle the
	// credit only when its own claim was the sole one.
	HeldClaimIDs string `gorm:"size:1024" json:"heldClaimIds,omitempty"`

	MatchedClaimType string     `gorm:"size:24" json:"matchedClaimType,omitempty"`
	MatchedClaimID   *uuid.UUID `gorm:"type:uuid;index" json:"matchedClaimId,omitempty"`
	MatchedUserID    *uuid.UUID `gorm:"type:uuid;index" json:"matchedUserId,omitempty"`
	MatchedAt        *time.Time `json:"matchedAt,omitempty"`
	// MatchStrength records how the bind was justified: "exact" | "partial".
	MatchStrength string `gorm:"size:16" json:"matchStrength,omitempty"`
	// ResolvedBy is the admin who assigned a held credit by hand, if any.
	ResolvedBy *uuid.UUID `gorm:"type:uuid" json:"resolvedBy,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// BeforeCreate mints the id in Go rather than leaning on the column default.
// The default only exists on PostgreSQL (pgcrypto); the SQLite test rigs have
// no gen_random_uuid(), and an ingestor that inserts fine in production but
// not under test is worse than useless.
func (c *InboundCredit) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}
