package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// RewardMerkleService is the SP6 root-publisher. It turns the currently-ELIGIBLE
// (SP7) subset of SP5's per-user claimable-MCGP balances into a CUMULATIVE
// merkle distribution that the MCGPRewardDistributor publishes on-chain:
//
//	leaf   = keccak256(bytes.concat(keccak256(abi.encode(address account, uint256 cumulativeAmount))))
//	parent = keccak256(sortedPair(left, right))   // OZ sorted-pair (lexicographic 32-byte compare)
//
// This leaf encoding + parent hashing is BYTE-IDENTICAL to
// smart-contracts/contracts/MCGPRewardDistributor.sol (asserted against the
// contract's published test vector in reward_merkle_service_test.go). If it
// drifts, every on-chain claim fails MerkleProof.verify.
//
// It holds no on-chain key: BuildDistribution computes + persists the tree (DB
// only); the admin handler returns the new root + an UNSIGNED setMerkleRoot tx
// for the admin to sign+broadcast (mirroring the P2P prepare-unsigned-tx
// pattern). The service never broadcasts.
type RewardMerkleService struct {
	db   *gorm.DB
	elig claimEligibilityChecker

	// distributorAddress is the MCGPRewardDistributor the published roots target
	// (env REWARD_DISTRIBUTOR_ADDRESS). Empty ⇒ feature dormant; BuildDistribution
	// and the claim-data endpoint surface a clear error / empty result.
	distributorAddress string

	// chainID is the chain the distributor is deployed on (stamped into the
	// user's claim tx and the unsigned setMerkleRoot tx).
	chainID int64

	// claimedReader optionally reads the distributor's on-chain claimed[account].
	// When wired (production, via SetClaimedReader), ClaimDataForUser /
	// PrepareClaimForUser gate Included/unsignedTx on REMAINING claimable
	// (cumulativeAmount > claimed[account]) so a fully-claimed user is no longer
	// handed a tx that would revert "MCGP: nothing to claim". nil (tests / no RPC)
	// ⇒ membership-only gating, the prior behavior. A read failure is treated as
	// "assume claimable" (fail-open) so an RPC blip never hides a real claim.
	claimedReader claimedOfReader
}

// SetClaimedReader wires the optional on-chain claimed[account] reader so claim
// data is gated on remaining-claimable (cumulative − claimed), not just tree
// membership. Additive: leaving it unset preserves the membership-only behavior.
// nil-safe; a typed-nil reader is normalized to "unset".
func (s *RewardMerkleService) SetClaimedReader(reader claimedOfReader) {
	if s == nil {
		return
	}
	if reader == nil || isNilClaimedReader(reader) {
		s.claimedReader = nil
		return
	}
	s.claimedReader = reader
}

// claimEligibilityChecker is the SP7 surface SP6 needs: decide who may be
// included in a published root. An interface keeps the merkle builder
// unit-testable without the full referral graph.
type claimEligibilityChecker interface {
	IsClaimEligible(ctx context.Context, userID uuid.UUID) (bool, []string, error)
}

// mcgpBaseUnitDecimals is MCGP's 18-decimal fixed-point scale. The leaf amount
// is the claimable MCGP in base units (1 MCGP == 10^18 base units).
const mcgpBaseUnitDecimals = 18

// NewRewardMerkleService builds the SP6 root-publisher. distributorAddress and
// chainID come from cfg in main.go; an empty distributorAddress leaves the
// feature dormant (BuildDistribution errors clearly, claim-data returns empty).
func NewRewardMerkleService(db *gorm.DB, elig claimEligibilityChecker, distributorAddress string, chainID int64) *RewardMerkleService {
	return &RewardMerkleService{
		db:                 db,
		elig:               elig,
		distributorAddress: strings.TrimSpace(distributorAddress),
		chainID:            chainID,
	}
}

// DistributorAddress returns the configured distributor contract address (empty
// when the feature isn't deployed — handlers treat empty as "unavailable").
func (s *RewardMerkleService) DistributorAddress() string {
	return s.distributorAddress
}

// ChainID returns the chain the distributor is deployed on.
func (s *RewardMerkleService) ChainID() int64 { return s.chainID }

// IsConfigured reports whether a distributor address is set.
func (s *RewardMerkleService) IsConfigured() bool {
	return s != nil && s.distributorAddress != ""
}

// ─── Leaf + tree primitives (byte-identical to MCGPRewardDistributor.sol) ─────

// leafAbiArgs is the abi.encode(address, uint256) tuple used for the leaf
// pre-image. Built once.
var leafAbiArgs = func() abi.Arguments {
	addrTy, err := abi.NewType("address", "", nil)
	if err != nil {
		panic(fmt.Sprintf("reward merkle: address abi type: %v", err))
	}
	uintTy, err := abi.NewType("uint256", "", nil)
	if err != nil {
		panic(fmt.Sprintf("reward merkle: uint256 abi type: %v", err))
	}
	return abi.Arguments{{Type: addrTy}, {Type: uintTy}}
}()

// RewardLeaf computes the OpenZeppelin double-hashed, second-preimage-safe leaf
// for (account, cumulativeBaseUnits):
//
//	leaf = keccak256(bytes.concat(keccak256(abi.encode(account, amount))))
//
// abi.encode here is the 32-byte left-padded address followed by the 32-byte
// big-endian amount (64 bytes), keccak'd, then keccak'd again. amount is MCGP in
// BASE UNITS (18 decimals) as a big.Int — never a float. MUST stay byte-identical
// to MCGPRewardDistributor.leafHash (verified in tests against the contract's
// published vector).
func RewardLeaf(account common.Address, amount *big.Int) [32]byte {
	enc, err := leafAbiArgs.Pack(account, amount)
	if err != nil {
		// Packing an address + non-negative *big.Int cannot fail; treat as a
		// programmer error rather than returning an error from a pure helper.
		panic(fmt.Sprintf("reward merkle: pack leaf (account=%s amount=%s): %v", account.Hex(), amount, err))
	}
	inner := crypto.Keccak256(enc)
	var out [32]byte
	copy(out[:], crypto.Keccak256(inner))
	return out
}

// hashMerklePair is the OZ sorted-pair parent hash: keccak256 of the two 32-byte
// children concatenated in ascending lexicographic byte order. Because both
// inputs are fixed 32-byte arrays, a Go byte-slice compare is identical to the
// contract's Buffer/word compare.
func hashMerklePair(a, b [32]byte) [32]byte {
	var out [32]byte
	if bytesLE(a, b) {
		copy(out[:], crypto.Keccak256(a[:], b[:]))
	} else {
		copy(out[:], crypto.Keccak256(b[:], a[:]))
	}
	return out
}

// bytesLE reports whether a <= b in lexicographic (big-endian) byte order.
func bytesLE(a, b [32]byte) bool {
	for i := 0; i < 32; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return true // equal
}

// merkleTree holds the layered tree so per-leaf proofs can be extracted. Layer 0
// is the leaves (in insertion order); the last layer is the single root.
type merkleTree struct {
	layers [][][32]byte
}

// buildMerkleTree constructs an OZ-style tree over leaves (in order). Odd nodes
// are promoted unchanged to the next layer (carry-up), matching the proof shape
// the contract's MerkleProof.verify expects with sorted pairs. Panics on an
// empty leaf set — callers must guard (a no-eligible-users epoch is rejected
// before this is called).
func buildMerkleTree(leaves [][32]byte) *merkleTree {
	if len(leaves) == 0 {
		panic("reward merkle: empty tree")
	}
	layers := [][][32]byte{append([][32]byte(nil), leaves...)}
	for len(layers[len(layers)-1]) > 1 {
		prev := layers[len(layers)-1]
		next := make([][32]byte, 0, (len(prev)+1)/2)
		for i := 0; i < len(prev); i += 2 {
			if i+1 < len(prev) {
				next = append(next, hashMerklePair(prev[i], prev[i+1]))
			} else {
				next = append(next, prev[i]) // carry the odd node up
			}
		}
		layers = append(layers, next)
	}
	return &merkleTree{layers: layers}
}

// Root returns the tree root.
func (t *merkleTree) Root() [32]byte {
	last := t.layers[len(t.layers)-1]
	return last[0]
}

// Proof returns the sibling hashes proving the leaf at leafIndex against the
// root, skipping carried-up odd nodes (matching the contract's proof shape).
func (t *merkleTree) Proof(leafIndex int) [][32]byte {
	var proof [][32]byte
	idx := leafIndex
	for l := 0; l < len(t.layers)-1; l++ {
		layer := t.layers[l]
		isRight := idx%2 == 1
		pairIdx := idx + 1
		if isRight {
			pairIdx = idx - 1
		}
		if pairIdx < len(layer) {
			proof = append(proof, layer[pairIdx])
		}
		idx = idx / 2
	}
	return proof
}

// ─── Base-unit conversion (the money-critical floor) ─────────────────────────

// floorToBaseUnits converts a claimable MCGP float into 18-decimal BASE UNITS as
// a big.Int, FLOORED (truncated toward zero). This is the single documented
// conversion the leaf is built from; it must be deterministic so re-publishing
// from the same claimable yields the same base units (and thus the same root).
//
// We go through big.Float at high precision and floor, rather than float64
// arithmetic, so the conversion is independent of float64 rounding of mcgp*1e18.
// A non-positive or non-finite claimable yields 0. Flooring (never rounding up)
// guarantees we never publish MORE than the user has actually accrued — the
// treasury can always cover a claim.
func floorToBaseUnits(mcgp float64) *big.Int {
	if mcgp <= 0 || isNaNOrInf(mcgp) {
		return big.NewInt(0)
	}
	// 256 bits of mantissa is ample for any realistic MCGP balance * 1e18.
	bf := new(big.Float).SetPrec(256).SetFloat64(mcgp)
	scale := new(big.Float).SetPrec(256).SetInt(baseUnitScale())
	bf.Mul(bf, scale)
	// Floor: Int() truncates toward zero, and the product is non-negative here.
	out, _ := bf.Int(nil)
	if out == nil {
		return big.NewInt(0)
	}
	return out
}

// baseUnitScale returns 10^18 as a big.Int (computed once).
var baseUnitScale = func() func() *big.Int {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(mcgpBaseUnitDecimals), nil)
	return func() *big.Int { return new(big.Int).Set(scale) }
}()

// isNaNOrInf reports whether f is NaN or ±Inf without importing math at call
// sites (keeps floorToBaseUnits self-contained).
func isNaNOrInf(f float64) bool {
	return f != f || f > 1e308 || f < -1e308
}

// ─── Distribution building / persistence ─────────────────────────────────────

// EligibleClaimant is one user the SP5/SP7 pipeline says should be in the tree:
// their on-chain account + cumulative base units. Exposed for testability /
// callers that want to inspect the eligible set without persisting.
type EligibleClaimant struct {
	UserID              uuid.UUID
	Account             common.Address
	CumulativeBaseUnits *big.Int
}

// BuildResult is the outcome of a BuildDistribution call.
type BuildResult struct {
	Distribution models.RewardDistribution
	Entries      []models.RewardDistributionEntry
}

// ErrRewardDistributorNotConfigured is returned when no distributor address is
// configured (REWARD_DISTRIBUTOR_ADDRESS unset) — the feature is dormant.
var ErrRewardDistributorNotConfigured = errors.New("reward distributor address not configured")

// ErrNoEligibleClaimants is returned when no eligible user has a positive
// claimable — there is nothing to publish (an empty tree has no defined root).
var ErrNoEligibleClaimants = errors.New("no eligible claimants with a positive claimable balance")

// BuildDistribution builds a NEW cumulative epoch from the CURRENT eligible
// claimables and persists it (the RewardDistribution row + one
// RewardDistributionEntry per leaf) in a single transaction. It returns the new
// distribution (root, epoch, …) + entries. It does NOT touch the chain — the
// caller returns an unsigned setMerkleRoot tx for the admin to sign+broadcast.
//
// Eligibility (SP7) + a positive claimable + a non-empty wallet address are all
// required to be in the tree. The per-user cumulative base units are FLOORED
// from the SP5 float (floorToBaseUnits) and then made MONOTONIC against the
// user's previous published cumulative, so an epoch can only ever grow a user's
// cumulative (matching the contract's cumulativeAmount > claimed guard and
// keeping re-publishes safe).
func (s *RewardMerkleService) BuildDistribution(ctx context.Context, publishedBy *uuid.UUID) (*BuildResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("reward merkle service not initialized")
	}
	if !s.IsConfigured() {
		return nil, ErrRewardDistributorNotConfigured
	}

	// 1) Collect every user with a positive claimable balance.
	var claimables []models.RewardClaimable
	if err := s.db.WithContext(ctx).
		Where("claimable_mcgp > 0").
		Order("user_id ASC"). // stable ordering ⇒ deterministic leaf order ⇒ stable root
		Find(&claimables).Error; err != nil {
		return nil, fmt.Errorf("load claimables: %w", err)
	}

	// 2) Filter to eligible + walletted users; floor + monotonic-clamp amounts.
	prev, err := s.previousCumulativeByUser(ctx)
	if err != nil {
		return nil, err
	}

	claimants := make([]EligibleClaimant, 0, len(claimables))
	for _, cl := range claimables {
		eligible, _, err := s.elig.IsClaimEligible(ctx, cl.UserID)
		if err != nil {
			return nil, fmt.Errorf("eligibility for %s: %w", cl.UserID, err)
		}
		if !eligible {
			continue
		}

		var user models.User
		if err := s.db.WithContext(ctx).
			Select("id", "wallet_address").
			First(&user, "id = ?", cl.UserID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, fmt.Errorf("load user %s: %w", cl.UserID, err)
		}
		addr := strings.TrimSpace(user.WalletAddress)
		if addr == "" || !common.IsHexAddress(addr) {
			// No usable on-chain address ⇒ the user cannot claim; exclude them
			// (their claimable persists and they join the next epoch once they
			// register a wallet).
			continue
		}

		amount := floorToBaseUnits(cl.ClaimableMCGP)
		// Monotonic clamp: an epoch can only ever grow a user's cumulative.
		if p, ok := prev[cl.UserID]; ok && amount.Cmp(p) < 0 {
			amount = new(big.Int).Set(p)
		}
		if amount.Sign() <= 0 {
			continue // floored to zero (dust) — nothing claimable on-chain
		}

		claimants = append(claimants, EligibleClaimant{
			UserID:              cl.UserID,
			Account:             common.HexToAddress(addr),
			CumulativeBaseUnits: amount,
		})
	}

	if len(claimants) == 0 {
		return nil, ErrNoEligibleClaimants
	}

	// 3) Build the tree (leaves in claimant order — already user_id-sorted).
	leaves := make([][32]byte, len(claimants))
	for i, c := range claimants {
		leaves[i] = RewardLeaf(c.Account, c.CumulativeBaseUnits)
	}
	tree := buildMerkleTree(leaves)
	root := tree.Root()
	rootHex := hexBytes32(root)

	total := new(big.Int)
	for _, c := range claimants {
		total.Add(total, c.CumulativeBaseUnits)
	}

	// 4) Persist epoch + entries atomically.
	now := time.Now()
	dist := models.RewardDistribution{
		ID:                 uuid.New(),
		MerkleRoot:         rootHex,
		DistributorAddress: s.distributorAddress,
		ChainID:            s.chainID,
		LeafCount:          len(claimants),
		TotalBaseUnits:     total.String(),
		Status:             models.RewardDistributionStatusPending,
		PublishedBy:        publishedBy,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	entries := make([]models.RewardDistributionEntry, len(claimants))

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Next epoch number = max(epoch)+1 (1-based). Locked within the tx so two
		// concurrent publishes can't collide on the same epoch number.
		var maxEpoch *int64
		if err := tx.Model(&models.RewardDistribution{}).
			Select("MAX(epoch)").Scan(&maxEpoch).Error; err != nil {
			return fmt.Errorf("compute next epoch: %w", err)
		}
		nextEpoch := int64(1)
		if maxEpoch != nil {
			nextEpoch = *maxEpoch + 1
		}
		dist.Epoch = nextEpoch

		if err := tx.Create(&dist).Error; err != nil {
			return fmt.Errorf("create distribution: %w", err)
		}

		for i, c := range claimants {
			proof := tree.Proof(i)
			proofJSON, err := json.Marshal(hexProof(proof))
			if err != nil {
				return fmt.Errorf("marshal proof for %s: %w", c.UserID, err)
			}
			entries[i] = models.RewardDistributionEntry{
				ID:                  uuid.New(),
				DistributionID:      dist.ID,
				Epoch:               nextEpoch,
				UserID:              c.UserID,
				Account:             strings.ToLower(c.Account.Hex()),
				CumulativeBaseUnits: c.CumulativeBaseUnits.String(),
				Leaf:                hexBytes32(leaves[i]),
				Proof:               string(proofJSON),
				CreatedAt:           now,
			}
		}
		if err := tx.Create(&entries).Error; err != nil {
			return fmt.Errorf("create distribution entries: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &BuildResult{Distribution: dist, Entries: entries}, nil
}

// previousCumulativeByUser returns each user's HIGHEST-ever published cumulative
// base units across ALL prior epochs, used to clamp the new epoch monotonic
// (cumulative can only grow). Empty on the first-ever epoch.
//
// It MUST scan every epoch a user has appeared in, not just the latest one. If a
// user is present in epoch1 (cumulative 100) but EXCLUDED from epoch2 (transient
// SP7 flag, wallet removed, floored-to-dust, or no positive delta), they have no
// row in the latest epoch — so a latest-only read would lose their high-water
// mark and could republish a LOWER cumulative in epoch3, bricking their on-chain
// claim ("nothing to claim", since the contract requires cumulativeAmount >
// claimed[account]). Taking the per-user MAX over all entries preserves the
// guarantee that an epoch can only ever GROW a user's published cumulative.
//
// All cumulatives (pending + published) are considered so a not-yet-confirmed
// epoch's cumulative is never silently regressed by a subsequent build (the
// monotonic baseline is the full published history, regardless of on-chain
// confirmation status).
//
// MAX is computed per-user as a big.Int comparison, NOT a SQL string MAX:
// cumulative_base_units is a varying-length decimal string, and lexical string
// ordering is wrong for it (e.g. "9" > "100"). We stream all (user_id,
// cumulative) rows and fold the max with big.Int.Cmp.
func (s *RewardMerkleService) previousCumulativeByUser(ctx context.Context) (map[uuid.UUID]*big.Int, error) {
	out := map[uuid.UUID]*big.Int{}

	type row struct {
		UserID              uuid.UUID
		CumulativeBaseUnits string
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Model(&models.RewardDistributionEntry{}).
		Select("user_id", "cumulative_base_units").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load prior epoch entries: %w", err)
	}
	for _, r := range rows {
		v, ok := new(big.Int).SetString(r.CumulativeBaseUnits, 10)
		if !ok {
			return nil, fmt.Errorf("parse previous cumulative %q for user %s", r.CumulativeBaseUnits, r.UserID)
		}
		if cur, seen := out[r.UserID]; !seen || v.Cmp(cur) > 0 {
			out[r.UserID] = v
		}
	}
	return out, nil
}

// latestDistribution returns the most recent epoch row of ANY status (nil when
// none exists). Used for admin history + the "is a newer epoch pending?" check.
func (s *RewardMerkleService) latestDistribution(ctx context.Context) (*models.RewardDistribution, error) {
	var dist models.RewardDistribution
	err := s.db.WithContext(ctx).Order("epoch DESC").First(&dist).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load latest distribution: %w", err)
	}
	return &dist, nil
}

// latestPublishedDistribution returns the most recent epoch whose setMerkleRoot
// tx an admin has CONFIRMED on-chain (status='published'), i.e. the epoch whose
// root is actually live in the contract (nil when none is published yet). Claim
// data MUST be served from this epoch — serving a freshly-built 'pending' epoch's
// proof while the on-chain merkleRoot is still the previous epoch makes every
// claim() revert "MCGP: bad proof" until the admin confirms the new root.
func (s *RewardMerkleService) latestPublishedDistribution(ctx context.Context) (*models.RewardDistribution, error) {
	var dist models.RewardDistribution
	err := s.db.WithContext(ctx).
		Where("status = ?", models.RewardDistributionStatusPublished).
		Order("epoch DESC").
		First(&dist).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load latest published distribution: %w", err)
	}
	return &dist, nil
}

// ─── Read paths (admin history + user claim data) ────────────────────────────

// ListDistributions returns published epochs, newest first (admin history).
func (s *RewardMerkleService) ListDistributions(ctx context.Context, limit int) ([]models.RewardDistribution, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("reward merkle service not initialized")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []models.RewardDistribution
	if err := s.db.WithContext(ctx).
		Order("epoch DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list distributions: %w", err)
	}
	return rows, nil
}

// ClaimData is the user-facing claim payload for the latest epoch.
type ClaimData struct {
	// Included is false when the user isn't in the latest PUBLISHED tree
	// (ineligible, zero claimable, or no published epoch yet) — the app shows
	// "nothing to claim".
	Included bool `json:"included"`

	// PendingNextEpoch is true when a newer epoch has been BUILT but its
	// setMerkleRoot tx is not yet confirmed on-chain. The proof returned here is
	// for the last PUBLISHED epoch (still valid on-chain); the app can surface a
	// "more rewards pending — retry shortly" hint without bricking the claim. It
	// can be true regardless of Included (the user may already be claimable for
	// the published epoch and ALSO have more coming in the pending one).
	PendingNextEpoch bool `json:"pendingNextEpoch,omitempty"`

	// Epoch is the epoch the proof is for (0 when not included).
	Epoch int64 `json:"epoch"`

	// Account is the on-chain address the leaf is bound to.
	Account string `json:"account,omitempty"`

	// CumulativeAmountBaseUnits is the user's cumulative claimable MCGP in base
	// units (18-dec) as a decimal string — the exact `cumulativeAmount` arg the
	// claim(account, cumulativeAmount, proof) tx must use.
	CumulativeAmountBaseUnits string `json:"cumulativeAmountBaseUnits,omitempty"`

	// Proof is the merkle proof (array of 0x-prefixed sibling hashes).
	Proof []string `json:"proof,omitempty"`

	// Leaf is the leaf hash (debug/verification aid).
	Leaf string `json:"leaf,omitempty"`

	// MerkleRoot is the published root the proof verifies against.
	MerkleRoot string `json:"merkleRoot,omitempty"`

	// DistributorAddress is the contract the claim tx is sent to.
	DistributorAddress string `json:"distributorAddress,omitempty"`

	// ChainID is the chain the distributor is deployed on.
	ChainID int64 `json:"chainId,omitempty"`
}

// ClaimDataForUser returns the user's proof + cumulative amount from the latest
// PUBLISHED epoch (the root actually live on-chain) so the app can build the
// claim(account, cumulative, proof) tx. Serving an unconfirmed 'pending' epoch's
// proof would make claim() revert "MCGP: bad proof" until the admin broadcasts
// setMerkleRoot, so the publish-confirmation status gates which proof we hand
// out. Returns Included=false (cleanly, no error) when the user isn't in the
// latest published tree (ineligible, zero, or no epoch published yet).
// PendingNextEpoch flags when a newer-but-unconfirmed epoch exists so the app can
// tell the user to retry shortly. DistributorAddress/ChainID are always populated
// when configured so the app knows where to send the tx.
func (s *RewardMerkleService) ClaimDataForUser(ctx context.Context, userID uuid.UUID) (*ClaimData, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("reward merkle service not initialized")
	}
	out := &ClaimData{
		Included:           false,
		DistributorAddress: s.distributorAddress,
		ChainID:            s.chainID,
	}

	// Detect a built-but-unconfirmed newer epoch so we can hint the app to retry,
	// without ever serving its (not-yet-live) proof. Cheap: one indexed read.
	latest, err := s.latestDistribution(ctx)
	if err != nil {
		return nil, err
	}
	if latest != nil && latest.Status != models.RewardDistributionStatusPublished {
		out.PendingNextEpoch = true
	}

	published, err := s.latestPublishedDistribution(ctx)
	if err != nil {
		return nil, err
	}
	if published == nil {
		return out, nil // no root live on-chain yet — nothing claimable
	}

	var entry models.RewardDistributionEntry
	err = s.db.WithContext(ctx).
		Where("distribution_id = ? AND user_id = ?", published.ID, userID).
		First(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, nil // user not in the latest published tree — empty/ineligible
	}
	if err != nil {
		return nil, fmt.Errorf("load claim entry for %s: %w", userID, err)
	}

	// Gate on REMAINING claimable, not just tree membership. The contract pays
	// cumulativeAmount − claimed[account] and REVERTS "MCGP: nothing to claim" when
	// that is 0 (a fully-claimed user). Without this check the app keeps the Claim
	// button active and broadcasts a guaranteed-revert tx (the user pays gas for
	// nothing). When a claimed reader is wired, compare cumulative vs on-chain
	// claimed[account] and report Included=false (nothing to claim) once fully
	// claimed. A read failure is fail-open (assume claimable) so an RPC blip never
	// hides a legitimately-claimable user. nil reader ⇒ membership-only (prior
	// behavior, used by tests / RPC-less envs).
	if s.claimedReader != nil {
		cumulative, ok := new(big.Int).SetString(entry.CumulativeBaseUnits, 10)
		if !ok {
			return nil, fmt.Errorf("parse cumulative %q for %s", entry.CumulativeBaseUnits, userID)
		}
		if claimed, cerr := s.claimedReader.ClaimedOf(ctx, s.distributorAddress, entry.Account); cerr == nil {
			// remaining = cumulative − claimed; <= 0 ⇒ nothing left to claim.
			if cumulative.Cmp(claimed) <= 0 {
				return out, nil // Included stays false — fully claimed
			}
		}
		// cerr != nil ⇒ fail-open: fall through and treat as claimable.
	}

	var proof []string
	if err := json.Unmarshal([]byte(entry.Proof), &proof); err != nil {
		return nil, fmt.Errorf("decode proof for %s: %w", userID, err)
	}
	if proof == nil {
		proof = []string{}
	}

	out.Included = true
	out.Epoch = published.Epoch
	out.Account = entry.Account
	out.CumulativeAmountBaseUnits = entry.CumulativeBaseUnits
	out.Proof = proof
	out.Leaf = entry.Leaf
	out.MerkleRoot = published.MerkleRoot
	return out, nil
}

// PrepareSetMerkleRoot builds the unsigned setMerkleRoot(root) calldata + tx for
// the admin to sign and broadcast. The root hex is the 0x-prefixed 32-byte root
// from a built distribution. Returns the ABI-encoded calldata; the handler wraps
// it into the unsigned-tx JSON the app/admin signs (mirroring the P2P prepare
// pattern). No chain client is required here — root publishing is low-frequency
// and the admin signs client-side.
func (s *RewardMerkleService) PrepareSetMerkleRoot(rootHex string) ([]byte, error) {
	if !s.IsConfigured() {
		return nil, ErrRewardDistributorNotConfigured
	}
	root, err := parseBytes32(rootHex)
	if err != nil {
		return nil, fmt.Errorf("parse root: %w", err)
	}
	data, err := parsedRewardDistributorABI.Pack("setMerkleRoot", root)
	if err != nil {
		return nil, fmt.Errorf("pack setMerkleRoot: %w", err)
	}
	return data, nil
}

// PreparedClaim is the unsigned claim(account, cumulativeAmount, proof) tx for a
// user, returned by PrepareClaimForUser alongside the underlying ClaimData. The
// app signs Tx client-side and broadcasts (the backend holds no key — mirrors
// the P2P PrepareDeposit and SP6 PrepareSetMerkleRoot pattern).
type PreparedClaim struct {
	// Claimable is the underlying SP6 claim data (cumulative, proof, account,
	// merkleRoot, included flag, …). Included=false ⇒ Tx is nil (nothing to claim).
	Claimable *ClaimData `json:"claimData"`

	// Tx is the unsigned claim(account, cumulative, proof) tx the app signs +
	// broadcasts. nil when the user has nothing claimable on the live root
	// (Claimable.Included=false) — the app shows "nothing to claim".
	Tx *blockchainUnsignedTx `json:"unsignedTx,omitempty"`
}

// blockchainUnsignedTx mirrors blockchain.UnsignedTx without importing the
// blockchain package into services (services must not depend on handlers/
// blockchain transport types). The JSON tags match blockchain.UnsignedTx so the
// app sees one consistent unsigned-tx shape across P2P, reward-publish, and
// reward-claim.
type blockchainUnsignedTx struct {
	To      string `json:"to"`
	Value   string `json:"value"`
	Data    string `json:"data"`
	ChainID string `json:"chainId"`
}

// PrepareClaimForUser builds the UNSIGNED claim(account, cumulativeAmount, proof)
// tx the app signs + broadcasts to the distributor, sourced from the latest
// PUBLISHED epoch (the root live on-chain) via ClaimDataForUser. This is purely a
// convenience over /rewards/claim-data — the same cumulative+proof, pre-encoded
// into the exact claim calldata so the app doesn't have to ABI-encode it itself.
// The established codebase pattern is backend-prepared unsigned txs (P2P
// PrepareDeposit, SP6 PrepareSetMerkleRoot), so we mirror it here.
//
// Returns Tx=nil (cleanly, no error) when the user isn't claimable on the live
// root (ineligible / zero / no published epoch). The backend never signs or
// broadcasts; PendingNextEpoch on the embedded ClaimData still flags a newer
// unconfirmed epoch so the app can hint "more rewards pending".
func (s *RewardMerkleService) PrepareClaimForUser(ctx context.Context, userID uuid.UUID) (*PreparedClaim, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("reward merkle service not initialized")
	}
	data, err := s.ClaimDataForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := &PreparedClaim{Claimable: data}
	if !data.Included {
		return out, nil // nothing to claim on the live root — no tx to sign
	}

	account := common.HexToAddress(data.Account)
	cumulative, ok := new(big.Int).SetString(data.CumulativeAmountBaseUnits, 10)
	if !ok {
		return nil, fmt.Errorf("parse cumulative %q for claim", data.CumulativeAmountBaseUnits)
	}
	proof := make([][32]byte, 0, len(data.Proof))
	for _, p := range data.Proof {
		b, perr := parseBytes32(p)
		if perr != nil {
			return nil, fmt.Errorf("parse proof element %q: %w", p, perr)
		}
		proof = append(proof, b)
	}

	calldata, err := parsedRewardDistributorABI.Pack("claim", account, cumulative, proof)
	if err != nil {
		return nil, fmt.Errorf("pack claim: %w", err)
	}
	out.Tx = &blockchainUnsignedTx{
		To:      s.distributorAddress,
		Value:   "0",
		Data:    common.Bytes2Hex(calldata),
		ChainID: strconv.FormatInt(s.chainID, 10),
	}
	return out, nil
}

// MarkPublished records that the admin confirmed the setMerkleRoot tx on-chain.
func (s *RewardMerkleService) MarkPublished(ctx context.Context, epoch int64, txHash string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("reward merkle service not initialized")
	}
	res := s.db.WithContext(ctx).Model(&models.RewardDistribution{}).
		Where("epoch = ?", epoch).
		Updates(map[string]interface{}{
			"status":     models.RewardDistributionStatusPublished,
			"tx_hash":    strings.TrimSpace(txHash),
			"updated_at": time.Now(),
		})
	if res.Error != nil {
		return fmt.Errorf("mark epoch %d published: %w", epoch, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("epoch %d not found", epoch)
	}
	return nil
}

// ─── hex helpers ─────────────────────────────────────────────────────────────

// hexBytes32 renders a 32-byte array as 0x-prefixed lowercase hex.
func hexBytes32(b [32]byte) string {
	return "0x" + common.Bytes2Hex(b[:])
}

// hexProof renders a proof (slice of 32-byte arrays) as 0x-prefixed hex strings.
func hexProof(proof [][32]byte) []string {
	out := make([]string, len(proof))
	for i, p := range proof {
		out[i] = hexBytes32(p)
	}
	return out
}

// parseBytes32 parses a 0x-prefixed 32-byte hex string.
func parseBytes32(s string) ([32]byte, error) {
	var out [32]byte
	h := strings.TrimPrefix(strings.TrimSpace(s), "0x")
	b := common.FromHex("0x" + h)
	if len(b) != 32 {
		return out, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}
