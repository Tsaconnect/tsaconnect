package services

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

func openLinkIn() PayOpenLinkInput {
	return PayOpenLinkInput{Label: "  Sky Air — Counter 3 "}
}

// fullUpdate is an edit that sends every field, as a form saving the whole
// link does.
func fullUpdate(in PayOpenLinkInput, active *bool, version *int) PayOpenLinkUpdate {
	return PayOpenLinkUpdate{Label: &in.Label, MinAmountKobo: &in.MinAmountKobo, MaxAmountKobo: &in.MaxAmountKobo,
		Active: active, ExpectedVersion: version}
}

func mustOpenLink(t *testing.T, s *PayPaymentService, m *models.PayMerchant, mode string, in PayOpenLinkInput) *models.PayOpenLink {
	t.Helper()
	l, err := s.CreateOpenLink(context.Background(), m, mode, in)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestPayOpenLink_CreateMakesAReadableSlugAndAddress(t *testing.T) {
	s, m := newPayPaymentRig(t)
	l := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	if !regexp.MustCompile(`^sky-air-[a-z2-7]{8}$`).MatchString(l.Slug) || l.Label != "Sky Air — Counter 3" || !l.Active ||
		l.Version != 1 || l.Mode != models.PayModeTest || l.MerchantID != m.ID {
		t.Fatalf("link = %+v", l)
	}
	views, err := s.OpenLinkViews(context.Background(), m, []models.PayOpenLink{*l}, false)
	if err != nil || views[0].URL != "https://tsaconnectworld.com/pay/l/"+l.Slug || views[0].MerchantMinAmount != m.MinAmountKobo ||
		views[0].MerchantMaxAmount != m.MaxAmountKobo || views[0].PaidCount != 0 || !views[0].TakingPayments || views[0].NotTakingReason != "" {
		t.Fatalf("view %+v err %v", views, err)
	}
	if other := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn()); other.Slug == l.Slug {
		t.Fatal("two open links share a slug")
	}
}

func TestPayOpenLink_SlugBase(t *testing.T) {
	for name, want := range map[string]string{
		"Mama Ngozi Store":           "mama-ngozi-store",
		"  Kemi's Cakes & Co. ":      "kemi-s-cakes-co",
		"Ọlá":                        "l",
		"":                           "pay",
		"!!!":                        "pay",
		strings.Repeat("abcde ", 20): "abcde-abcde-abcde-abcde-abcde-abcde-abcd",
	} {
		if got := payOpenLinkSlugBase(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

func TestPayOpenLink_Refusals(t *testing.T) {
	s, m := newPayPaymentRig(t)
	for name, in := range map[string]PayOpenLinkInput{
		"no label":           {Label: "  "},
		"long label":         {Label: strings.Repeat("a", 81)},
		"RTL override":       {Label: "Shop ‮12"},
		"min below merchant": {Label: "Shop", MinAmountKobo: 99_999},
		"max above merchant": {Label: "Shop", MaxAmountKobo: 500_000_001},
		"min more than max":  {Label: "Shop", MinAmountKobo: 300_000, MaxAmountKobo: 200_000},
		"negative min":       {Label: "Shop", MinAmountKobo: -1},
		"max below merchant": {Label: "Shop", MaxAmountKobo: 50},
	} {
		if _, err := s.CreateOpenLink(context.Background(), m, models.PayModeTest, in); !errors.Is(err, ErrPayPaymentInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	_, err := s.CreateOpenLink(context.Background(), m, models.PayModeTest, PayOpenLinkInput{Label: "Shop", MinAmountKobo: 1})
	var invalid *PayInvalidError
	if !errors.As(err, &invalid) || invalid.Human != "The smallest amount must be between ₦1,000.00 and ₦5,000,000.00" {
		t.Fatalf("min refusal: %v", err)
	}
	if _, err := s.CreateOpenLink(context.Background(), m, models.PayModeTest, PayOpenLinkInput{Label: "Shop 👨‍👩‍👧", MinAmountKobo: 200_000, MaxAmountKobo: 200_000}); err != nil {
		t.Fatalf("a one-amount range and a ZWJ emoji: %v", err)
	}
	m.SettlementMode = ""
	if _, err := s.CreateOpenLink(context.Background(), m, models.PayModeTest, openLinkIn()); !errors.Is(err, ErrPayMerchantNotReady) {
		t.Fatalf("no payout set: %v", err)
	}
}

func TestPayOpenLink_UpdateIsAgainstTheVersionRead(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	l := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	v1 := 1
	up := fullUpdate(PayOpenLinkInput{Label: "Counter 4", MinAmountKobo: 200_000}, nil, &v1)
	got, err := s.UpdateOpenLink(ctx, m, models.PayModeTest, l.ID, up)
	if err != nil || got.Label != "Counter 4" || got.MinAmountKobo != 200_000 || got.Version != 2 || !got.Active {
		t.Fatalf("update %+v err %v", got, err)
	}
	if _, err := s.UpdateOpenLink(ctx, m, models.PayModeTest, l.ID, up); !errors.Is(err, ErrPayOpenLinkStale) {
		t.Fatalf("a stale version: %v", err)
	}
	v2, off := 2, false
	up = fullUpdate(PayOpenLinkInput{Label: "Counter 4"}, &off, &v2)
	if got, err = s.UpdateOpenLink(ctx, m, models.PayModeTest, l.ID, up); err != nil || got.Active || got.MinAmountKobo != 0 {
		t.Fatalf("pause %+v err %v", got, err)
	}
	if _, err := s.UpdateOpenLink(ctx, m, models.PayModeTest, l.ID, fullUpdate(openLinkIn(), nil, nil)); !errors.Is(err, ErrPayPaymentInvalid) {
		t.Fatalf("no expectedVersion: %v", err)
	}
	other := seedPayMerchant(t, s.db)
	v3 := 3
	up.ExpectedVersion = &v3
	if _, err := s.UpdateOpenLink(ctx, other, models.PayModeTest, l.ID, up); !errors.Is(err, ErrPayOpenLinkNotFound) {
		t.Fatalf("another merchant: %v", err)
	}
	if _, err := s.UpdateOpenLink(ctx, m, models.PayModeLive, l.ID, up); !errors.Is(err, ErrPayOpenLinkNotFound) {
		t.Fatalf("the other mode: %v", err)
	}
}

// Replacing gives the link a new address and retires the old one; the link
// itself — its settings and its payments — carries on.
func TestPayOpenLink_ReplaceRetiresTheOldAddress(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	l := mustOpenLink(t, s, m, models.PayModeTest, PayOpenLinkInput{Label: "Shop 12", MinAmountKobo: 200_000, MaxAmountKobo: 900_000})
	paid, err := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.ReplaceOpenLink(ctx, m, models.PayModeTest, l.ID)
	if err != nil || next.ID != l.ID || next.Slug == l.Slug || !regexp.MustCompile(`^sky-air-[a-z2-7]{8}$`).MatchString(next.Slug) ||
		next.Label != "Shop 12" || next.MinAmountKobo != 200_000 || next.MaxAmountKobo != 900_000 || !next.Active || next.Version != 2 {
		t.Fatalf("next %+v err %v", next, err)
	}
	if _, err := s.OpenLinkPublic(ctx, l.Slug, false); !errors.Is(err, ErrPayOpenLinkNotFound) {
		t.Fatalf("the old address still reads: %v", err)
	}
	if _, err := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false); !errors.Is(err, ErrPayOpenLinkNotFound) {
		t.Fatalf("paying the old address: %v", err)
	}
	if _, err := s.CreateFromOpenLink(ctx, next.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false); err != nil {
		t.Fatalf("paying the new address: %v", err)
	}
	var through []uuid.UUID
	s.db.Model(&models.PayPayment{}).Where("open_link_id = ?", l.ID).Pluck("id", &through)
	if len(through) != 2 || !slices.Contains(through, paid.ID) {
		t.Fatalf("the link's payments across the replace: %v", through)
	}
	v := 1
	if _, err := s.UpdateOpenLink(ctx, m, models.PayModeTest, l.ID, fullUpdate(openLinkIn(), nil, &v)); !errors.Is(err, ErrPayOpenLinkStale) {
		t.Fatalf("an edit made before the replace: %v", err)
	}
	if _, err := s.ReplaceOpenLink(ctx, seedPayMerchant(t, s.db), models.PayModeTest, l.ID); !errors.Is(err, ErrPayOpenLinkNotFound) {
		t.Fatalf("another merchant's: %v", err)
	}
	if _, err := s.ReplaceOpenLink(ctx, m, models.PayModeLive, l.ID); !errors.Is(err, ErrPayOpenLinkNotFound) {
		t.Fatalf("the other mode: %v", err)
	}
}

func TestPayOpenLink_ListIsTheMerchantsOwnInOneModePaged(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	for i := 0; i < 3; i++ {
		mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	}
	mustOpenLink(t, s, m, models.PayModeLive, openLinkIn())
	mustOpenLink(t, s, seedPayMerchant(t, s.db), models.PayModeTest, openLinkIn())
	ls, total, err := s.OpenLinks(ctx, m.ID, models.PayModeTest, 1, 2)
	if err != nil || total != 3 || len(ls) != 2 {
		t.Fatalf("page 1: total %d len %d err %v", total, len(ls), err)
	}
	if rest, _, _ := s.OpenLinks(ctx, m.ID, models.PayModeTest, 2, 2); len(rest) != 1 {
		t.Fatalf("page 2: %d", len(rest))
	}
}

func TestPayOpenLink_PublicShowsTheEffectiveRangeAndWhetherItIsOpen(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	l := mustOpenLink(t, s, m, models.PayModeTest, PayOpenLinkInput{Label: "Shop 12", MinAmountKobo: 200_000})
	pub, err := s.OpenLinkPublic(ctx, strings.ToUpper(l.Slug), false)
	if err != nil || pub.MerchantName != "Sky Air" || pub.Label != "Shop 12" || pub.Mode != models.PayModeTest ||
		pub.MinAmount != 200_000 || pub.MaxAmount != m.MaxAmountKobo || !pub.Active {
		t.Fatalf("public %+v err %v", pub, err)
	}
	// The merchant's bounds narrowed after the link was made: the link follows.
	s.db.Model(m).Update("max_amount_kobo", 1_000_000)
	if pub, _ := s.OpenLinkPublic(ctx, l.Slug, false); pub.MaxAmount != 1_000_000 {
		t.Fatalf("max %d", pub.MaxAmount)
	}
	if _, err := s.OpenLinkPublic(ctx, "no-such-link", false); !errors.Is(err, ErrPayOpenLinkNotFound) {
		t.Fatalf("unknown slug: %v", err)
	}
	live := mustOpenLink(t, s, m, models.PayModeLive, openLinkIn())
	for name, c := range map[string]struct {
		slug   string
		global bool
		setup  map[string]any
	}{
		"live, globally off": {live.Slug, false, nil},
		"live, merchant off": {live.Slug, true, map[string]any{"live_enabled": false}},
		"suspended":          {l.Slug, false, map[string]any{"status": models.PayMerchantSuspended}},
		"no payout set":      {l.Slug, false, map[string]any{"settlement_mode": ""}},
	} {
		s.db.Model(&models.PayMerchant{}).Where("id = ?", m.ID).Updates(map[string]any{"live_enabled": true, "status": models.PayMerchantActive, "settlement_mode": models.PaySettlementFiat})
		if c.setup != nil {
			s.db.Model(&models.PayMerchant{}).Where("id = ?", m.ID).Updates(c.setup)
		}
		if pub, err := s.OpenLinkPublic(ctx, c.slug, c.global); err != nil || pub.Active {
			t.Fatalf("%s: %+v %v", name, pub, err)
		}
		if _, err := s.CreateFromOpenLink(ctx, c.slug, PayOpenPaymentInput{AmountKobo: 300_000}, c.global); !errors.Is(err, ErrPayOpenLinkClosed) {
			t.Fatalf("%s: paying: %v", name, err)
		}
	}
	s.db.Model(&models.PayMerchant{}).Where("id = ?", m.ID).Updates(map[string]any{"live_enabled": true, "status": models.PayMerchantActive, "settlement_mode": models.PaySettlementFiat})
	if pub, _ := s.OpenLinkPublic(ctx, live.Slug, true); !pub.Active {
		t.Fatal("live, both switches on: closed")
	}
	off := false
	v := 1
	s.UpdateOpenLink(ctx, m, models.PayModeTest, l.ID, fullUpdate(PayOpenLinkInput{Label: "Shop 12"}, &off, &v))
	if pub, _ := s.OpenLinkPublic(ctx, l.Slug, false); pub.Active {
		t.Fatal("a paused link reads open")
	}
}

func TestPayOpenLink_ACustomerStartsTheirOwnPayment(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	l := mustOpenLink(t, s, m, models.PayModeTest, PayOpenLinkInput{Label: "Shop 12", MinAmountKobo: 200_000, MaxAmountKobo: 900_000})

	p, err := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 450_000}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != models.PaySourceOpen || p.OpenLinkID == nil || *p.OpenLinkID != l.ID || p.Mode != models.PayModeTest ||
		!regexp.MustCompile(`^open_[a-z2-7]{12}$`).MatchString(p.Reference) || p.Description != "" ||
		p.ReturnURL != "" || !p.ExpiresAt.Equal(payTestNow.Add(30*time.Minute)) || p.AmountKobo != 450_000 {
		t.Fatalf("payment %+v", p)
	}
	noted, err := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 450_000, Note: "  2 bags of rice "}, false)
	if err != nil || noted.ID == p.ID || noted.Description != "2 bags of rice" {
		t.Fatalf("the same amount again is a new payment: %+v %v", noted, err)
	}
	pub, err := s.PublicView(ctx, noted.ID, false)
	if err != nil || pub.Description != "2 bags of rice" || pub.LinkLabel != "Shop 12" || pub.Source != models.PaySourceOpen ||
		pub.OpenLinkURL != "https://tsaconnectworld.com/pay/l/"+l.Slug {
		t.Fatalf("public view %+v err %v", pub, err)
	}
	// No note: no description — the landing shows it as the customer's words —
	// and the owner's view names the link instead.
	if pub, _ := s.PublicView(ctx, p.ID, false); pub.Description != "" || pub.LinkLabel != "Shop 12" {
		t.Fatalf("public view without a note %+v", pub)
	}
	if views, err := s.Views(ctx, []models.PayPayment{*p, *noted}); err != nil || views[0].LinkLabel != "Shop 12" || views[1].LinkLabel != "Shop 12" ||
		views[0].OpenLinkID == nil || *views[0].OpenLinkID != l.ID {
		t.Fatalf("owner views %+v err %v", views, err)
	}

	for name, in := range map[string]PayOpenPaymentInput{
		"below the link's min": {AmountKobo: 199_999},
		"above the link's max": {AmountKobo: 900_001},
		"no amount":            {},
		"a long note":          {AmountKobo: 450_000, Note: strings.Repeat("n", 141)},
		"a zero-width note":    {AmountKobo: 450_000, Note: "rice​"},
		"a NUL in the note":    {AmountKobo: 450_000, Note: "rice\x00"},
	} {
		if _, err := s.CreateFromOpenLink(ctx, l.Slug, in, false); !errors.Is(err, ErrPayPaymentInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	_, err = s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 1}, false)
	var invalid *PayInvalidError
	if !errors.As(err, &invalid) || invalid.Human != "Enter an amount between ₦2,000.00 and ₦9,000.00" {
		t.Fatalf("the payer is told the link's range in naira: %v", err)
	}
	if _, err := s.CreateFromOpenLink(ctx, "no-such-link", PayOpenPaymentInput{AmountKobo: 450_000}, false); !errors.Is(err, ErrPayOpenLinkNotFound) {
		t.Fatalf("unknown slug: %v", err)
	}
	if rows, total, _ := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeTest, Source: models.PaySourceOpen}); total != 2 || len(rows) != 2 {
		t.Fatalf("source=open listed %d", total)
	}
}

func TestPayOpenLink_ViewsCountWhatWasPaidThroughEachLink(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	// The merchant carries the whole fee, so what they receive is less than
	// the price.
	s.db.Model(m).Update("customer_fee_share_percent", 0)
	a := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	b := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	net := func(amount int64) int64 {
		split, _ := PayAmounts(amount, 100, 0)
		return split.MerchantNetKobo
	}
	pay := func(l *models.PayOpenLink, amount int64, succeed bool) {
		p, err := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: amount}, false)
		if err != nil {
			t.Fatal(err)
		}
		if succeed {
			if _, err := s.SimulateTestOutcome(ctx, payOwnerOf(t, s, p.ID), p.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	pay(a, 300_000, true)
	pay(a, 450_000, true)
	pay(a, 999_000, false) // pending: not paid
	pay(b, 200_000, true)
	ls, _, _ := s.OpenLinks(ctx, m.ID, models.PayModeTest, 1, 20)
	views, err := s.OpenLinkViews(ctx, m, ls, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		want := map[uuid.UUID][3]int64{a.ID: {2, 750_000, net(300_000) + net(450_000)}, b.ID: {1, 200_000, net(200_000)}}[v.ID]
		if v.PaidCount != want[0] || v.PaidTotal != want[1] || v.ReceivedTotal != want[2] {
			t.Fatalf("%s: count %d total %d, want %v", v.ID, v.PaidCount, v.PaidTotal, want)
		}
	}
	if net(200_000) >= 200_000 {
		t.Fatal("the fee split did not take the fee from the merchant")
	}
	// The unpaid one nobody started paying is scan noise: not listed.
	if recent, _ := s.OpenLinkPayments(ctx, a.ID); len(recent) != 2 {
		t.Fatalf("a's recent payments: %d", len(recent))
	}
}

// collectPayAlerts records every event published on bus.
func collectPayAlerts(bus *events.Bus) func() []events.Event {
	var mu sync.Mutex
	var seen []events.Event
	bus.Subscribe("*", func(e events.Event) {
		mu.Lock()
		seen = append(seen, e)
		mu.Unlock()
	})
	return func() []events.Event {
		time.Sleep(50 * time.Millisecond) // the bus delivers on its own goroutine
		mu.Lock()
		defer mu.Unlock()
		return append([]events.Event(nil), seen...)
	}
}

func TestPayReceived_EachPaymentIsAnnouncedOnceToItsOwner(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	bus := events.NewBus()
	seen := collectPayAlerts(bus)
	s.SetEvents(bus)

	test, _, _ := s.Create(ctx, m, models.PayModeTest, payIn())
	if _, err := s.SimulateTestOutcome(ctx, payOwnerOf(t, s, test.ID), test.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Succeed(ctx, test.ID, "again"); !errors.Is(err, ErrPayTransition) {
		t.Fatalf("a second settle: %v", err)
	}
	in := payIn()
	in.Reference = "PNR-LIVE"
	live, _, _ := s.Create(ctx, m, models.PayModeLive, in)
	if _, _, err := s.Lock(ctx, payUser(t, s, true), live.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Succeed(ctx, live.ID, "settled"); err != nil {
		t.Fatal(err)
	}
	in.Reference = "PNR-FAILS"
	failed, _, _ := s.Create(ctx, m, models.PayModeTest, in)
	s.SimulateTestOutcome(ctx, payOwnerOf(t, s, failed.ID), failed.ID, false)

	got := seen()
	if len(got) != 2 {
		t.Fatalf("events %+v: one per succeeded payment, none for a failed one or a second settle", got)
	}
	for i, want := range []struct {
		id           uuid.UUID
		mode, title  string
		messageStart string
	}{
		{test.ID, models.PayModeTest, "Test: ₦50,000.00 received", ""},
		{live.ID, models.PayModeLive, "₦50,000.00 received", "Ada O. paid Sky Air"},
	} {
		e := got[i]
		if e.Type != events.PayPaymentReceived || e.UserID != m.UserID || e.Title != want.title ||
			!strings.HasSuffix(e.Message, " paid Sky Air · LOS-ABV 12 Oct") || !strings.HasPrefix(e.Message, want.messageStart) ||
			e.Data["paymentId"] != want.id.String() || e.Data["mode"] != want.mode || e.Data["source"] != models.PaySourceAPI {
			t.Fatalf("event %d = %+v", i, e)
		}
	}
}

func TestPayReceived_ANilBusIsSilentAndSafe(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	if _, err := s.SimulateTestOutcome(context.Background(), payOwnerOf(t, s, p.ID), p.ID, true); err != nil {
		t.Fatalf("succeed with no bus: %v", err)
	}
}

// The reconciler settles a payment through the same guarded succeed, so it
// announces it exactly as a test outcome does.
func TestPayReceived_TheReconcilersSettleIsAnnounced(t *testing.T) {
	r := newPayCheckoutRig(t)
	bus := events.NewBus()
	seen := collectPayAlerts(bus)
	r.pay.SetEvents(bus)
	r.startedOrder(t, models.PaycrestStatusSettled, nil)
	if err := r.pay.ReconcileTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.pay.ReconcileTick(context.Background())
	if r.get(t).Status != models.PayStatusSucceeded {
		t.Fatalf("status %s", r.get(t).Status)
	}
	got := seen()
	if len(got) != 1 || got[0].Type != events.PayPaymentReceived || got[0].Data["paymentId"] != r.p.ID.String() ||
		got[0].Title != "₦50,000.00 received" {
		t.Fatalf("events %+v", got)
	}
}

// Informational: in the app, by push and by email, and a muted owner is not
// pushed or emailed.
func TestPayReceived_IsDeliveredLikeOtherInformationalAlerts(t *testing.T) {
	if !pushWorthy(events.PayPaymentReceived) || !emailEvents[events.PayPaymentReceived] {
		t.Fatal("payment received must push and email")
	}
	if live, email := deliveryChannels(events.PayPaymentReceived, false, true); !live || email {
		t.Fatalf("email muted: live %v email %v", live, email)
	}
	if live, email := deliveryChannels(events.PayPaymentReceived, true, false); live || email {
		t.Fatalf("all muted: live %v email %v", live, email)
	}
}

// Every visitor who enters an amount makes a payment; the owner sees only the
// ones someone went on to pay, and is sent no webhook for the rest. Once tried,
// a payment stays tried — released and lapsed, it is still the owner's to see.
func TestPayOpenLink_UntriedPaymentsAreScanNoise(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	withWebhooks(t, s, m)
	l := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	untried, _ := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false)
	tried, _ := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false)
	if _, _, err := s.Lock(ctx, payUser(t, s, false), tried.ID, false); err != nil {
		t.Fatal(err)
	}
	api, _, _ := s.Create(ctx, m, models.PayModeTest, payIn())
	owner := func() []uuid.UUID {
		rows, _, err := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeTest, HideUntriedOpen: true})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]uuid.UUID, len(rows))
		for i := range rows {
			ids[i] = rows[i].ID
		}
		return ids
	}
	if ids := owner(); len(ids) != 2 || slices.Contains(ids, untried.ID) {
		t.Fatalf("owner list %v: the untried open payment was listed, or another left out", ids)
	}
	if _, total, _ := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeTest}); total != 3 {
		t.Fatalf("the admin list still sees every payment: %d", total)
	}

	// The payer gives up: released, then everything lapses.
	if err := s.Release(ctx, tried.ID, "the payer went back"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, tried.ID); got.LockedAt != nil || got.AttemptedAt == nil || got.Untried() {
		t.Fatalf("after release: locked %v attempted %v", got.LockedAt, got.AttemptedAt)
	}
	s.now = func() time.Time { return payTestNow.Add(time.Hour) }
	if n, err := s.ExpireDue(ctx, 10); err != nil || n != 3 {
		t.Fatalf("expired %d err %v", n, err)
	}
	for _, id := range []uuid.UUID{untried.ID, tried.ID, api.ID} {
		if p, _ := s.Get(ctx, id); p.Status != models.PayStatusExpired {
			t.Fatalf("%s: %s", id, p.Status)
		}
	}
	ds := deliveries(t, s)
	if len(ds) != 2 || ds[0].PaymentID == untried.ID || ds[1].PaymentID == untried.ID {
		t.Fatalf("deliveries %+v: the tried open payment and the API payment only", ds)
	}
	if ids := owner(); len(ids) != 2 || !slices.Contains(ids, tried.ID) {
		t.Fatalf("owner list after expiry %v: a tried payment stays listed", ids)
	}
}

// Untried open payments lapse in one statement ahead of the per-payment sweep,
// owe no webhook, and never touch a payment someone is paying.
func TestPayOpenLink_UntriedPaymentsExpireInBulk(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	withWebhooks(t, s, m)
	l := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	for i := 0; i < 5; i++ {
		if _, err := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false); err != nil {
			t.Fatal(err)
		}
	}
	paying, _ := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false)
	s.Lock(ctx, payUser(t, s, false), paying.ID, false)
	young, _ := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false)
	s.db.Model(young).Update("expires_at", payTestNow.Add(2*time.Hour))

	s.now = func() time.Time { return payTestNow.Add(time.Hour) }
	if n, err := s.expireUntried(ctx); err != nil || n != 5 {
		t.Fatalf("bulk expired %d err %v", n, err)
	}
	if n, _ := s.expireUntried(ctx); n != 0 {
		t.Fatalf("a second pass expired %d", n)
	}
	var expired int64
	s.db.Model(&models.PayPayment{}).Where("status = ? AND failure_reason = ?", models.PayStatusExpired, "expired").Count(&expired)
	if expired != 5 || len(deliveries(t, s)) != 0 {
		t.Fatalf("expired %d, deliveries %d", expired, len(deliveries(t, s)))
	}
	for _, id := range []uuid.UUID{paying.ID, young.ID} {
		if p, _ := s.Get(ctx, id); p.Status == models.PayStatusExpired {
			t.Fatalf("%s expired", id)
		}
	}
}

func TestPayOpenLink_OwnerViewSaysWhyItIsNotTakingPayments(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	test := mustOpenLink(t, s, m, models.PayModeTest, PayOpenLinkInput{Label: "Shop", MinAmountKobo: 2_000_000})
	live := mustOpenLink(t, s, m, models.PayModeLive, openLinkIn())
	reason := func(l *models.PayOpenLink, global bool) string {
		cur, _ := s.OpenLinkForMerchant(ctx, m.ID, l.Mode, l.ID)
		mm, _ := s.merchants.Get(ctx, m.ID)
		v, err := s.OpenLinkViews(ctx, mm, []models.PayOpenLink{*cur}, global)
		if err != nil {
			t.Fatal(err)
		}
		if v[0].TakingPayments != (v[0].NotTakingReason == "") {
			t.Fatalf("takingPayments %v with reason %q", v[0].TakingPayments, v[0].NotTakingReason)
		}
		return v[0].NotTakingReason
	}
	set := func(fields map[string]any) {
		s.db.Model(&models.PayMerchant{}).Where("id = ?", m.ID).Updates(fields)
	}
	if got := reason(test, false); got != "" {
		t.Fatalf("an open link: %q", got)
	}
	if got := reason(live, false); got != PayOpenLinkLiveOff {
		t.Fatalf("live, globally off: %q", got)
	}
	if got := reason(live, true); got != "" {
		t.Fatalf("live, both on: %q", got)
	}
	off, v := false, 1
	if _, err := s.UpdateOpenLink(ctx, m, models.PayModeTest, test.ID, fullUpdate(PayOpenLinkInput{Label: "Shop", MinAmountKobo: 2_000_000}, &off, &v)); err != nil {
		t.Fatal(err)
	}
	if got := reason(test, false); got != PayOpenLinkPaused {
		t.Fatalf("paused: %q", got)
	}
	set(map[string]any{"settlement_mode": ""})
	if got := reason(test, false); got != PayOpenLinkNoPayout {
		t.Fatalf("no payout: %q", got)
	}
	set(map[string]any{"settlement_mode": models.PaySettlementFiat, "status": models.PayMerchantSuspended})
	if got := reason(test, false); got != PayOpenLinkSuspended {
		t.Fatalf("suspended: %q", got)
	}
}

// The merchant's limits can move after a link's range was set, until no
// amount fits it. The link then takes no payments and never shows an
// impossible range — and the owner can still pause, resume or rename it.
func TestPayOpenLink_ARangeTheMerchantsLimitsNoLongerFit(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	l := mustOpenLink(t, s, m, models.PayModeTest, PayOpenLinkInput{Label: "Big orders", MinAmountKobo: 2_000_000})
	s.db.Model(m).Update("max_amount_kobo", 1_000_000)
	m.MaxAmountKobo = 1_000_000

	pub, err := s.OpenLinkPublic(ctx, l.Slug, false)
	if err != nil || pub.Active || pub.MinAmount != m.MinAmountKobo || pub.MaxAmount != 1_000_000 {
		t.Fatalf("public %+v err %v", pub, err)
	}
	if _, err := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 2_000_000}, false); !errors.Is(err, ErrPayOpenLinkClosed) {
		t.Fatalf("paying: %v", err)
	}
	views, _ := s.OpenLinkViews(ctx, m, []models.PayOpenLink{*l}, false)
	if views[0].TakingPayments || views[0].NotTakingReason != PayOpenLinkRangeOutside {
		t.Fatalf("owner view %+v", views[0])
	}
	off, v := false, 1
	same := PayOpenLinkInput{Label: "Big orders, paused", MinAmountKobo: 2_000_000}
	if _, err := s.UpdateOpenLink(ctx, m, models.PayModeTest, l.ID, fullUpdate(same, &off, &v)); err != nil {
		t.Fatalf("pausing and renaming with the range unchanged: %v", err)
	}
	v = 2
	moved := PayOpenLinkInput{Label: "Big orders", MinAmountKobo: 3_000_000}
	if _, err := s.UpdateOpenLink(ctx, m, models.PayModeTest, l.ID, fullUpdate(moved, nil, &v)); !errors.Is(err, ErrPayPaymentInvalid) {
		t.Fatalf("a changed range is still checked: %v", err)
	}
}

// A replace racing another change to the link is refused as stale, never
// silently applied over it.
func TestPayOpenLink_AReplaceRacingAnEditIsStale(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	l := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	const name = "test:racing_replace"
	raced := false
	if err := s.db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if raced || tx.Statement.Table != "pay_open_links" {
			return
		}
		raced = true
		// Someone else's edit lands between the replace's read and its write.
		tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE pay_open_links SET version = version + 1 WHERE id = ?", l.ID)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Callback().Update().Remove(name) })
	if _, err := s.ReplaceOpenLink(ctx, m, models.PayModeTest, l.ID); !errors.Is(err, ErrPayOpenLinkStale) || !raced {
		t.Fatalf("err = %v (raced %v)", err, raced)
	}
	if got, _ := s.OpenLinkForMerchant(ctx, m.ID, models.PayModeTest, l.ID); got.Slug != l.Slug {
		t.Fatal("a stale replace changed the slug")
	}
}

// A random value that collides is drawn again; one that always collides is an
// error, not a loop.
func TestPayTryFresh_RetriesACollision(t *testing.T) {
	taken := errors.New("taken")
	values := []string{"a", "a", "b"}
	var tried []string
	err := payTryFresh(func() (string, error) { v := values[0]; values = values[1:]; return v, nil },
		func(v string) error {
			tried = append(tried, v)
			if v == "a" {
				return taken
			}
			return nil
		}, func(err error) bool { return errors.Is(err, taken) })
	if err != nil || strings.Join(tried, ",") != "a,a,b" {
		t.Fatalf("err %v tried %v", err, tried)
	}
	n := 0
	err = payTryFresh(func() (string, error) { return "a", nil }, func(string) error { n++; return taken }, func(err error) bool { return errors.Is(err, taken) })
	if !errors.Is(err, taken) || n != payFreshAttempts {
		t.Fatalf("always taken: err %v after %d", err, n)
	}
	// Anything but a collision is returned at once.
	boom := errors.New("boom")
	n = 0
	if err := payTryFresh(func() (string, error) { return "a", nil }, func(string) error { n++; return boom }, func(err error) bool { return errors.Is(err, taken) }); !errors.Is(err, boom) || n != 1 {
		t.Fatalf("err %v after %d", err, n)
	}
}

// With no note, the payment-received alert says what it was for by the link's
// label.
func TestPayReceived_AnOpenPaymentWithNoNoteIsNamedByItsLink(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	bus := events.NewBus()
	seen := collectPayAlerts(bus)
	s.SetEvents(bus)
	l := mustOpenLink(t, s, m, models.PayModeTest, PayOpenLinkInput{Label: "Shop 12"})
	p, _ := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false)
	if _, err := s.SimulateTestOutcome(ctx, payOwnerOf(t, s, p.ID), p.ID, true); err != nil {
		t.Fatal(err)
	}
	got := seen()
	if len(got) != 1 || !strings.HasSuffix(got[0].Message, " paid Sky Air · Shop 12") || got[0].Data["source"] != models.PaySourceOpen {
		t.Fatalf("events %+v", got)
	}
}

// A replaced address is retired for good: no link — not even the one it was
// taken from — is ever handed it again.
func TestPayOpenLink_ARetiredSlugIsNeverReused(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	suffixes := []string{"aaaaaaaa", "bbbbbbbb", "aaaaaaaa", "bbbbbbbb", "cccccccc"}
	prev := payOpenLinkSlugSuffix
	payOpenLinkSlugSuffix = func() (string, error) { v := suffixes[0]; suffixes = suffixes[1:]; return v, nil }
	t.Cleanup(func() { payOpenLinkSlugSuffix = prev })

	l := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn()) // sky-air-aaaaaaaa
	next, err := s.ReplaceOpenLink(ctx, m, models.PayModeTest, l.ID)
	if err != nil || next.Slug != "sky-air-bbbbbbbb" {
		t.Fatalf("replace: %+v %v", next, err)
	}
	var retired models.PayRetiredSlug
	if err := s.db.First(&retired, "slug = ?", "sky-air-aaaaaaaa").Error; err != nil || retired.OpenLinkID != l.ID {
		t.Fatalf("retired %+v err %v", retired, err)
	}
	// The next new link draws the retired slug, then the live one, then a free one.
	other := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	if other.Slug != "sky-air-cccccccc" {
		t.Fatalf("new link got %q", other.Slug)
	}
	if _, err := s.OpenLinkPublic(ctx, "sky-air-aaaaaaaa", false); !errors.Is(err, ErrPayOpenLinkNotFound) {
		t.Fatalf("the retired address: %v", err)
	}
}

// A payment started at an address that has since been replaced never points
// its payer to the new one; a payment at the current address does.
func TestPayOpenLink_AnOldPaymentGetsNoNewAddress(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	l := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	old, _ := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false)
	if old.OpenLinkSlug != l.Slug {
		t.Fatalf("slug stamped %q", old.OpenLinkSlug)
	}
	next, err := s.ReplaceOpenLink(ctx, m, models.PayModeTest, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pub, _ := s.PublicView(ctx, old.ID, false); pub.OpenLinkURL != "" || pub.LinkLabel != l.Label {
		t.Fatalf("old payment's public view %+v", pub)
	}
	fresh, _ := s.CreateFromOpenLink(ctx, next.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false)
	if pub, _ := s.PublicView(ctx, fresh.ID, false); pub.OpenLinkURL != "https://tsaconnectworld.com/pay/l/"+next.Slug {
		t.Fatalf("new payment's public view %+v", pub)
	}
}

func TestPayOpenLink_ValidSlug(t *testing.T) {
	for slug, want := range map[string]bool{
		"sky-air-ab2c3d4e": true, "SKY-AIR-AB2C3D4E": true, "pay-a": true, "a": true,
		"": false, "-sky": false, "sky-": false, "sky--air": false, "sky_air": false, "sky air": false,
		"sky%00air": false, "sky\x00air": false, "sky\xffair": false, "ṡky": false, strings.Repeat("a", 65): false,
	} {
		if got := ValidPayOpenLinkSlug(slug); got != want {
			t.Errorf("%q: %v, want %v", slug, got, want)
		}
	}
}

// An edit carries only what changed: whatever it leaves out stays as it was.
func TestPayOpenLink_AnEditLeavesOutWhatItDoesNotChange(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	l := mustOpenLink(t, s, m, models.PayModeTest, PayOpenLinkInput{Label: "Shop 12", MinAmountKobo: 200_000, MaxAmountKobo: 900_000})
	off, v := false, 1
	got, err := s.UpdateOpenLink(ctx, m, models.PayModeTest, l.ID, PayOpenLinkUpdate{Active: &off, ExpectedVersion: &v})
	if err != nil || got.Label != "Shop 12" || got.MinAmountKobo != 200_000 || got.MaxAmountKobo != 900_000 || got.Active {
		t.Fatalf("pause only: %+v %v", got, err)
	}
	v, hi := 2, int64(800_000)
	got, err = s.UpdateOpenLink(ctx, m, models.PayModeTest, l.ID, PayOpenLinkUpdate{MaxAmountKobo: &hi, ExpectedVersion: &v})
	if err != nil || got.Label != "Shop 12" || got.MinAmountKobo != 200_000 || got.MaxAmountKobo != 800_000 || got.Active {
		t.Fatalf("max only: %+v %v", got, err)
	}
	v, lo := 3, int64(850_000)
	if _, err := s.UpdateOpenLink(ctx, m, models.PayModeTest, l.ID, PayOpenLinkUpdate{MinAmountKobo: &lo, ExpectedVersion: &v}); !errors.Is(err, ErrPayPaymentInvalid) {
		t.Fatalf("a min past the stored max is still checked against it: %v", err)
	}
	blank := " "
	if _, err := s.UpdateOpenLink(ctx, m, models.PayModeTest, l.ID, PayOpenLinkUpdate{Label: &blank, ExpectedVersion: &v}); !errors.Is(err, ErrPayPaymentInvalid) {
		t.Fatalf("a blank label: %v", err)
	}
}

// The payment-received alert quotes a customer's note as theirs, with any link
// in it removed, and names the payer by first name and last initial.
func TestPayReceived_TheCustomersNoteIsQuotedAndDefanged(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	bus := events.NewBus()
	seen := collectPayAlerts(bus)
	s.SetEvents(bus)
	l := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	p, err := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000,
		Note: "rice — see https://evil.example/login and www.evil.example/x please"}, false)
	if err != nil {
		t.Fatal(err)
	}
	payer := payUser(t, s, true) // Ada Okafor, verified
	if _, _, err := s.Lock(ctx, payer, p.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Succeed(ctx, p.ID, "test"); err != nil {
		t.Fatal(err)
	}
	got := seen()
	want := "Ada O. paid Sky Air · Customer's note: “rice — see [link removed] and [link removed] please”"
	if len(got) != 1 || got[0].Message != want {
		t.Fatalf("events %+v\nwant %q", got, want)
	}
}

func TestPayReceived_ADeletedPayerIsACustomer(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	bus := events.NewBus()
	seen := collectPayAlerts(bus)
	s.SetEvents(bus)
	p, _, _ := s.Create(ctx, m, models.PayModeTest, payIn())
	payer := payUser(t, s, true)
	s.db.Exec(`UPDATE users SET account_status = ? WHERE id = ?`, models.AccountStatusDeleted, payer.ID)
	s.Lock(ctx, payer, p.ID, false)
	s.Succeed(ctx, p.ID, "test")
	if got := seen(); len(got) != 1 || !strings.HasPrefix(got[0].Message, "A customer paid Sky Air") {
		t.Fatalf("events %+v", got)
	}
}

func TestPayShortName(t *testing.T) {
	for name, want := range map[string]string{"Ada Okafor": "Ada O.", "Ada Ngozi Okafor": "Ada O.", "Ada": "Ada", " Émile  Zola ": "Émile Z."} {
		if got := payShortName(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

// The merchant API cancels only what the merchant's server created.
func TestPayPayment_TheMerchantAPICancelsOnlyItsOwnPayments(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	link, _ := s.CreateLink(ctx, m, models.PayModeTest, payLinkIn())
	l := mustOpenLink(t, s, m, models.PayModeTest, openLinkIn())
	open, _ := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false)
	api, _, _ := s.Create(ctx, m, models.PayModeTest, payIn())
	for name, id := range map[string]uuid.UUID{"an owner's link": link.ID, "a customer's open payment": open.ID} {
		if err := s.Cancel(ctx, m.ID, models.PayModeTest, id); !errors.Is(err, ErrPayTransition) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := s.Cancel(ctx, m.ID, models.PayModeTest, api.ID); err != nil {
		t.Fatalf("its own: %v", err)
	}
}

func TestPayCheckoutView_NamesTheSourceAndTheLink(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	l := mustOpenLink(t, s, m, models.PayModeTest, PayOpenLinkInput{Label: "Shop 12"})
	p, _ := s.CreateFromOpenLink(ctx, l.Slug, PayOpenPaymentInput{AmountKobo: 300_000}, false)
	v, err := s.CheckoutView(ctx, payUser(t, s, false), p.ID, false)
	if err != nil || v.Source != models.PaySourceOpen || v.LinkLabel != "Shop 12" || v.Description != "" {
		t.Fatalf("checkout %+v err %v", v, err)
	}
	api, _, _ := s.Create(ctx, m, models.PayModeTest, payIn())
	if v, _ := s.CheckoutView(ctx, payUser(t, s, false), api.ID, false); v.Source != models.PaySourceAPI || v.LinkLabel != "" {
		t.Fatalf("api checkout %+v", v)
	}
}

// Reads through an open link are served by indexes.
func TestPayOpenLink_PaymentsHaveTheirIndexes(t *testing.T) {
	db := newPayTestDB(t)
	for _, name := range []string{"idx_pay_payments_open_link_created", "idx_pay_payments_open_link_paid"} {
		if !db.Migrator().HasIndex(&models.PayPayment{}, name) {
			t.Fatalf("pay_payments has no %s", name)
		}
	}
	var ddl string
	db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_pay_payments_open_link_paid'`).Scan(&ddl)
	if !strings.Contains(ddl, "WHERE status = 'succeeded'") {
		t.Fatalf("paid index: %s", ddl)
	}
}
