package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupReferralGraphTestDB opens an in-memory SQLite DB with a users table
// created via raw DDL (avoiding the PostgreSQL gen_random_uuid() default that
// AutoMigrate would emit). Mirrors the handlers/tp_handler_test.go setup.
//
// WITH RECURSIVE is exercised here exactly as it runs in production against
// PostgreSQL — the SQL is identical; only the engine differs.
func setupReferralGraphTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE users (
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
		persona_inquiry_id TEXT,
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
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	return db
}

// mkUser inserts a user with the given referrer (nil = root) and account status.
func mkUser(t *testing.T, db *gorm.DB, username string, referredBy *uuid.UUID, status string) models.User {
	t.Helper()
	id := uuid.New()
	u := models.User{
		ID:            id,
		Name:          username,
		Username:      username,
		Email:         username + "@test.com",
		Password:      "x",
		ReferralCode:  username,
		ReferredBy:    referredBy,
		AccountStatus: status,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

// ptr returns a pointer to the given UUID.
func ptr(id uuid.UUID) *uuid.UUID { return &id }

// softDelete marks a user soft-deleted exactly as DeleteAccount does
// (user_handler.go:461-462): sets BOTH account_status='deleted' and deleted_at.
func softDelete(t *testing.T, db *gorm.DB, id uuid.UUID) {
	t.Helper()
	now := time.Now()
	if err := db.Model(&models.User{}).Where("id = ?", id).Updates(map[string]interface{}{
		"account_status": models.AccountStatusDeleted,
		"deleted_at":     now,
	}).Error; err != nil {
		t.Fatalf("soft-delete %s: %v", id, err)
	}
}

// buildChain creates root <- g1 <- g2 <- ... <- gN (gN is the deepest leaf).
// Returns the slice [root, g1, ..., gN]. Index i is i hops above the leaf.
func buildChain(t *testing.T, db *gorm.DB, n int) []models.User {
	t.Helper()
	users := make([]models.User, 0, n+1)
	var parent *uuid.UUID
	for i := 0; i <= n; i++ {
		name := "chain_" + uuid.NewString()[:8]
		u := mkUser(t, db, name, parent, models.AccountStatusActive)
		users = append(users, u)
		parent = ptr(u.ID)
	}
	return users
}

func TestUplines_ChainGenerationsAndOrder(t *testing.T) {
	db := setupReferralGraphTestDB(t)
	svc := NewReferralGraphService(db)

	// gen1..gen5: u0(root) <- u1 <- u2 <- u3 <- u4 <- u5(leaf).
	u := buildChain(t, db, 5)
	leaf := u[5]

	got, err := svc.Uplines(context.Background(), leaf.ID, 10)
	if err != nil {
		t.Fatalf("Uplines: %v", err)
	}
	// 5 ancestors above the leaf (u4..u0).
	if len(got) != 5 {
		t.Fatalf("expected 5 ancestors, got %d", len(got))
	}
	// Generation order: gen1 = direct referrer (u4) ... gen5 = u0.
	wantOrder := []uuid.UUID{u[4].ID, u[3].ID, u[2].ID, u[1].ID, u[0].ID}
	for i, a := range got {
		if a.Generation != i+1 {
			t.Errorf("ancestor %d: generation = %d, want %d", i, a.Generation, i+1)
		}
		if a.UserID != wantOrder[i] {
			t.Errorf("ancestor %d (gen %d): id = %s, want %s", i, a.Generation, a.UserID, wantOrder[i])
		}
		if !a.Active {
			t.Errorf("ancestor %d: expected Active=true", i)
		}
	}
}

func TestUplines_MaxGenBoundRespected(t *testing.T) {
	db := setupReferralGraphTestDB(t)
	svc := NewReferralGraphService(db)

	u := buildChain(t, db, 5) // 5 ancestors available
	leaf := u[5]

	got, err := svc.Uplines(context.Background(), leaf.ID, 3)
	if err != nil {
		t.Fatalf("Uplines: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("maxGen=3: expected 3 ancestors, got %d", len(got))
	}
	if got[len(got)-1].Generation != 3 {
		t.Errorf("deepest returned generation = %d, want 3", got[len(got)-1].Generation)
	}
	// gen3 must be u2 (3 hops above leaf u5).
	if got[2].UserID != u[2].ID {
		t.Errorf("gen3 id = %s, want %s", got[2].UserID, u[2].ID)
	}
}

func TestUplines_InactiveMidChainFlaggedAndWalkContinues(t *testing.T) {
	db := setupReferralGraphTestDB(t)
	svc := NewReferralGraphService(db)

	// root(active) <- a(active) <- b(SUSPENDED) <- c(active) <- leaf(active)
	root := mkUser(t, db, "root", nil, models.AccountStatusActive)
	a := mkUser(t, db, "a", ptr(root.ID), models.AccountStatusActive)
	b := mkUser(t, db, "b", ptr(a.ID), models.AccountStatusSuspended) // inactive mid-chain
	c := mkUser(t, db, "c", ptr(b.ID), models.AccountStatusActive)
	leaf := mkUser(t, db, "leaf", ptr(c.ID), models.AccountStatusActive)

	got, err := svc.Uplines(context.Background(), leaf.ID, 10)
	if err != nil {
		t.Fatalf("Uplines: %v", err)
	}
	// CRITICAL: the walk must NOT stop at the inactive ancestor b. All 4
	// ancestors (c, b, a, root) must come back, with b flagged inactive.
	if len(got) != 4 {
		t.Fatalf("expected 4 ancestors past the inactive node, got %d", len(got))
	}
	wantOrder := []uuid.UUID{c.ID, b.ID, a.ID, root.ID}
	wantActive := []bool{true, false, true, true} // b is suspended
	for i, anc := range got {
		if anc.Generation != i+1 {
			t.Errorf("ancestor %d: generation = %d, want %d", i, anc.Generation, i+1)
		}
		if anc.UserID != wantOrder[i] {
			t.Errorf("ancestor %d: id = %s, want %s", i, anc.UserID, wantOrder[i])
		}
		if anc.Active != wantActive[i] {
			t.Errorf("ancestor %d (id %s): Active = %v, want %v", i, anc.UserID, anc.Active, wantActive[i])
		}
	}
	// And specifically: the ancestors ABOVE the inactive node are present.
	if got[1].UserID != b.ID || got[1].Active {
		t.Errorf("gen2 should be inactive b, got id=%s active=%v", got[1].UserID, got[1].Active)
	}
	if got[3].UserID != root.ID {
		t.Errorf("gen4 should be root (above the inactive node), got %s", got[3].UserID)
	}
}

func TestUplines_CycleTerminatesAtMaxGen(t *testing.T) {
	db := setupReferralGraphTestDB(t)
	svc := NewReferralGraphService(db)

	// Build A and B referring each other: A.referred_by = B, B.referred_by = A.
	a := mkUser(t, db, "cyc_a", nil, models.AccountStatusActive)
	b := mkUser(t, db, "cyc_b", ptr(a.ID), models.AccountStatusActive)
	// Close the cycle: A now points back at B.
	if err := db.Model(&models.User{}).Where("id = ?", a.ID).Update("referred_by", b.ID).Error; err != nil {
		t.Fatalf("close cycle: %v", err)
	}

	done := make(chan struct{})
	var got []Ancestor
	var err error
	go func() {
		got, err = svc.Uplines(context.Background(), a.ID, 10)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Uplines hung on a referral cycle (no termination)")
	}
	if err != nil {
		t.Fatalf("Uplines on cycle: %v", err)
	}
	// Each node is visited at most once on the path, so from A we reach B (gen1)
	// then A is already on the path and the climb stops. Bounded, finite.
	if len(got) == 0 {
		t.Fatalf("expected at least the direct referrer, got 0")
	}
	if len(got) > 2 {
		t.Fatalf("cycle produced %d ancestors; node revisited (cycle guard failed)", len(got))
	}
	if got[0].UserID != b.ID || got[0].Generation != 1 {
		t.Errorf("gen1 should be b, got id=%s gen=%d", got[0].UserID, got[0].Generation)
	}
	// Confirm no node appears twice.
	seen := map[uuid.UUID]bool{}
	for _, anc := range got {
		if seen[anc.UserID] {
			t.Fatalf("node %s revisited in cycle walk", anc.UserID)
		}
		seen[anc.UserID] = true
	}
}

func TestUplines_SelfReferenceDoesNotLoop(t *testing.T) {
	db := setupReferralGraphTestDB(t)
	svc := NewReferralGraphService(db)

	u := mkUser(t, db, "selfref", nil, models.AccountStatusActive)
	if err := db.Model(&models.User{}).Where("id = ?", u.ID).Update("referred_by", u.ID).Error; err != nil {
		t.Fatalf("self-ref: %v", err)
	}

	done := make(chan struct{})
	var got []Ancestor
	var err error
	go func() {
		got, err = svc.Uplines(context.Background(), u.ID, 10)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Uplines hung on a self-reference")
	}
	if err != nil {
		t.Fatalf("Uplines self-ref: %v", err)
	}
	// The node is already on the path from depth 0, so it is never re-added.
	if len(got) != 0 {
		t.Fatalf("self-reference should yield 0 ancestors, got %d", len(got))
	}
}

func TestDownlineCounts_PerGeneration(t *testing.T) {
	db := setupReferralGraphTestDB(t)
	svc := NewReferralGraphService(db)

	// root
	//  ├── c1 ── gc1
	//  │        └ gc2
	//  └── c2
	// gen1: c1, c2 (2); gen2: gc1, gc2 (2).
	root := mkUser(t, db, "dl_root", nil, models.AccountStatusActive)
	c1 := mkUser(t, db, "dl_c1", ptr(root.ID), models.AccountStatusActive)
	_ = mkUser(t, db, "dl_c2", ptr(root.ID), models.AccountStatusActive)
	_ = mkUser(t, db, "dl_gc1", ptr(c1.ID), models.AccountStatusActive)
	_ = mkUser(t, db, "dl_gc2", ptr(c1.ID), models.AccountStatusSuspended)

	counts, err := svc.DownlineCounts(context.Background(), root.ID, 10)
	if err != nil {
		t.Fatalf("DownlineCounts: %v", err)
	}
	if counts[1] != 2 {
		t.Errorf("gen1 count = %d, want 2", counts[1])
	}
	if counts[2] != 2 {
		t.Errorf("gen2 count = %d, want 2", counts[2])
	}
	if _, ok := counts[3]; ok {
		t.Errorf("gen3 should be absent, got %d", counts[3])
	}
}

func TestDownlineCounts_MaxGenBound(t *testing.T) {
	db := setupReferralGraphTestDB(t)
	svc := NewReferralGraphService(db)

	u := buildChain(t, db, 5) // root has 5 generations below it
	root := u[0]

	counts, err := svc.DownlineCounts(context.Background(), root.ID, 3)
	if err != nil {
		t.Fatalf("DownlineCounts: %v", err)
	}
	for g := 1; g <= 3; g++ {
		if counts[g] != 1 {
			t.Errorf("gen%d count = %d, want 1", g, counts[g])
		}
	}
	if _, ok := counts[4]; ok {
		t.Errorf("maxGen=3: gen4 must be excluded, got %d", counts[4])
	}
}

func TestDownlines_PaginatedAtGeneration(t *testing.T) {
	db := setupReferralGraphTestDB(t)
	svc := NewReferralGraphService(db)

	root := mkUser(t, db, "p_root", nil, models.AccountStatusActive)
	// 3 direct children at gen1, with deterministic usernames for ordering.
	mkUser(t, db, "p_aaa", ptr(root.ID), models.AccountStatusActive)
	mkUser(t, db, "p_bbb", ptr(root.ID), models.AccountStatusSuspended)
	mkUser(t, db, "p_ccc", ptr(root.ID), models.AccountStatusActive)
	// A gen2 descendant that must NOT appear in a gen1 query.
	c := mkUser(t, db, "p_ddd_gen2parent", ptr(root.ID), models.AccountStatusActive)
	mkUser(t, db, "p_zzz_gen2", ptr(c.ID), models.AccountStatusActive)

	page1, err := svc.Downlines(context.Background(), root.ID, 1, 2, 0)
	if err != nil {
		t.Fatalf("Downlines page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1: expected 2, got %d", len(page1))
	}
	// Ordered by username asc: p_aaa, p_bbb.
	if page1[0].Username != "p_aaa" || page1[1].Username != "p_bbb" {
		t.Errorf("page1 order = [%s, %s], want [p_aaa, p_bbb]", page1[0].Username, page1[1].Username)
	}
	if page1[0].Generation != 1 {
		t.Errorf("page1[0] generation = %d, want 1", page1[0].Generation)
	}
	// p_bbb is suspended -> Active must be false.
	if page1[1].Username == "p_bbb" && page1[1].Active {
		t.Errorf("p_bbb should be inactive")
	}

	page2, err := svc.Downlines(context.Background(), root.ID, 1, 2, 2)
	if err != nil {
		t.Fatalf("Downlines page2: %v", err)
	}
	// gen1 has 4 children total (aaa, bbb, ccc, ddd_gen2parent); page2 = remaining 2.
	if len(page2) != 2 {
		t.Fatalf("page2: expected 2, got %d", len(page2))
	}
	if page2[0].Username != "p_ccc" {
		t.Errorf("page2[0] = %s, want p_ccc", page2[0].Username)
	}
	// The gen2 node must never appear in a gen1 query.
	for _, d := range append(append([]Descendant{}, page1...), page2...) {
		if d.Username == "p_zzz_gen2" {
			t.Errorf("gen2 descendant leaked into gen1 query")
		}
		if d.Generation != 1 {
			t.Errorf("descendant %s has generation %d in a gen1 query", d.Username, d.Generation)
		}
	}

	// Generation 2 query returns exactly the one gen2 node.
	gen2, err := svc.Downlines(context.Background(), root.ID, 2, 50, 0)
	if err != nil {
		t.Fatalf("Downlines gen2: %v", err)
	}
	if len(gen2) != 1 || gen2[0].Username != "p_zzz_gen2" {
		t.Fatalf("gen2 query = %+v, want single p_zzz_gen2", gen2)
	}
	if gen2[0].Generation != 2 {
		t.Errorf("gen2 node generation = %d, want 2", gen2[0].Generation)
	}
}

// TestDownlines_SoftDeletedExcluded_UplineDeletedRetained pins the SP2 finding
// fix: soft-deleted DESCENDANTS must be absent from DownlineCounts/Downlines
// (matching the existing referrals UI), while a soft-deleted ANCESTOR must
// still be returned by Uplines (flagged Active=false) so SP3 can route its
// generation share to the global pool.
func TestDownlines_SoftDeletedExcluded_UplineDeletedRetained(t *testing.T) {
	db := setupReferralGraphTestDB(t)
	svc := NewReferralGraphService(db)

	// root <- live (active) , root <- gone (soft-deleted) at gen1.
	// live <- liveGc (active) at gen2; gone <- goneGc (active) at gen2 — goneGc
	// sits under a deleted parent so it must also drop out of the descent.
	root := mkUser(t, db, "sd_root", nil, models.AccountStatusActive)
	live := mkUser(t, db, "sd_live", ptr(root.ID), models.AccountStatusActive)
	gone := mkUser(t, db, "sd_gone", ptr(root.ID), models.AccountStatusActive)
	_ = mkUser(t, db, "sd_live_gc", ptr(live.ID), models.AccountStatusActive)
	_ = mkUser(t, db, "sd_gone_gc", ptr(gone.ID), models.AccountStatusActive)
	softDelete(t, db, gone.ID)

	// DownlineCounts: gen1 = only live (1, not 2); gen2 = only liveGc (1, not 2).
	counts, err := svc.DownlineCounts(context.Background(), root.ID, 10)
	if err != nil {
		t.Fatalf("DownlineCounts: %v", err)
	}
	if counts[1] != 1 {
		t.Errorf("gen1 count = %d, want 1 (soft-deleted child excluded)", counts[1])
	}
	if counts[2] != 1 {
		t.Errorf("gen2 count = %d, want 1 (descendant of deleted parent pruned)", counts[2])
	}

	// Downlines gen1: the soft-deleted child must not appear.
	gen1, err := svc.Downlines(context.Background(), root.ID, 1, 50, 0)
	if err != nil {
		t.Fatalf("Downlines gen1: %v", err)
	}
	if len(gen1) != 1 {
		t.Fatalf("Downlines gen1: expected 1 (deleted child excluded), got %d", len(gen1))
	}
	for _, d := range gen1 {
		if d.UserID == gone.ID {
			t.Errorf("soft-deleted descendant %s leaked into Downlines", gone.ID)
		}
	}
	if gen1[0].UserID != live.ID {
		t.Errorf("Downlines gen1 = %s, want live %s", gen1[0].UserID, live.ID)
	}

	// Uplines: a soft-deleted ANCESTOR is still returned, flagged inactive.
	// Chain: anc(soft-deleted) <- child(active) <- grandchild(active).
	anc := mkUser(t, db, "sd_anc", nil, models.AccountStatusActive)
	child := mkUser(t, db, "sd_child", ptr(anc.ID), models.AccountStatusActive)
	grandchild := mkUser(t, db, "sd_grandchild", ptr(child.ID), models.AccountStatusActive)
	softDelete(t, db, anc.ID)

	ups, err := svc.Uplines(context.Background(), grandchild.ID, 10)
	if err != nil {
		t.Fatalf("Uplines: %v", err)
	}
	if len(ups) != 2 {
		t.Fatalf("Uplines: expected 2 ancestors (deleted ancestor retained), got %d", len(ups))
	}
	// gen1 = child (active), gen2 = anc (deleted -> Active=false but PRESENT).
	if ups[0].UserID != child.ID || !ups[0].Active {
		t.Errorf("gen1 = {%s active=%v}, want live child %s active", ups[0].UserID, ups[0].Active, child.ID)
	}
	if ups[1].UserID != anc.ID {
		t.Fatalf("gen2 = %s, want soft-deleted ancestor %s (must be retained)", ups[1].UserID, anc.ID)
	}
	if ups[1].Active {
		t.Errorf("soft-deleted ancestor %s must be flagged Active=false", anc.ID)
	}
}
