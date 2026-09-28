package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// WalletLinkChallenge is a single-use nonce a user signs (EIP-191
// personal_sign) with a wallet's key to prove they own it before the wallet is
// linked to their account. The backend operator spends Spendable MCGP keyed by
// users.wallet_address with no user signature, so linking an address the
// caller cannot sign for is linking someone else's money.
//
// A row with UsedAt set is also the durable record that UserID proved
// Address: switching back to a wallet already proven needs no new signature,
// and operator-debit paths check for one before spending.
type WalletLinkChallenge struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index:idx_wallet_link_challenges_user_address" json:"-"`
	// Address is stored lower-case so proof lookups need no LOWER().
	Address   string     `gorm:"size:42;not null;index:idx_wallet_link_challenges_user_address" json:"-"`
	Nonce     string     `gorm:"size:64;not null;uniqueIndex" json:"-"`
	Message   string     `gorm:"type:text;not null" json:"-"`
	ExpiresAt time.Time  `gorm:"not null" json:"-"`
	UsedAt    *time.Time `json:"-"`
	CreatedAt time.Time  `json:"-"`
}

func (c *WalletLinkChallenge) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}
