package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Leg kinds. Phase 2 uses bridge (the LiFi conversion, which delivers straight
// to the user), deliver (a same-token top-up of a small shortfall) and refund.
// pull/deposit/topup are Phase 3's, listed here so the vocabulary is one list.
const (
	RampLegKindPull    = "pull"
	RampLegKindBridge  = "bridge"
	RampLegKindDeposit = "deposit"
	RampLegKindDeliver = "deliver"
	RampLegKindTopUp   = "topup"
	RampLegKindRefund  = "refund"

	RampLegStatusPending   = "pending"
	RampLegStatusConfirmed = "confirmed"
	RampLegStatusFailed    = "failed"
)

func RampLegTerminal(status string) bool {
	return status == RampLegStatusConfirmed || status == RampLegStatusFailed
}

// RampLeg is one on-chain movement belonging to a converted ramp order. Refund
// amounts are computed from these rows, never entered by hand, so every field
// an operator would need to reconstruct the money trail is stored here.
type RampLeg struct {
	ID      uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	OrderID uuid.UUID `gorm:"type:uuid;not null;index:idx_ramp_legs_order_kind_attempt,unique,priority:1" json:"orderId"`
	Kind    string    `gorm:"size:16;not null;index:idx_ramp_legs_order_kind_attempt,unique,priority:2" json:"kind"`
	// Attempt separates a retry from the leg it replaces: the 30-minute retry
	// window re-quotes, and each attempt is its own row with its own hash.
	Attempt int `gorm:"not null;default:1;index:idx_ramp_legs_order_kind_attempt,unique,priority:3" json:"attempt"`

	ChainKey string `gorm:"size:24;not null" json:"chain"`
	Token    string `gorm:"size:16;not null" json:"token"`
	FromAddr string `gorm:"size:128;not null" json:"from"`
	ToAddr   string `gorm:"size:128;not null" json:"to"`
	// Amount is in TOKEN BASE UNITS, as a decimal string — never a
	// human-readable decimal like PaycrestOrderRecord.TokenAmount. WHICH
	// side's base units depends on Kind: for a bridge or refund leg it is
	// the ORIGIN token's base units (what left the treasury, in Token/
	// ChainKey as recorded on this same row); for a deliver leg it is
	// already the DESTINATION token's base units, because that is the one
	// token/chain a deliver leg ever moves on. This distinction is
	// load-bearing for creditedToUser (conversion_orchestrator.go), which
	// only trusts a bridge leg's OWN Amount as destination-denominated once
	// AmountReceived exists — see that function's own comment for why.
	Amount string `gorm:"size:64" json:"amount"`
	// AmountReceived is what actually landed, read back on confirmation —
	// also base units, but NOT always in the same token/chain Amount is
	// denominated in on THIS SAME ROW. For a deliver or refund leg the two
	// agree (both denominated in this row's own Token/ChainKey, since that
	// is the only chain either kind ever moves on). For a BRIDGE leg they
	// deliberately DISAGREE: Amount is what left the treasury on the ORIGIN
	// chain (this row's own Token/ChainKey), but AmountReceived is what the
	// DESTINATION-chain verifier proved landed in the user's wallet —
	// order.RequestedToken on order.RequestedChain, which this row does not
	// itself record. Resolving AmountReceived's decimals for a bridge leg
	// therefore means looking up the ORDER's requested token/chain, never
	// this row's own. No consumer in this codebase does that resolution
	// today — creditedToUser (conversion_orchestrator.go) only sums the raw
	// base-unit integer, never re-derives it from decimals — which is why
	// this asymmetry is currently harmless. It will not stay harmless if a
	// future reader assumes this row's own Token/ChainKey describe both
	// columns.
	AmountReceived string `gorm:"size:64" json:"amountReceived,omitempty"`

	TxHash string `gorm:"size:128;index" json:"txHash,omitempty"`
	// LiFiTool is named explicitly so the column stays predictable for the
	// leg-store's Updates(map[string]any{...}) calls (GORM would otherwise
	// name it "li_fi_tool" from the initialism).
	LiFiTool string `gorm:"size:64;column:lifi_tool" json:"lifiTool,omitempty"`
	Status   string `gorm:"size:16;not null" json:"status"`
	Error    string `gorm:"size:512" json:"error,omitempty"`

	CreatedAt   time.Time  `json:"createdAt"`
	ConfirmedAt *time.Time `json:"confirmedAt,omitempty"`
}

func (l *RampLeg) BeforeCreate(tx *gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}
