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
	if _, err := s.SimulateTestOutcome(context.Background(), payOwnerOf(t, s, p.ID), p.ID, true); err != nil {
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
		s.SimulateTestOutcome(context.Background(), payOwnerOf(t, s, p.ID), p.ID, true)
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
		if _, err := s.SimulateTestOutcome(context.Background(), payOwnerOf(t, s, p.ID), p.ID, true); err != nil {
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
	if _, err := s.SimulateTestOutcome(context.Background(), payOwnerOf(t, s, p2.ID), p2.ID, true); err != nil {
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

// queueMerchantDeliveries seeds a fresh merchant whose test webhook points at
// url and produces n payment.succeeded deliveries, returned oldest-due first
// (next_attempt_at is spaced explicitly so the fetch order is deterministic).
func queueMerchantDeliveries(t *testing.T, s *PayPaymentService, name, url string, n int) []models.PayWebhookDelivery {
	t.Helper()
	domain := strings.ToLower(strings.ReplaceAll(name, " ", "")) + ".ng"
	m := &models.PayMerchant{MerchantRequestID: uuid.New(), UserID: uuid.New(), BusinessName: name,
		WebsiteDomain: domain, Status: models.PayMerchantActive, SettlementMode: models.PaySettlementFiat,
		BankCode: "058", AccountNumber: "0000000001", AccountName: strings.ToUpper(name),
		CustomerFeeSharePercent: 100, MinAmountKobo: 100_000, MaxAmountKobo: 500_000_000,
		WebhookSecretVersion: 1, ReturnDomains: domain, TestWebhookURL: url}
	if err := s.db.Create(m).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		in := payIn()
		in.Reference = fmt.Sprintf("%s-%d", name, i)
		in.ReturnURL = ""
		p, _, err := s.Create(context.Background(), m, models.PayModeTest, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SimulateTestOutcome(context.Background(), payOwnerOf(t, s, p.ID), p.ID, true); err != nil {
			t.Fatal(err)
		}
		s.db.Model(&models.PayWebhookDelivery{}).Where("payment_id = ?", p.ID).
			Update("next_attempt_at", payTestNow.Add(-time.Duration(n-i)*time.Minute))
	}
	var rows []models.PayWebhookDelivery
	s.db.Where("merchant_id = ?", m.ID).Order("next_attempt_at ASC").Find(&rows)
	return rows
}

func TestPayWebhook_DifferentMerchantsAreDeliveredInParallel(t *testing.T) {
	s, _ := newPayPaymentRig(t)
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	for i := 0; i < 10; i++ {
		queueMerchantDeliveries(t, s, fmt.Sprintf("Airline%02d", i), srv.URL, 2)
	}
	sender := NewPayWebhookSender(s.db, s.merchants, srv.Client(), func() time.Time { return payTestNow })

	start := time.Now()
	n, err := sender.DeliverDue(context.Background(), PayWebhookDefaultBatch)
	elapsed := time.Since(start)
	if err != nil || n != 20 || hits.Load() != 20 {
		t.Fatalf("delivered %d (hits %d) err %v, want all 20", n, hits.Load(), err)
	}
	// Sequential would be 20 x 300ms = 6s; 8 workers over 10 merchants needs
	// two rounds of two sequential deliveries (~1.2s).
	if elapsed >= 2500*time.Millisecond {
		t.Fatalf("tick took %s, want < 2.5s — deliveries are not running in parallel", elapsed)
	}
}

func TestPayWebhook_OneMerchantsDeliveriesStaySequentialAndInOrder(t *testing.T) {
	s, _ := newPayPaymentRig(t)
	arrived := make(chan string, 4)
	var inFlight, maxInFlight atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			old := maxInFlight.Load()
			if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
				break
			}
		}
		id := r.Header.Get("X-TSA-Delivery")
		arrived <- id
		// The first event is the slow one, so a fully parallel sender would
		// land the second event first.
		if len(arrived) == 1 {
			time.Sleep(200 * time.Millisecond)
		}
	}))
	defer srv.Close()
	rows := queueMerchantDeliveries(t, s, "Solo Air", srv.URL, 2)
	sender := NewPayWebhookSender(s.db, s.merchants, srv.Client(), func() time.Time { return payTestNow })

	if n, err := sender.DeliverDue(context.Background(), PayWebhookDefaultBatch); err != nil || n != 2 {
		t.Fatalf("delivered %d err %v", n, err)
	}
	first, second := <-arrived, <-arrived
	if first != rows[0].ID.String() || second != rows[1].ID.String() {
		t.Fatalf("arrival order %s, %s; want %s, %s", first, second, rows[0].ID, rows[1].ID)
	}
	if maxInFlight.Load() != 1 {
		t.Fatalf("a merchant was hit by %d parallel requests, want 1", maxInFlight.Load())
	}
}

func TestPayWebhook_ACancelledContextStopsStartingNewDeliveries(t *testing.T) {
	s, _ := newPayPaymentRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		cancel()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()
	for i := 0; i < 10; i++ {
		queueMerchantDeliveries(t, s, fmt.Sprintf("Cancel%02d", i), srv.URL, 2)
	}
	sender := NewPayWebhookSender(s.db, s.merchants, srv.Client(), func() time.Time { return payTestNow })

	if _, err := sender.DeliverDue(ctx, PayWebhookDefaultBatch); err == nil {
		t.Fatal("a cancelled tick reported success")
	}
	// At most one in-flight first delivery per worker; nothing new starts
	// once the context is cancelled.
	if got := hits.Load(); got > payWebhookConcurrency {
		t.Fatalf("%d deliveries started, want <= %d (the workers' in-flight requests)", got, payWebhookConcurrency)
	}
}

func TestPayWebhook_TheDefaultBatchCoversEveryFetchedMerchantDelivery(t *testing.T) {
	if PayWebhookDefaultBatch != payWebhookMerchantsPerTick*payWebhookPerMerchantPerTick {
		t.Fatalf("PayWebhookDefaultBatch = %d, want merchants (%d) x per-merchant (%d)",
			PayWebhookDefaultBatch, payWebhookMerchantsPerTick, payWebhookPerMerchantPerTick)
	}
	if PayWebhookDefaultBatch != 50 {
		t.Fatalf("PayWebhookDefaultBatch = %d, want 50", PayWebhookDefaultBatch)
	}
}

type retentionRow struct {
	age         time.Duration // since created_at
	finishedAge time.Duration // since delivered_at / failed_at; zero means age
	delivered   bool
	failed      bool
}

// seedRetentionRows inserts one delivery per spec and returns their ids in
// spec order.
func seedRetentionRows(t *testing.T, s *PayPaymentService, specs []retentionRow) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, len(specs))
	for i, sp := range specs {
		created := payTestNow.Add(-sp.age)
		finished := created
		if sp.finishedAge != 0 {
			finished = payTestNow.Add(-sp.finishedAge)
		}
		d := models.PayWebhookDelivery{ID: uuid.New(), MerchantID: uuid.New(), PaymentID: uuid.New(),
			Mode: models.PayModeTest, Event: models.PayEventSucceeded, Payload: `{"big":"payload"}`,
			NextAttemptAt: &created, CreatedAt: created}
		if sp.delivered {
			d.DeliveredAt = &finished
		}
		if sp.failed {
			d.FailedAt = &finished
		}
		if err := s.db.Create(&d).Error; err != nil {
			t.Fatal(err)
		}
		ids[i] = d.ID
	}
	return ids
}

func countDeliveries(t *testing.T, s *PayPaymentService, id uuid.UUID) int64 {
	t.Helper()
	var n int64
	s.db.Model(&models.PayWebhookDelivery{}).Where("id = ?", id).Count(&n)
	return n
}

func TestPayWebhook_PurgeOldRemovesFinishedRowsPastRetentionOnly(t *testing.T) {
	s, _ := newPayPaymentRig(t)
	old, recent := PayWebhookRetention+time.Hour, 24*time.Hour
	ids := seedRetentionRows(t, s, []retentionRow{
		{age: old, delivered: true},                      // 0: purged
		{age: old, failed: true},                         // 1: purged
		{age: recent, delivered: true},                   // 2: kept, too recent
		{age: old},                                       // 3: kept, still pending
		{age: old, finishedAge: recent, delivered: true}, // 4: kept, created long ago but only just delivered (e.g. after a resend)
		{age: old, finishedAge: recent, failed: true},    // 5: kept, gave up recently
	})
	sender := NewPayWebhookSender(s.db, s.merchants, nil, func() time.Time { return payTestNow })

	n, err := sender.PurgeOld(context.Background(), PayWebhookRetention, PayWebhookPurgeBatch)
	if err != nil || n != 2 {
		t.Fatalf("purged %d err %v, want 2", n, err)
	}
	for i, want := range []int64{0, 0, 1, 1, 1, 1} {
		if got := countDeliveries(t, s, ids[i]); got != want {
			t.Errorf("row %d: %d left, want %d", i, got, want)
		}
	}
}

func TestPayWebhook_PurgeOldRespectsTheLimit(t *testing.T) {
	s, _ := newPayPaymentRig(t)
	old := PayWebhookRetention + time.Hour
	seedRetentionRows(t, s, []retentionRow{
		{age: old, delivered: true}, {age: old, delivered: true}, {age: old, failed: true},
	})
	sender := NewPayWebhookSender(s.db, s.merchants, nil, func() time.Time { return payTestNow })

	if n, err := sender.PurgeOld(context.Background(), PayWebhookRetention, 2); err != nil || n != 2 {
		t.Fatalf("first call purged %d err %v, want 2", n, err)
	}
	if n, err := sender.PurgeOld(context.Background(), PayWebhookRetention, 2); err != nil || n != 1 {
		t.Fatalf("second call purged %d err %v, want 1", n, err)
	}
}

// An admin Resend can requeue a row between PurgeOld's select and its delete;
// the delete must re-check that the row is still finished and old.
func TestPayWebhook_PurgeOldDeleteSparesARowResentAfterTheSelect(t *testing.T) {
	s, _ := newPayPaymentRig(t)
	old := PayWebhookRetention + time.Hour
	ids := seedRetentionRows(t, s, []retentionRow{{age: old, failed: true}, {age: old, failed: true}})
	sender := NewPayWebhookSender(s.db, s.merchants, nil, func() time.Time { return payTestNow })
	cutoff := payTestNow.Add(-PayWebhookRetention)

	selected, err := sender.purgeableIDs(context.Background(), cutoff, 10)
	if err != nil || len(selected) != 2 {
		t.Fatalf("selected %d err %v", len(selected), err)
	}
	if err := sender.Resend(context.Background(), ids[0]); err != nil {
		t.Fatal(err)
	}
	n, err := sender.deleteFinished(context.Background(), selected, cutoff)
	if err != nil || n != 1 {
		t.Fatalf("deleted %d err %v, want 1", n, err)
	}
	if countDeliveries(t, s, ids[0]) != 1 || countDeliveries(t, s, ids[1]) != 0 {
		t.Fatal("the resent (pending) row must survive and the untouched one must go")
	}
}

func TestPayWebhookDelivery_FinishedTimestampsAreIndexedForThePurge(t *testing.T) {
	s, _ := newPayPaymentRig(t)
	for _, col := range []string{"DeliveredAt", "FailedAt"} {
		if !s.db.Migrator().HasIndex(&models.PayWebhookDelivery{}, col) {
			t.Errorf("pay_webhook_deliveries.%s has no index; PurgeOld would scan the table every tick", col)
		}
	}
}

func TestPayWebhook_NeverMoreThanTheConcurrencyBoundInFlight(t *testing.T) {
	s, _ := newPayPaymentRig(t)
	var inFlight, maxInFlight atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			old := maxInFlight.Load()
			if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()
	for i := 0; i < 20; i++ {
		queueMerchantDeliveries(t, s, fmt.Sprintf("Bound%02d", i), srv.URL, 1)
	}
	sender := NewPayWebhookSender(s.db, s.merchants, srv.Client(), func() time.Time { return payTestNow })

	if n, err := sender.DeliverDue(context.Background(), PayWebhookDefaultBatch); err != nil || n != 20 {
		t.Fatalf("delivered %d err %v", n, err)
	}
	if got := maxInFlight.Load(); got > payWebhookConcurrency || got < 2 {
		t.Fatalf("max in flight = %d, want between 2 and %d", got, payWebhookConcurrency)
	}
}

// panicTransport panics for any request whose path holds "/panic" and
// answers 200 to everything else.
type panicTransport struct{ ok atomic.Int32 }

func (p *panicTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "/panic") {
		panic("boom in round trip")
	}
	p.ok.Add(1)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
}

func TestPayWebhook_APanickingDeliveryIsAnErrorNotACrashAndOthersStillComplete(t *testing.T) {
	s, _ := newPayPaymentRig(t)
	queueMerchantDeliveries(t, s, "Panicker", "https://panic.example/panic", 2)
	for i := 0; i < 4; i++ {
		queueMerchantDeliveries(t, s, fmt.Sprintf("Fine%02d", i), fmt.Sprintf("https://fine%d.example/hook", i), 2)
	}
	tr := &panicTransport{}
	sender := NewPayWebhookSender(s.db, s.merchants, &http.Client{Transport: tr}, func() time.Time { return payTestNow })

	n, err := sender.DeliverDue(context.Background(), PayWebhookDefaultBatch)
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("err = %v, want a panic error", err)
	}
	if n != 8 || tr.ok.Load() != 8 {
		t.Fatalf("delivered %d (ok requests %d), want the 4 healthy merchants' 8", n, tr.ok.Load())
	}
}

func TestPayWebhook_APanickedRowAdvancesInsteadOfBlockingItsMerchantForever(t *testing.T) {
	s, _ := newPayPaymentRig(t)
	rows := queueMerchantDeliveries(t, s, "Poison", "https://panic.example/panic", 2)
	sender := NewPayWebhookSender(s.db, s.merchants, &http.Client{Transport: &panicTransport{}}, func() time.Time { return payTestNow })

	for tick := 1; tick <= 2; tick++ {
		if _, err := sender.DeliverDue(context.Background(), PayWebhookDefaultBatch); err == nil {
			t.Fatalf("tick %d: want a panic error", tick)
		}
	}
	for i, r := range rows {
		var got models.PayWebhookDelivery
		if err := s.db.First(&got, "id = ?", r.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.Attempts != 1 || got.NextAttemptAt == nil || !got.NextAttemptAt.After(payTestNow) {
			t.Errorf("row %d: attempts=%d next=%v; want one attempt booked and a retry in the future", i, got.Attempts, got.NextAttemptAt)
		}
		if strings.Contains(got.LastError, "panic.example") || got.LastError == "" {
			t.Errorf("row %d: last_error = %q", i, got.LastError)
		}
	}
}

// cancelAfterResponse cancels ctx as soon as the merchant has "answered",
// i.e. after the request reached them but before the outcome is recorded.
type cancelAfterResponse struct {
	cancel func()
	hits   atomic.Int32
}

func (c *cancelAfterResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	c.hits.Add(1)
	c.cancel()
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
}

func TestPayWebhook_AnAnsweredDeliveryIsRecordedEvenIfTheTickIsCancelledMeanwhile(t *testing.T) {
	s, _ := newPayPaymentRig(t)
	rows := queueMerchantDeliveries(t, s, "Answered", "https://answered.example/hook", 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr := &cancelAfterResponse{cancel: cancel}
	sender := NewPayWebhookSender(s.db, s.merchants, &http.Client{Transport: tr}, func() time.Time { return payTestNow })

	n, err := sender.DeliverDue(ctx, PayWebhookDefaultBatch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation reported", err)
	}
	if n != 1 || tr.hits.Load() != 1 {
		t.Fatalf("delivered %d, hits %d; want exactly the first delivery sent and counted", n, tr.hits.Load())
	}
	var first, second models.PayWebhookDelivery
	s.db.First(&first, "id = ?", rows[0].ID)
	s.db.First(&second, "id = ?", rows[1].ID)
	if first.DeliveredAt == nil || first.Attempts != 1 {
		t.Fatalf("the answered delivery was not recorded: %+v", first)
	}
	if second.Attempts != 0 || second.DeliveredAt != nil {
		t.Fatalf("a delivery started after cancellation: %+v", second)
	}
}

type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func TestPayWebhook_ATransportFailureDoesNotStoreTheURLQuery(t *testing.T) {
	s, m := newPayPaymentRig(t)
	d := queuedDelivery(t, s, m, "https://skyair.ng/h?token=SECRET")
	sender := NewPayWebhookSender(s.db, s.merchants, &http.Client{Transport: errTransport{}}, func() time.Time { return payTestNow })
	if _, err := sender.DeliverDue(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	var got models.PayWebhookDelivery
	s.db.First(&got, "id = ?", d.ID)
	if got.Attempts != 1 || !strings.Contains(got.LastError, "connection refused") || strings.Contains(got.LastError, "SECRET") {
		t.Fatalf("attempts %d last_error %q", got.Attempts, got.LastError)
	}
}

func TestPayWebhook_ResendForMerchantRequeuesOnlyItsOwnUndeliveredEvent(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	d := queuedDelivery(t, s, m, "https://skyair.ng/h")
	failedAt := payTestNow
	s.db.Model(d).Updates(map[string]any{"failed_at": &failedAt, "next_attempt_at": nil, "attempts": 7})
	sender := NewPayWebhookSender(s.db, s.merchants, &http.Client{Transport: failingTransport{t}}, func() time.Time { return payTestNow })
	sender.SetPayments(s)

	other := seedPayMerchant(t, s.db)
	for name, c := range map[string]struct {
		merchant uuid.UUID
		mode     string
		id       uuid.UUID
	}{
		"another merchant": {other.ID, models.PayModeTest, d.ID},
		"the other mode":   {m.ID, models.PayModeLive, d.ID},
		"no such delivery": {m.ID, models.PayModeTest, uuid.New()},
	} {
		if _, err := sender.ResendForMerchant(ctx, c.merchant, c.mode, c.id); !errors.Is(err, ErrPayDeliveryNotFound) {
			t.Fatalf("%s: err %v", name, err)
		}
	}
	got, err := sender.ResendForMerchant(ctx, m.ID, models.PayModeTest, d.ID)
	if err != nil || got.ID != d.ID || got.FailedAt != nil || got.Attempts != 0 || got.NextAttemptAt == nil {
		t.Fatalf("resend: %+v %v", got, err)
	}
	var n int64
	s.db.Model(&models.PayWebhookDelivery{}).Count(&n)
	if n != 1 {
		t.Fatalf("an undelivered event is retried in place, not copied: %d rows", n)
	}
}

func TestPayWebhook_ResendForMerchantOfADeliveredEventIsANewDeliveryOfThePaymentNow(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	d := queuedDelivery(t, s, m, "https://skyair.ng/h")
	deliveredAt := payTestNow
	s.db.Model(d).Updates(map[string]any{"delivered_at": &deliveredAt, "next_attempt_at": nil, "attempts": 1, "last_status_code": 200})
	s.db.Model(&models.PayPayment{}).Where("id = ?", d.PaymentID).Update("settlement_ref", "ord_now")
	sender := NewPayWebhookSender(s.db, s.merchants, &http.Client{Transport: failingTransport{t}}, func() time.Time { return payTestNow })
	sender.SetPayments(s)

	first, err := sender.ResendForMerchant(ctx, m.ID, models.PayModeTest, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sender.ResendForMerchant(ctx, m.ID, models.PayModeTest, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []models.PayWebhookDelivery{first, second} {
		if got.ID == d.ID || got.Event != d.Event || got.PaymentID != d.PaymentID || got.Mode != d.Mode ||
			got.Attempts != 0 || got.DeliveredAt != nil || got.NextAttemptAt == nil {
			t.Fatalf("a delivered event is sent anew as its own delivery: %+v", got)
		}
		var body struct {
			ID   string
			Type string
			Data struct{ SettlementRef string }
		}
		if err := json.Unmarshal([]byte(got.Payload), &body); err != nil || body.ID != got.ID.String() || body.Type != d.Event || body.Data.SettlementRef != "ord_now" {
			t.Fatalf("payload %s: the new id and the payment as it is now", got.Payload)
		}
	}
	if first.ID == second.ID {
		t.Fatal("two resends shared one delivery id")
	}
	var original models.PayWebhookDelivery
	s.db.First(&original, "id = ?", d.ID)
	if original.DeliveredAt == nil || original.Attempts != 1 || original.LastStatusCode != 200 {
		t.Fatalf("the delivered record was changed: %+v", original)
	}

	s.db.Model(m).Update("test_webhook_url", "")
	if _, err := sender.ResendForMerchant(ctx, m.ID, models.PayModeTest, d.ID); !errors.Is(err, ErrPayWebhookNoURL) {
		t.Fatalf("no webhook URL for the mode: err %v", err)
	}
}
