package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// fakeBanks is a BankDirectory with a fixed list and fixed name enquiries.
type fakeBanks struct {
	banks    []NombaBank
	accounts map[string]string // bankCode/number -> account name
	lookups  int
}

func (f *fakeBanks) ListBanks(context.Context) ([]NombaBank, error) { return f.banks, nil }

func (f *fakeBanks) Resolve(_ context.Context, code, number string) (NombaLookupResult, error) {
	f.lookups++
	name, ok := f.accounts[code+"/"+number]
	if !ok {
		return NombaLookupResult{}, ErrNombaAccountNotFound
	}
	return NombaLookupResult{AccountNumber: number, AccountName: name}, nil
}

func newFakeBanks() *fakeBanks {
	return &fakeBanks{
		banks: []NombaBank{
			{Code: "999992", Name: "OPAY DIGITAL SERVICES LIMITED (OPAY)"},
			{Code: "011", Name: "First Bank of Nigeria"},
			{Code: "058", Name: "Guaranty Trust Bank"},
		},
		accounts: map[string]string{
			"999992/8012345678": "JOYCLARE KOSARACHI ELEM",
			"058/0123456789":    "ADA LOVELACE",
		},
	}
}

// createPayerDeclarationsTable is the inbound_payer_declarations DDL; every
// matcher rig needs it, because the no-name path consults declarations.
func createPayerDeclarationsTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE inbound_payer_declarations (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL,
		claim_type TEXT NOT NULL, claim_id TEXT NOT NULL, amount_kobo INTEGER NOT NULL,
		bank_code TEXT NOT NULL, bank_name TEXT NOT NULL, account_number TEXT NOT NULL,
		typed_name TEXT NOT NULL, resolved_name TEXT NOT NULL,
		status TEXT NOT NULL, review_reason TEXT, credit_id TEXT, matched_at DATETIME,
		created_at DATETIME, updated_at DATETIME,
		UNIQUE (claim_type, claim_id))`).Error; err != nil {
		t.Fatalf("create inbound_payer_declarations: %v", err)
	}
}

func setupPayerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupMatcherDB(t)
	for _, ddl := range []string{
		`ALTER TABLE users ADD COLUMN verification_status TEXT`,
		`CREATE TABLE inbound_credit_audit_logs (
			id TEXT PRIMARY KEY, credit_id TEXT NOT NULL, actor_id TEXT,
			action TEXT NOT NULL, note TEXT, metadata TEXT, created_at DATETIME)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	return db
}

func seedVerifiedBuyer(t *testing.T, db *gorm.DB, legalName string) uuid.UUID {
	t.Helper()
	id := seedMatcherUser(t, db, legalName)
	if err := db.Exec(`UPDATE users SET verification_status = ? WHERE id = ?`,
		models.VerificationStatusVerified, id).Error; err != nil {
		t.Fatalf("verify user: %v", err)
	}
	return id
}

// seedSenderCredit is a credit from a named sender at a named bank, at a time.
func seedSenderCredit(t *testing.T, db *gorm.DB, ref string, kobo int64, bank, name string, at time.Time, status string) *models.InboundCredit {
	t.Helper()
	c := &models.InboundCredit{
		Provider: models.InboundCreditProviderFlutterwave, ProviderRef: ref, Currency: "NGN",
		AmountKobo: kobo, GrossKobo: kobo,
		SenderRaw: "Received Money from " + bank + "|" + name, SenderBank: bank, SenderName: name,
		TransactionDate: at.UTC(), SeenAt: at.UTC(), Status: status,
	}
	if status == models.InboundCreditHeld {
		c.HoldReasonCode = models.InboundHoldNoNameMatch
		c.HoldReason = "test hold"
	}
	if err := db.Create(c).Error; err != nil {
		t.Fatalf("insert credit: %v", err)
	}
	return c
}

type payerRig struct {
	db    *gorm.DB
	src   *fakeClaimSource
	m     *InboundMatcher
	banks *fakeBanks
	svc   *InboundPayerService
	buyer uuid.UUID
	claim InboundClaim
}

// newPayerRig: a verified buyer "Chidi Okonkwo" with an open ₦1,500 claim,
// opened 10 minutes ago.
func newPayerRig(t *testing.T, kobo int64) *payerRig {
	t.Helper()
	db := setupPayerDB(t)
	buyer := seedVerifiedBuyer(t, db, "Chidi Okonkwo")
	claim := claimFor(buyer, kobo)
	claim.OpenedAt = time.Now().Add(-10 * time.Minute)
	src := &fakeClaimSource{typ: InboundClaimTypeITStable, claims: []InboundClaim{claim}}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITStable, src)
	banks := newFakeBanks()
	return &payerRig{db: db, src: src, m: m, banks: banks, buyer: buyer, claim: claim,
		svc: NewInboundPayerService(db, m, banks)}
}

func (r *payerRig) declare(t *testing.T, bank, number, name string) (*DeclarePayerResult, error) {
	t.Helper()
	return r.svc.Declare(context.Background(), r.buyer, r.claim.ID, bank, number, name)
}

func TestPayer_HeldCreditFromAnotherAccountBindsOnDeclaration(t *testing.T) {
	r := newPayerRig(t, 150_000)
	credit := seedSenderCredit(t, r.db, "sess-1", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now().Add(-5*time.Minute), models.InboundCreditHeld)

	res, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if res.Outcome != models.PayerDeclarationMatched {
		t.Fatalf("outcome = %q (%s), want matched", res.Outcome, res.Declaration.ReviewReason)
	}
	got := reloadCredit(t, r.db, credit.ID)
	if got.Status != models.InboundCreditMatched || got.MatchedClaimID == nil || *got.MatchedClaimID != r.claim.ID {
		t.Fatalf("credit = %s / %v, want matched to the claim", got.Status, got.MatchedClaimID)
	}
	if got.MatchStrength != string(NameMatchDeclared) {
		t.Errorf("strength = %q, want declared", got.MatchStrength)
	}
	if got.ResolvedBy != nil {
		t.Error("a declared bind is not an operator's: resolved_by must stay empty")
	}
	if len(r.src.accepted) != 1 {
		t.Fatalf("accepted %d times, want 1 (the product's own settle path)", len(r.src.accepted))
	}
	var audits int64
	r.db.Model(&models.InboundCreditAuditLog{}).
		Where("credit_id = ? AND action = ?", credit.ID, models.InboundAuditPayerDeclared).Count(&audits)
	if audits != 1 {
		t.Errorf("audit rows = %d, want 1", audits)
	}
}

func TestPayer_TypedNameMustMatchTheBanksName(t *testing.T) {
	r := newPayerRig(t, 150_000)
	_, err := r.declare(t, "999992", "8012345678", "Someone Else Entirely")
	var mismatch *PayerNameMismatchError
	if !errors.As(err, &mismatch) || mismatch.ResolvedName != "JOYCLARE KOSARACHI ELEM" {
		t.Fatalf("err = %v, want a name mismatch naming the bank's record", err)
	}
	var n int64
	r.db.Model(&models.InboundPayerDeclaration{}).Count(&n)
	if n != 0 {
		t.Errorf("a refused declaration was saved (%d rows)", n)
	}
}

func TestPayer_SenderNameMustMatchTheDeclaredAccount(t *testing.T) {
	r := newPayerRig(t, 150_000)
	credit := seedSenderCredit(t, r.db, "sess-2", 150_000, "OPAY", "SOMEBODY ELSE",
		time.Now().Add(-5*time.Minute), models.InboundCreditHeld)

	res, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if res.Outcome != models.PayerDeclarationWaiting {
		t.Fatalf("outcome = %q, want waiting — the held credit is from someone else", res.Outcome)
	}
	if got := reloadCredit(t, r.db, credit.ID); got.Status != models.InboundCreditHeld {
		t.Errorf("credit status = %q, want still held", got.Status)
	}
}

func TestPayer_DifferentBankDoesNotBind(t *testing.T) {
	r := newPayerRig(t, 150_000)
	seedSenderCredit(t, r.db, "sess-3", 150_000, "PalmPay", "JOYCLARE KOSARACHI ELEM",
		time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	res, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if res.Outcome != models.PayerDeclarationWaiting {
		t.Fatalf("outcome = %q, want waiting — same name, other bank", res.Outcome)
	}
}

func TestPayer_CreditBeforeTheOrderOpenedIsIgnored(t *testing.T) {
	r := newPayerRig(t, 150_000)
	seedSenderCredit(t, r.db, "sess-4", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now().Add(-2*time.Hour), models.InboundCreditHeld)
	res, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if res.Outcome != models.PayerDeclarationWaiting {
		t.Fatalf("outcome = %q, want waiting — that money predates the order", res.Outcome)
	}
}

func TestPayer_OverTheCapGoesToReview(t *testing.T) {
	r := newPayerRig(t, DefaultPayerSelfResolveMaxKobo+100)
	credit := seedSenderCredit(t, r.db, "sess-5", DefaultPayerSelfResolveMaxKobo+100, "OPAY",
		"JOYCLARE KOSARACHI ELEM", time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	res, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if res.Outcome != models.PayerDeclarationReview {
		t.Fatalf("outcome = %q, want review over ₦500,000", res.Outcome)
	}
	if res.Declaration.CreditID == nil || *res.Declaration.CreditID != credit.ID {
		t.Error("review should point the admin at the credit")
	}
	if got := reloadCredit(t, r.db, credit.ID); got.Status != models.InboundCreditHeld {
		t.Errorf("credit status = %q, want still held for the admin", got.Status)
	}
	if len(r.src.accepted) != 0 {
		t.Error("nothing may settle over the cap")
	}
}

func TestPayer_TwoFittingCreditsGoToReview(t *testing.T) {
	r := newPayerRig(t, 150_000)
	for _, ref := range []string{"sess-6a", "sess-6b"} {
		seedSenderCredit(t, r.db, ref, 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
			time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	}
	res, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if res.Outcome != models.PayerDeclarationReview {
		t.Fatalf("outcome = %q, want review", res.Outcome)
	}
}

func TestPayer_SomeoneElsesOrderReadsAsNoOrder(t *testing.T) {
	r := newPayerRig(t, 150_000)
	stranger := seedVerifiedBuyer(t, r.db, "Mallam Stranger")
	_, err := r.svc.Declare(context.Background(), stranger, r.claim.ID, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if !errors.Is(err, ErrPayerClaimNotFound) {
		t.Fatalf("err = %v, want ErrPayerClaimNotFound", err)
	}
	if r.banks.lookups != 0 {
		t.Error("no name enquiry may run for someone else's order")
	}
}

func TestPayer_UnverifiedBuyerIsRefused(t *testing.T) {
	r := newPayerRig(t, 150_000)
	r.db.Exec(`UPDATE users SET verification_status = 'pending' WHERE id = ?`, r.buyer)
	_, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if !errors.Is(err, ErrKYCVerificationRequired) {
		t.Fatalf("err = %v, want ErrKYCVerificationRequired", err)
	}
}

// A declaration made before the money arrives: the matcher binds the credit
// when it lands, instead of holding it as no_name_match.
func TestPayer_MatcherBindsALaterCreditOnTheDeclaration(t *testing.T) {
	r := newPayerRig(t, 150_000)
	res, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if err != nil || res.Outcome != models.PayerDeclarationWaiting {
		t.Fatalf("Declare: %v / %v, want waiting", err, res)
	}
	credit := seedSenderCredit(t, r.db, "sess-7", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now(), models.InboundCreditUnmatched)

	if _, err := r.m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	got := reloadCredit(t, r.db, credit.ID)
	if got.Status != models.InboundCreditMatched || got.MatchStrength != string(NameMatchDeclared) {
		t.Fatalf("credit = %s/%s (%s), want matched on the declaration", got.Status, got.MatchStrength, got.HoldReason)
	}
	var d models.InboundPayerDeclaration
	r.db.First(&d, "id = ?", res.Declaration.ID)
	if d.Status != models.PayerDeclarationMatched || d.CreditID == nil || *d.CreditID != credit.ID {
		t.Errorf("declaration = %s/%v, want matched to the credit", d.Status, d.CreditID)
	}
}

// The buyer's own name still wins: a declaration never changes an ordinary
// match.
func TestPayer_OwnNameStillMatchesWithoutADeclaration(t *testing.T) {
	r := newPayerRig(t, 150_000)
	credit := seedSenderCredit(t, r.db, "sess-8", 150_000, "OPAY", "CHIDI OKONKWO",
		time.Now(), models.InboundCreditUnmatched)
	if _, err := r.m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	if got := reloadCredit(t, r.db, credit.ID); got.MatchStrength != string(NameMatchExact) {
		t.Errorf("strength = %q, want exact", got.MatchStrength)
	}
}

func TestPayer_ProductReviewKeepsTheDeclarationOpen(t *testing.T) {
	r := newPayerRig(t, 150_000)
	r.src.acceptErr = &ClaimNeedsReviewError{Code: "trade_not_within_merchant_cap", Reason: "over cap"}
	credit := seedSenderCredit(t, r.db, "sess-9", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	res, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if res.Outcome != models.PayerDeclarationReview {
		t.Fatalf("outcome = %q, want review — the product refused", res.Outcome)
	}
	if got := reloadCredit(t, r.db, credit.ID); got.Status != models.InboundCreditHeld {
		t.Errorf("credit = %q, want held", got.Status)
	}
}

func TestPayerSameBank(t *testing.T) {
	cases := []struct {
		declared, statement string
		want                bankVerdict
	}{
		{"OPAY DIGITAL SERVICES LIMITED (OPAY)", "OPAY", bankSame},
		{"First Bank of Nigeria", "FIRST BANK OF NIGERIA PLC", bankSame},
		{"Guaranty Trust Bank", "GTBANK", bankSame},
		{"PALMPAY", "PalmPay", bankSame},
		{"OPAY DIGITAL SERVICES LIMITED (OPAY)", "PalmPay", bankDifferent},
		{"First Bank of Nigeria", "First City Monument Bank", bankUnsure},
		{"First Bank of Nigeria", "", bankUnsure},
	}
	for _, c := range cases {
		if got := payerSameBank(c.declared, c.statement); got != c.want {
			t.Errorf("payerSameBank(%q, %q) = %v, want %v", c.declared, c.statement, got, c.want)
		}
	}
}

// A buyer can correct a declaration that has not matched yet; the row is
// replaced, never duplicated.
func TestPayer_RedeclaringReplacesAnUnmatchedDeclaration(t *testing.T) {
	r := newPayerRig(t, 150_000)
	first, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if err != nil || first.Outcome != models.PayerDeclarationWaiting {
		t.Fatalf("first: %v / %v", err, first)
	}
	second, err := r.declare(t, "058", "0123456789", "Ada Lovelace")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.Declaration.ID != first.Declaration.ID {
		t.Errorf("id changed %s -> %s, want the same row updated", first.Declaration.ID, second.Declaration.ID)
	}
	if second.Declaration.ResolvedName != "ADA LOVELACE" || second.Declaration.BankCode != "058" {
		t.Errorf("declaration = %+v, want the corrected account", second.Declaration)
	}
	var n int64
	r.db.Model(&models.InboundPayerDeclaration{}).Count(&n)
	if n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

// Security review 2026-10-16: a declaration must never take a credit the
// matcher has not decided yet — its sender may be another buyer whose own
// name would bind it on the next tick.
func TestPayer_DeclarationNeverTakesAnUnmatchedCredit(t *testing.T) {
	r := newPayerRig(t, 150_000)
	credit := seedSenderCredit(t, r.db, "sess-10", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now().Add(-1*time.Minute), models.InboundCreditUnmatched)
	res, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if res.Outcome != models.PayerDeclarationWaiting {
		t.Fatalf("outcome = %q, want waiting — the matcher decides unmatched credits", res.Outcome)
	}
	if got := reloadCredit(t, r.db, credit.ID); got.Status != models.InboundCreditUnmatched {
		t.Errorf("credit = %q, want untouched", got.Status)
	}
}

// The attack the review describes: the victim pays from their own account for
// their own order; an attacker with a same-amount order declares the victim's
// account first. The matcher must still give the money to the victim.
func TestPayer_OwnersNameBeatsAnotherBuyersDeclaration(t *testing.T) {
	r := newPayerRig(t, 150_000) // the attacker's order
	victim := seedVerifiedBuyer(t, r.db, "Joyclare Kosarachi Elem")
	victimClaim := claimFor(victim, 150_000)
	victimClaim.OpenedAt = time.Now().Add(-10 * time.Minute)
	r.src.claims = append(r.src.claims, victimClaim)

	if _, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem"); err != nil {
		t.Fatalf("Declare: %v", err)
	}
	credit := seedSenderCredit(t, r.db, "sess-11", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now(), models.InboundCreditUnmatched)
	if _, err := r.m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	got := reloadCredit(t, r.db, credit.ID)
	if got.MatchedClaimID == nil || *got.MatchedClaimID != victimClaim.ID {
		t.Fatalf("credit matched to %v, want the victim's own claim", got.MatchedClaimID)
	}
}

// A credit held for any reason other than "nobody's name" belongs to an
// operator: two names matched here, and a third buyer must not take it.
func TestPayer_ContestedHoldIsNotForDeclarations(t *testing.T) {
	r := newPayerRig(t, 150_000)
	credit := seedSenderCredit(t, r.db, "sess-12", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	r.db.Model(&models.InboundCredit{}).Where("id = ?", credit.ID).
		Update("hold_reason_code", models.InboundHoldContested)

	res, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if res.Outcome == models.PayerDeclarationMatched {
		t.Fatal("a contested credit was taken by a declaration")
	}
	if got := reloadCredit(t, r.db, credit.ID); got.Status != models.InboundCreditHeld {
		t.Errorf("credit = %q, want still held", got.Status)
	}
}

// With another buyer waiting on the same amount, the money may be theirs: an
// admin decides.
func TestPayer_AnotherOrderAtTheSameAmountGoesToReview(t *testing.T) {
	r := newPayerRig(t, 150_000)
	other := seedVerifiedBuyer(t, r.db, "Ngozi Obi")
	r.src.claims = append(r.src.claims, claimFor(other, 150_000))
	credit := seedSenderCredit(t, r.db, "sess-13", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now().Add(-5*time.Minute), models.InboundCreditHeld)

	res, err := r.declare(t, "999992", "8012345678", "Joyclare Kosarachi Elem")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if res.Outcome != models.PayerDeclarationReview {
		t.Fatalf("outcome = %q, want review", res.Outcome)
	}
	if got := reloadCredit(t, r.db, credit.ID); got.Status != models.InboundCreditHeld {
		t.Errorf("credit = %q, want still held", got.Status)
	}
}
