package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	GasSponsorshipKindAuthorization = "authorization" // relayed EIP-3009 transfer
	GasSponsorshipKindTopUp         = "topup"         // native gas sent to the user
)

// GasSponsorship is one transaction we paid gas for on a user's behalf.
//
// The (order, kind) unique index IS the once-per-order rule: the row is
// inserted BEFORE broadcasting, so two concurrent requests cannot both send.
// A row with an empty TxHash is a send in flight (or one that crashed before
// recording its hash); Release deletes it when the failure was ours.
type GasSponsorship struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	OrderID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_gas_sponsorship_order_kind" json:"orderId"`
	Kind      string    `gorm:"size:16;not null;uniqueIndex:idx_gas_sponsorship_order_kind" json:"kind"`
	ChainKey  string    `gorm:"size:24;not null" json:"chain"`
	Token     string    `gorm:"size:16;not null" json:"token"`
	Wallet    string    `gorm:"size:128;not null" json:"wallet"`
	AmountWei string    `gorm:"size:40" json:"amountWei,omitempty"`
	TxHash    string    `gorm:"size:128;index" json:"txHash,omitempty"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (g *GasSponsorship) BeforeCreate(tx *gorm.DB) error {
	if g.ID == uuid.Nil {
		g.ID = uuid.New()
	}
	return nil
}
