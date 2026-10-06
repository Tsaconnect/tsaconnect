package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Operator actions on an IT stablecoin buy. Persisted; stable once written.
const (
	ITAuditStableBuyRefunded        = "it.stable_buy.refunded"
	ITAuditStableBuyDeliveredAnyway = "it.stable_buy.delivered_anyway"
	ITAuditStableBuyResent          = "it.stable_buy.resent"
	// An operator attached Flutterwave's on-chain hash to a converted buy it
	// delivered without one (route 2, spec §15.5 N5).
	ITAuditConversionHashAttached = "it.stable_buy.conversion_hash_attached"
)

// ITIntentAuditLog is the append-only record of operator actions on a parked
// or stalled stablecoin buy (spec 2026-10-03 §5.2/§5.4) — modelled on
// InboundCreditAuditLog, for the same reason: the intent row carries only the
// current state, and "which admin, on what evidence" is what a dispute turns on.
type ITIntentAuditLog struct {
	ID       uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	IntentID uuid.UUID      `gorm:"type:uuid;not null;index:idx_it_intent_audit_intent_created,priority:1" json:"intentId"`
	ActorID  *uuid.UUID     `gorm:"type:uuid;index" json:"actorId,omitempty"`
	Action   string         `gorm:"size:48;not null;index" json:"action"`
	Note     string         `gorm:"size:512" json:"note,omitempty"`
	Metadata datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"`

	CreatedAt time.Time `gorm:"index;index:idx_it_intent_audit_intent_created,priority:2" json:"createdAt"`
}

func (ITIntentAuditLog) TableName() string { return "it_intent_audit_logs" }

// BeforeCreate mints the id in Go: gen_random_uuid() exists only on PostgreSQL.
func (l *ITIntentAuditLog) BeforeCreate(*gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}
