package services

import (
	"context"
	"sync"
	"testing"
	"time"
)

// concurrencyProbe stands in for the operator signer and records the maximum
// number of callers inside SignAndBroadcast at once.
type concurrencyProbe struct {
	mu        sync.Mutex
	active    int
	maxActive int
	calls     int
}

func (p *concurrencyProbe) Address() string { return "0x0000000000000000000000000000000000000A11" }

func (p *concurrencyProbe) SignAndBroadcast(_ context.Context, _ []byte) (string, error) {
	p.mu.Lock()
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.calls++
	p.mu.Unlock()

	// Widen the window a real broadcast would occupy. Without serialisation the
	// overlap is easy to observe; with it, impossible.
	time.Sleep(5 * time.Millisecond)

	p.mu.Lock()
	p.active--
	p.mu.Unlock()
	return "0xdeadbeef", nil
}

// TestOperatorBroadcastIsSerialised pins the property that keeps the operator
// EOA's nonces distinct.
//
// buildUnsignedTx reads the nonce with PendingNonceAt, which only reflects what
// the node has already seen. Two callers that read it concurrently receive the
// same value while the first transaction is still un-mined, sign different
// payloads with the same nonce, and one is silently dropped by the node — after
// which the caller burns a 60s WaitForReceipt on a transaction that will never
// exist.
//
// This races today: the scheduler runs one goroutine per job (bill-reconciler
// settles, pm-recovery credits) alongside the HTTP request path, all sharing one
// signer with no lock.
//
// Serialising build+sign+broadcast is what fixes it: the nonce is then read
// AFTER the previous broadcast has returned. Deliberately no in-process nonce
// counter — a counter advances even when buildUnsignedTx fails at EstimateGas
// (a duplicate ref or an over-draw both revert there, routinely), and a burned
// nonce leaves every later transaction queued at the node forever. PendingNonceAt
// self-heals; a counter does not.
func TestOperatorBroadcastIsSerialised(t *testing.T) {
	const goroutines = 20

	svc := newTestSwap()
	probe := &concurrencyProbe{}
	svc.SetOperatorSigner(probe)

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var ref [32]byte
			ref[0] = byte(i)
			if _, err := svc.OperatorSettleDebit(context.Background(), ref); err != nil {
				t.Errorf("OperatorSettleDebit: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if probe.calls != goroutines {
		t.Fatalf("signer called %d times, want %d", probe.calls, goroutines)
	}
	if probe.maxActive != 1 {
		t.Errorf("max concurrent signer calls = %d, want 1.\n"+
			"Concurrent callers read the same pending nonce and one transaction is "+
			"silently dropped.", probe.maxActive)
	}
}
