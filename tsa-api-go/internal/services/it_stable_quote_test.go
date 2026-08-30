package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"
)

type stubStableRateQuoter struct {
	rate FWRate
	err  error
	got  struct{ src, dst string }
}

func (s *stubStableRateQuoter) QuoteRate(_ context.Context, src, dst string, _ float64) (FWRate, error) {
	s.got.src, s.got.dst = src, dst
	return s.rate, s.err
}

// newStableSellTestService builds a quoting-only ITService (no DB).
//
// Deliberately wires NO FxProvider: checkRatePlausible is a no-op without one,
// so the arithmetic tests below exercise the spread maths in isolation. The
// plausibility tests wire a stubFx explicitly.
func newStableSellTestService(t *testing.T, q *stubStableRateQuoter) *ITService {
	t.Helper()
	s := &ITService{}
	s.SetRateQuoter(q)
	s.SetStableSpreadBps(155)
	s.SetStableRateLockTTL(15 * time.Minute)
	return s
}

func TestQuoteStableSell_AppliesTheSpreadToTheFlutterwaveRate(t *testing.T) {
	// FW: 1 USDC buys ₦1,650.25. Selling 100 USDC grosses ₦165,025 =
	// 16,502,500 kobo. Less 155bps → 16,246,711 kobo (floored).
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1650.25},
	}}
	s := newStableSellTestService(t, q)

	got, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN")
	if err != nil {
		t.Fatalf("QuoteStableSell: %v", err)
	}
	if got.GrossMinor.String() != "16502500" {
		t.Errorf("gross = %s, want 16502500", got.GrossMinor)
	}
	if got.NetMinor.String() != "16246711" {
		t.Errorf("net = %s, want 16246711", got.NetMinor)
	}
	if got.AssetAmount.String() != "100000000" {
		t.Errorf("assetAmount = %s, want 100000000 base units", got.AssetAmount)
	}
}

func TestQuoteStableSell_QuotesTheAssetAsSourceCurrency(t *testing.T) {
	// The probe must ask "USDC → NGN", not the reverse; asking backwards
	// inverts the rate and misprices by orders of magnitude.
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1650.25},
	}}
	s := newStableSellTestService(t, q)

	if _, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN"); err != nil {
		t.Fatalf("QuoteStableSell: %v", err)
	}
	if q.got.src != "USDC" || q.got.dst != "NGN" {
		t.Fatalf("probed %s→%s, want USDC→NGN", q.got.src, q.got.dst)
	}
}

func TestQuoteStableSell_SetsAnExpiryFromTheLockTTL(t *testing.T) {
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1650.25},
	}}
	s := newStableSellTestService(t, q)

	before := time.Now()
	got, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN")
	if err != nil {
		t.Fatalf("QuoteStableSell: %v", err)
	}
	if got.ExpiresAt.Before(before.Add(14*time.Minute)) || got.ExpiresAt.After(before.Add(16*time.Minute)) {
		t.Fatalf("ExpiresAt = %v, want ~15 minutes out", got.ExpiresAt)
	}
}

func TestQuoteStableSell_RejectsNonStableAssets(t *testing.T) {
	s := newStableSellTestService(t, &stubStableRateQuoter{})
	if _, err := s.QuoteStableSell(context.Background(), "MCGP", "100", "NGN"); err == nil {
		t.Fatal("MCGP accepted by the stablecoin quoter")
	}
}

func TestQuoteStableSell_RejectsOverPreciseAmounts(t *testing.T) {
	s := newStableSellTestService(t, &stubStableRateQuoter{})
	if _, err := s.QuoteStableSell(context.Background(), "USDC", "1.1234567", "NGN"); err == nil {
		t.Fatal("over-precise amount accepted")
	}
}

func TestQuoteStableSell_FailsClosedWithoutARateQuoter(t *testing.T) {
	// No live rate means no quote. Guessing one would price real money against
	// a number nobody stands behind.
	s := &ITService{}
	s.SetStableSpreadBps(155)
	if _, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN"); err == nil {
		t.Fatal("quoted without a rate source")
	}
}

func TestQuoteStableSell_RefusesAnImplausibleProviderRate(t *testing.T) {
	// Not hypothetical: the Flutterwave sandbox returned 1,406,790 USDC as the
	// cost of 1,000 NGN — internally self-consistent, six orders of magnitude
	// wrong. Quoting that to a user would be catastrophic, so we quote nothing.
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1406790.278235},
		Destination: FWMoney{Currency: "NGN", Amount: 1000},
	}}
	s := newStableSellTestService(t, q)
	s.fx = stubFx{ngn: 1500} // independent feed: ~₦1500/USD (same package: set directly)

	_, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN")
	if !errors.Is(err, ErrStableRateImplausible) {
		t.Fatalf("err = %v, want ErrStableRateImplausible", err)
	}
}

func TestQuoteStableSell_AcceptsARateNearTheIndependentFeed(t *testing.T) {
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1500},
	}}
	s := newStableSellTestService(t, q)
	s.fx = stubFx{ngn: 1500}

	if _, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN"); err != nil {
		t.Fatalf("a rate matching the independent feed was refused: %v", err)
	}
}

func TestQuoteStableSell_SkipsThePlausibilityCheckWhenTheFeedIsStale(t *testing.T) {
	// The guard must not become a second outage surface: if the independent
	// feed is stale, quoting proceeds on the provider rate rather than
	// treating a stale number as ground truth. The provider rate here
	// (₦5,000/USDC) sits well outside ±25% of the stubbed ₦1,500/USD feed —
	// this is diagnostic: if staleness were not honoured, the rate would be
	// (wrongly) rejected as implausible. See the non-stale counterpart below,
	// which asserts the identical rate IS rejected once the feed is fresh.
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 5000},
	}}
	s := newStableSellTestService(t, q)
	s.fx = stubFx{ngn: 1500, stale: true}

	if _, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN"); err != nil {
		t.Fatalf("stale independent feed blocked a quote: %v", err)
	}
}

func TestQuoteStableSell_RejectsTheSameRateWhenTheFeedIsFresh(t *testing.T) {
	// Brackets the test above: the identical implausible rate, against the
	// SAME feed value but fresh, must be rejected — proving staleness (not
	// some other difference) is what changed the outcome there.
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 5000},
	}}
	s := newStableSellTestService(t, q)
	s.fx = stubFx{ngn: 1500}

	_, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN")
	if !errors.Is(err, ErrStableRateImplausible) {
		t.Fatalf("err = %v, want ErrStableRateImplausible", err)
	}
}

func TestQuoteStableSell_PropagatesProviderFailure(t *testing.T) {
	q := &stubStableRateQuoter{err: context.DeadlineExceeded}
	s := newStableSellTestService(t, q)
	if _, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN"); err == nil {
		t.Fatal("provider failure swallowed")
	}
}

func TestQuoteStableSell_NeverExceedsGross(t *testing.T) {
	// Property: the user can never be quoted more than the trade is worth.
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1650.25},
	}}
	s := newStableSellTestService(t, q)
	// The property under test is the ARITHMETIC, which must hold at every
	// magnitude — including dust below the provider floor. Drop the floor so
	// the sweep still reaches those inputs; the floor itself is covered by
	// TestQuoteStableSell_EnforcesProviderMinimum.
	if err := s.SetStableMinSell("0.000001"); err != nil {
		t.Fatalf("SetStableMinSell: %v", err)
	}
	for _, amt := range []string{"0.000001", "1", "10.5", "100", "999999.999999"} {
		got, err := s.QuoteStableSell(context.Background(), "USDC", amt, "NGN")
		if err != nil {
			t.Fatalf("%s: %v", amt, err)
		}
		if got.NetMinor.Cmp(got.GrossMinor) > 0 {
			t.Fatalf("%s: net %s exceeds gross %s", amt, got.NetMinor, got.GrossMinor)
		}
		if got.NetMinor.Sign() < 0 {
			t.Fatalf("%s: negative net %s", amt, got.NetMinor)
		}
	}
}

type stubFeeQuoter struct {
	quote *FeeQuote
	err   error
	got   FeeQuoteInput
}

func (s *stubFeeQuoter) Quote(_ context.Context, in FeeQuoteInput) (*FeeQuote, error) {
	s.got = in
	return s.quote, s.err
}

func TestQuoteStableSell_DeductsTheItemisedFeeFromTheSpreadAmount(t *testing.T) {
	// The spread is the company margin; the platform fee, provider
	// pass-through and tax are separate lines charged on top of it.
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1650.25},
	}}
	s := newStableSellTestService(t, q)
	s.SetFeeQuoter(&stubFeeQuoter{quote: &FeeQuote{
		TotalFees: FeeLine{Minor: big.NewInt(50000)}, // ₦500
		Viable:    true,
	}})

	got, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN")
	if err != nil {
		t.Fatalf("QuoteStableSell: %v", err)
	}
	if got.SpreadMinor.String() != "16246711" {
		t.Errorf("spread = %s, want 16246711", got.SpreadMinor)
	}
	if got.FeeMinor.String() != "50000" {
		t.Errorf("fee = %s, want 50000", got.FeeMinor)
	}
	if got.NetMinor.String() != "16196711" {
		t.Errorf("net = %s, want 16196711 (spread less fee)", got.NetMinor)
	}
}

func TestQuoteStableSell_AsksTheFeeEngineForAStablecoinWithdrawal(t *testing.T) {
	// A wrong flow or asset here would price the trade on MCGP's tiers.
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1650.25},
	}}
	s := newStableSellTestService(t, q)
	fq := &stubFeeQuoter{quote: &FeeQuote{TotalFees: FeeLine{Minor: big.NewInt(0)}, Viable: true}}
	s.SetFeeQuoter(fq)

	if _, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN"); err != nil {
		t.Fatalf("QuoteStableSell: %v", err)
	}
	// Amount must be the MAJOR-unit decimal string ("162467.11"), not
	// SpreadMinor's raw minor-unit string ("16246711") — the latter is the
	// 100x denomination bug applyStableSellFees exists to avoid; this
	// assertion makes a regression fail the test instead of only being
	// caught by reading the code.
	if fq.got.Amount != "162467.11" {
		t.Errorf("amount = %q, want \"162467.11\"", fq.got.Amount)
	}
	if fq.got.Flow != FeeFlowWithdraw {
		t.Errorf("flow = %v, want withdraw", fq.got.Flow)
	}
	if fq.got.Asset != FeeAssetUSDC {
		t.Errorf("asset = %v, want USDC", fq.got.Asset)
	}
	if fq.got.Currency != "NGN" {
		t.Errorf("currency = %q, want NGN", fq.got.Currency)
	}
}

func TestQuoteStableSell_RefusesWhenTheFeeIsAtLeastTheProceeds(t *testing.T) {
	// A fee that swallows the trade must fail loudly, never quote zero or a
	// negative payout.
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1650.25},
	}}
	s := newStableSellTestService(t, q)
	s.SetFeeQuoter(&stubFeeQuoter{quote: &FeeQuote{
		TotalFees: FeeLine{Minor: big.NewInt(99999999)},
		Viable:    false, // the engine already flags fee-exceeds-amount
	}})

	if _, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN"); err == nil {
		t.Fatal("quoted a trade whose fee exceeds the proceeds")
	}
}

func TestQuoteStableSell_WithoutAFeeQuoterChargesNoFeeLine(t *testing.T) {
	// Dev environments boot without the fee engine; the quote degrades to
	// spread-only rather than refusing to price.
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1650.25},
	}}
	got, err := newStableSellTestService(t, q).QuoteStableSell(context.Background(), "USDC", "100", "NGN")
	if err != nil {
		t.Fatalf("QuoteStableSell: %v", err)
	}
	if got.FeeMinor.Sign() != 0 || got.NetMinor.Cmp(got.SpreadMinor) != 0 {
		t.Fatalf("fee=%s net=%s spread=%s, want no fee line", got.FeeMinor, got.NetMinor, got.SpreadMinor)
	}
}

func TestQuoteStableSell_EnforcesProviderMinimum(t *testing.T) {
	// Below Flutterwave's floor the conversion is rejected AFTER the user has
	// irreversibly sent funds, so the quote is the last point where refusing
	// costs nothing. The app enforces the same number, but a client-side check
	// does not protect a direct API caller — this is the real gate.
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1650.25},
	}}

	for _, tc := range []struct {
		name    string
		amount  string
		wantErr bool
	}{
		{"dust", "0.000001", true},
		{"just under the floor", "9.999999", true},
		{"exactly the floor", "10", false},
		{"above the floor", "10.000001", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStableSellTestService(t, q)
			_, err := s.QuoteStableSell(context.Background(), "USDC", tc.amount, "NGN")
			if tc.wantErr {
				if !errors.Is(err, ErrStableBelowMinimum) {
					t.Fatalf("err = %v, want ErrStableBelowMinimum", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestStableMinSell_DefaultsWhenUnset(t *testing.T) {
	// A floor that vanishes when config is absent is the failure this guards:
	// the ramp must not boot without one.
	s := &ITService{}
	want, err := StableToBaseUnits(DefaultStableMinSellMajor)
	if err != nil {
		t.Fatalf("default constant is unparseable: %v", err)
	}
	if got := s.stableMinSell(); got.Cmp(want) != 0 {
		t.Fatalf("unconfigured floor = %s, want %s", got, want)
	}
}

func TestSetStableMinSell(t *testing.T) {
	def, err := StableToBaseUnits(DefaultStableMinSellMajor)
	if err != nil {
		t.Fatalf("default constant is unparseable: %v", err)
	}

	t.Run("empty keeps the default", func(t *testing.T) {
		s := &ITService{}
		if err := s.SetStableMinSell("  "); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := s.stableMinSell(); got.Cmp(def) != 0 {
			t.Fatalf("floor = %s, want the %s default", got, def)
		}
	})

	t.Run("garbage errors and does NOT disable the floor", func(t *testing.T) {
		s := &ITService{}
		if err := s.SetStableMinSell("not-a-number"); err == nil {
			t.Fatal("malformed minimum accepted")
		}
		if got := s.stableMinSell(); got.Cmp(def) != 0 {
			t.Fatalf("floor = %s after a bad value, want the %s default", got, def)
		}
	})

	t.Run("negative rejected", func(t *testing.T) {
		s := &ITService{}
		if err := s.SetStableMinSell("-5"); err == nil {
			t.Fatal("negative minimum accepted")
		}
	})

	t.Run("valid value applies", func(t *testing.T) {
		s := &ITService{}
		if err := s.SetStableMinSell("25.5"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want, _ := StableToBaseUnits("25.5")
		if got := s.stableMinSell(); got.Cmp(want) != 0 {
			t.Fatalf("floor = %s, want %s", got, want)
		}
	})
}

// TestSetStableSpreadBps guards against the fix in this round: a rejected or
// unset spread must never collapse to 0, because ApplySellSpread/
// ApplyBuySpread both treat spreadBps<=0 as "pass the gross amount through
// unchanged" — a zero spread here means every stablecoin trade runs at zero
// company margin. Mirrors TestSetStableMinSell's shape.
func TestSetStableSpreadBps(t *testing.T) {
	t.Run("rejected >=10000 leaves the default in force, not zero", func(t *testing.T) {
		s := &ITService{}
		if err := s.SetStableSpreadBps(10000); err == nil {
			t.Fatal("spread of 10000bps accepted")
		}
		if got := s.stableSpread(); got != DefaultStableSpreadBps {
			t.Fatalf("effective spread = %d after a rejected value, want default %d", got, DefaultStableSpreadBps)
		}
	})

	t.Run("unset (zero value) is never in force", func(t *testing.T) {
		s := &ITService{}
		if err := s.SetStableSpreadBps(0); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := s.stableSpread(); got != DefaultStableSpreadBps {
			t.Fatalf("effective spread = %d, want default %d — a zero spread must never be in force", got, DefaultStableSpreadBps)
		}
	})

	t.Run("valid value applies", func(t *testing.T) {
		s := &ITService{}
		if err := s.SetStableSpreadBps(200); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := s.stableSpread(); got != 200 {
			t.Fatalf("effective spread = %d, want 200", got)
		}
	})
}
