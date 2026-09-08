package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Dispute subject types. The value is stored, so these strings are a wire
// contract with the app and the admin console — do not rename them.
const (
	DisputeSubjectPaycrestOrder = "paycrest_order"
	DisputeSubjectITIntent      = "it_intent"
	DisputeSubjectBillOrder     = "bill_order"
)

// Dispute lifecycle.
//
// escalated sits between open and closed because our disputes are frequently
// not ours to settle: a stranded Paycrest deposit is recovered by emailing
// their support, and until they answer the dispute is neither untouched nor
// resolved. Without the state those two look identical in the queue, and an
// operator re-does work someone already started.
const (
	DisputeStatusOpen      = "open"
	DisputeStatusEscalated = "escalated"
	DisputeStatusResolved  = "resolved"
	DisputeStatusRejected  = "rejected"
)

// ValidDisputeResolution reports whether s is a status an admin may CLOSE a
// dispute with. open and escalated are excluded: neither closes anything.
func ValidDisputeResolution(s string) bool {
	return s == DisputeStatusResolved || s == DisputeStatusRejected
}

// DisputeOpen reports whether a dispute is still live. Escalated counts as
// open — the user is still waiting on an answer.
func DisputeOpen(status string) bool {
	return status == DisputeStatusOpen || status == DisputeStatusEscalated
}

// Dispute is a user's complaint about one order, whatever kind of order that
// is, reviewed and closed out by an operator.
//
// It is polymorphic rather than one table per order type because the queue's
// only query is "what is still open, newest first" — a shape that does not
// change when a new kind of order gains disputes. The per-type knowledge lives
// in exactly one place instead (the subject resolvers in the service), which is
// what keeps this from becoming a fourth near-identical reports table after
// bill_reports, p2p_disputes and the marketplace's dispute flow.
//
// One dispute per subject, enforced by the unique index rather than a
// pre-check: two taps race, and only the database can settle that.
type Dispute struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`

	SubjectType string    `gorm:"size:24;not null;uniqueIndex:idx_disputes_subject" json:"subjectType"`
	SubjectID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_disputes_subject" json:"subjectId"`
	UserID      uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`

	Reason string `gorm:"type:text;not null" json:"reason"`
	// EvidenceURLs is a JSON-encoded []string of uploaded image URLs, decoded
	// into Evidence on load. Same storage shape as P2PDispute, so the admin
	// console renders both with one component.
	EvidenceURLs string   `gorm:"type:text" json:"-"`
	Evidence     []string `gorm:"-" json:"evidenceUrls,omitempty"`

	Status string `gorm:"size:16;not null;default:'open';index" json:"status"`

	// ExternalRef is the third party's handle on the escalation — a Paycrest
	// support thread, a Flutterwave ticket. Free text because every provider
	// numbers things differently, and an operator needs to find it again more
	// than we need to parse it.
	ExternalRef string     `gorm:"size:128" json:"externalRef,omitempty"`
	EscalatedBy *uuid.UUID `gorm:"type:uuid" json:"escalatedBy,omitempty"`
	EscalatedAt *time.Time `json:"escalatedAt,omitempty"`
	// EscalationNote is why it went out and to whom, for the next operator.
	EscalationNote string `gorm:"type:text" json:"escalationNote,omitempty"`

	ResolvedBy *uuid.UUID `gorm:"type:uuid" json:"resolvedBy,omitempty"`
	Resolution string     `gorm:"type:text" json:"resolution,omitempty"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (Dispute) TableName() string { return "disputes" }

// App-side UUID, not gen_random_uuid(), so the SQLite test rig works — same
// reason as BillReport.
func (d *Dispute) BeforeCreate(tx *gorm.DB) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	if d.Status == "" {
		d.Status = DisputeStatusOpen
	}
	return nil
}

// AfterFind decodes the stored evidence JSON so responses expose an array.
func (d *Dispute) AfterFind(*gorm.DB) error {
	if d.EvidenceURLs != "" {
		_ = json.Unmarshal([]byte(d.EvidenceURLs), &d.Evidence)
	}
	return nil
}

// SetEvidence stores urls, keeping the encoded and decoded views in step.
func (d *Dispute) SetEvidence(urls []string) error {
	if len(urls) == 0 {
		d.EvidenceURLs = ""
		d.Evidence = nil
		return nil
	}
	raw, err := json.Marshal(urls)
	if err != nil {
		return err
	}
	d.EvidenceURLs = string(raw)
	d.Evidence = urls
	return nil
}
