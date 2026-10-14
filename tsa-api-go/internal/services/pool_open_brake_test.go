package services

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math/big"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Pre-merge review I-1 (1): a failed open that sent slices locks LP stock for
// the slice window and burns treasury gas. poolOpenFailLimit of them in a row
// inside poolOpenFailWindow close the pool's gates (reopened by a restart,
// which re-runs every gate) and raise ALERT liquidity_pool_open_failing. A
// real failed open counts; a failure that sent nothing does not.
func TestPoolOpen_RepeatedFailedOpensCloseThePool(t *testing.T) {
	r := newPoolOpenRig(t)
	lp, _ := r.addLP(t, 50)
	r.chain.neverLand[walletOf(lp)] = true // broadcast, never mined: unproven
	if _, _, err := r.open(t, "5000"); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("open = %v", err)
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	now := time.Now()
	r.desk.noteOpenOutcome(false, 0, now) // nothing sent: never counts
	r.desk.noteOpenOutcome(false, 1, now)
	if r.gates.Open() == false {
		t.Fatal("closed after two failed opens that sent slices")
	}
	r.desk.noteOpenOutcome(false, 2, now)
	if r.gates.Open() || !strings.Contains(buf.String(), "ALERT liquidity_pool_open_failing") {
		t.Fatalf("open=%v after three failed opens that sent slices:\n%s", r.gates.Open(), buf.String())
	}
}

// The count is of consecutive failures inside the window: an open that
// succeeds clears it, and failures further apart than the window never trip.
func TestPoolOpenBreaker_SuccessAndTimeReset(t *testing.T) {
	var b poolOpenBreaker
	now := time.Now()
	b.record(false, 1, now)
	b.record(false, 1, now)
	b.record(true, 2, now)
	if b.record(false, 1, now) {
		t.Fatal("a success in between must reset the count")
	}
	var c poolOpenBreaker
	c.record(false, 1, now)
	c.record(false, 1, now.Add(poolOpenFailWindow/2))
	if c.record(false, 1, now.Add(poolOpenFailWindow+time.Second)) {
		t.Fatal("a failure older than the window still counted")
	}
	if !c.record(false, 1, now.Add(poolOpenFailWindow+2*time.Second)) {
		t.Fatal("three failures inside the window must trip")
	}
}

// Pre-merge review I-1 (2): a failed open that sent slices counts against her
// pool daily limit (the failed order locked LP stock just as a live one does);
// a failed open that sent nothing does not.
func TestPoolDailyCap_CountsFailedOpensThatSentSlices(t *testing.T) {
	r := newPoolOpenRig(t)
	lp, _ := r.addLP(t, 50)
	r.chain.neverLand[walletOf(lp)] = true
	if _, _, err := r.open(t, "5000"); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("open = %v", err)
	}
	var failed models.ITIntent
	r.db.Where("provider = ? AND failure_reason = ?", models.ITProviderPool, models.ITFailurePoolOpenFailed).First(&failed)
	used, _ := new(big.Int).SetString(failed.AssetAmount, 10)
	set := specPoolSettings()
	room := new(big.Int).Sub(set.UserDailyMaxUnits, used)
	room.Add(room, big.NewInt(1)) // one unit past the cap once the failed open counts
	if err := r.it.CheckPoolDailyCap(context.Background(), r.userID, room, set); !errors.Is(err, ErrPoolDailyCapReached) {
		t.Fatalf("cap with a failed open that sent slices = %v, want reached", err)
	}

	r2 := newPoolOpenRig(t)
	lp2, _ := r2.addLP(t, 50)
	r2.chain.prepareErr[walletOf(lp2)] = errors.New("execution reverted: VoucherExpired") // nothing sent
	if _, _, err := r2.open(t, "5000"); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("open = %v", err)
	}
	if err := r2.it.CheckPoolDailyCap(context.Background(), r2.userID, set.UserDailyMaxUnits, set); err != nil {
		t.Fatalf("a failed open that sent nothing counted: %v", err)
	}
}

// countingChain counts the lane's native-balance reads.
type countingChain struct {
	sponsorChain
	mu       sync.Mutex
	balances int
}

func (c *countingChain) BalanceAt(ctx context.Context, a common.Address, b *big.Int) (*big.Int, error) {
	c.mu.Lock()
	c.balances++
	c.mu.Unlock()
	return c.sponsorChain.BalanceAt(ctx, a, b)
}

// Pre-merge review I-1 (4): the burst's reads come before its sends. Every
// slice's open is prepared (gas price, estimate) before the first broadcast,
// and the treasury's gas is read once for the burst, not once per slice; only
// the nonce is read per slice, under the lane lock, and recorded before its
// broadcast.
func TestPoolOpen_BurstReadsOnceThenSends(t *testing.T) {
	r := newPoolOpenRig(t)
	threeLPs(t, r)
	lane := r.desk.c.Treasury.Lane("bsc")
	cc := &countingChain{sponsorChain: lane.Chain}
	lane.Chain = cc
	var mu sync.Mutex
	var events []string
	r.chain.onPrepare = func() { mu.Lock(); events = append(events, "prepare"); mu.Unlock() }
	r.chain.onBroadcast = func(uint64, [32]byte) { mu.Lock(); events = append(events, "broadcast"); mu.Unlock() }
	intent, _, err := r.open(t, "100000") // two slices
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "prepare,prepare,broadcast,broadcast" {
		t.Fatalf("events = %s, want both slices prepared before the first send", got)
	}
	// One read for the guard's cached gas reading, one for the burst, and one
	// per send: the key lane's fee gate reads the balance under its lock.
	if cc.balances != 4 {
		t.Fatalf("treasury balance read %d times for a two-slice burst, want 4", cc.balances)
	}
	slices := r.slicesOf(t, intent.ID)
	if len(slices) != 2 || slices[0].OpenNonce == nil || slices[1].OpenNonce == nil || *slices[0].OpenNonce == *slices[1].OpenNonce {
		t.Fatalf("slices = %+v, want two distinct recorded nonces", slices)
	}
}

// The nonce stays a per-slice read under the lane lock (pre-merge review
// I-1): a transaction another sender lands on the treasury between two slices
// moves the second slice past it, where the nonce prepared before the burst
// (or the floor alone) would collide with it.
func TestPoolOpen_EachSliceReadsTheNonceAtItsSend(t *testing.T) {
	r := newPoolOpenRig(t)
	threeLPs(t, r)
	first := true
	r.chain.onBroadcast = func(uint64, [32]byte) {
		if first {
			first = false
			r.chain.cmu.Lock()
			r.chain.nextNonce += 5 // five transactions from elsewhere
			r.chain.cmu.Unlock()
		}
	}
	intent, _, err := r.open(t, "100000")
	if err != nil {
		t.Fatal(err)
	}
	slices := r.slicesOf(t, intent.ID)
	if len(slices) != 2 || slices[0].OpenNonce == nil || slices[1].OpenNonce == nil {
		t.Fatalf("slices = %+v", slices)
	}
	a, b := *slices[0].OpenNonce, *slices[1].OpenNonce
	if a > b {
		a, b = b, a
	}
	if b != a+6 {
		t.Fatalf("nonces %d and %d, want the second past the five sent in between (%d)", a, b, a+6)
	}
}
