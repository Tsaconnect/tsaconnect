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
//   None, Pending, Paid, Released, Cancelled, Disputed, Resolved
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
	ID              uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	AdID            uuid.UUID  `gorm:"type:uuid;not null;index" json:"adId"`
	BuyerID         uuid.UUID  `gorm:"type:uuid;not null;index" json:"buyerId"`
	SellerID        uuid.UUID  `gorm:"type:uuid;not null;index" json:"sellerId"`
	Token           string     `gorm:"size:16;not null" json:"token"`
	ChainID         int64      `gorm:"not null" json:"chainId"`
	// Funding discriminates the two-sided escrow flow. Pooled (0) = sell-ad:
	// the crypto comes from the merchant's deposited on-chain pool. Deposited-
	// by-taker (1) = buy-ad: the user deposits crypto just-in-time at
	// openTradeWithDeposit and there is no on-chain pool to credit on refund —
	// the crypto returns to the user's wallet. Mirrors P2PEscrowV2.sol's
	// `Funding` enum { PooledFromMaker, DepositedByTaker }.
	Funding         uint8      `gorm:"not null;default:0" json:"funding"`
	TokenAmount     string     `gorm:"type:numeric(78,0);not null" json:"tokenAmount"`
	FiatAmount      string     `gorm:"type:numeric(38,2);not null" json:"fiatAmount"`
	FiatCurrency    string     `gorm:"size:3;not null" json:"fiatCurrency"`
	Rate            string     `gorm:"type:numeric(38,10);not null" json:"rate"`
	PaymentMethodID uuid.UUID  `gorm:"type:uuid;not null" json:"paymentMethodId"`
	OnChainTradeID  string     `gorm:"size:66;uniqueIndex" json:"onChainTradeId,omitempty"`
	OpenTxHash      string     `gorm:"size:66" json:"openTxHash,omitempty"`
	ReleaseTxHash   string     `gorm:"size:66" json:"releaseTxHash,omitempty"`
	Status          string     `gorm:"size:24;not null;default:'pending_payment';index" json:"status"`
	ProofImageURL   string     `gorm:"type:text" json:"proofImageUrl,omitempty"`
	CancelReason    string     `gorm:"type:text" json:"cancelReason,omitempty"`
	ResolvedByAdmin *uuid.UUID `gorm:"type:uuid" json:"resolvedByAdmin,omitempty"`
	MarkedPaidAt    *time.Time `json:"markedPaidAt,omitempty"`
	ReleasedAt      *time.Time `json:"releasedAt,omitempty"`
	ExpiresAt       time.Time  `gorm:"index;not null" json:"expiresAt"`
	// ExpiredNotifiedAt — set by the timeout watchdog when it first detects
	// the trade has passed `expires_at`. Watchdog filters on `NULL` so the
	// expired-notification fires exactly once per trade.
	ExpiredNotifiedAt *time.Time `gorm:"index" json:"expiredNotifiedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

func (P2PTrade) TableName() string {
	return "p2p_trades"
}

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
