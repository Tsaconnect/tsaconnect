package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func newPayPostgresRig(t *testing.T) (*PayPaymentService, *models.PayMerchant) {
	t.Helper()
	db := newITPostgresDB(t, &models.PayMerchant{}, &models.PayPayment{}, &models.PayWebhookDelivery{}, &models.PayAuditEvent{}, &models.PayRefund{})
	now := func() time.Time { return payTestNow }
	merchants := NewPayMerchantService(db, nil, nil, []byte(strings.Repeat("k", 32)), now)
	return NewPayPaymentService(db, merchants, 100, "https://tsaconnectworld.com/pay", now), seedPayMerchant(t, db)
}

func TestPayLock_ConcurrentPayersHaveOneWinnerOnPostgres(t *testing.T) {
	s, m := newPayPostgresRig(t)
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, winner := 0, uuid.Nil
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := &models.User{ID: uuid.New()}
			if _, _, err := s.Lock(context.Background(), u, p.ID, false); err == nil {
				mu.Lock()
				wins, winner = wins+1, u.ID
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	got, _ := s.Get(context.Background(), p.ID)
	if wins != 1 || got.PayerUserID == nil || *got.PayerUserID != winner {
		t.Fatalf("wins %d payer %v winner %v", wins, got.PayerUserID, winner)
	}
}

func TestPaySucceed_ConcurrentQueuesOneWebhookOnPostgres(t *testing.T) {
	s, m := newPayPostgresRig(t)
	s.db.Model(m).Update("test_webhook_url", "https://skyair.ng/t")
	m.TestWebhookURL = "https://skyair.ng/t"
	p, _, _ := s.Create(context.Background(), m, models.PayModeTest, payIn())
	s.Lock(context.Background(), &models.User{ID: uuid.New()}, p.ID, false)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Succeed(context.Background(), p.ID, "x") }()
	}
	wg.Wait()
	var n int64
	s.db.Model(&models.PayWebhookDelivery{}).Count(&n)
	if n != 1 {
		t.Fatalf("%d webhooks for one success", n)
	}
}

func TestPayCreate_ConcurrentSameReferenceOnPostgres(t *testing.T) {
	s, m := newPayPostgresRig(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids, created := map[uuid.UUID]bool{}, 0
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, c, err := s.Create(context.Background(), m, models.PayModeLive, payIn())
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids[p.ID] = true
			if c {
				created++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(ids) != 1 || created != 1 {
		t.Fatalf("ids %d created %d", len(ids), created)
	}
}

func TestPayWorkersQueriesRunOnPostgres(t *testing.T) {
	s, m := newPayPostgresRig(t)
	s.Create(context.Background(), m, models.PayModeTest, payIn())
	s.now = func() time.Time { return payTestNow.Add(time.Hour) }
	if n, err := s.ExpireDue(context.Background(), 10); err != nil || n != 1 {
		t.Fatalf("ExpireDue: %d %v", n, err)
	}
	sender := NewPayWebhookSender(s.db, s.merchants, NewPayWebhookHTTPClient(), s.now)
	if _, err := sender.DeliverDue(context.Background(), 10); err != nil {
		t.Fatalf("DeliverDue: %v", err)
	}
}

func TestPayMerchantUpdate_ConcurrentSameVersionHasOneWinnerOnPostgres(t *testing.T) {
	s, m := newPayPostgresRig(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, stale := 0, 0
	version := m.Version
	for i := 0; i < 8; i++ {
		wg.Add(1)
		share := 10 + i
		go func() {
			defer wg.Done()
			_, _, err := s.merchants.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{ExpectedVersion: &version, CustomerFeeSharePercent: &share})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrPayMerchantStale):
				stale++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _ := s.merchants.Get(context.Background(), m.ID)
	if wins != 1 || stale != 7 || got.Version != version+1 {
		t.Fatalf("wins %d stale %d version %d (started %d)", wins, stale, got.Version, version)
	}
}

func TestPayWebhook_PurgeOldOnPostgres(t *testing.T) {
	s, _ := newPayPostgresRig(t)
	old := PayWebhookRetention + time.Hour
	ids := seedRetentionRows(t, s, []retentionRow{{age: old, delivered: true}, {age: old, failed: true}, {age: old}})
	sender := NewPayWebhookSender(s.db, s.merchants, nil, func() time.Time { return payTestNow })
	if n, err := sender.PurgeOld(context.Background(), PayWebhookRetention, PayWebhookPurgeBatch); err != nil || n != 2 {
		t.Fatalf("purged %d err %v, want 2", n, err)
	}
	if countDeliveries(t, s, ids[0]) != 0 || countDeliveries(t, s, ids[1]) != 0 || countDeliveries(t, s, ids[2]) != 1 {
		t.Fatal("wrong rows purged")
	}
}

func TestPayWebhook_ConcurrentDeliveryOnPostgresDeliversEachOnce(t *testing.T) {
	s, _ := newPayPostgresRig(t)
	var mu sync.Mutex
	seen := map[string]int{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get("X-TSA-Delivery")]++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}))
	defer srv.Close()
	want := 0
	for i := 0; i < 6; i++ {
		want += len(queueMerchantDeliveries(t, s, fmt.Sprintf("PgAir%02d", i), srv.URL, 2))
	}
	sender := NewPayWebhookSender(s.db, s.merchants, srv.Client(), func() time.Time { return payTestNow })

	n, err := sender.DeliverDue(context.Background(), PayWebhookDefaultBatch)
	if err != nil || n != want {
		t.Fatalf("delivered %d err %v, want %d", n, err, want)
	}
	if len(seen) != want {
		t.Fatalf("%d distinct deliveries reached the endpoint, want %d", len(seen), want)
	}
	for id, c := range seen {
		if c != 1 {
			t.Errorf("delivery %s sent %d times", id, c)
		}
	}
	var pending int64
	s.db.Model(&models.PayWebhookDelivery{}).Where("delivered_at IS NULL").Count(&pending)
	if pending != 0 {
		t.Fatalf("%d deliveries left undelivered", pending)
	}
}

// The grouped summary (CASE + GROUP BY alias, SUM cast), the refund
// aggregates' SUMs and the export's keyset walk are Postgres-dialect sensitive.
func TestPayReconciliation_SummaryAndExportRunOnPostgres(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPostgresRig(t)
	s.SetRefundAggregator(NewPayRefundService(s.db, s, s.merchants, nil, nil, 5, s.now))
	live := models.PayModeLive
	refunded := reconPayment(t, s, m, live, "PG-1", reconTime("2026-10-02T10:00:00Z"), reconAt("2026-10-02T10:05:00Z"))
	if err := s.db.Create(&models.PayRefund{PaymentID: refunded.ID, MerchantID: m.ID, Mode: live, AmountMicroUSD: 1_500_000,
		CapMicroUSD: 33_000_000, CreditedMicroUSD: 1_500_000, ToAddress: refundPayerWallet, Status: models.PayRefundConfirmed,
		RequestedVia: models.PayRefundViaWeb, CreatedAt: payTestNow, ConfirmedAt: reconAt("2026-10-03T09:00:00Z")}).Error; err != nil {
		t.Fatal(err)
	}
	reconPayment(t, s, m, live, "PG-2", reconTime("2026-10-03T10:00:00Z"), reconAt("2026-10-02T10:05:00Z")) // same paid instant
	reconPayment(t, s, m, live, "PG-3", reconTime("2026-10-04T10:00:00Z"), nil)
	called := reconPayment(t, s, m, live, "PG-4", reconTime("2026-10-05T10:00:00Z"), nil)
	s.db.Model(&models.PayPayment{}).Where("id = ?", called.ID).Updates(map[string]any{"status": models.PayStatusExpired, "failure_reason": PayReasonMerchantCancelled})
	start, end, _ := ParsePayDateRange("2026-10-01", "2026-10-31")

	sum, err := s.Summary(ctx, m.ID, live, start, end)
	if err != nil || sum.Succeeded.Count != 2 || sum.Succeeded.CustomerTotalKobo != 2*5_050_506 ||
		sum.ByStatus["succeeded"] != 2 || sum.ByStatus["pending"] != 1 || sum.ByStatus["cancelled"] != 1 ||
		sum.Refunds.Count != 1 || sum.Refunds.AmountMicroUSD != 1_500_000 {
		t.Fatalf("summary %+v err %v", sum, err)
	}
	prev := payExportChunk
	payExportChunk = 1
	t.Cleanup(func() { payExportChunk = prev })
	var buf bytes.Buffer
	if err := s.ExportCSV(ctx, &buf, m.ID, live, start, end); err != nil {
		t.Fatal(err)
	}
	rows := readExport(t, buf.String())
	if len(rows) != 3 || rows[1][15] == rows[2][15] {
		t.Fatalf("rows %q", rows)
	}
	for _, row := range rows[1:] {
		want := []string{"0.00", "none"}
		if row[15] == refunded.ID.String() {
			want = []string{"1.50", "partial"}
		}
		if row[12] != want[0] || row[13] != want[1] {
			t.Errorf("%s refunded %q status %q, want %q", row[3], row[12], row[13], want)
		}
	}
}

// The same collision on Postgres, whose unique-violation message names the
// index rather than the column.
func TestPayCode_ACollisionIsRetriedOnPostgres(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPostgresRig(t)
	stubPayCodes(t, "7K2QFM9XWD", "7K2QFM9XWD", "AB12CD34EF")
	if _, _, err := s.Create(ctx, m, models.PayModeLive, payIn()); err != nil {
		t.Fatal(err)
	}
	in := payIn()
	in.Reference = "PNR-OTHER"
	second, created, err := s.Create(ctx, m, models.PayModeLive, in)
	if err != nil || !created || *second.PayCode != "AB12CD34EF" {
		t.Fatalf("second: %+v created %v err %v", second, created, err)
	}
}
