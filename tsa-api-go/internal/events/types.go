package events

import "github.com/google/uuid"

// Event type constants
const (
	// Transaction events
	TransactionCompleted = "transaction.completed"
	TransactionFailed    = "transaction.failed"
	TransactionPending   = "transaction.pending"
	TransactionReceived  = "transaction.received"

	// Order events
	OrderPlaced    = "order.placed"
	OrderEscrowed  = "order.escrowed"
	OrderDelivered = "order.delivered"
	OrderCompleted = "order.completed"
	OrderRefunded  = "order.refunded"

	// Security events
	SecurityLoginNewDevice  = "security.login_new_device"
	SecurityFailedAttempts  = "security.failed_attempts"
	SecurityPasswordChanged = "security.password_changed"
	SecurityAccountLocked   = "security.account_locked"

	// Verification events
	VerificationSubmitted = "verification.submitted"
	VerificationApproved  = "verification.approved"
	VerificationRejected  = "verification.rejected"

	// Merchant events
	MerchantSubmitted = "merchant.submitted"
	MerchantApproved  = "merchant.approved"
	MerchantRejected  = "merchant.rejected"
	MerchantSuspended = "merchant.suspended"

	// Product events
	ProductListed   = "product.listed"
	ProductApproved = "product.approved"
	ProductRejected = "product.rejected"

	// Wallet events
	WalletWhitelistAdded = "wallet.whitelist_added"
	WalletLimitChanged   = "wallet.limit_changed"
)

// Event is the payload passed through the event bus.
type Event struct {
	Type    string
	UserID  uuid.UUID
	Title   string
	Message string
	Data    map[string]interface{}
}
