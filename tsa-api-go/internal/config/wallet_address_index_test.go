package config

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/gorm"
)

func openWalletIndexDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openPayMigrateDB(t)
	prepareWalletIndexSchema(t, db)
	return db
}

// prepareWalletIndexSchema is the slice of production schema the migration
// reads: users with the case-sensitive index it has today, and the proofs.
func prepareWalletIndexSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, wallet_address TEXT DEFAULT '',
		account_status TEXT DEFAULT 'active', created_at TIMESTAMP)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_users_wallet_address ON users (wallet_address) WHERE wallet_address != ''`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.WalletLinkChallenge{}); err != nil {
		t.Fatal(err)
	}
}

func insertWalletUser(t *testing.T, db *gorm.DB, id, wallet string) {
	t.Helper()
	insertWalletUserStatus(t, db, id, wallet, "active")
}

func insertWalletUserStatus(t *testing.T, db *gorm.DB, id, wallet, status string) {
	t.Helper()
	if err := db.Exec(`INSERT INTO users (id, wallet_address, account_status, created_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`,
		id, wallet, status).Error; err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

// recordProof leaves what an accepted wallet proof leaves: a used challenge.
func recordProof(t *testing.T, db *gorm.DB, userID, wallet string) {
	t.Helper()
	if err := db.Exec(`INSERT INTO wallet_link_challenges (id, user_id, address, nonce, message, expires_at, used_at, created_at)
		VALUES (?, ?, ?, ?, 'test', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		uuid.NewString(), userID, strings.ToLower(wallet), strings.ReplaceAll(uuid.NewString(), "-", "")).Error; err != nil {
		t.Fatalf("record proof: %v", err)
	}
}

func walletOf(t *testing.T, db *gorm.DB, id string) string {
	t.Helper()
	var w string
	if err := db.Raw(`SELECT wallet_address FROM users WHERE id = ?`, id).Scan(&w).Error; err != nil {
		t.Fatal(err)
	}
	return w
}

func indexExists(t *testing.T, db *gorm.DB, name string) bool {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

const (
	walletA = "0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce"
	walletB = "0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96"
)

// Production may hold one address on two accounts in different case. The
// migration must not fail the boot on them: it alerts with the user ids, keeps
// the case-sensitive index, and still normalises every row it safely can.
func TestMigrateWalletAddressUniqueness_DuplicatesAlertAndDoNotFail(t *testing.T) {
	db := openWalletIndexDB(t)
	insertWalletUser(t, db, "victim", walletA)
	insertWalletUser(t, db, "attacker", strings.ToLower(walletA))
	insertWalletUser(t, db, "legacy", strings.ToLower(walletB))
	insertWalletUser(t, db, "nowallet", "")
	logs := captureLog(t)

	migrateWalletAddressUniqueness(db)

	out := logs.String()
	if !strings.Contains(out, "ALERT wallet_address_case_duplicates=1") ||
		!strings.Contains(out, "user_ids=victim,attacker") && !strings.Contains(out, "user_ids=attacker,victim") {
		t.Fatalf("expected an ALERT naming both accounts, got:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), strings.ToLower(walletA)) {
		t.Fatalf("the alert must name user ids only, not addresses:\n%s", out)
	}
	if indexExists(t, db, walletAddressCIIndex) {
		t.Fatal("the case-insensitive index cannot hold with duplicates present")
	}
	if !indexExists(t, db, walletAddressLegacyIndex) {
		t.Fatal("the case-sensitive index must be kept until the duplicates are resolved")
	}
	if got := walletOf(t, db, "legacy"); got != walletB {
		t.Fatalf("a non-duplicate row must be normalised to checksum, got %q", got)
	}
	if walletOf(t, db, "victim") != walletA || walletOf(t, db, "attacker") != strings.ToLower(walletA) {
		t.Fatal("duplicate rows must be left for a human to resolve")
	}
}

func TestMigrateWalletAddressUniqueness_CleanTableGetsCaseInsensitiveIndex(t *testing.T) {
	db := openWalletIndexDB(t)
	insertWalletUser(t, db, "one", strings.ToLower(walletA))
	insertWalletUser(t, db, "two", walletB)
	insertWalletUser(t, db, "empty1", "")
	insertWalletUser(t, db, "empty2", "")

	migrateWalletAddressUniqueness(db)
	migrateWalletAddressUniqueness(db) // idempotent

	if got := walletOf(t, db, "one"); got != common.HexToAddress(walletA).Hex() {
		t.Fatalf("expected checksum form, got %q", got)
	}
	if !indexExists(t, db, walletAddressCIIndex) {
		t.Fatal("case-insensitive index not built")
	}
	if indexExists(t, db, walletAddressLegacyIndex) {
		t.Fatal("the redundant case-sensitive index should be dropped")
	}
	if err := db.Exec(`INSERT INTO users (id, wallet_address) VALUES ('three', '0x000000000000000000000000000000000000dEaD')`).Error; err != nil {
		t.Fatalf("a different address must still insert: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, wallet_address) VALUES ('four', ?)`, strings.ToLower(walletB)).Error; err == nil {
		t.Fatal("a case variant of a held address must violate the unique index")
	}
	if err := db.Exec(`INSERT INTO users (id, wallet_address) VALUES ('empty3', '')`).Error; err != nil {
		t.Fatalf("empty addresses must not collide: %v", err)
	}
}

// A pre-fix squat on a wallet whose owner has since proved it must not keep
// the index from building: the unproven link is cleared and audited.
func TestMigrateWalletAddressUniqueness_ProvenOwnerResolvesItsGroup(t *testing.T) {
	db := openWalletIndexDB(t)
	insertWalletUser(t, db, "owner", strings.ToLower(walletA))
	insertWalletUser(t, db, "squatter", walletA)
	recordProof(t, db, "owner", walletA)
	logs := captureLog(t)

	migrateWalletAddressUniqueness(db)

	out := logs.String()
	if !strings.Contains(out, "AUDIT wallet_link_evicted=1 owner_user=owner evicted_users=squatter") {
		t.Fatalf("expected an AUDIT line naming both accounts, got:\n%s", out)
	}
	if strings.Contains(out, "ALERT wallet_address_case_duplicates") {
		t.Fatalf("a resolvable group must not alert:\n%s", out)
	}
	if walletOf(t, db, "squatter") != "" || walletOf(t, db, "owner") != walletA {
		t.Fatalf("owner=%q squatter=%q, want the owner on the checksummed wallet and the squat cleared",
			walletOf(t, db, "owner"), walletOf(t, db, "squatter"))
	}
	if !indexExists(t, db, walletAddressCIIndex) {
		t.Fatal("case-insensitive index not built after resolving the group")
	}
}

// Two proven holders is not something the migration may decide.
func TestMigrateWalletAddressUniqueness_TwoProvenHoldersStillAlert(t *testing.T) {
	db := openWalletIndexDB(t)
	insertWalletUser(t, db, "a", strings.ToLower(walletA))
	insertWalletUser(t, db, "b", walletA)
	recordProof(t, db, "a", walletA)
	recordProof(t, db, "b", walletA)
	logs := captureLog(t)

	migrateWalletAddressUniqueness(db)

	if !strings.Contains(logs.String(), "ALERT wallet_address_case_duplicates=1") {
		t.Fatalf("expected an ALERT, got:\n%s", logs.String())
	}
	if walletOf(t, db, "a") == "" || walletOf(t, db, "b") == "" {
		t.Fatal("neither proven link may be cleared automatically")
	}
}

func TestMigrateWalletAddressUniqueness_DeletedAccountsDoNotBlockTheIndex(t *testing.T) {
	db := openWalletIndexDB(t)
	insertWalletUserStatus(t, db, "gone", strings.ToLower(walletA), "deleted")
	insertWalletUser(t, db, "live", walletA)

	migrateWalletAddressUniqueness(db)

	if walletOf(t, db, "gone") != "" {
		t.Fatal("a deleted account's wallet must be released")
	}
	if !indexExists(t, db, walletAddressCIIndex) {
		t.Fatal("a deleted account's stale wallet must not block the index")
	}
}

// Once the index is there, boots do not scan users again.
func TestMigrateWalletAddressUniqueness_NoScanOnceIndexed(t *testing.T) {
	db := openWalletIndexDB(t)
	migrateWalletAddressUniqueness(db)
	insertWalletUser(t, db, "later", strings.ToLower(walletB))

	migrateWalletAddressUniqueness(db)

	if got := walletOf(t, db, "later"); got != strings.ToLower(walletB) {
		t.Fatalf("an indexed database was scanned again (row rewritten to %q)", got)
	}
}

// The same migration against real PostgreSQL: the CONCURRENTLY build, the
// validity check and the functional index. Skips without DATABASE_URL.
func TestMigrateWalletAddressUniqueness_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "wallet_idx_test_")
	prepareWalletIndexSchema(t, db)
	owner, squatter, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	insertWalletUser(t, db, owner, strings.ToLower(walletA))
	insertWalletUser(t, db, squatter, walletA)
	insertWalletUser(t, db, other, strings.ToLower(walletB))
	logs := captureLog(t)

	migrateWalletAddressUniqueness(db)
	if !strings.Contains(logs.String(), "ALERT wallet_address_case_duplicates=") || walletAddressCIIndexReady(db) {
		t.Fatalf("unproven duplicates must alert and leave the index unbuilt:\n%s", logs.String())
	}

	recordProof(t, db, owner, walletA)
	migrateWalletAddressUniqueness(db)

	if !walletAddressCIIndexReady(db) {
		t.Fatalf("index not built (or invalid) after the groups resolved:\n%s", logs.String())
	}
	if walletOf(t, db, squatter) != "" || walletOf(t, db, owner) != walletA || walletOf(t, db, other) != walletB {
		t.Fatal("the proven owner must keep the checksummed wallet, the squat be cleared, the rest normalised")
	}
	if err := db.Exec(`INSERT INTO users (id, wallet_address) VALUES (?, ?)`, uuid.NewString(), strings.ToLower(walletA)).Error; err == nil {
		t.Fatal("a case variant of a held address must violate the unique index")
	}
}

// AutoMigrate cannot run under SQLite, so its wiring is asserted on the source.
func TestAutoMigrateRunsTheWalletAddressUniquenessMigration(t *testing.T) {
	raw, err := os.ReadFile("database.go")
	if err != nil {
		t.Fatalf("read database.go: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, "migrateWalletAddressUniqueness(DB)") {
		t.Fatal("AutoMigrate no longer runs migrateWalletAddressUniqueness")
	}
	if strings.Contains(src, `CREATE UNIQUE INDEX IF NOT EXISTS idx_users_wallet_address ON users (wallet_address)`) {
		t.Fatal("database.go rebuilds the case-sensitive wallet index directly again")
	}
}
