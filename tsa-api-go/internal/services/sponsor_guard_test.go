package services

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"
)

func newTestGuard(t *testing.T, mutate ...func(*SponsorLimits)) (*SponsorGuard, *time.Time) {
	t.Helper()
	clock := time.Date(2026, 9, 14, 23, 50, 0, 0, time.UTC)
	limits := SponsorLimits{
		PerUserPerWindow: 2,
		Window:           time.Minute,
		DailyGasBudget:   big.NewInt(100_000),
		BalanceFloor:     big.NewInt(1_000),
	}
	for _, m := range mutate {
		m(&limits)
	}
	return NewSponsorGuard("test_sponsor", limits, func() time.Time { return clock }), &clock
}

func TestSponsorGuard_RateLimitPerUserWindow(t *testing.T) {
	g, clock := newTestGuard(t)
	for i := 0; i < 2; i++ {
		if _, err := g.ReserveUser("0xAbC"); err != nil {
			t.Fatalf("reservation %d: %v", i, err)
		}
	}
	if _, err := g.ReserveUser("0xabc"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("third reservation in window: want ErrRateLimited, got %v", err)
	}
	*clock = clock.Add(2 * time.Minute)
	if _, err := g.ReserveUser("0xabc"); err != nil {
		t.Fatalf("after window: %v", err)
	}
}

func TestSponsorGuard_ReleaseUserHandsSlotBack(t *testing.T) {
	g, _ := newTestGuard(t, func(l *SponsorLimits) { l.PerUserPerWindow = 1 })
	res, err := g.ReserveUser("0xabc")
	if err != nil {
		t.Fatal(err)
	}
	g.ReleaseUser(res)
	if _, err := g.ReserveUser("0xabc"); err != nil {
		t.Fatalf("released slot not reusable: %v", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if n := len(g.perUser); n != 1 {
		t.Fatalf("perUser entries = %d, want 1", n)
	}
}

// A guard built without a per-user limit (as ramp lanes are) must not refuse
// everyone; zero means no limit.
func TestSponsorGuard_ZeroPerUserWindowIsUnlimited(t *testing.T) {
	g, _ := newTestGuard(t, func(l *SponsorLimits) { l.PerUserPerWindow = 0 })
	for i := 0; i < 50; i++ {
		if _, err := g.ReserveUser("0xabc"); err != nil {
			t.Fatalf("reservation %d with no per-user limit: %v", i, err)
		}
	}
}

// Addresses are free to mint: keys that relay once and never return must not
// stay in the map, even when nothing ever reserves for them again.
func TestSponsorGuard_SweepsExpiredKeys(t *testing.T) {
	g, clock := newTestGuard(t)
	for i := 0; i < 100; i++ {
		if _, err := g.ReserveUser(fmt.Sprintf("0x%040x", i)); err != nil {
			t.Fatalf("reservation %d: %v", i, err)
		}
	}
	*clock = clock.Add(2 * time.Minute)
	if _, err := g.ReserveUser("0xnewcomer"); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if n := len(g.perUser); n != 1 {
		t.Fatalf("perUser entries = %d, want 1 (expired keys swept)", n)
	}
}

func TestSponsorGuard_DailyBudgetRollsAtUTCMidnight(t *testing.T) {
	g, clock := newTestGuard(t)
	if _, err := g.ReserveGas(80_000); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ReserveGas(30_000); !errors.Is(err, ErrDailyCapExceeded) {
		t.Fatalf("over budget: want ErrDailyCapExceeded, got %v", err)
	}
	*clock = clock.Add(15 * time.Minute) // 00:05 next UTC day
	if _, err := g.ReserveGas(30_000); err != nil {
		t.Fatalf("after midnight: %v", err)
	}
}

func TestSponsorGuard_ReleaseGasSkippedAfterDayRoll(t *testing.T) {
	g, clock := newTestGuard(t)
	res, err := g.ReserveGas(50_000)
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(15 * time.Minute)
	if _, err := g.ReserveGas(10_000); err != nil {
		t.Fatal(err)
	}
	g.ReleaseGas(res) // yesterday's reservation must not decrement today
	if got := g.SpentToday(); got.Cmp(big.NewInt(10_000)) != 0 {
		t.Fatalf("SpentToday = %s, want 10000", got)
	}
}

// Floors are alert-only (owner 2026-10-13): below the floor only alerts; the
// one refusal is a balance that cannot pay the transaction's own need.
func TestSponsorGuard_FloorAlertsAndOnlyTheFeeRefuses(t *testing.T) {
	g, _ := newTestGuard(t)
	if !g.AlertFloor(big.NewInt(999)) || g.AlertFloor(big.NewInt(1_000)) {
		t.Fatal("AlertFloor must report below-floor only")
	}
	if err := g.RequireFee(big.NewInt(999), big.NewInt(500)); err != nil {
		t.Fatalf("below the floor but able to pay: %v", err)
	}
	if err := g.RequireFee(big.NewInt(499), big.NewInt(500)); !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("unable to pay: want ErrRelayerUnderfunded, got %v", err)
	}
}

func TestSponsorGuard_KillSwitch(t *testing.T) {
	g, _ := newTestGuard(t)
	if g.Killed() {
		t.Fatal("new guard reports killed")
	}
	g.SetKillSwitch(true)
	if !g.Killed() {
		t.Fatal("kill switch not applied")
	}
}

func TestSponsorGuard_DailyBudgetHoldsUnderConcurrency(t *testing.T) {
	g, _ := newTestGuard(t, func(l *SponsorLimits) { l.DailyGasBudget = big.NewInt(50_000) })
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.ReserveGas(10_000); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 5 {
		t.Fatalf("reservations granted = %d, want exactly 5", ok)
	}
}
