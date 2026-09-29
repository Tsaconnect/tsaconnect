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

// M4: the reserved-word check reads a name as a customer does — through
// punctuation, spacing, styled and full-width letters, accents, and Cyrillic,
// Greek and digit lookalikes — without catching ordinary names.
func TestFoldBusinessName_SeesThroughLookalikes(t *testing.T) {
	s := &PayMerchantService{}
	for _, bad := range []string{
		"T.S.A-Pay", "T_S_A pay", "ＴＳＡ Ｐａｙ", "Tѕа Pay", // Cyrillic ѕ, а
		"ΤSΑ Connect", // Greek Τ, Α
		"TSA P@y", "TSA C0nnect", "Supp0rt Team", "Sùppört Desk", "R3fund Centre", "𝐓𝐒𝐀 𝐏𝐚𝐲",
	} {
		if _, err := s.NormalizeBusinessName(bad); !errors.Is(err, ErrPayMerchantInvalid) {
			t.Errorf("%q passed as %q", bad, foldBusinessName(bad))
		}
	}
	for _, fine := range []string{"Sky Air", "Obaino4Christ Ventures", "Kite Travel Nigeria", "Café Lagos", "Tasty Bites"} {
		if got, err := s.NormalizeBusinessName(fine); err != nil || got != fine {
			t.Errorf("%q refused: %v (%q)", fine, err, got)
		}
	}
	if got := foldBusinessName("T.S.A-Pay"); got != "tsapay" {
		t.Fatalf("fold = %q", got)
	}
}

// The rule holds at onboarding and for a super admin's direct rename too.
func TestPayMerchant_BusinessNameRuleAtOnboardingAndAdminRename(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	req := payRequest()
	req.BusinessName = "TSA  Pay Official"
	if _, err := s.CreateFromRequest(context.Background(), req); !errors.Is(err, ErrPayMerchantInvalid) {
		t.Fatalf("onboarding a reserved name: err = %v", err)
	}
	req = payRequest()
	req.BusinessName = "  Sky   Air "
	m, err := s.CreateFromRequest(context.Background(), req)
	if err != nil || m.BusinessName != "Sky Air" {
		t.Fatalf("err %v merchant %+v", err, m)
	}
	for _, bad := range []string{"Sky\u202eAir", "Sky Air Supp0rt"} {
		name := bad
		if _, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{BusinessName: &name}); !errors.Is(err, ErrPayMerchantInvalid) {
			t.Errorf("admin rename to %q: err = %v", bad, err)
		}
	}
	renamed := " Sky  Air Limited "
	got, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{BusinessName: &renamed})
	if err != nil || got.BusinessName != "Sky Air Limited" {
		t.Fatalf("err %v merchant %+v", err, got)
	}
}

// L2: a Latin name spelled partly in another alphabet is refused outright —
// no lookalike table can list every letter that passes for a Latin one — and
// small capitals are folded like the letters they are. East Asian names that
// mix scripts as a matter of course, and names wholly in one script, pass.
func TestNormalizeBusinessName_MixedScriptsAndSmallCapitals(t *testing.T) {
	s := &PayMerchantService{}
	for _, bad := range []string{
		"TSA Cօnnect", // Armenian օ
		"ᴛsa pay",     // small capital ᴛ
		"TꜱA Pay",     // small capital ꜱ
		"ᏚᎪ Pay",      // Cherokee Ꮪ Ꭺ
		"ꓢupport",     // Lisu ꓢ
		"Sky Αir",     // Greek Α in an ordinary name
	} {
		_, err := s.NormalizeBusinessName(bad)
		if !errors.Is(err, ErrPayBusinessNameInvalid) || !errors.Is(err, ErrPayMerchantInvalid) {
			t.Errorf("%q: err = %v (folded %q)", bad, err, foldBusinessName(bad))
		}
	}
	for _, fine := range []string{"Ткань Лагос", "東京 Sushi Bar", "すし Tokyo", "서울 BBQ", "Café Lagos", "Obaino4Christ Ventures"} {
		if got, err := s.NormalizeBusinessName(fine); err != nil || got != fine {
			t.Errorf("%q refused: %v", fine, err)
		}
	}
	// The message reads as it always has; the reason is there on its own.
	_, err := s.NormalizeBusinessName("TSA Pay")
	var bn *BusinessNameError
	if !errors.As(err, &bn) || bn.Reason == "" || err.Error() != ErrPayMerchantInvalid.Error()+": "+bn.Reason {
		t.Fatalf("err = %v", err)
	}
}

// Configured reserved words add to the built-in ones; they never replace them.
func TestSetReservedBusinessNames_AddsToTheDefaults(t *testing.T) {
	s := &PayMerchantService{}
	s.SetReservedBusinessNames([]string{"Kite"})
	for _, bad := range []string{"Kite Air", "TSA Pay Travel", "Refund Desk"} {
		if _, err := s.NormalizeBusinessName(bad); !errors.Is(err, ErrPayBusinessNameInvalid) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

// An approval is held to the rule as it stands when the admin approves: a
// name that was fine when requested and is not any more stays unapproved.
func TestPayMerchant_ApprovalRechecksTheName(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	f.requestRename(t, "Kite Travel")
	rows := f.nameChanges(t)
	f.s.SetReservedBusinessNames([]string{"Kite"})
	if _, _, err := f.s.ReviewBusinessNameChange(ctx, uuid.New(), rows[0].ID, true, ""); !errors.Is(err, ErrPayBusinessNameInvalid) {
		t.Fatalf("approving a name the rule now refuses: err = %v", err)
	}
	if rows = f.nameChanges(t); rows[0].Status != models.PayNameChangePending || f.stored(t).BusinessName != "Sky Air" {
		t.Fatalf("a refused approval changed something: %+v %+v", rows[0], f.stored(t))
	}
	if change, _, err := f.s.ReviewBusinessNameChange(ctx, uuid.New(), rows[0].ID, false, "reserved now"); err != nil || change.Status != models.PayNameChangeRejected {
		t.Fatalf("it can still be rejected: %+v %v", change, err)
	}
}
