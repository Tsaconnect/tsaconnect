package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func newPayPaymentRig(t *testing.T) (*PayPaymentService, *models.PayMerchant) {
	t.Helper()
	db := newPayTestDB(t)
	now := func() time.Time { return payTestNow }
	merchants := NewPayMerchantService(db, nil, nil, []byte(strings.Repeat("k", 32)), now)
	return NewPayPaymentService(db, merchants, 100, "https://tsaconnectworld.com/pay", now), seedPayMerchant(t, db)
}

func payIn() CreatePayPaymentInput {
	return CreatePayPaymentInput{AmountKobo: 5_000_000, Currency: "NGN", Reference: "PNR-ABC123",
		Description: "LOS-ABV 12 Oct", ReturnURL: "https://www.skyair.ng/booking/done"}
}

func TestPayPayment_CreateSnapshotsTheSplitAndTarget(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, created, err := s.Create(context.Background(), m, models.PayModeLive, payIn())
	if err != nil || !created {
		t.Fatalf("err %v created %v", err, created)
	}
	if p.CustomerTotalKobo != 5_050_506 || p.MerchantNetKobo != 5_000_000 || p.FeeBps != 100 || p.CustomerFeeSharePercent != 100 ||
		p.SettleBankCode != "058" || p.SettleAccountNumber != "0123456789" || p.SettlementMode != models.PaySettlementFiat ||
		p.Status != models.PayStatusPending || !p.ExpiresAt.Equal(payTestNow.Add(30*time.Minute)) {
		t.Fatalf("payment = %+v", p)
	}
	v := s.View(p)
	if v.AppURL != "tsaconnect://pay?id="+p.ID.String() || v.CheckoutURL != "https://tsaconnectworld.com/pay/"+p.ID.String() || v.Fee != 50_506 {
		t.Fatalf("view = %+v", v)
	}
}

func TestPayPayment_SameReferenceSameBodyIsTheSamePayment(t *testing.T) {
	s, m := newPayPaymentRig(t)
	a, _, _ := s.Create(context.Background(), m, models.PayModeLive, payIn())
	b, created, err := s.Create(context.Background(), m, models.PayModeLive, payIn())
	if err != nil || created || a.ID != b.ID {
		t.Fatalf("retry: err %v created %v", err, created)
	}
	for name, mutate := range map[string]func(*CreatePayPaymentInput){
		"amount":      func(in *CreatePayPaymentInput) { in.AmountKobo = 6_000_000 },
		"description": func(in *CreatePayPaymentInput) { in.Description = "other" },
		"return url":  func(in *CreatePayPaymentInput) { in.ReturnURL = "https://skyair.ng/other" },
	} {
		in := payIn()
		mutate(&in)
		if _, _, err := s.Create(context.Background(), m, models.PayModeLive, in); !errors.Is(err, ErrPayReferenceConflict) {
			t.Fatalf("changed %s: err = %v", name, err)
		}
	}
}

// The insert-race path: another request stored the same reference between our
// pre-check and our insert. We must return that payment, not an error.
func TestPayPayment_AnInsertRaceReturnsTheWinner(t *testing.T) {
	s, m := newPayPaymentRig(t)
	winner, _, _ := s.Create(context.Background(), m, models.PayModeLive, payIn())
	loser := *winner
	loser.ID = [16]byte{}
	got, created, err := s.createOrExisting(context.Background(), &loser, payIn())
	if err != nil || created || got.ID != winner.ID {
		t.Fatalf("err %v created %v got %v", err, created, got)
	}
}

func TestPayPayment_CreateRefusesWhatTheMerchantDidNotAllow(t *testing.T) {
	s, m := newPayPaymentRig(t)
	for name, mutate := range map[string]func(*CreatePayPaymentInput){
		"below min":        func(in *CreatePayPaymentInput) { in.AmountKobo = 99_999 },
		"above max":        func(in *CreatePayPaymentInput) { in.AmountKobo = 500_000_001 },
		"not NGN":          func(in *CreatePayPaymentInput) { in.Currency = "USD" },
		"no reference":     func(in *CreatePayPaymentInput) { in.Reference = " " },
		"long reference":   func(in *CreatePayPaymentInput) { in.Reference = strings.Repeat("r", 129) },
		"foreign return":   func(in *CreatePayPaymentInput) { in.ReturnURL = "https://evil.example/phish" },
		"http return":      func(in *CreatePayPaymentInput) { in.ReturnURL = "http://skyair.ng/done" },
		"lookalike return": func(in *CreatePayPaymentInput) { in.ReturnURL = "https://skyair.ng.evil.example/x" },
		"userinfo return":  func(in *CreatePayPaymentInput) { in.ReturnURL = "https://skyair.ng@evil.example/x" },
		"port return":      func(in *CreatePayPaymentInput) { in.ReturnURL = "https://skyair.ng:8443/x" },
		"expiry too short": func(in *CreatePayPaymentInput) { in.ExpiresInSeconds = 899 },
		"expiry too long":  func(in *CreatePayPaymentInput) { in.ExpiresInSeconds = 86_401 },
	} {
		in := payIn()
		mutate(&in)
		if _, _, err := s.Create(context.Background(), m, models.PayModeLive, in); !errors.Is(err, ErrPayPaymentInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	for i, ok := range []func(*CreatePayPaymentInput){
		func(in *CreatePayPaymentInput) { in.AmountKobo = 100_000 },
		func(in *CreatePayPaymentInput) { in.AmountKobo = 500_000_000 },
		func(in *CreatePayPaymentInput) { in.ExpiresInSeconds = 900 },
		func(in *CreatePayPaymentInput) { in.ExpiresInSeconds = 86_400 },
		func(in *CreatePayPaymentInput) { in.ReturnURL = "https://SKYAIR.NG/x" },
	} {
		in := payIn()
		in.Reference = "OK-" + string(rune('A'+i))
		ok(&in)
		if _, _, err := s.Create(context.Background(), m, models.PayModeLive, in); err != nil {
			t.Fatalf("boundary %d refused: %v", i, err)
		}
	}
	m.SettlementMode = ""
	if _, _, err := s.Create(context.Background(), m, models.PayModeLive, payIn()); !errors.Is(err, ErrPayMerchantNotReady) {
		t.Fatalf("unconfigured merchant: err = %v", err)
	}
}

func TestPayPayment_AMerchantReadsOnlyItsOwnPaymentsInItsOwnMode(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, _, _ := s.Create(context.Background(), m, models.PayModeLive, payIn())
	other := seedPayMerchant(t, s.db)
	if _, err := s.GetForMerchant(context.Background(), other.ID, models.PayModeLive, p.ID); !errors.Is(err, ErrPayPaymentNotFound) {
		t.Fatalf("other merchant: err = %v", err)
	}
	if _, err := s.GetForMerchant(context.Background(), m.ID, models.PayModeTest, p.ID); !errors.Is(err, ErrPayPaymentNotFound) {
		t.Fatalf("a test key read a live payment: err = %v", err)
	}
}

func payUser(t *testing.T, s *PayPaymentService, verified bool) *models.User {
	t.Helper()
	if !verified {
		return &models.User{ID: uuid.New()}
	}
	id := seedVerifiedUser(t, s.db)
	var u models.User
	s.db.First(&u, "id = ?", id)
	return &u
}

// payOwnerOf returns the merchant's own user for a payment — the only kind of
// non-admin user allowed to simulate a test outcome.
func payOwnerOf(t *testing.T, s *PayPaymentService, paymentID uuid.UUID) *models.User {
	t.Helper()
	p, err := s.Get(context.Background(), paymentID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.merchants.Get(context.Background(), p.MerchantID)
	if err != nil {
		t.Fatal(err)
	}
	return &models.User{ID: m.UserID}
}

func deliveries(t *testing.T, s *PayPaymentService) []models.PayWebhookDelivery {
	t.Helper()
	var ds []models.PayWebhookDelivery
	s.db.Order("created_at").Find(&ds)
	return ds
}

func withWebhooks(t *testing.T, s *PayPaymentService, m *models.PayMerchant) {
	t.Helper()
	s.db.Model(m).Updates(map[string]any{"webhook_url": "https://skyair.ng/live", "test_webhook_url": "https://skyair.ng/test"})
	m.WebhookURL, m.TestWebhookURL = "https://skyair.ng/live", "https://skyair.ng/test"
}

func TestPayPayment_LockIsFirstComeAndResumable(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	alice, bob := payUser(t, s, false), payUser(t, s, false)
	if _, fresh, err := s.Lock(context.Background(), alice, p.ID, false); err != nil || !fresh {
		t.Fatalf("first lock: %v fresh=%v", err, fresh)
	}
	if _, fresh, err := s.Lock(context.Background(), alice, p.ID, false); err != nil || fresh {
		t.Fatalf("the same payer resuming: %v fresh=%v", err, fresh)
	}
	if _, _, err := s.Lock(context.Background(), bob, p.ID, false); !errors.Is(err, ErrPayNotPayable) {
		t.Fatalf("second payer: err = %v", err)
	}
	bv, _ := s.CheckoutView(context.Background(), bob, p.ID, false)
	av, _ := s.CheckoutView(context.Background(), alice, p.ID, false)
	if bv.Payable || bv.YouArePaying || !strings.Contains(bv.NotPayableWhy, "Someone else") || !av.YouArePaying {
		t.Fatalf("bob %+v alice %+v", bv, av)
	}
}

func TestPayPayment_LockRefusals(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	s.now = func() time.Time { return p.ExpiresAt.Add(-payMinTimeToStart + time.Second) }
	if _, _, err := s.Lock(context.Background(), payUser(t, s, false), p.ID, false); !errors.Is(err, ErrPayNotPayable) {
		t.Fatalf("< 5 min left: err = %v", err)
	}
	s.now = func() time.Time { return p.ExpiresAt.Add(-payMinTimeToStart) }
	if _, _, err := s.Lock(context.Background(), payUser(t, s, false), p.ID, false); err != nil {
		t.Fatalf("exactly 5 min left must be allowed: %v", err)
	}
	s.now = func() time.Time { return payTestNow }

	in := payIn()
	in.Reference = "LIVE-1"
	live, _, _ := s.Create(context.Background(), m, models.PayModeLive, in)
	if _, _, err := s.Lock(context.Background(), payUser(t, s, true), live.ID, false); !errors.Is(err, ErrPayNotPayable) {
		t.Fatalf("live while disabled: err = %v", err)
	}
	if _, _, err := s.Lock(context.Background(), payUser(t, s, false), live.ID, true); !errors.Is(err, ErrKYCVerificationRequired) {
		t.Fatalf("unverified live payer: err = %v", err)
	}
	s.db.Model(m).Update("status", models.PayMerchantSuspended)
	if _, _, err := s.Lock(context.Background(), payUser(t, s, true), live.ID, true); !errors.Is(err, ErrPayNotPayable) {
		t.Fatalf("suspended merchant: err = %v", err)
	}
}

// Every operation against every state: only the allowed ones change anything,
// and each terminal transition queues exactly one webhook.
func TestPayPayment_TransitionTable(t *testing.T) {
	ops := map[string]func(s *PayPaymentService, p *models.PayPayment) error{
		"lock": func(s *PayPaymentService, p *models.PayPayment) error {
			_, _, err := s.Lock(context.Background(), &models.User{ID: uuid.New()}, p.ID, false)
			return err
		},
		"release": func(s *PayPaymentService, p *models.PayPayment) error {
			return s.Release(context.Background(), p.ID, "x")
		},
		"succeed": func(s *PayPaymentService, p *models.PayPayment) error {
			return s.Succeed(context.Background(), p.ID, "ref")
		},
		"fail": func(s *PayPaymentService, p *models.PayPayment) error { return s.Fail(context.Background(), p.ID, "x") },
		"cancel": func(s *PayPaymentService, p *models.PayPayment) error {
			return s.Cancel(context.Background(), p.MerchantID, p.Mode, p.ID)
		},
	}
	allowed := map[string]map[string]string{ // from -> op -> resulting status
		models.PayStatusPending:    {"lock": models.PayStatusProcessing, "cancel": models.PayStatusExpired},
		models.PayStatusProcessing: {"release": models.PayStatusPending, "succeed": models.PayStatusSucceeded, "fail": models.PayStatusFailed},
		models.PayStatusSucceeded:  {},
		models.PayStatusFailed:     {},
		models.PayStatusExpired:    {},
	}
	events := map[string]int{"succeed": 1, "fail": 1, "cancel": 1}
	for from, allowedOps := range allowed {
		for op, run := range ops {
			s, m := newPayPaymentRig(t)
			withWebhooks(t, s, m)
			p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
			s.db.Model(p).Updates(map[string]any{"status": from, "payer_user_id": uuid.New()})
			err := run(s, p)
			got, _ := s.Get(context.Background(), p.ID)
			want, ok := allowedOps[op]
			if ok && (err != nil || got.Status != want) {
				t.Fatalf("%s from %s: err %v status %s, want %s", op, from, err, got.Status, want)
			}
			if !ok && (err == nil || got.Status != from) {
				t.Fatalf("%s from %s must be refused and change nothing: err %v status %s", op, from, err, got.Status)
			}
			wantEvents := 0
			if ok {
				wantEvents = events[op]
			}
			if n := len(deliveries(t, s)); n != wantEvents {
				t.Fatalf("%s from %s queued %d webhooks, want %d", op, from, n, wantEvents)
			}
		}
	}
}

func TestPayPayment_ReleaseClearsThePayerAndTheAttempt(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	s.Lock(context.Background(), payUser(t, s, false), p.ID, false)
	if err := s.LinkOrder(context.Background(), p.ID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkIntent(context.Background(), p.ID, uuid.New()); !errors.Is(err, ErrPayInProgress) {
		t.Fatalf("a second attempt: err = %v", err)
	}
	s.Release(context.Background(), p.ID, "x")
	got, _ := s.Get(context.Background(), p.ID)
	if got.PayerUserID != nil || got.LockedAt != nil || got.PaycrestOrderID != nil || got.SellIntentID != nil {
		t.Fatalf("after release: %+v", got)
	}
	if _, _, err := s.Lock(context.Background(), payUser(t, s, false), p.ID, false); err != nil {
		t.Fatalf("another customer may pay after release: %v", err)
	}
}

func TestPayPayment_WebhooksAreModeIsolatedAndCarryLivemode(t *testing.T) {
	s, m := newPayPaymentRig(t)
	withWebhooks(t, s, m)
	test, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	s.SimulateTestOutcome(context.Background(), payOwnerOf(t, s, test.ID), test.ID, true)
	ds := deliveries(t, s)
	if len(ds) != 1 || ds[0].Mode != models.PayModeTest || !strings.Contains(ds[0].Payload, `"livemode":false`) ||
		!strings.Contains(ds[0].Payload, `"id":"`+ds[0].ID.String()+`"`) || !strings.Contains(ds[0].Payload, `"settlementRef":"test"`) {
		t.Fatalf("deliveries = %+v", ds)
	}
	s.db.Model(m).Update("test_webhook_url", "")
	m.TestWebhookURL = ""
	in := payIn()
	in.Reference = "PNR-2"
	quiet, _, _ := s.Create(context.Background(), m, models.PayModeTest, in)
	s.SimulateTestOutcome(context.Background(), payOwnerOf(t, s, quiet.ID), quiet.ID, false)
	if n := len(deliveries(t, s)); n != 1 {
		t.Fatalf("a test event with no test URL was queued (%d)", n)
	}
}

func TestPayPayment_ExpireDueTouchesOnlyPendingPastExpiry(t *testing.T) {
	s, m := newPayPaymentRig(t)
	withWebhooks(t, s, m)
	pending, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	in := payIn()
	in.Reference = "PNR-2"
	inFlight, _, _ := s.Create(context.Background(), m, models.PayModeTest, in)
	payer := payUser(t, s, false)
	s.Lock(context.Background(), payer, inFlight.ID, false)
	s.now = func() time.Time { return pending.ExpiresAt } // exactly at expiry: not yet
	if n, _ := s.ExpireDue(context.Background(), 50); n != 0 {
		t.Fatal("expired at exactly expires_at")
	}
	s.now = func() time.Time { return payTestNow.Add(31 * time.Minute) }
	if n, err := s.ExpireDue(context.Background(), 50); err != nil || n != 1 {
		t.Fatalf("expired %d, err %v", n, err)
	}
	a, _ := s.Get(context.Background(), pending.ID)
	b, _ := s.Get(context.Background(), inFlight.ID)
	if a.Status != models.PayStatusExpired || b.Status != models.PayStatusProcessing {
		t.Fatalf("statuses %s / %s — a payment in flight must never expire", a.Status, b.Status)
	}
	if _, fresh, err := s.Lock(context.Background(), payer, b.ID, false); err != nil || fresh {
		t.Fatalf("the payer resuming after expires_at: %v", err)
	}
	if ds := deliveries(t, s); len(ds) != 1 || ds[0].Event != models.PayEventExpired || ds[0].PaymentID != pending.ID {
		t.Fatalf("deliveries = %+v", ds)
	}
}

func TestPayPayment_SuspensionExpiresPendingPayments(t *testing.T) {
	s, m := newPayPaymentRig(t)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	if n, err := s.ExpirePendingForMerchant(context.Background(), m.ID, "", "merchant suspended"); err != nil || n != 1 {
		t.Fatalf("n %d err %v", n, err)
	}
	if got, _ := s.Get(context.Background(), p.ID); got.Status != models.PayStatusExpired {
		t.Fatalf("status %s", got.Status)
	}
}

func TestPayPayment_SucceedRollsBackWhenTheWebhookRowCannotBeWritten(t *testing.T) {
	s, m := newPayPaymentRig(t)
	withWebhooks(t, s, m)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	s.Lock(context.Background(), payUser(t, s, false), p.ID, false)
	s.db.Migrator().DropTable(&models.PayWebhookDelivery{})
	if err := s.Succeed(context.Background(), p.ID, "x"); err == nil {
		t.Fatal("succeeded without its webhook row")
	}
	if got, _ := s.Get(context.Background(), p.ID); got.Status != models.PayStatusProcessing {
		t.Fatalf("status %s — the transition must roll back with the webhook", got.Status)
	}
}

func TestPayPayment_SimulateTestOutcomeIsTestOnly(t *testing.T) {
	s, m := newPayPaymentRig(t)
	in := payIn()
	in.Reference = "LIVE-2"
	live, _, _ := s.Create(context.Background(), m, models.PayModeLive, in)
	if _, err := s.SimulateTestOutcome(context.Background(), payUser(t, s, true), live.ID, true); !errors.Is(err, ErrPayNotPayable) {
		t.Fatalf("simulating a live payment: err = %v", err)
	}
	test, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	got, err := s.SimulateTestOutcome(context.Background(), payOwnerOf(t, s, test.ID), test.ID, false)
	if err != nil || got.Status != models.PayStatusFailed || got.FailureReason != "declined in test mode" {
		t.Fatalf("err %v payment %+v", err, got)
	}
}

func TestPayPayment_SimulateTestOutcomeReleasesTheLockWhenResolvingFails(t *testing.T) {
	s, m := newPayPaymentRig(t)
	withWebhooks(t, s, m)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	s.db.Migrator().DropTable(&models.PayWebhookDelivery{})
	if _, err := s.SimulateTestOutcome(context.Background(), payOwnerOf(t, s, p.ID), p.ID, true); err == nil {
		t.Fatal("resolved without its webhook row")
	}
	got, _ := s.Get(context.Background(), p.ID)
	if got.Status != models.PayStatusPending || got.PayerUserID != nil || got.LockedAt != nil {
		t.Fatalf("payment stranded after a failed resolve: %+v", got)
	}
}

func TestPayPayment_SimulateTestOutcomeLeavesAnEarlierLockAloneOnFailure(t *testing.T) {
	s, m := newPayPaymentRig(t)
	withWebhooks(t, s, m)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	alice := payOwnerOf(t, s, p.ID)
	s.Lock(context.Background(), alice, p.ID, false) // Alice's own earlier lock: resume is not fresh
	s.db.Migrator().DropTable(&models.PayWebhookDelivery{})
	if _, err := s.SimulateTestOutcome(context.Background(), alice, p.ID, true); err == nil {
		t.Fatal("resolved without its webhook row")
	}
	if got, _ := s.Get(context.Background(), p.ID); got.Status != models.PayStatusProcessing {
		t.Fatalf("status %s: a resumed (not fresh) lock must not be released", got.Status)
	}
}

// lockAt puts a payment into processing with the given lock time and mode,
// bypassing Lock so a test can age it.
func lockAt(t *testing.T, s *PayPaymentService, m *models.PayMerchant, mode, ref string, at time.Time, extra map[string]any) *models.PayPayment {
	t.Helper()
	in := payIn()
	in.Reference = ref
	p, _, err := s.Create(context.Background(), m, mode, in)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{"status": models.PayStatusProcessing, "payer_user_id": uuid.New(), "locked_at": at}
	for k, v := range extra {
		fields[k] = v
	}
	if err := s.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Updates(fields).Error; err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPayPayment_ReleaseStuckTestPaymentsTouchesOnlyOldTestOnes(t *testing.T) {
	s, m := newPayPaymentRig(t)
	old := payTestNow.Add(-30 * time.Minute)
	stuck := lockAt(t, s, m, models.PayModeTest, "OLD-TEST", old, nil)
	young := lockAt(t, s, m, models.PayModeTest, "YOUNG-TEST", payTestNow.Add(-2*time.Minute), nil)
	live := lockAt(t, s, m, models.PayModeLive, "OLD-LIVE", old, nil)
	linked := lockAt(t, s, m, models.PayModeTest, "OLD-LINKED", old, map[string]any{"paycrest_order_id": uuid.New()})

	n, err := s.ReleaseStuckTestPayments(context.Background(), 10*time.Minute, 100)
	if err != nil || n != 1 {
		t.Fatalf("released %d err %v, want 1", n, err)
	}
	status := func(id uuid.UUID) string { got, _ := s.Get(context.Background(), id); return got.Status }
	if status(stuck.ID) != models.PayStatusPending || status(young.ID) != models.PayStatusProcessing ||
		status(live.ID) != models.PayStatusProcessing || status(linked.ID) != models.PayStatusProcessing {
		t.Fatalf("stuck %s young %s live %s linked %s", status(stuck.ID), status(young.ID), status(live.ID), status(linked.ID))
	}
	got, _ := s.Get(context.Background(), stuck.ID)
	if got.PayerUserID != nil || got.LockedAt != nil {
		t.Fatalf("release left the lock behind: %+v", got)
	}
}

func TestPayPayment_ReconcileTickReleasesStuckTestPayments(t *testing.T) {
	s, m := newPayPaymentRig(t)
	stuck := lockAt(t, s, m, models.PayModeTest, "OLD-TEST", payTestNow.Add(-payStuckTestReleaseAfter-time.Minute), nil)
	if err := s.ReconcileTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(context.Background(), stuck.ID); got.Status != models.PayStatusPending {
		t.Fatalf("status %s, want pending after a reconcile tick", got.Status)
	}
}

func TestPayPayment_CheckoutViewSaysWhenALivePayerIsNotVerified(t *testing.T) {
	s, m := newPayPaymentRig(t)
	in := payIn()
	in.Reference = "LIVE-KYC"
	live, _, _ := s.Create(context.Background(), m, models.PayModeLive, in)
	v, err := s.CheckoutView(context.Background(), payUser(t, s, false), live.ID, true)
	if err != nil || v.Payable || v.NotPayableWhy != "Verify your identity to pay this" || v.NotPayableCode != KYCVerificationRequiredCode {
		t.Fatalf("unverified: %+v err %v", v, err)
	}
	// Verified with no legal name: Lock would refuse, so say so here — and that
	// only support can fix it, not another verification.
	nameless := payUser(t, s, true)
	if err := s.db.Exec(`UPDATE users SET verified_legal_name = '' WHERE id = ?`, nameless.ID).Error; err != nil {
		t.Fatal(err)
	}
	if v, _ := s.CheckoutView(context.Background(), nameless, live.ID, true); v.Payable || v.NotPayableCode != KYCLegalNameMissingCode {
		t.Fatalf("verified without a legal name: %+v", v)
	}
	v, _ = s.CheckoutView(context.Background(), payUser(t, s, true), live.ID, true)
	if !v.Payable || v.NotPayableCode != "" {
		t.Fatalf("verified payer must be payable: %+v", v)
	}
	// Test mode never needs KYC; an anonymous viewer is not asked either.
	test, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	if v, _ := s.CheckoutView(context.Background(), payOwnerOf(t, s, test.ID), test.ID, true); !v.Payable {
		t.Fatalf("test-mode payment must not ask for KYC: %+v", v)
	}
	if v, _ := s.CheckoutView(context.Background(), nil, live.ID, true); !v.Payable {
		t.Fatalf("no viewer: %+v", v)
	}
}

func TestPayPayment_CheckoutViewDoesNotReVerifyThePayerAlreadyHoldingTheLock(t *testing.T) {
	s, m := newPayPaymentRig(t)
	in := payIn()
	in.Reference = "LIVE-HELD"
	live, _, _ := s.Create(context.Background(), m, models.PayModeLive, in)
	payer := payUser(t, s, true)
	if _, _, err := s.Lock(context.Background(), payer, live.ID, true); err != nil {
		t.Fatal(err)
	}
	s.db.Table("users").Where("id = ?", payer.ID).Update("verification_status", "pending")
	if v, _ := s.CheckoutView(context.Background(), payer, live.ID, true); !v.Payable || !v.YouArePaying {
		t.Fatalf("the holder must still see their own payment: %+v", v)
	}
}

func TestPayPayment_SimulateTestOutcomeIsForTheMerchantOwnerOrAnAdmin(t *testing.T) {
	s, m := newPayPaymentRig(t)
	withWebhooks(t, s, m)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())

	stranger := payUser(t, s, false)
	if _, err := s.SimulateTestOutcome(context.Background(), stranger, p.ID, true); !errors.Is(err, ErrPayForbidden) {
		t.Fatalf("stranger: err = %v, want ErrPayForbidden", err)
	}
	if _, err := s.SimulateTestOutcome(context.Background(), nil, p.ID, true); !errors.Is(err, ErrPayForbidden) {
		t.Fatalf("nil user: err = %v, want ErrPayForbidden", err)
	}
	if got, _ := s.Get(context.Background(), p.ID); got.Status != models.PayStatusPending || len(deliveries(t, s)) != 0 {
		t.Fatalf("a refused simulation must not lock, resolve or queue a webhook (status %s)", got.Status)
	}

	admin := payUser(t, s, false)
	admin.Role = models.RoleAdmin
	if got, err := s.SimulateTestOutcome(context.Background(), admin, p.ID, true); err != nil || got.Status != models.PayStatusSucceeded {
		t.Fatalf("admin: %v %v", got, err)
	}
}

func TestPayPayment_CheckoutViewHidesTheReturnURLUntilSucceeded(t *testing.T) {
	s, m := newPayPaymentRig(t)
	withWebhooks(t, s, m)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	stranger := payUser(t, s, false)

	v, err := s.CheckoutView(context.Background(), stranger, p.ID, false)
	if err != nil || v.ReturnURL != "" {
		t.Fatalf("pending: returnUrl = %q, err = %v; want it withheld", v.ReturnURL, err)
	}
	if _, err := s.SimulateTestOutcome(context.Background(), payOwnerOf(t, s, p.ID), p.ID, true); err != nil {
		t.Fatal(err)
	}
	if v, _ = s.CheckoutView(context.Background(), stranger, p.ID, false); v.ReturnURL == "" {
		t.Fatal("succeeded: returnUrl should be shown")
	}
}

func TestPayPayment_ListFiltersByMerchantModeStatusAndSearch(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	mk := func(ref, desc, mode string) *models.PayPayment {
		in := payIn()
		in.Reference, in.Description = ref, desc
		p, _, err := s.Create(ctx, m, mode, in)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := mk("PNR-AAA111", "LOS-ABV 12 Oct", models.PayModeLive)
	mk("PNR-BBB222", "LOS-PHC 13 Oct", models.PayModeLive)
	mk("PNR-CCC333", "LOS-ABV test", models.PayModeTest)
	mk("PNR-100%_\\", "wildcards", models.PayModeLive)
	other := seedPayMerchant(t, s.db)
	if _, _, err := s.Create(ctx, other, models.PayModeLive, payIn()); err != nil {
		t.Fatal(err)
	}

	rows, total, err := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeLive, Query: "abv"})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != a.ID {
		t.Fatalf("err %v total %d rows %+v: search must match description, case-insensitively, within merchant and mode", err, total, rows)
	}
	if _, total, _ := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeLive, Query: "pnr-bbb"}); total != 1 {
		t.Fatalf("reference search: total %d", total)
	}
	if _, total, _ := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeLive, Query: a.ID.String()[:8]}); total != 1 {
		t.Fatalf("id-prefix search: total %d", total)
	}
	if _, total, _ := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeLive, Query: "%"}); total != 1 {
		t.Fatalf("a %% in the search is a literal, not a wildcard: total %d", total)
	}
	if _, total, _ := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeLive, Query: "_"}); total != 1 {
		t.Fatalf("an _ in the search is a literal, not a wildcard: total %d", total)
	}
	if _, total, _ := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeLive}); total != 3 {
		t.Fatalf("all live: total %d — another merchant's or test payments leaked in", total)
	}
	if _, total, _ := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeLive, Status: models.PayStatusSucceeded}); total != 0 {
		t.Fatalf("status filter: total %d", total)
	}
}

// The owner's payments list filters by merchant and mode and sorts newest
// first; one index serves all three.
func TestPayPayment_OwnerListHasItsIndex(t *testing.T) {
	db := newPayTestDB(t)
	if !db.Migrator().HasIndex(&models.PayPayment{}, "idx_pay_payments_merchant_mode_created") {
		t.Fatal("pay_payments has no (merchant_id, mode, created_at) index")
	}
	var ddl string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_pay_payments_merchant_mode_created'`).Scan(&ddl).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(ddl, "`", ""), "(merchant_id,mode,created_at)") {
		t.Fatalf("index columns: %s", ddl)
	}
}

func TestPayPayment_CheckoutViewCarriesTheLinkedOrderStatusForThePayerOnly(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	if err := s.db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	in := payIn()
	in.Reference = "LIVE-STATUS"
	live, _, _ := s.Create(ctx, m, models.PayModeLive, in)
	payer := payUser(t, s, true)
	if _, _, err := s.Lock(ctx, payer, live.ID, true); err != nil {
		t.Fatal(err)
	}
	link := func(owner uuid.UUID, status string) {
		t.Helper()
		orderID := uuid.New()
		row := models.PaycrestOrderRecord{ID: orderID, UserID: owner, Direction: "sell", Status: status, Reference: "PCS-" + orderID.String()}
		if err := s.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Model(&models.PayPayment{}).Where("id = ?", live.ID).Update("paycrest_order_id", orderID).Error; err != nil {
			t.Fatal(err)
		}
	}

	if v, err := s.CheckoutView(ctx, payer, live.ID, true); err != nil || v.OrderStatus != "" {
		t.Fatalf("no order yet: status %q, err %v", v.OrderStatus, err)
	}

	link(payer.ID, "settling")
	if v, _ := s.CheckoutView(ctx, payer, live.ID, true); v.OrderStatus != "settling" || v.OrderID == nil {
		t.Fatalf("payer: %+v, want the order and its status", v)
	}
	stranger := payUser(t, s, true)
	if v, _ := s.CheckoutView(ctx, stranger, live.ID, true); v.OrderStatus != "" || v.OrderID != nil {
		t.Fatalf("stranger must see neither the order nor its status: %+v", v)
	}

	// An order row that is not the payer's is never read through their payment.
	link(stranger.ID, "settled")
	if v, _ := s.CheckoutView(ctx, payer, live.ID, true); v.OrderStatus != "" {
		t.Fatalf("someone else's order leaked its status: %q", v.OrderStatus)
	}
}

func TestPayPayment_PayerHistoryIsHerOwnLivePaymentsAsPayments(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	if err := s.db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	payer := payUser(t, s, true)
	other := payUser(t, s, true)
	pay := func(ref string, who *models.User) *models.PayPayment {
		t.Helper()
		in := payIn()
		in.Reference = ref
		p, _, err := s.Create(ctx, m, models.PayModeLive, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Lock(ctx, who, p.ID, true); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := pay("LIVE-1", payer)
	second := pay("LIVE-2", payer)
	pay("LIVE-OTHER", other)
	released := pay("LIVE-RELEASED", payer)
	// A released attempt moved none of her money: it is not hers to list.
	s.db.Model(&models.PayPayment{}).Where("id = ?", released.ID).Updates(map[string]any{"payer_user_id": nil, "status": models.PayStatusPending})
	// She paid `first` later than `second`, though the merchant raised it earlier.
	s.db.Model(&models.PayPayment{}).Where("id = ?", first.ID).Update("locked_at", payTestNow.Add(time.Hour))
	s.db.Model(&models.PayPayment{}).Where("id = ?", second.ID).Update("locked_at", payTestNow.Add(time.Minute))
	// A test payment moves no money.
	testIn := payIn()
	testIn.Reference = "TEST-1"
	test, _, _ := s.Create(ctx, m, models.PayModeTest, testIn)
	s.db.Model(&models.PayPayment{}).Where("id = ?", test.ID).Update("payer_user_id", payer.ID)

	got, err := s.PayerHistory(ctx, payer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != first.ID || got[1].ID != second.ID {
		t.Fatalf("history = %+v, want [first, second] newest paid first", got)
	}
	h := got[0]
	if h.MerchantName != m.BusinessName || h.CustomerTotal != first.CustomerTotalKobo || h.Currency != "NGN" ||
		h.Status != models.PayStatusProcessing || !h.PaidAt.Equal(payTestNow.Add(time.Hour)) {
		t.Fatalf("row = %+v", h)
	}
	if none, _ := s.PayerHistory(ctx, uuid.New()); len(none) != 0 {
		t.Fatalf("a stranger's history = %+v", none)
	}
}

func TestPayPayment_PayerHistoryKeepsARefundedAttemptAfterItsPaymentIsReleased(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	if err := s.db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	payer := payUser(t, s, true)
	other := payUser(t, s, true)
	in := payIn()
	in.Reference = "LIVE-REFUND"
	p, _, _ := s.Create(ctx, m, models.PayModeLive, in)
	order := func(owner uuid.UUID, status string, at time.Time) uuid.UUID {
		t.Helper()
		id := uuid.New()
		row := models.PaycrestOrderRecord{ID: id, UserID: owner, Direction: "sell", Status: status,
			Reference: "PCS-" + id.String(), PayPaymentID: &p.ID, CreatedAt: at}
		if err := s.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Her first attempt was funded and refunded; the refund released the payment.
	refunded := order(payer.ID, models.PaycrestStatusRefunded, payTestNow.Add(time.Minute))
	// An attempt that lapsed with nothing sent, someone else's refund, and one
	// that was never linked as a payment's order are not hers to list.
	order(payer.ID, models.PaycrestStatusExpired, payTestNow.Add(2*time.Minute))
	order(other.ID, models.PaycrestStatusRefunded, payTestNow.Add(3*time.Minute))

	got, err := s.PayerHistory(ctx, payer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != PayHistoryRefunded || got[0].OrderID == nil || *got[0].OrderID != refunded ||
		got[0].ID != p.ID || got[0].MerchantName != m.BusinessName || !got[0].PaidAt.Equal(payTestNow.Add(time.Minute)) {
		t.Fatalf("history = %+v, want the refunded attempt, pointing at its order", got)
	}

	// She tries again on the same payment: the new attempt is the payment row,
	// and the refund stays beside it — two rows, never merged.
	if _, _, err := s.Lock(ctx, payer, p.ID, true); err != nil {
		t.Fatal(err)
	}
	current := order(payer.ID, models.PaycrestStatusAwaitingDeposit, payTestNow.Add(time.Hour))
	s.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Updates(map[string]any{
		"paycrest_order_id": current, "locked_at": payTestNow.Add(time.Hour)})
	got, _ = s.PayerHistory(ctx, payer.ID)
	if len(got) != 2 || got[0].OrderID != nil || got[0].Status != models.PayStatusProcessing ||
		got[1].OrderID == nil || *got[1].OrderID != refunded {
		t.Fatalf("history = %+v, want [the payment, the refunded attempt]", got)
	}

	// A refunded order still linked as the payment's attempt (the reconciler has
	// not released it yet) is the payment's row, not a second one.
	s.db.Model(&models.PaycrestOrderRecord{}).Where("id = ?", current).Update("status", models.PaycrestStatusRefunded)
	if got, _ = s.PayerHistory(ctx, payer.ID); len(got) != 2 {
		t.Fatalf("history = %+v, want the current attempt listed once, as the payment", got)
	}
}

func TestPayPayment_CheckoutViewKeepsAFailedPaymentsOrderAndReasonForItsPayer(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	if err := s.db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	in := payIn()
	in.Reference = "LIVE-FAILED"
	live, _, _ := s.Create(ctx, m, models.PayModeLive, in)
	payer := payUser(t, s, true)
	if _, _, err := s.Lock(ctx, payer, live.ID, true); err != nil {
		t.Fatal(err)
	}
	orderID := uuid.New()
	s.db.Create(&models.PaycrestOrderRecord{ID: orderID, UserID: payer.ID, Direction: "sell",
		Status: models.PaycrestStatusUnmatched, Reference: "PCS-" + orderID.String()})
	s.db.Model(&models.PayPayment{}).Where("id = ?", live.ID).Updates(map[string]any{
		"paycrest_order_id": orderID, "status": models.PayStatusFailed,
		"failure_reason": "the deposit couldn't be matched — support is reviewing it"})

	// Failed is terminal: she can no longer resume it, but she must still be able to report it.
	v, err := s.CheckoutView(ctx, payer, live.ID, true)
	if err != nil || v.OrderID == nil || *v.OrderID != orderID || v.OrderStatus != models.PaycrestStatusUnmatched ||
		v.FailureReason != "the deposit couldn't be matched — support is reviewing it" || v.YouArePaying {
		t.Fatalf("payer: %+v, err %v", v, err)
	}
	stranger := payUser(t, s, true)
	if v, _ := s.CheckoutView(ctx, stranger, live.ID, true); v.OrderID != nil || v.OrderStatus != "" || v.FailureReason != "" {
		t.Fatalf("stranger must see neither the order nor why it failed: %+v", v)
	}
}

// Switching a merchant off must also stop live payments it created while it
// was on: the API refuses its live key, and nobody may start paying one either.
func TestPayPayment_AMerchantSwitchedOffCannotBePaidLive(t *testing.T) {
	s, m := newPayPaymentRig(t)
	in := payIn()
	in.Reference = "LIVE-OFF"
	live, _, _ := s.Create(context.Background(), m, models.PayModeLive, in)
	test, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	s.db.Model(m).Update("live_enabled", false)

	payer := payUser(t, s, true)
	if v, _ := s.CheckoutView(context.Background(), payer, live.ID, true); v.Payable {
		t.Fatalf("a switched-off merchant's live payment reads as payable: %+v", v)
	}
	if _, _, err := s.Lock(context.Background(), payer, live.ID, true); !errors.Is(err, ErrPayNotPayable) {
		t.Fatalf("live lock on a switched-off merchant: err = %v", err)
	}
	if _, _, err := s.Lock(context.Background(), payOwnerOf(t, s, test.ID), test.ID, true); err != nil {
		t.Fatalf("a test payment must not need the live switch: %v", err)
	}
}

// Switching live off expires only what can no longer be paid: pending LIVE
// payments. Test payments keep working, and a payment a payer is already paying
// or that has finished is never touched.
func TestPayPayment_ExpirePendingForMerchantCanBeLimitedToLive(t *testing.T) {
	s, m := newPayPaymentRig(t)
	withWebhooks(t, s, m)
	mk := func(mode, ref, status string) *models.PayPayment {
		in := payIn()
		in.Reference = ref
		p, _, err := s.Create(context.Background(), m, mode, in)
		if err != nil {
			t.Fatal(err)
		}
		if status != models.PayStatusPending {
			s.db.Model(p).Update("status", status)
		}
		return p
	}
	livePending := mk(models.PayModeLive, "L-PENDING", models.PayStatusPending)
	liveProcessing := mk(models.PayModeLive, "L-PROC", models.PayStatusProcessing)
	liveSucceeded := mk(models.PayModeLive, "L-DONE", models.PayStatusSucceeded)
	testPending := mk(models.PayModeTest, "T-PENDING", models.PayStatusPending)

	n, err := s.ExpirePendingForMerchant(context.Background(), m.ID, models.PayModeLive, "live payments switched off")
	if err != nil || n != 1 {
		t.Fatalf("n %d err %v", n, err)
	}
	for p, want := range map[*models.PayPayment]string{
		livePending: models.PayStatusExpired, liveProcessing: models.PayStatusProcessing,
		liveSucceeded: models.PayStatusSucceeded, testPending: models.PayStatusPending,
	} {
		if got, _ := s.Get(context.Background(), p.ID); got.Status != want {
			t.Errorf("%s: status %s, want %s", p.Reference, got.Status, want)
		}
	}
	if got, _ := s.Get(context.Background(), livePending.ID); got.FailureReason != "live payments switched off" {
		t.Errorf("reason %q", got.FailureReason)
	}
	if ds := deliveries(t, s); len(ds) != 1 || ds[0].Event != models.PayEventExpired || ds[0].PaymentID != livePending.ID {
		t.Fatalf("deliveries = %+v", ds)
	}
}
