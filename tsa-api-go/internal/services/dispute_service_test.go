package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newDisputeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Dispute{}, &models.PaycrestOrderRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Raw DDL for users: AutoMigrate of models.User carries Postgres-only
	// defaults that SQLite rejects, the same reason the wallet tests hand-roll
	// their schema. Only the columns ListAdmin selects are needed.
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id       TEXT PRIMARY KEY,
		name     TEXT,
		username TEXT,
		email    TEXT
	)`).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	return db
}

func newDisputeSvc(t *testing.T) (*DisputeService, *gorm.DB) {
	t.Helper()
	db := newDisputeTestDB(t)
	svc := NewDisputeService(db, nil)
	svc.RegisterSubject(models.DisputeSubjectPaycrestOrder, PaycrestDisputeSubject(db))
	return svc, db
}

func seedPaycrestOrder(t *testing.T, db *gorm.DB, userID uuid.UUID, status string) uuid.UUID {
	t.Helper()
	row := &models.PaycrestOrderRecord{
		ID: uuid.New(), UserID: userID, Direction: models.PaycrestDirectionSell,
		Status: status, Reference: "ref-" + uuid.New().String(),
		FiatCode: "NGN", FiatAmount: "1000", Token: "USDC",
		ChainKey: "base", Network: "base", WalletAddress: "0xabc",
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return row.ID
}

// ── Filing ────────────────────────────────────────────────────────────────

func TestFileDispute_RecordsReasonAndEvidence(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)

	d, err := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder,
		SubjectID:   order,
		Reason:      "Paid in full, never credited",
		Evidence:    []string{"https://res.cloudinary.com/tsa/image/upload/v1/receipt.jpg"},
	})
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if d.Status != models.DisputeStatusOpen {
		t.Errorf("status = %q, want open", d.Status)
	}
	if len(d.Evidence) != 1 || d.Evidence[0] != "https://res.cloudinary.com/tsa/image/upload/v1/receipt.jpg" {
		t.Errorf("evidence = %v, want the one url", d.Evidence)
	}

	// Round-trips through storage as an array, not the raw JSON string.
	var back models.Dispute
	if err := db.First(&back, "id = ?", d.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(back.Evidence) != 1 {
		t.Errorf("evidence did not survive a reload: %v", back.Evidence)
	}
}

func TestFileDispute_RejectsSecondDisputeOnTheSameOrder(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)
	in := DisputeInput{SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "first"}

	if _, err := svc.File(context.Background(), user, in); err != nil {
		t.Fatalf("first File: %v", err)
	}
	in.Reason = "second"
	if _, err := svc.File(context.Background(), user, in); !errors.Is(err, ErrDisputeAlreadyExists) {
		t.Fatalf("err = %v, want ErrDisputeAlreadyExists", err)
	}
}

func TestFileDispute_RejectsAnotherUsersOrder(t *testing.T) {
	svc, db := newDisputeSvc(t)
	order := seedPaycrestOrder(t, db, uuid.New(), models.PaycrestStatusUnmatched)

	_, err := svc.File(context.Background(), uuid.New(), DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "not mine",
	})
	if !errors.Is(err, ErrDisputeSubjectNotFound) {
		t.Fatalf("err = %v, want ErrDisputeSubjectNotFound", err)
	}
}

// TestFileDispute_RejectsOrdersWhereNoMoneyMoved: an order still waiting on the
// user has nothing to dispute — cancelling is the right action there, and
// offering a dispute instead would put a human in a loop that needs no human.
func TestFileDispute_RejectsOrdersWhereNoMoneyMoved(t *testing.T) {
	for _, status := range []string{
		models.PaycrestStatusAwaitingPayment,
		models.PaycrestStatusAwaitingDeposit,
		models.PaycrestStatusCancelled,
	} {
		t.Run(status, func(t *testing.T) {
			svc, db := newDisputeSvc(t)
			user := uuid.New()
			order := seedPaycrestOrder(t, db, user, status)

			_, err := svc.File(context.Background(), user, DisputeInput{
				SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "x",
			})
			if !errors.Is(err, ErrDisputeNotDisputable) {
				t.Fatalf("err = %v, want ErrDisputeNotDisputable", err)
			}
		})
	}
}

func TestFileDispute_RejectsAnUnknownSubjectType(t *testing.T) {
	svc, _ := newDisputeSvc(t)

	_, err := svc.File(context.Background(), uuid.New(), DisputeInput{
		SubjectType: "spaceship", SubjectID: uuid.New(), Reason: "x",
	})
	if !errors.Is(err, ErrDisputeBadRequest) {
		t.Fatalf("err = %v, want ErrDisputeBadRequest", err)
	}
}

func TestFileDispute_RequiresAReason(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)

	_, err := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "   ",
	})
	if !errors.Is(err, ErrDisputeBadRequest) {
		t.Fatalf("err = %v, want ErrDisputeBadRequest", err)
	}
}

func TestFileDispute_CapsReasonAndEvidenceCount(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)

	_, err := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order,
		Reason: strings.Repeat("x", disputeReasonMax+1),
	})
	if !errors.Is(err, ErrDisputeBadRequest) {
		t.Fatalf("long reason err = %v, want ErrDisputeBadRequest", err)
	}

	tooMany := make([]string, disputeEvidenceMax+1)
	for i := range tooMany {
		tooMany[i] = "https://res.cloudinary.com/tsa/image/upload/v1/x.jpg"
	}
	_, err = svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order,
		Reason: "ok", Evidence: tooMany,
	})
	if !errors.Is(err, ErrDisputeBadRequest) {
		t.Fatalf("too much evidence err = %v, want ErrDisputeBadRequest", err)
	}
}

// ── Lifecycle ─────────────────────────────────────────────────────────────

func TestEscalate_RecordsWhoAndWhere(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)
	d, _ := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "x",
	})
	admin := uuid.New()

	got, err := svc.Escalate(context.Background(), admin, d.ID, "PC-4471", "emailed paycrest support")
	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if got.Status != models.DisputeStatusEscalated {
		t.Errorf("status = %q, want escalated", got.Status)
	}
	if got.ExternalRef != "PC-4471" || got.EscalatedAt == nil || got.EscalatedBy == nil {
		t.Errorf("escalation not recorded: %+v", got)
	}
}

// TestEscalate_StillResolvable: escalation is a waypoint, not a dead end.
func TestEscalate_StillResolvable(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)
	d, _ := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "x",
	})
	admin := uuid.New()
	if _, err := svc.Escalate(context.Background(), admin, d.ID, "PC-1", ""); err != nil {
		t.Fatalf("Escalate: %v", err)
	}

	got, err := svc.Resolve(context.Background(), admin, d.ID, models.DisputeStatusResolved, "refunded by provider")
	if err != nil {
		t.Fatalf("Resolve after escalate: %v", err)
	}
	if got.Status != models.DisputeStatusResolved || got.ResolvedAt == nil {
		t.Errorf("not resolved: %+v", got)
	}
}

func TestResolve_RefusesAClosedDispute(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)
	d, _ := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "x",
	})
	admin := uuid.New()
	if _, err := svc.Resolve(context.Background(), admin, d.ID, models.DisputeStatusRejected, "no evidence"); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}

	// The second admin loses the race rather than overwriting the decision.
	if _, err := svc.Resolve(context.Background(), admin, d.ID, models.DisputeStatusResolved, "changed my mind"); !errors.Is(err, ErrDisputeClosed) {
		t.Fatalf("err = %v, want ErrDisputeClosed", err)
	}
}

func TestEscalate_RefusesAClosedDispute(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)
	d, _ := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "x",
	})
	admin := uuid.New()
	if _, err := svc.Resolve(context.Background(), admin, d.ID, models.DisputeStatusResolved, "done"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if _, err := svc.Escalate(context.Background(), admin, d.ID, "PC-9", ""); !errors.Is(err, ErrDisputeClosed) {
		t.Fatalf("err = %v, want ErrDisputeClosed", err)
	}
}

func TestResolve_RejectsAStatusThatClosesNothing(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)
	d, _ := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "x",
	})

	if _, err := svc.Resolve(context.Background(), uuid.New(), d.ID, models.DisputeStatusOpen, ""); !errors.Is(err, ErrDisputeBadRequest) {
		t.Fatalf("err = %v, want ErrDisputeBadRequest", err)
	}
}

// ── Reads ─────────────────────────────────────────────────────────────────

// TestForSubject drives the app's button: file, or view the one you filed.
func TestForSubject_FindsTheUsersDisputeOnAnOrder(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)

	if _, err := svc.ForSubject(context.Background(), user, models.DisputeSubjectPaycrestOrder, order); !errors.Is(err, ErrOrderDisputeNotFound) {
		t.Fatalf("before filing: err = %v, want ErrOrderDisputeNotFound", err)
	}
	filed, _ := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "x",
	})

	got, err := svc.ForSubject(context.Background(), user, models.DisputeSubjectPaycrestOrder, order)
	if err != nil {
		t.Fatalf("ForSubject: %v", err)
	}
	if got.ID != filed.ID {
		t.Errorf("got %s, want %s", got.ID, filed.ID)
	}
}

func TestForSubject_DoesNotLeakAnotherUsersDispute(t *testing.T) {
	svc, db := newDisputeSvc(t)
	owner := uuid.New()
	order := seedPaycrestOrder(t, db, owner, models.PaycrestStatusUnmatched)
	if _, err := svc.File(context.Background(), owner, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "x",
	}); err != nil {
		t.Fatalf("File: %v", err)
	}

	if _, err := svc.ForSubject(context.Background(), uuid.New(), models.DisputeSubjectPaycrestOrder, order); !errors.Is(err, ErrOrderDisputeNotFound) {
		t.Fatalf("err = %v, want ErrOrderDisputeNotFound", err)
	}
}

func TestListAdmin_FiltersByStatusAndSubjectType(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	o1 := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)
	o2 := seedPaycrestOrder(t, db, user, models.PaycrestStatusFailed)
	d1, _ := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: o1, Reason: "one",
	})
	if _, err := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: o2, Reason: "two",
	}); err != nil {
		t.Fatalf("File: %v", err)
	}
	if _, err := svc.Resolve(context.Background(), uuid.New(), d1.ID, models.DisputeStatusResolved, "done"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	rows, total, err := svc.ListAdmin(context.Background(), DisputeQuery{Status: models.DisputeStatusOpen, Limit: 20})
	if err != nil {
		t.Fatalf("ListAdmin: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("open disputes: got %d rows / total %d, want 1", len(rows), total)
	}
	if rows[0].Dispute.Reason != "two" {
		t.Errorf("wrong dispute returned: %q", rows[0].Dispute.Reason)
	}

	_, total, err = svc.ListAdmin(context.Background(), DisputeQuery{SubjectType: models.DisputeSubjectBillOrder, Limit: 20})
	if err != nil {
		t.Fatalf("ListAdmin by type: %v", err)
	}
	if total != 0 {
		t.Errorf("bill disputes total = %d, want 0", total)
	}
}

// TestListAdmin_AttachesTheReporter: the queue is unusable without knowing who
// filed, and one query per row would not survive a busy day.
func TestListAdmin_AttachesTheReporter(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := models.User{ID: uuid.New(), Name: "Ada", Username: "ada", Email: "ada@example.com"}
	if err := db.Exec(`INSERT INTO users (id, name, username, email) VALUES (?, ?, ?, ?)`,
		user.ID, user.Name, user.Username, user.Email).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	order := seedPaycrestOrder(t, db, user.ID, models.PaycrestStatusUnmatched)
	if _, err := svc.File(context.Background(), user.ID, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "x",
	}); err != nil {
		t.Fatalf("File: %v", err)
	}

	rows, _, err := svc.ListAdmin(context.Background(), DisputeQuery{Limit: 20})
	if err != nil {
		t.Fatalf("ListAdmin: %v", err)
	}
	if len(rows) != 1 || rows[0].User == nil {
		t.Fatalf("reporter not attached: %+v", rows)
	}
	if rows[0].User.Email != "ada@example.com" {
		t.Errorf("email = %q", rows[0].User.Email)
	}
}

// ── Evidence validation ───────────────────────────────────────────────────
//
// Evidence is rendered directly in the admin console, as an <img> inside an
// <a href>. An unvalidated URL there means a javascript: href reaching the DOM
// and, more quietly, every reviewing admin's browser fetching a host the
// reporter chose. The existing p2p dispute flow already screens these
// (p2p_trade_service.go PrepareRaiseDispute); this must not be laxer.

func fileWithEvidence(t *testing.T, urls []string) error {
	t.Helper()
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)
	_, err := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order,
		Reason: "x", Evidence: urls,
	})
	return err
}

func TestFileDispute_RejectsEvidenceThatIsNotAnUploadedImage(t *testing.T) {
	cases := map[string][]string{
		"javascript url":     {"javascript:alert(document.cookie)"},
		"data url":           {"data:text/html;base64,PHNjcmlwdD4="},
		"plain http":         {"http://res.cloudinary.com/demo/image/upload/a.jpg"},
		"attacker host":      {"https://evil.example.com/beacon.png"},
		"lookalike host":     {"https://res.cloudinary.com.evil.example.com/a.jpg"},
		"empty string":       {"   "},
		"one bad among good": {"https://res.cloudinary.com/demo/image/upload/a.jpg", "https://evil.example.com/b.png"},
	}
	for name, urls := range cases {
		t.Run(name, func(t *testing.T) {
			if err := fileWithEvidence(t, urls); !errors.Is(err, ErrDisputeBadRequest) {
				t.Fatalf("err = %v, want ErrDisputeBadRequest", err)
			}
		})
	}
}

func TestFileDispute_RejectsAnAbsurdlyLongEvidenceURL(t *testing.T) {
	long := "https://res.cloudinary.com/demo/image/upload/" + strings.Repeat("a", disputeEvidenceURLMax)

	if err := fileWithEvidence(t, []string{long}); !errors.Is(err, ErrDisputeBadRequest) {
		t.Fatalf("err = %v, want ErrDisputeBadRequest", err)
	}
}

func TestFileDispute_AcceptsAnUploadedImage(t *testing.T) {
	if err := fileWithEvidence(t, []string{
		"https://res.cloudinary.com/tsa/image/upload/v1/receipt.jpg",
	}); err != nil {
		t.Fatalf("File: %v", err)
	}
}

// ── Escalation is correctable ─────────────────────────────────────────────

// TestEscalate_CanBeCorrected: an operator who mistypes the provider's thread
// reference must be able to fix it. Refusing — and worse, refusing with "this
// dispute has already been closed" when it is plainly open — leaves the queue
// carrying a reference nobody can follow.
func TestEscalate_CanBeCorrected(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)
	d, _ := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "x",
	})
	admin := uuid.New()
	if _, err := svc.Escalate(context.Background(), admin, d.ID, "PC-1", "typo"); err != nil {
		t.Fatalf("first Escalate: %v", err)
	}

	got, err := svc.Escalate(context.Background(), admin, d.ID, "PC-4471", "corrected")
	if err != nil {
		t.Fatalf("second Escalate: %v", err)
	}
	if got.ExternalRef != "PC-4471" || got.EscalationNote != "corrected" {
		t.Errorf("correction not stored: ref=%q note=%q", got.ExternalRef, got.EscalationNote)
	}
	if got.Status != models.DisputeStatusEscalated {
		t.Errorf("status = %q, want escalated", got.Status)
	}
}

// ── Admin queue shows what was ordered ────────────────────────────────────

// TestListAdmin_DescribesTheOrder: a queue row of "Bank ramp" plus a UUID is
// not reviewable — the operator has to open every row to learn what the
// complaint is even about. The label comes from the same per-type resolver
// that gates filing, batched so it does not cost a query per row.
func TestListAdmin_DescribesTheOrder(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	o1 := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)
	o2 := seedPaycrestOrder(t, db, user, models.PaycrestStatusFailed)
	for _, o := range []uuid.UUID{o1, o2} {
		if _, err := svc.File(context.Background(), user, DisputeInput{
			SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: o, Reason: "x",
		}); err != nil {
			t.Fatalf("File: %v", err)
		}
	}

	rows, _, err := svc.ListAdmin(context.Background(), DisputeQuery{Limit: 20})
	if err != nil {
		t.Fatalf("ListAdmin: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Subject == "" {
			t.Fatalf("dispute %s has no subject label", r.Dispute.ID)
		}
		if !strings.Contains(r.Subject, "NGN") {
			t.Errorf("subject %q does not describe the order", r.Subject)
		}
	}
}

func TestGetAdmin_DescribesTheOrder(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)
	d, _ := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "x",
	})

	row, err := svc.GetAdmin(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("GetAdmin: %v", err)
	}
	if row.Subject == "" {
		t.Fatal("no subject label on the detail view")
	}
}

// TestListAdmin_SurvivesADeletedSubject: labels are a convenience, and a
// dispute whose order row has gone must still appear in the queue — that is
// precisely the case an operator needs to see.
func TestListAdmin_SurvivesADeletedSubject(t *testing.T) {
	svc, db := newDisputeSvc(t)
	user := uuid.New()
	order := seedPaycrestOrder(t, db, user, models.PaycrestStatusUnmatched)
	if _, err := svc.File(context.Background(), user, DisputeInput{
		SubjectType: models.DisputeSubjectPaycrestOrder, SubjectID: order, Reason: "x",
	}); err != nil {
		t.Fatalf("File: %v", err)
	}
	if err := db.Exec("DELETE FROM paycrest_order_records WHERE id = ?", order).Error; err != nil {
		t.Fatalf("delete order: %v", err)
	}

	rows, total, err := svc.ListAdmin(context.Background(), DisputeQuery{Limit: 20})
	if err != nil {
		t.Fatalf("ListAdmin: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("the dispute vanished with its order: %d rows", len(rows))
	}
}
