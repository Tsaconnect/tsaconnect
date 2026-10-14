package services

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/crypto"
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

// fixedChain is a Sonic RPC whose pending nonce never moves (a lagging
// load-balanced view) and whose gas estimate can be made to revert.
type fixedChain struct {
	mu          sync.Mutex
	nonce       uint64
	estimateErr error
}

func (c *fixedChain) GetNonce(string) (uint64, error)    { return c.nonce, nil }
func (c *fixedChain) SuggestGasPrice() (*big.Int, error) { return big.NewInt(1_000_000_000), nil }
func (c *fixedChain) EstimateGas(ethereum.CallMsg) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return 120_000, c.estimateErr
}
func (c *fixedChain) ChainID() *big.Int { return big.NewInt(146) }

func operatorOnSharedLane(t *testing.T) (*MCGPSwapService, *SignerRegistry, *laneNode, *fixedChain) {
	t.Helper()
	key, _ := crypto.GenerateKey()
	reg := NewSignerRegistry()
	node := &laneNode{id: 146}
	svc := NewMCGPSwapService(nil, &config.Config{Env: "development", MCGPSwapBAddress: "0x000000000000000000000000000000000000bEEF"})
	chain := &fixedChain{nonce: 100}
	svc.chain = chain
	svc.SetOperatorLane(reg.Lane("it_operator", key, node))
	svc.operatorVerified.Store(true) // as ProbeOperatorRole does once operators() reads true
	// Same key, same chain: the gas sponsor's lane after the fallback.
	if reg.Lane("gas_sponsor", key, node) != svc.operatorLane {
		t.Fatal("setup: one key on one chain must be one lane")
	}
	return svc, reg, node, chain
}

// An OperatorSequence waiting on a receipt holds only the sequence lock: a gas
// top-up on the same key and chain still goes out, on the next nonce.
func TestOperatorSequence_DoesNotBlockAnotherRoleOnTheSameKeyLane(t *testing.T) {
	svc, _, node, _ := operatorOnSharedLane(t)
	sent, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- svc.OperatorSequence(context.Background(), func(ops OperatorOps) error {
			if _, err := ops.OperatorSettleDebit(context.Background(), [32]byte{1}); err != nil {
				return err
			}
			close(sent)
			<-release // a 60 s receipt wait
			return nil
		})
	}()
	<-sent
	start := time.Now()
	if _, _, err := svc.operatorLane.Send(context.Background(), func() ([]byte, error) { return unsignedOn(146, 100) }); err != nil {
		t.Fatalf("sponsor send: %v", err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("a send on the shared lane waited %v behind an operator sequence", time.Since(start))
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := node.sent(); len(got) != 2 || got[0] != 100 || got[1] != 101 {
		t.Fatalf("nonces %v, want the operator's 100 then the sponsor's 101", got)
	}
}

func TestOperatorDebits_ConcurrentOnALaggingRPCGetDistinctNonces(t *testing.T) {
	svc, _, node, _ := operatorOnSharedLane(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := svc.OperatorSettleDebit(context.Background(), [32]byte{byte(i)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	seen := map[uint64]bool{}
	for _, n := range node.sent() {
		seen[n] = true
	}
	if len(seen) != 4 || !seen[100] || !seen[103] {
		t.Fatalf("nonces %v, want 100..103 each once", node.sent())
	}
}

// A debit that reverts at EstimateGas (a duplicate debitRef, an over-draw)
// sends nothing and does not move the floor: the next operation still signs
// the chain's own pending nonce, so no gap opens.
func TestOperatorDebit_EstimateGasRevertDoesNotBurnANonce(t *testing.T) {
	svc, _, node, chain := operatorOnSharedLane(t)
	chain.estimateErr = errors.New("execution reverted: Debit exists")
	if _, err := svc.OperatorSettleDebit(context.Background(), [32]byte{1}); !errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("err = %v, want a not-broadcast refusal", err)
	}
	chain.mu.Lock()
	chain.estimateErr = nil
	chain.mu.Unlock()
	if _, err := svc.OperatorSettleDebit(context.Background(), [32]byte{2}); err != nil {
		t.Fatal(err)
	}
	if got := node.sent(); len(got) != 1 || got[0] != 100 {
		t.Fatalf("nonces %v, want only 100", got)
	}
}

// Until operators(<key>) is verified on the swap, the wired operator is
// dormant: HasOperator is false and every op is refused before it is built.
func TestOperator_UnverifiedKeyIsDormantAndSendsNothing(t *testing.T) {
	svc, _, node, _ := operatorOnSharedLane(t)
	svc.operatorVerified.Store(false)
	if svc.HasOperator() || !svc.OperatorConfigured() {
		t.Fatalf("HasOperator=%v OperatorConfigured=%v, want dormant but configured", svc.HasOperator(), svc.OperatorConfigured())
	}
	if _, err := svc.OperatorSettleDebit(context.Background(), [32]byte{1}); !errors.Is(err, ErrOperatorRoleMissing) || !errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("err = %v, want ErrOperatorRoleMissing (not broadcast)", err)
	}
	if len(node.sent()) != 0 {
		t.Fatal("nothing may be sent")
	}
}
