package services

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestComposeBusinessName(t *testing.T) {
	cases := []struct {
		name, base, legal string
		verified          bool
		want              string
	}{
		{"id caps become a readable name", "Ojay store", "ADEMU EMMANUEL", false, "Ojay store - Ademu Emmanuel"},
		{"three names", "Ojay store", "UCHENNA EVARISTUS UMEUGOKWE", false, "Ojay store - Uchenna Evaristus Umeugokwe"},
		{"hyphens and apostrophes keep their capitals", "Shop", "ADA O'NEIL-OKAFOR", false, "Shop - Ada O'Neil-Okafor"},
		{"a verified business shows its own name", "Ojay store", "ADEMU EMMANUEL", true, "Ojay store"},
		{"no legal name on record, no suffix", "Ojay store", "", false, "Ojay store"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := composeBusinessName(tc.base, tc.legal, tc.verified); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStripLegalSuffix(t *testing.T) {
	if got := stripLegalSuffix("Ojay store - Ademu Emmanuel", "ADEMU EMMANUEL"); got != "Ojay store" {
		t.Errorf("pasting the shown name back must not add the legal name twice: %q", got)
	}
	if got := stripLegalSuffix("OJAY STORE - ademu emmanuel", "ADEMU EMMANUEL"); got != "OJAY STORE" {
		t.Errorf("case is ignored: %q", got)
	}
	if got := stripLegalSuffix("Ojay store", "ADEMU EMMANUEL"); got != "Ojay store" {
		t.Errorf("a plain name is untouched: %q", got)
	}
	if got := stripLegalSuffix("Ojay store - Someone Else", "ADEMU EMMANUEL"); got != "Ojay store - Someone Else" {
		t.Errorf("another person's name is not the owner's suffix: %q", got)
	}
	if got := stripLegalSuffix("Ojay store", ""); got != "Ojay store" {
		t.Errorf("no legal name, nothing to strip: %q", got)
	}
}

// addOwnedMerchant adds a second verified owner and their TSA Pay merchant to
// the fixture's database.
func (f *ownerFixture) addOwnedMerchant(t *testing.T, legalName, business string) (uuid.UUID, *models.PayMerchant, error) {
	t.Helper()
	owner := uuid.New()
	if err := f.s.db.Exec(`INSERT INTO users (id, name, email, wallet_address, verification_status, verified_legal_name) VALUES (?, ?, ?, ?, ?, ?)`,
		owner, "typed at signup", fmt.Sprintf("%s@example.com", owner), "0x"+owner.String()[:8], models.VerificationStatusVerified, legalName).Error; err != nil {
		t.Fatal(err)
	}
	req := payRequest()
	req.ID, req.UserID, req.BusinessName = uuid.New(), owner, business
	m, err := f.s.CreateFromRequest(context.Background(), req)
	return owner, m, err
}

func TestBusinessName_OnboardingComposesAndIsUnique(t *testing.T) {
	f := ownedMerchant(t, models.VerificationStatusVerified, "Ada Chioma Obi")
	if f.m.BusinessName != "Sky Air - Ada Chioma Obi" || f.m.BusinessBaseName != "Sky Air" || f.m.BusinessVerified {
		t.Fatalf("merchant = %q / base %q / verified %v", f.m.BusinessName, f.m.BusinessBaseName, f.m.BusinessVerified)
	}
	if f.m.BusinessNameKey != businessNameKey("Sky Air - Ada Chioma Obi") {
		t.Fatalf("key = %q", f.m.BusinessNameKey)
	}

	// The same name for someone else is fine: their own legal name tells them apart.
	_, other, err := f.addOwnedMerchant(t, "John Emeka Okafor", "Sky Air")
	if err != nil || other.BusinessName != "Sky Air - John Emeka Okafor" {
		t.Fatalf("a second Sky Air: %v %+v", err, other)
	}
	// Two people with the same legal name and the same business would show one name.
	if _, _, err := f.addOwnedMerchant(t, "ADA CHIOMA OBI", "sky-air"); !errors.Is(err, ErrPayBusinessNameTaken) {
		t.Fatalf("an identical shown name: err = %v, want ErrPayBusinessNameTaken", err)
	}
}

func TestBusinessName_UnverifiedOwnerWithNoLegalNameKeepsThePlainName(t *testing.T) {
	f := ownedMerchant(t, "", "")
	if f.m.BusinessName != "Sky Air" || f.m.BusinessBaseName != "Sky Air" {
		t.Fatalf("merchant = %q / base %q", f.m.BusinessName, f.m.BusinessBaseName)
	}
	// With no legal name to tell them apart, a second "Sky Air" is simply taken.
	if _, _, err := f.addOwnedMerchant(t, "", "Sky Air"); !errors.Is(err, ErrPayBusinessNameTaken) {
		t.Fatalf("err = %v", err)
	}
}

func TestBusinessName_RenameIsRefusedWhenTakenAndNeverSendsACode(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, models.VerificationStatusVerified, "Ada Chioma Obi")
	if _, _, err := f.addOwnedMerchant(t, "Ada Chioma Obi", "Kite Travel"); err != nil {
		t.Fatal(err)
	}
	// Same legal name, so "Kite Travel" would be shown identically.
	sent := f.mail.sent
	if _, err := f.s.RequestBusinessNameChange(ctx, f.owner, "Kite Travel", nil); !errors.Is(err, ErrPayBusinessNameTaken) {
		t.Fatalf("err = %v, want ErrPayBusinessNameTaken", err)
	}
	if f.mail.sent != sent {
		t.Error("a refused name must not send a confirmation code")
	}
	// Spelling it differently does not get around it.
	if _, err := f.s.RequestBusinessNameChange(ctx, f.owner, "KITE-travel", nil); !errors.Is(err, ErrPayBusinessNameTaken) {
		t.Fatalf("a lookalike spelling: err = %v", err)
	}
}

func TestBusinessName_ApprovalRechecksUniquenessAndUsesTheLegalNameNow(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, models.VerificationStatusVerified, "Ada Chioma Obi")
	f.requestRename(t, "Kite Travel")
	rows := f.nameChanges(t)

	// Someone else takes the name while the request waits.
	if _, _, err := f.addOwnedMerchant(t, "Ada Chioma Obi", "Kite Travel"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ReviewBusinessNameChange(ctx, uuid.New(), rows[0].ID, true, ""); !errors.Is(err, ErrPayBusinessNameTaken) {
		t.Fatalf("approving a name that was taken meanwhile: err = %v", err)
	}
	if got := f.stored(t).BusinessName; got != "Sky Air - Ada Chioma Obi" {
		t.Fatalf("a refused approval changed the name: %q", got)
	}
	if rows = f.nameChanges(t); rows[0].Status != models.PayNameChangePending {
		t.Fatalf("a refused approval decided the request: %+v", rows[0])
	}
}

func TestBusinessName_AdminEditTakesTheOwnersOwnNameAndNeverDoublesTheSuffix(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, models.VerificationStatusVerified, "Ada Chioma Obi")

	// The console sends back what it shows: nothing changes.
	same := f.m.BusinessName
	m, _, err := f.s.Update(ctx, payActor, f.m.ID, PayMerchantUpdate{BusinessName: &same})
	if err != nil || m.BusinessName != "Sky Air - Ada Chioma Obi" || m.BusinessBaseName != "Sky Air" {
		t.Fatalf("giving back the shown name: %v %+v", err, m)
	}
	// A new name, with or without the suffix, gets the suffix once.
	for _, given := range []string{"Kite Travel", "Kite Travel - Ada Chioma Obi"} {
		g := given
		m, _, err := f.s.Update(ctx, payActor, f.m.ID, PayMerchantUpdate{BusinessName: &g})
		if err != nil || m.BusinessName != "Kite Travel - Ada Chioma Obi" || m.BusinessBaseName != "Kite Travel" {
			t.Fatalf("rename to %q: %v %+v", given, err, m)
		}
	}
	// A taken name is refused.
	if _, _, err := f.addOwnedMerchant(t, "Ada Chioma Obi", "Zed Air"); err != nil {
		t.Fatal(err)
	}
	taken := "Zed Air"
	if _, _, err := f.s.Update(ctx, payActor, f.m.ID, PayMerchantUpdate{BusinessName: &taken}); !errors.Is(err, ErrPayBusinessNameTaken) {
		t.Fatalf("err = %v", err)
	}
}

func TestBusinessName_RecomposesWhenTheLegalNameChanges(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, models.VerificationStatusVerified, "Ada Chioma Obi")
	RecomposeBusinessName(ctx, f.s.db, f.owner, "ADA CHIOMA OKAFOR")
	if got := f.stored(t); got.BusinessName != "Sky Air - Ada Chioma Okafor" || got.BusinessBaseName != "Sky Air" || got.Version != f.m.Version+1 {
		t.Fatalf("merchant = %+v", got)
	}

	// Taken: the old name stays.
	if _, _, err := f.addOwnedMerchant(t, "Zed Person", "Sky Air"); err != nil {
		t.Fatal(err)
	}
	RecomposeBusinessName(ctx, f.s.db, f.owner, "ZED PERSON")
	if got := f.stored(t).BusinessName; got != "Sky Air - Ada Chioma Okafor" {
		t.Fatalf("a taken name must leave the old one: %q", got)
	}

	// A verified business keeps its own name whatever its owner is called.
	if err := f.s.db.Model(&models.PayMerchant{}).Where("id = ?", f.m.ID).Update("business_verified", true).Error; err != nil {
		t.Fatal(err)
	}
	RecomposeBusinessName(ctx, f.s.db, f.owner, "SOMEONE ELSE")
	if got := f.stored(t).BusinessName; got != "Sky Air - Ada Chioma Okafor" {
		t.Fatalf("a verified business is not recomposed: %q", got)
	}
}

func TestComposeBusinessNames_OneTimeStepForExistingMerchants(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, models.VerificationStatusVerified, "Ada Chioma Obi")
	// An existing merchant from before names were composed: plain name, no base, no key.
	if err := f.s.db.Model(&models.PayMerchant{}).Where("id = ?", f.m.ID).
		Updates(map[string]any{"business_name": "Sky Air", "business_base_name": "", "business_name_key": ""}).Error; err != nil {
		t.Fatal(err)
	}
	_, noName, err := f.addOwnedMerchant(t, "", "No Legal Name Ltd")
	if err != nil {
		t.Fatal(err)
	}
	if err := FillBusinessNameKeys(ctx, f.s.db); err != nil {
		t.Fatal(err)
	}

	items, err := ComposeBusinessNames(ctx, f.s.db, true)
	if err != nil || len(items) != 2 {
		t.Fatalf("dry run: %v %+v", err, items)
	}
	if got := f.stored(t).BusinessName; got != "Sky Air" {
		t.Fatalf("a dry run changed a name: %q", got)
	}

	if _, err := ComposeBusinessNames(ctx, f.s.db, false); err != nil {
		t.Fatal(err)
	}
	got := f.stored(t)
	if got.BusinessName != "Sky Air - Ada Chioma Obi" || got.BusinessBaseName != "Sky Air" || got.BusinessNameKey != businessNameKey("Sky Air - Ada Chioma Obi") {
		t.Fatalf("merchant = %+v", got)
	}
	if again, _ := f.s.Get(ctx, noName.ID); again.BusinessName != "No Legal Name Ltd" {
		t.Fatalf("an owner with no legal name is left as is: %q", again.BusinessName)
	}
	// Running it again changes nothing.
	items, err = ComposeBusinessNames(ctx, f.s.db, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Skipped == "" {
			t.Fatalf("a second run found something to rename: %+v", it)
		}
	}
}

// The owner's legal name is joined with " - ", so the owner's own part may not
// hold a spaced dash that could pass its own text off as the verified one.
func TestBusinessName_RefusesASpacedDashInTheOwnersPart(t *testing.T) {
	s := &PayMerchantService{}
	for _, bad := range []string{"Ojay store - John Okafor", "Ojay store – John Okafor", "Ojay store — John Okafor", "Ojay store \u2212 John", "A - B"} {
		if _, err := s.NormalizeBusinessName(bad); !errors.Is(err, ErrPayBusinessNameInvalid) {
			t.Errorf("%q: err = %v, want ErrPayBusinessNameInvalid", bad, err)
		}
	}
	for _, fine := range []string{"Sky-Air", "Ojay store", "Sky -Air", "Sky- Air", "Co. Ltd (Lagos)"} {
		if _, err := s.NormalizeBusinessName(fine); err != nil {
			t.Errorf("%q refused: %v", fine, err)
		}
	}
}

// A legal name that is only a scan of an ID's field labels is never added to a
// business name; a real name that happens to be a label word is.
func TestComposeBusinessName_IgnoresALegalNameThatIsAScanOfFieldLabels(t *testing.T) {
	for _, junk := range []string{"SURNAME N SURNAME N", "Surnamen Surnamen", "Surname", "First Middle Last", "Given Names", "NONE", "N A", "- -"} {
		if got := composeBusinessName("Justice_Ad", junk, false); got != "Justice_Ad" {
			t.Errorf("%q: composed %q, want the plain name", junk, got)
		}
	}
	for legal, want := range map[string]string{
		"GIVEN NCUBE":          "Shop - Given Ncube",  // Given is a real given name
		"FIRST OKAFOR":         "Shop - First Okafor", // so is First
		"ADA SURNAMELOVER OBI": "Shop - Ada Surnamelover Obi",
		"LAST ADE B":           "Shop - Last Ade B",
	} {
		if got := composeBusinessName("Shop", legal, false); got != want {
			t.Errorf("%q: composed %q, want %q", legal, got, want)
		}
	}
}

func TestComposeBusinessNames_SkipsJunkLegalNamesAndTidiesSpaces(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, models.VerificationStatusVerified, "Ada Chioma Obi")
	if err := f.s.db.Model(&models.PayMerchant{}).Where("id = ?", f.m.ID).
		Updates(map[string]any{"business_name": "Sky Air  ", "business_base_name": "", "business_name_key": ""}).Error; err != nil {
		t.Fatal(err)
	}
	junkOwner, junk, err := f.addOwnedMerchant(t, "SURNAME N SURNAME N", "Justice_Ad")
	if err != nil {
		t.Fatal(err)
	}
	_ = junkOwner
	if err := FillBusinessNameKeys(ctx, f.s.db); err != nil {
		t.Fatal(err)
	}
	if _, err := ComposeBusinessNames(ctx, f.s.db, false); err != nil {
		t.Fatal(err)
	}
	if got := f.stored(t).BusinessName; got != "Sky Air - Ada Chioma Obi" {
		t.Errorf("stray spaces doubled at the joiner: %q", got)
	}
	if again, _ := f.s.Get(ctx, junk.ID); again.BusinessName != "Justice_Ad" {
		t.Errorf("a junk legal name must leave the plain name: %q", again.BusinessName)
	}
}
