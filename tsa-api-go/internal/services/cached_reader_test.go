package services

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCachedReader_ServesTTLThenRereads(t *testing.T) {
	c := newCachedReader[int](30*time.Second, 5*time.Second)
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	var reads int
	read := func(v int) func() (int, error) {
		return func() (int, error) { reads++; c.Put("k", t0, v, nil); return v, nil }
	}
	if v, _ := c.Get("k", t0, read(1)); v != 1 {
		t.Fatal("first read")
	}
	if v, _ := c.Get("k", t0.Add(29*time.Second), read(2)); v != 1 || reads != 1 {
		t.Fatalf("inside ttl: v=%d reads=%d", v, reads)
	}
	if v, _ := c.Get("k", t0.Add(31*time.Second), read(3)); v != 3 || reads != 2 {
		t.Fatalf("after ttl: v=%d reads=%d", v, reads)
	}
}

func TestCachedReader_RemembersAFailureForFailTTL(t *testing.T) {
	c := newCachedReader[int](30*time.Second, 5*time.Second)
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	boom := errors.New("rpc 403")
	fail := func() (int, error) { c.Put("k", t0, 0, boom); return 0, boom }
	ok := func() (int, error) { c.Put("k", t0.Add(6*time.Second), 9, nil); return 9, nil }
	if _, err := c.Get("k", t0, fail); !errors.Is(err, boom) {
		t.Fatal("first failure")
	}
	if _, err := c.Get("k", t0.Add(4*time.Second), ok); !errors.Is(err, boom) {
		t.Fatal("a failure must be remembered for failTTL")
	}
	if v, err := c.Get("k", t0.Add(6*time.Second), ok); err != nil || v != 9 {
		t.Fatalf("after failTTL: %v %v", v, err)
	}
}

func TestCachedReader_SingleflightsConcurrentReads(t *testing.T) {
	c := newCachedReader[int](time.Minute, time.Second)
	var calls atomic.Int32
	gate := make(chan struct{})
	read := func() (int, error) { calls.Add(1); <-gate; return 7, nil }
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Get("k", time.Now(), read) }()
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("read ran %d times, want 1", calls.Load())
	}
}
