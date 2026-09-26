package services

import (
	"context"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestNewEmailCode_IsSixDigits(t *testing.T) {
	re := regexp.MustCompile(`^\d{6}$`)
	for i := 0; i < 50; i++ {
		c, err := NewEmailCode()
		if err != nil || !re.MatchString(c) {
			t.Fatalf("code %q err %v", c, err)
		}
	}
}

func TestEmailCodeHash_BindsTheCodeToItsSubject(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	h := EmailCodeHash(a, "123456")
	if !EmailCodeMatches(a, "123456", h) {
		t.Fatal("the right code for the right subject must match")
	}
	if EmailCodeMatches(b, "123456", h) || EmailCodeMatches(a, "123457", h) || EmailCodeMatches(a, " 123456", h) {
		t.Fatal("another subject, another code, or an unnormalised code must not match")
	}
}

// codeRow stands in for any row an emailed code unlocks (a web sign-in
// challenge, a TSA Pay owner action).
type codeRow struct {
	ID       uuid.UUID `gorm:"type:uuid;primaryKey"`
	CodeHash string
	Attempts int
}

func (codeRow) TableName() string { return "code_rows" }

func seedCodeRow(t *testing.T, attempts int) (*gorm.DB, codeRow) {
	t.Helper()
	db := newPayTestDB(t)
	if err := db.AutoMigrate(&codeRow{}); err != nil {
		t.Fatal(err)
	}
	row := codeRow{ID: uuid.New(), Attempts: attempts}
	row.CodeHash = EmailCodeHash(row.ID, "123456")
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return db, row
}

func redeem(t *testing.T, db *gorm.DB, row codeRow, typed string) EmailCodeOutcome {
	t.Helper()
	var stored codeRow
	attempts := row.Attempts
	if err := db.First(&stored, "id = ?", row.ID).Error; err == nil {
		attempts = stored.Attempts
	}
	out, err := RedeemEmailCode(context.Background(), db, &codeRow{}, row.ID, attempts, row.CodeHash, typed)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func codeRowExists(t *testing.T, db *gorm.DB, id uuid.UUID) bool {
	t.Helper()
	var n int64
	if err := db.Model(&codeRow{}).Where("id = ?", id).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestRedeemEmailCode_ARightCodeIsClaimedOnce(t *testing.T) {
	db, row := seedCodeRow(t, 0)
	if got := redeem(t, db, row, " 123456 "); got != EmailCodeClaimed {
		t.Fatalf("the right code (typed with spaces): %v", got)
	}
	if codeRowExists(t, db, row.ID) {
		t.Fatal("a claimed row must be deleted")
	}
	if got := redeem(t, db, row, "123456"); got == EmailCodeClaimed {
		t.Fatal("the same code was claimed twice")
	}
}

func TestRedeemEmailCode_TheLastWrongCodeDropsTheRow(t *testing.T) {
	db, row := seedCodeRow(t, 0)
	for i := 1; i < EmailCodeAttempts; i++ {
		if got := redeem(t, db, row, "000000"); got != EmailCodeWrong || !codeRowExists(t, db, row.ID) {
			t.Fatalf("wrong code %d: %v, row kept %v", i, got, codeRowExists(t, db, row.ID))
		}
	}
	if got := redeem(t, db, row, "000000"); got != EmailCodeWrong {
		t.Fatalf("the last wrong code: %v", got)
	}
	if codeRowExists(t, db, row.ID) {
		t.Fatal("the last allowed wrong code must drop the row at once")
	}
}

// Guesses that spent every attempt between the caller's read and its redeem
// leave none for it, even with the right code.
func TestRedeemEmailCode_NoAttemptLeftIsExhausted(t *testing.T) {
	db, row := seedCodeRow(t, EmailCodeAttempts)
	out, err := RedeemEmailCode(context.Background(), db, &codeRow{}, row.ID, 0, row.CodeHash, "123456")
	if err != nil || out != EmailCodeExhausted {
		t.Fatalf("out %v err %v", out, err)
	}
	if codeRowExists(t, db, row.ID) {
		t.Fatal("an exhausted row must be dropped")
	}
}

// Two requests with the right code both spend an attempt and pass the compare;
// only the one whose delete lands is Claimed. The hook lands the winner's
// claim right after this request spends its attempt.
func TestRedeemEmailCode_TheClaimIsSingleUseUnderARace(t *testing.T) {
	db, row := seedCodeRow(t, 0)
	fired := false
	if err := db.Callback().Update().After("gorm:update").Register("test:winner_claims", func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "code_rows" {
			return
		}
		fired = true
		tx.Session(&gorm.Session{NewDB: true}).Exec(`DELETE FROM code_rows`)
	}); err != nil {
		t.Fatal(err)
	}
	out, err := RedeemEmailCode(context.Background(), db, &codeRow{}, row.ID, 0, row.CodeHash, "123456")
	if !fired || err != nil || out != EmailCodeGone {
		t.Fatalf("the losing redeem: fired %v out %v err %v", fired, out, err)
	}
	_ = db.Callback().Update().Remove("test:winner_claims")

	db, row = seedCodeRow(t, 0)
	var wg sync.WaitGroup
	var claimed atomic.Int32
	for i := 0; i < EmailCodeAttempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := RedeemEmailCode(context.Background(), db, &codeRow{}, row.ID, 0, row.CodeHash, "123456"); err == nil && out == EmailCodeClaimed {
				claimed.Add(1)
			}
		}()
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("%d concurrent redeems claimed the row, want 1", claimed.Load())
	}
}

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{
		"owner@skyair.ng": "o***@skyair.ng", "a@b.co": "a***@b.co", "nope": "your email",
		"élodie@skyair.ng": "é***@skyair.ng", "日本@example.jp": "日***@example.jp",
	} {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
