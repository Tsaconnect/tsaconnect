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
	OrderPlaced          = "order.placed"
	OrderEscrowed        = "order.escrowed"
	OrderShipped         = "order.shipped"
	OrderDelivered       = "order.delivered"
	OrderCompleted       = "order.completed"
	OrderRefundRequested = "order.refund_requested"
	OrderRefundApproved  = "order.refund_approved"
	OrderRefundRejected  = "order.refund_rejected"
	OrderRefunded        = "order.refunded"
	OrderCancelRequested = "order.cancel_requested"
	OrderCancelApproved  = "order.cancel_approved"
	OrderCancelRejected  = "order.cancel_rejected"
	OrderDisputed        = "order.disputed"
	OrderDisputeResolved = "order.dispute_resolved"

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

	// P2P events — fired by P2PTradeService on every state transition so the
	// notification dispatcher, chat system-message emitter, and chain
	// reconciler can react. UserID is set to the participant who triggered
	// the transition (buyer / seller / admin); Data carries the trade & ad
	// IDs for downstream handlers.
	P2PAdCreated      = "p2p.ad.created"
	P2PAdClosed       = "p2p.ad.closed"
	P2PAdPaused       = "p2p.ad.paused"
	P2PAdResumed      = "p2p.ad.resumed"
	P2PTradeOpened    = "p2p.trade.opened"
	P2PTradePaid      = "p2p.trade.paid"
	P2PTradeReleasing = "p2p.trade.releasing"
	P2PTradeReleased  = "p2p.trade.released"
	P2PTradeCancelled = "p2p.trade.cancelled"
	P2PTradeDisputed  = "p2p.trade.disputed"
	P2PTradeResolved  = "p2p.trade.resolved"
	// P2PTradeExpired fires when the timeout watchdog finds a pending_payment
	// trade past its expiry. The buyer never marked paid; the seller should
	// reclaim their escrow via the cancelByTimeout flow.
	P2PTradeExpired = "p2p.trade.expired"
	// Pool-model capital movement.
	P2PDepositConfirmed  = "p2p.deposit.confirmed"
	P2PWithdrawConfirmed = "p2p.withdraw.confirmed"

	// TP reward events — fired by the payout-computation engine
	// (services.RewardService, fee/TP overhaul SP5). The notification dispatcher
	// (SP8) subscribes to push/email level-ups; an admin-ops handler subscribes
	// to TPPrizeDue for manual physical-prize fulfilment.
	//
	// TPLevelUp fires once per newly-crossed reward level. UserID is the user who
	// crossed; Data carries: "level" (int), "rewardUsd" (float64), "mcgpAmount"
	// (float64, the locked MCGP), "rateUsdPerMcgp" (float64), "prize" (string,
	// empty for L1..L5).
	TPLevelUp = "tp.level.up"

	// TPPrizeDue fires once per crossed L6+ level so admin ops can fulfil the
	// physical prize manually (NEVER auto-paid). UserID is the recipient; Data
	// carries "level" (int) and "prize" (string).
	TPPrizeDue = "tp.prize.due"
)

// Event is the payload passed through the event bus.
type Event struct {
	Type    string
	UserID  uuid.UUID
	Title   string
	Message string
	Data    map[string]interface{}
}
