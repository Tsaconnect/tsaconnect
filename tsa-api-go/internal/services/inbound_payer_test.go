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

// soleCandidate records the rig's claim as the only candidate when the credit
// was held — what the matcher writes on a no-name hold.
func (r *payerRig) soleCandidate(t *testing.T, credit *models.InboundCredit) {
	t.Helper()
	if err := r.db.Model(&models.InboundCredit{}).Where("id = ?", credit.ID).
		Update("held_claim_ids", InboundClaimTypeITStable+":"+r.claim.ID.String()).Error; err != nil {
		t.Fatalf("held_claim_ids: %v", err)
	}
}

func (r *payerRig) declare(t *testing.T, bank, number string) (*DeclarePayerResult, error) {
	t.Helper()
	return r.svc.Declare(context.Background(), r.buyer, r.claim.ID, bank, number)
}

func TestPayer_HeldCreditFromAnotherAccountBindsOnDeclaration(t *testing.T) {
	r := newPayerRig(t, 150_000)
	credit := seedSenderCredit(t, r.db, "sess-1", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	r.soleCandidate(t, credit)

	res, err := r.declare(t, "999992", "8012345678")
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

// The buyer never types the name: it is the bank's, from the name enquiry.
func TestPayer_NameComesFromTheBank(t *testing.T) {
	r := newPayerRig(t, 150_000)
	res, err := r.declare(t, "999992", "8012345678")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if res.Declaration.ResolvedName != "JOYCLARE KOSARACHI ELEM" || r.banks.lookups != 1 {
		t.Fatalf("declaration name = %q (lookups %d), want the bank's record", res.Declaration.ResolvedName, r.banks.lookups)
	}
}

// An account number the bank doesn't know is refused, nothing saved.
func TestPayer_UnknownAccountIsRefused(t *testing.T) {
	r := newPayerRig(t, 150_000)
	if _, err := r.declare(t, "999992", "0000000000"); !errors.Is(err, ErrPayerAccountNotFound) {
		t.Fatalf("err = %v, want ErrPayerAccountNotFound", err)
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

	res, err := r.declare(t, "999992", "8012345678")
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
	res, err := r.declare(t, "999992", "8012345678")
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
	res, err := r.declare(t, "999992", "8012345678")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if res.Outcome != models.PayerDeclarationWaiting {
		t.Fatalf("outcome = %q, want waiting — that money predates the order", res.Outcome)
	}
}

func TestPayer_OverTheCapGoesToReview(t *testing.T) {
	r := newPayerRig(t, PayerSelfResolveMaxKobo+100)
	credit := seedSenderCredit(t, r.db, "sess-5", PayerSelfResolveMaxKobo+100, "OPAY",
		"JOYCLARE KOSARACHI ELEM", time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	res, err := r.declare(t, "999992", "8012345678")
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
	res, err := r.declare(t, "999992", "8012345678")
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
	_, err := r.svc.Declare(context.Background(), stranger, r.claim.ID, "999992", "8012345678")
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
	_, err := r.declare(t, "999992", "8012345678")
	if !errors.Is(err, ErrKYCVerificationRequired) {
		t.Fatalf("err = %v, want ErrKYCVerificationRequired", err)
	}
}

// A declaration made before the money arrives: the matcher binds the credit
// when it lands, instead of holding it as no_name_match.
func TestPayer_MatcherBindsALaterCreditOnTheDeclaration(t *testing.T) {
	r := newPayerRig(t, 150_000)
	res, err := r.declare(t, "999992", "8012345678")
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
	r.soleCandidate(t, credit)
	res, err := r.declare(t, "999992", "8012345678")
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
	first, err := r.declare(t, "999992", "8012345678")
	if err != nil || first.Outcome != models.PayerDeclarationWaiting {
		t.Fatalf("first: %v / %v", err, first)
	}
	second, err := r.declare(t, "058", "0123456789")
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
	res, err := r.declare(t, "999992", "8012345678")
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

	if _, err := r.declare(t, "999992", "8012345678"); err != nil {
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

	res, err := r.declare(t, "999992", "8012345678")
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

	res, err := r.declare(t, "999992", "8012345678")
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

// An admin deciding the credit a declaration points at settles the declaration
// too, so the review queue empties as admins work it.
func TestPayer_AdminAssignMatchesTheDeclarationAndRefundClosesIt(t *testing.T) {
	ctx := context.Background()
	admin := uuid.New()

	// Over the cap, so the declaration waits in review for the admin.
	r := newPayerRig(t, PayerSelfResolveMaxKobo+100)
	credit := seedSenderCredit(t, r.db, "sess-14", PayerSelfResolveMaxKobo+100, "OPAY",
		"JOYCLARE KOSARACHI ELEM", time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	res, err := r.declare(t, "999992", "8012345678")
	if err != nil || res.Outcome != models.PayerDeclarationReview {
		t.Fatalf("Declare: %v / %v, want review", err, res)
	}
	adminSvc := NewInboundAdminService(r.db)
	adminSvc.SetBinder(r.m)
	adminSvc.AddClaimSource(InboundClaimTypeITStable, r.src)
	if _, err := adminSvc.Assign(ctx, admin, credit.ID, InboundClaimTypeITStable, r.claim.ID, "checked the receipt"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	var d models.InboundPayerDeclaration
	r.db.First(&d, "id = ?", res.Declaration.ID)
	if d.Status != models.PayerDeclarationMatched || d.CreditID == nil || *d.CreditID != credit.ID {
		t.Fatalf("after assign: %s/%v, want matched to the credit", d.Status, d.CreditID)
	}

	// A refund closes a review declaration that pointed at the credit.
	r2 := newPayerRig(t, PayerSelfResolveMaxKobo+100)
	credit2 := seedSenderCredit(t, r2.db, "sess-15", PayerSelfResolveMaxKobo+100, "OPAY",
		"JOYCLARE KOSARACHI ELEM", time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	res2, err := r2.declare(t, "999992", "8012345678")
	if err != nil || res2.Outcome != models.PayerDeclarationReview {
		t.Fatalf("Declare: %v / %v, want review", err, res2)
	}
	if _, err := NewInboundAdminService(r2.db).MarkRefunded(ctx, admin, credit2.ID, "sent back to the sender"); err != nil {
		t.Fatalf("MarkRefunded: %v", err)
	}
	var d2 models.InboundPayerDeclaration
	r2.db.First(&d2, "id = ?", res2.Declaration.ID)
	if d2.Status != models.PayerDeclarationClosed || d2.ReviewReason == "" {
		t.Fatalf("after refund: %s %q, want closed with a reason", d2.Status, d2.ReviewReason)
	}
}

// A manual assignment the matcher refuses carries a sentinel, so the console
// gets a 409 with the real reason instead of a bare 500.
func TestInboundMatcher_BindManuallyRefusalIsTyped(t *testing.T) {
	r := newPayerRig(t, 150_000)
	credit := seedSenderCredit(t, r.db, "sess-16", 200_000, "OPAY", "SOMEONE",
		time.Now(), models.InboundCreditHeld)
	err := r.m.BindManually(context.Background(), credit.ID, InboundClaimTypeITStable, r.claim.ID, uuid.New())
	if !errors.Is(err, ErrInboundBindRefused) {
		t.Fatalf("err = %v, want ErrInboundBindRefused (amount differs)", err)
	}
}

// ─── review 2026-10-16 ───────────────────────────────────────────────────────

// #1: a declared bind claims the credit (binding) before the product settles,
// so an admin refund racing it is refused instead of paying twice.
func TestPayer_AdminRefundCannotRaceADeclaredBind(t *testing.T) {
	r := newPayerRig(t, 150_000)
	credit := seedSenderCredit(t, r.db, "sess-20", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	r.soleCandidate(t, credit)

	admin := NewInboundAdminService(r.db)
	var refundErr error
	// The admin acts while the product settle runs.
	r.src.onAccept = func() {
		_, refundErr = admin.MarkRefunded(context.Background(), uuid.New(), credit.ID, "refunding")
	}
	res, err := r.declare(t, "999992", "8012345678")
	if err != nil || res.Outcome != models.PayerDeclarationMatched {
		t.Fatalf("Declare: %v / %v, want matched", err, res)
	}
	if !errors.Is(refundErr, ErrInboundCreditNotHeld) {
		t.Fatalf("racing refund err = %v, want ErrInboundCreditNotHeld", refundErr)
	}
	if got := reloadCredit(t, r.db, credit.ID); got.Status != models.InboundCreditMatched {
		t.Errorf("credit = %q, want matched (and never refunded)", got.Status)
	}
}

// #1: a bind interrupted after the claim is finished by the next tick, never
// handed back to held where an admin could refund it.
func TestPayer_InterruptedBindIsFinishedNotHandedBack(t *testing.T) {
	r := newPayerRig(t, 150_000)
	credit := seedSenderCredit(t, r.db, "sess-21", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	r.soleCandidate(t, credit)
	r.src.acceptErr = errors.New("chain blip")
	if _, err := r.declare(t, "999992", "8012345678"); err != nil {
		t.Fatalf("Declare: %v (a settle failure is logged, the declaration saved)", err)
	}
	if got := reloadCredit(t, r.db, credit.ID); got.Status != models.InboundCreditBinding {
		t.Fatalf("credit = %q, want binding until finished", got.Status)
	}

	r.src.acceptErr = nil
	// Not resumed while the bind could still be running…
	if _, err := r.m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	if got := reloadCredit(t, r.db, credit.ID); got.Status != models.InboundCreditBinding {
		t.Fatalf("credit = %q, want still binding — a live bind is never resumed alongside itself", got.Status)
	}
	// …only once it is clearly interrupted.
	r.m.now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := r.m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	got := reloadCredit(t, r.db, credit.ID)
	if got.Status != models.InboundCreditMatched || got.MatchStrength != string(NameMatchDeclared) {
		t.Fatalf("credit = %s/%s, want matched on resume", got.Status, got.MatchStrength)
	}
}

// #2: "First Bank" alone must not read as the same bank as FCMB.
func TestPayerSameBank_AmbiguousSingleWordIsUnsure(t *testing.T) {
	if got := payerSameBank("First City Monument Bank", "First Bank"); got != bankUnsure {
		t.Errorf("FCMB vs First Bank = %v, want unsure", got)
	}
	if got := payerSameBank("FCMB", "FIRST CITY MONUMENT BANK"); got != bankSame {
		t.Errorf("FCMB alias = %v, want same", got)
	}
}

// #5: candidates are judged as of the money's arrival. A credit held while
// another order also asked the amount stays an admin's even after that order
// is gone.
func TestPayer_OtherCandidateAtArrivalSendsToReviewEvenIfGoneNow(t *testing.T) {
	r := newPayerRig(t, 150_000)
	credit := seedSenderCredit(t, r.db, "sess-22", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	r.db.Model(&models.InboundCredit{}).Where("id = ?", credit.ID).Update("held_claim_ids",
		InboundClaimTypeITStable+":"+uuid.New().String()+","+InboundClaimTypeITStable+":"+r.claim.ID.String())
	res, err := r.declare(t, "999992", "8012345678")
	if err != nil || res.Outcome != models.PayerDeclarationReview {
		t.Fatalf("Declare: %v / %v, want review", err, res)
	}
}

// #5: the matcher records who the money could have been for when it holds it.
func TestInboundMatcher_NoNameHoldRecordsItsCandidates(t *testing.T) {
	r := newPayerRig(t, 150_000)
	credit := seedSenderCredit(t, r.db, "sess-23", 150_000, "OPAY", "SOMEBODY ELSE",
		time.Now(), models.InboundCreditUnmatched)
	if _, err := r.m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	got := reloadCredit(t, r.db, credit.ID)
	if got.Status != models.InboundCreditHeld || got.HeldClaimIDs != InboundClaimTypeITStable+":"+r.claim.ID.String() {
		t.Fatalf("credit = %s held_claim_ids=%q", got.Status, got.HeldClaimIDs)
	}
}

// #7: a product refusal keeps the product's own reason on the credit.
func TestPayer_ProductRefusalKeepsTheProductsReason(t *testing.T) {
	r := newPayerRig(t, 150_000)
	r.src.acceptErr = &ClaimNeedsReviewError{Code: "trade_not_within_merchant_cap", Reason: "over the merchant's daily cap"}
	credit := seedSenderCredit(t, r.db, "sess-24", 150_000, "OPAY", "JOYCLARE KOSARACHI ELEM",
		time.Now().Add(-5*time.Minute), models.InboundCreditHeld)
	r.soleCandidate(t, credit)
	if _, err := r.declare(t, "999992", "8012345678"); err != nil {
		t.Fatalf("Declare: %v", err)
	}
	got := reloadCredit(t, r.db, credit.ID)
	if got.Status != models.InboundCreditHeld || got.HoldReasonCode != "trade_not_within_merchant_cap" {
		t.Fatalf("credit = %s/%s, want held with the product's code", got.Status, got.HoldReasonCode)
	}
}

// #10: a declaration closes when the order's payment matches the buyer's own
// name, and a stale waiting one closes on its own.
func TestPayer_DeclarationsCloseWhenNoLongerOwed(t *testing.T) {
	r := newPayerRig(t, 150_000)
	res, err := r.declare(t, "999992", "8012345678")
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	seedSenderCredit(t, r.db, "sess-25", 150_000, "OPAY", "CHIDI OKONKWO", time.Now(), models.InboundCreditUnmatched)
	if _, err := r.m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	var d models.InboundPayerDeclaration
	r.db.First(&d, "id = ?", res.Declaration.ID)
	if d.Status != models.PayerDeclarationClosed {
		t.Fatalf("after an own-name match: %s, want closed", d.Status)
	}

	r2 := newPayerRig(t, 150_000)
	res2, _ := r2.declare(t, "999992", "8012345678")
	r2.db.Model(&models.InboundPayerDeclaration{}).Where("id = ?", res2.Declaration.ID).
		Update("created_at", time.Now().Add(-72*time.Hour))
	if _, err := r2.m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	var d2 models.InboundPayerDeclaration
	r2.db.First(&d2, "id = ?", res2.Declaration.ID)
	if d2.Status != models.PayerDeclarationClosed {
		t.Fatalf("stale waiting declaration: %s, want closed", d2.Status)
	}
}

// One cap for both paths into a declared bind.
func TestPayerSelfResolveMaxIsOneConstant(t *testing.T) {
	if PayerSelfResolveMaxKobo != 500_000*100 {
		t.Fatalf("cap = %d kobo, want ₦500,000 (owner, 2026-10-16)", PayerSelfResolveMaxKobo)
	}
}
