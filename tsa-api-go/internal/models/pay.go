package models

import (
	"net/netip"
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

	// PaySourceAPI is a payment a merchant's own server created with its API
	// key; PaySourceLink is a one-time payment link the owner made by hand in
	// the app or on the web, for one customer; PaySourceOpen is a payment a
	// customer started themselves, for an amount they entered, through one of
	// the owner's reusable open links (a shop's QR, say).
	PaySourceAPI  = "api"
	PaySourceLink = "link"
	PaySourceOpen = "open"
)

// PayMerchant is a business approved to accept payments through TSA Pay.
// Distinct from PaymentMerchant (P2P liquidity providers) — do not merge.
// Version is bumped by every settings edit and is the admin console's
// compare-and-set token.
type PayMerchant struct {
	ID                uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	MerchantRequestID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"merchantRequestId"`
	UserID            uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	BusinessName      string    `gorm:"size:255;not null" json:"businessName"`
	WebsiteDomain     string    `gorm:"size:255" json:"websiteDomain"`
	Status            string    `gorm:"size:16;not null;index" json:"status"`
	// LiveEnabled is this merchant's own live switch, on top of the global
	// TSA_PAY_LIVE_ENABLED: a live key is refused until a super admin turns it
	// on. Only the admin merchant update can change it.
	LiveEnabled             bool      `gorm:"not null;default:false" json:"liveEnabled"`
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
	AllowedIPs              string    `gorm:"size:1024" json:"-"` // the live key's allowlist: comma-separated IPs/CIDRs, "" = anywhere (see KeyAllowedFrom)
	Version                 int       `gorm:"not null;default:1" json:"version"`
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

// AllowedIPList is AllowedIPs split, for checks and the views.
func (m *PayMerchant) AllowedIPList() []string {
	var out []string
	for _, e := range strings.Split(m.AllowedIPs, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// KeyAllowedFrom reports whether a request made with this merchant's key of
// mode may come from ip. Only the live key is restricted — a test key moves
// no money and is used from developers' own machines — and only once the
// merchant has listed an address. An entry that no longer parses matches
// nothing, so a bad row fails closed.
func (m *PayMerchant) KeyAllowedFrom(mode string, ip netip.Addr) bool {
	if mode != PayModeLive {
		return true
	}
	list := m.AllowedIPList()
	if len(list) == 0 {
		return true
	}
	ip = ip.Unmap()
	for _, e := range list {
		if p, err := netip.ParsePrefix(e); err == nil {
			if p.Contains(ip) {
				return true
			}
		} else if a, err := netip.ParseAddr(e); err == nil && a == ip {
			return true
		}
	}
	return false
}

// LiveAllowed is the one live rule: a live key works, and a live payment can be
// paid, only while TSA Pay live is on globally AND a super admin has switched
// this merchant live.
func (m *PayMerchant) LiveAllowed(global bool) bool {
	return global && m.LiveEnabled
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
// intent) is linked at a time. CustomerName is who a payment link was made
// for, as the owner typed it, and is shown to whoever pays it; an API payment
// never has one. OpenLinkID is the open link an open payment was started from,
// and OpenLinkSlug the address it was started at (a replaced link has a new
// one). AttemptedAt is when a payer first locked it: unlike LockedAt it is
// never cleared, so a payment someone tried and gave up on stays tried.
type PayPayment struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	MerchantID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:uq_pay_payments_merchant_ref,priority:1;index:idx_pay_payments_merchant_mode_created,priority:1;index:idx_pay_payments_merchant_mode_succeeded,priority:1" json:"merchantId"`
	Mode       string    `gorm:"size:8;not null;uniqueIndex:uq_pay_payments_merchant_ref,priority:2;index:idx_pay_payments_merchant_mode_created,priority:2;index:idx_pay_payments_merchant_mode_succeeded,priority:2" json:"mode"`
	Reference  string    `gorm:"size:128;not null;uniqueIndex:uq_pay_payments_merchant_ref,priority:3" json:"reference"`
	// PayCode is the short code a payer reads off a page or an invoice and
	// types into the app (spec D7): ten Crockford base32 characters, bare and
	// upper case; services.FormatPayCode shows it as TSA-XXXXX-XXXXX. Nil only
	// on a payment made before codes existed, until EnsurePayCode gives it one.
	PayCode                 *string    `gorm:"size:10;uniqueIndex:idx_pay_payments_pay_code" json:"-"`
	AmountKobo              int64      `gorm:"not null" json:"amount"`
	Currency                string     `gorm:"size:3;not null" json:"currency"`
	Description             string     `gorm:"size:255" json:"description"`
	Source                  string     `gorm:"size:8;not null;default:'api'" json:"source"`
	CustomerName            string     `gorm:"size:100" json:"customerName,omitempty"`
	OpenLinkID              *uuid.UUID `gorm:"type:uuid;index:idx_pay_payments_open_link_created,priority:1;index:idx_pay_payments_open_link_paid,where:status = 'succeeded'" json:"openLinkId,omitempty"`
	OpenLinkSlug            string     `gorm:"size:64" json:"-"`
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
	AttemptedAt             *time.Time `json:"-"`
	SellIntentID            *uuid.UUID `gorm:"type:uuid;uniqueIndex" json:"-"`
	PaycrestOrderID         *uuid.UUID `gorm:"type:uuid;uniqueIndex" json:"-"`
	SettlementRef           string     `gorm:"size:128" json:"settlementRef,omitempty"`
	FailureReason           string     `gorm:"size:255" json:"failureReason,omitempty"`
	SucceededAt             *time.Time `gorm:"index:idx_pay_payments_merchant_mode_succeeded,priority:3" json:"succeededAt,omitempty"`
	CreatedAt               time.Time  `gorm:"index:idx_pay_payments_merchant_mode_created,priority:3;index:idx_pay_payments_open_link_created,priority:2" json:"createdAt"`
	UpdatedAt               time.Time  `json:"updatedAt"`
}

// PayUntriedOpenSQL is Untried as a WHERE condition on pay_payments. Keep the
// two in step: they are the one definition of an untried payment.
const PayUntriedOpenSQL = "source = '" + PaySourceOpen + "' AND attempted_at IS NULL"

// Untried reports whether p is an open-link payment nobody has ever started
// paying. Each visitor who enters an amount at an open link makes one, so an
// untried one is a scan, not a payment its merchant needs to see or be told
// about. (Payments of every other source are never "untried": someone chose to
// create each one.)
func (p *PayPayment) Untried() bool {
	return p.Source == PaySourceOpen && p.AttemptedAt == nil
}

func (p *PayPayment) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}

// PayOpenLink is a reusable payment link: a fixed address — printed as a shop's
// QR, or shared — through which any customer starts a payment for the amount
// they enter, within [MinAmountKobo, MaxAmountKobo] (0 meaning the merchant's
// own bound). A merchant may have many in each mode. Label is what the payer
// sees ("Mama Ngozi Store — Shop 12").
//
// Replacing a link (a poster was copied, say) gives it a new Slug: the old
// address stops existing, while the link — and every payment made through it —
// stays the same row. Active is the owner's pause switch. Version is the
// compare-and-set token an edit is made against.
type PayOpenLink struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	MerchantID    uuid.UUID `gorm:"type:uuid;not null;index:idx_pay_open_links_merchant_mode,priority:1" json:"merchantId"`
	Mode          string    `gorm:"size:8;not null;index:idx_pay_open_links_merchant_mode,priority:2" json:"mode"`
	Slug          string    `gorm:"size:64;not null;uniqueIndex" json:"slug"`
	Label         string    `gorm:"size:80;not null" json:"label"`
	MinAmountKobo int64     `gorm:"not null;default:0" json:"minAmount"`
	MaxAmountKobo int64     `gorm:"not null;default:0" json:"maxAmount"`
	Active        bool      `gorm:"not null;default:true" json:"active"`
	Version       int       `gorm:"not null;default:1" json:"version"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (l *PayOpenLink) BeforeCreate(tx *gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}

// PayRetiredSlug is an open-link address that was replaced. Kept so no new
// link is ever handed an address that was once printed for another: a poster
// that still hangs somewhere must never start paying someone else.
type PayRetiredSlug struct {
	Slug       string    `gorm:"size:64;primaryKey" json:"slug"`
	OpenLinkID uuid.UUID `gorm:"type:uuid;not null;index" json:"openLinkId"`
	RetiredAt  time.Time `gorm:"not null" json:"retiredAt"`
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
	DeliveredAt    *time.Time `gorm:"index" json:"deliveredAt,omitempty"`
	FailedAt       *time.Time `gorm:"index" json:"failedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

func (d *PayWebhookDelivery) BeforeCreate(tx *gorm.DB) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	return nil
}

// PayAuditAction names what a PayAuditEvent row records. Go has no closed
// enum, so this does not stop a stray string literal at some future call
// site — but it gives the ones the codebase writes one place to be spelled
// correctly from, rather than retyped (and possibly mistyped) at each
// writeAudit call.
type PayAuditAction string

const (
	PayAuditActionMerchantUpdate PayAuditAction = "merchant.update"
	PayAuditActionMerchantKeys   PayAuditAction = "merchant.keys"
	// An admin switched a credential off; the owner creates its replacement.
	PayAuditActionMerchantKeysRevoked PayAuditAction = "merchant.keys_revoked"
	// PayAuditActionBusinessNameReview: an admin approved (renaming the
	// merchant) or rejected an owner's business-name request.
	PayAuditActionBusinessNameReview PayAuditAction = "merchant.business_name_review"
)

// PayAuditEvent is the durable record of a settlement/credential change to a
// merchant, written in the same transaction as the change. Details is JSON
// holding before/after values of non-secret fields only — never a key, key
// hash or webhook secret.
type PayAuditEvent struct {
	ID         uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	MerchantID uuid.UUID      `gorm:"type:uuid;not null;index" json:"merchantId"`
	ActorID    uuid.UUID      `gorm:"type:uuid;not null;index" json:"actorId"`
	Action     PayAuditAction `gorm:"size:32;not null" json:"action"`
	Details    string         `gorm:"type:text;not null" json:"details"`
	CreatedAt  time.Time      `json:"createdAt"`
}

func (e *PayAuditEvent) BeforeCreate(tx *gorm.DB) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	return nil
}

// PayOwnerAction is a merchant owner's sensitive change waiting on the code
// emailed to them: a crypto payout, a key roll, webhook/return-domain changes, or
// revealing/rolling the webhook secret. A change made with the account password
// alone could be a stolen login, so nothing is applied until the owner proves
// they read the owner's email. One per merchant per kind (a new request replaces
// an older one); deleted once used, exhausted, expired or superseded.
// BaseVersion is the merchant version the change was requested against, so any
// edit in between makes it stale.
type PayOwnerAction struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	MerchantID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:uq_pay_owner_action_kind" json:"-"`
	Kind        string    `gorm:"size:24;not null;uniqueIndex:uq_pay_owner_action_kind" json:"-"`
	OwnerID     uuid.UUID `gorm:"type:uuid;not null" json:"-"`
	Payload     string    `gorm:"type:text;not null" json:"-"`
	BaseVersion int       `gorm:"not null" json:"-"`
	CodeHash    string    `gorm:"size:64;not null" json:"-"`
	Attempts    int       `gorm:"not null;default:0" json:"-"`
	ExpiresAt   time.Time `gorm:"not null" json:"-"`
	CreatedAt   time.Time `json:"-"`
}

// PayOwnerAction kinds.
const (
	PayOwnerActionSettlement    = "settlement"
	PayOwnerActionKeys          = "keys"
	PayOwnerActionEndpoints     = "endpoints"
	PayOwnerActionWebhookSecret = "webhook_secret"
	PayOwnerActionBusinessName  = "business_name"
)

func (a *PayOwnerAction) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

// Business-name change review statuses.
const (
	PayNameChangePending  = "pending"
	PayNameChangeApproved = "approved"
	PayNameChangeRejected = "rejected"
)

// PayMerchantNameChange records one owner request to rename a TSA Pay
// business, with the owner's verified legal name as it stood when they asked.
// Nothing about the merchant changes until an admin approves it: customers,
// checkout, links, receipts and the payout-account name check all keep the
// current name meanwhile. A later request (or asking for the current name
// back) closes a pending one as rejected, with a note saying why.
type PayMerchantNameChange struct {
	ID                uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	MerchantID        uuid.UUID  `gorm:"type:uuid;not null;index" json:"merchantId"`
	OldName           string     `gorm:"size:255;not null" json:"oldName"`
	NewName           string     `gorm:"size:255;not null" json:"newName"`
	OwnerVerifiedName string     `gorm:"size:255" json:"ownerVerifiedName"`
	Status            string     `gorm:"size:16;not null;index" json:"status"`
	RequestedBy       uuid.UUID  `gorm:"type:uuid;not null" json:"requestedBy"`
	ReviewedBy        *uuid.UUID `gorm:"type:uuid" json:"reviewedBy,omitempty"`
	Note              string     `gorm:"size:512" json:"note,omitempty"`
	ReviewedAt        *time.Time `json:"reviewedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

func (c *PayMerchantNameChange) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}
