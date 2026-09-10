package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// Inbound attribution — deciding which human a credit into the central account
// belongs to (central-account design §5.3).
//
// Everything upstream of here is factual: the ingestor records that money
// arrived and stops. This file is where a judgement is made, and the judgement
// moves real money into a real person's balance. It is therefore written to
// fail closed at every fork: when the evidence is thin, ambiguous, or simply
// absent, the credit is HELD for an operator rather than guessed at.
//
// The three rules that carry all the weight:
//
//  1. EXACTLY ONE candidate, or nothing. There is no ranking and no
//     best-match-wins. Ranking creates a path where a weak match beats no
//     match, and the first time that fires it pays one customer with another
//     customer's money — irreversibly, because the credit is on-chain.
//     Contention always holds.
//
//  2. A missing verified name is "cannot attribute", never "does not match".
//     Didit legitimately withholds the name when a workflow restricts
//     attributes or a session no longer resolves. Reading that absence as a
//     clean "no" would let a rival claim bind unopposed, which is exactly the
//     bug that credits the wrong human.
//
//  3. Settle first, then write the status. A crash between the two replays the
//     settle, which the ClaimSource contract requires to be idempotent on
//     ProviderRef. The other order would mark a credit `matched` that no
//     product ever received — money vanishing with no trace to re-drive.
//
// The matcher knows nothing about MCGP, stablecoins or P2P. It asks every
// registered ClaimSource what is open and tells exactly one of them "this is
// yours".

// inboundHoldReasonMax matches models.InboundCredit.HoldReason's varchar(255).
// A reason is cut to fit rather than allowed to fail the UPDATE: an
// over-length explanation that loses the hold itself is a far worse trade than
// a truncated one.
const inboundHoldReasonMax = 255

// inboundHoldReasonMaxClaims bounds how many contending claims a reason spells
// out before summarising. Past a handful the operator is reading the queue,
// not the reason line.
const inboundHoldReasonMaxClaims = 4

// inboundDefaultMatchBatch bounds one MatchPending call when the caller gives
// no limit.
const inboundDefaultMatchBatch = 100

// HoldNotifier is told when a credit could not be attributed.
type HoldNotifier interface {
	NotifyHold(ctx context.Context, credit *models.InboundCredit) error
}

// registeredClaimSource pairs a source with the claim type it answers for.
// Kept as a slice rather than a map so candidate evaluation and hold reasons
// are in a stable order — an operator comparing two holds should not have to
// wonder whether the ordering means anything.
type registeredClaimSource struct {
	claimType string
	src       ClaimSource
}

// InboundMatcher binds unmatched inbound credits to open product claims.
type InboundMatcher struct {
	db       *gorm.DB
	sources  []registeredClaimSource
	notifier HoldNotifier
	// now is injectable so tests can pin the clock.
	now func() time.Time
}

// NewInboundMatcher builds a matcher with no claim sources and no notifier.
// Both are wired afterwards, because a matcher with nothing registered is a
// legitimate state: it holds everything, which is the correct behaviour for a
// deployment where no product has been migrated yet.
func NewInboundMatcher(db *gorm.DB) *InboundMatcher {
	return &InboundMatcher{db: db, now: time.Now}
}

// Register adds a claim source for claimType. Registering the same type twice
// replaces the earlier source.
func (m *InboundMatcher) Register(claimType string, src ClaimSource) {
	claimType = strings.TrimSpace(claimType)
	if m == nil || src == nil || claimType == "" {
		return
	}
	for i := range m.sources {
		if m.sources[i].claimType == claimType {
			m.sources[i].src = src
			return
		}
	}
	m.sources = append(m.sources, registeredClaimSource{claimType: claimType, src: src})
}

// SetHoldNotifier wires hold alerting. Nil is valid and inert — see holdCredit
// for why an alert can never be allowed to decide whether a hold sticks.
func (m *InboundMatcher) SetHoldNotifier(n HoldNotifier) { m.notifier = n }

func (m *InboundMatcher) sourceFor(claimType string) (ClaimSource, bool) {
	for _, s := range m.sources {
		if s.claimType == claimType {
			return s.src, true
		}
	}
	return nil, false
}

// MatchPending decides on up to limit unmatched credits and reports how many
// reached a decision — bound or held.
//
// A credit that could not be decided (a source that would not answer, a settle
// path that failed) is counted in neither and left `unmatched`, so the next
// tick retries it. That is the difference between "we do not know yet" and
// "we know we cannot": only the second is a hold, because a hold is terminal
// until a human acts.
func (m *InboundMatcher) MatchPending(ctx context.Context, limit int) (int, error) {
	if m == nil || m.db == nil {
		return 0, errors.New("inbound matcher: not configured")
	}
	if limit <= 0 {
		limit = inboundDefaultMatchBatch
	}

	var credits []models.InboundCredit
	if err := m.db.WithContext(ctx).
		Where("status = ?", models.InboundCreditUnmatched).
		Order("transaction_date ASC").
		Limit(limit).
		Find(&credits).Error; err != nil {
		return 0, fmt.Errorf("inbound match: load pending: %w", err)
	}

	var decided int
	var errs []error
	for i := range credits {
		if ctx.Err() != nil {
			return decided, ctx.Err()
		}
		ok, err := m.decide(ctx, &credits[i])
		if err != nil {
			errs = append(errs, err)
			// Logged per credit as well as returned: the poller joins these
			// into one line, and an operator chasing one payment needs the
			// provider ref, not an aggregate.
			log.Printf("[inbound-match] WARNING credit ref=%s left unmatched: %v",
				credits[i].ProviderRef, err)
			continue
		}
		if ok {
			decided++
		}
	}
	return decided, errors.Join(errs...)
}

// inboundCandidate is one amount-equal claim and what the name check made of
// it. verifiable is false when the claim's owner has no Didit-verified legal
// name — which is NOT the same as a name that failed to match, and is kept
// apart all the way through to the hold reason.
type inboundCandidate struct {
	claim      InboundClaim
	kycName    string
	strength   NameMatchStrength
	verifiable bool
}

// decide runs the binding rule for one credit. It returns true when the credit
// reached a decision (bound or held) and an error when it could not — in which
// case the row is left untouched for the next tick.
func (m *InboundMatcher) decide(ctx context.Context, credit *models.InboundCredit) (bool, error) {
	claims, err := m.gatherClaims(ctx, credit.Currency)
	if err != nil {
		// A partial view of the open claims is worse than no view: the
		// products that DID answer would look uncontested and one of them
		// would bind money that belongs to a product that was merely down.
		return false, err
	}

	want := roundKoboToNaira(credit.GrossKobo)
	var candidates []InboundClaim
	for _, c := range claims {
		if c.AmountKobo == want {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return m.holdCredit(ctx, credit, fmt.Sprintf(
			"no open claim for %s — none of the %d claim(s) open across %d product(s) asks this amount; sender %s via %s. Check the amount, or refund",
			inboundMoney(credit.Currency, want), len(claims), len(m.sources),
			inboundSenderLabel(credit.SenderName), inboundOrDash(credit.SenderBank)))
	}

	// No sender name means nothing to compare against, which is its own
	// operator problem (the provider changed the remark format, or the payer's
	// bank sent none) and reads as its own reason.
	if strings.TrimSpace(credit.SenderName) == "" {
		return m.holdCredit(ctx, credit, fmt.Sprintf(
			"%s: %d claim(s) match the amount but the statement carried no sender name to check them against; raw remark %q",
			inboundMoney(credit.Currency, want), len(candidates),
			truncate(credit.SenderRaw, 80)))
	}

	names, err := m.verifiedNames(ctx, candidates)
	if err != nil {
		return false, err
	}

	scored := make([]inboundCandidate, 0, len(candidates))
	var matched []inboundCandidate
	var unverifiable []inboundCandidate
	for _, c := range candidates {
		cand := inboundCandidate{claim: c, kycName: names[c.UserID], strength: NameMatchNone}
		cand.verifiable = strings.TrimSpace(cand.kycName) != ""
		if cand.verifiable {
			cand.strength = MatchAccountName(cand.kycName, credit.SenderName)
			if cand.strength.IsMatch() {
				matched = append(matched, cand)
			}
		} else {
			unverifiable = append(unverifiable, cand)
		}
		scored = append(scored, cand)
	}

	// Rule 2. An unverifiable rival cannot be ruled out, so it contends. This
	// is checked BEFORE the match count on purpose: a single clean match
	// standing beside a claim whose owner has no name on file is not a clean
	// match, it is an unresolved question about two people.
	if len(unverifiable) > 0 {
		return m.holdCredit(ctx, credit, inboundUnverifiableReason(credit, want, scored, unverifiable, matched))
	}

	switch len(matched) {
	case 1:
		return m.bindCredit(ctx, credit, matched[0], nil)
	case 0:
		return m.holdCredit(ctx, credit, inboundNoNameMatchReason(credit, want, scored))
	default:
		return m.holdCredit(ctx, credit, inboundContestedReason(credit, want, matched))
	}
}

// gatherClaims asks every registered source what is open, stamping each claim
// with the type it was registered under. Stamping rather than trusting the
// source's own Type field means a source that forgets to set it cannot produce
// a claim the matcher is unable to route back to it.
func (m *InboundMatcher) gatherClaims(ctx context.Context, currency string) ([]InboundClaim, error) {
	var all []InboundClaim
	for _, s := range m.sources {
		claims, err := s.src.OpenClaims(ctx, currency)
		if err != nil {
			return nil, fmt.Errorf("inbound match: open claims for %s: %w", s.claimType, err)
		}
		for _, c := range claims {
			c.Type = s.claimType
			all = append(all, c)
		}
	}
	return all, nil
}

// verifiedNames loads the Didit legal name for every candidate's owner. A user
// row that is missing entirely maps to "", i.e. unverifiable — the same
// treatment a blank name gets, because both mean "we cannot say whose money
// this is".
func (m *InboundMatcher) verifiedNames(ctx context.Context, claims []InboundClaim) (map[uuid.UUID]string, error) {
	ids := make([]uuid.UUID, 0, len(claims))
	seen := make(map[uuid.UUID]struct{}, len(claims))
	for _, c := range claims {
		if _, ok := seen[c.UserID]; ok {
			continue
		}
		seen[c.UserID] = struct{}{}
		ids = append(ids, c.UserID)
	}

	var rows []struct {
		ID                uuid.UUID
		VerifiedLegalName string
	}
	if err := m.db.WithContext(ctx).
		Table("users").
		Select("id", "verified_legal_name").
		Where("id IN ?", ids).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("inbound match: load verified names: %w", err)
	}
	out := make(map[uuid.UUID]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.VerifiedLegalName
	}
	return out, nil
}

// bindCredit settles the claim and then records the bind.
//
// Accept runs FIRST and the status write is a single conditional UPDATE
// guarded on the row still being `unmatched`. That ordering makes the failure
// modes survivable in the only direction that matters: a crash after Accept
// replays a settle the source is contractually required to no-op, whereas a
// crash after the status write would leave a credit marked `matched` that no
// product ever received.
//
// resolvedBy is nil for an automatic bind and the admin's id for a manual one.
func (m *InboundMatcher) bindCredit(ctx context.Context, credit *models.InboundCredit, cand inboundCandidate, resolvedBy *uuid.UUID) (bool, error) {
	src, ok := m.sourceFor(cand.claim.Type)
	if !ok {
		return false, fmt.Errorf("inbound match: no source registered for claim type %q", cand.claim.Type)
	}
	if err := src.Accept(ctx, cand.claim.ID, credit); err != nil {
		// Deliberately NOT a hold. A settle path that failed is a retryable
		// condition, and a hold is terminal until a human acts — turning a
		// transient chain or provider error into an operator ticket would bury
		// the queue in work that the next tick would have done for free.
		return false, fmt.Errorf("inbound match: accept %s claim %s for ref %s: %w",
			cand.claim.Type, cand.claim.ID, credit.ProviderRef, err)
	}

	now := m.now().UTC()
	updates := map[string]any{
		"status":             models.InboundCreditMatched,
		"matched_claim_type": cand.claim.Type,
		"matched_claim_id":   cand.claim.ID,
		"matched_user_id":    cand.claim.UserID,
		"matched_at":         now,
		"match_strength":     string(cand.strength),
		"hold_reason":        "",
	}
	if resolvedBy != nil {
		updates["resolved_by"] = *resolvedBy
		// A manual bind is justified by the admin who made it, not by the
		// name — the usual reason for one is precisely that the name did NOT
		// match. Recording a strength here would dress a human decision up as
		// an automatic one in the audit trail.
		updates["match_strength"] = ""
	}

	res := m.db.WithContext(ctx).Model(&models.InboundCredit{}).
		Where("id = ? AND status IN ?", credit.ID,
			[]string{models.InboundCreditUnmatched, models.InboundCreditHeld}).
		Updates(updates)
	if res.Error != nil {
		// The money IS credited; only our record of it failed. Loud and
		// retryable — the next tick re-runs Accept (a no-op) and rewrites the
		// status.
		return false, fmt.Errorf("inbound match: settled ref %s but could not record the bind: %w",
			credit.ProviderRef, res.Error)
	}
	if res.RowsAffected == 0 {
		// Someone else decided this row between our read and our write. The
		// settle was idempotent, so nothing was double-credited; the winner's
		// decision stands.
		log.Printf("[inbound-match] ref=%s was decided concurrently — leaving the existing decision alone",
			credit.ProviderRef)
		return false, nil
	}
	log.Printf("[inbound-match] bound ref=%s %s claim=%s user=%s strength=%s manual=%t",
		credit.ProviderRef, cand.claim.Type, cand.claim.ID, cand.claim.UserID,
		cand.strength, resolvedBy != nil)
	return true, nil
}

// holdCredit records that the credit could not be attributed and alerts.
//
// The hold is written first and the notifier is called after, never inside a
// transaction with it: an email that fails must not be able to unwrite a hold.
// Losing the alert costs an operator a few minutes; losing the hold leaves
// money silently unattributed with the row back in `unmatched`, where the next
// tick would re-derive the same non-answer and alert again forever.
func (m *InboundMatcher) holdCredit(ctx context.Context, credit *models.InboundCredit, reason string) (bool, error) {
	reason = truncate(strings.TrimSpace(reason), inboundHoldReasonMax)
	res := m.db.WithContext(ctx).Model(&models.InboundCredit{}).
		Where("id = ? AND status = ?", credit.ID, models.InboundCreditUnmatched).
		Updates(map[string]any{
			"status":      models.InboundCreditHeld,
			"hold_reason": reason,
		})
	if res.Error != nil {
		return false, fmt.Errorf("inbound match: hold ref %s: %w", credit.ProviderRef, res.Error)
	}
	if res.RowsAffected == 0 {
		return false, nil // decided concurrently
	}
	credit.Status = models.InboundCreditHeld
	credit.HoldReason = reason
	log.Printf("[inbound-match] HELD ref=%s amountKobo=%d: %s", credit.ProviderRef, credit.GrossKobo, reason)

	m.notifyHold(ctx, credit)
	return true, nil
}

// notifyHold alerts on a hold. A nil notifier is a no-op and an alert error is
// logged and swallowed — by the trade described on holdCredit.
func (m *InboundMatcher) notifyHold(ctx context.Context, credit *models.InboundCredit) {
	if m.notifier == nil {
		return
	}
	if err := m.notifier.NotifyHold(ctx, credit); err != nil {
		log.Printf("[inbound-match] WARNING hold alert not delivered for ref=%s: %v", credit.ProviderRef, err)
	}
}

// BindManually assigns a held (or still-unmatched) credit to a claim chosen by
// an operator in the admin queue.
//
// It runs the SAME Accept path as an automatic bind and writes the same
// columns, so a hand-assigned credit and an auto-matched one can never diverge
// in how the product settled or how the row reads afterwards. The only
// differences are ResolvedBy, which records who decided, and MatchStrength,
// which stays empty because a name did not justify this.
//
// The amount and the name are NOT re-checked: overriding them is the entire
// reason an operator is here (the payer sent ₦1,999, or paid from a spouse's
// account). What is checked is that the claim is still open — settling into an
// order that has already been fulfilled would credit the same money twice.
func (m *InboundMatcher) BindManually(ctx context.Context, creditID uuid.UUID, claimType string, claimID uuid.UUID, adminID uuid.UUID) error {
	if m == nil || m.db == nil {
		return errors.New("inbound matcher: not configured")
	}
	claimType = strings.TrimSpace(claimType)
	src, ok := m.sourceFor(claimType)
	if !ok {
		return fmt.Errorf("inbound bind: no claim source registered for %q", claimType)
	}

	var credit models.InboundCredit
	if err := m.db.WithContext(ctx).First(&credit, "id = ?", creditID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("inbound bind: credit %s not found", creditID)
		}
		return fmt.Errorf("inbound bind: load credit %s: %w", creditID, err)
	}

	switch credit.Status {
	case models.InboundCreditUnmatched, models.InboundCreditHeld:
		// bindable
	case models.InboundCreditMatched:
		// Re-confirming the bind that already exists is a no-op, so a double
		// submit from the console does not read as a failure. Pointing the
		// same money at a DIFFERENT claim is refused: the first product has
		// already been paid and nothing here can take that back.
		if credit.MatchedClaimType == claimType && credit.MatchedClaimID != nil && *credit.MatchedClaimID == claimID {
			return nil
		}
		return fmt.Errorf("inbound bind: credit %s is already matched to %s claim %v — reassigning would credit it twice",
			creditID, credit.MatchedClaimType, credit.MatchedClaimID)
	default:
		return fmt.Errorf("inbound bind: credit %s is %s and cannot be assigned", creditID, credit.Status)
	}

	// The claim's owner comes from the source, not from the caller: the admin
	// console picks a claim, and letting it also assert whose claim it is
	// would let a console bug credit the wrong user.
	claims, err := src.OpenClaims(ctx, credit.Currency)
	if err != nil {
		return fmt.Errorf("inbound bind: open claims for %s: %w", claimType, err)
	}
	var target *InboundClaim
	for i := range claims {
		if claims[i].ID == claimID {
			target = &claims[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("inbound bind: %s claim %s is not open — it may already be settled or expired", claimType, claimID)
	}
	target.Type = claimType

	bound, err := m.bindCredit(ctx, &credit, inboundCandidate{claim: *target}, &adminID)
	if err != nil {
		return err
	}
	if !bound {
		return fmt.Errorf("inbound bind: credit %s was decided by someone else while assigning", creditID)
	}
	return nil
}

// ─── amount ────────────────────────────────────────────────────────────────

// roundKoboToNaira snaps a kobo amount to the nearest whole naira, half away
// from zero.
//
// Both sides of the amount comparison pass through here so they cannot drift:
// orders are minted at whole naira (see OpenBuyOrder) and a credit's gross is
// rounded before it is compared. Without the rounding, a provider fee
// reconstruction that lands a kobo out — which float-sourced statement amounts
// routinely do — would fail an otherwise perfect match and queue a hold.
func roundKoboToNaira(kobo int64) int64 {
	if kobo < 0 {
		return -roundKoboToNaira(-kobo)
	}
	return (kobo + 50) / 100 * 100
}

// wholeNairaKobo is roundKoboToNaira over the decimal kobo strings the IT
// service passes around, for minting an order at an amount a payer can actually
// transfer.
//
// Input that does not parse is returned UNCHANGED rather than rejected here:
// CreateBuyIntent already refuses a non-numeric or non-positive amount with the
// error every caller expects, and a second rejection with different wording
// would be one more thing to keep in step.
func wholeNairaKobo(ngnKobo string) string {
	n, ok := new(big.Int).SetString(strings.TrimSpace(ngnKobo), 10)
	if !ok {
		return ngnKobo
	}
	hundred := big.NewInt(100)
	q, r := new(big.Int).QuoRem(n, hundred, new(big.Int))
	// Half away from zero, matching roundKoboToNaira, so the order and the
	// credit it will be matched against can never round in opposite directions.
	if r.CmpAbs(big.NewInt(50)) >= 0 {
		if n.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return q.Mul(q, hundred).String()
}

// ─── hold reasons ──────────────────────────────────────────────────────────
//
// A hold reason is read by an operator deciding where real money goes, so it
// says how many claims matched the amount, how many matched the name, and who
// was in contention. "No match" on its own tells them nothing they can act on.

func inboundUnverifiableReason(credit *models.InboundCredit, want int64, all, unverifiable, matched []inboundCandidate) string {
	// How the checkable rivals fared is only worth saying when there ARE any.
	// "0 of the other 0 matched" is noise in the one case an operator meets
	// most: a single claim whose owner simply has no name captured yet.
	rest := ""
	if checkable := len(all) - len(unverifiable); checkable > 0 {
		rest = fmt.Sprintf(" (%d of the other %d matched the name)", len(matched), checkable)
	}
	return fmt.Sprintf(
		"%s from %s: %d claim(s) match the amount; %d with no Didit-verified legal name cannot be checked%s. Capture the name or assign by hand: %s",
		inboundMoney(credit.Currency, want), inboundSenderLabel(credit.SenderName),
		len(all), len(unverifiable), rest,
		inboundClaimList(unverifiable, false))
}

func inboundNoNameMatchReason(credit *models.InboundCredit, want int64, scored []inboundCandidate) string {
	return fmt.Sprintf(
		"%s from %s: %d claim(s) match the amount but the sender matches none of their verified names: %s",
		inboundMoney(credit.Currency, want), inboundSenderLabel(credit.SenderName),
		len(scored), inboundClaimList(scored, true))
}

func inboundContestedReason(credit *models.InboundCredit, want int64, matched []inboundCandidate) string {
	return fmt.Sprintf(
		"contested: %s from %s matches %d open claims at once, so no automatic bind is safe — assign by hand: %s",
		inboundMoney(credit.Currency, want), inboundSenderLabel(credit.SenderName),
		len(matched), inboundClaimList(matched, true))
}

// inboundSenderMax bounds how much of the sender name a reason spends.
//
// The reason has 255 bytes and the CLAIM LIST is the part an operator acts on
// — it names the users in dispute. An unbounded sender name (the provider's
// free text, which we do not control) would push that list out of the line
// entirely and leave a hold nobody can resolve without opening the row.
const inboundSenderMax = 48

func inboundSenderLabel(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "an unnamed sender"
	}
	if len(name) > inboundSenderMax {
		name = truncate(name, inboundSenderMax-1) + "…"
	}
	return fmt.Sprintf("%q", name)
}

// inboundClaimList renders candidates as "it_mcgp/1a2b3c4d user 9f8e7d6c
// \"EMMANUEL ADEMU\" exact", short ids because the reason has 255 bytes to
// work with and a full uuid pair eats a fifth of that per claim.
func inboundClaimList(cands []inboundCandidate, withName bool) string {
	var parts []string
	for i, c := range cands {
		if i == inboundHoldReasonMaxClaims {
			parts = append(parts, fmt.Sprintf("+%d more", len(cands)-i))
			break
		}
		p := fmt.Sprintf("%s/%s user %s", c.claim.Type, shortID(c.claim.ID), shortID(c.claim.UserID))
		if withName {
			p += fmt.Sprintf(" %q %s", inboundOrDash(c.kycName), c.strength)
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

// shortID is the first uuid group — enough for an operator to find the row in
// the admin console, short enough that several fit in one reason line.
func shortID(id uuid.UUID) string {
	s := id.String()
	if len(s) < 8 {
		return s
	}
	return s[:8]
}

func inboundOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// inboundMoney renders minor units as "NGN 2,000.00". ASCII currency code
// rather than ₦ because the same string ends up in log lines and alert emails
// whose encoding we do not control, and a mangled amount in a money alert is
// worse than a verbose one.
func inboundMoney(currency string, kobo int64) string {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = inboundDefaultCurrency
	}
	neg := kobo < 0
	if neg {
		kobo = -kobo
	}
	major := fmt.Sprintf("%d", kobo/100)
	var grouped strings.Builder
	for i, r := range major {
		if i > 0 && (len(major)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(r)
	}
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s %s%s.%02d", currency, sign, grouped.String(), kobo%100)
}
