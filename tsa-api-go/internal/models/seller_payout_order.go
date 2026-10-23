package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SellerPayoutOrder statuses. The path is awaiting_release -> received ->
// bridging -> bridged -> selling -> ordered -> depositing -> deposited -> paid;
// anything unproven, unexpected or over a limit parks in needs_review, and the
// machine never moves a payout out of it on its own.
const (
	// SellerPayoutAwaitingRelease: the marketplace order exists with the
	// treasury as its on-chain seller; nothing has been released yet.
	SellerPayoutAwaitingRelease = "awaiting_release"
	// SellerPayoutReceived: the release is proven to have credited the
	// treasury on the order's chain (ReceivedAmount is that proven amount).
	SellerPayoutReceived = "received"
	// SellerPayoutBridging: the Sonic -> Base bridge leg is reserved or sent.
	SellerPayoutBridging = "bridging"
	// SellerPayoutBridged: the bridge landing on Base is proven.
	SellerPayoutBridged = "bridged"
	// SellerPayoutSelling: the Paycrest sell is being opened (claimed before
	// the call, so only one tick ever opens it).
	SellerPayoutSelling = "selling"
	// SellerPayoutOrdered: the Paycrest order is open and awaits the deposit.
	SellerPayoutOrdered = "ordered"
	// SellerPayoutDepositing: the deposit leg is reserved or sent.
	SellerPayoutDepositing = "depositing"
	// SellerPayoutDeposited: the deposit landed at Paycrest's address (proven).
	SellerPayoutDeposited = "deposited"
	// SellerPayoutPaid: Paycrest settled the seller's naira. Terminal.
	SellerPayoutPaid = "paid"
	// SellerPayoutCancelled: the escrow order ended without paying the seller
	// (cancelled or refunded to the buyer); nothing reached the treasury.
	SellerPayoutCancelled = "cancelled"
	// SellerPayoutPayingWallet: an operator chose to pay the seller's wallet
	// instead (only before any bridge moved money); the transfer is in flight.
	SellerPayoutPayingWallet = "paying_wallet"
	// SellerPayoutPaidWallet: the seller's wallet was credited (proven). Terminal.
	SellerPayoutPaidWallet = "paid_wallet"
	// SellerPayoutPayingCompany: the company route's naira transfer from the
	// central account is claimed or submitted (company_ref names it). Claimed
	// BEFORE the request leaves, so a crash mid-request still reads as "may have
	// been sent".
	SellerPayoutPayingCompany = "paying_company"
	// SellerPayoutNeedsReview: parked for a person; see FailureReason.
	SellerPayoutNeedsReview = "needs_review"
)

// Venues: who delivers the naira. The company pays it from the central account
// (and the operator rebalances the USDC it holds in batches); Paycrest sells the
// USDC on Base.
const (
	SellerPayoutVenuePaycrest = "paycrest"
	SellerPayoutVenueCompany  = "company"
)

func SellerPayoutTerminal(status string) bool {
	return status == SellerPayoutPaid || status == SellerPayoutPaidWallet || status == SellerPayoutCancelled
}

// SellerPayoutOrder is one marketplace order's payout of fiat to the seller's
// bank. The treasury is the order's on-chain seller, so the escrow's release
// lands on it; this row carries the payout from there to the seller's account.
//
// It holds only its own state. The legs (bridge, deposit) are ramp_legs rows
// whose OrderID is this row's ID; the Paycrest order is a PaycrestOrderRecord
// that points back here (SellerPayoutID). Nothing is copied that lives there.
type SellerPayoutOrder struct {
	ID                 uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	MarketplaceOrderID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"marketplaceOrderId"`
	SellerUserID       uuid.UUID `gorm:"type:uuid;not null;index" json:"sellerUserId"`

	// The destination, snapshotted when the order was created so a later change
	// to the seller's payout method cannot redirect an order already in flight.
	Currency        string `gorm:"size:3;not null" json:"currency"`
	InstitutionCode string `gorm:"size:64;not null" json:"institutionCode"`
	InstitutionName string `gorm:"size:128;not null" json:"institutionName"`
	AccountNumber   string `gorm:"size:32;not null" json:"accountNumber"`
	AccountName     string `gorm:"size:128;not null" json:"accountName"`
	SellerWallet    string `gorm:"size:64" json:"-"` // for an operator's manual fallback

	// Where the release lands: the escrow's chain and token.
	ChainKey string `gorm:"size:24;not null" json:"chain"`
	Token    string `gorm:"size:16;not null" json:"token"`
	// ExpectedAmount is what the escrow should credit the treasury, in the
	// token's base units, from the order's amounts at creation. The proven
	// ReceivedAmount is compared with it; a smaller one parks.
	ExpectedAmount string `gorm:"size:78;not null" json:"expectedAmount"`
	// FiatTargetKobo is the naira (in kobo) quoted for the seller when the
	// order was created: what the payout aims to pay. The platform absorbs rate
	// drift below it up to the absorb limit; beyond that the payout parks.
	FiatTargetKobo int64 `gorm:"not null" json:"fiatTargetKobo"`

	Status string `gorm:"size:20;not null;index" json:"status"`
	// Venue is who delivers the naira, fixed when the order is created.
	Venue string `gorm:"size:12;not null;default:'paycrest'" json:"venue"`
	// ReceivedAmount is the amount the release provably credited, base units.
	ReceivedAmount string `gorm:"size:78" json:"receivedAmount,omitempty"`
	// PaycrestOrderID links the sell once opened (set exactly once).
	PaycrestOrderID *uuid.UUID `gorm:"type:uuid;index" json:"paycrestOrderId,omitempty"`
	FailureReason   string     `gorm:"size:512" json:"failureReason,omitempty"`
	// ParkedFrom is the status the payout was in when it went to needs_review,
	// and RetrySafe says it stopped before anything that could have moved money
	// was sent, so an admin may simply let it run again.
	ParkedFrom string `gorm:"size:20" json:"parkedFrom,omitempty"`
	// AttemptStartedAt restarts the clock of the retry windows: set when the
	// machine is let go again (an operator's retry, the switch turned back on),
	// so time spent paused or parked is never counted against a payout.
	AttemptStartedAt *time.Time `json:"-"`
	// NextAttemptAt is the earliest the machine tries a refused step again.
	NextAttemptAt *time.Time `json:"-"`
	RetrySafe     bool       `gorm:"not null;default:false" json:"retrySafe"`
	// The company route: CompanyRefGen is the reference generation (a retry
	// after a checked failure takes a new one, so the provider never replays the
	// old answer), CompanyRef the transfer reference of the current attempt,
	// CompanyAttempts how many were claimed, CompanyTransferID the provider's id.
	CompanyRefGen     int    `gorm:"not null;default:0" json:"-"`
	CompanyRef        string `gorm:"size:42;index" json:"-"`
	CompanyAttempts   int    `gorm:"not null;default:0" json:"-"`
	CompanyTransferID string `gorm:"size:64" json:"-"`
	// CompanyPaidAt is set ONLY when the provider's own transfer read says the
	// central account sent the naira. A payout marked paid by hand never has it,
	// so it can never be counted as USDC owed to a rebalance.
	CompanyPaidAt *time.Time `json:"companyPaidAt,omitempty"`
	// RebalancedAt is when an operator moved the USDC a paid company payout left
	// in the treasury back to the central account's funding side (a batch).
	RebalancedAt *time.Time `gorm:"index" json:"rebalancedAt,omitempty"`
	// CheckedAt rotates the worker's attention across rows.
	CheckedAt *time.Time `gorm:"index" json:"-"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

func (SellerPayoutOrder) TableName() string { return "seller_payouts" }

func (p *SellerPayoutOrder) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}

// RampLegsOneLiveDepositIndexSQL: at most one deposit in flight or done per
// payout, ever. A second tick's Reserve returns the existing leg instead of
// sending, so a payout can never fund Paycrest twice.
const RampLegsOneLiveDepositIndexSQL = `CREATE UNIQUE INDEX IF NOT EXISTS uq_ramp_legs_one_live_deposit
    ON ramp_legs (order_id)
    WHERE kind = 'deposit' AND status IN ('pending','confirmed')`

// SellerPayoutSetting is the operator's switch for seller payouts, one row. No
// row means on. Off: checkout stops creating payout orders (sellers are paid
// to their wallets) and the machine stops SENDING (bridge, sale, deposit);
// proofs and bookkeeping continue.
type SellerPayoutSetting struct {
	Key string `gorm:"primaryKey;size:16" json:"key"`
	// No default: GORM would turn an explicit false into the default.
	Enabled   bool       `gorm:"not null" json:"enabled"`
	UpdatedBy *uuid.UUID `gorm:"type:uuid" json:"updatedBy,omitempty"`
	Note      string     `gorm:"size:255" json:"note,omitempty"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

func (SellerPayoutSetting) TableName() string { return "seller_payout_settings" }

const SellerPayoutSettingKey = "payouts"
