package services

import (
	"log"
	"math/big"
	"strings"
	"sync"
	"time"
)

// SponsorLimits are the spend controls shared by every path that pays gas on a
// user's behalf. A nil budget or floor, or a PerUserPerWindow <= 0, disables
// that control.
type SponsorLimits struct {
	PerUserPerWindow int
	Window           time.Duration
	DailyGasBudget   *big.Int // GAS UNITS per UTC day
	BalanceFloor     *big.Int // wei; refuse to sponsor below this native balance
}

// SponsorReservation records what a request consumed so it can be handed back
// when the request never reaches the chain.
type SponsorReservation struct {
	day  string
	gas  uint64
	user string
	at   time.Time
}

// SponsorGuard is the budget and abuse control for one sponsoring key on one
// chain. Extracted from RelayerService so the ramp sponsor does not grow a
// second copy of the check-and-reserve logic, which has already needed a
// concurrency fix once (see TestSponsorGuard_DailyBudgetHoldsUnderConcurrency).
type SponsorGuard struct {
	name   string
	limits SponsorLimits
	now    func() time.Time

	mu        sync.Mutex
	perUser   map[string][]time.Time
	lastSweep time.Time
	spentDay  string
	spentGas  *big.Int
	killed    bool
}

func NewSponsorGuard(name string, limits SponsorLimits, now func() time.Time) *SponsorGuard {
	if now == nil {
		now = time.Now
	}
	return &SponsorGuard{
		name: name, limits: limits, now: now,
		perUser: map[string][]time.Time{}, spentGas: new(big.Int),
	}
}

func (g *SponsorGuard) SetKillSwitch(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.killed = on
}

func (g *SponsorGuard) Killed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.killed
}

// CheckFloor refuses rather than broadcast transactions that will fail for gas.
func (g *SponsorGuard) CheckFloor(balance *big.Int) error {
	if g.limits.BalanceFloor != nil && balance != nil && balance.Cmp(g.limits.BalanceFloor) < 0 {
		log.Printf("[%s] ALERT %s_underfunded_total=1 balance=%s floor=%s", g.name, g.name, balance, g.limits.BalanceFloor)
		return ErrRelayerUnderfunded
	}
	return nil
}

// ReserveUser prunes the window and claims a slot in one critical section.
func (g *SponsorGuard) ReserveUser(key string) (SponsorReservation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	key = strings.ToLower(strings.TrimSpace(key))
	now := g.now()
	cutoff := now.Add(-g.limits.Window)
	if g.limits.Window > 0 && now.Sub(g.lastSweep) >= g.limits.Window {
		g.sweepLocked(cutoff)
		g.lastSweep = now
	}

	kept := g.perUser[key][:0]
	for _, t := range g.perUser[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		// Drop the key rather than leaving an empty slice: addresses are free to
		// mint, so per-key pruning alone leaks one entry per address forever.
		delete(g.perUser, key)
	} else {
		g.perUser[key] = kept
	}

	// PerUserPerWindow <= 0 means no per-user limit. Without this guard a zero
	// value would refuse every request (len(kept) >= 0 is always true).
	if g.limits.PerUserPerWindow > 0 && len(kept) >= g.limits.PerUserPerWindow {
		log.Printf("[%s] %s_rate_limited_total=1 from=%s", g.name, g.name, key)
		return SponsorReservation{}, ErrRateLimited
	}
	g.perUser[key] = append(kept, now)
	return SponsorReservation{user: key, at: now}, nil
}

// sweepLocked drops every timestamp not after cutoff and deletes keys left
// empty. ReserveUser runs it at most once per window, so the map is bounded by
// the keys active in the last window rather than every key ever seen.
func (g *SponsorGuard) sweepLocked(cutoff time.Time) {
	for key, times := range g.perUser {
		kept := times[:0]
		for _, t := range times {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(g.perUser, key)
		} else {
			g.perUser[key] = kept
		}
	}
}

// ReleaseUser gives a slot back when the failure was ours, not the caller's.
func (g *SponsorGuard) ReleaseUser(res SponsorReservation) {
	if res.user == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	entries := g.perUser[res.user]
	for i, t := range entries {
		if t.Equal(res.at) {
			remaining := append(entries[:i], entries[i+1:]...)
			if len(remaining) == 0 {
				delete(g.perUser, res.user)
			} else {
				g.perUser[res.user] = remaining
			}
			return
		}
	}
}

// ReserveGas checks AND reserves in one critical section; check-then-act
// overran a 5-request budget 2x under 50 goroutines.
func (g *SponsorGuard) ReserveGas(units uint64) (SponsorReservation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rollDayLocked()

	projected := new(big.Int).Add(g.spentGas, new(big.Int).SetUint64(units))
	if g.limits.DailyGasBudget != nil && projected.Cmp(g.limits.DailyGasBudget) > 0 {
		log.Printf("[%s] ALERT %s_daily_cap_exhausted_total=1 spent=%s budget=%s",
			g.name, g.name, g.spentGas, g.limits.DailyGasBudget)
		return SponsorReservation{}, ErrDailyCapExceeded
	}
	g.spentGas = projected
	return SponsorReservation{day: g.spentDay, gas: units}, nil
}

// ReleaseGas hands back gas for a request that never broadcast. Skipped when the
// UTC day rolled in between: the counter it would decrement is another day's.
func (g *SponsorGuard) ReleaseGas(res SponsorReservation) {
	if res.gas == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.spentDay != res.day {
		return
	}
	g.spentGas = new(big.Int).Sub(g.spentGas, new(big.Int).SetUint64(res.gas))
	if g.spentGas.Sign() < 0 {
		g.spentGas = new(big.Int)
	}
}

func (g *SponsorGuard) SpentToday() *big.Int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rollDayLocked()
	return new(big.Int).Set(g.spentGas)
}

// Floor is the native balance below which this guard refuses to sponsor.
func (g *SponsorGuard) Floor() *big.Int { return g.limits.BalanceFloor }

func (g *SponsorGuard) rollDayLocked() {
	day := g.now().UTC().Format("2006-01-02")
	if g.spentDay != day {
		g.spentDay = day
		g.spentGas = new(big.Int)
	}
}
