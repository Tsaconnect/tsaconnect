package services

import (
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// cachedReader is a keyed read-through cache with separate success and failure
// lifetimes, single-flighted per key. Extracted from the company wallet's stock
// cache (spec §9.1, M11) so the pool's LP balances read the chain the same way.
// The caller's read function stores its own result with Put, which is also
// how a fresh (uncached) read refreshes the cache.
type cachedReader[T any] struct {
	ttl, failTTL time.Duration
	mu           sync.Mutex
	entries      map[string]cachedEntry[T]
	flight       singleflight.Group
}

type cachedEntry[T any] struct {
	value T
	err   error
	at    time.Time
}

func newCachedReader[T any](ttl, failTTL time.Duration) *cachedReader[T] {
	return &cachedReader[T]{ttl: ttl, failTTL: failTTL, entries: map[string]cachedEntry[T]{}}
}

// Get returns a value younger than ttl, or a failure younger than failTTL;
// otherwise it runs read once for every concurrent caller of key.
func (c *cachedReader[T]) Get(key string, now time.Time, read func() (T, error)) (T, error) {
	var zero T
	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()
	if ok {
		age := now.Sub(e.at)
		if e.err == nil && age < c.ttl {
			return e.value, nil
		}
		if e.err != nil && age < c.failTTL {
			return zero, e.err
		}
	}
	v, err, _ := c.flight.Do(key, func() (any, error) { return read() })
	if err != nil {
		return zero, err
	}
	return v.(T), nil
}

// Put records a reading (or its failure) for key.
func (c *cachedReader[T]) Put(key string, now time.Time, v T, err error) {
	c.mu.Lock()
	c.entries[key] = cachedEntry[T]{value: v, err: err, at: now}
	c.mu.Unlock()
}
