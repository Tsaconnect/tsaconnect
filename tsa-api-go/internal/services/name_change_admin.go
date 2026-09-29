package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/big"
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
	// RefundAmount is what a refund sends (or sent) back, in base units: all
	// that was credited, or only the overpaid part of a kept fee.
	RefundAmount   string     `json:"refundBaseUnits,omitempty"`
	RefundAttempts int        `json:"refundAttempts"`
	RefundRetryAt  *time.Time `json:"refundRetryAt,omitempty"`
	// RefundStuck: the refund's outcome is unknown and waits for an admin —
	// exactly when AdminRecordRefundOutcome will take one (refund_uncertain,
	// or refund_sending past the alert window).
	RefundStuck bool `json:"refundStuck"`
	// ClearedPayRef is a payment we stopped waiting for, still watched through
	// the late-payment window and sent back whole should it land.
	ClearedPayRef string `json:"clearedPayRef,omitempty"`
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

// NameChangeFilter narrows the admin listing. FeeStatuses (any of) finds the
// changes whose money needs a look, whatever their status.
type NameChangeFilter struct {
	Status      string
	FeeStatuses []string
	UserID      *uuid.UUID
	Page        int
	Limit       int
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
	if len(f.FeeStatuses) > 0 {
		q = q.Where("fee_status IN ?", f.FeeStatuses)
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
			PayCredited: r.PayCredited, FeeTxHash: r.FeeTxHash, RefundAmount: r.RefundAmount,
			RefundAttempts: r.RefundAttempts, RefundRetryAt: r.RefundRetryAt, RefundStuck: len(s.refundOutcomeFrom(r)) > 0}
		if r.PayRef != nil {
			out[i].PayRef = *r.PayRef
		}
		if r.ClearedPayRef != nil {
			out[i].ClearedPayRef = *r.ClearedPayRef
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
	if errors.Is(err, errNameChangeAccountUnverified) {
		return nil, ErrNameChangeAccountUnverified // it stays held
	}
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

// refundOutcomeFrom is the fee status an admin may record a refund's outcome
// from — the refund may have left and nothing will find out on its own —
// or nil when the change has no such refund.
func (s *NameChangeService) refundOutcomeFrom(ch *models.UserNameChange) []string {
	switch {
	case ch.FeeStatus == models.NameChangeFeeRefundUncertain:
		return []string{models.NameChangeFeeRefundUncertain}
	case ch.FeeStatus == models.NameChangeFeeRefundSending && s.now().Sub(ch.UpdatedAt) > nameChangeRefundAlertAfter:
		return []string{models.NameChangeFeeRefundSending}
	}
	return nil
}

// AdminRecordRefundOutcome closes a refund the sweep could not: one that may
// have left (refund_uncertain, or refund_sending past the alert window). The
// admin's word is checked against the chain before it moves money bookkeeping:
// "sent" must be a mined transfer of exactly the refund from the treasury to
// the paying wallet, used for no other change; "never left" is taken only when
// no transaction was recorded or the chain has definitely never heard of it —
// that answer sends the refund again (refund_failed, retried at once).
func (s *NameChangeService) AdminRecordRefundOutcome(ctx context.Context, adminID, changeID uuid.UUID, o NameChangeRefundOutcome) (*NameChangeAdminView, error) {
	ch, err := s.get(ctx, changeID)
	if err != nil {
		return nil, err
	}
	if ch.UserID == adminID {
		return nil, ErrNameChangeSelfReview
	}
	from := s.refundOutcomeFrom(ch)
	if from == nil {
		return nil, ErrNameChangeBadState
	}
	to, tx := models.NameChangeFeeRefundFailed, ""
	extra := map[string]any{}
	if o.Sent {
		tx = strings.ToLower(strings.TrimSpace(o.TxHash))
		if len(tx) != 66 || !strings.HasPrefix(tx, "0x") {
			return nil, fmt.Errorf("%w: a sent refund needs its transaction hash", ErrNameChangeInvalid)
		}
		if err := s.verifyRefundSent(ctx, ch, tx); err != nil {
			return nil, err
		}
		to = models.NameChangeFeeRefunded
	} else {
		if err := s.verifyRefundNeverLeft(ctx, ch); err != nil {
			return nil, err
		}
		// Found never to have left: the sweep retries it at once, its backoff
		// (and the attempt count that grows it) reset.
		extra["refund_retry_at"], extra["refund_attempts"] = nil, 0
	}
	if !s.setFeeWith(ctx, ch.ID, from, to, tx, extra) {
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

// verifyRefundSent holds an admin's "sent" to the chain: txHash is mined,
// signed by the treasury, and moved exactly the claimed refund of the fee's
// token to the paying wallet — and no other change already counts it.
func (s *NameChangeService) verifyRefundSent(ctx context.Context, ch *models.UserNameChange, txHash string) error {
	if s.rail == nil {
		return ErrNameChangeChainUnreadable
	}
	want, ok := new(big.Int).SetString(strings.TrimSpace(ch.RefundAmount), 10)
	if !ok || want.Sign() <= 0 || ch.PayFrom == "" {
		return fmt.Errorf("%w: no refund amount or paying wallet recorded — check it by hand", ErrNameChangeRefundUnproven)
	}
	treasury := s.rail.To()
	p, err := s.rail.VerifyPayment(ctx, ch.PayChain, txHash, ch.PayTokenAddress, treasury, ch.PayFrom)
	switch {
	case errors.Is(err, ErrTransferNotFound), errors.Is(err, ErrTransferReverted):
		return fmt.Errorf("%w (%v)", ErrNameChangeRefundUnproven, err)
	case err != nil:
		log.Printf("[name-change] change=%s refund %s unreadable: %v", ch.ID, txHash, err)
		return ErrNameChangeChainUnreadable
	case p == nil || p.Confirmations < 1:
		return fmt.Errorf("%w: it isn't mined yet", ErrNameChangeRefundUnproven)
	case !strings.EqualFold(p.Sender, treasury):
		return fmt.Errorf("%w: it was signed by %s, not the treasury", ErrNameChangeRefundUnproven, p.Sender)
	case p.Credited == nil || p.Credited.Cmp(want) != 0:
		return fmt.Errorf("%w: it moved %v, the refund is %s (base units)", ErrNameChangeRefundUnproven, p.Credited, want)
	}
	var others int64
	if err := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
		Where("fee_tx_hash = ? AND id <> ?", txHash, ch.ID).Count(&others).Error; err != nil {
		return err
	}
	if others > 0 {
		return fmt.Errorf("%w: that transaction is already recorded as another change's refund", ErrNameChangeRefundUnproven)
	}
	return nil
}

// verifyRefundNeverLeft holds an admin's "never left" to the chain: only when
// no refund transaction was recorded, or the chain has definitely never heard
// of the one that was. A known one — even pending — may still pay the user,
// and sending the refund again would pay them twice.
func (s *NameChangeService) verifyRefundNeverLeft(ctx context.Context, ch *models.UserNameChange) error {
	if ch.FeeTxHash == "" {
		return nil
	}
	if s.rail == nil {
		return ErrNameChangeChainUnreadable
	}
	known, _, err := s.rail.TransactionKnown(ctx, ch.PayChain, ch.FeeTxHash)
	if err != nil {
		log.Printf("[name-change] change=%s refund %s lookup failed: %v", ch.ID, ch.FeeTxHash, err)
		return ErrNameChangeChainUnreadable
	}
	if known {
		return ErrNameChangeRefundMayHaveLeft
	}
	return nil
}

// AdminClearUnprovenPayment is a super admin's way out for a stablecoin change
// stuck on a named transaction that will never pay it. The chain is read again
// first, and has the last word: a payment that is proven, mined with money in
// it, or merely known to the chain (pending, or not yet deep enough) may still
// pay and is never cleared. What is cleared is either refuted (it cannot pay)
// or unknown to the chain — and an unknown one might still be broadcast, so it
// is kept watched through the late-payment window and sent back whole should
// it land (return_ops). The reason is required and recorded; a paying change
// closes unpaid.
func (s *NameChangeService) AdminClearUnprovenPayment(ctx context.Context, adminID, changeID uuid.UUID, reason string) (*NameChangeAdminView, error) {
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" {
		return nil, fmt.Errorf("%w: say what the chain shows for this payment", ErrNameChangeInvalid)
	}
	ch, err := s.get(ctx, changeID)
	if err != nil {
		return nil, err
	}
	if ch.UserID == adminID {
		return nil, ErrNameChangeSelfReview
	}
	if ch.PayMethod != models.NameChangePayStablecoin || ch.PayRef == nil || ch.FeeStatus != models.NameChangeFeeNone ||
		(ch.Status != models.NameChangeStatusPaying && ch.Status != models.NameChangeStatusCancelled) {
		return nil, ErrNameChangeBadState
	}
	tx := *ch.PayRef
	watch := false
	switch out, p, _ := s.checkStablePayment(ctx, ch, tx); {
	case out == stableProven:
		return nil, ErrNameChangePaymentLive
	case out == stablePending && p != nil && p.Credited != nil && p.Credited.Sign() > 0:
		return nil, ErrNameChangePaymentLive // mined, paying, only not deep enough yet
	case out == stablePending:
		if s.rail == nil {
			return nil, ErrNameChangeChainUnreadable
		}
		known, _, err := s.rail.TransactionKnown(ctx, ch.PayChain, tx)
		if err != nil {
			log.Printf("[name-change] change=%s payment %s lookup failed: %v", ch.ID, tx, err)
			return nil, ErrNameChangeChainUnreadable
		}
		if known {
			return nil, ErrNameChangePaymentLive
		}
		watch = true
	}
	if !s.clearUnprovenPayment(ctx, ch, fmt.Sprintf("by admin %s (%s)", adminID, reason), watch) {
		return nil, ErrNameChangeBadState // proven, or changed, in between
	}
	log.Printf("[name-change] AUDIT change=%s admin=%s action=clear_unproven_payment tx=%s status_was=%s watched=%t reason=%q",
		ch.ID, adminID, tx, ch.Status, watch, reason)
	return s.adminView(ctx, s.reload(ctx, ch))
}
