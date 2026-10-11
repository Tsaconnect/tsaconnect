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
	// SecurityPaySettlementChanged: where a TSA Pay merchant is paid was
	// changed from their own account. The one change a stolen login would make
	// to redirect a merchant's money, so it is told to the owner on every
	// channel — the change stands, but it is never silent.
	SecurityPaySettlementChanged = "security.pay_settlement_changed"
	// A TSA Pay merchant's API key was rolled, their webhook/return-domain
	// settings changed, or their webhook signing secret was revealed or rolled.
	// Like the payout change: the owner is always told.
	SecurityPayKeyRolled        = "security.pay_key_rolled"
	SecurityPayEndpointsChanged = "security.pay_endpoints_changed"
	SecurityPayWebhookSecret    = "security.pay_webhook_secret"
	SecurityPayKeyRefusedIP     = "security.pay_key_refused_ip"
	// A TSA Pay admin revoked one of the merchant's credentials (an API key or
	// the webhook signing secrets); the message carries the admin's reason.
	SecurityPayKeysRevoked = "security.pay_keys_revoked"

	// Account-deletion events (Apple 5.1.1(v) self-service deletion).
	// AccountDeletionRequested fires twice per request: once for the departing
	// user (confirmation + grace-window notice) and once per admin/super-admin
	// (review queue). Data carries "requestId", "userId", "email", "reason" and
	// "purgeAfter". AccountDeletionRestored fires when an admin reinstates an
	// account. Both email — a departing user must get a paper trail, and a
	// silent reinstatement would be a security hole.
	AccountDeletionRequested = "account.deletion_requested"
	AccountDeletionCompleted = "account.deletion_completed"
	AccountDeletionRestored  = "account.deletion_restored"

	// Verification events
	VerificationSubmitted = "verification.submitted"
	VerificationApproved  = "verification.approved"
	VerificationRejected  = "verification.rejected"

	// Merchant events
	MerchantSubmitted = "merchant.submitted"
	MerchantApproved  = "merchant.approved"
	MerchantRejected  = "merchant.rejected"
	MerchantSuspended = "merchant.suspended"
	// A super admin switched a TSA Pay merchant's live payments on or off.
	// Informational: told to the owner by email and push, and mutable.
	PayLiveEnabled  = "merchant.pay_live_enabled"
	PayLiveDisabled = "merchant.pay_live_disabled"
	// PayPaymentReceived: a TSA Pay payment to the merchant succeeded — from
	// their API, a payment link or an open link, live or test. Told to the owner
	// once per payment, in the app, by push and by email; informational, so it
	// respects their mute settings. Data carries paymentId, merchantId, mode and
	// source.
	PayPaymentReceived = "pay_payment_received"

	// A TSA Pay owner asked to rename their business, and an admin approved
	// (the rename takes effect) or rejected it. Told to the owner.
	PayBusinessNameRequested = "pay_business_name_requested"
	PayBusinessNameApproved  = "pay_business_name_approved"
	PayBusinessNameRejected  = "pay_business_name_rejected"

	// TSA Pay refunds. Requested (owner: approve it in the app — not sent when
	// they asked in the app), received (payer: money reached their wallet),
	// confirmed / failed / needs review (owner). Data carries refundId and
	// paymentId. Test refunds are prefixed "Test: ".
	PayRefundRequested   = "pay_refund_requested"
	PayRefundReceived    = "pay_refund_received"
	PayRefundConfirmed   = "pay_refund_confirmed"
	PayRefundFailed      = "pay_refund_failed"
	PayRefundNeedsReview = "pay_refund_needs_review"

	// Name change by re-verification: started (fee paid), applied, held for an
	// admin because the evidence was not strong enough to apply itself,
	// refused by that admin, or run out of attempts or time. Data carries
	// nameChangeId and status.
	NameChangeStarted  = "name_change_started"
	NameChangeApproved = "name_change_approved"
	NameChangeHeld     = "name_change_held"
	NameChangeRejected = "name_change_rejected"
	NameChangeLapsed   = "name_change_lapsed"

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
	// LiquidityVoucherExpiring: an LP's stock-ad voucher needs renewing (TSA
	// Liquidity spec §8.4 step 6, §19). Data: "adId".
	LiquidityVoucherExpiring = "liquidity.voucher_expiring"

	// TP reward events — fired by the payout-computation engine
	// (services.RewardService, fee/TP overhaul SP5). The notification dispatcher
	// (SP8) subscribes to push/email level-ups; an admin-ops handler subscribes
	// to TPPrizeDue for manual physical-prize fulfilment.
	//
	// TPLevelUp fires once per newly-crossed reward level. UserID is the user who
	// crossed; Data carries: "level" (int), "levelName" (string, the PM rank name
	// e.g. "Ambassador"), "rewardUsd" (float64), "mcgpAmount" (float64, the locked
	// MCGP), "rateUsdPerMcgp" (float64), "prize" (string, empty for L1..L5).
	TPLevelUp = "tp.level.up"

	// TPPrizeDue fires once per crossed L6+ level so admin ops can fulfil the
	// physical prize manually (NEVER auto-paid). UserID is the recipient; Data
	// carries "level" (int) and "prize" (string).
	TPPrizeDue = "tp.prize.due"

	// RampRateUpdated: a routed stablecoin buy's money arrived late or
	// off-amount, so it was priced at the live rate. Data carries intentId,
	// asset and assetAmount (base units).
	RampRateUpdated = "ramp.rate_updated"
)

// Event is the payload passed through the event bus.
type Event struct {
	Type   string
	UserID uuid.UUID
	// ActorID is the user who performed the action (uuid.Nil for system and
	// reconciler events). Serialised to clients as data.actorId.
	ActorID uuid.UUID
	Title   string
	Message string
	Data    map[string]interface{}
}
