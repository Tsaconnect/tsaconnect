package models

import (
	"time"

	"github.com/google/uuid"
)

// SP6 root-publisher models (fee/TP overhaul). SP5 produces a per-user
// CUMULATIVE claimable-MCGP balance (reward_claimables.claimable_mcgp, a
// float). SP6 turns the currently-ELIGIBLE (SP7) subset of those balances into
// an on-chain CUMULATIVE merkle root that the MCGPRewardDistributor publishes;
// users then claim (cumulative − already-claimed) MCGP with an app-signed tx +
// merkle proof.
//
// Two tables:
//
//   - RewardDistribution      — one row per published epoch. Stores the merkle
//                               root, the distributor address it targets, the
//                               leaf count, and the total cumulative base units
//                               in the tree. Append-only; the latest published
//                               row is the live root.
//   - RewardDistributionEntry — one row per (epoch, user). Stores the user's
//                               CUMULATIVE amount in MCGP BASE UNITS (18-dec) as
//                               a decimal STRING (never a float — the leaf is
//                               built from this exact big.Int) plus the leaf hex
//                               and the JSON-encoded merkle proof, so the user
//                               claim-data endpoint is a single indexed read and
//                               re-publishing from the same inputs is stable.
//
// BASE-UNIT DETERMINISM (the money-critical rule): the leaf amount is the
// canonical CumulativeBaseUnits string on the entry. It is derived from the
// SP5 float claimable via a single documented FLOOR to 18-decimal base units
// (see reward_merkle_service.go floorToBaseUnits) and then made MONOTONIC
// against the user's previous published cumulative (an epoch can only ever grow
// a user's cumulative, never shrink it — matching the contract's
// cumulativeAmount > claimed guard). Persisting the exact base-units string
// means a re-publish from identical claimables reproduces the identical root.
//
// Additive + dormant until an admin publishes the first epoch.

// RewardDistributionStatus values.
const (
	// RewardDistributionStatusPending is a freshly-built epoch whose root has
	// not yet been confirmed on-chain (the admin still has to sign+broadcast the
	// setMerkleRoot tx). The latest row — pending or published — is the live
	// claim source; pending vs published only records on-chain confirmation.
	RewardDistributionStatusPending = "pending"
	// RewardDistributionStatusPublished marks an epoch whose setMerkleRoot tx an
	// admin has confirmed on-chain (recorded via the publish-confirm path).
	RewardDistributionStatusPublished = "published"
)

// RewardDistribution is one published merkle epoch.
type RewardDistribution struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`

	// Epoch is a monotonically increasing epoch number (1, 2, 3 …). The latest
	// epoch is the live root.
	Epoch int64 `gorm:"not null;uniqueIndex:idx_reward_dist_epoch" json:"epoch"`

	// MerkleRoot is the 0x-prefixed root hex the admin publishes via
	// setMerkleRoot(root). Encodes CUMULATIVE per-account base-unit amounts.
	MerkleRoot string `gorm:"size:66;not null" json:"merkleRoot"`

	// DistributorAddress is the MCGPRewardDistributor this root targets (the
	// contract the user's claim tx is sent to). Recorded per-epoch so a later
	// re-deploy doesn't retro-point old epochs at a new contract.
	DistributorAddress string `gorm:"size:42;not null" json:"distributorAddress"`

	// ChainID is the chain the distributor is deployed on (the chainId stamped
	// into the user's claim tx).
	ChainID int64 `gorm:"not null;default:0" json:"chainId"`

	// LeafCount is how many eligible users are in this tree.
	LeafCount int `gorm:"not null;default:0" json:"leafCount"`

	// TotalBaseUnits is the sum of every leaf's cumulative base units, as a
	// decimal string (the tree's total cumulative MCGP owed in base units).
	TotalBaseUnits string `gorm:"size:80;not null;default:'0'" json:"totalBaseUnits"`

	// Status is RewardDistributionStatusPending or …Published.
	Status string `gorm:"size:16;not null;default:'pending'" json:"status"`

	// PublishedBy is the admin who built (and will sign) this epoch.
	PublishedBy *uuid.UUID `gorm:"type:uuid" json:"publishedBy,omitempty"`

	// TxHash is the setMerkleRoot tx hash once the admin confirms it on-chain.
	TxHash string `gorm:"size:66" json:"txHash,omitempty"`

	CreatedAt time.Time `gorm:"not null;index:idx_reward_dist_created" json:"createdAt"`
	UpdatedAt time.Time `gorm:"not null" json:"updatedAt"`
}

// TableName overrides the default table name.
func (RewardDistribution) TableName() string {
	return "reward_distributions"
}

// RewardDistributionEntry is one (epoch, user) leaf: the user's cumulative
// base-unit amount, its leaf hash, and its merkle proof.
type RewardDistributionEntry struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`

	// DistributionID points at the parent RewardDistribution (epoch).
	DistributionID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_reward_dist_entry,priority:1;index:idx_reward_dist_entry_dist" json:"distributionId"`

	// Epoch denormalizes the parent epoch for cheap "latest epoch for user"
	// lookups without a join.
	Epoch int64 `gorm:"not null;index:idx_reward_dist_entry_user_epoch,priority:2" json:"epoch"`

	// UserID is the reward recipient.
	UserID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_reward_dist_entry,priority:2;index:idx_reward_dist_entry_user_epoch,priority:1" json:"userId"`

	// Account is the user's on-chain wallet address (lowercased 0x hex). The
	// leaf is bound to THIS address — the user's claim tx must originate the
	// proof against it. A user with no wallet address is excluded from the tree
	// (no address ⇒ no claim possible).
	Account string `gorm:"size:42;not null" json:"account"`

	// CumulativeBaseUnits is the user's CUMULATIVE claimable MCGP in BASE UNITS
	// (18 decimals) as a decimal STRING — the EXACT big.Int the leaf was built
	// from. Never a float. Monotonic non-decreasing across epochs.
	CumulativeBaseUnits string `gorm:"size:80;not null" json:"cumulativeBaseUnits"`

	// Leaf is the 0x-prefixed double-hashed leaf for (Account, CumulativeBaseUnits).
	Leaf string `gorm:"size:66;not null" json:"leaf"`

	// Proof is the JSON array of 0x-prefixed sibling hashes proving Leaf against
	// the epoch's MerkleRoot.
	Proof string `gorm:"type:text;not null" json:"proof"`

	CreatedAt time.Time `gorm:"not null" json:"createdAt"`
}

// TableName overrides the default table name.
func (RewardDistributionEntry) TableName() string {
	return "reward_distribution_entries"
}
