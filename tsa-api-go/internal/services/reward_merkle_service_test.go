package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ─── Cross-impl leaf + root vector (vs the contract's published vector) ───────

// The contract stage (MCGPRewardDistributor.test.js "PRINTS a concrete test
// vector") published these exact values. The Go leaf encoding + sorted-pair tree
// MUST reproduce them byte-for-byte, or every on-chain claim fails
// MerkleProof.verify. This is the cross-implementation agreement guard.
const (
	vecAccountA = "0x1111111111111111111111111111111111111111"
	vecAmountA  = "100000000000000000000" // 100 MCGP @ 18 decimals
	vecLeafA    = "0x923a571f712ff2d205bc7da76152ab30459ae47d04647ff145ba9ad746468fc6"
	vecRoot     = "0xef925f05ffc885610bf79d9f8a35391afd3a310570d03583a9918834c93a7047"

	vecAccountB = "0x2222222222222222222222222222222222222222"
	vecAmountB  = "250000000000000000000"
	vecLeafB    = "0x961b65b12221d8db52e43da798be51b873abf56862b42a2bc18e831faf7fbbd8"

	vecAccountC = "0x3333333333333333333333333333333333333333"
	vecAmountC  = "75000000000000000000"
	vecLeafC    = "0x714af8c35e4f32fe84b0a1a78bd6a4bdee0f1cbf9eb6eb981239ef2ddd8e0ac4"
)

func mustBig(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad bigint %q", s)
	}
	return v
}

// TestRewardLeaf_MatchesContractVector asserts the Go leaf for the contract's
// (account, amount) test vector equals the contract's reported leaf hex.
func TestRewardLeaf_MatchesContractVector(t *testing.T) {
	cases := []struct {
		account string
		amount  string
		want    string
	}{
		{vecAccountA, vecAmountA, vecLeafA},
		{vecAccountB, vecAmountB, vecLeafB},
		{vecAccountC, vecAmountC, vecLeafC},
	}
	for _, tc := range cases {
		got := hexBytes32(RewardLeaf(common.HexToAddress(tc.account), mustBig(t, tc.amount)))
		if got != tc.want {
			t.Fatalf("leaf(%s, %s) = %s, want %s (DRIFT from contract — claims would fail)", tc.account, tc.amount, got, tc.want)
		}
	}
}

// TestRewardTree_MatchesContractRootAndProof asserts the Go sorted-pair tree
// over the contract's 3-leaf vector yields the contract's root and the
// contract's proof for account A.
func TestRewardTree_MatchesContractRootAndProof(t *testing.T) {
	leaves := [][32]byte{
		RewardLeaf(common.HexToAddress(vecAccountA), mustBig(t, vecAmountA)),
		RewardLeaf(common.HexToAddress(vecAccountB), mustBig(t, vecAmountB)),
		RewardLeaf(common.HexToAddress(vecAccountC), mustBig(t, vecAmountC)),
	}
	tree := buildMerkleTree(leaves)

	if got := hexBytes32(tree.Root()); got != vecRoot {
		t.Fatalf("root = %s, want %s (DRIFT from contract)", got, vecRoot)
	}

	// The contract's published proof for A is [leafB, leafC].
	proof := hexProof(tree.Proof(0))
	wantProof := []string{vecLeafB, vecLeafC}
	if len(proof) != len(wantProof) {
		t.Fatalf("proofA length = %d, want %d", len(proof), len(wantProof))
	}
	for i := range proof {
		if proof[i] != wantProof[i] {
			t.Fatalf("proofA[%d] = %s, want %s", i, proof[i], wantProof[i])
		}
	}
}

// ─── Base-unit conversion determinism + floor ────────────────────────────────

func TestFloorToBaseUnits_DeterministicAndFloored(t *testing.T) {
	cases := []struct {
		mcgp float64
		want string
	}{
		{100, "100000000000000000000"}, // exact 100 MCGP
		{0, "0"},                       // zero
		{-5, "0"},                      // negative → 0
		{0.000000000000000001, "1"},    // 1 base unit
		{1.5, "1500000000000000000"},   // 1.5 MCGP
		{75, "75000000000000000000"},   // matches vector C
	}
	for _, tc := range cases {
		got := floorToBaseUnits(tc.mcgp).String()
		if got != tc.want {
			t.Fatalf("floorToBaseUnits(%v) = %s, want %s", tc.mcgp, got, tc.want)
		}
	}

	// Determinism: same input → same output across repeated calls.
	for i := 0; i < 5; i++ {
		if got := floorToBaseUnits(123.456).String(); got != floorToBaseUnits(123.456).String() {
			t.Fatalf("non-deterministic floorToBaseUnits at i=%d", i)
		}
	}

	// Floor never rounds UP: a value whose 1e18 scaling has a fractional part
	// truncates down, never up (treasury can always cover the published amount).
	got := floorToBaseUnits(1.2345678901234567891) // more digits than 1e18 resolves
	scale := baseUnitScale()
	// got must be <= mcgp*1e18 (i.e. flooring, not rounding).
	upper := new(big.Float).SetPrec(256).SetFloat64(1.2345678901234567891)
	upper.Mul(upper, new(big.Float).SetPrec(256).SetInt(scale))
	upperInt, _ := upper.Int(nil)
	if got.Cmp(upperInt) > 0 {
		t.Fatalf("floor produced %s > scaled %s (rounded up!)", got, upperInt)
	}
}

// ─── DB-backed BuildDistribution / claim-data tests ──────────────────────────

// fakeEligibility implements claimEligibilityChecker. eligible maps user id →
// whether they may claim; missing ids default to eligible.
type fakeEligibility struct {
	eligible map[uuid.UUID]bool
}

func (f *fakeEligibility) IsClaimEligible(_ context.Context, userID uuid.UUID) (bool, []string, error) {
	if f.eligible == nil {
		return true, nil, nil
	}
	if e, ok := f.eligible[userID]; ok {
		if !e {
			return false, []string{"flagged in test"}, nil
		}
		return true, nil, nil
	}
	return true, nil, nil
}

// setupMerkleTestDB opens an in-memory SQLite DB with the users, reward_claimables,
// and SP6 distribution tables (raw DDL so PostgreSQL gen_random_uuid() defaults
// don't apply; all IDs set in Go).
func setupMerkleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	stmts := []string{
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			username TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL UNIQUE,
			password TEXT NOT NULL,
			role TEXT DEFAULT 'user',
			phone_number TEXT,
			country TEXT,
			state TEXT,
			city TEXT,
			address TEXT,
			profile_photo TEXT,
			referral_code TEXT,
			referred_by TEXT,
			didit_session_id TEXT,
			verification_status TEXT DEFAULT 'pending',
			verification_notes TEXT,
			account_status TEXT DEFAULT 'active',
			last_login DATETIME,
			login_attempts INTEGER DEFAULT 0,
			lock_until DATETIME,
			wallet_address TEXT,
			seed_phrase_backed_up INTEGER DEFAULT 0,
			mute_notifications INTEGER DEFAULT 0,
			mute_email INTEGER DEFAULT 0,
			tp_balance REAL DEFAULT 0,
			cashback_balance REAL DEFAULT 0,
			last_settled_global_index INTEGER,
			email_verified INTEGER DEFAULT 0,
			deleted_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME, declared_given_names TEXT, declared_surname TEXT
		)`,
		`CREATE TABLE reward_claimables (
			user_id TEXT PRIMARY KEY,
			claimable_mcgp REAL NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE reward_distributions (
			id TEXT PRIMARY KEY,
			epoch INTEGER NOT NULL,
			merkle_root TEXT NOT NULL,
			distributor_address TEXT NOT NULL,
			chain_id INTEGER NOT NULL DEFAULT 0,
			leaf_count INTEGER NOT NULL DEFAULT 0,
			total_base_units TEXT NOT NULL DEFAULT '0',
			status TEXT NOT NULL DEFAULT 'pending',
			published_by TEXT,
			tx_hash TEXT,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE reward_distribution_entries (
			id TEXT PRIMARY KEY,
			distribution_id TEXT NOT NULL,
			epoch INTEGER NOT NULL,
			user_id TEXT NOT NULL,
			account TEXT NOT NULL,
			cumulative_base_units TEXT NOT NULL,
			leaf TEXT NOT NULL,
			proof TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	return db
}

// mkMerkleUser inserts a user with a wallet address (+ verified/active by
// default) and a claimable balance.
func mkMerkleUser(t *testing.T, db *gorm.DB, wallet string, claimable float64) uuid.UUID {
	t.Helper()
	id := uuid.New()
	u := models.User{
		ID:                 id,
		Name:               "u",
		Username:           "u" + id.String()[:8],
		Email:              id.String()[:8] + "@test.com",
		Password:           "x",
		VerificationStatus: models.VerificationStatusVerified,
		AccountStatus:      models.AccountStatusActive,
		WalletAddress:      wallet,
		TPBalance:          models.ThresholdTPForLevel(MinClaimLevel), // claimable unless a test lowers it
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if claimable != 0 {
		if err := db.Create(&models.RewardClaimable{
			UserID:        id,
			ClaimableMCGP: claimable,
			UpdatedAt:     time.Now(),
		}).Error; err != nil {
			t.Fatalf("create claimable: %v", err)
		}
	}
	return id
}

const (
	walletA = "0x1111111111111111111111111111111111111111"
	walletB = "0x2222222222222222222222222222222222222222"
	walletC = "0x3333333333333333333333333333333333333333"
)

// TestBuildDistribution_OnlyEligibleIncluded checks an ineligible user is
// excluded while eligible users are in the tree.
func TestBuildDistribution_OnlyEligibleIncluded(t *testing.T) {
	db := setupMerkleTestDB(t)
	good := mkMerkleUser(t, db, walletA, 100)
	bad := mkMerkleUser(t, db, walletB, 250)

	elig := &fakeEligibility{eligible: map[uuid.UUID]bool{
		good: true,
		bad:  false, // flagged/unverified → excluded
	}}
	svc := NewRewardMerkleService(db, elig, "0xdistributor000000000000000000000000000001", 146)

	build, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if build.Distribution.LeafCount != 1 {
		t.Fatalf("leaf count = %d, want 1 (only eligible user)", build.Distribution.LeafCount)
	}
	if len(build.Entries) != 1 || build.Entries[0].UserID != good {
		t.Fatalf("expected only the eligible user in the tree, got %+v", build.Entries)
	}
	if build.Entries[0].CumulativeBaseUnits != "100000000000000000000" {
		t.Fatalf("cumulative base units = %s, want 100e18", build.Entries[0].CumulativeBaseUnits)
	}
}

// TestBuildDistribution_ExcludesNoWallet checks a user without a wallet address
// is excluded (cannot claim).
func TestBuildDistribution_ExcludesNoWallet(t *testing.T) {
	db := setupMerkleTestDB(t)
	withWallet := mkMerkleUser(t, db, walletA, 100)
	_ = mkMerkleUser(t, db, "", 50) // no wallet → excluded

	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdist0000000000000000000000000000000000a1", 146)
	build, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if build.Distribution.LeafCount != 1 || build.Entries[0].UserID != withWallet {
		t.Fatalf("expected only the walletted user, got leafCount=%d entries=%+v", build.Distribution.LeafCount, build.Entries)
	}
}

// TestBuildDistribution_ExcludesSoftDeleted asserts a soft-deleted user (deleted_at
// set, but still account_status=active and eligible per the gate) NEVER gets a
// merkle leaf — User.DeletedAt is a plain *time.Time, so the build query must
// filter deleted_at IS NULL explicitly. Without it a deleted account could claim.
func TestBuildDistribution_ExcludesSoftDeleted(t *testing.T) {
	db := setupMerkleTestDB(t)
	live := mkMerkleUser(t, db, walletA, 100)
	deleted := mkMerkleUser(t, db, walletB, 250)
	// Soft-delete the second user, leaving account_status active (the hole this
	// guards: deletion that sets deleted_at but not status).
	if err := db.Model(&models.User{}).Where("id = ?", deleted).
		Update("deleted_at", time.Now()).Error; err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	// fakeEligibility (nil map) deems everyone eligible, so this isolates the
	// build-query defense-in-depth (not the eligibility gate).
	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdist0000000000000000000000000000000000a1", 146)
	build, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if build.Distribution.LeafCount != 1 || build.Entries[0].UserID != live {
		t.Fatalf("expected only the live user, got leafCount=%d entries=%+v", build.Distribution.LeafCount, build.Entries)
	}
}

// TestBuildDistribution_ExcludesDuplicateWallet asserts that when two eligible
// users share one wallet address, BOTH are excluded — the on-chain claimed[]
// mapping is keyed by address, so two leaves for one address are an ambiguous
// proof / double-claim. A third user on a unique wallet is unaffected.
func TestBuildDistribution_ExcludesDuplicateWallet(t *testing.T) {
	db := setupMerkleTestDB(t)
	const shared = "0x3333333333333333333333333333333333333333"
	mkMerkleUser(t, db, shared, 100) // dup A
	mkMerkleUser(t, db, shared, 250) // dup B (same wallet)
	unique := mkMerkleUser(t, db, walletA, 75)

	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdist0000000000000000000000000000000000a1", 146)
	build, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if build.Distribution.LeafCount != 1 || build.Entries[0].UserID != unique {
		t.Fatalf("expected only the unique-wallet user, got leafCount=%d entries=%+v", build.Distribution.LeafCount, build.Entries)
	}
}

// TestBuildDistribution_RootMatchesIndependentTree builds a distribution and
// verifies the persisted root equals an independently-built tree over the same
// leaves — and that each persisted proof verifies against the root.
func TestBuildDistribution_RootMatchesIndependentTree(t *testing.T) {
	db := setupMerkleTestDB(t)
	mkMerkleUser(t, db, walletA, 100)
	mkMerkleUser(t, db, walletB, 250)
	mkMerkleUser(t, db, walletC, 75)

	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdist0000000000000000000000000000000000a1", 146)
	build, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if build.Distribution.LeafCount != 3 {
		t.Fatalf("leaf count = %d, want 3", build.Distribution.LeafCount)
	}

	// Each persisted proof must verify against the published root.
	root, err := parseBytes32(build.Distribution.MerkleRoot)
	if err != nil {
		t.Fatalf("parse root: %v", err)
	}
	for _, e := range build.Entries {
		leaf, err := parseBytes32(e.Leaf)
		if err != nil {
			t.Fatalf("parse leaf: %v", err)
		}
		var proofHex []string
		if err := json.Unmarshal([]byte(e.Proof), &proofHex); err != nil {
			t.Fatalf("unmarshal proof: %v", err)
		}
		if !verifyProof(t, leaf, proofHex, root) {
			t.Fatalf("proof for user %s does not verify against root", e.UserID)
		}
	}
}

// verifyProof reproduces the contract's sorted-pair MerkleProof.verify off-chain
// so the test is self-contained.
func verifyProof(t *testing.T, leaf [32]byte, proofHex []string, root [32]byte) bool {
	t.Helper()
	computed := leaf
	for _, p := range proofHex {
		sib, err := parseBytes32(p)
		if err != nil {
			t.Fatalf("parse proof node: %v", err)
		}
		computed = hashMerklePair(computed, sib)
	}
	return computed == root
}

// TestBuildDistribution_CumulativeMonotonic publishes twice; the second epoch's
// cumulative can only grow, never shrink, even if the float claimable dips.
func TestBuildDistribution_CumulativeMonotonic(t *testing.T) {
	db := setupMerkleTestDB(t)
	user := mkMerkleUser(t, db, walletA, 100)

	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdist0000000000000000000000000000000000a1", 146)

	// Epoch 1: cumulative 100.
	b1, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build epoch 1: %v", err)
	}
	if b1.Distribution.Epoch != 1 {
		t.Fatalf("epoch1 = %d, want 1", b1.Distribution.Epoch)
	}
	if b1.Entries[0].CumulativeBaseUnits != "100000000000000000000" {
		t.Fatalf("epoch1 cumulative = %s, want 100e18", b1.Entries[0].CumulativeBaseUnits)
	}

	// Claimable grows to 175 → epoch 2 cumulative 175.
	if err := db.Model(&models.RewardClaimable{}).
		Where("user_id = ?", user).
		Update("claimable_mcgp", 175).Error; err != nil {
		t.Fatalf("bump claimable: %v", err)
	}
	b2, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build epoch 2: %v", err)
	}
	if b2.Distribution.Epoch != 2 {
		t.Fatalf("epoch2 = %d, want 2", b2.Distribution.Epoch)
	}
	if b2.Entries[0].CumulativeBaseUnits != "175000000000000000000" {
		t.Fatalf("epoch2 cumulative = %s, want 175e18", b2.Entries[0].CumulativeBaseUnits)
	}

	// Claimable DROPS to 50 (e.g. a correction) → epoch 3 must CLAMP to the prior
	// 175 (monotonic), never shrink below an already-published cumulative.
	if err := db.Model(&models.RewardClaimable{}).
		Where("user_id = ?", user).
		Update("claimable_mcgp", 50).Error; err != nil {
		t.Fatalf("drop claimable: %v", err)
	}
	b3, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build epoch 3: %v", err)
	}
	if b3.Entries[0].CumulativeBaseUnits != "175000000000000000000" {
		t.Fatalf("epoch3 cumulative = %s, want 175e18 (monotonic clamp)", b3.Entries[0].CumulativeBaseUnits)
	}
}

// TestBuildDistribution_ClampSurvivesExclusionGap is the regression for the
// monotonic-clamp gap across an exclusion epoch. A user present in epoch1
// (cumulative 100) but EXCLUDED from epoch2 (e.g. transiently flagged by SP7)
// must STILL be clamped to their epoch1 high-water mark when they re-appear in
// epoch3 with a lower claimable (a correction) — otherwise epoch3 would publish
// cumulative 60 < the 100 they already claimed and brick their on-chain claim.
func TestBuildDistribution_ClampSurvivesExclusionGap(t *testing.T) {
	db := setupMerkleTestDB(t)
	// userB keeps every epoch's tree non-empty so we can exclude userA in epoch2.
	userA := mkMerkleUser(t, db, walletA, 100)
	userB := mkMerkleUser(t, db, walletB, 250)

	elig := &fakeEligibility{eligible: map[uuid.UUID]bool{userA: true, userB: true}}
	svc := NewRewardMerkleService(db, elig, "0xdist0000000000000000000000000000000000a1", 146)

	// Epoch 1: userA cumulative 100.
	if _, err := svc.BuildDistribution(context.Background(), nil); err != nil {
		t.Fatalf("build epoch 1: %v", err)
	}

	// Epoch 2: userA is EXCLUDED (flagged). They have no row in this epoch, so a
	// latest-epoch-only clamp would lose their high-water mark.
	elig.eligible[userA] = false
	b2, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build epoch 2: %v", err)
	}
	for _, e := range b2.Entries {
		if e.UserID == userA {
			t.Fatalf("userA should be excluded from epoch 2, but is present: %+v", e)
		}
	}

	// Epoch 3: userA re-appears but their claimable has DROPPED to 60 (an SP5
	// correction). The clamp must restore them to their epoch1 cumulative (100),
	// NOT publish 60 < 100.
	elig.eligible[userA] = true
	if err := db.Model(&models.RewardClaimable{}).
		Where("user_id = ?", userA).
		Update("claimable_mcgp", 60).Error; err != nil {
		t.Fatalf("drop userA claimable: %v", err)
	}
	b3, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build epoch 3: %v", err)
	}
	var gotA string
	for _, e := range b3.Entries {
		if e.UserID == userA {
			gotA = e.CumulativeBaseUnits
		}
	}
	if gotA != "100000000000000000000" {
		t.Fatalf("epoch3 userA cumulative = %q, want 100e18 (clamped to pre-exclusion high-water mark)", gotA)
	}
}

// TestClaimData_IncludedAndExcluded checks claim-data returns the correct proof
// for an included user and empty for an excluded one.
func TestClaimData_IncludedAndExcluded(t *testing.T) {
	db := setupMerkleTestDB(t)
	included := mkMerkleUser(t, db, walletA, 100)
	excluded := mkMerkleUser(t, db, walletB, 250)

	elig := &fakeEligibility{eligible: map[uuid.UUID]bool{
		included: true,
		excluded: false,
	}}
	svc := NewRewardMerkleService(db, elig, "0xdist0000000000000000000000000000000000a1", 146)

	build, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Before the admin confirms the root on-chain, claim data must NOT serve the
	// freshly-built (pending) epoch's proof — it would fail "MCGP: bad proof".
	cdPending, err := svc.ClaimDataForUser(context.Background(), included)
	if err != nil {
		t.Fatalf("claim data (pending): %v", err)
	}
	if cdPending.Included {
		t.Fatalf("pending (unconfirmed) epoch must not be served as claimable")
	}
	if !cdPending.PendingNextEpoch {
		t.Fatalf("expected PendingNextEpoch=true while the epoch is unconfirmed")
	}

	// Admin confirms the root on-chain → the epoch is now the live published root.
	svc.SetPublishVerifier(okPublishVerifier{root: build.Distribution.MerkleRoot})
	if err := svc.MarkPublished(context.Background(), build.Distribution.Epoch, "0xtxhash"); err != nil {
		t.Fatalf("mark published: %v", err)
	}

	// Included user: full claim payload.
	cd, err := svc.ClaimDataForUser(context.Background(), included)
	if err != nil {
		t.Fatalf("claim data (included): %v", err)
	}
	if !cd.Included {
		t.Fatalf("expected included=true for the eligible user")
	}
	if cd.PendingNextEpoch {
		t.Fatalf("no newer epoch exists — PendingNextEpoch should be false")
	}
	if cd.CumulativeAmountBaseUnits != "100000000000000000000" {
		t.Fatalf("claim cumulative = %s, want 100e18", cd.CumulativeAmountBaseUnits)
	}
	if cd.DistributorAddress != "0xdist0000000000000000000000000000000000a1" || cd.ChainID != 146 {
		t.Fatalf("claim distributor/chain wrong: %+v", cd)
	}
	// The returned proof must verify against the returned root.
	leaf, _ := parseBytes32(cd.Leaf)
	root, _ := parseBytes32(cd.MerkleRoot)
	if !verifyProof(t, leaf, cd.Proof, root) {
		t.Fatalf("returned claim proof does not verify")
	}

	// Excluded user: clean empty (no error).
	cd2, err := svc.ClaimDataForUser(context.Background(), excluded)
	if err != nil {
		t.Fatalf("claim data (excluded): %v", err)
	}
	if cd2.Included {
		t.Fatalf("expected included=false for the ineligible user")
	}
	if cd2.Proof != nil || cd2.CumulativeAmountBaseUnits != "" {
		t.Fatalf("excluded user should have no proof/amount, got %+v", cd2)
	}
	// Distributor/chain still surfaced so the app knows where it would claim.
	if cd2.DistributorAddress == "" {
		t.Fatalf("excluded claim-data should still carry the distributor address")
	}
}

// TestBuildDistribution_NoEligibleClaimants returns a clean sentinel when the
// tree would be empty.
func TestBuildDistribution_NoEligibleClaimants(t *testing.T) {
	db := setupMerkleTestDB(t)
	// A user with a zero claimable is filtered out by the WHERE clause.
	mkMerkleUser(t, db, walletA, 0)

	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdist0000000000000000000000000000000000a1", 146)
	_, err := svc.BuildDistribution(context.Background(), nil)
	if err != ErrNoEligibleClaimants {
		t.Fatalf("err = %v, want ErrNoEligibleClaimants", err)
	}
}

// TestBuildDistribution_DormantWhenUnconfigured errors clearly with no address.
func TestBuildDistribution_DormantWhenUnconfigured(t *testing.T) {
	db := setupMerkleTestDB(t)
	svc := NewRewardMerkleService(db, &fakeEligibility{}, "", 146)
	if svc.IsConfigured() {
		t.Fatalf("service should be dormant with empty address")
	}
	_, err := svc.BuildDistribution(context.Background(), nil)
	if err != ErrRewardDistributorNotConfigured {
		t.Fatalf("err = %v, want ErrRewardDistributorNotConfigured", err)
	}
}

// TestPrepareSetMerkleRoot_PacksCalldata checks the unsigned setMerkleRoot
// calldata round-trips through the distributor ABI.
func TestPrepareSetMerkleRoot_PacksCalldata(t *testing.T) {
	svc := NewRewardMerkleService(nil, &fakeEligibility{}, "0xdist0000000000000000000000000000000000a1", 146)
	data, err := svc.PrepareSetMerkleRoot(vecRoot)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	method, err := parsedRewardDistributorABI.MethodById(data[:4])
	if err != nil {
		t.Fatalf("method by id: %v", err)
	}
	if method.Name != "setMerkleRoot" {
		t.Fatalf("method = %s, want setMerkleRoot", method.Name)
	}
	args, err := method.Inputs.Unpack(data[4:])
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	got := args[0].([32]byte)
	want, _ := parseBytes32(vecRoot)
	if got != want {
		t.Fatalf("packed root = %x, want %x", got, want)
	}
}

// TestPrepareClaimForUser_EncodesClaimTx asserts PrepareClaimForUser builds a
// well-formed unsigned claim(account, cumulative, proof) tx for a published,
// included user — the selector is claim's, the To/ChainID target the
// distributor, and the embedded ClaimData matches /rewards/claim-data. An
// excluded user gets a nil Tx (nothing to claim).
func TestPrepareClaimForUser_EncodesClaimTx(t *testing.T) {
	db := setupMerkleTestDB(t)
	included := mkMerkleUser(t, db, walletA, 100)
	excluded := mkMerkleUser(t, db, walletB, 250)

	elig := &fakeEligibility{eligible: map[uuid.UUID]bool{included: true, excluded: false}}
	const dist = "0xdist0000000000000000000000000000000000a1"
	svc := NewRewardMerkleService(db, elig, dist, 146)

	build, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	svc.SetPublishVerifier(okPublishVerifier{root: build.Distribution.MerkleRoot})
	if err := svc.MarkPublished(context.Background(), build.Distribution.Epoch, "0xtxhash"); err != nil {
		t.Fatalf("mark published: %v", err)
	}

	prep, err := svc.PrepareClaimForUser(context.Background(), included)
	if err != nil {
		t.Fatalf("prepare claim (included): %v", err)
	}
	if prep.Claimable == nil || !prep.Claimable.Included {
		t.Fatalf("expected included claim data, got %+v", prep.Claimable)
	}
	if prep.Tx == nil {
		t.Fatalf("expected an unsigned claim tx for an included user")
	}
	if prep.Tx.To != dist {
		t.Errorf("tx.To = %s, want distributor %s", prep.Tx.To, dist)
	}
	if prep.Tx.ChainID != "146" {
		t.Errorf("tx.ChainID = %s, want 146", prep.Tx.ChainID)
	}
	if prep.Tx.Value != "0" {
		t.Errorf("tx.Value = %s, want 0", prep.Tx.Value)
	}
	// The calldata must begin with the claim() 4-byte selector and decode back to
	// the same account + cumulative the claim data advertises.
	data := common.FromHex(prep.Tx.Data)
	if len(data) < 4 {
		t.Fatalf("calldata too short: %d bytes", len(data))
	}
	method, err := parsedRewardDistributorABI.MethodById(data[:4])
	if err != nil || method.Name != "claim" {
		t.Fatalf("calldata selector is not claim(): method=%v err=%v", method, err)
	}
	args, err := method.Inputs.Unpack(data[4:])
	if err != nil {
		t.Fatalf("unpack claim args: %v", err)
	}
	gotAcct := args[0].(common.Address)
	gotCum := args[1].(*big.Int)
	if gotAcct != common.HexToAddress(walletA) {
		t.Errorf("claim account = %s, want %s", gotAcct.Hex(), walletA)
	}
	wantCum, _ := new(big.Int).SetString(prep.Claimable.CumulativeAmountBaseUnits, 10)
	if gotCum.Cmp(wantCum) != 0 {
		t.Errorf("claim cumulative = %s, want %s", gotCum, wantCum)
	}

	// Excluded user: no tx to sign.
	prepExcl, err := svc.PrepareClaimForUser(context.Background(), excluded)
	if err != nil {
		t.Fatalf("prepare claim (excluded): %v", err)
	}
	if prepExcl.Tx != nil {
		t.Errorf("excluded user should get a nil unsigned tx, got %+v", prepExcl.Tx)
	}
	if prepExcl.Claimable == nil || prepExcl.Claimable.Included {
		t.Errorf("excluded user claim data should be included=false")
	}
}

// fakeClaimedReader is a claimedOfReader returning a fixed claimed[account]
// keyed by lower-cased account hex (and an optional read error).
type fakeClaimedReader struct {
	claimed map[string]*big.Int
	err     error
}

func (f *fakeClaimedReader) ClaimedOf(_ context.Context, _ string, account string) (*big.Int, error) {
	if f.err != nil {
		return nil, f.err
	}
	if v, ok := f.claimed[strings.ToLower(account)]; ok {
		return v, nil
	}
	return big.NewInt(0), nil
}

// TestClaimData_GatesOnRemainingClaimable asserts that with an on-chain claimed
// reader wired, a fully-claimed user is reported NOT claimable (Included=false,
// Tx=nil) — so the app stops building a guaranteed-revert claim() — while a
// partially-claimed user stays claimable, and a reader error fails OPEN.
func TestClaimData_GatesOnRemainingClaimable(t *testing.T) {
	db := setupMerkleTestDB(t)
	included := mkMerkleUser(t, db, walletA, 100) // cumulative 100e18

	elig := &fakeEligibility{eligible: map[uuid.UUID]bool{included: true}}
	const dist = "0xdist0000000000000000000000000000000000a1"
	svc := NewRewardMerkleService(db, elig, dist, 146)

	build, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	svc.SetPublishVerifier(okPublishVerifier{root: build.Distribution.MerkleRoot})
	if err := svc.MarkPublished(context.Background(), build.Distribution.Epoch, "0xtxhash"); err != nil {
		t.Fatalf("mark published: %v", err)
	}

	cumulative := mustBig(t, "100000000000000000000") // 100 MCGP base units

	// (1) Fully claimed (claimed == cumulative) ⇒ nothing left ⇒ not claimable.
	svc.SetClaimedReader(&fakeClaimedReader{claimed: map[string]*big.Int{
		strings.ToLower(walletA): new(big.Int).Set(cumulative),
	}})
	cd, err := svc.ClaimDataForUser(context.Background(), included)
	if err != nil {
		t.Fatalf("claim data (fully claimed): %v", err)
	}
	if cd.Included {
		t.Fatalf("fully-claimed user must NOT be claimable (would revert 'nothing to claim')")
	}
	prep, err := svc.PrepareClaimForUser(context.Background(), included)
	if err != nil {
		t.Fatalf("prepare (fully claimed): %v", err)
	}
	if prep.Tx != nil {
		t.Fatalf("fully-claimed user must get a nil unsigned tx, got %+v", prep.Tx)
	}

	// (2) Partially claimed (claimed < cumulative) ⇒ still claimable.
	half := new(big.Int).Div(cumulative, big.NewInt(2))
	svc.SetClaimedReader(&fakeClaimedReader{claimed: map[string]*big.Int{
		strings.ToLower(walletA): half,
	}})
	cd2, err := svc.ClaimDataForUser(context.Background(), included)
	if err != nil {
		t.Fatalf("claim data (partial): %v", err)
	}
	if !cd2.Included {
		t.Fatalf("partially-claimed user must remain claimable")
	}

	// (3) Reader error ⇒ fail OPEN (treat as claimable) so an RPC blip can't hide
	// a legitimate claim.
	svc.SetClaimedReader(&fakeClaimedReader{err: errReadFail})
	cd3, err := svc.ClaimDataForUser(context.Background(), included)
	if err != nil {
		t.Fatalf("claim data (reader error): %v", err)
	}
	if !cd3.Included {
		t.Fatalf("a claimed-read error must fail open (still claimable)")
	}

	// (4) No reader (nil) ⇒ membership-only, prior behavior preserved.
	svc.SetClaimedReader(nil)
	cd4, err := svc.ClaimDataForUser(context.Background(), included)
	if err != nil {
		t.Fatalf("claim data (no reader): %v", err)
	}
	if !cd4.Included {
		t.Fatalf("with no claimed reader, membership-only gating must keep the user claimable")
	}
}

var errReadFail = errors.New("rpc read failed")

// PM 2026-10-23: TP is claimable from the pool only once a user reaches Level 3.
func TestBuildDistribution_RequiresLevelThree(t *testing.T) {
	db := setupMerkleTestDB(t)
	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdistributor000000000000000000000000000001", 146)

	low := mkMerkleUser(t, db, walletA, 5)
	db.Model(&models.User{}).Where("id = ?", low).Update("tp_balance", models.ThresholdTPForLevel(MinClaimLevel)-0.01)
	mkMerkleUser(t, db, walletB, 5)

	res, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(res.Entries) != 1 || res.Entries[0].UserID == low {
		t.Fatalf("entries = %+v; want only the Level 3 user", res.Entries)
	}
}

// A user already in a published epoch keeps their leaf even if their level reads lower now.
func TestBuildDistribution_KeepsPublishedUserBelowLevelThree(t *testing.T) {
	db := setupMerkleTestDB(t)
	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdistributor000000000000000000000000000001", 146)
	id := mkMerkleUser(t, db, walletA, 5)
	first, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil || len(first.Entries) != 1 {
		t.Fatalf("first build: %v %+v", err, first)
	}
	markEpochPublished(t, db, first.Distribution.Epoch)
	db.Model(&models.User{}).Where("id = ?", id).Update("tp_balance", 0)
	second, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil || len(second.Entries) != 1 {
		t.Fatalf("second build must keep the published user: %v %+v", err, second)
	}
}

func markEpochPublished(t *testing.T, db *gorm.DB, epoch int64) {
	t.Helper()
	if err := db.Model(&models.RewardDistribution{}).Where("epoch = ?", epoch).
		Update("status", models.RewardDistributionStatusPublished).Error; err != nil {
		t.Fatalf("mark published: %v", err)
	}
}

// An epoch that was built but never published grants nothing on-chain, so it must
// not exempt a user from the Level 3 gate.
func TestBuildDistribution_UnpublishedEpochDoesNotGrandfather(t *testing.T) {
	db := setupMerkleTestDB(t)
	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdistributor000000000000000000000000000001", 146)
	id := mkMerkleUser(t, db, walletA, 5)
	if _, err := svc.BuildDistribution(context.Background(), nil); err != nil {
		t.Fatalf("first build: %v", err)
	}
	db.Model(&models.User{}).Where("id = ?", id).Update("tp_balance", 0)
	_, err := svc.BuildDistribution(context.Background(), nil)
	if err == nil {
		t.Fatalf("second build included a sub-Level 3 user from an unpublished epoch")
	}
}
