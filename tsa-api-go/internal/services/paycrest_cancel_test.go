package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// newCancelTestService builds a service whose client panics if reached —
// cancelling is entirely local (Paycrest has no cancel endpoint: see the
// PaycrestClient doc comment), so any provider call here is a bug.
func newCancelTestService(t *testing.T) (*PaycrestOrderService, *gorm.DB) {
	t.Helper()
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatalf("migrate paycrest_order_records: %v", err)
	}
	svc := NewPaycrestOrderService(db, stubPaycrestOrderer{},
		NewPaycrestQuoteService(stubPaycrestQuoter{}, nil, nil), nil, "", "")
	return svc, db
}

// seedOrder inserts one order in the given state.
func seedOrder(t *testing.T, db *gorm.DB, userID uuid.UUID, direction, status string) *models.PaycrestOrderRecord {
	t.Helper()
	row := &models.PaycrestOrderRecord{
		ID:            uuid.New(),
		UserID:        userID,
		Direction:     direction,
		Status:        status,
		Reference:     "ref-" + uuid.New().String(),
		PaycrestID:    "pc-" + uuid.New().String(),
		FiatCode:      "NGN",
		FiatAmount:    "1000",
		Token:         "USDT",
		ChainKey:      "base",
		Network:       "base",
		WalletAddress: "0xabc",
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return row
}

// TestCancel_FreesTheOneOpenOrderLock is the whole point of the feature. A
// user who changes their mind is otherwise locked out of the direction until
// validUntil passes and the poller marks the order expired — up to half an
// hour of being unable to start the order they actually want.
func TestCancel_FreesTheOneOpenOrderLock(t *testing.T) {
	svc, db := newCancelTestService(t)
	user := uuid.New()
	row := seedOrder(t, db, user, models.PaycrestDirectionBuy, models.PaycrestStatusAwaitingPayment)

	if err := svc.assertNoOpenOrder(context.Background(), user, models.PaycrestDirectionBuy); !errors.Is(err, ErrPaycrestOrderOpen) {
		t.Fatalf("precondition: expected the seeded order to hold the lock, got %v", err)
	}

	got, err := svc.Cancel(context.Background(), user, row.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got.Status != models.PaycrestStatusCancelled {
		t.Errorf("status = %q, want %q", got.Status, models.PaycrestStatusCancelled)
	}
	if err := svc.assertNoOpenOrder(context.Background(), user, models.PaycrestDirectionBuy); err != nil {
		t.Errorf("lock still held after cancel: %v", err)
	}
}

func TestCancel_SellAwaitingDeposit(t *testing.T) {
	svc, db := newCancelTestService(t)
	user := uuid.New()
	row := seedOrder(t, db, user, models.PaycrestDirectionSell, models.PaycrestStatusAwaitingDeposit)

	if _, err := svc.Cancel(context.Background(), user, row.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := svc.assertNoOpenOrder(context.Background(), user, models.PaycrestDirectionSell); err != nil {
		t.Errorf("lock still held after cancel: %v", err)
	}
}

// TestCancel_RefusesOnceThePaymentIsClaimed guards the stranded-funds path.
// Someone who has tapped "I've sent it" may have money in flight to a provider
// address; letting them cancel would tell them the order is dead while the
// deposit is still on its way to it.
func TestCancel_RefusesOnceThePaymentIsClaimed(t *testing.T) {
	svc, db := newCancelTestService(t)
	user := uuid.New()
	row := seedOrder(t, db, user, models.PaycrestDirectionSell, models.PaycrestStatusAwaitingDeposit)
	now := time.Now()
	if err := db.Model(row).Update("payment_claimed_at", &now).Error; err != nil {
		t.Fatalf("set claim: %v", err)
	}

	_, err := svc.Cancel(context.Background(), user, row.ID)
	if !errors.Is(err, ErrPaycrestNotCancellable) {
		t.Fatalf("err = %v, want ErrPaycrestNotCancellable", err)
	}

	var after models.PaycrestOrderRecord
	db.First(&after, "id = ?", row.ID)
	if after.Status != models.PaycrestStatusAwaitingDeposit {
		t.Errorf("status changed to %q on a refused cancel", after.Status)
	}
}

// TestCancel_RefusesOnceMoneyHasMoved covers the states where the provider has
// already seen funds or delivered.
func TestCancel_RefusesOnceMoneyHasMoved(t *testing.T) {
	for _, status := range []string{
		models.PaycrestStatusPending,
		models.PaycrestStatusSettling,
		models.PaycrestStatusSettled,
		models.PaycrestStatusExpired,
		models.PaycrestStatusUnmatched,
	} {
		t.Run(status, func(t *testing.T) {
			svc, db := newCancelTestService(t)
			user := uuid.New()
			row := seedOrder(t, db, user, models.PaycrestDirectionBuy, status)

			if _, err := svc.Cancel(context.Background(), user, row.ID); !errors.Is(err, ErrPaycrestNotCancellable) {
				t.Fatalf("err = %v, want ErrPaycrestNotCancellable", err)
			}
		})
	}
}

func TestCancel_RefusesAnotherUsersOrder(t *testing.T) {
	svc, db := newCancelTestService(t)
	owner := uuid.New()
	row := seedOrder(t, db, owner, models.PaycrestDirectionBuy, models.PaycrestStatusAwaitingPayment)

	if _, err := svc.Cancel(context.Background(), uuid.New(), row.ID); !errors.Is(err, ErrPaycrestOrderNotYours) {
		t.Fatalf("err = %v, want ErrPaycrestOrderNotYours", err)
	}
}

// TestCancel_SecondCallIsRefused: the conditional UPDATE means a caller that
// loses the race — a second tap, or a poller sync landing in the same instant
// — is told the order is no longer cancellable rather than silently "winning".
func TestCancel_SecondCallIsRefused(t *testing.T) {
	svc, db := newCancelTestService(t)
	user := uuid.New()
	row := seedOrder(t, db, user, models.PaycrestDirectionBuy, models.PaycrestStatusAwaitingPayment)

	if _, err := svc.Cancel(context.Background(), user, row.ID); err != nil {
		t.Fatalf("first Cancel: %v", err)
	}
	if _, err := svc.Cancel(context.Background(), user, row.ID); !errors.Is(err, ErrPaycrestNotCancellable) {
		t.Fatalf("second Cancel err = %v, want ErrPaycrestNotCancellable", err)
	}
}

// TestCancel_PollerLeavesCancelledOrdersAlone is the regression that would
// otherwise re-lock the user: the poller reads Paycrest's copy, which is still
// live (they have no cancel endpoint), and writes its status back over ours.
func TestCancel_PollerLeavesCancelledOrdersAlone(t *testing.T) {
	svc, db := newCancelTestService(t)
	user := uuid.New()
	row := seedOrder(t, db, user, models.PaycrestDirectionBuy, models.PaycrestStatusAwaitingPayment)

	if _, err := svc.Cancel(context.Background(), user, row.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	due, err := svc.DueForSync(context.Background(), 50)
	if err != nil {
		t.Fatalf("DueForSync: %v", err)
	}
	for _, r := range due {
		if r.ID == row.ID {
			t.Fatal("a cancelled order was handed to the poller; syncing it would overwrite our status")
		}
	}
	if !models.PaycrestOrderTerminal(models.PaycrestStatusCancelled) {
		t.Error("PaycrestOrderTerminal(cancelled) = false; Sync would still refresh it")
	}
}

// A payment's open order is not in her order list, so the lock names it as a
// payment — while still being the one-open-order lock every caller checks for.
func TestAssertNoOpenOrder_APaymentsOrderIsNamedAsAPayment(t *testing.T) {
	svc, db := newCancelTestService(t)
	user := uuid.New()
	pay := seedOrder(t, db, user, models.PaycrestDirectionSell, models.PaycrestStatusAwaitingDeposit)
	payment := uuid.New()
	db.Model(pay).Update("pay_payment_id", payment)

	err := svc.assertNoOpenOrder(context.Background(), user, models.PaycrestDirectionSell)
	if !errors.Is(err, ErrPaycrestPayOrderOpen) || !errors.Is(err, ErrPaycrestOrderOpen) {
		t.Fatalf("err = %v, want the payment's lock, which is still the open-order lock", err)
	}

	// An ordinary order open alongside it is the one she can find and cancel.
	seedOrder(t, db, user, models.PaycrestDirectionSell, models.PaycrestStatusPending)
	if err := svc.assertNoOpenOrder(context.Background(), user, models.PaycrestDirectionSell); !errors.Is(err, ErrPaycrestOrderOpen) || errors.Is(err, ErrPaycrestPayOrderOpen) {
		t.Fatalf("err = %v, want the ordinary open-order lock", err)
	}
}
