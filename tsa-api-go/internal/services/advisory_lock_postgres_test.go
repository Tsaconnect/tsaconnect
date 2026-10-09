package services

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/gorm"
)

// §9.2: two holders of one pool key never overlap; different keys do.
func TestWithAdvisoryLock_SerialisesOneKey_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "liq_c1_lock_", &models.ITIntent{})
	var inside, maxInside int32
	run := func(key string) {
		_ = withAdvisoryLock(context.Background(), db, key, "test", func(*gorm.DB) error {
			n := atomic.AddInt32(&inside, 1)
			for {
				m := atomic.LoadInt32(&maxInside)
				if n <= m || atomic.CompareAndSwapInt32(&maxInside, m, n) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			atomic.AddInt32(&inside, -1)
			return nil
		})
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); run(poolLockKey("bsc", "USDT")) }()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Fatalf("max concurrent holders = %d, want 1", maxInside)
	}
}
