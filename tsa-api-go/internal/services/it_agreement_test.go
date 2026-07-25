package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestITAgreement_GateAcceptAndReconsent(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	user := uuid.New()

	// Before accepting, the gate blocks and the status reads false.
	if err := s.RequireITAgreement(ctx, user); !errors.Is(err, ErrITAgreementRequired) {
		t.Fatalf("gate before accept = %v, want ErrITAgreementRequired", err)
	}
	if ok, _ := s.HasAcceptedITAgreement(ctx, user); ok {
		t.Fatal("HasAccepted = true before accepting")
	}

	// Accept → gate passes.
	if err := s.AcceptITAgreement(ctx, user, "1.2.3.4", "test-agent"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := s.RequireITAgreement(ctx, user); err != nil {
		t.Fatalf("gate after accept = %v, want nil", err)
	}

	// Idempotent: re-accepting does not add a second row for the same version.
	if err := s.AcceptITAgreement(ctx, user, "1.2.3.4", "test-agent"); err != nil {
		t.Fatalf("re-accept: %v", err)
	}
	var count int64
	s.db.Model(&models.ITAgreementAcceptance{}).Where("user_id = ?", user).Count(&count)
	if count != 1 {
		t.Fatalf("acceptance rows = %d, want 1 (idempotent)", count)
	}

	// The audit row captured the version + ip.
	var row models.ITAgreementAcceptance
	if err := s.db.First(&row, "user_id = ?", user).Error; err != nil {
		t.Fatal(err)
	}
	if row.Version != ITAgreementVersion || row.IPAddress != "1.2.3.4" {
		t.Fatalf("audit row = %+v", row)
	}
}

// A user who accepted an OLDER version must re-consent when the version bumps —
// the gate compares against the current version only.
func TestITAgreement_ReconsentOnVersionBump(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	user := uuid.New()

	// Simulate a prior acceptance of an older version directly.
	old := &models.ITAgreementAcceptance{UserID: user, Version: "0.9"}
	if err := s.db.Create(old).Error; err != nil {
		t.Fatal(err)
	}

	if ok, _ := s.HasAcceptedITAgreement(ctx, user); ok {
		t.Fatal("stale-version acceptance counted as current")
	}
	if err := s.RequireITAgreement(ctx, user); !errors.Is(err, ErrITAgreementRequired) {
		t.Fatalf("gate with only old-version acceptance = %v, want ErrITAgreementRequired", err)
	}
}

func TestCurrentITAgreement_ShapeMatchesVersion(t *testing.T) {
	ag := CurrentITAgreement()
	if ag.Version != ITAgreementVersion {
		t.Fatalf("served version %q != const %q", ag.Version, ITAgreementVersion)
	}
	if ag.Title == "" || ag.Intro == "" || len(ag.Clauses) != 5 {
		t.Fatalf("agreement body incomplete: %+v", ag)
	}
}
