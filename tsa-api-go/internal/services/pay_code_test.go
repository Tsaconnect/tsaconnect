package services

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

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
	} {
		if got, ok := NormalizePayCode(in); !ok || got != want {
			t.Fatalf("NormalizePayCode(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "TSA", "7K2QF-M9XW", "7K2QF-M9XWDD", "TSA-7K2QF-M9XWU", "7K2QF-M9XW!",
		"TSA-7K2QF-M9XWD-1", strings.Repeat("A", 100)} {
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
