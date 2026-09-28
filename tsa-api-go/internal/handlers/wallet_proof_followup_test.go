package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/gorm"
)

// ── pre-fix squats (links made before proofs existed) ────────────────────────

// An account that linked someone's wallet before this fix holds an unproven
// link. It must not lock the real owner out: the owner's valid proof takes the
// wallet and the squat is cleared, with an audit line naming both accounts.
func TestClaim_PreFixSquatIsEvictedByTheOwnersProof(t *testing.T) {
	db := newCallerWalletTestDB(t)
	wallet := newTestWallet(t)
	squatter := models.User{ID: uuid.New(), Username: "squatter", WalletAddress: strings.ToLower(wallet.Address), AccountStatus: models.AccountStatusActive}
	owner := models.User{ID: uuid.New(), Username: "owner", AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &squatter)
	insertTestUser(t, db, &owner)
	logs := captureHandlerLog(t)

	got, ok, w := callerWalletWithProof(&owner, wallet.Address, signedProof(t, db, owner.ID, wallet))
	if !ok || got != wallet.Address {
		t.Fatalf("owner's proven claim: ok=%v code=%d body=%s", ok, w.Code, w.Body.String())
	}
	if storedUserWallet(t, db, squatter.ID) != "" {
		t.Fatal("the unproven squat must be cleared")
	}
	want := "AUDIT wallet_link_evicted=1 owner_user=" + owner.ID.String() + " evicted_users=" + squatter.ID.String()
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("expected %q in the log, got:\n%s", want, logs.String())
	}

	// The squatter's reads of that wallet are over too.
	squatter.WalletAddress = storedUserWallet(t, db, squatter.ID)
	c, rw := testGinContext()
	if _, ok := requireOwnWallet(c, &squatter, wallet.Address); ok {
		t.Fatalf("the evicted account can still read the wallet (code=%d)", rw.Code)
	}
}

// A holder that proved the wallet is never evicted, whoever else proves it.
func TestClaim_ProvenHolderIsNotEvictable(t *testing.T) {
	db := newCallerWalletTestDB(t)
	wallet := newTestWallet(t)
	holder := models.User{ID: uuid.New(), Username: "holder", WalletAddress: wallet.Address, AccountStatus: models.AccountStatusActive}
	other := models.User{ID: uuid.New(), Username: "other", AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &holder)
	insertTestUser(t, db, &other)
	mustRecordWalletProof(t, db, holder.ID, wallet.Address)

	_, ok, w := callerWalletWithProof(&other, wallet.Address, signedProof(t, db, other.ID, wallet))
	if ok || w.Code != http.StatusConflict {
		t.Fatalf("ok=%v code=%d, want 409", ok, w.Code)
	}
	if storedUserWallet(t, db, holder.ID) != wallet.Address {
		t.Fatal("a proven holder lost its wallet")
	}
}

// A proof belongs to the account that made it: X proving A does not make A
// debit-able for Y, who holds A without a proof of its own.
func TestRequireProvenWallet_ProofIsPerAccount(t *testing.T) {
	db := newCallerWalletTestDB(t)
	wallet := newTestWallet(t)
	x := models.User{ID: uuid.New(), Username: "x", AccountStatus: models.AccountStatusActive}
	y := models.User{ID: uuid.New(), Username: "y", WalletAddress: wallet.Address, AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &x)
	insertTestUser(t, db, &y)
	mustRecordWalletProof(t, db, x.ID, wallet.Address)

	c, w := testGinContext()
	if _, ok := requireProvenWallet(c, &y); ok || w.Code != http.StatusForbidden {
		t.Fatalf("ok=%v code=%d, want 403", ok, w.Code)
	}
	mustRecordWalletProof(t, db, y.ID, wallet.Address)
	c, w = testGinContext()
	if got, ok := requireProvenWallet(c, &y); !ok || got != wallet.Address {
		t.Fatalf("y's own proof: ok=%v code=%d", ok, w.Code)
	}
}

// ── challenge message, cap and pruning ───────────────────────────────────────

func TestWalletChallenge_MessageNamesTheAccountAndWhereToSign(t *testing.T) {
	r, user, _ := newWalletProofRouter(t, "")
	wallet := newTestWallet(t)
	_, out := postWalletJSON(t, r, "/wallet/challenge", map[string]string{"walletAddress": wallet.Address})
	msg := out["data"].(map[string]any)["message"].(string)
	for _, want := range []string{
		"TSA Connect: link wallet to account\n\n",
		"\nWallet: " + wallet.Address + "\n",
		"\nAccount: " + user.ID.String() + "\n",
		"\nUser: " + user.Username + "\n",
		"\nPurpose: link-wallet\n",
		"\n\ntsa.mcgpchain.com — only sign this inside the TSA Connect app",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

// The app signs only this exact text (tsa-app services/walletProof.ts parses it
// line for line; its test builds the same golden string). Any change here must
// ship with the app's template, or no wallet can be linked.
func TestWalletProofMessage_Golden(t *testing.T) {
	got := walletProofMessage(
		uuid.MustParse("5f0c3a52-7a1e-4d1b-9f7e-3c2b1a0d9e8f"), "ojay",
		"0x2c7536E3605D9C16a7a3D7b1898e529396a65c23", strings.Repeat("ab", 32),
		time.Date(2026, 9, 28, 12, 10, 0, 0, time.UTC))
	want := "TSA Connect: link wallet to account\n\n" +
		"Sign this message to prove you own this wallet and link it to your TSA Connect account. " +
		"It costs no gas and does not move any funds.\n\n" +
		"Wallet: 0x2c7536E3605D9C16a7a3D7b1898e529396a65c23\n" +
		"Account: 5f0c3a52-7a1e-4d1b-9f7e-3c2b1a0d9e8f\n" +
		"User: ojay\n" +
		"Purpose: link-wallet\n" +
		"Nonce: " + strings.Repeat("ab", 32) + "\n" +
		"Expires: 2026-09-28T12:10:00Z\n\n" +
		"tsa.mcgpchain.com — only sign this inside the TSA Connect app"
	if got != want {
		t.Fatalf("message drifted from the app's template:\n got %q\nwant %q", got, want)
	}
}

func TestWalletProofUserLabel(t *testing.T) {
	cases := []struct {
		user models.User
		want string
	}{
		{models.User{Username: "ojay"}, "ojay"},
		{models.User{Email: "ojay@gmail.com"}, "o***@gmail.com"},
		{models.User{Username: "evil\nPurpose: drain"}, "evilPurpose: drain"},
		{models.User{}, "-"},
	}
	for _, tc := range cases {
		if got := walletProofUserLabel(&tc.user); got != tc.want {
			t.Errorf("label(%+v) = %q, want %q", tc.user, got, tc.want)
		}
	}
}

func TestWalletChallenge_OpenChallengesAreCapped(t *testing.T) {
	r, _, _ := newWalletProofRouter(t, "")
	wallet := newTestWallet(t)
	for i := 0; i < maxOpenWalletChallenges; i++ {
		if w, _ := postWalletJSON(t, r, "/wallet/challenge", map[string]string{"walletAddress": wallet.Address}); w.Code != http.StatusOK {
			t.Fatalf("challenge %d: %d %s", i+1, w.Code, w.Body.String())
		}
	}
	if w, _ := postWalletJSON(t, r, "/wallet/challenge", map[string]string{"walletAddress": wallet.Address}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("challenge past the cap: %d, want 429", w.Code)
	}
}

func TestConsumeWalletProof_KeepsOnlyTheLatestProof(t *testing.T) {
	db := newCallerWalletTestDB(t)
	wallet := newTestWallet(t)
	user := models.User{ID: uuid.New(), Username: "u", AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &user)
	mustRecordWalletProof(t, db, user.ID, wallet.Address)

	latest := signedProof(t, db, user.ID, wallet)
	if err := db.Transaction(func(tx *gorm.DB) error {
		return consumeWalletProof(tx, user.ID, wallet.Address, latest, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	var used []models.WalletLinkChallenge
	db.Where("user_id = ? AND used_at IS NOT NULL", user.ID).Find(&used)
	if len(used) != 1 || used[0].Nonce != latest.Nonce {
		t.Fatalf("kept %d proofs, want only the latest", len(used))
	}
}

type syncBuilder struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *syncBuilder) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *syncBuilder) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

func captureHandlerLog(t *testing.T) *syncBuilder {
	t.Helper()
	buf := &syncBuilder{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

// ── PostgreSQL semantics (skip without DATABASE_URL) ─────────────────────────

func newWalletProofPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	db := testutil.TempPostgres(t, "wallet_proof_test_", &models.WalletLinkChallenge{})
	for _, ddl := range []string{
		`CREATE TABLE users (id uuid PRIMARY KEY, username text, name text, wallet_address text DEFAULT '',
			account_status text DEFAULT 'active', created_at timestamptz, updated_at timestamptz)`,
		`CREATE UNIQUE INDEX uq_users_wallet_address_ci ON users (LOWER(wallet_address)) WHERE wallet_address <> ''`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })
	return db
}

// Requests carrying the same nonce at the same moment: exactly one spends it.
func TestConsumeWalletProof_Postgres_ConcurrentDoubleSpend(t *testing.T) {
	db := newWalletProofPostgres(t)
	wallet := newTestWallet(t)
	user := models.User{ID: uuid.New(), Username: "u", AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &user)
	proof := signedProof(t, db, user.ID, wallet)

	const racers = 8
	errs := make(chan error, racers)
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < racers; i++ {
		go func() {
			start.Wait()
			errs <- db.Transaction(func(tx *gorm.DB) error {
				return consumeWalletProof(tx, user.ID, wallet.Address, proof, time.Now())
			})
		}()
	}
	start.Done()
	wins := 0
	for i := 0; i < racers; i++ {
		if err := <-errs; err == nil {
			wins++
		} else if !errors.Is(err, errWalletProofInvalid) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d requests spent one nonce, want exactly 1", wins)
	}
}

// Two accounts owned by one key holder claim the wallet at once: one links,
// the other gets 409 — from the proven-holder check or the unique index.
func TestClaimWallet_Postgres_ConcurrentClaimsOneWins(t *testing.T) {
	db := newWalletProofPostgres(t)
	wallet := newTestWallet(t)
	users := []*models.User{
		{ID: uuid.New(), Username: "a", AccountStatus: models.AccountStatusActive},
		{ID: uuid.New(), Username: "b", AccountStatus: models.AccountStatusActive},
	}
	proofs := make([]walletProof, len(users))
	for i, u := range users {
		insertTestUser(t, db, u)
		proofs[i] = signedProof(t, db, u.ID, wallet)
	}

	codes := make([]int, len(users))
	oks := make([]bool, len(users))
	var wg, start sync.WaitGroup
	start.Add(1)
	for i := range users {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			_, ok, w := callerWalletWithProof(users[i], wallet.Address, proofs[i])
			oks[i], codes[i] = ok, w.Code
		}(i)
	}
	start.Done()
	wg.Wait()
	if oks[0] == oks[1] {
		t.Fatalf("oks=%v codes=%v, want exactly one winner", oks, codes)
	}
	loser := 0
	if oks[0] {
		loser = 1
	}
	if codes[loser] != http.StatusConflict {
		t.Fatalf("loser code %d, want 409", codes[loser])
	}
}
