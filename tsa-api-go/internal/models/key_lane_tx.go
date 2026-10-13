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
