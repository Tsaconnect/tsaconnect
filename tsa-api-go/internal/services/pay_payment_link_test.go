package services

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func payLinkIn() CreatePayLinkInput {
	return CreatePayLinkInput{AmountKobo: 2_500_000, Description: "  Wedding cake deposit ", CustomerName: " Ada Obi "}
}

func TestPayLink_CreateMakesALinkPaymentOpenUntilPaidByDefault(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, err := s.CreateLink(context.Background(), m, models.PayModeTest, payLinkIn())
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != models.PaySourceLink || p.CustomerName != "Ada Obi" || p.Description != "Wedding cake deposit" ||
		p.ReturnURL != "" || p.Currency != "NGN" || p.Status != models.PayStatusPending ||
		p.ExpiresAt != nil {
		t.Fatalf("link = %+v", p)
	}
	if !regexp.MustCompile(`^link_[a-z2-7]{12}$`).MatchString(p.Reference) {
		t.Fatalf("generated reference %q", p.Reference)
	}
	// The same split and settlement snapshot an API payment gets.
	if p.CustomerTotalKobo != 2_525_253 || p.MerchantNetKobo != 2_500_000 || p.SettleAccountNumber != "0123456789" {
		t.Fatalf("split/settlement = %+v", p)
	}
	v := s.View(p)
	if v.Source != models.PaySourceLink || v.CustomerName != "Ada Obi" || v.Cancelled {
		t.Fatalf("view = %+v", v)
	}
	other, err := s.CreateLink(context.Background(), m, models.PayModeTest, payLinkIn())
	if err != nil || other.Reference == p.Reference {
		t.Fatalf("a second blank reference: err %v ref %q", err, other.Reference)
	}
}

func TestPayLink_StaysOpenOneThreeOrSevenDays(t *testing.T) {
	s, m := newPayPaymentRig(t)
	for _, days := range []int{1, 3, 7} {
		in := payLinkIn()
		in.ExpiresInDays = days
		p, err := s.CreateLink(context.Background(), m, models.PayModeTest, in)
		if err != nil || !p.ExpiresAt.Equal(payTestNow.Add(time.Duration(days)*24*time.Hour)) {
			t.Fatalf("%d days: err %v expires %v", days, err, p)
		}
	}
	for _, days := range []int{2, 8, 30, -1} {
		in := payLinkIn()
		in.ExpiresInDays = days
		if _, err := s.CreateLink(context.Background(), m, models.PayModeTest, in); !errors.Is(err, ErrPayPaymentInvalid) {
			t.Fatalf("%d days: err = %v", days, err)
		}
	}
}

func TestPayLink_CreateRefusals(t *testing.T) {
	s, m := newPayPaymentRig(t)
	for name, mutate := range map[string]func(*CreatePayLinkInput){
		"no description":         func(in *CreatePayLinkInput) { in.Description = "   " },
		"long description":       func(in *CreatePayLinkInput) { in.Description = strings.Repeat("d", 256) },
		"long customer name":     func(in *CreatePayLinkInput) { in.CustomerName = strings.Repeat("a", 101) },
		"control in the name":    func(in *CreatePayLinkInput) { in.CustomerName = "Ada\nObi" },
		"below the merchant min": func(in *CreatePayLinkInput) { in.AmountKobo = 99_999 },
		"above the merchant max": func(in *CreatePayLinkInput) { in.AmountKobo = 500_000_001 },
		"a reference too long":   func(in *CreatePayLinkInput) { in.Reference = strings.Repeat("r", 129) },
		// Text the payer reads must be what it looks like: no control or
		// invisible format characters in either field.
		"RTL override in the description":     func(in *CreatePayLinkInput) { in.Description = "Cake \u202egnidde" },
		"zero-width in the description":       func(in *CreatePayLinkInput) { in.Description = "Ca\u200bke" },
		"NUL in the description":              func(in *CreatePayLinkInput) { in.Description = "Cake\x00" },
		"tab in the description":              func(in *CreatePayLinkInput) { in.Description = "Cake\tdeposit" },
		"zero-width space in the name":        func(in *CreatePayLinkInput) { in.CustomerName = "Ada\u200bObi" },
		"Hangul filler in the name":           func(in *CreatePayLinkInput) { in.CustomerName = "Ada\u3164" },
		"line separator in the description":   func(in *CreatePayLinkInput) { in.Description = "Cake\u2028deposit" },
		"grapheme joiner in the description":  func(in *CreatePayLinkInput) { in.Description = "Ca\u034fke" },
		"a hidden character in the reference": func(in *CreatePayLinkInput) { in.Reference = "INV\u202e1" },
		"word joiner in the name":             func(in *CreatePayLinkInput) { in.CustomerName = "Ada\u2060Obi" },
		"BOM in the description":              func(in *CreatePayLinkInput) { in.Description = "\ufeffCake" },
		"LTR mark in the description":         func(in *CreatePayLinkInput) { in.Description = "Cake\u200e" },
		"RTL isolate in the name":             func(in *CreatePayLinkInput) { in.CustomerName = "\u2067Ada" },
		"RTL override in the name":            func(in *CreatePayLinkInput) { in.CustomerName = "\u202eAda" },
	} {
		in := payLinkIn()
		mutate(&in)
		if _, err := s.CreateLink(context.Background(), m, models.PayModeTest, in); !errors.Is(err, ErrPayPaymentInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	// One check, two wordings: the owner is told in naira, the API in kobo.
	in := payLinkIn()
	in.AmountKobo = 1
	_, err := s.CreateLink(context.Background(), m, models.PayModeTest, in)
	var invalid *PayInvalidError
	if !errors.As(err, &invalid) || invalid.Human != "Enter an amount between ₦1,000.00 and ₦5,000,000.00" ||
		err.Error() != "invalid payment: amount must be between 100000 and 500000000 kobo" {
		t.Fatalf("amount refusal: %v (%+v)", err, invalid)
	}
	// The zero-width joiner holds a combined emoji together, so it is allowed.
	in = payLinkIn()
	in.Description, in.CustomerName = "Family portrait 👨‍👩‍👧", "The Obis 👨‍👩‍👧"
	if p, err := s.CreateLink(context.Background(), m, models.PayModeTest, in); err != nil || p.CustomerName != "The Obis 👨‍👩‍👧" {
		t.Fatalf("a ZWJ emoji: %v", err)
	}
	in.Description = "Family portrait \u202e👨‍👩‍👧"
	if _, err := s.CreateLink(context.Background(), m, models.PayModeTest, in); !errors.Is(err, ErrPayPaymentInvalid) {
		t.Fatalf("an RTL override beside a ZWJ emoji: %v", err)
	}
	// A name is counted in characters, not bytes: 100 accented letters fit.
	in = payLinkIn()
	in.CustomerName = strings.Repeat("é", 100)
	if _, err := s.CreateLink(context.Background(), m, models.PayModeTest, in); err != nil {
		t.Fatalf("100-character name: %v", err)
	}
	in = payLinkIn()
	in.CustomerName = ""
	if p, err := s.CreateLink(context.Background(), m, models.PayModeTest, in); err != nil || p.CustomerName != "" {
		t.Fatalf("no customer name: %v", err)
	}
	m.SettlementMode = ""
	if _, err := s.CreateLink(context.Background(), m, models.PayModeTest, payLinkIn()); !errors.Is(err, ErrPayMerchantNotReady) {
		t.Fatalf("unconfigured merchant: err = %v", err)
	}
}

// An owner re-using a reference made a mistake; it is never an idempotent
// retry, whichever kind of payment already has it.
func TestPayLink_AReferenceAlreadyUsedIsRefused(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	in := payLinkIn()
	in.Reference = " INV-7 "
	first, err := s.CreateLink(ctx, m, models.PayModeTest, in)
	if err != nil || first.Reference != "INV-7" {
		t.Fatalf("first: %v %+v", err, first)
	}
	if _, err := s.CreateLink(ctx, m, models.PayModeTest, in); !errors.Is(err, ErrPayLinkReferenceTaken) {
		t.Fatalf("the identical link again: err = %v", err)
	}
	if _, _, err := s.Create(ctx, m, models.PayModeTest, payIn()); err != nil {
		t.Fatal(err)
	}
	in.Reference = payIn().Reference
	if _, err := s.CreateLink(ctx, m, models.PayModeTest, in); !errors.Is(err, ErrPayLinkReferenceTaken) {
		t.Fatalf("an API payment's reference: err = %v", err)
	}
	in.Reference = "INV-7"
	if _, err := s.CreateLink(ctx, m, models.PayModeLive, in); err != nil {
		t.Fatalf("the same reference in the other mode: %v", err)
	}
	// The insert-race path answers the same.
	loser := *first
	loser.ID = uuid.Nil
	if _, _, err := s.createOrExisting(ctx, &loser, CreatePayPaymentInput{}); !errors.Is(err, ErrPayLinkReferenceTaken) {
		t.Fatalf("race: err = %v", err)
	}
}

// The API's idempotent retry only ever returns an API payment: its server
// naming a link's reference with the same body is a conflict, not that link.
func TestPayLink_TheAPINeverRetriesIntoALink(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	in := payLinkIn()
	in.Reference = "INV-8"
	link, err := s.CreateLink(ctx, m, models.PayModeTest, in)
	if err != nil {
		t.Fatal(err)
	}
	same := CreatePayPaymentInput{AmountKobo: link.AmountKobo, Currency: "NGN", Reference: link.Reference, Description: link.Description}
	if _, _, err := s.Create(ctx, m, models.PayModeTest, same); !errors.Is(err, ErrPayReferenceConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestPayLink_CancelLinkIsForPendingLinksOnly(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	withWebhooks(t, s, m)
	link, _ := s.CreateLink(ctx, m, models.PayModeTest, payLinkIn())
	api, _, _ := s.Create(ctx, m, models.PayModeTest, payIn())
	busy, _ := s.CreateLink(ctx, m, models.PayModeTest, payLinkIn())
	if _, _, err := s.Lock(ctx, payUser(t, s, false), busy.ID, false); err != nil {
		t.Fatal(err)
	}
	other := seedPayMerchant(t, s.db)

	if err := s.CancelLink(ctx, other.ID, models.PayModeTest, link.ID); !errors.Is(err, ErrPayTransition) {
		t.Fatalf("another merchant's link: err = %v", err)
	}
	if err := s.CancelLink(ctx, m.ID, models.PayModeLive, link.ID); !errors.Is(err, ErrPayTransition) {
		t.Fatalf("the other mode: err = %v", err)
	}
	if err := s.CancelLink(ctx, m.ID, models.PayModeTest, api.ID); !errors.Is(err, ErrPayTransition) {
		t.Fatalf("an API-made payment: err = %v", err)
	}
	if err := s.CancelLink(ctx, m.ID, models.PayModeTest, busy.ID); !errors.Is(err, ErrPayTransition) {
		t.Fatalf("a link being paid: err = %v", err)
	}
	if err := s.CancelLink(ctx, m.ID, models.PayModeTest, link.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, link.ID)
	if got.Status != models.PayStatusExpired || got.FailureReason != PayReasonMerchantCancelled {
		t.Fatalf("cancelled = %+v", got)
	}
	if err := s.CancelLink(ctx, m.ID, models.PayModeTest, link.ID); !errors.Is(err, ErrPayTransition) {
		t.Fatalf("cancelling twice: err = %v", err)
	}
	if ds := deliveries(t, s); len(ds) != 1 || ds[0].PaymentID != link.ID || ds[0].Event != models.PayEventExpired {
		t.Fatalf("deliveries = %+v", ds)
	}
	for _, id := range []uuid.UUID{api.ID, busy.ID} {
		if p, _ := s.Get(ctx, id); p.Status == models.PayStatusExpired {
			t.Fatalf("%s was cancelled", id)
		}
	}
}

// A cancelled payment and one that ran out of time both end expired; the views
// tell them apart.
func TestPayLink_ViewsTellCancelledFromTimedOut(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	cancelled, _ := s.CreateLink(ctx, m, models.PayModeTest, payLinkIn())
	in := payLinkIn()
	in.ExpiresInDays = 1
	lapsed, _ := s.CreateLink(ctx, m, models.PayModeTest, in)
	api, _, _ := s.Create(ctx, m, models.PayModeTest, payIn())
	if err := s.CancelLink(ctx, m.ID, models.PayModeTest, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(ctx, m.ID, models.PayModeTest, api.ID); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return payTestNow.Add(25 * time.Hour) }
	if n, err := s.ExpireDue(ctx, 10); err != nil || n != 1 {
		t.Fatalf("expired %d err %v", n, err)
	}
	for id, want := range map[uuid.UUID]bool{cancelled.ID: true, lapsed.ID: false, api.ID: true} {
		p, _ := s.Get(ctx, id)
		pub, err := s.PublicView(ctx, id, false)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != models.PayStatusExpired || s.View(p).Cancelled != want || pub.Cancelled != want {
			t.Fatalf("%s: status %s view %v public %v, want cancelled=%v", id, p.Status, s.View(p).Cancelled, pub.Cancelled, want)
		}
	}
}

func TestPayLink_PublicAndCheckoutViewsShowWhatAndWhoFor(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	link, _ := s.CreateLink(ctx, m, models.PayModeTest, payLinkIn())
	pub, err := s.PublicView(ctx, link.ID, false)
	if err != nil || pub.Description != "Wedding cake deposit" || pub.CustomerName != "Ada Obi" || pub.Cancelled || pub.SucceededAt != nil ||
		pub.Mode != models.PayModeTest || pub.Source != models.PaySourceLink {
		t.Fatalf("public %+v err %v", pub, err)
	}
	// An API payment's description is the merchant server's own (a booking
	// code, say): never shown to whoever holds the id.
	api, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	pub, err = s.PublicView(ctx, api.ID, false)
	if err != nil || pub.Description != "" || pub.CustomerName != "" || pub.Mode != models.PayModeLive || pub.Source != models.PaySourceAPI {
		t.Fatalf("API payment public %+v err %v", pub, err)
	}
	co, err := s.CheckoutView(ctx, payUser(t, s, false), link.ID, false)
	if err != nil || co.CustomerName != "Ada Obi" {
		t.Fatalf("checkout %+v err %v", co, err)
	}
	if _, err := s.SimulateTestOutcome(ctx, payOwnerOf(t, s, link.ID), link.ID, true); err != nil {
		t.Fatal(err)
	}
	pub, _ = s.PublicView(ctx, link.ID, false)
	if pub.Status != models.PayStatusSucceeded || pub.SucceededAt == nil || !pub.SucceededAt.Equal(payTestNow) || pub.ReturnURL != "" {
		t.Fatalf("paid link public view %+v", pub)
	}
}

// Links are made by owners who may never set a webhook: paying or cancelling
// one then queues nothing and fails nothing.
func TestPayLink_AMerchantWithNoWebhookURLPaysAndCancelsCleanly(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	paid, _ := s.CreateLink(ctx, m, models.PayModeTest, payLinkIn())
	if _, err := s.SimulateTestOutcome(ctx, payOwnerOf(t, s, paid.ID), paid.ID, true); err != nil {
		t.Fatalf("succeed with no webhook url: %v", err)
	}
	live, _ := s.CreateLink(ctx, m, models.PayModeLive, payLinkIn())
	if _, _, err := s.Lock(ctx, payUser(t, s, true), live.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Succeed(ctx, live.ID, "settled"); err != nil {
		t.Fatalf("live succeed with no webhook url: %v", err)
	}
	gone, _ := s.CreateLink(ctx, m, models.PayModeTest, payLinkIn())
	if err := s.CancelLink(ctx, m.ID, models.PayModeTest, gone.ID); err != nil {
		t.Fatalf("cancel with no webhook url: %v", err)
	}
	for _, id := range []uuid.UUID{paid.ID, live.ID} {
		if p, _ := s.Get(ctx, id); p.Status != models.PayStatusSucceeded {
			t.Fatalf("%s: status %s", id, p.Status)
		}
	}
	if ds := deliveries(t, s); len(ds) != 0 {
		t.Fatalf("queued %d deliveries for a merchant with no webhook url", len(ds))
	}
}

func TestPayLink_ListFiltersBySource(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	link, _ := s.CreateLink(ctx, m, models.PayModeLive, payLinkIn())
	api, _, _ := s.Create(ctx, m, models.PayModeLive, payIn())
	for source, want := range map[string][]uuid.UUID{
		models.PaySourceLink: {link.ID},
		models.PaySourceAPI:  {api.ID},
		"":                   {link.ID, api.ID},
	} {
		rows, total, err := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeLive, Source: source})
		if err != nil || int(total) != len(want) || len(rows) != len(want) {
			t.Fatalf("source %q: err %v total %d rows %d", source, err, total, len(rows))
		}
		for _, r := range rows {
			if (source != "" && r.Source != source) || r.ID != want[0] && r.ID != want[len(want)-1] {
				t.Fatalf("source %q listed %+v", source, r)
			}
		}
	}
}

// Every list splits expired in two: payments their merchant called off
// (status=cancelled) and those that ran out of time (status=expired).
func TestPayLink_ListSplitsCancelledFromExpired(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	cancelled, _ := s.CreateLink(ctx, m, models.PayModeTest, payLinkIn())
	in := payLinkIn()
	in.ExpiresInDays = 1
	lapsed, _ := s.CreateLink(ctx, m, models.PayModeTest, in)
	open, _ := s.CreateLink(ctx, m, models.PayModeTest, payLinkIn())
	if err := s.CancelLink(ctx, m.ID, models.PayModeTest, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return payTestNow.Add(25 * time.Hour) }
	if _, err := s.ExpireDue(ctx, 10); err != nil {
		t.Fatal(err)
	}
	for status, want := range map[string][]uuid.UUID{
		PayListCancelled:        {cancelled.ID},
		models.PayStatusExpired: {lapsed.ID},
		models.PayStatusPending: {open.ID},
		"":                      {cancelled.ID, lapsed.ID, open.ID},
	} {
		rows, total, err := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeTest, Status: status})
		if err != nil || int(total) != len(want) {
			t.Fatalf("%q: err %v total %d", status, err, total)
		}
		for _, r := range rows {
			if !slices.Contains(want, r.ID) {
				t.Fatalf("%q listed %s", status, r.ID)
			}
		}
	}
	// The admin console's list is the same list.
	if _, total, _ := s.AdminList(ctx, &m.ID, models.PayModeTest, models.PayStatusExpired, 1, 20); total != 1 {
		t.Fatalf("admin expired: %d", total)
	}
}

func TestPayLink_SearchFindsTheCustomerName(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	link, _ := s.CreateLink(ctx, m, models.PayModeTest, payLinkIn())
	other := payLinkIn()
	other.CustomerName = "Tunde"
	s.CreateLink(ctx, m, models.PayModeTest, other)
	rows, total, err := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeTest, Query: "ada ob"})
	if err != nil || total != 1 || rows[0].ID != link.ID {
		t.Fatalf("err %v total %d", err, total)
	}
}

// Both paths: a NUL in the description or reference is a refusal, not a
// Postgres error (22021) surfacing as a 500; the description is counted in
// characters, as its varchar(255) column counts them.
func TestPayPayment_CreateRefusesNULAndCountsDescriptionCharacters(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	for name, mutate := range map[string]func(*CreatePayPaymentInput){
		"NUL in the description": func(in *CreatePayPaymentInput) { in.Description = "LOS\x00ABV" },
		"NUL in the reference":   func(in *CreatePayPaymentInput) { in.Reference = "PNR\x00" },
		"256 characters":         func(in *CreatePayPaymentInput) { in.Description = strings.Repeat("é", 256) },
	} {
		in := payIn()
		mutate(&in)
		if _, _, err := s.Create(ctx, m, models.PayModeLive, in); !errors.Is(err, ErrPayPaymentInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	in := payIn()
	in.Description = strings.Repeat("é", 255) // 510 bytes, 255 characters
	if _, _, err := s.Create(ctx, m, models.PayModeLive, in); err != nil {
		t.Fatalf("255 accented characters: %v", err)
	}
}
