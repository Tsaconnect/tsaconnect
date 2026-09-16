package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Utility bills v2 — a user buys airtime/data/electricity/cable/betting/exam
// e-PINs, paying with SPENDABLE MCGP via the operator debit path
// (BillPayment owns the money leg; this order owns what was bought). The bill
// is vended in Naira from the company's prepaid ClubKonnect float.
//
// Money never lives here: mcgp_amount/rate_kobo are quote snapshots for
// display and dispute reconstruction; the debit itself is the BillPayment row
// keyed by this order's ID, and the on-chain pendingSettle is the truth.

// Bill categories.
const (
	BillCategoryAirtime     = "airtime"
	BillCategoryData        = "data"
	BillCategoryElectricity = "electricity"
	BillCategoryCableTV     = "cabletv"
	BillCategoryBetting     = "betting"
	BillCategoryEpin        = "epin"
)

// Bill order statuses.
//
//	quoted ──pay──▶ paying ──debit ok──▶ paid ──vend──▶ vending ─┬─▶ delivered   (+ settle)
//	   │                │                                        └─▶ vend_failed (+ reverse → refunded)
//	   └─▶ expired      └─▶ failed (debit never confirmed)
//
// `vending` covers ClubKonnect's whole non-terminal range (received/processed/
// on-hold/201-network-unresponsive) — the reconciler polls it to a terminal
// state. `vend_failed` means the vend terminally failed and the payment reversal
// is owed; `refunded` means the reversal confirmed (user made whole on-chain).
const (
	BillStatusQuoted    = "quoted"
	BillStatusPaying    = "paying"
	BillStatusPaid      = "paid"
	BillStatusVending   = "vending"
	BillStatusDelivered = "delivered"
	BillStatusVendFail  = "vend_failed"
	BillStatusRefunded  = "refunded"
	BillStatusExpired   = "expired"
	BillStatusFailed    = "failed"
)

// BillOrder is one utility-bill purchase.
type BillOrder struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`

	Category string `gorm:"size:16;not null;index" json:"category"`
	// Provider is the ClubKonnect operator code for the category: telco ID
	// ("01"…), disco ID, cable ID ("dstv"), betting PRODUCT_CODE, or
	// "waec"/"jamb" for e-PINs.
	Provider     string `gorm:"size:48;not null" json:"provider"`
	ProviderName string `gorm:"size:64" json:"providerName,omitempty"`

	// CustomerID is the meter / smartcard / betting-account id; PhoneNumber the
	// MSISDN for airtime/data/e-PIN delivery; CustomerName the verified holder
	// name (electricity/cable lookup).
	CustomerID   string `gorm:"size:64" json:"customerId,omitempty"`
	CustomerName string `gorm:"size:128" json:"customerName,omitempty"`
	PhoneNumber  string `gorm:"size:16" json:"phoneNumber,omitempty"`
	// PlanRef is the fixed-price product key where the category has one: data
	// PRODUCT_CODE, cable PACKAGE_ID, e-PIN exam PRODUCT_CODE.
	PlanRef  string `gorm:"size:64" json:"planRef,omitempty"`
	PlanName string `gorm:"size:128" json:"planName,omitempty"`
	// MeterType: electricity only — "01" prepaid / "02" postpaid.
	MeterType string `gorm:"size:8" json:"meterType,omitempty"`

	// NgnAmount is what the USER pays in whole Naira (face value; data includes
	// the retail markup). VendNgnAmount is what is sent to ClubKonnect — equal to
	// NgnAmount except for data, where the vend is plan-priced and this records
	// the plan cost.
	NgnAmount     int64 `gorm:"not null" json:"ngnAmount"`
	VendNgnAmount int64 `gorm:"not null" json:"-"`

	// Quote snapshot: McgpAmount (18dp base units, decimal string) at RateKobo
	// (kobo per MCGP). The debit uses exactly these numbers.
	McgpAmount string `gorm:"type:numeric(78,0);not null" json:"mcgpAmount"`
	RateKobo   string `gorm:"type:numeric(38,0);not null" json:"rateKobo"`

	// Vendor references. VendorOrderID is ClubKonnect's orderid; the vend/query
	// RequestID is always this row's ID (client-side idempotency).
	VendorOrderID    string `gorm:"size:32;index" json:"vendorOrderId,omitempty"`
	VendorStatusCode int    `json:"vendorStatusCode,omitempty"`
	// DeliveryPayload carries what the customer actually received when the
	// vend returns one: prepaid meter token, exam PIN.
	DeliveryPayload string `gorm:"size:512" json:"deliveryPayload,omitempty"`

	FailureReason string `gorm:"size:256" json:"failureReason,omitempty"`

	Status string `gorm:"size:16;not null;default:'quoted';index" json:"status"`
	// ClaimSeq is the row's version: every guarded transition bumps it, and a
	// caller claims the order by writing the version it read. That is what
	// stops two reconcile passes both re-vending an order neither of them
	// moved — and, unlike comparing updated_at, it does not depend on how
	// precisely the database stores timestamps.
	ClaimSeq       int       `gorm:"not null;default:0" json:"-"`
	QuoteExpiresAt time.Time `json:"quoteExpiresAt"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (BillOrder) TableName() string { return "bill_orders" }

func (o *BillOrder) BeforeCreate(tx *gorm.DB) error {
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	return nil
}

// IsExpired reports whether a still-quoted order has passed its quote window.
func (o *BillOrder) IsExpired(now time.Time) bool {
	return o.Status == BillStatusQuoted && now.After(o.QuoteExpiresAt)
}
