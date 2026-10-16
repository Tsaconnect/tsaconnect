package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Payer declaration states.
const (
	// PayerDeclarationWaiting — verified, no matching credit yet. The matcher
	// accepts the declared payer when one arrives.
	PayerDeclarationWaiting = "waiting"
	// PayerDeclarationMatched — a credit was bound to the claim on it.
	PayerDeclarationMatched = "matched"
	// PayerDeclarationReview — a credit fits but needs an operator: over the
	// self-resolve cap, several candidates, or a sender bank that could not be
	// compared. ReviewReason says which.
	PayerDeclarationReview = "review"
	// PayerDeclarationClosed — an admin decided the credit it pointed at
	// otherwise (assigned it to another order, refunded or ignored it).
	PayerDeclarationClosed = "closed"
)

// InboundAuditPayerDeclared — a credit was bound on the buyer's own verified
// declaration of the account that paid (no operator involved).
const InboundAuditPayerDeclared = "inbound.payer_declared"

// InboundPayerDeclaration is a buyer's statement of which bank account paid
// for a claim, when it is not an account in their own name.
//
// The central-account statement carries the sender's NAME and bank but never
// their account number (funding rows have none), so the number is used only for
// a name enquiry at the bank: ResolvedName is the bank's own record, and a
// credit is attributed on this declaration only when the typed name, the
// resolved name and the statement's sender name all agree.
//
// One row per claim; a new declaration for the same claim replaces it while it
// is not yet matched.
type InboundPayerDeclaration struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`

	ClaimType string    `gorm:"size:24;not null;uniqueIndex:uq_payer_declaration_claim,priority:1" json:"claimType"`
	ClaimID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:uq_payer_declaration_claim,priority:2" json:"claimId"`
	// AmountKobo is the claim's amount when declared, for the admin view.
	AmountKobo int64 `gorm:"not null" json:"amountKobo"`

	BankCode      string `gorm:"size:16;not null" json:"bankCode"`
	BankName      string `gorm:"size:128;not null" json:"bankName"`
	AccountNumber string `gorm:"size:16;not null" json:"accountNumber"`
	// TypedName is what the buyer entered; ResolvedName is the bank's answer.
	TypedName    string `gorm:"size:128;not null" json:"typedName"`
	ResolvedName string `gorm:"size:128;not null" json:"resolvedName"`

	Status       string     `gorm:"size:16;not null;index" json:"status"`
	ReviewReason string     `gorm:"size:255" json:"reviewReason,omitempty"`
	CreditID     *uuid.UUID `gorm:"type:uuid;index" json:"creditId,omitempty"`
	MatchedAt    *time.Time `json:"matchedAt,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (InboundPayerDeclaration) TableName() string { return "inbound_payer_declarations" }

// BeforeCreate mints the id in Go, like the other inbound tables, so the row
// is identical on Postgres and the SQLite test rigs.
func (d *InboundPayerDeclaration) BeforeCreate(tx *gorm.DB) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	return nil
}
