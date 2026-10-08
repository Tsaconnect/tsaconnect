package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// newPayReceiptRig is newPayPaymentRig with the users column Payer reads to
// tell a deleted account (the shared test users table leaves it out).
func newPayReceiptRig(t *testing.T) (*PayPaymentService, *models.PayMerchant) {
	t.Helper()
	s, m := newPayPaymentRig(t)
	if err := s.db.Exec(`ALTER TABLE users ADD COLUMN account_status TEXT DEFAULT 'active'`).Error; err != nil {
		t.Fatal(err)
	}
	return s, m
}

// paid locks p for payer and settles it, and returns it as stored.
func paid(t *testing.T, s *PayPaymentService, p *models.PayPayment, payer *models.User) *models.PayPayment {
	t.Helper()
	ctx := context.Background()
	if _, _, err := s.Lock(ctx, payer, p.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Succeed(ctx, p.ID, "ref"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// D10 as amended: a payable (pending) payment answers anyone holding its code —
// an untried open-link payment included, whose id its visitor already has —
// however the code is typed.
func TestPayReceipt_IDByCodeAnswersAPendingPaymentForAnyone(t *testing.T) {
	ctx := context.Background()
	s, m := newPayReceiptRig(t)
	stubPayCodes(t, "7K2QFM9XWD", "AB12CD34EF")
	api, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	l := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	untried, err := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false)
	if err != nil {
		t.Fatal(err)
	}
	anyone := payUser(t, s, false)
	for code, want := range map[string]uuid.UUID{"TSA-7K2QF-M9XWD": api.ID, "tsa ab12c d34ef": untried.ID} {
		if got, err := s.IDByCode(ctx, code, anyone); err != nil || got != want {
			t.Fatalf("IDByCode(%q) = %v, %v", code, got, err)
		}
	}
	for _, code := range []string{"TSA-AAAAA-AAAAA", "hello", ""} {
		if _, err := s.IDByCode(ctx, code, anyone); !errors.Is(err, ErrPayCodeNotFound) {
			t.Fatalf("IDByCode(%q) err = %v", code, err)
		}
	}
}

// A receipt's code is shared publicly, so once a payment is no longer payable
// only its payer can turn the code into the id (the bearer of the checkout
// view); to anyone else it is exactly an unknown code.
func TestPayReceipt_IDByCodeAnswersAnUnpayablePaymentOnlyToItsPayer(t *testing.T) {
	ctx := context.Background()
	s, m := newPayReceiptRig(t)
	stubPayCodes(t, "7K2QFM9XWD", "AB12CD34EF", "QQ77RR66SS")
	payer, other := payUser(t, s, true), payUser(t, s, true)
	in := func(ref string) CreatePayPaymentInput { i := payIn(); i.Reference = ref; return i }

	succeeded, _, _ := s.Create(ctx, m, models.PayModeLive, in("PAID"))
	paid(t, s, succeeded, payer) // 7K2QF…
	processing, _, _ := s.Create(ctx, m, models.PayModeLive, in("LOCKED"))
	if _, _, err := s.Lock(ctx, payer, processing.ID, true); err != nil { // AB12C…
		t.Fatal(err)
	}
	failed, _, _ := s.Create(ctx, m, models.PayModeLive, in("FAILED"))
	s.Lock(ctx, payer, failed.ID, true)
	s.Fail(ctx, failed.ID, "declined") // QQ77R…

	for code, want := range map[string]uuid.UUID{"TSA-7K2QF-M9XWD": succeeded.ID, "TSA-AB12C-D34EF": processing.ID, "TSA-QQ77R-R66SS": failed.ID} {
		if got, err := s.IDByCode(ctx, code, payer); err != nil || got != want {
			t.Fatalf("payer IDByCode(%q) = %v, %v", code, got, err)
		}
		if got, err := s.IDByCode(ctx, code, other); !errors.Is(err, ErrPayCodeNotFound) || got != uuid.Nil {
			t.Fatalf("other IDByCode(%q) = %v, %v", code, got, err)
		}
	}
}

// D12/I15: only a succeeded LIVE payment has a public receipt.
func TestPayReceipt_OnlyASucceededLivePaymentHasOne(t *testing.T) {
	ctx := context.Background()
	s, m := newPayReceiptRig(t)
	stubPayCodes(t, "7K2QFM9XWD", "AB12CD34EF", "QQ77RR66SS", "ZZ99YY88XX")
	payer := payUser(t, s, true)
	in := func(ref string) CreatePayPaymentInput { i := payIn(); i.Reference = ref; return i }

	s.Create(ctx, m, models.PayModeLive, in("PENDING")) // 7K2QF…
	test, _, _ := s.Create(ctx, m, models.PayModeTest, in("TEST"))
	paid(t, s, test, payer) // AB12C…
	failed, _, _ := s.Create(ctx, m, models.PayModeLive, in("FAILED"))
	s.Lock(ctx, payer, failed.ID, true)
	s.Fail(ctx, failed.ID, "declined") // QQ77R…
	live, _, _ := s.Create(ctx, m, models.PayModeLive, in("LIVE"))
	paid(t, s, live, payer) // ZZ99Y…

	for _, code := range []string{"TSA-7K2QF-M9XWD", "TSA-AB12C-D34EF", "TSA-QQ77R-R66SS", "TSA-AAAAA-AAAAA", "nope"} {
		if _, err := s.PublicReceipt(ctx, code); !errors.Is(err, ErrPayCodeNotFound) {
			t.Fatalf("PublicReceipt(%q) err = %v", code, err)
		}
	}
	if _, err := s.PublicReceipt(ctx, "TSA-ZZ99Y-Y88XX"); err != nil {
		t.Fatalf("succeeded live: %v", err)
	}
}

func TestPayReceipt_SaysWhoWasPaidHowMuchWhenAndByWhomInInitials(t *testing.T) {
	ctx := context.Background()
	s, m := newPayReceiptRig(t)
	stubPayCodes(t, "7K2QFM9XWD")
	payer := payUser(t, s, true) // verified as "Ada Okafor"
	p, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	p = paid(t, s, p, payer)

	v, err := s.PublicReceipt(ctx, "TSA-7K2QF-M9XWD")
	if err != nil {
		t.Fatal(err)
	}
	if v.Code != "TSA-7K2QF-M9XWD" || v.MerchantName != "Sky Air" || v.Amount != p.CustomerTotalKobo || v.Currency != "NGN" ||
		!v.PaidAt.Equal(payTestNow) || v.PayerInitials != "A.O." || v.RefundStatus != "none" || v.RefundedAmount != "0.00" {
		t.Fatalf("receipt = %+v", v)
	}

	// A deleted account leaves no initials behind.
	s.db.Exec(`UPDATE users SET account_status = ? WHERE id = ?`, models.AccountStatusDeleted, payer.ID)
	if v, _ := s.PublicReceipt(ctx, "TSA-7K2QF-M9XWD"); v.PayerInitials != "" {
		t.Fatalf("deleted payer initials = %q", v.PayerInitials)
	}
}

// The public page's rule (PublicView): a link's description; an open-link
// customer's own note, marked as hers with any web address removed, or the
// link's label when she left none; never an API payment's description (the
// merchant's own booking codes).
func TestPayReceipt_PurposeFollowsThePublicPageRule(t *testing.T) {
	ctx := context.Background()
	s, m := newPayReceiptRig(t)
	stubPayCodes(t, "AAAAAAAAA1", "AAAAAAAAA2", "AAAAAAAAA3", "AAAAAAAAA4", "AAAAAAAAA5")
	payer := payUser(t, s, true)

	api, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	paid(t, s, api, payer)
	link, err := s.CreateLink(ctx, m, models.PayModeLive, payLinkIn())
	if err != nil {
		t.Fatal(err)
	}
	paid(t, s, link, payer)
	l := mustOpenLink(t, s, m, models.PayModeLive, PayOpenLinkInput{Label: "Shop 12"})
	note, err := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000, Note: "see www.evil.example now"}, true)
	if err != nil {
		t.Fatal(err)
	}
	paid(t, s, note, payer)
	bare, err := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, true)
	if err != nil {
		t.Fatal(err)
	}
	paid(t, s, bare, payer)

	for code, want := range map[string]struct {
		desc string
		note bool
	}{
		"TSA-AAAAA-AAAA1": {"", false},
		"TSA-AAAAA-AAAA2": {"Wedding cake deposit", false},
		"TSA-AAAAA-AAAA3": {"see [link removed] now", true},
		"TSA-AAAAA-AAAA4": {"Shop 12", false},
	} {
		v, err := s.PublicReceipt(ctx, code)
		if err != nil || v.Description != want.desc || v.DescriptionIsCustomerNote != want.note {
			t.Fatalf("%s: %+v err %v; want %+v", code, v, err, want)
		}
	}
}

func TestPayReceipt_RefundFieldsComeFromTheRefundHook(t *testing.T) {
	ctx := context.Background()
	s, m := newPayReceiptRig(t)
	stubPayCodes(t, "7K2QFM9XWD")
	p, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	paid(t, s, p, payUser(t, s, true))
	s.SetRefundAggregator(&fakeRefundAgg{byPayment: map[uuid.UUID]PayRefundAgg{
		p.ID: {RefundedMicroUSD: 12_500_000, CapMicroUSD: 30_000_000, Status: "partial"},
	}})
	v, err := s.PublicReceipt(ctx, "TSA-7K2QF-M9XWD")
	if err != nil || v.RefundStatus != "partial" || v.RefundedAmount != "12.50" {
		t.Fatalf("receipt %+v err %v", v, err)
	}
}

func TestPayReceipt_Initials(t *testing.T) {
	for name, want := range map[string]string{
		"Ada Okafor":        "A.O.",
		"ADA CHIOMA OKAFOR": "A.O.",
		"ada":               "A.",
		"  émeka   obi ":    "É.O.",
		"":                  "",
	} {
		if got := payInitials(name); got != want {
			t.Fatalf("payInitials(%q) = %q, want %q", name, got, want)
		}
	}
}
