package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// P2PDispute is the off-chain audit row for a disputed trade. Exactly one
// row per trade (enforced by a plain unique index on TradeID; an unconfirmed open row is rewritten by a retry, not duplicated).
// The on-chain dispute state lives in P2PEscrow.Trade.status; this record
// captures the WHY (raised reason, raised role) and the resolution decision
// the admin made (refundedToBuyer, free-form note).
//
// Lifecycle: open → (resolved | rejected). Rejected means the admin found
// the dispute baseless and chose not to override — the trade returns to its
// pre-dispute state via the contract's adminResolve(false) path.
type P2PDispute struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TradeID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"tradeId"`
	RaisedBy   uuid.UUID `gorm:"type:uuid;not null" json:"raisedBy"`
	RaisedRole string    `gorm:"size:8;not null" json:"raisedRole"`
	Reason     string    `gorm:"type:text;not null" json:"reason"`
	// EvidenceURLs is a JSON-encoded []string of Cloudinary image URLs submitted
	// with the dispute. Stored as text; decoded into Evidence on load (AfterFind).
	EvidenceURLs    string     `gorm:"type:text" json:"-"`
	Evidence        []string   `gorm:"-" json:"evidenceUrls,omitempty"`
	Status          string     `gorm:"size:16;not null;default:'open';index" json:"status"`
	ResolvedBy      *uuid.UUID `gorm:"type:uuid" json:"resolvedBy,omitempty"`
	Resolution      string     `gorm:"type:text" json:"resolution,omitempty"`
	RefundedToBuyer *bool      `json:"refundedToBuyer,omitempty"`
	ResolveTxHash   string     `gorm:"size:66" json:"resolveTxHash,omitempty"`
	// CreatedAt is "last prepared" while the row is unconfirmed: each replacing
	// retry resets it, and it gates the counter-party's right to replace.
	CreatedAt time.Time `json:"createdAt"`
	// PrepareNonce is new on every create and replace of an unconfirmed row;
	// a replace is conditional on the nonce it read, so two retries that read
	// the same row cannot both rewrite it. Nullable: rows prepared before the
	// column existed have none. Internal, never serialised.
	PrepareNonce *uuid.UUID `gorm:"type:uuid" json:"-"`
	ResolvedAt   *time.Time `json:"resolvedAt,omitempty"`
}

func (P2PDispute) TableName() string {
	return "p2p_disputes"
}

// AfterFind decodes the stored EvidenceURLs JSON into the transient Evidence
// slice so API responses expose `evidenceUrls` as an array.
func (d *P2PDispute) AfterFind(*gorm.DB) error {
	if d.EvidenceURLs != "" {
		_ = json.Unmarshal([]byte(d.EvidenceURLs), &d.Evidence)
	}
	return nil
}

const (
	P2PDisputeRoleBuyer  = "buyer"
	P2PDisputeRoleSeller = "seller"

	P2PDisputeStatusOpen     = "open"
	P2PDisputeStatusResolved = "resolved"
	P2PDisputeStatusRejected = "rejected"
)
