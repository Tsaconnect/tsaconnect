package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Fatal("unexpected network call")
	return nil, nil
}

// queuedDelivery points the merchant's test webhook at url and produces one
// payment.succeeded delivery.
func queuedDelivery(t *testing.T, s *PayPaymentService, m *models.PayMerchant, url string) *models.PayWebhookDelivery {
	t.Helper()
	s.db.Model(m).Update("test_webhook_url", url)
	m.TestWebhookURL = url
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	if _, err := s.SimulateTestOutcome(context.Background(), &models.User{ID: uuid.New()}, p.ID, true); err != nil {
		t.Fatal(err)
	}
	var d models.PayWebhookDelivery
	s.db.First(&d)
	return &d
}

func TestPayWebhook_SignsAndDeliversOnce(t *testing.T) {
	s, m := newPayPaymentRig(t)
	var hits atomic.Int32
	var sig, body, event, delivery string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		body, sig = string(b), r.Header.Get(PayWebhookSignatureHeader)
		event, delivery = r.Header.Get("X-TSA-Event"), r.Header.Get("X-TSA-Delivery")
	}))
	defer srv.Close()
	d := queuedDelivery(t, s, m, srv.URL)
	sender := NewPayWebhookSender(s.db, s.merchants, srv.Client(), func() time.Time { return payTestNow })
	if n, err := sender.DeliverDue(context.Background(), 10); err != nil || n != 1 {
		t.Fatalf("delivered %d err %v", n, err)
	}
	if n, _ := sender.DeliverDue(context.Background(), 10); n != 0 || hits.Load() != 1 {
		t.Fatal("a delivered event was sent again")
	}
	if want := SignPayWebhook(s.merchants.WebhookSecret(m, models.PayModeTest), payTestNow.Unix(), []byte(body)); sig != want {
		t.Fatalf("signature %q, want %q", sig, want)
	}
	var ev struct {
		ID, Type string
		Livemode bool
	}
	json.Unmarshal([]byte(body), &ev)
	if ev.ID != d.ID.String() || ev.Type != models.PayEventSucceeded || ev.Livemode || event != models.PayEventSucceeded || delivery != d.ID.String() {
		t.Fatalf("event %+v headers %q %q", ev, event, delivery)
	}
}

func TestPayWebhook_RetriesOnTheScheduleThenGivesUp(t *testing.T) {
	s, m := newPayPaymentRig(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	queuedDelivery(t, s, m, srv.URL)
	now := payTestNow
	sender := NewPayWebhookSender(s.db, s.merchants, srv.Client(), func() time.Time { return now })
	for i := 0; i <= len(PayWebhookSchedule); i++ {
		if n, _ := sender.DeliverDue(context.Background(), 10); n != 0 {
			t.Fatal("a 500 counted as delivered")
		}
		var d models.PayWebhookDelivery
		s.db.First(&d)
		if d.Attempts != i+1 || d.LastStatusCode != 500 {
			t.Fatalf("attempt %d: %+v", i, d)
		}
		if i < len(PayWebhookSchedule) {
			if d.NextAttemptAt == nil || !d.NextAttemptAt.Equal(now.Add(PayWebhookSchedule[i])) || d.FailedAt != nil {
				t.Fatalf("attempt %d next = %v", i, d.NextAttemptAt)
			}
			if n, _ := sender.DeliverDue(context.Background(), 10); n != 0 {
				t.Fatal("retried before its time")
			}
			now = *d.NextAttemptAt
		} else if d.FailedAt == nil || d.NextAttemptAt != nil {
			t.Fatalf("after the last attempt: %+v", d)
		}
	}
}

func TestPayWebhook_ARedirectIsNotFollowedAndCountsAsAFailure(t *testing.T) {
	s, m := newPayPaymentRig(t)
	var targetHits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits.Add(1) }))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()
	queuedDelivery(t, s, m, srv.URL)
	client := srv.Client()
	client.CheckRedirect = NewPayWebhookHTTPClient().CheckRedirect
	sender := NewPayWebhookSender(s.db, s.merchants, client, func() time.Time { return payTestNow })
	if n, _ := sender.DeliverDue(context.Background(), 10); n != 0 || targetHits.Load() != 0 {
		t.Fatalf("delivered %d, target hit %d", n, targetHits.Load())
	}
	var d models.PayWebhookDelivery
	s.db.First(&d)
	if d.LastStatusCode != http.StatusFound || d.NextAttemptAt == nil {
		t.Fatalf("delivery = %+v", d)
	}
}

func TestPayWebhook_RefusesAStoredHTTPURLWithoutCallingIt(t *testing.T) {
	s, m := newPayPaymentRig(t)
	queuedDelivery(t, s, m, "http://skyair.ng/h")
	sender := NewPayWebhookSender(s.db, s.merchants, &http.Client{Transport: failingTransport{t}}, func() time.Time { return payTestNow })
	sender.DeliverDue(context.Background(), 10)
	var d models.PayWebhookDelivery
	s.db.First(&d)
	if d.DeliveredAt != nil || d.LastError == "" {
		t.Fatalf("delivery = %+v", d)
	}
}

func TestPayWebhook_OneMerchantCannotHogATick(t *testing.T) {
	s, m := newPayPaymentRig(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	s.db.Model(m).Update("test_webhook_url", srv.URL)
	m.TestWebhookURL = srv.URL
	for i := 0; i < 5; i++ {
		in := payIn()
		in.Reference = "R" + string(rune('A'+i))
		p, _, _ := s.Create(context.Background(), m, models.PayModeTest, in)
		s.SimulateTestOutcome(context.Background(), &models.User{ID: uuid.New()}, p.ID, true)
	}
	sender := NewPayWebhookSender(s.db, s.merchants, srv.Client(), func() time.Time { return payTestNow })
	if n, _ := sender.DeliverDue(context.Background(), 50); n != payWebhookPerMerchantPerTick {
		t.Fatalf("one merchant got %d deliveries in a tick, want %d", n, payWebhookPerMerchantPerTick)
	}
}

func TestPayWebhook_ResendRequeuesAFailedDelivery(t *testing.T) {
	s, m := newPayPaymentRig(t)
	d := queuedDelivery(t, s, m, "https://skyair.ng/h")
	failedAt := payTestNow
	s.db.Model(d).Updates(map[string]any{"failed_at": &failedAt, "next_attempt_at": nil, "attempts": 7})
	sender := NewPayWebhookSender(s.db, s.merchants, &http.Client{Transport: failingTransport{t}}, func() time.Time { return payTestNow })
	if err := sender.Resend(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	var got models.PayWebhookDelivery
	s.db.First(&got, "id = ?", d.ID)
	if got.FailedAt != nil || got.Attempts != 0 || got.NextAttemptAt == nil {
		t.Fatalf("after resend: %+v", got)
	}
}

// A merchant with a due backlog that sorts earliest than everyone else's
// must not be able to keep another merchant's single due delivery out of
// the same tick — the property a naive "ORDER BY ... LIMIT, then cap per
// merchant" fetch does NOT have, because the cap only ever sees whichever
// merchant's rows the single global LIMIT happened to fetch.
func TestPayWebhook_ABusyMerchantCannotStarveAnotherMerchantOutOfATick(t *testing.T) {
	s, m1 := newPayPaymentRig(t)
	var hits1, hits2 atomic.Int32
	srv1 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits1.Add(1) }))
	defer srv1.Close()
	srv2 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits2.Add(1) }))
	defer srv2.Close()

	// m1: a due backlog bigger than the limit DeliverDue is called with
	// below, all created (and so sorting, at the tied next_attempt_at this
	// test's fixed clock gives every row) before m2's single delivery ever
	// exists — exactly the shape that lets a single global
	// `ORDER BY ... LIMIT` fetch fill up entirely with one merchant's rows
	// and never even reach another merchant's.
	s.db.Model(m1).Update("test_webhook_url", srv1.URL)
	m1.TestWebhookURL = srv1.URL
	for i := 0; i < 5; i++ {
		in := payIn()
		in.Reference = fmt.Sprintf("BUSY-%02d", i)
		p, _, err := s.Create(context.Background(), m1, models.PayModeTest, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SimulateTestOutcome(context.Background(), &models.User{ID: uuid.New()}, p.ID, true); err != nil {
			t.Fatal(err)
		}
	}

	// m2: one due delivery, queued after m1's whole backlog.
	m2 := &models.PayMerchant{MerchantRequestID: uuid.New(), UserID: uuid.New(), BusinessName: "Blue Air",
		WebsiteDomain: "blueair.ng", Status: models.PayMerchantActive, SettlementMode: models.PaySettlementFiat,
		BankCode: "058", AccountNumber: "0000000002", AccountName: "BLUE AIR LTD",
		CustomerFeeSharePercent: 100, MinAmountKobo: 100_000, MaxAmountKobo: 500_000_000,
		WebhookSecretVersion: 1, ReturnDomains: "blueair.ng", TestWebhookURL: srv2.URL}
	if err := s.db.Create(m2).Error; err != nil {
		t.Fatal(err)
	}
	in2 := payIn()
	in2.Reference = "M2-ONE"
	in2.ReturnURL = "" // payIn's return URL is on m1's allowed domains, not m2's
	p2, _, err := s.Create(context.Background(), m2, models.PayModeTest, in2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SimulateTestOutcome(context.Background(), &models.User{ID: uuid.New()}, p2.ID, true); err != nil {
		t.Fatal(err)
	}

	// limit (3) is smaller than m1's backlog (5) alone, so a single global
	// fetch-then-cap would exhaust the whole limit on m1 and never reach
	// m2. m2 must still be attempted this same tick.
	sender := NewPayWebhookSender(s.db, s.merchants, srv1.Client(), func() time.Time { return payTestNow })
	if _, err := sender.DeliverDue(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if hits2.Load() != 1 {
		t.Fatalf("m2 hits = %d, want 1 — its delivery was starved by m1's backlog", hits2.Load())
	}
	if got := hits1.Load(); got != payWebhookPerMerchantPerTick {
		t.Fatalf("m1 hits = %d, want the per-merchant cap %d", got, payWebhookPerMerchantPerTick)
	}
}

func TestPayWebhookHTTPClient_DialsOnlyPublicAddressesOn443(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	c := NewPayWebhookHTTPClient()
	for _, u := range []string{srv.URL, fmt.Sprintf("https://localhost:%d/", port)} {
		if _, err := c.Get(u); err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Fatalf("%s: err = %v — the dialer must refuse after DNS", u, err)
		}
	}
	if c.Timeout != 5*time.Second || c.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("timeout 5 s and no proxy")
	}
	req, _ := http.NewRequest("GET", "https://x.ng", nil)
	if err := c.CheckRedirect(req, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect = %v", err)
	}
}
