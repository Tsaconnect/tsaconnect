package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// The subject resolvers. Each answers the same questions for one kind of order
// — is it the caller's, has money moved, and what is it called — and nothing
// else. Keeping them here, together and small, is what stops per-type knowledge
// leaking back into the generic dispute flow.
//
// A missing order and someone else's order both return
// ErrDisputeSubjectNotFound: telling those apart would let anyone probe which
// order ids exist.

// subjectResolver builds a DisputeSubject from the two per-type pieces: the
// ownership-and-state check used when filing, and the batch describe used by
// the admin queue.
type subjectResolver struct {
	describe func(ctx context.Context, userID, subjectID uuid.UUID) (string, error)
	labels   func(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]string
}

func (r subjectResolver) Describe(ctx context.Context, userID, subjectID uuid.UUID) (string, error) {
	return r.describe(ctx, userID, subjectID)
}

func (r subjectResolver) Labels(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]string {
	return r.labels(ctx, ids)
}

// paycrestPreMoneyStatuses are the states where nothing has been sent yet.
// Disputing one of these is meaningless — cancelling is the right action, and
// putting an operator in the loop instead would waste their time and the
// user's.
var paycrestPreMoneyStatuses = map[string]bool{
	models.PaycrestStatusAwaitingPayment: true,
	models.PaycrestStatusAwaitingDeposit: true,
	models.PaycrestStatusCancelled:       true,
}

func paycrestLabel(row *models.PaycrestOrderRecord) string {
	return fmt.Sprintf("%s %s %s (%s)",
		row.Direction, row.FiatAmount, row.FiatCode, row.Reference)
}

// PaycrestDisputeSubject resolves ramp orders.
func PaycrestDisputeSubject(db *gorm.DB) DisputeSubject {
	return subjectResolver{
		describe: func(ctx context.Context, userID, subjectID uuid.UUID) (string, error) {
			var row models.PaycrestOrderRecord
			err := db.WithContext(ctx).First(&row, "id = ? AND user_id = ?", subjectID, userID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", ErrDisputeSubjectNotFound
			}
			if err != nil {
				return "", err
			}
			if paycrestPreMoneyStatuses[row.Status] {
				return "", ErrDisputeNotDisputable
			}
			return paycrestLabel(&row), nil
		},
		labels: func(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]string {
			out := map[uuid.UUID]string{}
			var rows []models.PaycrestOrderRecord
			if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
				return out // a label is a convenience; the queue must still render
			}
			for i := range rows {
				out[rows[i].ID] = paycrestLabel(&rows[i]) + " · " + rows[i].Status
			}
			return out
		},
	}
}

// itPreMoneyStatuses mirrors the Paycrest set for instant-trade intents: a
// quote nobody has funded, and one the user already cancelled.
var itPreMoneyStatuses = map[string]bool{
	models.ITStatusPendingPayment: true,
	models.ITStatusPendingDeposit: true,
	models.ITStatusCancelled:      true,
}

func itLabel(row *models.ITIntent) string {
	return fmt.Sprintf("%s %s", row.Direction, row.Asset)
}

// ITDisputeSubject resolves instant-trade intents.
func ITDisputeSubject(db *gorm.DB) DisputeSubject {
	return subjectResolver{
		describe: func(ctx context.Context, userID, subjectID uuid.UUID) (string, error) {
			var row models.ITIntent
			err := db.WithContext(ctx).First(&row, "id = ? AND user_id = ?", subjectID, userID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", ErrDisputeSubjectNotFound
			}
			if err != nil {
				return "", err
			}
			if itPreMoneyStatuses[row.Status] {
				return "", ErrDisputeNotDisputable
			}
			return itLabel(&row), nil
		},
		labels: func(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]string {
			out := map[uuid.UUID]string{}
			var rows []models.ITIntent
			if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
				return out
			}
			for i := range rows {
				out[rows[i].ID] = itLabel(&rows[i]) + " · " + rows[i].Status
			}
			return out
		},
	}
}

func billLabel(row *models.BillOrder) string {
	// The recipient (meter number, phone) is deliberately absent: the label is
	// a queue summary, and the detail view already carries the order id an
	// operator can look the rest up from.
	return fmt.Sprintf("%s · %s · ₦%d", row.Category, row.Provider, row.NgnAmount)
}

// BillDisputeSubject resolves bill orders, reusing the reportable-status list
// the bills stack already defined so the rule does not fork.
func BillDisputeSubject(db *gorm.DB) DisputeSubject {
	return subjectResolver{
		describe: func(ctx context.Context, userID, subjectID uuid.UUID) (string, error) {
			var row models.BillOrder
			err := db.WithContext(ctx).First(&row, "id = ? AND user_id = ?", subjectID, userID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", ErrDisputeSubjectNotFound
			}
			if err != nil {
				return "", err
			}
			disputable := false
			for _, st := range reportableBillStatuses {
				if row.Status == st {
					disputable = true
					break
				}
			}
			if !disputable {
				return "", ErrDisputeNotDisputable
			}
			return billLabel(&row), nil
		},
		labels: func(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]string {
			out := map[uuid.UUID]string{}
			var rows []models.BillOrder
			if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
				return out
			}
			for i := range rows {
				out[rows[i].ID] = billLabel(&rows[i]) + " · " + rows[i].Status
			}
			return out
		},
	}
}
