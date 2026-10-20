package models

import (
	"time"

	"github.com/google/uuid"
)

// FiatLPDeposit is USDT a fiat LP sent to the company desk wallet, locked in
// naira at the rate when the transfer was confirmed. The LP is owed Fiat by
// the company; Units (chain units of the pool token) is what that USDT was.
type FiatLPDeposit struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
	Wallet    string    `gorm:"size:42;not null" json:"wallet"` // the LP's wallet the USDT came from
	TxHash    string    `gorm:"size:66;not null;uniqueIndex" json:"txHash"`
	ChainID   int64     `gorm:"not null" json:"chainId"`
	Units     string    `gorm:"type:numeric(78,0);not null" json:"units"`
	Currency  string    `gorm:"size:8;not null" json:"currency"`
	Rate      string    `gorm:"type:numeric(30,8);not null" json:"rate"`
	Fiat      string    `gorm:"type:numeric(30,2);not null" json:"fiat"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

func (FiatLPDeposit) TableName() string { return "fiat_lp_deposits" }

// Fiat LP withdrawal kinds and statuses.
const (
	FiatLPKindPrincipal = "principal" // naira principal, paid as USDT at the day's rate
	FiatLPKindEarnings  = "earnings"  // accrued pool earnings, paid as USDT

	FiatLPRequested = "requested"
	FiatLPPaid      = "paid"
	FiatLPRejected  = "rejected"
)

// FiatLPWithdrawal is a fiat LP's exit request. A principal one reserves Fiat
// (naira) and UnitsReleased (the share of the LP's USDT it takes out of the
// earning weight) the moment it is requested; an earnings one reserves Units
// (USDT). A rejected one reserves nothing.
type FiatLPWithdrawal struct {
	ID            uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	UserID        uuid.UUID  `gorm:"type:uuid;not null;index" json:"userId"`
	Wallet        string     `gorm:"size:42;not null" json:"wallet"` // paid here, nowhere else
	Kind          string     `gorm:"size:16;not null" json:"kind"`
	Status        string     `gorm:"size:16;not null;index" json:"status"`
	Fiat          *string    `gorm:"type:numeric(30,2)" json:"fiat,omitempty"`
	UnitsReleased *string    `gorm:"type:numeric(78,0)" json:"unitsReleased,omitempty"`
	Units         *string    `gorm:"type:numeric(78,0)" json:"units,omitempty"` // earnings amount
	PaidUnits     *string    `gorm:"type:numeric(78,0)" json:"paidUnits,omitempty"`
	PayRate       *string    `gorm:"type:numeric(30,8)" json:"payRate,omitempty"`
	TxHash        *string    `gorm:"size:66;uniqueIndex" json:"txHash,omitempty"`
	Note          string     `gorm:"size:256" json:"note,omitempty"`
	ActorID       *uuid.UUID `gorm:"type:uuid" json:"actorId,omitempty"`
	RequestedAt   time.Time  `gorm:"index" json:"requestedAt"`
	ResolvedAt    *time.Time `json:"resolvedAt,omitempty"`
}

func (FiatLPWithdrawal) TableName() string { return "fiat_lp_withdrawals" }
