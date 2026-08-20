package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var (
	ErrBillAlreadyReported = errors.New("this order has already been reported")
	ErrBillNotReportable   = errors.New("only a paid order can be reported")
	ErrBillReportNotFound  = errors.New("bill report not found")
	ErrBillReportBadState  = errors.New("this report has already been resolved")
)

const billReportReasonMax = 1000

// reportableBillStatuses: money moved (or tried to), so there is something to
// complain about. Unpaid quotes and expired quotes are excluded.
var reportableBillStatuses = []string{
	models.BillStatusPaying, models.BillStatusPaid, models.BillStatusVending,
	models.BillStatusDelivered, models.BillStatusVendFail,
	models.BillStatusRefunded, models.BillStatusFailed,
}

// ReportOrder files a complaint against one of the caller's own bill orders.
func (s *BillService) ReportOrder(ctx context.Context, userID, orderID uuid.UUID, reason string) (*models.BillReport, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, fmt.Errorf("%w: reason required", ErrBillBadRequest)
	}
	if len(reason) > billReportReasonMax {
		return nil, fmt.Errorf("%w: reason too long", ErrBillBadRequest)
	}

	var order models.BillOrder
	if err := s.db.WithContext(ctx).First(&order, "id = ? AND user_id = ?", orderID, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBillNotFound
		}
		return nil, err
	}
	reportable := false
	for _, st := range reportableBillStatuses {
		if order.Status == st {
			reportable = true
			break
		}
	}
	if !reportable {
		return nil, ErrBillNotReportable
	}

	report := &models.BillReport{
		OrderID: order.ID,
		UserID:  userID,
		Reason:  reason,
		Status:  models.BillReportStatusOpen,
	}
	if err := s.db.WithContext(ctx).Create(report).Error; err != nil {
		if isBillDuplicateKeyErr(err) {
			return nil, ErrBillAlreadyReported
		}
		return nil, fmt.Errorf("persist bill report: %w", err)
	}
	return report, nil
}

// GetOrderReport returns the caller's report on an order, if any.
func (s *BillService) GetOrderReport(ctx context.Context, userID, orderID uuid.UUID) (*models.BillReport, error) {
	var report models.BillReport
	if err := s.db.WithContext(ctx).
		First(&report, "order_id = ? AND user_id = ?", orderID, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBillReportNotFound
		}
		return nil, err
	}
	return &report, nil
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

// ListReportsAdmin returns reports (optionally filtered by status), newest
// first, with orders and reporter identities batch-resolved.
func (s *BillService) ListReportsAdmin(ctx context.Context, status string, limit, offset int) ([]BillReportAdminRow, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.BillReport{})
	if st := strings.TrimSpace(status); st != "" {
		q = q.Where("status = ?", st)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var reports []models.BillReport
	if err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&reports).Error; err != nil {
		return nil, 0, err
	}
	if len(reports) == 0 {
		return []BillReportAdminRow{}, total, nil
	}

	orderIDs := make([]uuid.UUID, 0, len(reports))
	userIDs := make([]uuid.UUID, 0, len(reports))
	for _, r := range reports {
		orderIDs = append(orderIDs, r.OrderID)
		userIDs = append(userIDs, r.UserID)
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
	var users []models.User
	if err := s.db.WithContext(ctx).
		Select("id", "name", "username", "email").
		Where("id IN ?", userIDs).Find(&users).Error; err != nil {
		return nil, 0, err
	}
	usersByID := make(map[uuid.UUID]*BillReportUser, len(users))
	for _, u := range users {
		usersByID[u.ID] = &BillReportUser{ID: u.ID, Name: u.Name, Username: u.Username, Email: u.Email}
	}

	rows := make([]BillReportAdminRow, 0, len(reports))
	for _, r := range reports {
		rows = append(rows, BillReportAdminRow{
			Report: r,
			Order:  ordersByID[r.OrderID],
			User:   usersByID[r.UserID],
		})
	}
	return rows, total, nil
}

// ResolveReport closes an open report as resolved or rejected. The update is
// conditional on the report still being open, so two admins can't both win.
func (s *BillService) ResolveReport(ctx context.Context, adminID, reportID uuid.UUID, status, resolution string) (*models.BillReport, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if !models.ValidBillReportResolution(status) {
		return nil, fmt.Errorf("%w: status must be resolved or rejected", ErrBillBadRequest)
	}
	resolution = strings.TrimSpace(resolution)
	if len(resolution) > billReportReasonMax {
		return nil, fmt.Errorf("%w: resolution too long", ErrBillBadRequest)
	}

	var report models.BillReport
	if err := s.db.WithContext(ctx).First(&report, "id = ?", reportID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBillReportNotFound
		}
		return nil, err
	}
	if report.Status != models.BillReportStatusOpen {
		return nil, ErrBillReportBadState
	}

	now := time.Now()
	res := s.db.WithContext(ctx).Model(&models.BillReport{}).
		Where("id = ? AND status = ?", reportID, models.BillReportStatusOpen).
		Updates(map[string]interface{}{
			"status":      status,
			"resolved_by": adminID,
			"resolution":  resolution,
			"resolved_at": now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrBillReportBadState
	}
	if err := s.db.WithContext(ctx).First(&report, "id = ?", reportID).Error; err != nil {
		return nil, err
	}
	return &report, nil
}

// isBillDuplicateKeyErr matches unique-violation errors across Postgres
// (SQLSTATE 23505) and the SQLite test rig, same as the handlers-layer helper.
func isBillDuplicateKeyErr(err error) bool {
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
