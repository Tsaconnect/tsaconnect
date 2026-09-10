package services

import (
	"context"
	"errors"
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
	return InboundClaim{ID: uuid.New(), UserID: userID, AmountKobo: kobo, OpenedAt: time.Now()}
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
