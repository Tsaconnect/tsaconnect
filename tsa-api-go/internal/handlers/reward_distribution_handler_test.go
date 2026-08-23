package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ─── ConfirmPublish handler tests (finding #3) ───────────────────────────────
//
// ConfirmPublish flips a built epoch to "published" only after MarkPublished
// verifies the admin-supplied txHash ON-CHAIN: the tx must have mined
// successfully AND the distributor's live merkleRoot() must equal the epoch's
// expected root. These tests cover admin-gating, an unknown epoch, the rejected
// (root-mismatch / failed-tx) paths, and the happy path with the on-chain root
// matching — the chain reads stubbed by a fake verifier (mirroring the
// services-package reconcile tests' fake reader).

const testDistributor = "0xdist0000000000000000000000000000000000a1"

// allEligible structurally satisfies the merkle service's (unexported)
// claimEligibilityChecker — everyone is eligible so BuildDistribution includes
// any user with a positive claimable + wallet.
type allEligible struct{}

func (allEligible) IsClaimEligible(_ context.Context, _ uuid.UUID) (bool, []string, error) {
	return true, nil, nil
}

// fakePublishVerifier stubs the on-chain reads MarkPublished makes. txOK is the
// receipt-status result; onChainRoot is what merkleRoot() returns; the *Err
// fields force a read failure.
type fakePublishVerifier struct {
	txOK        bool
	txErr       error
	onChainRoot string
	rootErr     error
}

func (f *fakePublishVerifier) TxSucceeded(_ context.Context, _ string) (bool, error) {
	if f.txErr != nil {
		return false, f.txErr
	}
	return f.txOK, nil
}

func (f *fakePublishVerifier) OnChainMerkleRoot(_ context.Context, _ string) (string, error) {
	if f.rootErr != nil {
		return "", f.rootErr
	}
	return f.onChainRoot, nil
}

// setupRewardDistTestDB builds an in-memory DB with the users + SP6 distribution
// tables the merkle service reads/writes (raw DDL — IDs set in Go).
func setupRewardDistTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
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
			suspended_from_status TEXT
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

// mkRewardDistUser inserts an active+verified user with a wallet + a claimable so
// BuildDistribution includes them in the tree.
func mkRewardDistUser(t *testing.T, db *gorm.DB, wallet string, claimable float64) uuid.UUID {
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
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if claimable != 0 {
		if err := db.Create(&models.RewardClaimable{
			UserID: id, ClaimableMCGP: claimable, UpdatedAt: time.Now(),
		}).Error; err != nil {
			t.Fatalf("create claimable: %v", err)
		}
	}
	return id
}

// buildConfirmEpoch builds (but does not publish) a distribution and returns the
// configured merkle service + the epoch's expected root.
func buildConfirmEpoch(t *testing.T, verifier services.RewardPublishVerifier) (*services.RewardMerkleService, int64, string) {
	t.Helper()
	db := setupRewardDistTestDB(t)
	mkRewardDistUser(t, db, "0x1111111111111111111111111111111111111111", 100)
	mkRewardDistUser(t, db, "0x2222222222222222222222222222222222222222", 250)
	svc := services.NewRewardMerkleService(db, allEligible{}, testDistributor, 146)
	if verifier != nil {
		svc.SetPublishVerifier(verifier)
	}
	build, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build distribution: %v", err)
	}
	return svc, build.Distribution.Epoch, build.Distribution.MerkleRoot
}

// confirmRequestBody marshals the confirm POST body.
func confirmRequestBody(t *testing.T, epoch int64, txHash string) *bytes.Reader {
	t.Helper()
	b, err := json.Marshal(map[string]interface{}{"epoch": epoch, "txHash": txHash})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return bytes.NewReader(b)
}

// distStatus reads an epoch's persisted status from the service's DB via a fresh
// list (the handler/service share the same *gorm.DB).
func distStatus(t *testing.T, svc *services.RewardMerkleService, epoch int64) string {
	t.Helper()
	rows, err := svc.ListDistributions(context.Background(), 100)
	if err != nil {
		t.Fatalf("list distributions: %v", err)
	}
	for _, r := range rows {
		if r.Epoch == epoch {
			return r.Status
		}
	}
	t.Fatalf("epoch %d not found in list", epoch)
	return ""
}

// driveConfirm runs ConfirmPublish with the given user in context + JSON body.
func driveConfirm(h *RewardDistributionHandler, user *models.User, body *bytes.Reader) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if user != nil {
		c.Set("user", user)
	}
	c.Request, _ = http.NewRequest("POST", "/admin/reward-distributions/confirm", body)
	c.Request.Header.Set("Content-Type", "application/json")
	h.ConfirmPublish(c)
	return w
}

// TestConfirmPublish_RequiresAdmin: no user in context → 401 (admin-gating).
func TestConfirmPublish_RequiresAdmin(t *testing.T) {
	svc, epoch, _ := buildConfirmEpoch(t, &fakePublishVerifier{txOK: true})
	h := NewRewardDistributionHandler(svc)

	w := driveConfirm(h, nil, confirmRequestBody(t, epoch, "0xabc"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without an admin user, got %d: %s", w.Code, w.Body.String())
	}
	// Epoch must remain unpublished.
	if s := distStatus(t, svc, epoch); s != models.RewardDistributionStatusPending {
		t.Fatalf("epoch status = %q, want pending (unauthorized must not publish)", s)
	}
}

// TestConfirmPublish_BadEpochRejected: a non-existent epoch → 404, nothing flips.
func TestConfirmPublish_BadEpochRejected(t *testing.T) {
	svc, epoch, _ := buildConfirmEpoch(t, &fakePublishVerifier{txOK: true})
	h := NewRewardDistributionHandler(svc)
	admin := &models.User{ID: uuid.New(), Role: "admin"}

	w := driveConfirm(h, admin, confirmRequestBody(t, epoch+999, "0xabc"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown epoch, got %d: %s", w.Code, w.Body.String())
	}
	// The real epoch is untouched.
	if s := distStatus(t, svc, epoch); s != models.RewardDistributionStatusPending {
		t.Fatalf("real epoch status = %q, want pending", s)
	}
}

// TestConfirmPublish_HappyPath_OnChainRootMatches: tx succeeded + the on-chain
// root equals the epoch's expected root → 200, status flips to published, the
// txHash is recorded.
func TestConfirmPublish_HappyPath_OnChainRootMatches(t *testing.T) {
	// Build first to learn the expected root, then point the verifier's on-chain
	// root at it.
	svc, epoch, expectedRoot := buildConfirmEpoch(t, nil)
	svc.SetPublishVerifier(&fakePublishVerifier{txOK: true, onChainRoot: expectedRoot})
	h := NewRewardDistributionHandler(svc)
	admin := &models.User{ID: uuid.New(), Role: "admin"}

	txHash := "0x" + fmt.Sprintf("%064x", 1)
	w := driveConfirm(h, admin, confirmRequestBody(t, epoch, txHash))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on a matching on-chain root, got %d: %s", w.Code, w.Body.String())
	}
	if s := distStatus(t, svc, epoch); s != models.RewardDistributionStatusPublished {
		t.Fatalf("epoch status = %q, want published", s)
	}
}

// TestConfirmPublish_RootMismatchRejected: tx succeeded but the on-chain root is a
// DIFFERENT root (wrong/early txHash) → 422, the epoch stays unpublished so its
// claims aren't stranded behind a root that was never set.
func TestConfirmPublish_RootMismatchRejected(t *testing.T) {
	wrongRoot := "0x" + fmt.Sprintf("%064x", 0xdead)
	svc, epoch, _ := buildConfirmEpoch(t, &fakePublishVerifier{txOK: true, onChainRoot: wrongRoot})
	h := NewRewardDistributionHandler(svc)
	admin := &models.User{ID: uuid.New(), Role: "admin"}

	w := driveConfirm(h, admin, confirmRequestBody(t, epoch, "0xabc"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on a root mismatch, got %d: %s", w.Code, w.Body.String())
	}
	if s := distStatus(t, svc, epoch); s != models.RewardDistributionStatusPending {
		t.Fatalf("epoch status = %q, want pending (root mismatch must not publish)", s)
	}
}

// TestConfirmPublish_TxNotSucceededRejected: the setMerkleRoot tx did not mine
// successfully (receipt status != 1) → 422, epoch stays unpublished. The verifier
// is given the CORRECT on-chain root to prove it's the tx-status guard (which runs
// first) that rejects, not a root mismatch.
func TestConfirmPublish_TxNotSucceededRejected(t *testing.T) {
	svc, epoch, expectedRoot := buildConfirmEpoch(t, nil)
	svc.SetPublishVerifier(&fakePublishVerifier{txOK: false, onChainRoot: expectedRoot})
	h := NewRewardDistributionHandler(svc)
	admin := &models.User{ID: uuid.New(), Role: "admin"}

	w := driveConfirm(h, admin, confirmRequestBody(t, epoch, "0xabc"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on a failed tx, got %d: %s", w.Code, w.Body.String())
	}
	if s := distStatus(t, svc, epoch); s != models.RewardDistributionStatusPending {
		t.Fatalf("epoch status = %q, want pending (failed tx must not publish)", s)
	}
}
