package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
)

// ─── KeepSurplus: the merchant is paid what they invoiced; the cushion is ours ─
//
// The oracle is Paycrest's own arithmetic, independent of the code under test:
// the fee is inside the deposit, deposit × p/(1 + p) (the first live order's
// 0.0073 USDC fee on 0.742226 at 1% is exactly this), stated to 4 token
// decimals — rounded DOWN, HALF-UP or UP, since which one Paycrest uses is not
// known, and the guarantee has to hold for all three.

type feeRounding int

const (
	roundDown feeRounding = iota
	roundHalfUp
	roundUp
)

func (r feeRounding) String() string { return [...]string{"down", "half-up", "up"}[r] }

// paycrestPaysFor is what Paycrest pays for the last offramp's deposit at
// `rawRate`, with its fee rounded `r`: unrounded naira, no floor — the truth
// the code's own (floored, rounded-up-fee) model must never exceed.
func paycrestPaysFor(t *testing.T, tokenAmount, feePct, rawRate string, r feeRounding) *big.Rat {
	t.Helper()
	tokens, ok1 := new(big.Rat).SetString(tokenAmount)
	p, ok2 := new(big.Rat).SetString(feePct)
	rate, ok3 := new(big.Rat).SetString(rawRate)
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("unreadable %q %q %q", tokenAmount, feePct, rawRate)
	}
	p.Quo(p, big.NewRat(100, 1))
	fee := new(big.Rat).Quo(new(big.Rat).Mul(tokens, p), new(big.Rat).Add(big.NewRat(1, 1), p))
	scaled := new(big.Rat).Mul(fee, big.NewRat(10_000, 1))
	units, rem := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	switch {
	case r == roundUp && rem.Sign() > 0:
		units.Add(units, big.NewInt(1))
	case r == roundHalfUp && new(big.Int).Mul(rem, big.NewInt(2)).Cmp(scaled.Denom()) >= 0:
		units.Add(units, big.NewInt(1))
	}
	fee = new(big.Rat).SetFrac(units, big.NewInt(10_000))
	return new(big.Rat).Mul(new(big.Rat).Sub(tokens, fee), rate)
}

func keepSurplusReq(t *testing.T, r *orderConversionRig, fiat string) PaycrestSellRequest {
	t.Helper()
	req := payoutReq(t, r)
	req.FiatAmount = fiat
	req.KeepSurplus = true
	return req
}

func withRate(r *orderConversionRig, rate string) {
	r.quotes.pc.(*stubQuoter).rates["base|USDC"] = &PaycrestRates{Buy: &PaycrestQuote{Rate: rate}, Sell: &PaycrestQuote{Rate: rate}}
	r.client.offrampRate = rate
}

func naira(s string) *big.Rat { v, _ := new(big.Rat).SetString(s); return v }

// The merchant is paid at least the invoice under every fee rounding, and at
// most the invoice plus the margin plus what rounding and one fee step can add.
func TestCreateSellForFiat_KeepSurplusPaysTheInvoiceWithinTheMarginAtEverySize(t *testing.T) {
	for _, tc := range []struct{ fiat, rate string }{
		{"1000.00", "1362.66"}, // the first live payment
		{"50000.00", "1500"},
		{"5000000.00", "1500"},
	} {
		t.Run(tc.fiat, func(t *testing.T) {
			r := newOrderServiceWithFakes(t)
			withRate(r, tc.rate)
			if _, err := r.svc.CreateSellForFiat(context.Background(), keepSurplusReq(t, r, tc.fiat), false, nil); err != nil {
				t.Fatal(err)
			}
			sent := r.client.lastOfframp
			owed := naira(tc.fiat)
			tokens := naira(sent.AmountToken)
			rate := naira(tc.rate)
			// Slack above the margin: the fee moves in whole 0.0001-token units, so the
			// best step can sit up to one unit short of exact, and a fee rounded down
			// rather than up is up to one more; plus one 0.0001% fee step and the
			// payout's own floor to the kobo.
			slack := new(big.Rat).Mul(big.NewRat(2, 10_000), rate)
			slack.Add(slack, new(big.Rat).Mul(new(big.Rat).Mul(tokens, rate), big.NewRat(1, 1_000_000)))
			slack.Add(slack, big.NewRat(1, 100))
			// And what the fee is chosen to leave for Paycrest's reported fee
			// (paycrestReportedSellFee) over the one it takes: tokens × p²/(1 + p).
			p := new(big.Rat).Quo(naira(sent.SenderFeePercent), big.NewRat(100, 1))
			over := new(big.Rat).Quo(new(big.Rat).Mul(tokens, new(big.Rat).Mul(p, p)), new(big.Rat).Add(big.NewRat(1, 1), p))
			slack.Add(slack, new(big.Rat).Mul(over, rate))
			most := new(big.Rat).Add(new(big.Rat).Add(owed, payMerchantMargin(tokens)), slack)
			for _, rounding := range []feeRounding{roundDown, roundHalfUp, roundUp} {
				paid := paycrestPaysFor(t, sent.AmountToken, sent.SenderFeePercent, tc.rate, rounding)
				if paid.Cmp(owed) < 0 {
					t.Fatalf("fee rounded %s: merchant paid ₦%s, short of ₦%s", rounding, paid.FloatString(2), tc.fiat)
				}
				if paid.Cmp(most) > 0 {
					t.Fatalf("fee rounded %s: merchant paid ₦%s, more than ₦%s — the cushion still went to the merchant", rounding, paid.FloatString(2), most.FloatString(2))
				}
			}
			t.Logf("₦%s at %s: deposit %s, fee %s%%, merchant paid ₦%s–₦%s", tc.fiat, tc.rate, sent.AmountToken, sent.SenderFeePercent,
				paycrestPaysFor(t, sent.AmountToken, sent.SenderFeePercent, tc.rate, roundUp).FloatString(2),
				paycrestPaysFor(t, sent.AmountToken, sent.SenderFeePercent, tc.rate, roundDown).FloatString(2))
		})
	}
}

func TestCreateSellForFiat_WithoutKeepSurplusTheFeeIsTheConfiguredOne(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	if _, err := r.svc.CreateSellForFiat(context.Background(), payoutReq(t, r), false, nil); err != nil {
		t.Fatal(err)
	}
	if got := r.client.lastOfframp.SenderFeePercent; got != "1" {
		t.Fatalf("fee = %q: an ordinary sell must keep the configured fee", got)
	}
}

func TestCreateSellForFiat_KeepSurplusNeverTouchesAConvertedSell(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	if _, err := r.svc.CreateSellForFiat(context.Background(), keepSurplusReq(t, r, "50000.00"), true, nil); err != nil {
		t.Fatal(err)
	}
	if got := r.client.lastOfframp.SenderFeePercent; got != "0.5" {
		t.Fatalf("fee = %q: a converted sell is sent at F − B, whose budget is priced separately", got)
	}
}

// The drift budget grows with the order: a half-kobo tick in the provider's rate
// on a ₦500k order is absorbed; a move past the budget refuses the order rather
// than paying the merchant short.
func TestCreateSellForFiat_KeepSurplusAbsorbsATickButRefusesARealMove(t *testing.T) {
	tick := newOrderServiceWithFakes(t)
	withRate(tick, "1500")
	tick.client.offrampReportsFeeOnDeposit = true
	tick.client.offrampRate = "1499.995"
	if _, err := tick.svc.CreateSellForFiat(context.Background(), keepSurplusReq(t, tick, "500000.00"), false, nil); err != nil {
		t.Fatalf("a half-kobo tick refused a ₦500k order: %v", err)
	}
	moved := newOrderServiceWithFakes(t)
	withRate(moved, "1500")
	moved.client.offrampReportsFeeOnDeposit = true
	moved.client.offrampRate = "1499.95"
	if _, err := moved.svc.CreateSellForFiat(context.Background(), keepSurplusReq(t, moved, "500000.00"), false, nil); !errors.Is(err, ErrSellRateMoved) {
		t.Fatalf("err = %v, want ErrSellRateMoved: a 5-kobo move is past the budget", err)
	}
}

// The check trusts the larger of the fee Paycrest reports and our model: a
// reported fee that eats into the invoice refuses the order.
func TestCreateSellForFiat_KeepSurplusChecksAgainstTheFeePaycrestReports(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	withRate(r, "1500")
	r.client.offrampSenderFee = "0.5" // far more than 1.x% of the deposit
	if _, err := r.svc.CreateSellForFiat(context.Background(), keepSurplusReq(t, r, "1000.00"), false, nil); !errors.Is(err, ErrSellRateMoved) {
		t.Fatalf("err = %v: an order whose reported fee leaves the merchant short must be refused", err)
	}
}

// Paycrest reports deposit × p at creation, more than the deposit × p/(1 + p)
// it takes by deposit × p²/(1 + p) — past the drift margin from about ₦3k. An
// order at the very rate it was quoted must still open: from 2026-10-09 every
// ₦5,000 TSA Pay payment was refused as "the rate moved" with the rate unmoved.
func TestCreateSellForFiat_KeepSurplusClearsTheFeePaycrestReportsAtEverySize(t *testing.T) {
	for _, tc := range []struct{ fiat, rate string }{
		{"1000.00", "1362.66"},
		{"5000.00", "1352.22"}, // the refused live payment
		{"50000.00", "1500"},
		{"5000000.00", "1500"},
	} {
		t.Run(tc.fiat, func(t *testing.T) {
			r := newOrderServiceWithFakes(t)
			withRate(r, tc.rate)
			r.client.offrampReportsFeeOnDeposit = true
			if _, err := r.svc.CreateSellForFiat(context.Background(), keepSurplusReq(t, r, tc.fiat), false, nil); err != nil {
				t.Fatalf("₦%s at an unmoved %s: %v", tc.fiat, tc.rate, err)
			}
		})
	}
}

// A Paycrest that refuses the finely-stepped fee outright (4xx — nothing was
// created) gets the order again at the configured fee: we lose the cushion, not
// the payment. Any other failure (5xx, a lost answer) is NOT retried — an order
// may exist.
func TestCreateSellForFiat_KeepSurplusReopensAtTheConfiguredFeeOnlyWhenPaycrestRefusedIt(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	r.client.offrampErrFirst = &PaycrestRequestError{Op: "offramp", Status: 400, Msg: "senderFeePercent: invalid"}
	if _, err := r.svc.CreateSellForFiat(context.Background(), keepSurplusReq(t, r, "50000.00"), false, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
	if r.client.offrampCalls != 2 || r.client.lastOfframp.SenderFeePercent != "1" {
		t.Fatalf("calls %d, last fee %q: want a second order at the configured 1%%", r.client.offrampCalls, r.client.lastOfframp.SenderFeePercent)
	}

	five := newOrderServiceWithFakes(t)
	five.client.offrampErrFirst = &PaycrestRequestError{Op: "offramp", Status: 502, Msg: "bad gateway"}
	if _, err := five.svc.CreateSellForFiat(context.Background(), keepSurplusReq(t, five, "50000.00"), false, nil); err == nil {
		t.Fatal("a 5xx was swallowed")
	}
	if five.client.offrampCalls != 1 {
		t.Fatalf("calls = %d: a 5xx may have created an order and must not be retried", five.client.offrampCalls)
	}
}

func TestKeepSurplusFeePercent_TakesTheHighestStepThatStillClears(t *testing.T) {
	tokens := naira("33.671134")
	owed := naira("50000")
	got, capped := keepSurplusFeePercent(tokens, "1500", "1", owed)
	steps, ok := feePercentToSteps(got, surplusFeeStepsPerPercent)
	if !ok || capped || steps <= 10_000 {
		t.Fatalf("fee = %q capped=%v", got, capped)
	}
	target := new(big.Rat).Add(owed, payMerchantMargin(tokens))
	clears := func(s int) bool {
		f, _ := paycrestReportedSellFee(tokens, stepsToFeePercent(s, surplusFeeStepsPerPercent))
		return paycrestSellPayout(tokens, f, naira("1500")).Cmp(target) >= 0
	}
	if !clears(steps) || clears(steps+1) {
		t.Fatalf("fee %s%% is not the boundary: clears=%v, next clears=%v", got, clears(steps), clears(steps+1))
	}
}

func TestKeepSurplusFeePercent_KeepsTheConfiguredFeeWithNoCushionOrNoRate(t *testing.T) {
	tokens := new(big.Rat).SetFrac64(50000_00*101, 1500_00*100) // exactly ₦50,000 net of a 1% fee inside the deposit
	if got, _ := keepSurplusFeePercent(tokens, "1500", "1", naira("50000")); got != "1" {
		t.Fatalf("fee = %q, want the configured 1 with no cushion to keep", got)
	}
	if got, _ := keepSurplusFeePercent(naira("40"), "", "1", naira("50000")); got != "1" {
		t.Fatalf("fee = %q, want the configured 1 with no rate", got)
	}
}

// A deposit far larger than the invoice needs stops at the configured fee plus
// 0.5%, and says so — the excess is left to the merchant, never silently kept.
func TestKeepSurplusFeePercent_IsCappedAtHalfAPercentAboveTheConfiguredFee(t *testing.T) {
	got, capped := keepSurplusFeePercent(naira("67"), "1500", "1", naira("50000"))
	if got != "1.5" || !capped {
		t.Fatalf("fee = %q capped=%v, want 1.5 and capped", got, capped)
	}
}

func TestFeeSteps_RoundTripEveryStepAPaycrestFeeCanTake(t *testing.T) {
	for s := 1; s <= maxSenderFeePercent*surplusFeeStepsPerPercent; s++ {
		pct := stepsToFeePercent(s, surplusFeeStepsPerPercent)
		back, ok := feePercentToSteps(pct, surplusFeeStepsPerPercent)
		if !ok || back != s {
			t.Fatalf("step %d → %q → %d (ok %v)", s, pct, back, ok)
		}
		if len(pct) > 16 {
			t.Fatalf("step %d → %q overflows the varchar(16) column", s, pct)
		}
	}
}

func TestPaycrestRejected_OnlyARefusalIsSafeToResend(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&PaycrestRequestError{Status: 400}, true},
		{&PaycrestRequestError{Status: 422}, true},
		{fmt.Errorf("opening: %w", &PaycrestRequestError{Status: 422}), true},
		{&PaycrestRequestError{Status: 409}, false}, // may mean the order already exists
		{&PaycrestRequestError{Status: 401}, false},
		{&PaycrestRequestError{Status: 500}, false},
		{errors.New("paycrest offramp: context deadline exceeded"), false},
	} {
		if got := paycrestRejected(tc.err); got != tc.want {
			t.Errorf("paycrestRejected(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
	if got := (&PaycrestRequestError{Op: "offramp", Status: 400, Msg: "bad"}).Error(); got != "paycrest offramp: HTTP 400: bad" {
		t.Fatalf("text = %q: callers have always shown this shape", got)
	}
}

// Every rounding in the model goes against the merchant. On the first live
// order Paycrest took 0.0073 of 0.742226 at 1% (0.00734877… rounded down); the
// model must say 0.0074, so it can never promise the merchant more than they get.
func TestPaycrestSellFee_RoundsUpAgainstTheMerchant(t *testing.T) {
	fee, ok := paycrestSellFee(naira("0.742226"), "1")
	if !ok || fee.FloatString(4) != "0.0074" {
		t.Fatalf("fee = %v, want 0.0074 (0.00734877… rounded UP)", fee)
	}
	exact, _ := paycrestSellFee(naira("1.01"), "1") // 1.01 × 0.01/1.01 = 0.01 exactly
	if exact.FloatString(4) != "0.0100" {
		t.Fatalf("an exact fee was rounded: %s", exact.FloatString(4))
	}
	if _, ok := paycrestSellFee(naira("1"), "-1"); ok {
		t.Fatal("a negative percent was accepted")
	}
}

func TestPaycrestSellPayout_FloorsToTheKoboAgainstTheMerchant(t *testing.T) {
	// (0.742226 − 0.0074) × 1362.66 = 1001.3179… → ₦1,001.31, not the ₦1,001.32
	// rounding to nearest would promise.
	if got := paycrestSellPayout(naira("0.742226"), naira("0.0074"), naira("1362.66")).FloatString(2); got != "1001.31" {
		t.Fatalf("payout = %s, want 1001.31", got)
	}
	if got := paycrestSellPayout(naira("0.5"), naira("0.6"), naira("1500")); got.Sign() != 0 {
		t.Fatalf("a fee larger than the deposit paid ₦%s", got.FloatString(2))
	}
}

// Paycrest's raw rate is stored after Paycrest opened the order, so it must
// always fit its column: a refused insert would leave a live deposit address we
// hold no row for. A long rate is cut DOWN (a stricter check, never a looser one).
func TestStorableRawRate_AlwaysFitsAndNeverRoundsUp(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1362.66", "1362.66"},
		{" 1500 ", "1500"},
		{"1362.66999999999999999999999999999", "1362.66999999"}, // cut down, not rounded up to 1362.67
		{"1e3", ""}, {"-1500", ""}, {"NaN", ""}, {"", ""}, {"abc", ""},
		{"123456789012345678901234567890123.5", ""}, // too big to fit even at 8 decimals
	} {
		got := storableRawRate(tc.in)
		if got != tc.want {
			t.Errorf("storableRawRate(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if len(got) > rawRateColumnLen {
			t.Errorf("storableRawRate(%q) = %q overflows the column", tc.in, got)
		}
	}
}

func TestCreateSellForFiat_AnOverlongPaycrestRateIsStoredCutDownAndStillChecked(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	withRate(r, "1500")
	r.client.offrampRate = "1500.00000000000000000000000000000001"
	order, err := r.svc.CreateSellForFiat(context.Background(), keepSurplusReq(t, r, "50000.00"), false, nil)
	if err != nil {
		t.Fatalf("an overlong rate broke the order: %v", err)
	}
	if order.RawRate != "1500.00000000" {
		t.Fatalf("raw rate stored as %q", order.RawRate)
	}
}
