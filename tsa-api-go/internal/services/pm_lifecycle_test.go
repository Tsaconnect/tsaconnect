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

func TestProvisionAndActivatePM(t *testing.T) {
	ctx := context.Background()
	admin := uuid.New()

	t.Run("pending → provisions VA + activates", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		fake := &fakeNombaProvisioner{
			createVirtualAccountFn: func(ctx context.Context, accountRef, accountName string) (string, error) {
				return "9999000011", nil
			},
		}
		svc := NewPMOnboardingService(db, fake)
		pm := seedPM(t, db, models.PaymentMerchantStatusPending, "")

		got, err := svc.ProvisionAndActivatePM(ctx, pm.ID, admin)
		if err != nil {
			t.Fatalf("ProvisionAndActivatePM: %v", err)
		}
		if got.Status != models.PaymentMerchantStatusActive {
			t.Fatalf("status = %q, want active", got.Status)
		}
		if got.ProvisioningStatus != models.PMProvisioningActive {
			t.Fatalf("provisioning = %q, want active", got.ProvisioningStatus)
		}
		if got.InboundAccountNumber != "9999000011" {
			t.Fatalf("inbound = %q, want 9999000011", got.InboundAccountNumber)
		}
		if got.NombaAccountRef != pmAccountRef(pm.UserID) {
			t.Fatalf("accountRef = %q, want %q", got.NombaAccountRef, pmAccountRef(pm.UserID))
		}
		if fake.createVirtualAccountCalls != 1 {
			t.Fatalf("VA create called %d times, want 1", fake.createVirtualAccountCalls)
		}
	})

	t.Run("idempotent: skips VA create when accountRef already set", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		fake := &fakeNombaProvisioner{
			createVirtualAccountFn: func(ctx context.Context, accountRef, accountName string) (string, error) {
				return "0000", nil
			},
		}
		svc := NewPMOnboardingService(db, fake)
		pm := seedPM(t, db, models.PaymentMerchantStatusPending, "pm-existing")

		got, err := svc.ProvisionAndActivatePM(ctx, pm.ID, admin)
		if err != nil {
			t.Fatalf("ProvisionAndActivatePM: %v", err)
		}
		if got.Status != models.PaymentMerchantStatusActive {
			t.Fatalf("status = %q, want active", got.Status)
		}
		if fake.createVirtualAccountCalls != 0 {
			t.Fatalf("VA create called %d times, want 0 (already provisioned)", fake.createVirtualAccountCalls)
		}
	})

	t.Run("VA failure leaves row pending, retry-safe", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		fake := &fakeNombaProvisioner{
			createVirtualAccountFn: func(ctx context.Context, accountRef, accountName string) (string, error) {
				return "", errors.New("nomba 500")
			},
		}
		svc := NewPMOnboardingService(db, fake)
		pm := seedPM(t, db, models.PaymentMerchantStatusPending, "")

		if _, err := svc.ProvisionAndActivatePM(ctx, pm.ID, admin); err == nil {
			t.Fatal("expected error from VA failure")
		}
		var reloaded models.PaymentMerchant
		if err := db.First(&reloaded, "id = ?", pm.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if reloaded.Status != models.PaymentMerchantStatusPending {
			t.Fatalf("status = %q, want still pending after failure", reloaded.Status)
		}
	})

	t.Run("non-pending → ErrInvalidPMTransition", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		fake := &fakeNombaProvisioner{}
		svc := NewPMOnboardingService(db, fake)
		pm := seedPM(t, db, models.PaymentMerchantStatusActive, "pm-x")
		if _, err := svc.ProvisionAndActivatePM(ctx, pm.ID, admin); !errors.Is(err, ErrInvalidPMTransition) {
			t.Fatalf("err = %v, want ErrInvalidPMTransition", err)
		}
	})

	t.Run("provisioner nil → ErrPMProvisioningUnavailable", func(t *testing.T) {
		db := newSQLiteForOnboarding(t)
		svc := NewPMOnboardingService(db, nil)
		pm := seedPM(t, db, models.PaymentMerchantStatusPending, "")
		if _, err := svc.ProvisionAndActivatePM(ctx, pm.ID, admin); !errors.Is(err, ErrPMProvisioningUnavailable) {
			t.Fatalf("err = %v, want ErrPMProvisioningUnavailable", err)
		}
	})
}
