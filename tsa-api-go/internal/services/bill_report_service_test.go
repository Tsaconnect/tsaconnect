package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// paidOrder stages a quote and forces it into the given status so it is (or
// isn't) reportable without walking the whole pay/vend flow.
func (r *billRig) orderInStatus(t *testing.T, status string) *models.BillOrder {
	t.Helper()
	q := r.airtimeQuote(t)
	if err := r.db.Model(&models.BillOrder{}).
		Where("id = ?", q.Order.ID).Update("status", status).Error; err != nil {
		t.Fatalf("force status: %v", err)
	}
	q.Order.Status = status
	return q.Order
}

func TestBillReportOrder(t *testing.T) {
	r := newBillServiceRig(t)
	order := r.orderInStatus(t, models.BillStatusDelivered)

	report, err := r.svc.ReportOrder(context.Background(), r.userID, order.ID, "  No airtime received  ")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if report.Status != models.BillReportStatusOpen {
		t.Fatalf("status = %q, want open", report.Status)
	}
	if report.Reason != "No airtime received" {
		t.Fatalf("reason = %q, want trimmed", report.Reason)
	}
	if report.OrderID != order.ID || report.UserID != r.userID {
		t.Fatalf("report keys wrong: %+v", report)
	}
}

func TestBillReportOrderTwiceConflicts(t *testing.T) {
	r := newBillServiceRig(t)
	order := r.orderInStatus(t, models.BillStatusFailed)

	if _, err := r.svc.ReportOrder(context.Background(), r.userID, order.ID, "first"); err != nil {
		t.Fatalf("first report: %v", err)
	}
	_, err := r.svc.ReportOrder(context.Background(), r.userID, order.ID, "second")
	if !errors.Is(err, ErrBillAlreadyReported) {
		t.Fatalf("err = %v, want ErrBillAlreadyReported", err)
	}
}

func TestBillReportUnpaidOrderRejected(t *testing.T) {
	r := newBillServiceRig(t)
	q := r.airtimeQuote(t) // still 'quoted' — no money moved

	_, err := r.svc.ReportOrder(context.Background(), r.userID, q.Order.ID, "why")
	if !errors.Is(err, ErrBillNotReportable) {
		t.Fatalf("err = %v, want ErrBillNotReportable", err)
	}
}

func TestBillReportSomeoneElsesOrder(t *testing.T) {
	r := newBillServiceRig(t)
	order := r.orderInStatus(t, models.BillStatusDelivered)

	_, err := r.svc.ReportOrder(context.Background(), uuid.New(), order.ID, "not mine")
	if !errors.Is(err, ErrBillNotFound) {
		t.Fatalf("err = %v, want ErrBillNotFound", err)
	}
}

func TestBillReportReasonValidation(t *testing.T) {
	r := newBillServiceRig(t)
	order := r.orderInStatus(t, models.BillStatusDelivered)
	ctx := context.Background()

	if _, err := r.svc.ReportOrder(ctx, r.userID, order.ID, "   "); !errors.Is(err, ErrBillBadRequest) {
		t.Fatalf("blank reason err = %v, want ErrBillBadRequest", err)
	}
	if _, err := r.svc.ReportOrder(ctx, r.userID, order.ID, strings.Repeat("x", 1001)); !errors.Is(err, ErrBillBadRequest) {
		t.Fatalf("long reason err = %v, want ErrBillBadRequest", err)
	}
}

func TestBillGetOrderReport(t *testing.T) {
	r := newBillServiceRig(t)
	order := r.orderInStatus(t, models.BillStatusDelivered)
	ctx := context.Background()

	if _, err := r.svc.GetOrderReport(ctx, r.userID, order.ID); !errors.Is(err, ErrBillReportNotFound) {
		t.Fatalf("err = %v, want ErrBillReportNotFound", err)
	}
	created, err := r.svc.ReportOrder(ctx, r.userID, order.ID, "nothing arrived")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	got, err := r.svc.GetOrderReport(ctx, r.userID, order.ID)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("got report %s, want %s", got.ID, created.ID)
	}
	// Another user must not see it.
	if _, err := r.svc.GetOrderReport(ctx, uuid.New(), order.ID); !errors.Is(err, ErrBillReportNotFound) {
		t.Fatalf("cross-user err = %v, want ErrBillReportNotFound", err)
	}
}

func TestBillListReportsAdmin(t *testing.T) {
	r := newBillServiceRig(t)
	ctx := context.Background()
	if err := r.db.Exec(
		`INSERT INTO users (id, name, username, email) VALUES (?, ?, ?, ?)`,
		r.userID.String(), "Ada Obi", "ada", "ada@example.com").Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	order := r.orderInStatus(t, models.BillStatusDelivered)
	if err := r.db.Model(&models.BillOrder{}).
		Where("id = ?", order.ID).Update("delivery_payload", "1234-5678-9012").Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	report, err := r.svc.ReportOrder(ctx, r.userID, order.ID, "no value")
	if err != nil {
		t.Fatalf("report: %v", err)
	}

	rows, total, err := r.svc.ListReportsAdmin(ctx, models.BillReportStatusOpen, 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("total=%d rows=%d, want 1/1", total, len(rows))
	}
	row := rows[0]
	if row.Report.ID != report.ID {
		t.Fatalf("report id mismatch")
	}
	if row.Order == nil || row.Order.ID != order.ID {
		t.Fatalf("order not attached: %+v", row.Order)
	}
	if row.Order.DeliveryPayload != "" {
		t.Fatalf("delivery payload leaked to admin row: %q", row.Order.DeliveryPayload)
	}
	if row.User == nil || row.User.Username != "ada" || row.User.Email != "ada@example.com" {
		t.Fatalf("user not attached: %+v", row.User)
	}

	// Resolved filter finds nothing while the report is open.
	if _, total, err := r.svc.ListReportsAdmin(ctx, models.BillReportStatusResolved, 50, 0); err != nil || total != 0 {
		t.Fatalf("resolved filter total=%d err=%v, want 0/nil", total, err)
	}
}

func TestBillResolveReport(t *testing.T) {
	r := newBillServiceRig(t)
	ctx := context.Background()
	order := r.orderInStatus(t, models.BillStatusVendFail)
	report, err := r.svc.ReportOrder(ctx, r.userID, order.ID, "debited, no vend")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	adminID := uuid.New()

	resolved, err := r.svc.ResolveReport(ctx, adminID, report.ID, "Resolved", "MCGP refunded on-chain")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Status != models.BillReportStatusResolved {
		t.Fatalf("status = %q, want resolved", resolved.Status)
	}
	if resolved.ResolvedBy == nil || *resolved.ResolvedBy != adminID {
		t.Fatalf("resolvedBy = %v, want %s", resolved.ResolvedBy, adminID)
	}
	if resolved.ResolvedAt == nil || resolved.Resolution != "MCGP refunded on-chain" {
		t.Fatalf("resolution fields not set: %+v", resolved)
	}

	// A second resolution attempt conflicts.
	if _, err := r.svc.ResolveReport(ctx, adminID, report.ID, "rejected", ""); !errors.Is(err, ErrBillReportBadState) {
		t.Fatalf("re-resolve err = %v, want ErrBillReportBadState", err)
	}
}

func TestBillResolveReportValidation(t *testing.T) {
	r := newBillServiceRig(t)
	ctx := context.Background()

	if _, err := r.svc.ResolveReport(ctx, uuid.New(), uuid.New(), "open", ""); !errors.Is(err, ErrBillBadRequest) {
		t.Fatalf("open status err = %v, want ErrBillBadRequest", err)
	}
	if _, err := r.svc.ResolveReport(ctx, uuid.New(), uuid.New(), "resolved", ""); !errors.Is(err, ErrBillReportNotFound) {
		t.Fatalf("missing report err = %v, want ErrBillReportNotFound", err)
	}
}
