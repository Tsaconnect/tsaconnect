package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// seedPM inserts a payment_merchants row in the given status and returns it.
func seedPM(t *testing.T, db *gorm.DB, status string, accountRef string) *models.PaymentMerchant {
	t.Helper()
	pm := &models.PaymentMerchant{
		ID:                      uuid.New(),
		UserID:                  uuid.New(),
		Name:                    "Acme Corp",
		Status:                  status,
		ProvisioningStatus:      models.PMProvisioningPending,
		NombaAccountRef:         accountRef,
		SettlementBankCode:      "058",
		SettlementAccountNumber: "1234567890",
		OperatorWalletAddress:   "0xDeAdBeEf",
		PerTradeCapUSD:          500,
		DailyCapUSD:             10000,
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed pm: %v", err)
	}
	return pm
}

func TestRejectPM(t *testing.T) {
	ctx := context.Background()
	admin := uuid.New()

	t.Run("pending → rejected records note + reviewer", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		svc := NewPMService(db)
		pm := seedPM(t, db, models.PaymentMerchantStatusPending, "")

		got, err := svc.RejectPM(ctx, pm.ID, admin, "  bad docs  ")
		if err != nil {
			t.Fatalf("RejectPM: %v", err)
		}
		if got.Status != models.PaymentMerchantStatusRejected {
			t.Fatalf("status = %q, want rejected", got.Status)
		}
		if got.ReviewNote != "bad docs" {
			t.Fatalf("note = %q, want trimmed 'bad docs'", got.ReviewNote)
		}
		if got.ReviewedBy != admin {
			t.Fatalf("reviewedBy = %v, want %v", got.ReviewedBy, admin)
		}
		if got.ReviewedAt == nil {
			t.Fatal("reviewedAt not set")
		}
	})

	t.Run("blank note rejected", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		svc := NewPMService(db)
		pm := seedPM(t, db, models.PaymentMerchantStatusPending, "")
		if _, err := svc.RejectPM(ctx, pm.ID, admin, "   "); !errors.Is(err, ErrReviewNoteRequired) {
			t.Fatalf("err = %v, want ErrReviewNoteRequired", err)
		}
	})

	t.Run("non-pending rejected with ErrInvalidPMTransition", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		svc := NewPMService(db)
		pm := seedPM(t, db, models.PaymentMerchantStatusActive, "pm-x")
		if _, err := svc.RejectPM(ctx, pm.ID, admin, "note"); !errors.Is(err, ErrInvalidPMTransition) {
			t.Fatalf("err = %v, want ErrInvalidPMTransition", err)
		}
	})

	t.Run("missing PM → ErrPMNotFound", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		svc := NewPMService(db)
		if _, err := svc.RejectPM(ctx, uuid.New(), admin, "note"); !errors.Is(err, ErrPMNotFound) {
			t.Fatalf("err = %v, want ErrPMNotFound", err)
		}
	})
}

func TestPauseResumePM(t *testing.T) {
	ctx := context.Background()
	admin := uuid.New()

	t.Run("active → paused → active", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		svc := NewPMService(db)
		pm := seedPM(t, db, models.PaymentMerchantStatusActive, "pm-x")

		paused, err := svc.PausePM(ctx, pm.ID, admin)
		if err != nil {
			t.Fatalf("PausePM: %v", err)
		}
		if paused.Status != models.PaymentMerchantStatusPaused {
			t.Fatalf("status = %q, want paused", paused.Status)
		}

		resumed, err := svc.ResumePM(ctx, pm.ID, admin)
		if err != nil {
			t.Fatalf("ResumePM: %v", err)
		}
		if resumed.Status != models.PaymentMerchantStatusActive {
			t.Fatalf("status = %q, want active", resumed.Status)
		}
	})

	t.Run("pause non-active rejected", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		svc := NewPMService(db)
		pm := seedPM(t, db, models.PaymentMerchantStatusPending, "")
		if _, err := svc.PausePM(ctx, pm.ID, admin); !errors.Is(err, ErrInvalidPMTransition) {
			t.Fatalf("err = %v, want ErrInvalidPMTransition", err)
		}
	})

	t.Run("resume non-paused rejected", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		svc := NewPMService(db)
		pm := seedPM(t, db, models.PaymentMerchantStatusActive, "pm-x")
		if _, err := svc.ResumePM(ctx, pm.ID, admin); !errors.Is(err, ErrInvalidPMTransition) {
			t.Fatalf("err = %v, want ErrInvalidPMTransition", err)
		}
	})
}

func TestApprovePM(t *testing.T) {
	ctx := context.Background()
	admin := uuid.New()

	t.Run("pending → approved (no VA created)", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		fake := &fakeNombaProvisioner{}
		svc := NewPMOnboardingService(db, fake)
		pm := seedPM(t, db, models.PaymentMerchantStatusPending, "")

		got, err := svc.ApprovePM(ctx, pm.ID, admin)
		if err != nil {
			t.Fatalf("ApprovePM: %v", err)
		}
		if got.Status != models.PaymentMerchantStatusApproved {
			t.Fatalf("status = %q, want approved", got.Status)
		}
		if got.ReviewedBy != admin {
			t.Fatalf("reviewedBy = %v, want %v", got.ReviewedBy, admin)
		}
		// Approval must never provision a VA — that is deferred to activation.
		if fake.createVirtualAccountCalls != 0 {
			t.Fatalf("VA create called %d times, want 0 (approval never provisions)", fake.createVirtualAccountCalls)
		}
		if got.NombaAccountRef != "" || got.InboundAccountNumber != "" {
			t.Fatalf("VA fields set on approval: ref=%q inbound=%q", got.NombaAccountRef, got.InboundAccountNumber)
		}
	})

	t.Run("works without a fiat provider (pure transition)", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		svc := NewPMOnboardingService(db, nil)
		pm := seedPM(t, db, models.PaymentMerchantStatusPending, "")
		got, err := svc.ApprovePM(ctx, pm.ID, admin)
		if err != nil {
			t.Fatalf("ApprovePM with nil provisioner should succeed: %v", err)
		}
		if got.Status != models.PaymentMerchantStatusApproved {
			t.Fatalf("status = %q, want approved", got.Status)
		}
	})

	t.Run("non-pending → ErrInvalidPMTransition", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		svc := NewPMOnboardingService(db, &fakeNombaProvisioner{})
		pm := seedPM(t, db, models.PaymentMerchantStatusActive, "pm-x")
		if _, err := svc.ApprovePM(ctx, pm.ID, admin); !errors.Is(err, ErrInvalidPMTransition) {
			t.Fatalf("err = %v, want ErrInvalidPMTransition", err)
		}
	})

	t.Run("missing → ErrPMNotFound", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		svc := NewPMOnboardingService(db, &fakeNombaProvisioner{})
		if _, err := svc.ApprovePM(ctx, uuid.New(), admin); !errors.Is(err, ErrPMNotFound) {
			t.Fatalf("err = %v, want ErrPMNotFound", err)
		}
	})
}
