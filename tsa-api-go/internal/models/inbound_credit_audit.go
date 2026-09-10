package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Operator actions on a held credit. These strings are persisted and are what
// an incident review greps for, so they are stable once any row carries them.
const (
	// InboundAuditAssigned — an operator attributed the credit to a claim by
	// hand, after the matcher declined to.
	InboundAuditAssigned = "inbound.assigned"
	// InboundAuditRefunded — an operator sent the money back to the sender.
	InboundAuditRefunded = "inbound.refunded"
	// InboundAuditIgnored — an operator ruled it was not customer money.
	InboundAuditIgnored = "inbound.ignored"
)

// InboundCreditAuditLog is the append-only record of every operator action on
// a credit into the central account.
//
// inbound_credits carries only the CURRENT state: `refunded` says the money
// went back, not who decided that, when, or what they were looking at when
// they decided. Under central-account attribution a held credit is real
// customer money whose only exit is a human, which makes "which admin, on what
// evidence" the thing a later dispute turns on — and it must survive the credit
// row being written over. Modelled on p2p_audit_logs, which exists for the same
// reason over the same class of action.
//
// Metadata is JSON because each action has its own shape: an assignment stores
// the claim it was bound to, a refund or ignore stores the status it moved
// from. Free-form beats a column per action for something written once and read
// during incidents.
type InboundCreditAuditLog struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`

	CreditID uuid.UUID `gorm:"type:uuid;not null;index:idx_inbound_audit_credit_created,priority:1" json:"creditId"`

	// ActorID is nullable so the table can also carry entries with no human
	// behind them if the matcher ever writes here; every row this package
	// writes today has one, because operator attribution is the whole point.
	ActorID *uuid.UUID `gorm:"type:uuid;index" json:"actorId,omitempty"`

	Action string `gorm:"size:32;not null;index" json:"action"`

	// Note is the operator's own words on why. Optional, but it is the only
	// field that can explain a decision the metadata cannot.
	Note string `gorm:"size:512" json:"note,omitempty"`

	Metadata datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"`

	CreatedAt time.Time `gorm:"index;index:idx_inbound_audit_credit_created,priority:2" json:"createdAt"`
}

// TableName overrides the default table name.
func (InboundCreditAuditLog) TableName() string {
	return "inbound_credit_audit_logs"
}

// BeforeCreate mints the id in Go rather than leaning on the column default,
// for the same reason models.InboundCredit does: gen_random_uuid() exists only
// on PostgreSQL, and an audit row that writes in production but not under test
// is an audit trail nothing proves.
func (l *InboundCreditAuditLog) BeforeCreate(tx *gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}
