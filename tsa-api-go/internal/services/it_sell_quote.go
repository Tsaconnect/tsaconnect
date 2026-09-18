package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
)

// it_sell_quote.go — pricing for the MCGP sell leg, shared by the sell itself
// and the pre-trade quote the app shows while the user types.
//
// There is exactly one pricing path: CreateSellIntent and both quote directions
// go through priceSellAt, so what the screen shows is what the sell charges at
// the same rate. The sell still re-prices at submit time — a quote is a
// snapshot, not a lock.

// ErrITSellQuoteUnsolvable is returned when no MCGP amount could be found that
// pays out the requested naira within the search budget — in practice only a
// fee configuration that eats almost the whole amount.
var ErrITSellQuoteUnsolvable = errors.New("could not price this amount — try a different amount")

// SellPrice is one fully priced MCGP sell: the MCGP debited, the naira it is
// worth at the live sell rate, and the fees that come out of that payout.
type SellPrice struct {
	McgpWei  *big.Int
	RateKobo *big.Int
	Fees     *ITFeeBreakdown
}

// sellRate resolves the live sell rate, refusing a stale FX feed. A quote and a
// sell both refuse stale pricing: showing a figure the sell would reject is the
// dead end this file exists to remove.
func (s *ITService) sellRate(ctx context.Context) (*big.Int, error) {
	_, rate, fx, err := s.EffectiveKoboRates(ctx)
	if err != nil {
		return nil, err
	}
	if fx.Stale || rate == nil || rate.Sign() <= 0 {
		return nil, ErrITFxUnavailable
	}
	return rate, nil
}

// priceSellAt prices mcgpWei at a known sell rate: gross valuation, fees out of
// the payout, the payout-currency gate, and the minimum payout on the NET.
//
// destCurrency is the payout rail's currency; blank means the legacy NGN bank.
func (s *ITService) priceSellAt(ctx context.Context, mcgpWei, rateKobo *big.Int, destCurrency string) (*SellPrice, error) {
	gross := ngnForMcgp(mcgpWei, rateKobo)
	// The platform tier follows the payout destination: a foreign rail owes the
	// FX tier even though the valuation is in naira.
	fees, err := s.quoteITFees(ctx, FeeFlowWithdraw, gross, destCurrency)
	if err != nil {
		return nil, err
	}
	// The registry is the authority on whether a currency may move money at all.
	// The catalog governs what we OFFER as a product; without this check the two
	// could drift — a destination could sit in the catalog while its registry row
	// says PayoutEnabled=false, and nothing would stop the payout.
	if dest := strings.ToUpper(strings.TrimSpace(destCurrency)); dest != "" && s.currencies != nil {
		if _, cErr := s.currencies.RequirePayout(dest); cErr != nil {
			return nil, cErr
		}
	}
	if err := s.checkMinSellPayout(fees.Net); err != nil {
		return nil, err
	}
	return &SellPrice{McgpWei: new(big.Int).Set(mcgpWei), RateKobo: new(big.Int).Set(rateKobo), Fees: fees}, nil
}

// checkMinSellPayout enforces the payout floor on the NET — the amount handed
// to the gateway. A sell under the provider's floor would debit the user's
// premium and then fail the payout, stranding it, so this runs before any debit.
//
// Sells are denominated in NGN today (the valuation currency), so the floor is
// the NGN one even for a cross-currency payout: that is the balance actually
// debited and the figure this check has in hand.
func (s *ITService) checkMinSellPayout(netKobo *big.Int) error {
	minMinor, label := s.minPayoutMinorFor("NGN")
	if minMinor > 0 && netKobo.Cmp(big.NewInt(minMinor)) < 0 {
		return fmt.Errorf("minimum payout is %s%s after fees — sell a little more",
			minorLabel(label), minorToMajor(minMinor, s.exponentFor(label)))
	}
	return nil
}

// QuoteSell prices selling mcgpWei right now — the forward direction ("I sell
// 100 MCGP, what lands in my bank?").
func (s *ITService) QuoteSell(ctx context.Context, mcgpWei *big.Int, destCurrency string) (*SellPrice, error) {
	if mcgpWei == nil || mcgpWei.Sign() <= 0 {
		return nil, fmt.Errorf("invalid mcgpAmount")
	}
	rate, err := s.sellRate(ctx)
	if err != nil {
		return nil, err
	}
	return s.priceSellAt(ctx, mcgpWei, rate, destCurrency)
}

// sellQuoteMaxEvals bounds how many fee quotes one inverse search may run.
// Each can be a live Flutterwave fee call, so the budget is a latency bound as
// much as a CPU one. A linear fee schedule converges in well under ten.
const sellQuoteMaxEvals = 40

// sellQuoteBackScan is how far below the found boundary (in kobo of gross) the
// search looks for a cheaper amount that still pays the target.
//
// The net payout is not strictly monotone in the gross: every percentage fee
// line is floored to the kobo on its own, so when several lines tick over on
// the same kobo the net drops by up to one per line, then climbs back one kobo
// at a time. Five floored lines (platform, provider, VAT, platform tax, swap)
// bound that dip at four kobo, so a smaller amount that still clears the target
// can only sit within a few kobo of the boundary bisection finds.
const sellQuoteBackScan = 6

// QuoteSellForNet is the inverse direction: the SMALLEST MCGP amount whose net
// payout, after every fee, is at least netKobo ("I need ₦50,000 in my bank").
//
// The fees are never inverted algebraically — tiers, the live provider fee and
// per-line rounding make that wrong — so this searches over the gross naira
// using the sell's own pricing function:
//
//  1. net ≤ gross always, so any gross below the target fails; start there.
//  2. Secant steps on the observed fee ratio until an amount pays enough.
//  3. Bisect between the last failing and first passing gross.
//  4. Scan a few kobo below the boundary for a rounding dip (see
//     sellQuoteBackScan).
//
// The MCGP for a gross G is ceil(G·1e18 / rate): the smallest wei amount the
// sell values at G. Every result is re-priced through priceSellAt, so the
// returned Fees.Net is the exact figure the sell computes at this rate — and is
// guaranteed ≥ netKobo.
func (s *ITService) QuoteSellForNet(ctx context.Context, netKobo *big.Int, destCurrency string) (*SellPrice, error) {
	if netKobo == nil || netKobo.Sign() <= 0 {
		return nil, fmt.Errorf("invalid receive amount")
	}
	// A target under the floor can never be paid, whatever MCGP backs it; say
	// so directly instead of searching.
	if err := s.checkMinSellPayout(netKobo); err != nil {
		return nil, err
	}
	rate, err := s.sellRate(ctx)
	if err != nil {
		return nil, err
	}
	gross, err := s.searchSellGross(ctx, netKobo, destCurrency)
	if err != nil {
		return nil, err
	}
	price, err := s.priceSellAt(ctx, mcgpForSellGross(gross, rate), rate, destCurrency)
	if err != nil {
		return nil, err
	}
	// Belt and braces: the search already proved this, but a quote that says
	// "you receive ₦X" must never be below what the user asked for.
	if price.Fees.Net.Cmp(netKobo) < 0 {
		return nil, ErrITSellQuoteUnsolvable
	}
	return price, nil
}

// mcgpForSellGross is the smallest wei amount ngnForMcgp values at grossKobo:
// ceil(gross·1e18 / rate). Flooring instead would value one kobo short and pay
// out less than the quote promised.
func mcgpForSellGross(grossKobo, rateKobo *big.Int) *big.Int {
	num := new(big.Int).Mul(grossKobo, oneE18)
	q, r := new(big.Int).QuoRem(num, rateKobo, new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// sellNetEvaluator memoizes net(gross) for one search. Safe for concurrent use
// by the back-scan.
type sellNetEvaluator struct {
	s     *ITService
	dest  string
	mu    sync.Mutex
	memo  map[string]*big.Int
	evals int
}

// net returns the payout for a gross valuation, 0 when fees swallow it.
func (e *sellNetEvaluator) net(ctx context.Context, gross *big.Int) (*big.Int, error) {
	key := gross.String()
	e.mu.Lock()
	if v, ok := e.memo[key]; ok {
		e.mu.Unlock()
		return v, nil
	}
	if e.evals >= sellQuoteMaxEvals {
		e.mu.Unlock()
		return nil, ErrITSellQuoteUnsolvable
	}
	e.evals++
	e.mu.Unlock()

	var net *big.Int
	if gross.Sign() <= 0 {
		net = big.NewInt(0)
	} else {
		fees, err := e.s.quoteITFees(ctx, FeeFlowWithdraw, gross, e.dest)
		switch {
		case errors.Is(err, ErrITAmountBelowFees):
			net = big.NewInt(0)
		case err != nil:
			return nil, err
		default:
			net = fees.Net
		}
	}
	e.mu.Lock()
	e.memo[key] = net
	e.mu.Unlock()
	return net, nil
}

// searchSellGross finds the smallest gross (kobo) whose net ≥ target, within
// the rounding caveat documented on sellQuoteBackScan.
func (s *ITService) searchSellGross(ctx context.Context, target *big.Int, dest string) (*big.Int, error) {
	ev := &sellNetEvaluator{s: s, dest: dest, memo: map[string]*big.Int{}}
	one := big.NewInt(1)

	// net ≤ gross, so target−1 is a known failing gross without asking.
	lo := new(big.Int).Sub(target, one)
	var hi *big.Int

	g := new(big.Int).Set(target)
	for hi == nil {
		n, err := ev.net(ctx, g)
		if err != nil {
			return nil, err
		}
		if n.Cmp(target) >= 0 {
			hi = g
			break
		}
		if g.Cmp(lo) > 0 {
			lo = g
		}
		// Secant on the observed ratio: g·target/n, rounded up. With no net at
		// all (fees ate it) double instead.
		next := new(big.Int)
		if n.Sign() > 0 {
			num := new(big.Int).Mul(g, target)
			q, r := new(big.Int).QuoRem(num, n, new(big.Int))
			if r.Sign() > 0 {
				q.Add(q, one)
			}
			next = q
		} else {
			next.Mul(g, big.NewInt(2))
		}
		// Always make progress past the last failure — rounding can leave the
		// estimate on or below it.
		if next.Cmp(lo) <= 0 {
			next.Add(lo, one)
		}
		g = next
	}

	// The secant estimate usually lands within a few kobo of the boundary while
	// lo still sits at the first probe, ~one fee-rate below. Gallop down from hi
	// (1, 2, 4 … kobo) to tighten lo before bisecting, so the bisection covers a
	// handful of kobo instead of the whole fee spread.
	for step := big.NewInt(1); ; step.Lsh(step, 1) {
		probe := new(big.Int).Sub(hi, step)
		if probe.Cmp(lo) <= 0 {
			break
		}
		n, err := ev.net(ctx, probe)
		if err != nil {
			return nil, err
		}
		if n.Cmp(target) < 0 {
			lo = probe
			break
		}
		hi = probe
	}

	// Bisect: lo fails, hi passes.
	for new(big.Int).Sub(hi, lo).Cmp(one) > 0 {
		mid := new(big.Int).Add(lo, hi)
		mid.Rsh(mid, 1)
		n, err := ev.net(ctx, mid)
		if err != nil {
			return nil, err
		}
		if n.Cmp(target) >= 0 {
			hi = mid
		} else {
			lo = mid
		}
	}

	// Back-scan for a rounding dip. hi−1 is already known to fail, so look at
	// hi−2 … hi−1−sellQuoteBackScan (never below the target: net ≤ gross), in
	// parallel since each may be a network call. Repeat from any lower hit.
	for {
		lower, err := ev.lowestPassingBelow(ctx, hi, target)
		if err != nil {
			return nil, err
		}
		if lower == nil {
			return hi, nil
		}
		hi = lower
	}
}

// lowestPassingBelow checks the back-scan window under hi and returns the
// smallest gross in it that pays the target, or nil when none does.
func (e *sellNetEvaluator) lowestPassingBelow(ctx context.Context, hi, target *big.Int) (*big.Int, error) {
	var cands []*big.Int
	for d := int64(2); d <= sellQuoteBackScan+1; d++ {
		c := new(big.Int).Sub(hi, big.NewInt(d))
		if c.Cmp(target) < 0 {
			break
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return nil, nil
	}
	nets := make([]*big.Int, len(cands))
	errs := make([]error, len(cands))
	var wg sync.WaitGroup
	for i, c := range cands {
		wg.Add(1)
		go func(i int, c *big.Int) {
			defer wg.Done()
			nets[i], errs[i] = e.net(ctx, c)
		}(i, c)
	}
	wg.Wait()
	var lowest *big.Int
	for i, c := range cands {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if nets[i].Cmp(target) >= 0 {
			lowest = c // cands descend, so the last hit is the smallest
		}
	}
	return lowest, nil
}
