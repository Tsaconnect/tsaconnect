package services

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func newPayPostgresRig(t *testing.T) (*PayPaymentService, *models.PayMerchant) {
	t.Helper()
	db := newITPostgresDB(t, &models.PayMerchant{}, &models.PayPayment{}, &models.PayWebhookDelivery{})
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
