package models

import (
	"time"

	"github.com/google/uuid"
)

// Key lane transaction statuses.
const (
	KeyLaneTxPending  = "pending"  // signed and (about to be) broadcast; the chain has not mined its nonce
	KeyLaneTxMined    = "mined"    // the chain's mined nonce has passed it
	KeyLaneTxReplaced = "replaced" // the same call was re-signed at a higher fee (a speed-up)
	KeyLaneTxFilled   = "filled"   // a super-admin's gap-filling self-transfer
)

// KeyLaneTx is one transaction a hot key's send lane signed (signer
// consolidation 2026-10-13), written BEFORE it is broadcast, so a process that
// restarts mid-send knows which call every nonce it used carries: a nonce is
// only ever re-sent with that same call (byte-identical, or re-signed at a
// higher fee), never with a different one. RawTx is kept while unmined and
// cleared once the chain's mined nonce passes the row.
type KeyLaneTx struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	ChainID   int64     `gorm:"not null;uniqueIndex:uq_key_lane_txs_nonce_hash,priority:1;index:idx_key_lane_txs_lane,priority:1" json:"chainId"`
	Address   string    `gorm:"size:42;not null;uniqueIndex:uq_key_lane_txs_nonce_hash,priority:2;index:idx_key_lane_txs_lane,priority:2" json:"address"` // lower-case
	Nonce     int64     `gorm:"not null;uniqueIndex:uq_key_lane_txs_nonce_hash,priority:3;index:idx_key_lane_txs_lane,priority:3" json:"nonce"`
	Hash      string    `gorm:"size:66;not null;uniqueIndex:uq_key_lane_txs_nonce_hash,priority:4" json:"hash"`
	RawTx     []byte    `json:"-"`
	To        string    `gorm:"size:42" json:"to"`
	Data      string    `gorm:"type:text" json:"data"`
	Value     string    `gorm:"size:80" json:"value"`
	GasLimit  int64     `json:"gasLimit"`
	GasPrice  string    `gorm:"size:80" json:"gasPrice"`
	Status    string    `gorm:"size:16;not null;index" json:"status"`
	Role      string    `gorm:"size:64" json:"role"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (KeyLaneTx) TableName() string { return "key_lane_txs" }
