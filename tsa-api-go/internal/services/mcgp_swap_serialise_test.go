package services

import (
	"context"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// sectionProbe records the maximum number of goroutines inside the
// nonce-read → broadcast window at once. The fake chain opens the window when
// the nonce is read; the fake signer closes it after broadcasting.
//
// That span is exactly what signMu must cover. A probe on the signer alone
// cannot see it: serialising only the broadcast still lets two goroutines read
// the same nonce first, which is the bug.
type sectionProbe struct {
	mu        sync.Mutex
	active    int
	maxActive int
	broadcast int
}

func (p *sectionProbe) enter() {
	p.mu.Lock()
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.mu.Unlock()
}

func (p *sectionProbe) exit() {
	p.mu.Lock()
	p.active--
	p.broadcast++
	p.mu.Unlock()
}

type probeChain struct{ p *sectionProbe }

func (c *probeChain) GetNonce(string) (uint64, error) {
	c.p.enter()
	// Widen the window a real PendingNonceAt round trip occupies.
	time.Sleep(2 * time.Millisecond)
	return 100, nil
}
func (c *probeChain) SuggestGasPrice() (*big.Int, error)           { return big.NewInt(1_000_000_000), nil }
func (c *probeChain) EstimateGas(ethereum.CallMsg) (uint64, error) { return 120_000, nil }
func (c *probeChain) ChainID() *big.Int                            { return big.NewInt(146) }

type probeSigner struct{ p *sectionProbe }

func (s *probeSigner) Address() string { return "0x0000000000000000000000000000000000000A11" }
func (s *probeSigner) SignAndBroadcast(context.Context, []byte) (string, error) {
	time.Sleep(2 * time.Millisecond)
	s.p.exit()
	return "0xdeadbeef", nil
}

// TestOperatorBroadcastIsSerialised pins the property that keeps the operator
// EOA's nonces distinct.
//
// buildUnsignedTx reads the nonce with PendingNonceAt, which reflects only what
// the node has already seen. Two callers that read it concurrently receive the
// same value while the first transaction is still un-mined, sign different
// payloads with it, and the node silently drops one — after which the caller
// burns a 60s WaitForReceipt on a transaction that will never exist.
//
// This races today: the scheduler's bill-reconciler settles and reverses on this
// key while the HTTP request path debits on it, with no lock.
//
// Deliberately no in-process nonce counter. A counter advances even when
// buildUnsignedTx fails at EstimateGas — a duplicate debitRef ("Debit exists")
// and an over-draw both revert there, routinely — and one burned nonce leaves
// every later transaction queued at the node forever, each reporting a hash that
// never mines. PendingNonceAt self-heals from a gap; a counter does not.
func TestOperatorBroadcastIsSerialised(t *testing.T) {
	const goroutines = 20

	probe := &sectionProbe{}
	svc := NewMCGPSwapService(nil, &config.Config{
		Env:              "development",
		MCGPSwapBAddress: "0x000000000000000000000000000000000000bEEF",
	})
	svc.chain = &probeChain{p: probe}
	svc.SetOperatorSigner(&probeSigner{p: probe})

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

	if probe.broadcast != goroutines {
		t.Fatalf("broadcast %d times, want %d", probe.broadcast, goroutines)
	}
	if probe.maxActive != 1 {
		t.Errorf("max goroutines between the nonce read and the broadcast = %d, want 1.\n"+
			"Concurrent callers read the same pending nonce and one transaction is "+
			"silently dropped.", probe.maxActive)
	}
}
