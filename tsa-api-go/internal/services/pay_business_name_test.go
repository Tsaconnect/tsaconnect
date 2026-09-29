package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// requestRename asks for a business rename and confirms it with the emailed code.
func (f *ownerFixture) requestRename(t *testing.T, name string) *models.PayMerchant {
	t.Helper()
	res, err := f.s.RequestBusinessNameChange(context.Background(), f.owner, name, nil)
	if err != nil || res.Pending == nil || res.Pending.Kind != models.PayOwnerActionBusinessName {
		t.Fatalf("err %v result %+v", err, res)
	}
	r, err := f.s.ConfirmOwnerAction(context.Background(), f.owner, res.Pending.ID, f.mail.code(t))
	if err != nil {
		t.Fatal(err)
	}
	return r.Merchant
}

func (f *ownerFixture) nameChanges(t *testing.T) []models.PayMerchantNameChange {
	t.Helper()
	var rows []models.PayMerchantNameChange
	if err := f.s.db.Where("merchant_id = ?", f.m.ID).Order("created_at asc").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// A rename is only a request until an admin approves it: the merchant keeps its
// name everywhere — including the payout-account check — and then takes the
// new one, audited.
func TestPayMerchant_RenameTakesEffectOnlyOnApproval(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, models.VerificationStatusVerified, "Ada Chioma Obi")

	m := f.requestRename(t, "  Obaino4Christ   Ventures ")
	if m.BusinessName != "Sky Air" || f.stored(t).BusinessName != "Sky Air" || f.stored(t).Version != f.m.Version {
		t.Fatalf("a confirmed request renamed the merchant: %+v", f.stored(t))
	}
	rows := f.nameChanges(t)
	if len(rows) != 1 || rows[0].Status != models.PayNameChangePending || rows[0].NewName != "Obaino4Christ Ventures" ||
		rows[0].OldName != "Sky Air" || rows[0].OwnerVerifiedName != "Ada Chioma Obi" || rows[0].RequestedBy != f.owner {
		t.Fatalf("rename record %+v", rows)
	}
	if p, _ := f.s.PendingBusinessName(ctx, f.m.ID); p == nil || p.Name != "Obaino4Christ Ventures" || p.Since.IsZero() {
		t.Fatalf("pending = %+v", p)
	}

	// Payouts keep matching the current name until approval.
	f.banks.name = "OBAINO4CHRIST VENTURES"
	sent := f.mail.sent
	if _, err := f.s.UpdateSettlementByOwner(ctx, f.owner, bankChange("9876543210")); !errors.Is(err, ErrPaySettlementNameMismatch) {
		t.Fatalf("an account in the requested name before approval: err = %v", err)
	}
	if f.mail.sent != sent {
		t.Fatal("a refused payout change sent a code")
	}

	if _, _, err := f.s.ReviewBusinessNameChange(ctx, f.owner, rows[0].ID, true, ""); !errors.Is(err, ErrPayBusinessNameSelfReview) {
		t.Fatalf("the owner reviewing their own rename: err = %v", err)
	}
	change, m, err := f.s.ReviewBusinessNameChange(ctx, uuid.New(), rows[0].ID, true, "")
	if err != nil || change.Status != models.PayNameChangeApproved || change.ReviewedBy == nil || change.ReviewedAt == nil ||
		m.BusinessName != "Obaino4Christ Ventures" || m.Version != f.m.Version+1 {
		t.Fatalf("err %v change %+v merchant %+v", err, change, m)
	}
	for _, action := range []models.PayAuditAction{models.PayAuditActionBusinessNameReview, models.PayAuditActionMerchantUpdate} {
		var audit models.PayAuditEvent
		if err := f.s.db.Where("merchant_id = ? AND action = ?", f.m.ID, action).First(&audit).Error; err != nil {
			t.Fatalf("the approval must be audited as %s: %v", action, err)
		}
	}
	// Now an account in the new name pays out.
	f.request(t, bankChange("9876543210"))
	if _, _, err := f.s.ReviewBusinessNameChange(ctx, uuid.New(), rows[0].ID, false, ""); !errors.Is(err, ErrPayBusinessNameChangeDecided) {
		t.Fatalf("a decided change reviewed again: err = %v", err)
	}
}

func TestPayMerchant_RejectedRenameKeepsTheName(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	f.requestRename(t, "Kite Travel")
	rows := f.nameChanges(t)
	change, m, err := f.s.ReviewBusinessNameChange(ctx, uuid.New(), rows[0].ID, false, "not the registered name")
	if err != nil || change.Status != models.PayNameChangeRejected || change.Note != "not the registered name" || m.BusinessName != "Sky Air" {
		t.Fatalf("err %v change %+v merchant %+v", err, change, m)
	}
}

func TestPayMerchant_RenameIsGuardedVersionedAndValidated(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	stale := f.m.Version + 1
	if _, err := f.s.RequestBusinessNameChange(ctx, f.owner, "Kite Travel", &stale); !errors.Is(err, ErrPayMerchantStale) {
		t.Fatalf("a stale screen: err = %v", err)
	}
	for name, bad := range map[string]string{
		"too short":            "K",
		"a zero-width space":   "Kite​Travel",
		"a direction override": "Kite ‮Travel",
		"a reserved name":      "Official T S A pay Help",
		"another reserved one": "Kite Refund Desk",
	} {
		if _, err := f.s.RequestBusinessNameChange(ctx, f.owner, bad, nil); !errors.Is(err, ErrPayMerchantInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	f.nothingWaiting(t)

	// A later request supersedes the one still waiting.
	f.requestRename(t, "Kite Travel")
	f.requestRename(t, "Kite Travel Nigeria")
	rows := f.nameChanges(t)
	if len(rows) != 2 || rows[0].Status != models.PayNameChangeRejected || rows[1].Status != models.PayNameChangePending {
		t.Fatalf("renames %+v", rows)
	}
	// Asking for the current name back withdraws the request, with no code.
	res, err := f.s.RequestBusinessNameChange(ctx, f.owner, " Sky  Air ", nil)
	if err != nil || res.Pending != nil || res.Merchant == nil {
		t.Fatalf("err %v result %+v", err, res)
	}
	if rows = f.nameChanges(t); rows[1].Status != models.PayNameChangeRejected {
		t.Fatalf("renames %+v", rows)
	}
	if p, _ := f.s.PendingBusinessName(ctx, f.m.ID); p != nil {
		t.Fatalf("nothing should be pending: %+v", p)
	}

	f.s.SetReservedBusinessNames([]string{"Kite"})
	if _, err := f.s.RequestBusinessNameChange(ctx, f.owner, "Kite Air", nil); !errors.Is(err, ErrPayMerchantInvalid) {
		t.Fatalf("a configured reserved word: err = %v", err)
	}
}

// Renaming the merchant directly is an admin edit, super admin only (it is
// Sensitive), and it closes any request the owner still has waiting.
func TestPayMerchant_AdminRenameSupersedesAPendingRequest(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	f.requestRename(t, "Kite Travel")
	renamed := "Kite Travel Limited"
	if !(PayMerchantUpdate{BusinessName: &renamed}).Sensitive() {
		t.Fatal("renaming a merchant directly must need a super admin")
	}
	m, _, err := f.s.Update(ctx, payActor, f.m.ID, PayMerchantUpdate{BusinessName: &renamed})
	if err != nil || m.BusinessName != renamed {
		t.Fatalf("err %v merchant %+v", err, m)
	}
	if rows := f.nameChanges(t); rows[0].Status != models.PayNameChangeRejected {
		t.Fatalf("the owner's pending rename must be superseded: %+v", rows)
	}
}
