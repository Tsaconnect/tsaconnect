package services

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Re-review I-A on real row locks: two reconcilers (the chain reconciler job
// and the pool settle job) on one disputed slice of a paid order send exactly
// one adminResolve.
func TestPoolSlice_ConcurrentReconcileSendsOneResolve_Postgres(t *testing.T) {
	r := newPoolOpenRigPG(t)
	r.addLP(t, 30)
	ctx := context.Background()
	p := r.price(t, "5000")
	intent := poolIntentFor(p)
	var out []models.P2PTrade
	if err := r.desk.insertOrder(ctx, intent, p, nil, &out); err != nil || len(out) != 1 {
		t.Fatalf("insert order: %v (%d slices)", err, len(out))
	}
	sl := out[0]
	if err := r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("status", models.ITStatusPaymentReceived).Error; err != nil {
		t.Fatal(err)
	}
	r.chain.cmu.Lock()
	r.chain.trades[HashID(sl.ID)] = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusDisputed, Amount: usdt18(3)}
	r.chain.cmu.Unlock()
	r.trades.SetPoolSlices(r.it, &rendezvousSender{inner: releaseSender(), arrived: make(chan struct{}, 2)})
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = r.trades.ReconcileTrade(ctx, sl.ID)
		}()
	}
	close(start)
	wg.Wait()
	r.chain.cmu.Lock()
	n := len(r.chain.resolves)
	r.chain.cmu.Unlock()
	if n != 1 {
		t.Fatalf("adminResolve prepared %d times, want exactly 1", n)
	}
}

// rendezvousSender holds each Send until a second caller arrives (or 500 ms
// pass), so two reconcilers that both decided to send are both caught sending,
// whatever the scheduler does. Serialised: chainCountingSender is not safe for
// concurrent use.
type rendezvousSender struct {
	inner   *chainCountingSender
	arrived chan struct{}
	mu      sync.Mutex
}

func (s *rendezvousSender) Address() string { return s.inner.Address() }
func (s *rendezvousSender) SignAndBroadcast(ctx context.Context, u []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.SignAndBroadcast(ctx, u)
}
func (s *rendezvousSender) Send(ctx context.Context, chainID int64, build func() ([]byte, error)) (string, error) {
	s.arrived <- struct{}{}
	for deadline := time.Now().Add(500 * time.Millisecond); len(s.arrived) < 2 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Send(ctx, chainID, build)
}
