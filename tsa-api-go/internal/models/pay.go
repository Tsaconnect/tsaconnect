package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	PayModeLive = "live"
	PayModeTest = "test"

	PayMerchantActive    = "active"
	PayMerchantSuspended = "suspended"

	PaySettlementFiat   = "fiat"
	PaySettlementCrypto = "crypto"

	PayStatusPending    = "pending"
	PayStatusProcessing = "processing"
	PayStatusSucceeded  = "succeeded"
	PayStatusFailed     = "failed"
	PayStatusExpired    = "expired"

	PayEventSucceeded = "payment.succeeded"
	PayEventFailed    = "payment.failed"
	PayEventExpired   = "payment.expired"
)

// PayMerchant is a business approved to accept payments through TSA Pay.
// Distinct from PaymentMerchant (P2P liquidity providers) — do not merge.
type PayMerchant struct {
	ID                      uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	MerchantRequestID       uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"merchantRequestId"`
	UserID                  uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	BusinessName            string    `gorm:"size:255;not null" json:"businessName"`
	WebsiteDomain           string    `gorm:"size:255" json:"websiteDomain"`
	Status                  string    `gorm:"size:16;not null;index" json:"status"`
	SettlementMode          string    `gorm:"size:8" json:"settlementMode"` // "" until configured
	BankCode                string    `gorm:"size:16" json:"bankCode,omitempty"`
	AccountNumber           string    `gorm:"size:32" json:"accountNumber,omitempty"`
	AccountName             string    `gorm:"size:128" json:"accountName,omitempty"` // always the name enquiry's, never the applicant's
	ChainID                 int64     `json:"chainId,omitempty"`
	TokenAddress            string    `gorm:"size:128" json:"tokenAddress,omitempty"`
	TokenSymbol             string    `gorm:"size:16" json:"tokenSymbol,omitempty"`
	WalletAddress           string    `gorm:"size:128" json:"walletAddress,omitempty"`
	CustomerFeeSharePercent int       `gorm:"not null;default:100" json:"customerFeeSharePercent"`
	MinAmountKobo           int64     `gorm:"not null" json:"minAmount"`
	MaxAmountKobo           int64     `gorm:"not null" json:"maxAmount"`
	WebhookURL              string    `gorm:"size:512" json:"webhookUrl"`
	TestWebhookURL          string    `gorm:"size:512" json:"testWebhookUrl"`
	WebhookSecretVersion    int       `gorm:"not null;default:1" json:"-"`
	ReturnDomains           string    `gorm:"size:1024" json:"-"` // comma-separated lowercase hostnames
	LiveKeyHash             *string   `gorm:"size:64;uniqueIndex" json:"-"`
	LiveKeyPrefix           string    `gorm:"size:24" json:"liveKeyPrefix"`
	TestKeyHash             *string   `gorm:"size:64;uniqueIndex" json:"-"`
	TestKeyPrefix           string    `gorm:"size:24" json:"testKeyPrefix"`
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
}

func (m *PayMerchant) BeforeCreate(tx *gorm.DB) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	return nil
}

// ReturnDomainList is ReturnDomains split, for checks and the admin view.
func (m *PayMerchant) ReturnDomainList() []string {
	var out []string
	for _, d := range strings.Split(m.ReturnDomains, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// WebhookURLFor is where events of a mode go. Test events never reach the
// live endpoint: an empty test URL means test events are not sent.
func (m *PayMerchant) WebhookURLFor(mode string) string {
	if mode == PayModeLive {
		return m.WebhookURL
	}
	return m.TestWebhookURL
}

// PayPayment is one checkout a merchant created. Fee, split and settlement
// target are snapshotted at creation: editing the merchant never changes a
// payment already handed to a customer. At most one attempt (order or
// intent) is linked at a time.
type PayPayment struct {
	ID                      uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	MerchantID              uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:uq_pay_payments_merchant_ref,priority:1" json:"merchantId"`
	Mode                    string     `gorm:"size:8;not null;uniqueIndex:uq_pay_payments_merchant_ref,priority:2" json:"mode"`
	Reference               string     `gorm:"size:128;not null;uniqueIndex:uq_pay_payments_merchant_ref,priority:3" json:"reference"`
	AmountKobo              int64      `gorm:"not null" json:"amount"`
	Currency                string     `gorm:"size:3;not null" json:"currency"`
	Description             string     `gorm:"size:255" json:"description"`
	ReturnURL               string     `gorm:"size:1024" json:"returnUrl,omitempty"`
	FeeBps                  int        `gorm:"not null" json:"-"`
	CustomerFeeSharePercent int        `gorm:"not null" json:"-"`
	CustomerTotalKobo       int64      `gorm:"not null" json:"customerTotal"`
	MerchantNetKobo         int64      `gorm:"not null" json:"merchantNet"`
	SettlementMode          string     `gorm:"size:8;not null" json:"settlementMode"`
	SettleBankCode          string     `gorm:"size:16" json:"-"`
	SettleAccountNumber     string     `gorm:"size:32" json:"-"`
	SettleAccountName       string     `gorm:"size:128" json:"-"`
	SettleChainID           int64      `json:"-"`
	SettleTokenAddress      string     `gorm:"size:128" json:"-"`
	SettleTokenSymbol       string     `gorm:"size:16" json:"-"`
	SettleWalletAddress     string     `gorm:"size:128" json:"-"`
	ExpiresAt               time.Time  `gorm:"not null;index" json:"expiresAt"`
	Status                  string     `gorm:"size:16;not null;index" json:"status"`
	PayerUserID             *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	LockedAt                *time.Time `json:"-"`
	SellIntentID            *uuid.UUID `gorm:"type:uuid;uniqueIndex" json:"-"`
	PaycrestOrderID         *uuid.UUID `gorm:"type:uuid;uniqueIndex" json:"-"`
	SettlementRef           string     `gorm:"size:128" json:"settlementRef,omitempty"`
	FailureReason           string     `gorm:"size:255" json:"failureReason,omitempty"`
	SucceededAt             *time.Time `json:"succeededAt,omitempty"`
	CreatedAt               time.Time  `json:"createdAt"`
	UpdatedAt               time.Time  `json:"updatedAt"`
}

func (p *PayPayment) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}

// PayWebhookDelivery is one event owed to a merchant. The row's ID is the
// event id merchants dedupe on; Payload is the exact body we sign and send.
type PayWebhookDelivery struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	PaymentID      uuid.UUID  `gorm:"type:uuid;not null;index" json:"paymentId"`
	MerchantID     uuid.UUID  `gorm:"type:uuid;not null;index" json:"merchantId"`
	Mode           string     `gorm:"size:8;not null" json:"mode"`
	Event          string     `gorm:"size:32;not null" json:"event"`
	Payload        string     `gorm:"type:text;not null" json:"-"`
	Attempts       int        `gorm:"not null;default:0" json:"attempts"`
	NextAttemptAt  *time.Time `gorm:"index" json:"nextAttemptAt,omitempty"`
	LastStatusCode int        `json:"lastStatusCode"`
	LastError      string     `gorm:"size:255" json:"lastError,omitempty"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
	FailedAt       *time.Time `json:"failedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

func (d *PayWebhookDelivery) BeforeCreate(tx *gorm.DB) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	return nil
}
