package services

import (
	"context"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// slowDeadProvider stands in for a Flutterwave GetFees that never answers:
// every call burns `delay` (the real quoter's per-call timeout is 5s) and then
// reports "no live quote", so the engine falls back to the configured
// percentage.
type slowDeadProvider struct {
	delay time.Duration
	calls atomic.Int64
}

func (p *slowDeadProvider) QuoteProviderFee(ctx context.Context, _ FeeFlow, _ *big.Int, _ string) (*big.Int, bool) {
	p.calls.Add(1)
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
	}
	return nil, false
}

// liveBandedProvider answers every call with an amount-dependent fee — a flat
// fee per amount band, the shape of a bank-transfer tariff — and records how
// often each amount was asked for.
type liveBandedProvider struct {
	mu    sync.Mutex
	asked map[string]int
}

func (p *liveBandedProvider) QuoteProviderFee(_ context.Context, _ FeeFlow, amount *big.Int, _ string) (*big.Int, bool) {
	p.mu.Lock()
	if p.asked == nil {
		p.asked = map[string]int{}
	}
	p.asked[amount.String()]++
	p.mu.Unlock()
	switch {
	case amount.Cmp(big.NewInt(500_000)) <= 0: // ≤ ₦5,000
		return big.NewInt(1075), true
	case amount.Cmp(big.NewInt(5_000_000)) <= 0: // ≤ ₦50,000
		return big.NewInt(2688), true
	default:
		return big.NewInt(5375), true
	}
}

// TestQuoteSellForNet_DeadProviderCostsOneLookup is the latency bound. An
// inverse quote runs a dozen fee evaluations; if every one of them waited out
// a dead provider, one quote would take ~7-10 sequential timeouts (35-50s at
// 5s each). Once the provider has failed inside a quote, the rest of that
// quote uses the fallback it would have got anyway.
func TestQuoteSellForNet_DeadProviderCostsOneLookup(t *testing.T) {
	const delay = 100 * time.Millisecond
	for _, naira := range []int64{100, 50_000, 2_500_000} {
		s := newSellQuoteService(t)
		p := &slowDeadProvider{delay: delay}
		s.SetFeeQuoter(NewFeeQuoteService(liveFeeQuoteConfig(), p))
		start := time.Now()
		price, err := s.QuoteSellForNet(context.Background(), big.NewInt(naira*100), "")
		if err != nil {
			t.Fatalf("₦%d: %v", naira, err)
		}
		el := time.Since(start)
		t.Logf("₦%d: %d provider calls, %v", naira, p.calls.Load(), el)
		if got := p.calls.Load(); got != 1 {
			t.Fatalf("₦%d: %d provider lookups against a dead provider, want 1", naira, got)
		}
		if el > 3*delay {
			t.Fatalf("₦%d: quote took %v against a %v provider timeout", naira, el, delay)
		}
		if price.Fees.Lines.ProviderFee.Source != "fallback" {
			t.Fatalf("₦%d: provider source %q, want fallback", naira, price.Fees.Lines.ProviderFee.Source)
		}
		// The figures are the ones an unmemoized pricing (the submit path)
		// produces when the provider is down.
		want, err := s.QuoteSell(context.Background(), price.McgpWei, "")
		if err != nil {
			t.Fatalf("₦%d reprice: %v", naira, err)
		}
		if want.Fees.Net.Cmp(price.Fees.Net) != 0 || want.Fees.Fee.Cmp(price.Fees.Fee) != 0 {
			t.Fatalf("₦%d: memoized quote net/fee %s/%s, submit pricing %s/%s",
				naira, price.Fees.Net, price.Fees.Fee, want.Fees.Net, want.Fees.Fee)
		}
	}
}

// TestQuoteSellForNet_LiveProviderAskedOncePerAmount: with a live,
// amount-dependent provider the memo is keyed by amount, so no amount is
// fetched twice (the final re-price reuses the search's lookup) and every
// figure equals what the unmemoized submit pricing computes for the same MCGP.
func TestQuoteSellForNet_LiveProviderAskedOncePerAmount(t *testing.T) {
	for _, naira := range []int64{100, 4_990, 50_000, 2_500_000} {
		s := newSellQuoteService(t)
		p := &liveBandedProvider{}
		s.SetFeeQuoter(NewFeeQuoteService(liveFeeQuoteConfig(), p))
		target := big.NewInt(naira * 100)
		price, err := s.QuoteSellForNet(context.Background(), target, "")
		if err != nil {
			t.Fatalf("₦%d: %v", naira, err)
		}
		for amt, n := range p.asked {
			if n > 1 {
				t.Fatalf("₦%d: provider asked %d times for %s kobo", naira, n, amt)
			}
		}
		if price.Fees.Net.Cmp(target) < 0 {
			t.Fatalf("₦%d: net %s below request", naira, price.Fees.Net)
		}
		if price.Fees.Lines.ProviderFee.Source != "live" {
			t.Fatalf("₦%d: provider source %q, want live", naira, price.Fees.Lines.ProviderFee.Source)
		}
		want, err := s.QuoteSell(context.Background(), price.McgpWei, "")
		if err != nil {
			t.Fatalf("₦%d reprice: %v", naira, err)
		}
		if want.Fees.Net.Cmp(price.Fees.Net) != 0 || want.Fees.Fee.Cmp(price.Fees.Fee) != 0 {
			t.Fatalf("₦%d: memoized quote net/fee %s/%s, submit pricing %s/%s",
				naira, price.Fees.Net, price.Fees.Fee, want.Fees.Net, want.Fees.Fee)
		}
	}
}

// TestProviderFeeMemo_ScopedToOneQuote: the memo lives for one inverse quote.
// A failure seen by one quote must not make the next quote — or the sell
// itself, which prices through QuoteSell — skip the provider.
func TestProviderFeeMemo_ScopedToOneQuote(t *testing.T) {
	s := newSellQuoteService(t)
	p := &slowDeadProvider{delay: time.Millisecond}
	s.SetFeeQuoter(NewFeeQuoteService(liveFeeQuoteConfig(), p))
	ctx := context.Background()

	if _, err := s.QuoteSellForNet(ctx, big.NewInt(5_000_000), ""); err != nil {
		t.Fatalf("first quote: %v", err)
	}
	after1 := p.calls.Load()
	if _, err := s.QuoteSellForNet(ctx, big.NewInt(5_000_000), ""); err != nil {
		t.Fatalf("second quote: %v", err)
	}
	if p.calls.Load() == after1 {
		t.Fatal("second quote reused the first quote's provider failure")
	}
	after2 := p.calls.Load()
	if _, err := s.QuoteSell(ctx, new(big.Int).Mul(big.NewInt(100), oneE18), ""); err != nil {
		t.Fatalf("forward quote: %v", err)
	}
	if p.calls.Load() == after2 {
		t.Fatal("sell pricing skipped the provider after an earlier quote's failure")
	}
}

// TestQuoteSellForNet_BandedProviderNeverUnderpays sweeps targets across a
// provider fee band boundary (₦5,000 gross), where the net payout drops by the
// band step. The search may then land above a cheaper gross just below the
// boundary, but the quote must never promise less than was asked for.
func TestQuoteSellForNet_BandedProviderNeverUnderpays(t *testing.T) {
	s := newSellQuoteService(t)
	p := &liveBandedProvider{}
	s.SetFeeQuoter(NewFeeQuoteService(liveFeeQuoteConfig(), p))
	for target := int64(480_000); target <= 500_000; target += 250 {
		price, err := s.QuoteSellForNet(context.Background(), big.NewInt(target), "")
		if err != nil {
			t.Fatalf("target %d: %v", target, err)
		}
		if price.Fees.Net.Cmp(big.NewInt(target)) < 0 {
			t.Fatalf("target %d: net %s below request", target, price.Fees.Net)
		}
	}
}
