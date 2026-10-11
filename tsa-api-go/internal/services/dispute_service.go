package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var (
	ErrDisputeBadRequest      = errors.New("invalid dispute request")
	ErrDisputeAlreadyExists   = errors.New("this order has already been disputed")
	ErrDisputeSubjectNotFound = errors.New("order not found")
	ErrDisputeNotDisputable   = errors.New("this order cannot be disputed")
	ErrOrderDisputeNotFound   = errors.New("dispute not found")
	ErrDisputeClosed          = errors.New("this dispute has already been closed")
)

const (
	disputeReasonMax   = 1000
	disputeEvidenceMax = 4
	// A Cloudinary secure URL is well under this; the cap exists so four
	// megabyte-long strings cannot be posted into a text column.
	disputeEvidenceURLMax = 512
)

// validateEvidence screens uploaded-image URLs (isUploadedImageURL), because
// these render in an operator's browser.
func validateEvidence(urls []string) ([]string, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	if len(urls) > disputeEvidenceMax {
		return nil, fmt.Errorf("%w: at most %d images", ErrDisputeBadRequest, disputeEvidenceMax)
	}
	out := make([]string, 0, len(urls))
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" {
			return nil, fmt.Errorf("%w: empty evidence url", ErrDisputeBadRequest)
		}
		if len(u) > disputeEvidenceURLMax {
			return nil, fmt.Errorf("%w: evidence url too long", ErrDisputeBadRequest)
		}
		if !isUploadedImageURL(u) {
			return nil, fmt.Errorf("%w: evidence must be an uploaded image", ErrDisputeBadRequest)
		}
		out = append(out, u)
	}
	return out, nil
}

// DisputeSubject is everything the generic service needs to know about one
// kind of order: whether it is the caller's, and whether it has reached a
// state worth complaining about.
//
// This interface is the whole reason a single disputes table works. Every
// other part of the flow — filing, evidence, escalation, resolution, the admin
// queue — is identical whatever was ordered; only these two questions differ,
// so only these two are implemented per type.
type DisputeSubject interface {
	// Describe returns a short human label for the admin queue, or an error:
	// ErrDisputeSubjectNotFound when the order is not this user's (which is
	// also what a missing order returns — an attacker must not be able to tell
	// the two apart), ErrDisputeNotDisputable when it is too early.
	Describe(ctx context.Context, userID, subjectID uuid.UUID) (string, error)

	// Labels describes many orders at once for the admin queue. Separate from
	// Describe because the queue renders up to 200 rows and a per-row lookup
	// would be exactly the N+1 the batched reporter fetch avoids — and because
	// an admin is not the owner, so the ownership check Describe applies would
	// reject every row. Orders that have since been deleted are simply absent
	// from the map; a dispute must still appear without its label.
	Labels(ctx context.Context, subjectIDs []uuid.UUID) map[uuid.UUID]string
}

// DisputeInput is a filing request.
type DisputeInput struct {
	SubjectType string
	SubjectID   uuid.UUID
	Reason      string
	Evidence    []string
}

// DisputeQuery filters the admin queue.
type DisputeQuery struct {
	Status      string
	SubjectType string
	Limit       int
	Offset      int
}

// DisputeUser is the slim reporter identity shown beside a dispute —
// deliberately not the full models.User.
type DisputeUser struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Username string    `json:"username"`
	Email    string    `json:"email"`
}

// DisputeAdminRow is one dispute with its reporter and a subject label.
type DisputeAdminRow struct {
	Dispute models.Dispute `json:"dispute"`
	User    *DisputeUser   `json:"user,omitempty"`
	Subject string         `json:"subject,omitempty"`
	// Ramp is populated only for a paycrest_order dispute whose order is a
	// converted buy — the leg ledger and what it currently owes, so an
	// operator sees exactly what an admin refund would use BEFORE triggering
	// one. Absent (nil, omitted) for every other dispute; see RampLegsInfo
	// for why the OwedToUser field inside it never omits its own empty value.
	Ramp *RampLegsInfo `json:"ramp,omitempty"`
}

// rampLegsReader is what DisputeService needs from the converted-buy engine:
// an order's leg ledger and what it currently owes, by order id alone.
// *ConversionOrchestrator satisfies it (LegsAndOwedByOrderID); kept as an
// interface so GetAdmin's enrichment can be tested without a live treasury.
type rampLegsReader interface {
	LegsAndOwedByOrderID(ctx context.Context, orderID uuid.UUID) (*models.PaycrestOrderRecord, *RampLegsInfo, error)
}

// DisputeService owns every order dispute in the product except the two that
// resolve by moving funds themselves (P2P trades, marketplace orders) — those
// are workflows with their own machinery, not queue items.
type DisputeService struct {
	db       *gorm.DB
	bus      *events.Bus
	subjects map[string]DisputeSubject
	// ramp is dormant (nil) unless SetRampLegsReader is called, which main.go
	// only does when the ramp treasury itself is built — the same
	// dormant-if-unwired pattern as billService.SetDisputes.
	ramp rampLegsReader
}

func NewDisputeService(db *gorm.DB, bus *events.Bus) *DisputeService {
	return &DisputeService{db: db, bus: bus, subjects: map[string]DisputeSubject{}}
}

// SetRampLegsReader wires the converted-buy leg ledger into the dispute
// detail view. Nil-safe and optional: without it, a paycrest_order dispute's
// detail simply carries no Ramp field, same as today.
func (s *DisputeService) SetRampLegsReader(r rampLegsReader) {
	if s == nil {
		return
	}
	s.ramp = r
}

// RegisterSubject wires one order type in. Called at boot for each type whose
// stack is actually configured, so a dormant integration (bills without a
// provider, Paycrest without an API key) simply has no disputable subject
// rather than a route that fails at query time.
func (s *DisputeService) RegisterSubject(subjectType string, subject DisputeSubject) {
	if s == nil || subject == nil {
		return
	}
	s.subjects[subjectType] = subject
}

// File records a user's complaint about one of their own orders.
func (s *DisputeService) File(ctx context.Context, userID uuid.UUID, in DisputeInput) (*models.Dispute, error) {
	if s == nil {
		return nil, ErrDisputeBadRequest
	}
	subject, ok := s.subjects[in.SubjectType]
	if !ok {
		return nil, fmt.Errorf("%w: unknown subject type", ErrDisputeBadRequest)
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, fmt.Errorf("%w: reason required", ErrDisputeBadRequest)
	}
	if len(reason) > disputeReasonMax {
		return nil, fmt.Errorf("%w: reason too long", ErrDisputeBadRequest)
	}
	evidence, err := validateEvidence(in.Evidence)
	if err != nil {
		return nil, err
	}

	label, err := subject.Describe(ctx, userID, in.SubjectID)
	if err != nil {
		return nil, err
	}

	d := &models.Dispute{
		SubjectType: in.SubjectType,
		SubjectID:   in.SubjectID,
		UserID:      userID,
		Reason:      reason,
		Status:      models.DisputeStatusOpen,
	}
	if err := d.SetEvidence(evidence); err != nil {
		return nil, fmt.Errorf("%w: evidence", ErrDisputeBadRequest)
	}
	if err := s.db.WithContext(ctx).Create(d).Error; err != nil {
		// The unique index is the guard, not a pre-check: two taps race, and
		// only the database can settle that.
		if isDuplicateKeyErr(err) {
			return nil, ErrDisputeAlreadyExists
		}
		return nil, fmt.Errorf("persist dispute: %w", err)
	}
	s.publish(userID, "dispute.filed", "Dispute received",
		fmt.Sprintf("We are looking into your %s.", label), d)
	return d, nil
}

// Get returns one of the caller's own disputes.
func (s *DisputeService) Get(ctx context.Context, userID, id uuid.UUID) (*models.Dispute, error) {
	var d models.Dispute
	err := s.db.WithContext(ctx).First(&d, "id = ? AND user_id = ?", id, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrOrderDisputeNotFound
	}
	return &d, err
}

// ForSubject answers "have I already disputed this order?" — the question the
// app asks to decide between a File button and a link to the existing dispute.
func (s *DisputeService) ForSubject(ctx context.Context, userID uuid.UUID, subjectType string, subjectID uuid.UUID) (*models.Dispute, error) {
	var d models.Dispute
	err := s.db.WithContext(ctx).
		First(&d, "user_id = ? AND subject_type = ? AND subject_id = ?", userID, subjectType, subjectID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrOrderDisputeNotFound
	}
	return &d, err
}

// List returns the caller's disputes, newest first.
func (s *DisputeService) List(ctx context.Context, userID uuid.UUID, limit int) ([]models.Dispute, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	var rows []models.Dispute
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).
		Order("created_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ── Admin ─────────────────────────────────────────────────────────────────

// ListAdmin returns the review queue, newest first, with reporters resolved in
// one query rather than one per row.
func (s *DisputeService) ListAdmin(ctx context.Context, q DisputeQuery) ([]DisputeAdminRow, int64, error) {
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	query := s.db.WithContext(ctx).Model(&models.Dispute{})
	if st := strings.TrimSpace(q.Status); st != "" {
		query = query.Where("status = ?", st)
	}
	if t := strings.TrimSpace(q.SubjectType); t != "" {
		query = query.Where("subject_type = ?", t)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []models.Dispute
	if err := query.Order("created_at DESC").Limit(q.Limit).Offset(q.Offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	if len(rows) == 0 {
		return []DisputeAdminRow{}, total, nil
	}

	userIDs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		userIDs = append(userIDs, r.UserID)
	}
	var users []models.User
	if err := s.db.WithContext(ctx).
		Select("id", "name", "username", "email").
		Where("id IN ?", userIDs).Find(&users).Error; err != nil {
		return nil, 0, err
	}
	byID := make(map[uuid.UUID]*DisputeUser, len(users))
	for _, u := range users {
		byID[u.ID] = &DisputeUser{ID: u.ID, Name: u.Name, Username: u.Username, Email: u.Email}
	}

	labels := s.labelsFor(ctx, rows)

	out := make([]DisputeAdminRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, DisputeAdminRow{
			Dispute: r,
			User:    byID[r.UserID],
			Subject: labels[r.ID],
		})
	}
	return out, total, nil
}

// labelsFor describes each dispute's order, one query per subject TYPE present
// on the page rather than one per row. A row of "Bank ramp" plus a UUID is not
// reviewable: the operator would have to open every dispute to learn what the
// complaint is even about.
func (s *DisputeService) labelsFor(ctx context.Context, rows []models.Dispute) map[uuid.UUID]string {
	byType := map[string][]uuid.UUID{}
	for _, r := range rows {
		byType[r.SubjectType] = append(byType[r.SubjectType], r.SubjectID)
	}
	out := make(map[uuid.UUID]string, len(rows))
	for subjectType, ids := range byType {
		subject, ok := s.subjects[subjectType]
		if !ok {
			continue // a type whose stack is not wired on this instance
		}
		labels := subject.Labels(ctx, ids)
		for _, r := range rows {
			if r.SubjectType == subjectType {
				if l, found := labels[r.SubjectID]; found {
					out[r.ID] = l
				}
			}
		}
	}
	return out
}

// GetAdmin returns any dispute, with its reporter.
func (s *DisputeService) GetAdmin(ctx context.Context, id uuid.UUID) (*DisputeAdminRow, error) {
	var d models.Dispute
	if err := s.db.WithContext(ctx).First(&d, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrOrderDisputeNotFound
		}
		return nil, err
	}
	row := &DisputeAdminRow{Dispute: d, Subject: s.labelsFor(ctx, []models.Dispute{d})[d.ID]}
	var u models.User
	if err := s.db.WithContext(ctx).Select("id", "name", "username", "email").
		First(&u, "id = ?", d.UserID).Error; err == nil {
		row.User = &DisputeUser{ID: u.ID, Name: u.Name, Username: u.Username, Email: u.Email}
	}
	if d.SubjectType == models.DisputeSubjectPaycrestOrder && s.ramp != nil {
		if order, info, err := s.ramp.LegsAndOwedByOrderID(ctx, d.SubjectID); err == nil && order.Converted() {
			row.Ramp = info
		}
		// A lookup failure (order not converted, or gone) leaves Ramp nil —
		// the dispute detail must never fail to load over a ramp-only read.
	}
	return row, nil
}

// Escalate marks a dispute as handed to the third party who actually holds the
// money — Paycrest, Flutterwave — and records where it went.
//
// It is not a closing state: the dispute stays open to the user, and Resolve
// still applies once the provider answers.
func (s *DisputeService) Escalate(ctx context.Context, adminID, id uuid.UUID, externalRef, note string) (*models.Dispute, error) {
	externalRef = strings.TrimSpace(externalRef)
	if len(externalRef) > 128 {
		return nil, fmt.Errorf("%w: reference too long", ErrDisputeBadRequest)
	}
	if len(note) > disputeReasonMax {
		return nil, fmt.Errorf("%w: note too long", ErrDisputeBadRequest)
	}
	now := time.Now()
	// Conditional on the dispute still being LIVE, so a resolve landing in the
	// same instant wins rather than being reverted to escalated. Escalated is
	// included so a mistyped provider reference can be corrected — refusing
	// that left the queue carrying a thread id nobody could follow, and
	// reported it as "already closed" on a dispute that was plainly open.
	res := s.db.WithContext(ctx).Model(&models.Dispute{}).
		Where("id = ? AND status IN ?", id,
			[]string{models.DisputeStatusOpen, models.DisputeStatusEscalated}).
		Updates(map[string]any{
			"status":          models.DisputeStatusEscalated,
			"external_ref":    externalRef,
			"escalation_note": strings.TrimSpace(note),
			"escalated_by":    adminID,
			"escalated_at":    &now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, s.closedOrMissing(ctx, id)
	}
	d, err := s.byID(ctx, id)
	if err != nil {
		return nil, err
	}
	s.publish(d.UserID, "dispute.escalated", "Dispute escalated",
		"We have taken your case to the payment provider and will update you.", d)
	return d, nil
}

// Resolve closes a dispute as resolved or rejected.
func (s *DisputeService) Resolve(ctx context.Context, adminID, id uuid.UUID, status, resolution string) (*models.Dispute, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if !models.ValidDisputeResolution(status) {
		return nil, fmt.Errorf("%w: status must be resolved or rejected", ErrDisputeBadRequest)
	}
	resolution = strings.TrimSpace(resolution)
	if len(resolution) > disputeReasonMax {
		return nil, fmt.Errorf("%w: resolution too long", ErrDisputeBadRequest)
	}
	now := time.Now()
	res := s.db.WithContext(ctx).Model(&models.Dispute{}).
		Where("id = ? AND status IN ?", id,
			[]string{models.DisputeStatusOpen, models.DisputeStatusEscalated}).
		Updates(map[string]any{
			"status":      status,
			"resolved_by": adminID,
			"resolution":  resolution,
			"resolved_at": &now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, s.closedOrMissing(ctx, id)
	}
	d, err := s.byID(ctx, id)
	if err != nil {
		return nil, err
	}
	title := "Dispute resolved"
	msg := "Your dispute has been resolved. Open the app for details."
	if status == models.DisputeStatusRejected {
		title = "Dispute closed"
		msg = "We could not uphold your dispute. Open the app for the reason."
	}
	s.publish(d.UserID, "dispute."+status, title, msg, d)
	return d, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────

func (s *DisputeService) byID(ctx context.Context, id uuid.UUID) (*models.Dispute, error) {
	var d models.Dispute
	if err := s.db.WithContext(ctx).First(&d, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrOrderDisputeNotFound
		}
		return nil, err
	}
	return &d, nil
}

// closedOrMissing turns "the conditional update matched nothing" into the
// reason it matched nothing, so the caller can tell a stale id from a race.
func (s *DisputeService) closedOrMissing(ctx context.Context, id uuid.UUID) error {
	if _, err := s.byID(ctx, id); err != nil {
		return err
	}
	return ErrDisputeClosed
}

func (s *DisputeService) publish(userID uuid.UUID, kind, title, message string, d *models.Dispute) {
	if s.bus == nil || d == nil {
		return
	}
	s.bus.Publish(events.Event{
		Type: kind, UserID: userID, Title: title, Message: message,
		Data: map[string]interface{}{
			"disputeId":   d.ID.String(),
			"subjectType": d.SubjectType,
			"subjectId":   d.SubjectID.String(),
			"status":      d.Status,
		},
	})
}
