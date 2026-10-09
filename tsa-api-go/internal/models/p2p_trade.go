package models

import (
	"time"

	"github.com/google/uuid"
)

// P2PTrade is a single buyer's claim against a P2PAd. The DB status is a
// SUPERSET of the on-chain Trade status enum in P2PEscrow.sol — it adds
// off-chain-only transitional states that have no contract analogue.
//
// On-chain enum (P2PEscrow.sol Status):
//
//	None, Pending, Paid, Released, Cancelled, Disputed, Resolved
//
// DB lifecycle (this model), with on-chain counterpart noted:
//
//	pending_payment → paid              (buyer markPaid; chain: Pending → Paid)
//	pending_payment → cancelled         (cancelByTimeout; chain: Pending → Cancelled)
//	paid → releasing                    (seller broadcast release tx; chain UNCHANGED, still Paid)
//	releasing → released                (release tx confirmed; chain: Paid → Released)
//	releasing → paid                    (release tx reverted/lost; chain UNCHANGED, still Paid — seller retries)
//	paid → disputed                     (raiseDispute; chain: Paid → Disputed)
//	disputed → resolved                 (admin adminResolve; chain: Disputed → Resolved)
//
// The "releasing" state is purely off-chain — it exists so the UI can show
// "seller is releasing your USDT…" between submit and receipt confirmation.
// THE CHAIN RECONCILER MUST NOT TREAT "releasing" AS DRIFT: when DB=releasing
// and chain=Paid, the correct action is to wait (or, after a stale timeout,
// transition DB back to "paid" so the seller can re-attempt release).
//
// Source-of-truth rule (see plan doc §1): on-chain Trade.status is the only
// authority on whether FUNDS HAVE MOVED. DB status is allowed to lead chain
// status (markPaid hits chain after the seller has been told the buyer
// claims to have paid) but never to lie about settlement.
//
// OnChainTradeID is the bytes32 used on-chain (keccak256-derived from ID,
// stored hex with 0x prefix). Indexed unique because we never want two DB
// rows pointing at the same on-chain trade.
//
// PaymentMethodID is the bank/mobile-money account the buyer is paying to —
// chosen at openTrade time from the ad's PaymentMethodIDs list.
type P2PTrade struct {
	ID       uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	AdID     uuid.UUID `gorm:"type:uuid;not null;index" json:"adId"`
	BuyerID  uuid.UUID `gorm:"type:uuid;not null;index" json:"buyerId"`
	SellerID uuid.UUID `gorm:"type:uuid;not null;index" json:"sellerId"`
	// OpenerID is the user who funds + broadcasts the open (buyer on a sell-ad,
	// seller/depositor on a buy-ad). Backs a partial unique index that stops a
	// single opener from holding >1 active trade on the same ad (capacity-drain
	// guard, the concurrent backstop for the service-layer check). Nullable so
	// pre-existing rows don't collide on the partial unique index.
	OpenerID *uuid.UUID `gorm:"type:uuid;index" json:"openerId,omitempty"`
	Token    string     `gorm:"size:16;not null" json:"token"`
	ChainID  int64      `gorm:"not null" json:"chainId"`
	// Funding discriminates the two-sided escrow flow. Pooled (0) = sell-ad:
	// the crypto comes from the merchant's deposited on-chain pool. Deposited-
	// by-taker (1) = buy-ad: the user deposits crypto just-in-time at
	// openTradeWithDeposit and there is no on-chain pool to credit on refund —
	// the crypto returns to the user's wallet. Mirrors P2PEscrowV2.sol's
	// `Funding` enum { PooledFromMaker, DepositedByTaker }.
	Funding         uint8     `gorm:"not null;default:0" json:"funding"`
	TokenAmount     string    `gorm:"type:numeric(78,0);not null" json:"tokenAmount"`
	FiatAmount      string    `gorm:"type:numeric(38,2);not null" json:"fiatAmount"`
	FiatCurrency    string    `gorm:"size:3;not null" json:"fiatCurrency"`
	Rate            string    `gorm:"type:numeric(38,10);not null" json:"rate"`
	PaymentMethodID uuid.UUID `gorm:"type:uuid;not null" json:"paymentMethodId"`
	OnChainTradeID  string    `gorm:"size:66;uniqueIndex" json:"onChainTradeId,omitempty"`
	OpenTxHash      string    `gorm:"size:66" json:"openTxHash,omitempty"`
	ReleaseTxHash   string    `gorm:"size:66" json:"releaseTxHash,omitempty"`
	Status          string    `gorm:"size:24;not null;default:'pending_payment';index" json:"status"`
	ProofImageURL   string    `gorm:"type:text" json:"proofImageUrl,omitempty"`
	// PM automation links (design §4). Empty on manual P2P trades.
	PaymentRef          string `gorm:"size:32" json:"paymentRef,omitempty"`          // optional buyer reference aid
	NombaInboundEventID string `gorm:"size:64" json:"nombaInboundEventId,omitempty"` // matched inbound webhook (buy)
	NombaPayoutID       string `gorm:"size:64" json:"nombaPayoutId,omitempty"`       // Nomba payout reference (sell)
	PayoutAccountNumber string `gorm:"size:32" json:"payoutAccountNumber,omitempty"` // user's KYC-name-verified bank (sell)
	PayoutBank          string `gorm:"size:64" json:"payoutBank,omitempty"`
	PayoutAccountName   string `gorm:"size:128" json:"payoutAccountName,omitempty"`
	CancelReason        string `gorm:"type:text" json:"cancelReason,omitempty"`
	// NgnReservationReleased records that this sell trade's PM NGN payout
	// reservation has ALREADY been returned to the PM's free balance (reserved_ngn
	// -= fiat) outside the normal terminal-cancel path — specifically by the
	// failed-payout handler (pm_automation_service.go), which releases the
	// reservation but deliberately leaves the trade in pending_payment for the
	// timeout watchdog. Without this marker the later cancel path would release a
	// SECOND time, double-decrementing reserved_ngn (stealing another live trade's
	// reservation, or hard-erroring and wedging the cancel). releasePMSellReservationTx
	// skips the release when this is true.
	NgnReservationReleased bool       `gorm:"not null;default:false" json:"-"`
	ResolvedByAdmin        *uuid.UUID `gorm:"type:uuid" json:"resolvedByAdmin,omitempty"`
	MarkedPaidAt           *time.Time `json:"markedPaidAt,omitempty"`
	ReleasedAt             *time.Time `json:"releasedAt,omitempty"`
	ExpiresAt              time.Time  `gorm:"index;not null" json:"expiresAt"`
	// ExpiredNotifiedAt — set by the timeout watchdog when it first detects
	// the trade has passed `expires_at`. Watchdog filters on `NULL` so the
	// expired-notification fires exactly once per trade.
	ExpiredNotifiedAt *time.Time `gorm:"index" json:"expiredNotifiedAt,omitempty"`
	// TSA Liquidity slice fields (spec 2026-10-08 §7.4, plan C1 P1). A slice is
	// one LP's escrow trade backing a pool order: ITIntentID names the order.
	// NULL on every ordinary trade. Never serialised: slices are hidden from
	// every user P2P endpoint (§23) and their internals are server-side only.
	ITIntentID *uuid.UUID `gorm:"type:uuid" json:"-"` // indexed by MigratePoolSliceIndexes (partial)
	// OpenedAt: the open was proven on-chain (ReadTrade, zero fee snapshot,
	// receipt 2 deep — §12.2 step 4).
	OpenedAt *time.Time `json:"-"`
	// OpenNonce is the conversion treasury's nonce for the open, written BEFORE
	// broadcast (S43): S44 reclaims an unlanded slice only once it is consumed.
	OpenNonce *int64 `json:"-"`
	// Generation is the order attempt that opened the slice (S41).
	Generation int `gorm:"not null;default:1" json:"-"`
	// DroppedAt: the order no longer needs this slice (S18 unselected, a failed
	// open, a deterministic park, an F21 resolve to the LP, or the canceller's
	// decision); the expiry canceller may free it on-chain (plan C1 P1).
	DroppedAt *time.Time `json:"-"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`

	// ── Virtual PM-detail fields (gorm:"-": serialized, never persisted) ───
	// Populated by the GetTrade handler when the trade's ad is a PM ad, so the
	// buy screen can render a "pay to this Nomba account" card + the buyer's
	// KYC name. Left zero/nil for normal (manual) P2P trades.
	IsPM      bool     `gorm:"-" json:"isPm"`
	PmPayTo   *PmPayTo `gorm:"-" json:"pmPayTo,omitempty"`
	PayerName string   `gorm:"-" json:"payerName,omitempty"`
}

// PmPayTo is the PM's static fiat destination shown to a buyer (virtual; not persisted).
type PmPayTo struct {
	AccountNumber string `json:"accountNumber"`
	Bank          string `json:"bank"`
	AccountName   string `json:"accountName"`
	// PayerInstruction is what the buyer must be told before they transfer.
	// Filled only when this card points at the shared company account, where
	// the buyer's own name is what attributes the credit; empty when it points
	// at a merchant's account, which a merchant confirms by hand instead.
	PayerInstruction string `json:"payerInstruction,omitempty"`
}

func (P2PTrade) TableName() string {
	return "p2p_trades"
}

// IsPoolSlice reports whether the trade is a TSA Liquidity slice.
func (t *P2PTrade) IsPoolSlice() bool { return t != nil && t.ITIntentID != nil }

// Funding discriminator values — keep in lock-step with P2PEscrowV2.sol's
// `enum Funding { PooledFromMaker, DepositedByTaker }`.
const (
	P2PTradeFundingPooled           uint8 = 0 // sell-ad: crypto from merchant's pool
	P2PTradeFundingDepositedByTaker uint8 = 1 // buy-ad: crypto deposited JIT by the user
)

const (
	P2PTradeStatusPendingPayment = "pending_payment"
	P2PTradeStatusPaid           = "paid"
	P2PTradeStatusReleasing      = "releasing"
	P2PTradeStatusReleased       = "released"
	P2PTradeStatusCancelled      = "cancelled"
	P2PTradeStatusDisputed       = "disputed"
	P2PTradeStatusResolved       = "resolved"
)

// P2PTradeLiveStatuses are the states in which a trade still has money moving:
// escrow locked, naira in flight, or a dispute open.
var P2PTradeLiveStatuses = []string{
	P2PTradeStatusPendingPayment,
	P2PTradeStatusPaid,
	P2PTradeStatusReleasing,
	P2PTradeStatusDisputed,
}

// P2PTradeValidNextStatuses returns valid transitions for the trade
// state-machine. Empty slice means terminal.
func P2PTradeValidNextStatuses(current string) []string {
	switch current {
	case P2PTradeStatusPendingPayment:
		return []string{P2PTradeStatusPaid, P2PTradeStatusCancelled}
	case P2PTradeStatusPaid:
		return []string{P2PTradeStatusReleasing, P2PTradeStatusDisputed}
	case P2PTradeStatusReleasing:
		// On release-tx failure the trade reverts to Paid so the seller can retry.
		return []string{P2PTradeStatusReleased, P2PTradeStatusPaid}
	case P2PTradeStatusDisputed:
		return []string{P2PTradeStatusResolved}
	case P2PTradeStatusReleased, P2PTradeStatusCancelled, P2PTradeStatusResolved:
		return []string{}
	default:
		return []string{}
	}
}
