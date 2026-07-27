package models

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// evmAddressRe matches a 0x-prefixed 20-byte hex address. Case is not checked
// (EIP-55 checksums are mixed-case by design); the admin UI shows whatever was
// stored so a mistyped address is visible rather than silently normalized.
var evmAddressRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// ValidEVMAddress reports whether addr is a well-formed EVM address.
func ValidEVMAddress(addr string) bool {
	return evmAddressRe.MatchString(strings.TrimSpace(addr))
}

// FeeWalletSetting is the admin-assigned destination for system fees — the
// wallet the PM designates to receive every section's fee share. It is a
// SINGLETON: the read path get-or-creates the one row, seeding Address from
// config.SystemWalletAddress so a fresh database reports the deployed default
// rather than an empty string.
//
// This row is the RECORD of the assignment, not the enforcement point. Fees
// are paid on-chain by each escrow/swap contract's own `systemWallet`, which
// only the contract owner can change (P2PEscrow.setSystemWallet). Assigning a
// new address here tells the platform (and the admin dashboard) where fees are
// meant to land; the contracts must be pointed at it separately, and the
// dashboard flags any mismatch it can read.
type FeeWalletSetting struct {
	ID      uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	Address string    `gorm:"not null" json:"address"`
	// Who last assigned it, for the audit trail the admin panel shows.
	UpdatedBy *uuid.UUID `gorm:"type:uuid" json:"updatedBy,omitempty"`
	// Free-text reason captured on assignment (e.g. "rotated after key change").
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (FeeWalletSetting) TableName() string {
	return "fee_wallet_settings"
}

// BeforeCreate assigns a UUID when one isn't set, so inserts work regardless of
// the DB's default (Postgres gen_random_uuid() vs SQLite in tests / seeds).
func (s *FeeWalletSetting) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}
