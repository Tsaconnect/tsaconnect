package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var (
	ErrBillAlreadyReported = errors.New("this order has already been reported")
	ErrBillNotReportable   = errors.New("only a paid order can be reported")
	ErrBillReportNotFound  = errors.New("bill report not found")
	ErrBillReportBadState  = errors.New("this report has already been resolved")
	// ErrBillDisputesUnavailable means the bills stack was built without the
	// dispute service. Reporting is dormant rather than silently writing to a
	// table nothing reads.
	ErrBillDisputesUnavailable = errors.New("reporting is unavailable")
)

// reportableBillStatuses: money moved (or tried to), so there is something to
// complain about. Unpaid quotes and expired quotes are excluded.
var reportableBillStatuses = []string{
	models.BillStatusPaying, models.BillStatusPaid, models.BillStatusVending,
	models.BillStatusDelivered, models.BillStatusVendFail,
	models.BillStatusRefunded, models.BillStatusFailed,
}

// ReportOrder files a complaint against one of the caller's own bill orders.
//
// The storage moved to the generic disputes table so bills, ramp orders and
// instant trades land in ONE operator queue instead of three. This function
// stays, with its old name, route and response shape, because app builds
// already in users' hands call it — they must keep working across the release
// rather than 404ing until everyone updates.
func (s *BillService) ReportOrder(ctx context.Context, userID, orderID uuid.UUID, reason string) (*models.BillReport, error) {
	if s.disputes == nil {
		return nil, ErrBillDisputesUnavailable
	}
	d, err := s.disputes.File(ctx, userID, DisputeInput{
		SubjectType: models.DisputeSubjectBillOrder,
		SubjectID:   orderID,
		Reason:      reason,
	})
	if err != nil {
		return nil, billReportErr(err)
	}
	return billReportView(d), nil
}

// billReportErr maps the generic dispute sentinels back onto the bill ones the
// bills handler already translates into HTTP statuses and user-facing copy.
func billReportErr(err error) error {
	switch {
	case errors.Is(err, ErrDisputeAlreadyExists):
		return ErrBillAlreadyReported
	case errors.Is(err, ErrDisputeNotDisputable):
		return ErrBillNotReportable
	case errors.Is(err, ErrDisputeSubjectNotFound):
		return ErrBillNotFound
	case errors.Is(err, ErrOrderDisputeNotFound):
		return ErrBillReportNotFound
	case errors.Is(err, ErrDisputeClosed):
		return ErrBillReportBadState
	case errors.Is(err, ErrDisputeBadRequest):
		return fmt.Errorf("%w: %s", ErrBillBadRequest, strings.TrimPrefix(err.Error(), "invalid dispute request: "))
	}
	return err
}

// billReportView projects a Dispute into the BillReport shape the app and the
// existing admin console already parse. Escalated has no equivalent in the old
// vocabulary and reads as still-open, which is what it is to the user.
func billReportView(d *models.Dispute) *models.BillReport {
	if d == nil {
		return nil
	}
	status := d.Status
	if status == models.DisputeStatusEscalated {
		status = models.BillReportStatusOpen
	}
	return &models.BillReport{
		ID:         d.ID,
		OrderID:    d.SubjectID,
		UserID:     d.UserID,
		Reason:     d.Reason,
		Status:     status,
		ResolvedBy: d.ResolvedBy,
		Resolution: d.Resolution,
		ResolvedAt: d.ResolvedAt,
		CreatedAt:  d.CreatedAt,
		UpdatedAt:  d.UpdatedAt,
	}
}

// GetOrderReport returns the caller's report on an order, if any.
func (s *BillService) GetOrderReport(ctx context.Context, userID, orderID uuid.UUID) (*models.BillReport, error) {
	if s.disputes == nil {
		return nil, ErrBillDisputesUnavailable
	}
	d, err := s.disputes.ForSubject(ctx, userID, models.DisputeSubjectBillOrder, orderID)
	if err != nil {
		return nil, billReportErr(err)
	}
	return billReportView(d), nil
}

// BillReportUser is the slim identity shown to admins next to a report —
// deliberately not the full models.User.
type BillReportUser struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Username string    `json:"username"`
	Email    string    `json:"email"`
}

// BillReportAdminRow is one report with its order and reporter attached.
type BillReportAdminRow struct {
	Report models.BillReport `json:"report"`
	Order  *models.BillOrder `json:"order,omitempty"`
	User   *BillReportUser   `json:"user,omitempty"`
}

// ListReportsAdmin returns bill disputes in the old admin shape.
//
// Kept so the existing bill-reports console page keeps working until it is
// replaced by the unified disputes queue; it simply filters the shared table
// down to bill orders.
func (s *BillService) ListReportsAdmin(ctx context.Context, status string, limit, offset int) ([]BillReportAdminRow, int64, error) {
	if s.disputes == nil {
		return nil, 0, ErrBillDisputesUnavailable
	}
	rows, total, err := s.disputes.ListAdmin(ctx, DisputeQuery{
		Status:      strings.TrimSpace(status),
		SubjectType: models.DisputeSubjectBillOrder,
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		return nil, 0, err
	}
	if len(rows) == 0 {
		return []BillReportAdminRow{}, total, nil
	}

	orderIDs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		orderIDs = append(orderIDs, r.Dispute.SubjectID)
	}
	var orders []models.BillOrder
	if err := s.db.WithContext(ctx).Where("id IN ?", orderIDs).Find(&orders).Error; err != nil {
		return nil, 0, err
	}
	ordersByID := make(map[uuid.UUID]*models.BillOrder, len(orders))
	for i := range orders {
		// The meter token / exam PIN is redeemable value the review UI never
		// shows — keep it out of the admin payload entirely.
		orders[i].DeliveryPayload = ""
		ordersByID[orders[i].ID] = &orders[i]
	}

	out := make([]BillReportAdminRow, 0, len(rows))
	for _, r := range rows {
		row := BillReportAdminRow{
			Report: *billReportView(&r.Dispute),
			Order:  ordersByID[r.Dispute.SubjectID],
		}
		if r.User != nil {
			row.User = &BillReportUser{ID: r.User.ID, Name: r.User.Name, Username: r.User.Username, Email: r.User.Email}
		}
		out = append(out, row)
	}
	return out, total, nil
}

// ResolveReport closes an open bill dispute, in the old admin vocabulary.
func (s *BillService) ResolveReport(ctx context.Context, adminID, reportID uuid.UUID, status, resolution string) (*models.BillReport, error) {
	if s.disputes == nil {
		return nil, ErrBillDisputesUnavailable
	}
	if !models.ValidBillReportResolution(strings.ToLower(strings.TrimSpace(status))) {
		return nil, fmt.Errorf("%w: status must be resolved or rejected", ErrBillBadRequest)
	}
	d, err := s.disputes.Resolve(ctx, adminID, reportID, status, resolution)
	if err != nil {
		return nil, billReportErr(err)
	}
	return billReportView(d), nil
}

// isDuplicateKeyErr matches unique-violation errors across Postgres (SQLSTATE
// 23505) and the SQLite test rig, same as the handlers-layer helper. Used
// wherever a unique index is the guard rather than a pre-check.
func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "23505")
}
