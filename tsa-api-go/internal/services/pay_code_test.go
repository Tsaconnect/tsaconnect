package services

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var payCodeShape = regexp.MustCompile(`^[0-9ABCDEFGHJKMNPQRSTVWXYZ]{10}$`)

// stubPayCodes makes the code generator hand out these codes, in order, and
// fails the test if anything asks for one more.
func stubPayCodes(t *testing.T, codes ...string) {
	t.Helper()
	next := 0
	payCodeNext = func() (string, error) {
		if next >= len(codes) {
			t.Fatalf("asked for payment code %d; only %d stubbed", next+1, len(codes))
		}
		next++
		return codes[next-1], nil
	}
	t.Cleanup(func() { payCodeNext = newPayCode })
}

func TestPayCode_NewCodesAreTenCrockfordCharacters(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		c, err := newPayCode()
		if err != nil || !payCodeShape.MatchString(c) {
			t.Fatalf("code %q err %v", c, err)
		}
		if seen[c] {
			t.Fatalf("%q came up twice in 2000 draws of 50 bits", c)
		}
		seen[c] = true
	}
}

func TestPayCode_NormalizeForgivesWhatAPayerTypes(t *testing.T) {
	for in, want := range map[string]string{
		"TSA-7K2QF-M9XWD":   "7K2QFM9XWD",
		"tsa-7k2qf-m9xwd":   "7K2QFM9XWD",
		"7K2QF M9XWD":       "7K2QFM9XWD",
		"7k2qfm9xwd":        "7K2QFM9XWD",
		"TSA 7K2QF M9XWD\n": "7K2QFM9XWD",
		" TSA–7K2QF—M9XWD":  "7K2QFM9XWD", // NBSP, en and em dash: pasted from a chat app
		"TSA-7K2QF-M9XWO":   "7K2QFM9XW0", // O read as zero
		"TSA-7K2QF-M9XIL":   "7K2QFM9X11", // I and L read as one
		"TSA12345XY":        "TSA12345XY", // a bare code that itself starts with TSA
		"TSA-TSA12-345XY":   "TSA12345XY",
		// Ideographic space and the astral Yezidi hyphen: both on the explicit lists.
		"TSA\u30007K2QF\U00010EADM9XWD": "7K2QFM9XWD",
	} {
		if got, ok := NormalizePayCode(in); !ok || got != want {
			t.Fatalf("NormalizePayCode(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "TSA", "7K2QF-M9XW", "7K2QF-M9XWDD", "TSA-7K2QF-M9XWU", "7K2QF-M9XW!",
		"TSA-7K2QF-M9XWD-1", strings.Repeat("A", 100),
		"TSA-7K2QF-M9XWı", "TSA-7K2QF-M9XWſ", "TSA-7K2QF-M9XWß", "7K2QFM9Xß",
		// The Garay hyphen (Unicode 16 Pd) is on none of the three explicit lists:
		// the dash set must not follow the toolchain's Unicode tables. BOM is not a space.
		"7K2QF\U00010D6EM9XWD", "7K2QF\uFEFFM9XWD"} {
		if got, ok := NormalizePayCode(in); ok {
			t.Fatalf("NormalizePayCode(%q) = %q, accepted", in, got)
		}
	}
}

func TestPayCode_FormatGroupsTheCode(t *testing.T) {
	if got := FormatPayCode("7K2QFM9XWD"); got != "TSA-7K2QF-M9XWD" {
		t.Fatalf("FormatPayCode = %q", got)
	}
	if got := FormatPayCode(""); got != "" {
		t.Fatalf("FormatPayCode(\"\") = %q", got)
	}
}

// API, link and open-link payments all go through createOrExisting.
func TestPayCode_EveryKindOfPaymentGetsOneAtCreation(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	api, _, err := s.Create(ctx, m, models.PayModeLive, payIn())
	if err != nil {
		t.Fatal(err)
	}
	link, err := s.CreateLink(ctx, m, models.PayModeTest, payLinkIn())
	if err != nil {
		t.Fatal(err)
	}
	l := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	open, err := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range []*models.PayPayment{api, link, open} {
		stored, err := s.Get(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.PayCode == nil || !payCodeShape.MatchString(*stored.PayCode) || seen[*stored.PayCode] {
			t.Fatalf("%s payment code = %v", p.Source, stored.PayCode)
		}
		seen[*stored.PayCode] = true
	}
}

// A code that collides is drawn again; it is never read as another request
// having stored the same reference (which used to come back as a 500).
func TestPayCode_ACollisionIsRetriedNotMistakenForAReferenceRace(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	stubPayCodes(t, "7K2QFM9XWD", "7K2QFM9XWD", "AB12CD34EF")
	first, _, err := s.Create(ctx, m, models.PayModeLive, payIn())
	if err != nil || *first.PayCode != "7K2QFM9XWD" {
		t.Fatalf("first: %v %v", first, err)
	}
	in := payIn()
	in.Reference = "PNR-OTHER"
	second, created, err := s.Create(ctx, m, models.PayModeLive, in)
	if err != nil || !created || second.Reference != "PNR-OTHER" || *second.PayCode != "AB12CD34EF" {
		t.Fatalf("second: %+v created %v err %v", second, created, err)
	}
	if stored, _ := s.Get(ctx, second.ID); *stored.PayCode != "AB12CD34EF" {
		t.Fatalf("stored code = %v", *stored.PayCode)
	}
}

// The owner's link path retries on ErrPayLinkReferenceTaken; a code collision
// must be retried inside createOrExisting before it ever gets there.
func TestPayCode_ALinkWhoseCodeCollidesIsStillMade(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	stubPayCodes(t, "7K2QFM9XWD", "7K2QFM9XWD", "AB12CD34EF")
	if _, _, err := s.Create(ctx, m, models.PayModeTest, payIn()); err != nil {
		t.Fatal(err)
	}
	link, err := s.CreateLink(ctx, m, models.PayModeTest, payLinkIn())
	if err != nil || *link.PayCode != "AB12CD34EF" {
		t.Fatalf("link %+v err %v", link, err)
	}
}

func TestPayCode_ACodeThatCollidesEveryTimeIsAnError(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	codes := []string{"7K2QFM9XWD"}
	for i := 0; i < payFreshAttempts; i++ {
		codes = append(codes, "7K2QFM9XWD")
	}
	stubPayCodes(t, codes...)
	if _, _, err := s.Create(ctx, m, models.PayModeLive, payIn()); err != nil {
		t.Fatal(err)
	}
	in := payIn()
	in.Reference = "PNR-OTHER"
	_, _, err := s.Create(ctx, m, models.PayModeLive, in)
	if err == nil || !payCodeTaken(err) || errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrPayReferenceConflict) {
		t.Fatalf("err = %v", err)
	}
	var n int64
	s.db.Model(&models.PayPayment{}).Where("reference = ?", "PNR-OTHER").Count(&n)
	if n != 0 {
		t.Fatalf("%d rows stored for the refused payment", n)
	}
}

func TestPayCode_ViewsShowTheFormattedCode(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	stubPayCodes(t, "7K2QFM9XWD")
	p, _, err := s.Create(ctx, m, models.PayModeLive, payIn())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.View(p).PayCode; got != "TSA-7K2QF-M9XWD" {
		t.Fatalf("View.PayCode = %q", got)
	}
	cv, err := s.CheckoutView(ctx, &models.User{ID: uuid.New()}, p.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	pv, err := s.PublicView(ctx, p.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if cv.PayCode != "TSA-7K2QF-M9XWD" || pv.PayCode != "TSA-7K2QF-M9XWD" {
		t.Fatalf("checkout %q public %q", cv.PayCode, pv.PayCode)
	}
	// Nothing to verify until it is paid.
	if cv.ReceiptURL != "" || pv.ReceiptURL != "" || cv.SucceededAt != nil {
		t.Fatalf("pending receipt: checkout %q public %q succeededAt %v", cv.ReceiptURL, pv.ReceiptURL, cv.SucceededAt)
	}
}

func TestPayCode_OnlyASucceededLivePaymentLinksItsReceipt(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	stubPayCodes(t, "7K2QFM9XWD", "AB12CD34EF")
	payer := payUser(t, s, true)
	live, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	if _, _, err := s.Lock(ctx, payer, live.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Succeed(ctx, live.ID, "ref"); err != nil {
		t.Fatal(err)
	}
	cv, err := s.CheckoutView(ctx, payer, live.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	pv, err := s.PublicView(ctx, live.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://tsaconnectworld.com/r/TSA-7K2QF-M9XWD"
	if cv.ReceiptURL != want || pv.ReceiptURL != want || cv.SucceededAt == nil || !cv.SucceededAt.Equal(payTestNow) {
		t.Fatalf("checkout %q public %q succeededAt %v", cv.ReceiptURL, pv.ReceiptURL, cv.SucceededAt)
	}

	in := payIn()
	in.Reference = "TEST-1"
	test, _, _ := s.Create(ctx, m, models.PayModeTest, in)
	if _, _, err := s.Lock(ctx, payer, test.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Succeed(ctx, test.ID, "test"); err != nil {
		t.Fatal(err)
	}
	tv, err := s.PublicView(ctx, test.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	// A test payment has a code but no public receipt: it would vouch for money that never moved.
	if tv.ReceiptURL != "" || tv.PayCode != "TSA-AB12C-D34EF" {
		t.Fatalf("test payment: receipt %q code %q", tv.ReceiptURL, tv.PayCode)
	}
}

func TestPayCode_ALegacySucceededPaymentGetsItsCodeWhenFirstShown(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	stubPayCodes(t, "7K2QFM9XWD", "AB12CD34EF") // a third draw fails the test
	payer := payUser(t, s, true)
	p, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	s.Lock(ctx, payer, p.ID, true)
	s.Succeed(ctx, p.ID, "ref")
	// Made before codes existed.
	s.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Update("pay_code", nil)

	first, err := s.PublicView(ctx, p.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CheckoutView(ctx, payer, p.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Get(ctx, p.ID)
	if first.PayCode != "TSA-AB12C-D34EF" || again.PayCode != first.PayCode || stored.PayCode == nil || *stored.PayCode != "AB12CD34EF" {
		t.Fatalf("first %q again %q stored %v", first.PayCode, again.PayCode, stored.PayCode)
	}
	if again.ReceiptURL != "https://tsaconnectworld.com/r/TSA-AB12C-D34EF" {
		t.Fatalf("receipt %q", again.ReceiptURL)
	}
}

// Review Focus 4: a payment from before codes that is still pending shows no
// code at all (the pages hide the line) and is not written to by a read.
func TestPayCode_ALegacyPendingPaymentShowsNoCode(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	stubPayCodes(t, "7K2QFM9XWD")
	p, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	s.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Update("pay_code", nil)
	pv, err := s.PublicView(ctx, p.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Get(ctx, p.ID)
	if pv.PayCode != "" || stored.PayCode != nil {
		t.Fatalf("view %q stored %v", pv.PayCode, stored.PayCode)
	}
}

// Review Focus 4: two readers of the same legacy payment race; the one whose
// write finds the code already filled keeps the winner's code.
func TestPayCode_EnsureKeepsTheCodeAnotherRequestStoredFirst(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	stubPayCodes(t, "7K2QFM9XWD", "AB12CD34EF")
	p, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	s.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Update("pay_code", nil)
	stale, _ := s.Get(ctx, p.ID) // read before the other request wrote
	s.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Update("pay_code", "ZZ99YY88XX")

	got, err := s.EnsurePayCode(ctx, stale)
	if err != nil || got != "ZZ99YY88XX" || stale.PayCode == nil || *stale.PayCode != "ZZ99YY88XX" {
		t.Fatalf("got %q err %v p.PayCode %v", got, err, stale.PayCode)
	}
}

func TestPayCode_EnsureRetriesACollision(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	stubPayCodes(t, "7K2QFM9XWD", "AB12CD34EF", "7K2QFM9XWD", "QQ77RR66SS")
	s.Create(ctx, m, models.PayModeLive, payIn())
	in := payIn()
	in.Reference = "PNR-OTHER"
	legacy, _, _ := s.Create(ctx, m, models.PayModeLive, in)
	s.db.Model(&models.PayPayment{}).Where("id = ?", legacy.ID).Update("pay_code", nil)
	legacy.PayCode = nil

	got, err := s.EnsurePayCode(ctx, legacy)
	if err != nil || got != "QQ77RR66SS" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestPayCode_EnsureDoesNotTouchUpdatedAt(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	stubPayCodes(t, "7K2QFM9XWD", "AB12CD34EF")
	p, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	s.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Update("pay_code", nil)
	before, _ := s.Get(ctx, p.ID)
	if _, err := s.EnsurePayCode(ctx, before); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(ctx, p.ID)
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("updated_at moved: %v -> %v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestPayCode_PayerHistoryShowsTheCode(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	if err := s.db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	stubPayCodes(t, "7K2QFM9XWD")
	payer := payUser(t, s, true)
	p, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	if _, _, err := s.Lock(ctx, payer, p.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.PayerHistory(ctx, payer.ID)
	if err != nil || len(got) != 1 || got[0].PayCode != "TSA-7K2QF-M9XWD" {
		t.Fatalf("history %+v err %v", got, err)
	}
}

// A payer reads her code to the business, who types it into the dashboard's
// search however it came out.
func TestPayCode_MerchantSearchFindsAPaymentByItsCode(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	stubPayCodes(t, "7K2QFM9XWD", "AB12CD34EF")
	a, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	in := payIn()
	in.Reference = "PNR-OTHER"
	s.Create(ctx, m, models.PayModeLive, in)

	got, total, err := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeLive, Query: "tsa–7k2qf m9xwd"})
	if err != nil || total != 1 || len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("got %d (%v) err %v", total, got, err)
	}
}
