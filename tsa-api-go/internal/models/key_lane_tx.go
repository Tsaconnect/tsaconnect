package models

import (
	"time"

	"github.com/google/uuid"
)

// Key lane nonce allocation statuses (an outbox: signed and committed first,
// broadcast after the commit, outcome recorded after that). A committed
// allocation IS its nonce's transaction until the chain mines that nonce:
// there is no state in which it is given up or replaced by another call.
const (
	KeyLaneNonceSigned = "signed" // committed with its signing; no broadcast of it accepted yet
	KeyLaneNonceSent   = "sent"   // broadcast at least once (accepted, or the outcome unknown)
	KeyLaneNonceMined  = "mined"  // the chain's mined nonce has passed it
	// KeyLaneNonceCancelled: the nonce mined the lane's 0-value self-transfer
	// (a CANCEL of a call that could never be sent), so the call provably
	// never executed. Its operation key is freed.
	KeyLaneNonceCancelled = "cancelled"
)

// Key lane signing statuses.
const (
	KeyLaneTxSigned    = "signed"    // committed before its broadcast
	KeyLaneTxAccepted  = "accepted"  // the node accepted it: the allocation's current signing
	KeyLaneTxUncertain = "uncertain" // the broadcast's outcome is unknown
	KeyLaneTxReplaced  = "replaced"  // superseded by an accepted speed-up of the same call
	KeyLaneTxRefused   = "refused"   // the node definitely refused it: those bytes never left
	KeyLaneTxMined     = "mined"     // its nonce is mined (raw bytes dropped)
)

// KeyLaneNonce is the DB-authoritative allocation of one nonce of one hot key
// on one chain to ONE call (signer consolidation 2026-10-13): allocated,
// signed and committed under a per-(chain, address) advisory lock before any
// broadcast; the unique (chain, address, nonce) makes a second call at a
// nonce impossible, and a nonce is never released to another call. OpKey is
// the caller's operation key: a caller retrying the same operation gets this
// allocation back, never a new nonce.
type KeyLaneNonce struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	ChainID     int64     `gorm:"not null;uniqueIndex:uq_key_lane_nonces,priority:1;uniqueIndex:uq_key_lane_nonces_op,priority:1,where:op_key <> ''" json:"chainId"`
	Address     string    `gorm:"size:42;not null;uniqueIndex:uq_key_lane_nonces,priority:2;uniqueIndex:uq_key_lane_nonces_op,priority:2,where:op_key <> ''" json:"address"` // lower-case
	Nonce       int64     `gorm:"not null;uniqueIndex:uq_key_lane_nonces,priority:3" json:"nonce"`
	OpKey       string    `gorm:"size:200;not null;default:'';uniqueIndex:uq_key_lane_nonces_op,priority:3,where:op_key <> ''" json:"opKey"`
	Fingerprint string    `gorm:"size:66;not null" json:"fingerprint"` // keccak256(to|data|value)
	Status      string    `gorm:"size:16;not null;index" json:"status"`
	Role        string    `gorm:"size:64" json:"role"`
	// Version is bumped by every update; an update names the version it read
	// (and never touches a mined row), so a stale copy can never be written
	// back over newer state.
	Version int64 `gorm:"not null;default:0" json:"version"`
	// Cancel: the call can never be sent (refused permanently, or a
	// super-admin asked): the upkeep replaces it at its nonce with a 0-value
	// self-transfer. Once the nonce mines it resolves to cancelled or mined.
	Cancel bool `gorm:"not null;default:false" json:"cancel"`
	// Reserved: an owed call (ReservedSend): its fee is paid from the lane's
	// reservation, so its speed-ups are gated as the send was.
	Reserved bool `gorm:"not null;default:false" json:"reserved"`
	// PriceCap, when set, is a super-admin's per-allocation ceiling on a
	// speed-up's gas price (wei), replacing the default cap (audited).
	PriceCap string `gorm:"size:80" json:"priceCap"`
	// AcceptedHash / AcceptedPrice: the last signing of this call the node
	// accepted — what a speed-up steps from.
	AcceptedHash  string    `gorm:"size:66" json:"acceptedHash"`
	AcceptedPrice string    `gorm:"size:80" json:"acceptedPrice"`
	Bumps         int       `gorm:"not null;default:0" json:"bumps"`
	AcceptedBumps int       `gorm:"not null;default:0" json:"acceptedBumps"` // Bumps when the last signing was accepted
	BumpLimit     int       `gorm:"not null;default:3" json:"bumpLimit"`     // raised by a super-admin retry
	Rebroadcasts  int       `gorm:"not null;default:0" json:"rebroadcasts"`
	ActedAt       time.Time `json:"actedAt"` // the last signing, re-broadcast or speed-up
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (KeyLaneNonce) TableName() string { return "key_lane_nonces" }

// KeyLaneTx is one signing of an allocation's call, committed BEFORE it is
// broadcast. RawTx is kept while the nonce is unmined.
type KeyLaneTx struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	AllocationID uuid.UUID `gorm:"type:uuid;index" json:"allocationId"`
	ChainID      int64     `gorm:"not null;uniqueIndex:uq_key_lane_txs_nonce_hash,priority:1" json:"chainId"`
	Address      string    `gorm:"size:42;not null;uniqueIndex:uq_key_lane_txs_nonce_hash,priority:2" json:"address"`
	Nonce        int64     `gorm:"not null;uniqueIndex:uq_key_lane_txs_nonce_hash,priority:3" json:"nonce"`
	Hash         string    `gorm:"size:66;not null;uniqueIndex:uq_key_lane_txs_nonce_hash,priority:4;index" json:"hash"`
	RawTx        []byte    `json:"-"`
	To           string    `gorm:"size:42" json:"to"`
	Data         string    `gorm:"type:text" json:"data"`
	Value        string    `gorm:"size:80" json:"value"`
	GasLimit     int64     `json:"gasLimit"`
	GasPrice     string    `gorm:"size:80" json:"gasPrice"`
	Status       string    `gorm:"size:16;not null;index" json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func (KeyLaneTx) TableName() string { return "key_lane_txs" }

// KeyLaneState is one lane's shared state: its hold. Every process reads it
// (in the allocation transaction and on each watch tick), so a hold or an
// admin's clear on one process applies to all of them.
type KeyLaneState struct {
	ChainID    int64      `gorm:"primaryKey;autoIncrement:false" json:"chainId"`
	Address    string     `gorm:"primaryKey;size:42" json:"address"` // lower-case
	HeldNonce  *int64     `json:"heldNonce,omitempty"`
	HeldReason string     `gorm:"size:256" json:"heldReason,omitempty"`
	HeldSince  *time.Time `json:"heldSince,omitempty"`
	Version    int64      `gorm:"not null;default:0" json:"version"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

func (KeyLaneState) TableName() string { return "key_lane_states" }

// KeyLaneRetiredOp is a tombstone: an operation key the caller gave up on,
// proven (under the lane lock) never to have been allocated. Any later
// allocation for it is refused, so a slow or concurrent attempt of the
// retired operation can never be sent after the caller moved on.
type KeyLaneRetiredOp struct {
	ChainID   int64     `gorm:"primaryKey;autoIncrement:false" json:"chainId"`
	Address   string    `gorm:"primaryKey;size:42" json:"address"` // lower-case
	OpKey     string    `gorm:"primaryKey;size:200" json:"opKey"`
	CreatedAt time.Time `json:"createdAt"`
}

func (KeyLaneRetiredOp) TableName() string { return "key_lane_retired_ops" }
