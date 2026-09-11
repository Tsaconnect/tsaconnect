package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// setupInboundAdminDB extends the ingestor's inbound_credits rig with the two
// tables the operator surface reads and writes. Raw DDL for the audit table:
// its metadata column is jsonb, which AutoMigrate cannot express under SQLite.
func setupInboundAdminDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupInboundDB(t)
	if err := db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		username TEXT NOT NULL,
		email TEXT NOT NULL,
		verified_legal_name TEXT
	)`).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	if err := db.Exec(`CREATE TABLE inbound_credit_audit_logs (
		id TEXT PRIMARY KEY,
		credit_id TEXT NOT NULL,
		actor_id TEXT,
		action TEXT NOT NULL,
		note TEXT,
		metadata TEXT,
		created_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create inbound_credit_audit_logs: %v", err)
	}
	return db
}

func insertAdminTestUser(t *testing.T, db *gorm.DB, legalName string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	short := id.String()[:8]
	if err := db.Exec(`INSERT INTO users (id, name, username, email, verified_legal_name) VALUES (?,?,?,?,?)`,
		id.String(), "Signup Name "+short, "user"+short, "user"+short+"@tsa.test", legalName).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func insertHeldCredit(t *testing.T, db *gorm.DB, senderName string, grossKobo int64) *models.InboundCredit {
	t.Helper()
	c := &models.InboundCredit{
		Provider:        models.InboundCreditProviderFlutterwave,
		ProviderRef:     uuid.NewString(),
		Currency:        "NGN",
		AmountKobo:      grossKobo,
		GrossKobo:       grossKobo,
		SenderRaw:       "Received Money from GTBANK|" + senderName,
		SenderBank:      "GTBANK",
		SenderName:      senderName,
		TransactionDate: time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC),
		SeenAt:          time.Date(2026, 9, 10, 9, 0, 30, 0, time.UTC),
		Status:          models.InboundCreditHeld,
		HoldReason:      "no open claim matched on amount and name",
	}
	if err := db.Create(c).Error; err != nil {
		t.Fatalf("insert credit: %v", err)
	}
	return c
}

// fakeAdminClaimSource serves a fixed slice of open claims to the admin
// candidate view. Distinct from the matcher test's fake, which also records
// Accept calls that this view never makes.
type fakeAdminClaimSource struct {
	claims []InboundClaim
	err    error
}

func (f *fakeAdminClaimSource) OpenClaims(context.Context, string) ([]InboundClaim, error) {
	return f.claims, f.err
}
func (f *fakeAdminClaimSource) Accept(context.Context, uuid.UUID, *models.InboundCredit) error {
	return nil
}

// fakeBinder stands in for the matcher's BindManually.
type fakeBinder struct {
	calls []bindCall
	err   error
	// onBind mutates the credit the way the real matcher would, so the service
	// can be asserted to re-read rather than invent the post-bind state.
	onBind func()
}

type bindCall struct {
	creditID  uuid.UUID
	claimType string
	claimID   uuid.UUID
	adminID   uuid.UUID
}

func (f *fakeBinder) BindManually(_ context.Context, creditID uuid.UUID, claimType string, claimID, adminID uuid.UUID) error {
	f.calls = append(f.calls, bindCall{creditID, claimType, claimID, adminID})
	if f.err != nil {
		return f.err
	}
	if f.onBind != nil {
		f.onBind()
	}
	return nil
}

// ---------------------------------------------------------------- listing

func TestInboundAdminListHeldDefaultsToTheHoldQueue(t *testing.T) {
	db := setupInboundAdminDB(t)
	svc := NewInboundAdminService(db)

	held := insertHeldCredit(t, db, "ADEMU EMMANUEL", 200000)
	matched := insertHeldCredit(t, db, "OTHER PERSON", 500000)
	if err := db.Model(matched).Update("status", models.InboundCreditMatched).Error; err != nil {
		t.Fatalf("update: %v", err)
	}

	rows, total, err := svc.ListCredits(context.Background(), InboundCreditQuery{})
	if err != nil {
		t.Fatalf("ListCredits: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("got %d rows (total %d), want only the held one", len(rows), total)
	}

	// The operator has to see the sender exactly as the bank wrote it — the
	// parse is a convenience, the raw remark is the evidence.
	got := rows[0]
	if got.ID != held.ID {
		t.Fatalf("listed %s, want %s", got.ID, held.ID)
	}
	if got.SenderRaw != held.SenderRaw {
		t.Errorf("senderRaw = %q, want the statement text verbatim %q", got.SenderRaw, held.SenderRaw)
	}
	if got.SenderBank != "GTBANK" || got.SenderName != "ADEMU EMMANUEL" {
		t.Errorf("parsed sender = %q / %q", got.SenderBank, got.SenderName)
	}
	if got.GrossKobo != 200000 {
		t.Errorf("grossKobo = %d, want 200000", got.GrossKobo)
	}
	if got.TransactionDate.IsZero() {
		t.Error("transactionDate must be populated")
	}
	if got.HoldReason == "" {
		t.Error("holdReason must be populated — it is why an operator is being asked")
	}
}

func TestInboundAdminListCreditsFilters(t *testing.T) {
	db := setupInboundAdminDB(t)
	svc := NewInboundAdminService(db)

	old := insertHeldCredit(t, db, "OLD SENDER", 100000)
	if err := db.Model(old).Update("transaction_date", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	recent := insertHeldCredit(t, db, "NEW SENDER", 300000)
	refunded := insertHeldCredit(t, db, "REFUNDED SENDER", 400000)
	if err := db.Model(refunded).Update("status", models.InboundCreditRefunded).Error; err != nil {
		t.Fatalf("update: %v", err)
	}

	rows, _, err := svc.ListCredits(context.Background(), InboundCreditQuery{
		From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ListCredits: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != recent.ID {
		t.Fatalf("date filter returned %d rows, want only the September credit", len(rows))
	}

	rows, _, err = svc.ListCredits(context.Background(), InboundCreditQuery{Status: models.InboundCreditRefunded})
	if err != nil {
		t.Fatalf("ListCredits: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != refunded.ID {
		t.Fatalf("status filter returned %d rows, want the refunded credit", len(rows))
	}

	rows, total, err := svc.ListCredits(context.Background(), InboundCreditQuery{Status: inboundStatusAny})
	if err != nil {
		t.Fatalf("ListCredits: %v", err)
	}
	if total != 3 || len(rows) != 3 {
		t.Fatalf("status=all returned %d rows (total %d), want 3", len(rows), total)
	}

	rows, total, err = svc.ListCredits(context.Background(), InboundCreditQuery{Status: inboundStatusAny, Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("ListCredits: %v", err)
	}
	if total != 3 || len(rows) != 1 {
		t.Errorf("paged page 2 = %d rows (total %d), want 1 of 3", len(rows), total)
	}

	if _, _, err := svc.ListCredits(context.Background(), InboundCreditQuery{Status: "bogus"}); !errors.Is(err, ErrInboundBadRequest) {
		t.Errorf("unknown status error = %v, want ErrInboundBadRequest", err)
	}
}

// -------------------------------------------------------------- candidates

func TestInboundAdminCandidatesExplainsEveryRejection(t *testing.T) {
	db := setupInboundAdminDB(t)
	credit := insertHeldCredit(t, db, "ADEMU EMMANUEL O.", 200000)

	nameOnly := insertAdminTestUser(t, db, "Emmanuel Ademu") // right name, wrong amount
	amountOnly := insertAdminTestUser(t, db, "Peter Obi")    // right amount, wrong name
	noKYCName := insertAdminTestUser(t, db, "")              // right amount, no Didit name
	unrelated := insertAdminTestUser(t, db, "Someone Else")  // neither — not a near miss

	nameOnlyClaim, amountOnlyClaim := uuid.New(), uuid.New()
	noNameClaim, unrelatedClaim := uuid.New(), uuid.New()

	svc := NewInboundAdminService(db)
	svc.AddClaimSource(InboundClaimTypeITMCGP, &fakeAdminClaimSource{claims: []InboundClaim{
		{Type: InboundClaimTypeITMCGP, ID: nameOnlyClaim, UserID: nameOnly, AmountKobo: 150000},
		{Type: InboundClaimTypeITStable, ID: amountOnlyClaim, UserID: amountOnly, AmountKobo: 200000},
		{Type: InboundClaimTypeP2PTrade, ID: noNameClaim, UserID: noKYCName, AmountKobo: 200000},
		{Type: InboundClaimTypeITMCGP, ID: unrelatedClaim, UserID: unrelated, AmountKobo: 999900},
	}})

	view, err := svc.Candidates(context.Background(), credit.ID)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}

	byClaim := map[uuid.UUID]InboundCandidate{}
	for _, c := range view.Candidates {
		byClaim[c.ClaimID] = c
	}
	if _, present := byClaim[unrelatedClaim]; present {
		t.Error("a claim matching on neither amount nor name is not a near miss and must not be listed")
	}
	if len(view.Candidates) != 3 {
		t.Fatalf("listed %d candidates, want 3 near misses", len(view.Candidates))
	}

	if got := byClaim[nameOnlyClaim]; got.Rejection != InboundRejectAmount {
		t.Errorf("name-only claim rejection = %q, want %q", got.Rejection, InboundRejectAmount)
	} else if got.AmountDeltaKobo != -50000 {
		t.Errorf("amountDeltaKobo = %d, want -50000 (claim is ₦500 short of the credit)", got.AmountDeltaKobo)
	} else if got.NameStrength != string(NameMatchExact) {
		t.Errorf("nameStrength = %q, want exact", got.NameStrength)
	}

	if got := byClaim[amountOnlyClaim]; got.Rejection != InboundRejectName {
		t.Errorf("amount-only claim rejection = %q, want %q", got.Rejection, InboundRejectName)
	} else if got.NameStrength != string(NameMatchNone) {
		t.Errorf("nameStrength = %q, want none", got.NameStrength)
	}

	// An absent Didit name means "cannot attribute", never "no match" — the
	// operator must be told which of the two they are looking at.
	got := byClaim[noNameClaim]
	if got.Rejection != InboundRejectNoKYCName {
		t.Errorf("no-KYC-name claim rejection = %q, want %q", got.Rejection, InboundRejectNoKYCName)
	}
	if got.User == nil || got.User.Email == "" {
		t.Error("candidate must carry enough identity for an operator to act on it")
	}
}

// The evidence shown must be the matcher's own view of the names, not a second
// copy of the normalization rules that can drift away from it.
func TestInboundAdminCandidatesShowsMatcherTokens(t *testing.T) {
	db := setupInboundAdminDB(t)
	credit := insertHeldCredit(t, db, "MR ADEMU EMMANUEL-OKON O.", 200000)
	user := insertAdminTestUser(t, db, "Emmanuel Ademu")

	svc := NewInboundAdminService(db)
	svc.AddClaimSource(InboundClaimTypeITMCGP, &fakeAdminClaimSource{claims: []InboundClaim{
		{Type: InboundClaimTypeITMCGP, ID: uuid.New(), UserID: user, AmountKobo: 200000},
	}})

	view, err := svc.Candidates(context.Background(), credit.ID)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if want := AccountNameTokens(credit.SenderName); !equalStrings(view.SenderNameTokens, want) {
		t.Errorf("senderNameTokens = %v, want %v (AccountNameTokens of the raw sender)", view.SenderNameTokens, want)
	}
	c := view.Candidates[0]
	if want := AccountNameTokens("Emmanuel Ademu"); !equalStrings(c.KYCNameTokens, want) {
		t.Errorf("kycNameTokens = %v, want %v", c.KYCNameTokens, want)
	}
	if !equalStrings(c.SharedNameTokens, []string{"ADEMU", "EMMANUEL"}) {
		t.Errorf("sharedNameTokens = %v, want [ADEMU EMMANUEL]", c.SharedNameTokens)
	}
	// The raw Didit legal name is not part of the payload: the tokens carry the
	// same match evidence without opening a second surface onto document data.
	blob, _ := json.Marshal(view)
	if strings.Contains(string(blob), "Emmanuel Ademu") {
		t.Errorf("candidate payload leaked the raw verified legal name: %s", blob)
	}
}

// Two claims that both matched on amount and name are exactly why the matcher
// held. The operator has to see that it was contention, not a near miss.
func TestInboundAdminCandidatesFlagsContention(t *testing.T) {
	db := setupInboundAdminDB(t)
	credit := insertHeldCredit(t, db, "ADEMU EMMANUEL", 200000)
	first := insertAdminTestUser(t, db, "Emmanuel Ademu")
	second := insertAdminTestUser(t, db, "Ademu Emmanuel")

	svc := NewInboundAdminService(db)
	svc.AddClaimSource(InboundClaimTypeITMCGP, &fakeAdminClaimSource{claims: []InboundClaim{
		{Type: InboundClaimTypeITMCGP, ID: uuid.New(), UserID: first, AmountKobo: 200000},
		{Type: InboundClaimTypeITStable, ID: uuid.New(), UserID: second, AmountKobo: 200000},
	}})

	view, err := svc.Candidates(context.Background(), credit.ID)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(view.Candidates) != 2 {
		t.Fatalf("got %d candidates, want 2", len(view.Candidates))
	}
	for _, c := range view.Candidates {
		if c.Rejection != InboundRejectContention {
			t.Errorf("claim %s rejection = %q, want %q", c.ClaimID, c.Rejection, InboundRejectContention)
		}
	}
}

// A single claim that matches on both is bindable — which means the matcher
// should have taken it, and the state changed underneath. Say so plainly
// rather than inventing a rejection reason.
func TestInboundAdminCandidatesMarksASoleMatchBindable(t *testing.T) {
	db := setupInboundAdminDB(t)
	credit := insertHeldCredit(t, db, "ADEMU EMMANUEL", 200000)
	user := insertAdminTestUser(t, db, "Emmanuel Ademu")

	svc := NewInboundAdminService(db)
	svc.AddClaimSource(InboundClaimTypeITMCGP, &fakeAdminClaimSource{claims: []InboundClaim{
		{Type: InboundClaimTypeITMCGP, ID: uuid.New(), UserID: user, AmountKobo: 200000},
	}})

	view, err := svc.Candidates(context.Background(), credit.ID)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if got := view.Candidates[0]; got.Rejection != "" || !got.AmountMatches {
		t.Errorf("sole exact match = %+v, want no rejection", got)
	}
}

// Kobo below a naira are provider rounding, not a different amount — the
// matcher compares at naira granularity and this view has to agree with it.
func TestInboundAdminCandidatesComparesAtNairaGranularity(t *testing.T) {
	db := setupInboundAdminDB(t)
	credit := insertHeldCredit(t, db, "ADEMU EMMANUEL", 200049)
	user := insertAdminTestUser(t, db, "Emmanuel Ademu")

	svc := NewInboundAdminService(db)
	svc.AddClaimSource(InboundClaimTypeITMCGP, &fakeAdminClaimSource{claims: []InboundClaim{
		{Type: InboundClaimTypeITMCGP, ID: uuid.New(), UserID: user, AmountKobo: 200000},
	}})

	view, err := svc.Candidates(context.Background(), credit.ID)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if view.MatchedNairaKobo != 200000 {
		t.Errorf("matchedNairaKobo = %d, want 200000", view.MatchedNairaKobo)
	}
	if !view.Candidates[0].AmountMatches {
		t.Error("49 kobo of provider rounding must not read as a different amount")
	}
}

// A claim source that is down must not blank the queue: the operator still
// needs the credit and whatever the other sources could say about it.
func TestInboundAdminCandidatesSurvivesABrokenClaimSource(t *testing.T) {
	db := setupInboundAdminDB(t)
	credit := insertHeldCredit(t, db, "ADEMU EMMANUEL", 200000)
	user := insertAdminTestUser(t, db, "Emmanuel Ademu")

	svc := NewInboundAdminService(db)
	svc.AddClaimSource(InboundClaimTypeP2PTrade, &fakeAdminClaimSource{err: errors.New("p2p is down")})
	svc.AddClaimSource(InboundClaimTypeITMCGP, &fakeAdminClaimSource{claims: []InboundClaim{
		{Type: InboundClaimTypeITMCGP, ID: uuid.New(), UserID: user, AmountKobo: 200000},
	}})

	view, err := svc.Candidates(context.Background(), credit.ID)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(view.Candidates) != 1 {
		t.Fatalf("got %d candidates, want the one the healthy source knew about", len(view.Candidates))
	}
	if len(view.SourceErrors) != 1 {
		t.Errorf("sourceErrors = %v, want the failure surfaced so the list is not read as complete", view.SourceErrors)
	}
}

func TestInboundAdminCandidatesUnknownCredit(t *testing.T) {
	svc := NewInboundAdminService(setupInboundAdminDB(t))
	if _, err := svc.Candidates(context.Background(), uuid.New()); !errors.Is(err, ErrInboundCreditNotFound) {
		t.Errorf("err = %v, want ErrInboundCreditNotFound", err)
	}
}

// ------------------------------------------------------------------ assign

// A manual match must run the matcher's own bind path. If this surface wrote
// the status itself, a hand-assigned credit and an auto-matched one would take
// different routes into the product's settle path and could diverge.
func TestInboundAdminAssignDelegatesToTheMatcher(t *testing.T) {
	db := setupInboundAdminDB(t)
	credit := insertHeldCredit(t, db, "ADEMU EMMANUEL", 200000)
	admin := insertAdminTestUser(t, db, "")
	claimID := uuid.New()

	binder := &fakeBinder{onBind: func() {
		db.Model(&models.InboundCredit{}).Where("id = ?", credit.ID).Updates(map[string]any{
			"status":             models.InboundCreditMatched,
			"matched_claim_type": InboundClaimTypeITMCGP,
			"matched_claim_id":   claimID.String(),
			"resolved_by":        admin.String(),
		})
	}}
	svc := NewInboundAdminService(db)
	svc.SetBinder(binder)

	out, err := svc.Assign(context.Background(), admin, credit.ID, InboundClaimTypeITMCGP, claimID, "sender confirmed by phone")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if len(binder.calls) != 1 {
		t.Fatalf("BindManually called %d times, want 1", len(binder.calls))
	}
	call := binder.calls[0]
	if call.creditID != credit.ID || call.claimType != InboundClaimTypeITMCGP || call.claimID != claimID || call.adminID != admin {
		t.Errorf("BindManually args = %+v", call)
	}
	if out.Status != models.InboundCreditMatched {
		t.Errorf("status = %q, want the matcher's own result re-read from the row", out.Status)
	}

	logs := readAuditLogs(t, db, credit.ID)
	if len(logs) != 1 || logs[0].Action != models.InboundAuditAssigned {
		t.Fatalf("audit = %+v, want one inbound.assigned row", logs)
	}
	if logs[0].ActorID == nil || *logs[0].ActorID != admin {
		t.Errorf("audit actor = %v, want %s", logs[0].ActorID, admin)
	}
	if !strings.Contains(string(logs[0].Metadata), claimID.String()) {
		t.Errorf("audit metadata = %s, want the claim it was bound to", logs[0].Metadata)
	}
}

// A bind that fails must leave the credit held AND leave no audit row claiming
// an assignment that never happened.
func TestInboundAdminAssignFailureWritesNoAudit(t *testing.T) {
	db := setupInboundAdminDB(t)
	credit := insertHeldCredit(t, db, "ADEMU EMMANUEL", 200000)
	admin := insertAdminTestUser(t, db, "")

	svc := NewInboundAdminService(db)
	svc.SetBinder(&fakeBinder{err: errors.New("claim already settled")})

	if _, err := svc.Assign(context.Background(), admin, credit.ID, InboundClaimTypeITMCGP, uuid.New(), "note"); err == nil {
		t.Fatal("Assign should have failed")
	}
	if logs := readAuditLogs(t, db, credit.ID); len(logs) != 0 {
		t.Errorf("wrote %d audit rows for a failed bind", len(logs))
	}
	var after models.InboundCredit
	if err := db.First(&after, "id = ?", credit.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != models.InboundCreditHeld {
		t.Errorf("status = %q, want it still held", after.Status)
	}
}

func TestInboundAdminAssignValidation(t *testing.T) {
	db := setupInboundAdminDB(t)
	credit := insertHeldCredit(t, db, "ADEMU EMMANUEL", 200000)
	admin := insertAdminTestUser(t, db, "")
	svc := NewInboundAdminService(db)

	// No matcher wired: refuse rather than write the status here, which is the
	// exact divergence this endpoint exists to prevent.
	if _, err := svc.Assign(context.Background(), admin, credit.ID, InboundClaimTypeITMCGP, uuid.New(), "n"); !errors.Is(err, ErrInboundBinderUnavailable) {
		t.Errorf("no binder: err = %v, want ErrInboundBinderUnavailable", err)
	}

	svc.SetBinder(&fakeBinder{})
	if _, err := svc.Assign(context.Background(), admin, credit.ID, "not_a_product", uuid.New(), "n"); !errors.Is(err, ErrInboundBadRequest) {
		t.Errorf("bad claim type: err = %v, want ErrInboundBadRequest", err)
	}
	if _, err := svc.Assign(context.Background(), admin, uuid.New(), InboundClaimTypeITMCGP, uuid.New(), "n"); !errors.Is(err, ErrInboundCreditNotFound) {
		t.Errorf("unknown credit: err = %v, want ErrInboundCreditNotFound", err)
	}

	// Already attributed: re-binding would strand the first attribution.
	settled := insertHeldCredit(t, db, "SOMEONE", 100000)
	db.Model(settled).Update("status", models.InboundCreditMatched)
	if _, err := svc.Assign(context.Background(), admin, settled.ID, InboundClaimTypeITMCGP, uuid.New(), "n"); !errors.Is(err, ErrInboundCreditNotHeld) {
		t.Errorf("already matched: err = %v, want ErrInboundCreditNotHeld", err)
	}
}

// --------------------------------------------------------- refund / ignore

func TestInboundAdminMarkRefundedAndIgnored(t *testing.T) {
	db := setupInboundAdminDB(t)
	admin := insertAdminTestUser(t, db, "")
	svc := NewInboundAdminService(db)

	refunded := insertHeldCredit(t, db, "ADEMU EMMANUEL", 200000)
	out, err := svc.MarkRefunded(context.Background(), admin, refunded.ID, "returned via FW transfer TRF-99")
	if err != nil {
		t.Fatalf("MarkRefunded: %v", err)
	}
	if out.Status != models.InboundCreditRefunded {
		t.Errorf("status = %q, want refunded", out.Status)
	}
	if out.ResolvedBy == nil || *out.ResolvedBy != admin {
		t.Errorf("resolvedBy = %v, want %s", out.ResolvedBy, admin)
	}
	// The matcher's reason is evidence of why a human was needed; resolving
	// must not erase it.
	if out.HoldReason != refunded.HoldReason {
		t.Errorf("holdReason = %q, want the original %q", out.HoldReason, refunded.HoldReason)
	}
	logs := readAuditLogs(t, db, refunded.ID)
	if len(logs) != 1 || logs[0].Action != models.InboundAuditRefunded || logs[0].Note == "" {
		t.Fatalf("audit = %+v, want one inbound.refunded row carrying the operator's note", logs)
	}

	ignored := insertHeldCredit(t, db, "OUR OWN FLOAT", 5000000)
	out, err = svc.MarkIgnored(context.Background(), admin, ignored.ID, "company float top-up, not customer money")
	if err != nil {
		t.Fatalf("MarkIgnored: %v", err)
	}
	if out.Status != models.InboundCreditIgnored {
		t.Errorf("status = %q, want ignored", out.Status)
	}
	if logs := readAuditLogs(t, db, ignored.ID); len(logs) != 1 || logs[0].Action != models.InboundAuditIgnored {
		t.Errorf("audit = %+v, want one inbound.ignored row", logs)
	}
}

// "Refunded" asserts money left the account and "ignored" asserts it was never
// the customer's. Both are unverifiable after the fact, so the operator has to
// say what they did.
func TestInboundAdminResolveRequiresANote(t *testing.T) {
	db := setupInboundAdminDB(t)
	admin := insertAdminTestUser(t, db, "")
	svc := NewInboundAdminService(db)
	credit := insertHeldCredit(t, db, "ADEMU EMMANUEL", 200000)

	if _, err := svc.MarkRefunded(context.Background(), admin, credit.ID, "   "); !errors.Is(err, ErrInboundBadRequest) {
		t.Errorf("blank refund note: err = %v, want ErrInboundBadRequest", err)
	}
	if _, err := svc.MarkIgnored(context.Background(), admin, credit.ID, ""); !errors.Is(err, ErrInboundBadRequest) {
		t.Errorf("blank ignore note: err = %v, want ErrInboundBadRequest", err)
	}
	if logs := readAuditLogs(t, db, credit.ID); len(logs) != 0 {
		t.Errorf("wrote %d audit rows for a rejected action", len(logs))
	}
}

// Two operators on the same queue entry: the second must be told it already
// moved, not silently overwrite the first one's decision.
func TestInboundAdminResolveIsSingleWinner(t *testing.T) {
	db := setupInboundAdminDB(t)
	admin := insertAdminTestUser(t, db, "")
	other := insertAdminTestUser(t, db, "")
	svc := NewInboundAdminService(db)
	credit := insertHeldCredit(t, db, "ADEMU EMMANUEL", 200000)

	if _, err := svc.MarkRefunded(context.Background(), admin, credit.ID, "sent back"); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if _, err := svc.MarkIgnored(context.Background(), other, credit.ID, "not ours"); !errors.Is(err, ErrInboundCreditNotHeld) {
		t.Errorf("second resolve: err = %v, want ErrInboundCreditNotHeld", err)
	}
	var after models.InboundCredit
	if err := db.First(&after, "id = ?", credit.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != models.InboundCreditRefunded || *after.ResolvedBy != admin {
		t.Errorf("credit = %q by %v, want refunded by the first operator", after.Status, after.ResolvedBy)
	}
	if logs := readAuditLogs(t, db, credit.ID); len(logs) != 1 {
		t.Errorf("audit rows = %d, want only the action that actually happened", len(logs))
	}
}

func TestInboundAdminNairaRounding(t *testing.T) {
	tests := map[int64]int64{
		0:      0,
		49:     0,
		50:     100,
		199:    200,
		200000: 200000,
		200049: 200000,
		200050: 200100,
	}
	for in, want := range tests {
		if got := roundKoboToNaira(in); got != want {
			t.Errorf("roundKoboToNaira(%d) = %d, want %d", in, got, want)
		}
	}
}

func readAuditLogs(t *testing.T, db *gorm.DB, creditID uuid.UUID) []models.InboundCreditAuditLog {
	t.Helper()
	var out []models.InboundCreditAuditLog
	if err := db.Where("credit_id = ?", creditID).Order("created_at").Find(&out).Error; err != nil {
		t.Fatalf("read audit: %v", err)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
