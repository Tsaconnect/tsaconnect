package services

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ─── rig ───────────────────────────────────────────────────────────────────

// setupMatcherDB is setupInboundDB plus the two users columns the matcher
// reads. Raw DDL for the same reason the ingestor rig uses it: models.User
// carries PostgreSQL-only defaults SQLite rejects.
func setupMatcherDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupInboundDB(t)
	if err := db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		name TEXT,
		verified_legal_name TEXT,
		verified_legal_name_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("users ddl: %v", err)
	}
	return db
}

func seedMatcherUser(t *testing.T, db *gorm.DB, verifiedName string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := db.Exec(`INSERT INTO users (id, name, verified_legal_name) VALUES (?, ?, ?)`,
		id, "Display Name", verifiedName).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

// seedCredit writes one unmatched credit. grossKobo is what a claim must ask
// for; the netted amount is irrelevant to matching and is stored as-is.
func seedCredit(t *testing.T, db *gorm.DB, ref string, grossKobo int64, senderName string) *models.InboundCredit {
	t.Helper()
	c := &models.InboundCredit{
		Provider:        models.InboundCreditProviderFlutterwave,
		ProviderRef:     ref,
		Currency:        "NGN",
		AmountKobo:      grossKobo,
		GrossKobo:       grossKobo,
		SenderRaw:       "Received Money from GTBANK|" + senderName,
		SenderBank:      "GTBANK",
		SenderName:      senderName,
		TransactionDate: time.Now().UTC(),
		SeenAt:          time.Now().UTC(),
		Status:          models.InboundCreditUnmatched,
	}
	if err := db.Create(c).Error; err != nil {
		t.Fatalf("insert credit: %v", err)
	}
	return c
}

type acceptCall struct {
	claimID     uuid.UUID
	providerRef string
}

// fakeClaimSource is a ClaimSource whose Accept records what it was asked to
// settle, so tests can assert the matcher never settles twice.
type fakeClaimSource struct {
	typ       string
	claims    []InboundClaim
	openErr   error
	acceptErr error
	accepted  []acceptCall
}

func (f *fakeClaimSource) OpenClaims(_ context.Context, _ string) ([]InboundClaim, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.claims, nil
}

func (f *fakeClaimSource) Accept(_ context.Context, claimID uuid.UUID, credit *models.InboundCredit) error {
	f.accepted = append(f.accepted, acceptCall{claimID: claimID, providerRef: credit.ProviderRef})
	return f.acceptErr
}

func claimFor(userID uuid.UUID, kobo int64) InboundClaim {
	return InboundClaim{
		ID: uuid.New(), UserID: userID, AmountKobo: kobo,
		Currency: "NGN", OpenedAt: time.Now(),
	}
}

// claimInCurrency is claimFor for the cases that exist to prove the matcher
// checks the currency rather than trusting the source to have filtered on it.
func claimInCurrency(userID uuid.UUID, kobo int64, currency string) InboundClaim {
	c := claimFor(userID, kobo)
	c.Currency = currency
	return c
}

func reloadCredit(t *testing.T, db *gorm.DB, id uuid.UUID) *models.InboundCredit {
	t.Helper()
	var c models.InboundCredit
	if err := db.First(&c, "id = ?", id).Error; err != nil {
		t.Fatalf("reload credit: %v", err)
	}
	return &c
}

// countingNotifier records every hold it is told about, and can fail.
type countingNotifier struct {
	calls int
	err   error
}

func (n *countingNotifier) NotifyHold(_ context.Context, _ *models.InboundCredit) error {
	n.calls++
	return n.err
}

// ─── amount ────────────────────────────────────────────────────────────────

func TestRoundKoboToNaira(t *testing.T) {
	cases := []struct{ in, want int64 }{
		{0, 0},
		{100, 100},
		{199_800, 199_800},
		{199_849, 199_800}, // ₦1,998.49 → ₦1,998
		{199_850, 199_900}, // half rounds up
		{1, 0},             // sub-naira dust is not a naira
		{50, 100},
		{-150, -200},
	}
	for _, c := range cases {
		if got := roundKoboToNaira(c.in); got != c.want {
			t.Errorf("roundKoboToNaira(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// ─── binding rule ──────────────────────────────────────────────────────────

// Nothing is open, so there is nothing this money can be. It must hold with a
// reason that tells the operator the amount and that no claim asked for it —
// not a bare "no match".
func TestInboundMatcher_ZeroCandidatesHolds(t *testing.T) {
	db := setupMatcherDB(t)
	credit := seedCredit(t, db, "sess-0001", 200_000, "ADEMU EMMANUEL")

	src := &fakeClaimSource{typ: InboundClaimTypeITMCGP}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)

	decided, err := m.MatchPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	if decided != 1 {
		t.Fatalf("decided = %d, want 1", decided)
	}
	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditHeld {
		t.Fatalf("status = %q, want held", got.Status)
	}
	if len(src.accepted) != 0 {
		t.Fatalf("settled %d claims with nothing open", len(src.accepted))
	}
	for _, want := range []string{"NGN 2,000.00", "no open claim", "ADEMU EMMANUEL"} {
		if !strings.Contains(got.HoldReason, want) {
			t.Errorf("hold reason %q missing %q", got.HoldReason, want)
		}
	}
}

// Two people are owed the same amount and only one of them is the sender. The
// amount cannot decide it; the name can, and does.
func TestInboundMatcher_TwoAmountEqualCandidatesOneNameMatchBinds(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	other := seedMatcherUser(t, db, "Chidi Okonkwo")
	credit := seedCredit(t, db, "sess-0002", 200_000, "ADEMU EMMANUEL O.")

	mine := claimFor(payer, 200_000)
	src := &fakeClaimSource{
		typ:    InboundClaimTypeITMCGP,
		claims: []InboundClaim{claimFor(other, 200_000), mine},
	}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}

	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditMatched {
		t.Fatalf("status = %q reason=%q, want matched", got.Status, got.HoldReason)
	}
	if got.MatchedClaimID == nil || *got.MatchedClaimID != mine.ID {
		t.Errorf("matched claim = %v, want %s", got.MatchedClaimID, mine.ID)
	}
	if got.MatchedUserID == nil || *got.MatchedUserID != payer {
		t.Errorf("matched user = %v, want %s", got.MatchedUserID, payer)
	}
	if got.MatchedClaimType != InboundClaimTypeITMCGP {
		t.Errorf("claim type = %q", got.MatchedClaimType)
	}
	if got.MatchStrength != models.InboundMatchStrengthExact {
		t.Errorf("strength = %q, want exact (the trailing initial is discarded)", got.MatchStrength)
	}
	if got.MatchedAt == nil {
		t.Error("matched_at not stamped")
	}
	if got.HoldReason != "" {
		t.Errorf("bound credit carries a hold reason: %q", got.HoldReason)
	}
	if len(src.accepted) != 1 || src.accepted[0].claimID != mine.ID {
		t.Fatalf("accepted = %+v, want exactly the matched claim", src.accepted)
	}
	if src.accepted[0].providerRef != "sess-0002" {
		t.Errorf("Accept saw providerRef %q — it is the source's only dedupe key", src.accepted[0].providerRef)
	}
}

// Two claims for the same amount whose users BOTH answer to the sender name.
// Ranking one above the other would move money to a coin flip, so it holds.
func TestInboundMatcher_TwoNameMatchesHolds(t *testing.T) {
	db := setupMatcherDB(t)
	father := seedMatcherUser(t, db, "Emmanuel Ademu")
	son := seedMatcherUser(t, db, "Emmanuel Ademu Okon")
	credit := seedCredit(t, db, "sess-0003", 200_000, "EMMANUEL ADEMU")

	src := &fakeClaimSource{
		typ:    InboundClaimTypeITMCGP,
		claims: []InboundClaim{claimFor(father, 200_000), claimFor(son, 200_000)},
	}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}

	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditHeld {
		t.Fatalf("status = %q, want held — a contested credit must never bind", got.Status)
	}
	if len(src.accepted) != 0 {
		t.Fatalf("settled a contested credit: %+v", src.accepted)
	}
	if got.MatchedClaimID != nil {
		t.Errorf("held credit carries a claim id %v", got.MatchedClaimID)
	}
	for _, want := range []string{"contested", "2 open claims"} {
		if !strings.Contains(got.HoldReason, want) {
			t.Errorf("hold reason %q missing %q", got.HoldReason, want)
		}
	}
	// The operator has to be able to go straight to the two users in dispute.
	for _, u := range []uuid.UUID{father, son} {
		if !strings.Contains(got.HoldReason, shortID(u)) {
			t.Errorf("hold reason %q does not name contended user %s", got.HoldReason, u)
		}
	}
}

// A perfect name is not permission to move money that was never asked for.
func TestInboundMatcher_AmountMismatchHoldsDespitePerfectName(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0004", 200_000, "EMMANUEL ADEMU")

	src := &fakeClaimSource{
		typ:    InboundClaimTypeITMCGP,
		claims: []InboundClaim{claimFor(payer, 500_000)}, // asked for ₦5,000, sent ₦2,000
	}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}

	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditHeld {
		t.Fatalf("status = %q, want held", got.Status)
	}
	if len(src.accepted) != 0 {
		t.Fatalf("settled on a name alone: %+v", src.accepted)
	}
	if !strings.Contains(got.HoldReason, "no open claim") {
		t.Errorf("hold reason %q should say no claim asked for this amount", got.HoldReason)
	}
}

// An absent Didit name is "cannot attribute", not "does not match". The two
// need different operator actions — capture the name vs refund the sender —
// so the queue must be able to tell them apart from the reason alone.
func TestInboundMatcher_EmptyVerifiedNameHoldsDistinctly(t *testing.T) {
	db := setupMatcherDB(t)
	unverified := seedMatcherUser(t, db, "")
	credit := seedCredit(t, db, "sess-0005", 200_000, "EMMANUEL ADEMU")

	src := &fakeClaimSource{
		typ:    InboundClaimTypeITMCGP,
		claims: []InboundClaim{claimFor(unverified, 200_000)},
	}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	blank := reloadCredit(t, db, credit.ID)
	if blank.Status != models.InboundCreditHeld {
		t.Fatalf("status = %q, want held", blank.Status)
	}
	if len(src.accepted) != 0 {
		t.Fatalf("bound a claim whose owner has no verified name: %+v", src.accepted)
	}
	if !strings.Contains(blank.HoldReason, "no Didit-verified legal name") {
		t.Errorf("hold reason %q does not say the name is missing", blank.HoldReason)
	}
	if !strings.Contains(blank.HoldReason, shortID(unverified)) {
		t.Errorf("hold reason %q does not name the unverifiable user", blank.HoldReason)
	}

	// Same shape, but the name IS on file and simply does not match. The
	// reason must read differently, or the queue cannot triage.
	stranger := seedMatcherUser(t, db, "Chidi Okonkwo")
	credit2 := seedCredit(t, db, "sess-0006", 200_000, "EMMANUEL ADEMU")
	src.claims = []InboundClaim{claimFor(stranger, 200_000)}
	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	mismatch := reloadCredit(t, db, credit2.ID)
	if mismatch.HoldReason == blank.HoldReason {
		t.Fatalf("a missing name and a wrong name produced the same reason: %q", blank.HoldReason)
	}
	if strings.Contains(mismatch.HoldReason, "no Didit-verified legal name") {
		t.Errorf("a wrong name was reported as a missing one: %q", mismatch.HoldReason)
	}
	if !strings.Contains(mismatch.HoldReason, "CHIDI OKONKWO") &&
		!strings.Contains(mismatch.HoldReason, "Chidi Okonkwo") {
		t.Errorf("hold reason %q does not show the name that was compared", mismatch.HoldReason)
	}
}

// A claim whose owner has no verified name cannot be ruled OUT either, so it
// contends with the one that did match. Treating it as a clean "no" is exactly
// the bug that credits the wrong human.
func TestInboundMatcher_UnverifiableCandidateBlocksAnOtherwiseCleanBind(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	unknown := seedMatcherUser(t, db, "")
	credit := seedCredit(t, db, "sess-0007", 200_000, "EMMANUEL ADEMU")

	src := &fakeClaimSource{
		typ:    InboundClaimTypeITMCGP,
		claims: []InboundClaim{claimFor(payer, 200_000), claimFor(unknown, 200_000)},
	}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditHeld {
		t.Fatalf("status = %q, want held", got.Status)
	}
	if len(src.accepted) != 0 {
		t.Fatalf("bound past an unverifiable rival claim: %+v", src.accepted)
	}
}

// A remark we could not parse leaves nothing to compare. That is its own
// operator problem and reads as its own reason.
func TestInboundMatcher_UnparsedSenderHoldsDistinctly(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0008", 200_000, "")

	src := &fakeClaimSource{
		typ:    InboundClaimTypeITMCGP,
		claims: []InboundClaim{claimFor(payer, 200_000)},
	}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditHeld {
		t.Fatalf("status = %q, want held", got.Status)
	}
	if !strings.Contains(got.HoldReason, "no sender name") {
		t.Errorf("hold reason %q does not say the remark yielded no name", got.HoldReason)
	}
	if len(src.accepted) != 0 {
		t.Fatal("bound a credit with no sender name at all")
	}
}

// Claims from different products compete on equal terms — the matcher knows
// nothing about either and must hold rather than prefer one.
func TestInboundMatcher_ContentionAcrossProductsHolds(t *testing.T) {
	db := setupMatcherDB(t)
	a := seedMatcherUser(t, db, "Emmanuel Ademu")
	b := seedMatcherUser(t, db, "Ademu Emmanuel")
	credit := seedCredit(t, db, "sess-0009", 200_000, "EMMANUEL ADEMU")

	mcgp := &fakeClaimSource{typ: InboundClaimTypeITMCGP, claims: []InboundClaim{claimFor(a, 200_000)}}
	stable := &fakeClaimSource{typ: InboundClaimTypeITStable, claims: []InboundClaim{claimFor(b, 200_000)}}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, mcgp)
	m.Register(InboundClaimTypeITStable, stable)

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditHeld {
		t.Fatalf("status = %q, want held", got.Status)
	}
	if len(mcgp.accepted)+len(stable.accepted) != 0 {
		t.Fatal("settled a credit contested across two products")
	}
	if !strings.Contains(got.HoldReason, InboundClaimTypeITMCGP) ||
		!strings.Contains(got.HoldReason, InboundClaimTypeITStable) {
		t.Errorf("hold reason %q does not name both contending products", got.HoldReason)
	}
}

// ─── idempotency ───────────────────────────────────────────────────────────

// The poller re-runs every 30 seconds. A credit it already bound must not be
// picked up again, and the product must never be told to settle twice.
func TestInboundMatcher_ReplayedCreditDoesNotDoubleCredit(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0010", 200_000, "EMMANUEL ADEMU")

	claim := claimFor(payer, 200_000)
	src := &fakeClaimSource{typ: InboundClaimTypeITMCGP, claims: []InboundClaim{claim}}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)
	ctx := context.Background()

	if _, err := m.MatchPending(ctx, 10); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	// The claim source has not caught up — a settle path that is slow to close
	// its order still reports it open on the next tick.
	decided, err := m.MatchPending(ctx, 10)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if decided != 0 {
		t.Errorf("second pass decided %d credits, want 0 — the row is already matched", decided)
	}
	if len(src.accepted) != 1 {
		t.Fatalf("Accept called %d times for one credit", len(src.accepted))
	}
	if got := reloadCredit(t, db, credit.ID); got.Status != models.InboundCreditMatched {
		t.Errorf("status = %q", got.Status)
	}
}

// A crash between Accept and the status write leaves the row `unmatched` with
// the product already settled. The re-run must present the SAME provider ref
// so the source's own dedupe can recognise it — never a fresh key.
func TestInboundMatcher_CrashBetweenAcceptAndStatusReplaysSameRef(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0011", 200_000, "EMMANUEL ADEMU")

	claim := claimFor(payer, 200_000)
	src := &fakeClaimSource{typ: InboundClaimTypeITMCGP, claims: []InboundClaim{claim}}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)
	ctx := context.Background()

	if _, err := m.MatchPending(ctx, 10); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	// Simulate the crash: the settle happened, the status write did not.
	if err := db.Model(&models.InboundCredit{}).Where("id = ?", credit.ID).
		Updates(map[string]any{
			"status": models.InboundCreditUnmatched, "matched_claim_id": nil,
			"matched_user_id": nil, "matched_at": nil, "match_strength": "",
			"matched_claim_type": "",
		}).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := m.MatchPending(ctx, 10); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(src.accepted) != 2 {
		t.Fatalf("Accept called %d times, want 2 (the replay is expected)", len(src.accepted))
	}
	if src.accepted[0] != src.accepted[1] {
		t.Fatalf("replay presented %+v then %+v — the source cannot dedupe a changing key",
			src.accepted[0], src.accepted[1])
	}
}

// A settle path that fails must leave the credit exactly as it was: not
// matched (nothing settled) and not held (a hold is terminal until a human
// acts, and this is a retryable provider blip).
func TestInboundMatcher_AcceptFailureLeavesCreditUnmatched(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0012", 200_000, "EMMANUEL ADEMU")

	src := &fakeClaimSource{
		typ:       InboundClaimTypeITMCGP,
		claims:    []InboundClaim{claimFor(payer, 200_000)},
		acceptErr: errors.New("chain broadcast failed"),
	}
	notifier := &countingNotifier{}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)
	m.SetHoldNotifier(notifier)

	decided, err := m.MatchPending(context.Background(), 10)
	if err == nil {
		t.Fatal("a failed settle reported success")
	}
	if decided != 0 {
		t.Errorf("decided = %d, want 0", decided)
	}
	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditUnmatched {
		t.Fatalf("status = %q, want unmatched so the next tick retries", got.Status)
	}
	if notifier.calls != 0 {
		t.Errorf("alerted %d holds for a retryable failure", notifier.calls)
	}
}

// A source that cannot list its claims makes the whole candidate set unknown.
// Holding on a partial view would credit the one product that answered.
func TestInboundMatcher_OpenClaimsErrorDoesNotHold(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0013", 200_000, "EMMANUEL ADEMU")

	ok := &fakeClaimSource{typ: InboundClaimTypeITMCGP, claims: []InboundClaim{claimFor(payer, 200_000)}}
	broken := &fakeClaimSource{typ: InboundClaimTypeITStable, openErr: errors.New("db down")}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, ok)
	m.Register(InboundClaimTypeITStable, broken)

	if _, err := m.MatchPending(context.Background(), 10); err == nil {
		t.Fatal("a blind matcher reported success")
	}
	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditUnmatched {
		t.Fatalf("status = %q, want unmatched — the candidate set was never fully known", got.Status)
	}
	if len(ok.accepted) != 0 {
		t.Fatal("bound against a partial view of the open claims")
	}
}

// ─── hold alerting ─────────────────────────────────────────────────────────

// Losing an email is recoverable; losing the hold is not.
func TestInboundMatcher_NotifierErrorDoesNotRollBackHold(t *testing.T) {
	db := setupMatcherDB(t)
	credit := seedCredit(t, db, "sess-0014", 200_000, "EMMANUEL ADEMU")
	notifier := &countingNotifier{err: errors.New("smtp down")}

	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, &fakeClaimSource{typ: InboundClaimTypeITMCGP})
	m.SetHoldNotifier(notifier)

	decided, err := m.MatchPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("a failed alert failed the match: %v", err)
	}
	if decided != 1 {
		t.Errorf("decided = %d, want 1", decided)
	}
	if notifier.calls != 1 {
		t.Errorf("notifier called %d times, want 1 per hold", notifier.calls)
	}
	if got := reloadCredit(t, db, credit.ID); got.Status != models.InboundCreditHeld {
		t.Fatalf("status = %q — the hold was lost with the email", got.Status)
	}
}

func TestInboundMatcher_NilNotifierIsSafe(t *testing.T) {
	db := setupMatcherDB(t)
	credit := seedCredit(t, db, "sess-0015", 200_000, "EMMANUEL ADEMU")

	m := NewInboundMatcher(db) // no SetHoldNotifier at all
	m.Register(InboundClaimTypeITMCGP, &fakeClaimSource{typ: InboundClaimTypeITMCGP})
	m.SetHoldNotifier(nil) // and explicitly nil, which must also be inert

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	if got := reloadCredit(t, db, credit.ID); got.Status != models.InboundCreditHeld {
		t.Fatalf("status = %q", got.Status)
	}
}

// A hold reason is stored in a varchar(255); a long contention list must be
// cut rather than fail the write and lose the hold.
func TestInboundMatcher_HoldReasonFitsTheColumn(t *testing.T) {
	db := setupMatcherDB(t)
	credit := seedCredit(t, db, "sess-0016", 200_000, strings.Repeat("EMMANUEL ADEMU ", 20))

	var claims []InboundClaim
	for i := 0; i < 12; i++ {
		claims = append(claims, claimFor(seedMatcherUser(t, db, "Emmanuel Ademu"), 200_000))
	}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, &fakeClaimSource{typ: InboundClaimTypeITMCGP, claims: claims})

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	got := reloadCredit(t, db, credit.ID)
	if len(got.HoldReason) > inboundHoldReasonMax {
		t.Fatalf("hold reason is %d bytes, column is %d", len(got.HoldReason), inboundHoldReasonMax)
	}
	if got.Status != models.InboundCreditHeld {
		t.Fatalf("status = %q", got.Status)
	}
	// What gets cut matters. The claim list is the part an operator acts on,
	// so an oversized sender name must not be allowed to push it out of the
	// line — the cut has to come off the free text, not the evidence.
	if !strings.Contains(got.HoldReason, shortID(claims[0].UserID)) {
		t.Errorf("truncation dropped the contended users: %q", got.HoldReason)
	}
}

// ─── manual binding ────────────────────────────────────────────────────────

// The admin queue must run the same settle path as the automatic bind, so a
// hand-assigned credit and an auto-matched one cannot diverge.
func TestInboundMatcher_BindManuallyUsesAcceptAndRecordsTheAdmin(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0017", 200_000, "SOMEBODY ELSE ENTIRELY")

	claim := claimFor(payer, 200_000)
	src := &fakeClaimSource{typ: InboundClaimTypeITMCGP, claims: []InboundClaim{claim}}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)
	ctx := context.Background()

	if _, err := m.MatchPending(ctx, 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	if got := reloadCredit(t, db, credit.ID); got.Status != models.InboundCreditHeld {
		t.Fatalf("precondition: status = %q, want held", got.Status)
	}

	admin := uuid.New()
	if err := m.BindManually(ctx, credit.ID, InboundClaimTypeITMCGP, claim.ID, admin); err != nil {
		t.Fatalf("BindManually: %v", err)
	}
	if len(src.accepted) != 1 || src.accepted[0].claimID != claim.ID {
		t.Fatalf("manual bind did not run Accept: %+v", src.accepted)
	}
	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditMatched {
		t.Fatalf("status = %q, want matched", got.Status)
	}
	if got.ResolvedBy == nil || *got.ResolvedBy != admin {
		t.Errorf("resolved_by = %v, want %s", got.ResolvedBy, admin)
	}
	if got.MatchedUserID == nil || *got.MatchedUserID != payer {
		t.Errorf("matched user = %v, want the claim's owner %s", got.MatchedUserID, payer)
	}
	if got.MatchStrength != "" {
		t.Errorf("strength = %q — a manual bind is justified by the admin, not by the name", got.MatchStrength)
	}
	if got.HoldReason != "" {
		t.Errorf("resolved credit still reads as held: %q", got.HoldReason)
	}
}

// Assigning to a claim nobody has open would settle money into an order that
// is already done. Refuse, loudly.
func TestInboundMatcher_BindManuallyRefusesAClosedClaim(t *testing.T) {
	db := setupMatcherDB(t)
	credit := seedCredit(t, db, "sess-0018", 200_000, "EMMANUEL ADEMU")
	src := &fakeClaimSource{typ: InboundClaimTypeITMCGP}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)

	err := m.BindManually(context.Background(), credit.ID, InboundClaimTypeITMCGP, uuid.New(), uuid.New())
	if err == nil {
		t.Fatal("bound to a claim that is not open")
	}
	if len(src.accepted) != 0 {
		t.Fatal("settled against a closed claim")
	}
	if got := reloadCredit(t, db, credit.ID); got.Status != models.InboundCreditUnmatched {
		t.Errorf("status = %q, want the row untouched", got.Status)
	}
}

func TestInboundMatcher_BindManuallyRefusesAnUnknownClaimType(t *testing.T) {
	db := setupMatcherDB(t)
	credit := seedCredit(t, db, "sess-0019", 200_000, "EMMANUEL ADEMU")
	m := NewInboundMatcher(db)

	if err := m.BindManually(context.Background(), credit.ID, "not_a_product", uuid.New(), uuid.New()); err == nil {
		t.Fatal("accepted an unregistered claim type")
	}
}

// Re-assigning money that is already settled elsewhere would credit it twice.
func TestInboundMatcher_BindManuallyRefusesAnAlreadyMatchedCredit(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0020", 200_000, "EMMANUEL ADEMU")

	claim := claimFor(payer, 200_000)
	other := claimFor(seedMatcherUser(t, db, "Chidi Okonkwo"), 200_000)
	src := &fakeClaimSource{typ: InboundClaimTypeITMCGP, claims: []InboundClaim{claim, other}}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)
	ctx := context.Background()

	if _, err := m.MatchPending(ctx, 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	if got := reloadCredit(t, db, credit.ID); got.Status != models.InboundCreditMatched {
		t.Fatalf("precondition: status = %q", got.Status)
	}
	before := len(src.accepted)

	if err := m.BindManually(ctx, credit.ID, InboundClaimTypeITMCGP, other.ID, uuid.New()); err == nil {
		t.Fatal("re-assigned a credit that is already settled")
	}
	if len(src.accepted) != before {
		t.Fatal("ran a second settle for money already credited")
	}
	// Re-confirming the SAME claim is a no-op, not an error: the admin queue
	// double-submitting must not read as a failure.
	if err := m.BindManually(ctx, credit.ID, InboundClaimTypeITMCGP, claim.ID, uuid.New()); err != nil {
		t.Fatalf("re-confirming the existing bind errored: %v", err)
	}
	if len(src.accepted) != before {
		t.Fatal("re-confirming the existing bind settled again")
	}
}

// ─── batching ──────────────────────────────────────────────────────────────

// One tick must not outrun the 30s interval, so the batch bound is real.
func TestInboundMatcher_MatchPendingRespectsTheBatchLimit(t *testing.T) {
	db := setupMatcherDB(t)
	for i := 0; i < 5; i++ {
		seedCredit(t, db, "sess-batch-"+string(rune('a'+i)), 200_000, "EMMANUEL ADEMU")
	}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, &fakeClaimSource{typ: InboundClaimTypeITMCGP})

	decided, err := m.MatchPending(context.Background(), 2)
	if err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	if decided != 2 {
		t.Fatalf("decided = %d, want 2", decided)
	}
	var held int64
	db.Model(&models.InboundCredit{}).Where("status = ?", models.InboundCreditHeld).Count(&held)
	if held != 2 {
		t.Errorf("held %d rows, want 2", held)
	}
}

// ─── manual binding: the amount is not the operator's to override ──────────

// The queue exists so a human can decide WHOSE the money is. It does not exist
// so a human can decide HOW MUCH arrived. The P2P source credits the merchant
// trade.FiatAmount rather than the credit's own amount, so assigning a ₦5,000
// credit to a ₦500,000 trade pays the merchant ₦500,000 and releases the whole
// escrow to the buyer — out of nothing.
func TestInboundMatcher_BindManuallyRefusesAnAmountMismatch(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0021", 500_000, "EMMANUEL ADEMU") // ₦5,000 arrived

	claim := claimFor(payer, 50_000_000) // an open ₦500,000 trade
	src := &fakeClaimSource{typ: InboundClaimTypeP2PTrade, claims: []InboundClaim{claim}}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeP2PTrade, src)
	ctx := context.Background()

	if _, err := m.MatchPending(ctx, 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	if got := reloadCredit(t, db, credit.ID); got.Status != models.InboundCreditHeld {
		t.Fatalf("precondition: status = %q, want held", got.Status)
	}

	err := m.BindManually(ctx, credit.ID, InboundClaimTypeP2PTrade, claim.ID, uuid.New())
	if err == nil {
		t.Fatal("bound a ₦5,000 credit to a ₦500,000 claim")
	}

	// The money outcome, which is the whole point of the refusal: the settle
	// path never ran, so no merchant was paid and no escrow was released.
	if len(src.accepted) != 0 {
		t.Fatalf("settled a ₦500,000 claim out of a ₦5,000 credit: %+v", src.accepted)
	}
	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditHeld {
		t.Errorf("status = %q, want the credit left held for a real resolution", got.Status)
	}
	if got.MatchedClaimID != nil || got.MatchedUserID != nil {
		t.Errorf("refused bind still stamped claim=%v user=%v", got.MatchedClaimID, got.MatchedUserID)
	}
	if got.ResolvedBy != nil {
		t.Errorf("refused bind recorded an admin: %v", got.ResolvedBy)
	}

	// Both figures, or the operator cannot tell which one is wrong — the payer
	// underpaid, or they picked the wrong row.
	for _, want := range []string{"5,000.00", "500,000.00"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not state %q", err, want)
		}
	}
}

// The automatic path snaps both sides to whole naira before comparing, and the
// manual path must snap the same way — otherwise an operator would be refused
// a bind the matcher itself would have made unattended.
func TestInboundMatcher_BindManuallyAllowsASubNairaRoundingDifference(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0022", 199_849, "SOMEBODY ELSE ENTIRELY") // ₦1,998.49

	claim := claimFor(payer, 199_800) // ₦1,998
	src := &fakeClaimSource{typ: InboundClaimTypeITMCGP, claims: []InboundClaim{claim}}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)

	if err := m.BindManually(context.Background(), credit.ID, InboundClaimTypeITMCGP, claim.ID, uuid.New()); err != nil {
		t.Fatalf("refused a bind the matcher would have made itself: %v", err)
	}
	if len(src.accepted) != 1 {
		t.Fatalf("Accept called %d times, want 1", len(src.accepted))
	}
}

// ─── non-positive amounts ──────────────────────────────────────────────────

// A malformed statement row that parses to zero yields a target of zero, and a
// zero-amount claim is then an EXACT match — money moving on nothing at all.
func TestInboundMatcher_NonPositiveGrossHoldsAndNeverMatches(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gross int64
	}{
		{"zero", 0},
		{"negative", -50_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupMatcherDB(t)
			payer := seedMatcherUser(t, db, "Emmanuel Ademu")
			credit := seedCredit(t, db, "sess-np-"+tc.name, tc.gross, "EMMANUEL ADEMU")

			// A claim that would be an exact amount match on that target.
			src := &fakeClaimSource{
				typ:    InboundClaimTypeITMCGP,
				claims: []InboundClaim{claimFor(payer, roundKoboToNaira(tc.gross))},
			}
			m := NewInboundMatcher(db)
			m.Register(InboundClaimTypeITMCGP, src)

			decided, err := m.MatchPending(context.Background(), 10)
			if err != nil {
				t.Fatalf("MatchPending: %v", err)
			}
			if decided != 1 {
				t.Fatalf("decided = %d, want 1 — a hold is a decision", decided)
			}
			if len(src.accepted) != 0 {
				t.Fatalf("settled a claim for a %d-kobo credit: %+v", tc.gross, src.accepted)
			}
			got := reloadCredit(t, db, credit.ID)
			if got.Status != models.InboundCreditHeld {
				t.Fatalf("status = %q, want held", got.Status)
			}
			if got.MatchedClaimID != nil {
				t.Errorf("held credit carries a claim id %v", got.MatchedClaimID)
			}
		})
	}
}

// ─── claim currency ────────────────────────────────────────────────────────

// The matcher asks for claims in one currency and has, until now, had to take
// the source's word that it filtered on it. The three sources disagree about
// what an empty currency means, so the word is not good enough: a ₦200,000
// credit must never settle a claim priced at 200,000 of something else.
func TestInboundMatcher_ClaimInAnotherCurrencyHoldsRatherThanBinding(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0023", 200_000, "EMMANUEL ADEMU") // NGN

	src := &fakeClaimSource{
		typ:    InboundClaimTypeITStable,
		claims: []InboundClaim{claimInCurrency(payer, 200_000, "USD")},
	}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITStable, src)

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	if len(src.accepted) != 0 {
		t.Fatalf("settled an NGN credit against a USD claim: %+v", src.accepted)
	}
	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditHeld {
		t.Fatalf("status = %q, want held", got.Status)
	}
	if got.HoldReasonCode != models.InboundHoldCurrencyMismatch {
		t.Errorf("code = %q, want %q — a source bug reads as its own thing",
			got.HoldReasonCode, models.InboundHoldCurrencyMismatch)
	}
	// When this fires it is because one source forgot to populate the field.
	// Whoever reads the queue should not have to guess which product to blame.
	if !strings.Contains(got.HoldReason, InboundClaimTypeITStable) {
		t.Errorf("hold reason %q does not name the source at fault", got.HoldReason)
	}
}

// An unset Currency is exactly the shape a source bug takes: the field was
// simply never populated. Letting "" pass would make the assertion inert
// against the only failure it was written to catch.
func TestInboundMatcher_ClaimWithNoCurrencyDoesNotPassTheAssertion(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0024", 200_000, "EMMANUEL ADEMU")

	src := &fakeClaimSource{
		typ:    InboundClaimTypeITMCGP,
		claims: []InboundClaim{claimInCurrency(payer, 200_000, "")},
	}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	if len(src.accepted) != 0 {
		t.Fatalf("bound a claim that never said what currency it was in: %+v", src.accepted)
	}
	got := reloadCredit(t, db, credit.ID)
	if got.Status != models.InboundCreditHeld {
		t.Fatalf("status = %q, want held", got.Status)
	}
	if got.HoldReasonCode != models.InboundHoldCurrencyMismatch {
		t.Errorf("code = %q, want %q", got.HoldReasonCode, models.InboundHoldCurrencyMismatch)
	}
}

// Case and padding are a source's formatting, not a disagreement about money.
func TestInboundMatcher_ClaimCurrencyComparisonIsNormalised(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0025", 200_000, "EMMANUEL ADEMU")

	src := &fakeClaimSource{
		typ:    InboundClaimTypeITMCGP,
		claims: []InboundClaim{claimInCurrency(payer, 200_000, " ngn ")},
	}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	if got := reloadCredit(t, db, credit.ID); got.Status != models.InboundCreditMatched {
		t.Fatalf("status = %q reason=%q — \" ngn \" is NGN", got.Status, got.HoldReason)
	}
}

// ─── hold reason codes, and keeping PII out of everything but the console ──

// Every hold path must set a code, because the alert email renders the code
// and nothing else. A hold that reaches the queue with an empty code is an
// alert that says nothing at all.
func TestInboundMatcher_EveryHoldPathSetsACodeAndKeepsTheProse(t *testing.T) {
	payer := "Emmanuel Ademu"
	cases := []struct {
		name   string
		gross  int64
		sender string
		claims func(t *testing.T, db *gorm.DB) []InboundClaim
		want   string
	}{
		{
			name: "no claim at that amount", gross: 200_000, sender: "EMMANUEL ADEMU",
			claims: func(*testing.T, *gorm.DB) []InboundClaim { return nil },
			want:   models.InboundHoldNoClaimAtAmount,
		},
		{
			name: "no sender name on the statement", gross: 200_000, sender: "",
			claims: func(t *testing.T, db *gorm.DB) []InboundClaim {
				return []InboundClaim{claimFor(seedMatcherUser(t, db, payer), 200_000)}
			},
			want: models.InboundHoldNoSenderName,
		},
		{
			name: "a rival nobody can check", gross: 200_000, sender: "EMMANUEL ADEMU",
			claims: func(t *testing.T, db *gorm.DB) []InboundClaim {
				return []InboundClaim{
					claimFor(seedMatcherUser(t, db, payer), 200_000),
					claimFor(seedMatcherUser(t, db, ""), 200_000),
				}
			},
			want: models.InboundHoldUnverifiableRival,
		},
		{
			name: "checked, and none of them", gross: 200_000, sender: "EMMANUEL ADEMU",
			claims: func(t *testing.T, db *gorm.DB) []InboundClaim {
				return []InboundClaim{claimFor(seedMatcherUser(t, db, "Chidi Okonkwo"), 200_000)}
			},
			want: models.InboundHoldNoNameMatch,
		},
		{
			name: "two people answer to it", gross: 200_000, sender: "EMMANUEL ADEMU",
			claims: func(t *testing.T, db *gorm.DB) []InboundClaim {
				return []InboundClaim{
					claimFor(seedMatcherUser(t, db, payer), 200_000),
					claimFor(seedMatcherUser(t, db, "Emmanuel Ademu Okon"), 200_000),
				}
			},
			want: models.InboundHoldContested,
		},
		{
			name: "nothing actually arrived", gross: 0, sender: "EMMANUEL ADEMU",
			claims: func(t *testing.T, db *gorm.DB) []InboundClaim {
				return []InboundClaim{claimFor(seedMatcherUser(t, db, payer), 0)}
			},
			want: models.InboundHoldNonPositiveAmount,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupMatcherDB(t)
			credit := seedCredit(t, db, "sess-code-"+tc.want, tc.gross, tc.sender)
			m := NewInboundMatcher(db)
			m.Register(InboundClaimTypeITMCGP,
				&fakeClaimSource{typ: InboundClaimTypeITMCGP, claims: tc.claims(t, db)})

			if _, err := m.MatchPending(context.Background(), 10); err != nil {
				t.Fatalf("MatchPending: %v", err)
			}
			got := reloadCredit(t, db, credit.ID)
			if got.Status != models.InboundCreditHeld {
				t.Fatalf("status = %q, want held", got.Status)
			}
			if got.HoldReasonCode != tc.want {
				t.Errorf("code = %q, want %q", got.HoldReasonCode, tc.want)
			}
			// The prose still has to exist — it is what the admin console
			// shows, and the code alone cannot resolve anything.
			if strings.TrimSpace(got.HoldReason) == "" {
				t.Error("the code replaced the prose instead of joining it")
			}
		})
	}
}

// A bound credit must not carry a stale code from the hold it came out of, or
// a resolved row reads as still held.
func TestInboundMatcher_BindingClearsTheHoldReasonCode(t *testing.T) {
	db := setupMatcherDB(t)
	payer := seedMatcherUser(t, db, "Emmanuel Ademu")
	credit := seedCredit(t, db, "sess-0026", 200_000, "SOMEBODY ELSE ENTIRELY")

	claim := claimFor(payer, 200_000)
	src := &fakeClaimSource{typ: InboundClaimTypeITMCGP, claims: []InboundClaim{claim}}
	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP, src)
	ctx := context.Background()

	if _, err := m.MatchPending(ctx, 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	if got := reloadCredit(t, db, credit.ID); got.HoldReasonCode == "" {
		t.Fatal("precondition: the hold set no code")
	}
	if err := m.BindManually(ctx, credit.ID, InboundClaimTypeITMCGP, claim.ID, uuid.New()); err != nil {
		t.Fatalf("BindManually: %v", err)
	}
	got := reloadCredit(t, db, credit.ID)
	if got.HoldReasonCode != "" {
		t.Errorf("hold reason code = %q on a matched credit", got.HoldReasonCode)
	}
}

// The code is rendered into an alert email to a mailbox we do not control, so
// it must be safe on its own: no account name, no raw remark, no Didit name.
func TestInboundMatcher_HoldReasonCodeCarriesNoPII(t *testing.T) {
	db := setupMatcherDB(t)
	rival := seedMatcherUser(t, db, "Chidi Okonkwo")
	credit := seedCredit(t, db, "sess-0027", 200_000, "EMMANUEL ADEMU")

	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP,
		&fakeClaimSource{typ: InboundClaimTypeITMCGP, claims: []InboundClaim{claimFor(rival, 200_000)}})

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	got := reloadCredit(t, db, credit.ID)
	for _, pii := range []string{"EMMANUEL", "Emmanuel", "ADEMU", "Chidi", "Okonkwo", "GTBANK", "Received Money"} {
		if strings.Contains(got.HoldReasonCode, pii) {
			t.Errorf("hold reason code %q leaks %q", got.HoldReasonCode, pii)
		}
	}
	// And the prose that the code exists to replace really does carry it, so
	// this test cannot pass by the two being the same empty thing.
	if !strings.Contains(got.HoldReason, "EMMANUEL ADEMU") {
		t.Fatalf("prose reason %q lost the detail the console needs", got.HoldReason)
	}
}

// The application log is the other uncontrolled sink. It gets the code and the
// provider ref — enough to find the row — and never the prose.
func TestInboundMatcher_HoldLogLineCarriesNoPII(t *testing.T) {
	db := setupMatcherDB(t)
	rival := seedMatcherUser(t, db, "Chidi Okonkwo")
	credit := seedCredit(t, db, "sess-0028", 200_000, "EMMANUEL ADEMU")

	var logged bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

	m := NewInboundMatcher(db)
	m.Register(InboundClaimTypeITMCGP,
		&fakeClaimSource{typ: InboundClaimTypeITMCGP, claims: []InboundClaim{claimFor(rival, 200_000)}})

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}

	out := logged.String()
	for _, pii := range []string{"EMMANUEL", "Emmanuel", "ADEMU", "Chidi", "Okonkwo", "GTBANK", "Received Money"} {
		if strings.Contains(out, pii) {
			t.Errorf("hold log line leaks %q:\n%s", pii, out)
		}
	}
	// It still has to be actionable: the ref finds the row, the code says why.
	if !strings.Contains(out, credit.ProviderRef) {
		t.Errorf("hold log line does not carry the provider ref:\n%s", out)
	}
	if !strings.Contains(out, models.InboundHoldNoNameMatch) {
		t.Errorf("hold log line does not carry the reason code:\n%s", out)
	}
}
