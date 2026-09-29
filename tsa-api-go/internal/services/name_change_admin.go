package services

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Admin review of held name changes, and the admin's way out of an uncertain
// refund.

// NameChangeAdminView is a change as an admin reviews it: both names, every
// piece of same-person evidence and why the change was held, the account's age
// and activity, and every Didit session it ran.
type NameChangeAdminView struct {
	NameChangeView
	UserID                  uuid.UUID          `json:"userId"`
	User                    NameChangeUserInfo `json:"user"`
	OldVerifiedName         string             `json:"oldVerifiedName"`
	VerifiedLegalNameAt     *time.Time         `json:"verifiedLegalNameAt,omitempty"`
	OriginalDiditSessionID  string             `json:"originalDiditSessionId"`
	OriginalDocName         string             `json:"originalDocName,omitempty"`
	OriginalMatchStrength   string             `json:"originalMatchStrength,omitempty"`
	DOBMatch                *bool              `json:"dobMatch"`
	OriginalDOB             string             `json:"originalDob,omitempty"`
	NewDOB                  string             `json:"newDob,omitempty"`
	DocumentCountryMatch    *bool              `json:"documentCountryMatch"`
	OriginalDocumentCountry string             `json:"originalDocumentCountry,omitempty"`
	NewDocumentCountry      string             `json:"newDocumentCountry,omitempty"`
	DocumentNumberMatch     *bool              `json:"documentNumberMatch"`
	FaceMatch               *bool              `json:"faceMatch"`
	FaceSimilarity          *float64           `json:"faceSimilarity,omitempty"`
	HoldReason              string             `json:"holdReason,omitempty"`
	ReviewNote              string             `json:"reviewNote,omitempty"`
	DiditSessionIDs         []string           `json:"diditSessionIds"`
	ReviewedBy              *uuid.UUID         `json:"reviewedBy,omitempty"`
	ReviewedAt              *time.Time         `json:"reviewedAt,omitempty"`
	PayRef                  string             `json:"payRef,omitempty"`
	PayCredited             string             `json:"payCreditedBaseUnits,omitempty"`
	FeeTxHash               string             `json:"feeTxHash,omitempty"`
}

// NameChangeUserInfo identifies whose change it is, and how established the
// account is: a brand-new account or one nobody has signed in to for months is
// a different risk from a long-standing active one.
type NameChangeUserInfo struct {
	ID             uuid.UUID  `json:"id"`
	Name           string     `json:"name"`
	Username       string     `json:"username"`
	Email          string     `json:"email"`
	CreatedAt      *time.Time `json:"createdAt,omitempty"`
	AccountAgeDays int        `json:"accountAgeDays"`
	LastLogin      *time.Time `json:"lastLogin,omitempty"`
}

// NameChangeFilter narrows the admin listing.
type NameChangeFilter struct {
	Status string
	UserID *uuid.UUID
	Page   int
	Limit  int
}

// AdminList lists changes newest first.
func (s *NameChangeService) AdminList(ctx context.Context, f NameChangeFilter) ([]NameChangeAdminView, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit < 1 || f.Limit > 100 {
		f.Limit = 20
	}
	q := s.db.WithContext(ctx).Model(&models.UserNameChange{})
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.UserID != nil {
		q = q.Where("user_id = ?", *f.UserID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []models.UserNameChange
	if err := q.Order("created_at desc").Offset((f.Page - 1) * f.Limit).Limit(f.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out, err := s.adminViews(ctx, rows)
	return out, total, err
}

type nameChangeAdminUser struct {
	info                NameChangeUserInfo
	verifiedLegalNameAt *time.Time
}

// adminViews is rows as an admin reads them, with whose change each is.
func (s *NameChangeService) adminViews(ctx context.Context, rows []models.UserNameChange) ([]NameChangeAdminView, error) {
	users := map[uuid.UUID]nameChangeAdminUser{}
	if len(rows) > 0 {
		ids := make([]uuid.UUID, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.UserID)
		}
		var found []struct {
			ID                    uuid.UUID
			Name, Username, Email sql.NullString
			CreatedAt, LastLogin  sql.NullTime
			VerifiedLegalNameAt   sql.NullTime
		}
		if err := s.db.WithContext(ctx).Table("users").
			Select("id", "name", "username", "email", "created_at", "last_login", "verified_legal_name_at").
			Where("id IN ?", ids).Scan(&found).Error; err != nil {
			return nil, err
		}
		now := s.now()
		for _, u := range found {
			au := nameChangeAdminUser{info: NameChangeUserInfo{ID: u.ID, Name: u.Name.String, Username: u.Username.String, Email: u.Email.String}}
			if u.CreatedAt.Valid {
				at := u.CreatedAt.Time
				au.info.CreatedAt, au.info.AccountAgeDays = &at, int(now.Sub(at)/(24*time.Hour))
			}
			if u.LastLogin.Valid {
				at := u.LastLogin.Time
				au.info.LastLogin = &at
			}
			if u.VerifiedLegalNameAt.Valid {
				at := u.VerifiedLegalNameAt.Time
				au.verifiedLegalNameAt = &at
			}
			users[u.ID] = au
		}
	}
	out := make([]NameChangeAdminView, len(rows))
	for i := range rows {
		r := &rows[i]
		u, ok := users[r.UserID]
		if !ok {
			u.info.ID = r.UserID
		}
		out[i] = NameChangeAdminView{NameChangeView: *s.view(r), UserID: r.UserID, User: u.info,
			OldVerifiedName: r.OldVerifiedName, VerifiedLegalNameAt: u.verifiedLegalNameAt,
			OriginalDiditSessionID: r.OriginalDiditSessionID, OriginalDocName: r.OriginalDocName,
			OriginalMatchStrength: r.OriginalMatchStrength, DOBMatch: r.DOBMatch, OriginalDOB: r.OriginalDOB, NewDOB: r.NewDOB,
			DocumentCountryMatch: r.DocumentCountryMatch, OriginalDocumentCountry: r.OriginalDocumentCountry,
			NewDocumentCountry: r.NewDocumentCountry, DocumentNumberMatch: r.DocumentNumberMatch, FaceMatch: r.FaceMatch,
			FaceSimilarity: r.FaceSimilarity, HoldReason: r.HoldReason, ReviewNote: r.ReviewNote,
			DiditSessionIDs: append([]string{}, r.DiditSessionIDs...), ReviewedBy: r.ReviewedBy, ReviewedAt: r.ReviewedAt,
			PayCredited: r.PayCredited, FeeTxHash: r.FeeTxHash}
		if r.PayRef != nil {
			out[i].PayRef = *r.PayRef
		}
	}
	return out, nil
}

func (s *NameChangeService) adminView(ctx context.Context, ch *models.UserNameChange) (*NameChangeAdminView, error) {
	views, err := s.adminViews(ctx, []models.UserNameChange{*ch})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// heldForReview loads a held change an admin may decide: never their own.
func (s *NameChangeService) heldForReview(ctx context.Context, adminID, changeID uuid.UUID) (*models.UserNameChange, error) {
	ch, err := s.get(ctx, changeID)
	if err != nil {
		return nil, err
	}
	if ch.UserID == adminID {
		return nil, ErrNameChangeSelfReview
	}
	if ch.Status != models.NameChangeStatusHeldForReview {
		return nil, ErrNameChangeBadState
	}
	return ch, nil
}

// AdminApprove applies a held name: the admin has judged the new document to be
// the same person. Money still moving under the old name blocks it — the admin
// approves once that has finished.
func (s *NameChangeService) AdminApprove(ctx context.Context, adminID, changeID uuid.UUID, note string) (*NameChangeAdminView, error) {
	ch, err := s.heldForReview(ctx, adminID, changeID)
	if err != nil {
		return nil, err
	}
	if ch.NewVerifiedName == "" {
		return nil, ErrNameChangeBadState
	}
	if blockers, err := s.Blockers(ctx, ch.UserID); err != nil {
		return nil, err
	} else if len(blockers) > 0 {
		return nil, &NameChangeBlockedError{Blockers: blockers}
	}
	applied, err := s.apply(ctx, ch, []string{models.NameChangeStatusHeldForReview}, ch.NewVerifiedName, &adminID, note, nil)
	if err != nil {
		return nil, err
	}
	if applied.Status != models.NameChangeStatusApproved || applied.ReviewedBy == nil || *applied.ReviewedBy != adminID {
		return nil, ErrNameChangeBadState
	}
	return s.adminView(ctx, applied)
}

// AdminReject refuses a held name. The account keeps its name; the fee is
// kept, as verification was run. The admin's note stays internal.
func (s *NameChangeService) AdminReject(ctx context.Context, adminID, changeID uuid.UUID, note string) (*NameChangeAdminView, error) {
	ch, err := s.heldForReview(ctx, adminID, changeID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	moved, err := s.transition(ctx, ch.ID, []string{models.NameChangeStatusHeldForReview}, map[string]any{
		"status": models.NameChangeStatusRejected, "reviewed_by": adminID, "reviewed_at": &now,
		"review_note": truncateRunes(strings.TrimSpace(note), 512), "completed_at": &now,
		"user_message": "Your name change was not approved, so your name stays as it was. Contact support if you think this is a mistake."})
	if err != nil {
		return nil, err
	}
	if !moved {
		return nil, ErrNameChangeBadState
	}
	ch = s.reload(ctx, ch)
	s.resolveFee(ctx, ch)
	s.publish(ch, events.NameChangeRejected, "Your name change was not approved",
		"Your name stays as it was. Contact support if you think this is a mistake.")
	return s.adminView(ctx, s.reload(ctx, ch))
}

// NameChangeRefundOutcome is what an admin found on-chain for a refund whose
// broadcast ended uncertain.
type NameChangeRefundOutcome struct {
	// Sent: the refund transaction TxHash did reach the user.
	Sent   bool
	TxHash string
	Note   string
}

// AdminRecordRefundOutcome closes a refund the sweep could not: one that may
// have left (refund_uncertain, or refund_sending past the alert window). A
// refund an admin confirms sent is recorded with its transaction; one they
// confirm never left goes back to refund_failed, which the sweep retries.
func (s *NameChangeService) AdminRecordRefundOutcome(ctx context.Context, adminID, changeID uuid.UUID, o NameChangeRefundOutcome) (*NameChangeAdminView, error) {
	ch, err := s.get(ctx, changeID)
	if err != nil {
		return nil, err
	}
	from := []string{models.NameChangeFeeRefundUncertain}
	if ch.FeeStatus == models.NameChangeFeeRefundSending && s.now().Sub(ch.UpdatedAt) > nameChangeRefundAlertAfter {
		from = append(from, models.NameChangeFeeRefundSending)
	}
	to, tx := models.NameChangeFeeRefundFailed, ""
	if o.Sent {
		tx = strings.TrimSpace(o.TxHash)
		if len(tx) != 66 || !strings.HasPrefix(strings.ToLower(tx), "0x") {
			return nil, fmt.Errorf("%w: a sent refund needs its transaction hash", ErrNameChangeInvalid)
		}
		to = models.NameChangeFeeRefunded
	}
	if !s.setFee(ctx, ch.ID, from, to, tx) {
		return nil, ErrNameChangeBadState
	}
	note := truncateRunes(fmt.Sprintf("refund recorded by an admin as %s. %s", to, strings.TrimSpace(o.Note)), 512)
	if err := s.db.WithContext(ctx).Model(&models.UserNameChange{}).Where("id = ?", ch.ID).
		Updates(map[string]any{"review_note": note}).Error; err != nil {
		return nil, err
	}
	log.Printf("[name-change] AUDIT change=%s admin=%s refund_outcome=%s tx=%s", ch.ID, adminID, to, tx)
	return s.adminView(ctx, s.reload(ctx, ch))
}
