package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Verification attempts, and what Didit's decision does to the account.

// nameChangePurchaseStale is how long a claim to buy a Didit session may sit
// unrecorded before another request may take it over (a crash mid-purchase).
const nameChangePurchaseStale = 2 * time.Minute

// NameChangeAttempt is the change a verification session is for, and what the
// caller is to do: resume the open session, or buy one under Claim.
type NameChangeAttempt struct {
	Change *models.UserNameChange
	// Resume: Change.DiditSessionURL is the session to send the user to.
	Resume bool
	// Claim is this caller's exclusive right to buy the session; the session
	// is recorded against it (RecordSession) or the claim released
	// (AbandonAttempt).
	Claim string
	// Counted: this call used up an attempt, which AbandonAttempt gives back.
	Counted bool
}

// BeginAttempt claims the next verification attempt of the user's change and
// the right to buy its Didit session, or hands back the session already open.
//
// The attempt is counted before any session is bought (a crash then costs an
// attempt, never a free session), and the purchase is claimed with a guarded
// UPDATE, so two taps can never both pay Didit: the loser resumes the winner's
// session or is told to try again in a moment.
func (s *NameChangeService) BeginAttempt(ctx context.Context, userID uuid.UUID) (*NameChangeAttempt, error) {
	ch, err := s.openChange(ctx, userID)
	if err != nil {
		return nil, err
	}
	if ch == nil {
		return nil, ErrNameChangeNotFound
	}
	if !s.now().Before(ch.ExpiresAt) {
		return nil, ErrNameChangeLapsed
	}
	counted := false
	switch ch.Status {
	case models.NameChangeStatusPaying:
		return nil, ErrNameChangePaymentPending
	case models.NameChangeStatusHeldForReview:
		return nil, ErrNameChangeUnderReview
	case models.NameChangeStatusVerifying:
		if ch.DiditSessionURL != "" {
			return &NameChangeAttempt{Change: ch, Resume: true}, nil
		}
		// The attempt was counted but no session bought (a failed or
		// interrupted purchase): buying one now costs no further attempt.
	case models.NameChangeStatusPaid, models.NameChangeStatusDeclined:
		if ch.Attempts >= s.cfg.MaxAttempts {
			return nil, ErrNameChangeAttemptsExhausted
		}
		if blockers, err := s.Blockers(ctx, userID); err != nil {
			return nil, err
		} else if len(blockers) > 0 {
			return nil, &NameChangeBlockedError{Blockers: blockers}
		}
		counted = true
	default:
		return nil, ErrNameChangeBadState
	}
	if err := s.sessionLimit(ctx, userID); err != nil {
		return nil, err
	}
	if counted {
		res := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
			Where("id = ? AND status = ? AND attempts = ?", ch.ID, ch.Status, ch.Attempts).
			Updates(map[string]any{"status": models.NameChangeStatusVerifying, "attempts": ch.Attempts + 1,
				"didit_session_id": "", "didit_session_url": "", "user_message": ""})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected != 1 {
			return nil, ErrNameChangeBadState // cancelled, or another attempt started, in between
		}
	}
	claim := nameChangePurchasePrefix + uuid.NewString()
	res := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
		Where("id = ? AND status = ? AND (didit_session_id = '' OR (didit_session_id LIKE ? AND updated_at < ?))",
			ch.ID, models.NameChangeStatusVerifying, nameChangePurchasePrefix+"%", s.now().Add(-nameChangePurchaseStale)).
		Updates(map[string]any{"didit_session_id": claim, "updated_at": s.now()})
	if res.Error != nil {
		return nil, res.Error
	}
	fresh := s.reload(ctx, ch)
	if res.RowsAffected != 1 {
		if fresh.Status == models.NameChangeStatusVerifying && fresh.DiditSessionURL != "" {
			return &NameChangeAttempt{Change: fresh, Resume: true}, nil
		}
		return nil, ErrNameChangeSessionPending
	}
	return &NameChangeAttempt{Change: fresh, Claim: claim, Counted: counted}, nil
}

// sessionLimit refuses a new Didit session past MaxSessionsPer30Days, however
// the changes behind them were paid for.
func (s *NameChangeService) sessionLimit(ctx context.Context, userID uuid.UUID) error {
	var rows []models.UserNameChange
	if err := s.db.WithContext(ctx).Select("didit_session_ids").
		Where("user_id = ? AND updated_at > ?", userID, s.now().Add(-nameChangeSessionWindow)).Find(&rows).Error; err != nil {
		return err
	}
	n := 0
	for _, r := range rows {
		n += len(r.DiditSessionIDs)
	}
	if n >= s.cfg.MaxSessionsPer30Days {
		return ErrNameChangeSessionLimit
	}
	return nil
}

// RecordSession stores the Didit session bought under claim.
func (s *NameChangeService) RecordSession(ctx context.Context, changeID uuid.UUID, claim, sessionID, url string) error {
	ch, err := s.get(ctx, changeID)
	if err != nil {
		return err
	}
	ids := append(datatypes.JSONSlice[string]{}, ch.DiditSessionIDs...)
	ids = append(ids, sessionID)
	res := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
		Where("id = ? AND status = ? AND didit_session_id = ?", changeID, models.NameChangeStatusVerifying, claim).
		Updates(map[string]any{"didit_session_id": sessionID, "didit_session_url": url, "didit_session_ids": ids})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrNameChangeBadState
	}
	return nil
}

// AbandonAttempt releases a purchase claim no session could be bought under,
// and gives back the attempt it counted, so a Didit outage does not use up what
// the user paid for.
func (s *NameChangeService) AbandonAttempt(ctx context.Context, a *NameChangeAttempt) {
	if a == nil || a.Claim == "" {
		return
	}
	updates := map[string]any{"didit_session_id": ""}
	if a.Counted {
		back := models.NameChangeStatusDeclined
		if a.Change.Attempts <= 1 {
			back = models.NameChangeStatusPaid
		}
		updates["status"], updates["attempts"] = back, gorm.Expr("attempts - 1")
	}
	if err := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
		Where("id = ? AND status = ? AND didit_session_id = ?", a.Change.ID, models.NameChangeStatusVerifying, a.Claim).
		Updates(updates).Error; err != nil {
		log.Printf("[name-change] change=%s could not release a failed session purchase: %v", a.Change.ID, err)
	}
}

// NameChangeIdentity is what one Didit session's decision says about the
// person. Document numbers are only compared, never stored or logged.
type NameChangeIdentity struct {
	Name           string
	DateOfBirth    string
	DocumentNumber string
	IssuingCountry string
	// FaceMatches are the earlier sessions Didit's face search found this
	// face in (empty when the workflow does not run face search).
	FaceMatches []NameChangeFaceMatch
}

// NameChangeFaceMatch is one earlier session with the same face.
type NameChangeFaceMatch struct {
	SessionID  string
	Similarity float64
}

// NameChangeDecision is what Didit decided about one session, with the
// identity it read and the identity of the account's ORIGINAL KYC session
// (nil when that session could not be read).
type NameChangeDecision struct {
	SessionID string
	Approved  bool
	New       NameChangeIdentity
	Original  *NameChangeIdentity
}

// ApplyDecision acts on Didit's verdict for the change's current session.
//
// A pass is applied only on strong evidence that the new document is the same
// person as the account's original one: the new name a close match
// (MatchAccountName exact/partial) for BOTH the current verified name and the
// original KYC name — so small steps cannot be chained into a different name
// one change at a time — the same date of birth, the same document country, a
// face match against the original session when required, and no money in
// flight. Anything short of that waits for an admin, with the reasons and the
// evidence on the change. A failure is a declined attempt, or the end of the
// change once none are left. Anything not about the current session of a change
// still verifying is a no-op: Didit redelivers, and a late event must not
// reopen what already moved on.
func (s *NameChangeService) ApplyDecision(ctx context.Context, changeID uuid.UUID, d NameChangeDecision) (*models.UserNameChange, error) {
	ch, err := s.get(ctx, changeID)
	if err != nil {
		return nil, err
	}
	if ch.Status != models.NameChangeStatusVerifying || ch.DiditSessionID != d.SessionID {
		return ch, nil
	}
	name := strings.Join(strings.Fields(d.New.Name), " ")
	if !d.Approved || name == "" {
		return s.decline(ctx, ch, d.Approved)
	}

	evidence, reasons := s.assess(ch, name, d)
	blockers, err := s.Blockers(ctx, ch.UserID)
	if err != nil {
		return nil, err
	}
	for _, b := range blockers {
		reasons = append(reasons, "money in flight: "+b.Code)
	}
	if len(reasons) == 0 {
		applied, err := s.apply(ctx, ch, []string{models.NameChangeStatusVerifying}, name, nil, "", evidence)
		if !errors.Is(err, errNameChangeAccountUnverified) {
			return applied, err
		}
		// The account lost its verification while the session ran (an admin
		// revoked it, a later KYC event rejected it): a name read off a new
		// document is not put on an account in that state without a human.
		reasons = append(reasons, "the account is no longer verified")
	}
	evidence["status"] = models.NameChangeStatusHeldForReview
	evidence["new_verified_name"] = name
	evidence["didit_session_url"] = ""
	evidence["hold_reason"] = truncateRunes(strings.Join(reasons, "; "), 512)
	evidence["user_message"] = "Your new ID passed verification and our team is checking it before your name changes. We'll let you know."
	moved, err := s.transition(ctx, ch.ID, []string{models.NameChangeStatusVerifying}, evidence)
	if err != nil {
		return nil, err
	}
	ch = s.reload(ctx, ch)
	if moved {
		s.publish(ch, events.NameChangeHeld, "Your name change is being reviewed",
			"Your new ID passed verification. Our team is checking it before your name changes, and we'll let you know.")
	}
	return ch, nil
}

// assess weighs the same-person evidence. It returns the columns recording it
// and every reason the change cannot apply itself (none: it may).
func (s *NameChangeService) assess(ch *models.UserNameChange, name string, d NameChangeDecision) (map[string]any, []string) {
	ev := map[string]any{}
	var reasons []string

	current := MatchAccountName(ch.OldVerifiedName, name)
	ev["match_strength"] = string(current)
	if !current.IsMatch() {
		reasons = append(reasons, "the new name is not a close match for the current verified name")
	}

	orig := d.Original
	if orig == nil || strings.TrimSpace(orig.Name) == "" {
		reasons = append(reasons, "the original KYC session could not be read")
		orig = &NameChangeIdentity{}
	} else {
		first := MatchAccountName(orig.Name, name)
		ev["original_doc_name"], ev["original_match_strength"] = strings.Join(strings.Fields(orig.Name), " "), string(first)
		if !first.IsMatch() {
			reasons = append(reasons, "the new name is not a close match for the original KYC name")
		}
	}

	newDOB, oldDOB := normalizeDOB(d.New.DateOfBirth), normalizeDOB(orig.DateOfBirth)
	ev["new_dob"], ev["original_dob"] = newDOB, oldDOB
	switch {
	case newDOB == "" || oldDOB == "":
		reasons = append(reasons, "a date of birth is missing from a document")
	default:
		ev["dob_match"] = newDOB == oldDOB
		if newDOB != oldDOB {
			reasons = append(reasons, "the date of birth differs from the original document")
		}
	}

	newCountry, oldCountry := strings.ToUpper(strings.TrimSpace(d.New.IssuingCountry)), strings.ToUpper(strings.TrimSpace(orig.IssuingCountry))
	ev["new_document_country"], ev["original_document_country"] = newCountry, oldCountry
	switch {
	case newCountry == "" || oldCountry == "":
		// An unread country is no evidence of the same one: like a missing
		// date of birth, it leaves the decision to an admin.
		reasons = append(reasons, "a document's issuing country is missing")
	default:
		ev["document_country_match"] = newCountry == oldCountry
		if newCountry != oldCountry {
			reasons = append(reasons, "the document was issued by a different country")
		}
	}

	if n, o := normalizeDocNumber(d.New.DocumentNumber), normalizeDocNumber(orig.DocumentNumber); n != "" && o != "" {
		ev["document_number_match"] = n == o
	}

	face := false
	for _, m := range d.New.FaceMatches {
		if ch.OriginalDiditSessionID != "" && m.SessionID == ch.OriginalDiditSessionID {
			face = true
			similarity := m.Similarity
			ev["face_similarity"] = &similarity
			break
		}
	}
	ev["face_match"] = face
	if !face && s.cfg.RequireFaceMatch {
		reasons = append(reasons, "Didit found no face match with the original KYC session")
	}
	return ev, reasons
}

// normalizeDOB keeps a date of birth comparable across documents ("1990-05-17").
func normalizeDOB(s string) string {
	s = strings.TrimSpace(s)
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format("2006-01-02")
	}
	if len(s) >= 10 {
		if t, err := time.Parse("2006-01-02", s[:10]); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return s
}

func normalizeDocNumber(s string) string {
	return strings.ToUpper(strings.Join(strings.Fields(strings.ReplaceAll(s, "-", "")), ""))
}

// decline records a failed session: another attempt, or the end of the change.
func (s *NameChangeService) decline(ctx context.Context, ch *models.UserNameChange, passedWithoutName bool) (*models.UserNameChange, error) {
	left := s.cfg.MaxAttempts - ch.Attempts
	updates := map[string]any{"status": models.NameChangeStatusDeclined, "didit_session_url": "",
		"user_message": fmt.Sprintf("Verification didn't pass. You can try again (%d attempt(s) left).", left)}
	if passedWithoutName {
		// A pass with no readable name cannot change a name either.
		updates["review_note"] = "verification passed but no name could be read from the document"
	}
	lapsed := left <= 0
	if lapsed {
		now := s.now()
		updates["status"], updates["completed_at"] = models.NameChangeStatusLapsed, &now
		updates["user_message"] = "Verification didn't pass and no attempts are left. Your name stays as it was."
	}
	moved, err := s.transition(ctx, ch.ID, []string{models.NameChangeStatusVerifying}, updates)
	if err != nil {
		return nil, err
	}
	ch = s.reload(ctx, ch)
	if moved && lapsed {
		s.resolveFee(ctx, ch)
		ch = s.reload(ctx, ch)
		s.publishLapsed(ch)
	}
	return ch, nil
}

// errNameChangeAccountUnverified: apply found the account no longer verified,
// and changed nothing.
var errNameChangeAccountUnverified = errors.New("name change: account no longer verified")

// apply puts the new document name on the account — verified name and display
// name together, from the one document that was checked — and closes the
// change as approved. from is the status the change must still be in; extra
// are further columns to record with it (the evidence).
func (s *NameChangeService) apply(ctx context.Context, ch *models.UserNameChange, from []string, name string, reviewer *uuid.UUID, note string, extra map[string]any) (*models.UserNameChange, error) {
	now := s.now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{"status": models.NameChangeStatusApproved, "new_verified_name": name, "completed_at": &now,
			"didit_session_url": "", "user_message": "Your name now matches your new ID."}
		for k, v := range extra {
			updates[k] = v
		}
		if reviewer != nil {
			updates["reviewed_by"], updates["reviewed_at"], updates["review_note"] = *reviewer, &now, truncateRunes(strings.TrimSpace(note), 512)
		}
		res := tx.Model(&models.UserNameChange{}).Where("id = ? AND status IN ?", ch.ID, from).Updates(finished(updates))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrNameChangeBadState
		}
		// The account's own KYC session pointer is left alone: the change row
		// keeps the session that read the new name, and first-time KYC's
		// events keep describing the session they always did (and may no
		// longer touch the name — see NameSetByChange).
		// Only onto an account that is still verified: the new name is a
		// VERIFIED name, and an account that lost its verification while the
		// change ran must not get one back this way.
		res = tx.Table("users").Where("id = ? AND verification_status = ?", ch.UserID, models.VerificationStatusVerified).
			Updates(map[string]any{"name": name, "verified_legal_name": name, "verified_legal_name_at": &now, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errNameChangeAccountUnverified
		}
		source := "didit"
		if reviewer != nil {
			source = "admin"
		}
		meta, _ := json.Marshal(map[string]string{"name_change_id": ch.ID.String(), "session_id": ch.DiditSessionID, "source": source})
		return tx.Create(&models.VerificationLog{UserID: ch.UserID, Action: models.VerificationActionNameChanged,
			Status: models.VerificationLogStatusVerified, Notes: "Name changed by re-verification",
			Metadata: datatypes.JSON(meta), AdminID: reviewer}).Error
	})
	if errors.Is(err, ErrNameChangeBadState) {
		return s.reload(ctx, ch), nil // someone else closed it first
	}
	if err != nil {
		return nil, err
	}
	ch = s.reload(ctx, ch)
	s.resolveFee(ctx, ch)
	s.publish(ch, events.NameChangeApproved, "Your name has been updated",
		"Your account now carries the name on your new ID. Payouts must now come from and go to accounts in this name.")
	return s.reload(ctx, ch), nil
}
