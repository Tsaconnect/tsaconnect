package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// newSellQuoteService is the IT test service with a live sell rate:
// $0.02/MCGP, 5% sell discount, ₦1500/$ ⇒ 2850 kobo per MCGP.
func newSellQuoteService(t *testing.T) *ITService {
	t.Helper()
	s := newITTestService(t)
	if _, err := s.SetRate(context.Background(), uuid.New(), 20000, 500); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	return s
}

func testSellRate(t *testing.T, s *ITService) *big.Int {
	t.Helper()
	r, err := s.sellRate(context.Background())
	if err != nil {
		t.Fatalf("sell rate: %v", err)
	}
	if r.String() != "2850" {
		t.Fatalf("sell rate = %s, want 2850 kobo (fixture drifted)", r)
	}
	return r
}

// netTable prices every gross in [from, to] through the real fee engine.
func netTable(t *testing.T, s *ITService, from, to int64) map[int64]int64 {
	t.Helper()
	out := make(map[int64]int64, to-from+1)
	for g := from; g <= to; g++ {
		fees, err := s.quoteITFees(context.Background(), FeeFlowWithdraw, big.NewInt(g), "")
		if errors.Is(err, ErrITAmountBelowFees) {
			out[g] = 0
			continue
		}
		if err != nil {
			t.Fatalf("quote %d: %v", g, err)
		}
		out[g] = fees.Net.Int64()
	}
	return out
}

// TestQuoteSellForNet_MatchesBruteForceMinimum is the rounding rule itself:
// for every target in the window, the quote picks the SMALLEST gross whose net
// clears the target — the same answer an exhaustive scan gives — and never one
// whose net falls short.
//
// The window deliberately spans many kobo where several floored fee lines tick
// over together, so the net dips; a plain bisection returns a larger amount
// there, which is what the back-scan exists to catch.
func TestQuoteSellForNet_MatchesBruteForceMinimum(t *testing.T) {
	s := newSellQuoteService(t)
	rate := testSellRate(t, s)
	ctx := context.Background()

	const from, to = 10000, 13000 // ₦100 … ₦130 of payout
	table := netTable(t, s, from, to+2000)

	dips := 0
	for g := int64(from + 1); g <= to; g++ {
		if table[g] < table[g-1] {
			dips++
		}
	}
	if dips == 0 {
		t.Fatal("fixture has no rounding dips — the test would not exercise the back-scan")
	}

	for target := int64(from); target <= to; target++ {
		want := int64(-1)
		for g := target; g <= to+2000; g++ {
			if table[g] >= target {
				want = g
				break
			}
		}
		if want < 0 {
			t.Fatalf("target %d: table too short", target)
		}

		price, err := s.QuoteSellForNet(ctx, big.NewInt(target), "")
		if err != nil {
			t.Fatalf("target %d: %v", target, err)
		}
		gross := price.Fees.Gross.Int64()
		if price.Fees.Net.Int64() < target {
			t.Fatalf("target %d: net %s is below the target", target, price.Fees.Net)
		}
		if gross != want {
			t.Fatalf("target %d: gross %d, smallest paying gross is %d", target, gross, want)
		}
		// The MCGP is the smallest wei amount valued at that gross.
		if ngnForMcgp(price.McgpWei, rate).Int64() != gross {
			t.Fatalf("target %d: mcgp %s is valued at %s, not %d", target, price.McgpWei, ngnForMcgp(price.McgpWei, rate), gross)
		}
		less := new(big.Int).Sub(price.McgpWei, big.NewInt(1))
		if ngnForMcgp(less, rate).Int64() >= gross {
			t.Fatalf("target %d: one wei less still values at %d — not the smallest MCGP", target, gross)
		}
	}
}

// TestQuoteSellForNet_LargeAmountRoundTrips checks a realistic ₦50,000 request
// and that the forward quote for the returned MCGP reports the same net — the
// figure the sell itself will compute at this rate.
func TestQuoteSellForNet_LargeAmountRoundTrips(t *testing.T) {
	s := newSellQuoteService(t)
	ctx := context.Background()
	target := big.NewInt(5_000_000) // ₦50,000

	inv, err := s.QuoteSellForNet(ctx, target, "")
	if err != nil {
		t.Fatalf("inverse: %v", err)
	}
	if inv.Fees.Net.Cmp(target) < 0 {
		t.Fatalf("net %s below target %s", inv.Fees.Net, target)
	}
	fwd, err := s.QuoteSell(ctx, inv.McgpWei, "")
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if fwd.Fees.Net.Cmp(inv.Fees.Net) != 0 || fwd.Fees.Gross.Cmp(inv.Fees.Gross) != 0 {
		t.Fatalf("forward %s/%s != inverse %s/%s", fwd.Fees.Gross, fwd.Fees.Net, inv.Fees.Gross, inv.Fees.Net)
	}
	// One kobo less of gross must not pay the target.
	lessGross := new(big.Int).Sub(inv.Fees.Gross, big.NewInt(1))
	fees, err := s.quoteITFees(ctx, FeeFlowWithdraw, lessGross, "")
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if fees.Net.Cmp(target) >= 0 {
		t.Fatalf("gross %s already pays %s — the quote overcharges", lessGross, fees.Net)
	}
	if inv.Fees.Lines == nil || inv.Fees.Lines.TotalFees.Minor.Cmp(inv.Fees.Fee) != 0 {
		t.Fatal("itemized fee lines missing or disagreeing with the total")
	}
}

// TestQuoteSellForNet_BelowFloorRefused: a payout under the ₦100 floor can
// never be sent, so it is refused with the same message the sell uses.
func TestQuoteSellForNet_BelowFloorRefused(t *testing.T) {
	s := newSellQuoteService(t)
	_, err := s.QuoteSellForNet(context.Background(), big.NewInt(defaultMinSellNgnKobo-1), "")
	if err == nil || !strings.Contains(err.Error(), "minimum payout") {
		t.Fatalf("err = %v, want the minimum-payout refusal", err)
	}
	if _, err := s.QuoteSellForNet(context.Background(), big.NewInt(defaultMinSellNgnKobo), ""); err != nil {
		t.Fatalf("exactly the floor should quote: %v", err)
	}
}

// TestQuoteSell_BelowFloorRefused: the forward quote applies the same NET
// floor as the sell — 3 MCGP at ₦28.50 grosses ₦85.50.
func TestQuoteSell_BelowFloorRefused(t *testing.T) {
	s := newSellQuoteService(t)
	three := new(big.Int).Mul(big.NewInt(3), oneE18)
	_, err := s.QuoteSell(context.Background(), three, "")
	if err == nil || !strings.Contains(err.Error(), "minimum payout") {
		t.Fatalf("err = %v, want the minimum-payout refusal", err)
	}
}

// TestSellQuote_StaleOrMissingFxIsPricingUnavailable: both directions refuse a
// stale feed with ErrITFxUnavailable — the retryable "rates are refreshing"
// state in the app, never a quote the sell would then reject.
func TestSellQuote_StaleOrMissingFxIsPricingUnavailable(t *testing.T) {
	ctx := context.Background()
	for name, fx := range map[string]stubFx{
		"stale": {ngn: 1500, stale: true},
		"down":  {err: errors.New("feed down")},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSellQuoteService(t)
			s.fx = fx
			if _, err := s.QuoteSell(ctx, oneE18, ""); !errors.Is(err, ErrITFxUnavailable) {
				t.Fatalf("forward err = %v, want ErrITFxUnavailable", err)
			}
			if _, err := s.QuoteSellForNet(ctx, big.NewInt(50000), ""); !errors.Is(err, ErrITFxUnavailable) {
				t.Fatalf("inverse err = %v, want ErrITFxUnavailable", err)
			}
		})
	}
}

// swallowAllFees is a fee engine under which nothing is ever viable.
type swallowAllFees struct{ calls int }

func (f *swallowAllFees) Quote(context.Context, FeeQuoteInput) (*FeeQuote, error) {
	f.calls++
	return &FeeQuote{Viable: false, NetAmount: FeeLine{Minor: big.NewInt(0)}}, nil
}

// TestQuoteSellForNet_SearchIsBounded: when no amount can pay out, the search
// gives up within its evaluation budget instead of spinning (each evaluation
// can be a live provider call).
func TestQuoteSellForNet_SearchIsBounded(t *testing.T) {
	s := newSellQuoteService(t)
	fees := &swallowAllFees{}
	s.SetFeeQuoter(fees)
	_, err := s.QuoteSellForNet(context.Background(), big.NewInt(50000), "")
	if !errors.Is(err, ErrITSellQuoteUnsolvable) {
		t.Fatalf("err = %v, want ErrITSellQuoteUnsolvable", err)
	}
	if fees.calls > sellQuoteMaxEvals {
		t.Fatalf("%d fee quotes, budget is %d", fees.calls, sellQuoteMaxEvals)
	}
}

// TestSellQuote_FeeEngineDownIsUnavailable: an unpriceable fee is surfaced as
// such, not as a zero-fee quote.
func TestSellQuote_FeeEngineDownIsUnavailable(t *testing.T) {
	s := newSellQuoteService(t)
	s.SetFeeQuoter(nil)
	if _, err := s.QuoteSellForNet(context.Background(), big.NewInt(50000), ""); !errors.Is(err, ErrITFeeQuoteUnavailable) {
		t.Fatalf("err = %v, want ErrITFeeQuoteUnavailable", err)
	}
}

// TestMcgpForSellGross_RoundsUp: flooring would value the amount one kobo
// short of the gross the search chose.
func TestMcgpForSellGross_RoundsUp(t *testing.T) {
	rate := big.NewInt(2850)
	for _, g := range []int64{1, 100, 10001, 5_000_000, 123_456_789} {
		m := mcgpForSellGross(big.NewInt(g), rate)
		if got := ngnForMcgp(m, rate).Int64(); got != g {
			t.Fatalf("gross %d: mcgp %s values at %d", g, m, got)
		}
	}
}

// countingFees counts fee-engine calls; each can be a live provider request.
type countingFees struct {
	inner ITFeeQuoter
	mu    sync.Mutex
	calls int
}

func (c *countingFees) Quote(ctx context.Context, in FeeQuoteInput) (*FeeQuote, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.Quote(ctx, in)
}

// TestQuoteSellForNet_FewFeeCalls pins the latency budget: under the live fee
// schedule an inverse quote needs a handful of fee evaluations, not dozens.
func TestQuoteSellForNet_FewFeeCalls(t *testing.T) {
	s := newSellQuoteService(t)
	counter := &countingFees{inner: feeQuoterForTest()}
	s.SetFeeQuoter(counter)
	for _, naira := range []int64{100, 1_000, 50_000, 2_500_000} {
		counter.calls = 0
		if _, err := s.QuoteSellForNet(context.Background(), big.NewInt(naira*100), ""); err != nil {
			t.Fatalf("₦%d: %v", naira, err)
		}
		t.Logf("₦%d: %d fee evaluations", naira, counter.calls)
		if counter.calls > 16 {
			t.Fatalf("₦%d took %d fee evaluations", naira, counter.calls)
		}
	}
}
