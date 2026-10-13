package models

import (
	"time"

	"github.com/google/uuid"
)

// Key lane nonce allocation statuses.
const (
	KeyLaneNonceAllocated = "allocated" // reserved for one call; not yet passed by the chain's mined nonce
	KeyLaneNonceMined     = "mined"     // the chain's mined nonce has passed it
	KeyLaneNonceReleased  = "released"  // its only signing was definitely refused (never left): free for another call
)

// Key lane transaction statuses.
const (
	KeyLaneTxPending  = "pending"  // signed and broadcast (or about to be); outcome not yet known
	KeyLaneTxAccepted = "accepted" // the node accepted it; the allocation's current transaction
	KeyLaneTxReplaced = "replaced" // superseded by an accepted speed-up of the same call
	KeyLaneTxRejected = "rejected" // the node definitely refused it: it never left
	KeyLaneTxMined    = "mined"    // its nonce is mined (raw bytes dropped)
)

// KeyLaneNonce is the DB-authoritative allocation of one nonce of one hot key
// on one chain to ONE call (signer consolidation 2026-10-13): every process
// allocates under a per-(chain, address) advisory lock, the unique index makes
// a second call at an allocated nonce impossible, and a speed-up re-signs the
// same call (Fingerprint) under the same row.
type KeyLaneNonce struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	ChainID     int64     `gorm:"not null;uniqueIndex:uq_key_lane_nonces,priority:1" json:"chainId"`
	Address     string    `gorm:"size:42;not null;uniqueIndex:uq_key_lane_nonces,priority:2" json:"address"` // lower-case
	Nonce       int64     `gorm:"not null;uniqueIndex:uq_key_lane_nonces,priority:3" json:"nonce"`
	Fingerprint string    `gorm:"size:66;not null" json:"fingerprint"` // keccak256(to|data|value)
	Status      string    `gorm:"size:16;not null;index" json:"status"`
	Role        string    `gorm:"size:64" json:"role"`
	// AcceptedHash / AcceptedPrice: the last transaction for this call the node
	// accepted — what a speed-up bumps from, and where a receipt wait looks.
	AcceptedHash  string    `gorm:"size:66" json:"acceptedHash"`
	AcceptedPrice string    `gorm:"size:80" json:"acceptedPrice"`
	Bumps         int       `gorm:"not null;default:0" json:"bumps"`
	AcceptedBumps int       `gorm:"not null;default:0" json:"acceptedBumps"` // Bumps when the last signing was accepted
	Rebroadcasts  int       `gorm:"not null;default:0" json:"rebroadcasts"`
	ActedAt       time.Time `json:"actedAt"` // the last send, re-broadcast or speed-up
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (KeyLaneNonce) TableName() string { return "key_lane_nonces" }

// KeyLaneTx is one signing of an allocation's call, written BEFORE it is
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
