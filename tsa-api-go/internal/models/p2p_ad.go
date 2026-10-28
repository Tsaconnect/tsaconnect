package models

import (
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// OptionalNumeric is a string-backed value for Postgres `numeric` columns
// that are semantically optional. A plain `string` Go field can't represent
// "absent" — an empty string serializes as an empty SQL string literal, which the
// numeric type rejects with `invalid input syntax for type numeric: ""`.
// This Valuer maps `""` → NULL on write, and the Scanner maps NULL → `""`
// on read, so the rest of the codebase keeps treating the value as a plain
// (possibly empty) decimal string.
type OptionalNumeric string

// Value implements driver.Valuer.
func (n OptionalNumeric) Value() (driver.Value, error) {
	if n == "" {
		return nil, nil
	}
	return string(n), nil
}

// Scan implements sql.Scanner. NULL becomes the empty string so call sites
// can keep using `if val != "" {…}` to mean "limit set".
func (n *OptionalNumeric) Scan(value any) error {
	if value == nil {
		*n = ""
		return nil
	}
	switch v := value.(type) {
	case string:
		*n = OptionalNumeric(v)
	case []byte:
		*n = OptionalNumeric(string(v))
	default:
		return fmt.Errorf("OptionalNumeric: unsupported scan type %T", value)
	}
	return nil
}

// P2PAd represents a seller's offer to sell `Amount` of `Token` (Phase 1: sell
// ads only). Under the deposit-once / EIP-712 voucher model the ad lives
// entirely off-chain:
//
//	pending  → active (seller's voucher signature stored)
//	active   → paused | exhausted (remaining == 0) | cancelled (on-chain
//	           cancelVoucher tx)
//	paused   ↔ active
//	cancelled is terminal
//
// Voucher identity is the EIP-712 digest computed from
// (seller, token, maxAmount, minPerTrade, maxPerTrade, deadline, nonce). The
// nonce is server-generated at creation; the hash is recomputed by the
// contract on every openTrade so the backend never authoritatively asserts it.
// VoucherSignature is the seller's 65-byte EIP-712 sig, captured once and
// replayed on every buyer openTrade call.
//
// Rate is the fiat-per-token unit (e.g. NGN per USDT). For fixed-rate ads
// (RateType == 'fixed') Rate is the seller's literal quote, locked at ad
// creation and inherited by every trade opened against this ad — this avoids
// per-trade rate disputes.
//
// For float-rate ads (RateType == 'float') Rate is the most-recently-computed
// effective rate, used purely as a display cache for the browse list. The
// authoritative price at trade open is recomputed live from the reference
// feed via FloatAnchor + FloatOffsetBps; the live value is then snapshotted
// onto the P2PTrade.Rate column at open so in-flight trades are immune to
// subsequent feed movement.
//
// Token amounts are stored as strings (numeric in DB) so we never lose
// precision on USDT (6 dec), USDC (6 dec), or MCGP (18 dec).
type P2PAd struct {
	ID       uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	SellerID uuid.UUID `gorm:"type:uuid;not null;index" json:"sellerId"`
	Side     string    `gorm:"size:8;not null;index;default:'sell'" json:"side"`
	Token    string    `gorm:"size:16;not null;index" json:"token"`
	ChainID  int64     `gorm:"not null;default:146" json:"chainId"`
	// EscrowAddress is the escrow the ad's voucher is signed for (vouchers are bound to one
	// escrow). Empty: the chain's legacy escrow.
	EscrowAddress    string                      `gorm:"size:42" json:"escrowAddress,omitempty"`
	Amount           string                      `gorm:"type:numeric(78,0);not null" json:"amount"`
	Remaining        string                      `gorm:"type:numeric(78,0);not null" json:"remaining"`
	Reserved         string                      `gorm:"type:numeric(78,0);not null;default:'0'" json:"reserved"`
	FiatCurrency     string                      `gorm:"size:3;not null;index" json:"fiatCurrency"`
	Rate             string                      `gorm:"type:numeric(38,10);not null" json:"rate"`
	RateType         string                      `gorm:"size:8;not null;default:'fixed';index" json:"rateType"`
	FloatOffsetBps   *int                        `json:"floatOffsetBps,omitempty"`
	FloatAnchor      string                      `gorm:"size:16;not null;default:'bybit_sell'" json:"floatAnchor"`
	MinLimit         OptionalNumeric             `gorm:"type:numeric(78,0)" json:"minLimit,omitempty"`
	MaxLimit         OptionalNumeric             `gorm:"type:numeric(78,0)" json:"maxLimit,omitempty"`
	PaymentMethodIDs datatypes.JSONSlice[string] `gorm:"type:jsonb;not null" json:"paymentMethodIds"`
	Terms            string                      `gorm:"type:text" json:"terms,omitempty"`
	// ── On-chain identity (post-upgrade: voucher-based) ────────────────────
	// SellerAddress is the EVM wallet that signed (or will sign) this ad's
	// voucher. Captured at create-ad time so the voucher payload is fully
	// reconstructable from the ad row without a User lookup.
	SellerAddress string `gorm:"size:42;index" json:"sellerAddress,omitempty"`
	// Server-only (VoucherNonce, VoucherDeadline, VoucherSignature): never
	// serialised — a leaked sig lets anyone call openTrade directly.
	// VoucherNonce is the server-generated bytes32 used as the EIP-712 nonce
	// field. Doubles as a stable off-chain ad id for clients. Stored hex
	// with 0x prefix.
	VoucherNonce string `gorm:"size:66;index" json:"-"`
	// VoucherDeadline is the unix-seconds expiry encoded in the signed voucher.
	// Buyers calling openTrade past this deadline are rejected by the contract.
	VoucherDeadline *time.Time `gorm:"index" json:"-"`
	// VoucherHash is the EIP-712 digest the seller signs (and the contract
	// recomputes on every openTrade). Server-side convenience for indexing;
	// the contract is the source of truth.
	VoucherHash string `gorm:"size:66;index" json:"voucherHash,omitempty"`
	// VoucherSignature is the seller's 65-byte signature over VoucherHash,
	// captured once at ad activation. Stored hex with 0x prefix. Empty for
	// pending (unsigned) ads.
	VoucherSignature string `gorm:"size:132" json:"-"`

	// ── Legacy on-chain identity (pre-upgrade lock-on-publish; retained
	//    only for old rows during migration). New ads do not populate these.
	OnChainAdID  string `gorm:"size:66" json:"onChainAdId,omitempty"`
	EscrowTxHash string `gorm:"size:66;index" json:"escrowTxHash,omitempty"`

	// PMID links the ad to a PaymentMerchant when set; nil = a normal (manual)
	// P2P ad. This is the marketplace "PM"/automated filter key (design §4).
	PMID *uuid.UUID `gorm:"type:uuid;index" json:"pmId,omitempty"`

	Status    string     `gorm:"size:16;not null;default:'pending';index" json:"status"`
	ExpiresAt *time.Time `gorm:"index" json:"expiresAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	DeletedAt *time.Time `gorm:"index" json:"deletedAt,omitempty"`

	// FeeBps is the live on-chain fee a trade on this ad would snapshot (virtual,
	// never persisted). Absent when the chain could not be read.
	FeeBps *int `gorm:"-" json:"feeBps,omitempty"`
}

func (P2PAd) TableName() string {
	return "p2p_ads"
}

const (
	P2PAdSideSell = "sell"
	P2PAdSideBuy  = "buy"

	P2PAdStatusPending   = "pending"
	P2PAdStatusActive    = "active"
	P2PAdStatusPaused    = "paused"
	P2PAdStatusExhausted = "exhausted"
	P2PAdStatusCancelled = "cancelled"

	P2PAdRateTypeFixed = "fixed"
	P2PAdRateTypeFloat = "float"

	P2PAdFloatAnchorBybitSell = "bybit_sell"
)
