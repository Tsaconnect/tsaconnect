package services

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ── What this harness does and does not prove ────────────────────────────────
//
// SetMaxOpenConns(1) is load-bearing, not tidiness. gorm.io/driver/sqlite gives
// every pooled connection to ":memory:" its OWN empty database, and
// newSQLiteForPM sets no connection limit — so under goroutine fan-out most
// workers land in a fresh, tableless DB and error out. A concurrency test on
// that harness passes for the worst possible reason: "nobody oversold" is
// trivially true when most callers never reached the table. Nine other call
// sites in this repo already set the limit; the PM helper simply missed it.
//
// What these timing tests DO prove: the operations produce correct totals under
// fan-out, the dual-write stays in step, and currencies stay isolated.
//
// What they do NOT prove — and this was verified by mutation, not assumed —
// is atomicity. Each op runs in a transaction, and with one connection that
// transaction holds it for its whole duration, so nothing interleaves and a
// read-modify-write implementation passes these tests unchanged. It would still
// be unsafe in production: under Postgres READ COMMITTED with a real pool, two
// concurrent transactions can both read the old balance and both write.
//
// The atomicity regression is therefore caught structurally instead, by
// TestLedgerOps_AreSingleGuardedStatements at the bottom of this file — "one
// guarded UPDATE, no prior SELECT of the mutated row" is the property that
// makes these safe under a real database, and it is directly observable rather
// than dependent on timing.
//
// Neither approach exercises Postgres row-locking, EvalPlanQual re-evaluation,
// or true numeric semantics (SQLite's NUMERIC over TEXT is not Postgres
// numeric). That gap is accepted deliberately — this repo has no
// Postgres-backed test infrastructure and adding it was scoped out.
//
// The assertions matter as much as the harness. A lost update makes the final
// row UNDERSTATE, so `reserved <= available` and `reserved <= ceiling` both PASS
// on broken code. Only the pair below would fail on it:
//
//	successCount == what the balance can fund   AND   row == successCount × amount

func newSQLiteForPMConcurrent(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	// See the header: without this, fan-out silently lands in empty databases.
	sqlDB.SetMaxOpenConns(1)

	if err := db.Exec(`CREATE TABLE IF NOT EXISTS payment_merchants (
		id                        TEXT PRIMARY KEY,
		user_id                   TEXT NOT NULL,
		name                      TEXT NOT NULL,
		status                    TEXT NOT NULL DEFAULT 'pending',
		kind TEXT NOT NULL DEFAULT 'merchant', pool_chain_id INTEGER,
		nomba_account_id          TEXT,
		nomba_wallet_id           TEXT,
		nomba_creds_ref           TEXT,
		inbound_account_number    TEXT,
		inbound_bank              TEXT,
		nomba_account_ref         TEXT,
		operator_wallet_address   TEXT,
		nomba_sub_account_id      TEXT,
		settlement_bank_code      TEXT,
		settlement_account_number TEXT,
		settlement_account_name   TEXT,
		available_ngn             TEXT NOT NULL DEFAULT '0',
		reserved_ngn              TEXT NOT NULL DEFAULT '0',
		provisioning_status       TEXT NOT NULL DEFAULT 'pending',
		per_trade_cap_usd         REAL NOT NULL DEFAULT 0,
		daily_cap_usd             REAL NOT NULL DEFAULT 0,
		reviewed_by               TEXT,
		reviewed_at               DATETIME,
		review_note               TEXT,
		created_at                DATETIME,
		updated_at                DATETIME,
		CHECK (CAST(available_ngn AS NUMERIC) >= 0
		   AND CAST(reserved_ngn  AS NUMERIC) >= 0
		   AND CAST(reserved_ngn  AS NUMERIC) <= CAST(available_ngn AS NUMERIC))
	)`).Error; err != nil {
		t.Fatalf("create payment_merchants: %v", err)
	}

	// The CHECK mirrors chk_pm_balances_ledger from config/database.go. Without
	// it here, a bug driving reserved past available would pass the whole suite
	// and only fail at deploy.
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS pm_balances (
		pm_id      TEXT NOT NULL,
		currency   TEXT NOT NULL,
		available  TEXT NOT NULL DEFAULT '0',
		reserved   TEXT NOT NULL DEFAULT '0',
		created_at DATETIME,
		updated_at DATETIME,
		PRIMARY KEY (pm_id, currency),
		CHECK (CAST(available AS NUMERIC) >= 0
		   AND CAST(reserved  AS NUMERIC) >= 0
		   AND CAST(reserved  AS NUMERIC) <= CAST(available AS NUMERIC))
	)`).Error; err != nil {
		t.Fatalf("create pm_balances: %v", err)
	}
	return db
}

func seedConcurrentPM(t *testing.T, db *gorm.DB, availableNGN string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := db.Create(&models.PaymentMerchant{
		ID: id, UserID: uuid.New(), Name: "TSA",
		Status:       models.PaymentMerchantStatusActive,
		AvailableNGN: availableNGN, ReservedNGN: "0",
	}).Error; err != nil {
		t.Fatalf("seed PM: %v", err)
	}
	if err := db.Create(&models.PMBalance{
		PMID: id, Currency: "NGN", Available: availableNGN, Reserved: "0",
	}).Error; err != nil {
		t.Fatalf("seed NGN balance: %v", err)
	}
	return id
}

func balanceRow(t *testing.T, db *gorm.DB, pmID uuid.UUID, currency string) models.PMBalance {
	t.Helper()
	var row models.PMBalance
	if err := db.Where("pm_id = ? AND currency = ?", pmID, currency).First(&row).Error; err != nil {
		t.Fatalf("read %s balance: %v", currency, err)
	}
	return row
}

// TestReserveIn_ConcurrentNeverOversells fans out more reservations than the
// balance can fund and asserts BOTH that exactly the fundable number succeeded
// and that the row reflects exactly those — the pair that fails on a lost update.
func TestReserveIn_ConcurrentNeverOversells(t *testing.T) {
	db := newSQLiteForPMConcurrent(t)
	svc := NewPMService(db)
	pmID := seedConcurrentPM(t, db, "1000.00")

	const (
		workers  = 20
		each     = "100.00"
		fundable = 10 // 1000.00 / 100.00
	)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		okCount int
		errs    []error
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := svc.ReserveIn(context.Background(), pmID, "NGN", each)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if ok {
				okCount++
			}
		}()
	}
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("%d/%d workers errored (first: %v) — if these are \"no such table\", the "+
			"connection limit is missing and this test proves nothing", len(errs), workers, errs[0])
	}
	if okCount != fundable {
		t.Errorf("okCount = %d, want %d — more callers were told ok than the balance can fund", okCount, fundable)
	}

	row := balanceRow(t, db, pmID, "NGN")
	if !decimalEqual(row.Reserved, "1000.00") {
		t.Errorf("reserved = %q, want 1000.00 (= okCount × %s). A value BELOW this is the "+
			"signature of a lost update, which a `reserved <= available` assertion would miss",
			row.Reserved, each)
	}

	var pm models.PaymentMerchant
	if err := db.First(&pm, "id = ?", pmID).Error; err != nil {
		t.Fatalf("read PM: %v", err)
	}
	if !decimalEqual(pm.ReservedNGN, row.Reserved) {
		t.Errorf("dual-write diverged under concurrency: legacy reserved_ngn=%q, pm_balances reserved=%q",
			pm.ReservedNGN, row.Reserved)
	}
}

// TestCreditIn_ConcurrentFirstCreditRace covers the case a conditional UPDATE
// cannot express: the balance row does not exist yet. A FirstOrCreate here would
// let two concurrent first-credits race, and the only caller logs and swallows
// the error — losing money that already landed in the provider balance.
func TestCreditIn_ConcurrentFirstCreditRace(t *testing.T) {
	db := newSQLiteForPMConcurrent(t)
	svc := NewPMService(db)
	pmID := seedConcurrentPM(t, db, "0.00")

	// GHS has no row at all — every worker races to create it.
	const workers = 20
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.CreditIn(context.Background(), pmID, "GHS", "25.00"); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent first-credit failed: %v", err)
	}

	row := balanceRow(t, db, pmID, "GHS")
	if !decimalEqual(row.Available, "500.00") {
		t.Errorf("available = %q, want 500.00 (20 × 25.00) — a lower value means credits were lost to the row-creation race", row.Available)
	}
	if !decimalEqual(row.Reserved, "0") {
		t.Errorf("reserved = %q, want 0", row.Reserved)
	}
}

// TestLedgerOps_PerCurrencyIsolation is the currency-confusion guard: operations
// in one currency must never touch another's balance. Cedis credited to a naira
// ledger at 1:1 is roughly a 100x error.
func TestLedgerOps_PerCurrencyIsolation(t *testing.T) {
	db := newSQLiteForPMConcurrent(t)
	svc := NewPMService(db)
	ctx := context.Background()
	pmID := seedConcurrentPM(t, db, "1000.00")

	if err := svc.CreditIn(ctx, pmID, "GHS", "300.00"); err != nil {
		t.Fatalf("credit GHS: %v", err)
	}

	before := balanceRow(t, db, pmID, "NGN")

	ok, err := svc.ReserveIn(ctx, pmID, "GHS", "200.00")
	if err != nil || !ok {
		t.Fatalf("reserve GHS: ok=%v err=%v", ok, err)
	}
	if err := svc.ConsumeIn(ctx, pmID, "GHS", "200.00"); err != nil {
		t.Fatalf("consume GHS: %v", err)
	}

	after := balanceRow(t, db, pmID, "NGN")
	if !decimalEqual(before.Available, after.Available) || !decimalEqual(before.Reserved, after.Reserved) {
		t.Errorf("GHS operations moved the NGN balance: before(avail=%s res=%s) after(avail=%s res=%s)",
			before.Available, before.Reserved, after.Available, after.Reserved)
	}

	ghs := balanceRow(t, db, pmID, "GHS")
	if !decimalEqual(ghs.Available, "100.00") {
		t.Errorf("GHS available = %q, want 100.00", ghs.Available)
	}
}

// TestReserveIn_MissingCurrencyRowIsInsufficient pins that a currency the PM
// holds no balance in reports insufficient rather than erroring or, worse,
// fabricating a row.
func TestReserveIn_MissingCurrencyRowIsInsufficient(t *testing.T) {
	db := newSQLiteForPMConcurrent(t)
	svc := NewPMService(db)
	pmID := seedConcurrentPM(t, db, "1000.00")

	ok, err := svc.ReserveIn(context.Background(), pmID, "GHS", "1.00")
	if err != nil {
		t.Fatalf("reserve in unheld currency: %v", err)
	}
	if ok {
		t.Error("reserved against a currency the PM holds no balance in")
	}

	var count int64
	db.Model(&models.PMBalance{}).Where("pm_id = ? AND currency = ?", pmID, "GHS").Count(&count)
	if count != 0 {
		t.Error("a failed reserve fabricated a GHS balance row")
	}
}

// TestCheckPMBalanceDivergence_DetectsDrift proves the divergence harness fails
// on drift rather than merely reporting clean on a clean database.
func TestCheckPMBalanceDivergence_DetectsDrift(t *testing.T) {
	db := newSQLiteForPMConcurrent(t)
	svc := NewPMService(db)
	ctx := context.Background()
	pmID := seedConcurrentPM(t, db, "1000.00")

	if err := svc.CreditIn(ctx, pmID, "NGN", "500.00"); err != nil {
		t.Fatalf("credit: %v", err)
	}
	found, err := CheckPMBalanceDivergence(ctx, db)
	if err != nil {
		t.Fatalf("divergence check: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("clean ledger reported drift: %v", found)
	}

	// Poke one representation behind the ledger's back.
	if err := db.Model(&models.PMBalance{}).
		Where("pm_id = ? AND currency = ?", pmID, "NGN").
		Update("available", "999.99").Error; err != nil {
		t.Fatalf("inject drift: %v", err)
	}

	found, err = CheckPMBalanceDivergence(ctx, db)
	if err != nil {
		t.Fatalf("divergence check: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("injected drift not detected: got %d findings, want 1", len(found))
	}
}

// ── Statement-shape guard ────────────────────────────────────────────────────
//
// The concurrency test above CANNOT catch a read-modify-write regression, and
// it is important to say so rather than let it imply coverage it lacks.
//
// Each ledger op runs inside a transaction, and with SetMaxOpenConns(1) the
// single connection is held for that transaction's duration — so no two ops
// interleave and a read-modify-write is genuinely safe *in this harness*. It is
// NOT safe in production: under Postgres READ COMMITTED with a real connection
// pool, two concurrent transactions can both read the old balance and both
// write, losing an update. Verified by mutation: replacing the guarded UPDATE
// with a read-modify-write leaves the concurrency test green.
//
// So the regression is caught structurally instead. "One guarded UPDATE, no
// prior SELECT of the row being mutated" is exactly the property that makes the
// operation atomic under a real database, and unlike timing it is directly
// observable.

// sqlRecorder captures every statement GORM issues.
type sqlRecorder struct {
	logger.Interface
	mu    sync.Mutex
	stmts []string
}

func (r *sqlRecorder) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	r.mu.Lock()
	r.stmts = append(r.stmts, sql)
	r.mu.Unlock()
}

func (r *sqlRecorder) matching(substrs ...string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.stmts {
		up := strings.ToUpper(s)
		ok := true
		for _, sub := range substrs {
			if !strings.Contains(up, strings.ToUpper(sub)) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}

// TestLedgerOps_AreSingleGuardedStatements asserts the shape that makes these
// operations atomic: a guarded UPDATE, with no SELECT of the row first.
func TestLedgerOps_AreSingleGuardedStatements(t *testing.T) {
	rec := &sqlRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	db := newSQLiteForPMConcurrent(t)
	pmID := seedConcurrentPM(t, db, "1000.00")

	// Attach the recorder only now, so seeding is not counted.
	db = db.Session(&gorm.Session{Logger: rec})
	svc := NewPMService(db)
	ctx := context.Background()

	cases := []struct {
		name  string
		run   func() error
		table string
		guard string
	}{
		{
			name:  "ReserveIn guards on free balance",
			run:   func() error { _, err := svc.ReserveIn(ctx, pmID, "NGN", "100.00"); return err },
			table: "payment_merchants",
			guard: "CAST(available_ngn AS NUMERIC) - CAST(reserved_ngn AS NUMERIC)",
		},
		{
			name:  "ConsumeIn guards on reserved",
			run:   func() error { return svc.ConsumeIn(ctx, pmID, "NGN", "100.00") },
			table: "payment_merchants",
			guard: "CAST(reserved_ngn AS NUMERIC) >=",
		},
		{
			name:  "WithdrawIn guards on free balance",
			run:   func() error { _, err := svc.WithdrawIn(ctx, pmID, "NGN", "10.00"); return err },
			table: "payment_merchants",
			guard: "CAST(available_ngn AS NUMERIC) - CAST(reserved_ngn AS NUMERIC)",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec.mu.Lock()
			rec.stmts = nil
			rec.mu.Unlock()

			if err := c.run(); err != nil {
				t.Fatalf("run: %v", err)
			}

			// No SELECT of the row being mutated — that is the read half of a
			// read-modify-write, and it is what makes the operation non-atomic.
			if reads := rec.matching("SELECT", c.table); len(reads) > 0 {
				t.Errorf("read-modify-write detected: the op SELECTs %s before mutating it.\n"+
					"The guard must live in the UPDATE's WHERE clause so the read and the write "+
					"are one statement.\nOffending: %s", c.table, reads[0])
			}

			// Exactly one guarded UPDATE against the authoritative table.
			updates := rec.matching("UPDATE", c.table)
			if len(updates) != 1 {
				t.Fatalf("expected exactly 1 UPDATE of %s, got %d: %v", c.table, len(updates), updates)
			}
			if !strings.Contains(updates[0], c.guard) {
				t.Errorf("UPDATE is not guarded — missing %q.\nAn unguarded UPDATE cannot detect "+
					"insufficient balance and would let the ledger go negative.\nGot: %s",
					c.guard, updates[0])
			}
		})
	}
}

// TestCreditIn_IsSingleUpsert pins the one op that must create its row: a
// conditional UPDATE cannot, and a FirstOrCreate would race two concurrent
// first-credits in a currency.
func TestCreditIn_IsSingleUpsert(t *testing.T) {
	rec := &sqlRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	db := newSQLiteForPMConcurrent(t)
	pmID := seedConcurrentPM(t, db, "0.00")

	db = db.Session(&gorm.Session{Logger: rec})
	svc := NewPMService(db)

	if err := svc.CreditIn(context.Background(), pmID, "GHS", "25.00"); err != nil {
		t.Fatalf("credit: %v", err)
	}

	if reads := rec.matching("SELECT", "pm_balances"); len(reads) > 0 {
		t.Errorf("CreditIn SELECTs pm_balances before writing — that is the read half of the "+
			"row-creation race this upsert exists to avoid.\nOffending: %s", reads[0])
	}
	upserts := rec.matching("INSERT", "pm_balances", "ON CONFLICT")
	if len(upserts) != 1 {
		t.Fatalf("expected exactly 1 upsert of pm_balances, got %d: %v", len(upserts), upserts)
	}
}
