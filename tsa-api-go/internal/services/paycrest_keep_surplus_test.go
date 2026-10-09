package services

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// ─── KeepSurplus: the merchant is paid what they invoiced; our fee absorbs ──
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

// payerDeposit is what a KeepSurplus customer is shown: the invoice plus the
// configured fee, owed / (rate × (1 − p)), rounded up to 6 token decimals.
func payerDeposit(owed, rate, pct string) string {
	p := new(big.Rat).Quo(naira(pct), big.NewRat(100, 1))
	net := new(big.Rat).Mul(naira(rate), new(big.Rat).Sub(big.NewRat(1, 1), p))
	d := new(big.Rat).Mul(new(big.Rat).Quo(naira(owed), net), big.NewRat(1_000_000, 1))
	units, rem := new(big.Int).QuoRem(d.Num(), d.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		units.Add(units, big.NewInt(1))
	}
	return BaseUnitsToDecimal(units, 6)
}

// feeGivenUp is the naira our fee gave up on `tokens`: what Paycrest takes at
// the configured percent less what it takes at the one sent, at `rate`.
func feeGivenUp(t *testing.T, tokens *big.Rat, sentPct, basePct string, rate *big.Rat) *big.Rat {
	t.Helper()
	sent, ok1 := paycrestSellFee(tokens, sentPct)
	base, ok2 := paycrestSellFee(tokens, basePct)
	if !ok1 || !ok2 {
		t.Fatalf("unreadable fee %q / %q", sentPct, basePct)
	}
	return new(big.Rat).Mul(new(big.Rat).Sub(base, sent), rate)
}

// assertMerchantPaid fails unless the last offramp pays `owed` at `rate`
// under every way Paycrest might round its fee.
func assertMerchantPaid(t *testing.T, r *orderConversionRig, owed, rate string) {
	t.Helper()
	sent := r.client.lastOfframp
	for _, rounding := range []feeRounding{roundDown, roundHalfUp, roundUp} {
		if paid := paycrestPaysFor(t, sent.AmountToken, sent.SenderFeePercent, rate, rounding); paid.Cmp(naira(owed)) < 0 {
			t.Fatalf("fee rounded %s: merchant paid ₦%s, short of ₦%s", rounding, paid.FloatString(2), owed)
		}
	}
}

// payOrderStatuses counts the user's orders by status.
func payOrderStatuses(t *testing.T, r *orderConversionRig, req PaycrestSellRequest) map[string]int {
	t.Helper()
	var rows []models.PaycrestOrderRecord
	if err := r.db.Where("user_id = ?", req.UserID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, row := range rows {
		out[row.Status]++
	}
	return out
}

// The customer funds what she was shown — the invoice plus the fee at the
// venue's rate — and nothing on top. Rounding and drift come out of our fee.
func TestCreateSellForFiat_KeepSurplusDepositIsTheInvoicePlusTheFee(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	withRate(r, "1352.22")
	r.client.offrampReportsFeeOnDeposit = true
	if _, err := r.svc.CreateSellForFiat(context.Background(), keepSurplusReq(t, r, "5000.00"), false, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := r.client.lastOfframp.AmountToken, payerDeposit("5000.00", "1352.22", "1"); got != want {
		t.Fatalf("deposit = %s, want %s: the invoice plus the 1%% fee, no cushion", got, want)
	}
}

// At an unmoved rate the order opens first time, with Paycrest reporting its
// fee the way it does live (deposit × p): the merchant is paid at least the
// invoice under every fee rounding, and our fee gives up no more than the
// window, at every size. From 2026-10-09 the ₦5,000 here was refused as "the
// rate moved" with the rate unmoved.
func TestCreateSellForFiat_KeepSurplusPaysTheInvoiceAtEverySizeWithinTheWindow(t *testing.T) {
	for _, tc := range []struct{ fiat, rate string }{
		{"1000.00", "1362.66"}, // the first live payment
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
			if r.client.offrampCalls != 1 {
				t.Fatalf("%d orders opened at an unmoved rate, want 1", r.client.offrampCalls)
			}
			assertMerchantPaid(t, r, tc.fiat, tc.rate)
			sent := r.client.lastOfframp
			gave := feeGivenUp(t, naira(sent.AmountToken), sent.SenderFeePercent, "1", naira(tc.rate))
			if gave.Cmp(payFeeAbsorbLimit) > 0 {
				t.Fatalf("fee %s%% gave up ₦%s, past the ₦%s window", sent.SenderFeePercent, gave.FloatString(2), payFeeAbsorbLimit.FloatString(2))
			}
			t.Logf("₦%s at %s: deposit %s, fee %s%%, fee given up ₦%s", tc.fiat, tc.rate, sent.AmountToken, sent.SenderFeePercent, gave.FloatString(2))
		})
	}
}

// Paycrest filling a little under the quote is paid for out of our fee: the
// short order is cancelled and reopened once, at a fee priced at the rate
// Paycrest filled at, and the merchant is still paid in full.
func TestCreateSellForFiat_KeepSurplusFeeAbsorbsAMoveInsideTheWindow(t *testing.T) {
	for _, tc := range []struct{ fiat, quoted, filled string }{
		{"5000.00", "1352.22", "1351.28"}, // ≈ ₦3.50 of fee
		{"500000.00", "1500", "1499.995"}, // ≈ ₦1.67 of fee
	} {
		t.Run(tc.fiat, func(t *testing.T) {
			r := newOrderServiceWithFakes(t)
			withRate(r, tc.quoted)
			r.client.offrampReportsFeeOnDeposit = true
			r.client.offrampRate = tc.filled
			req := keepSurplusReq(t, r, tc.fiat)
			if _, err := r.svc.CreateSellForFiat(context.Background(), req, false, nil); err != nil {
				t.Fatalf("a move inside the window was refused: %v", err)
			}
			fees := r.client.offrampFeesSent
			if len(fees) != 2 || naira(fees[1]).Cmp(naira(fees[0])) >= 0 {
				t.Fatalf("fees sent %v: want one reopen at a lower fee", fees)
			}
			assertMerchantPaid(t, r, tc.fiat, tc.filled)
			gave := feeGivenUp(t, naira(r.client.lastOfframp.AmountToken), fees[1], "1", naira(tc.filled))
			if gave.Cmp(payFeeAbsorbLimit) > 0 {
				t.Fatalf("fee gave up ₦%s, past the window", gave.FloatString(2))
			}
			want := map[string]int{models.PaycrestStatusCancelled: 1, models.PaycrestStatusAwaitingDeposit: 1}
			if got := payOrderStatuses(t, r, req); !maps.Equal(got, want) {
				t.Fatalf("orders %v: want the short one cancelled and the reopened one live", got)
			}
		})
	}
}

// A move bigger than the window is not paid for out of our fee: the order is
// cancelled and refused, never reopened.
func TestCreateSellForFiat_KeepSurplusRefusesAMovePastTheWindow(t *testing.T) {
	for _, tc := range []struct{ fiat, quoted, filled string }{
		{"5000.00", "1352.22", "1350"},   // ≈ ₦8.30 of fee
		{"500000.00", "1500", "1499.95"}, // ≈ ₦16.70 of fee
	} {
		t.Run(tc.fiat, func(t *testing.T) {
			r := newOrderServiceWithFakes(t)
			withRate(r, tc.quoted)
			r.client.offrampReportsFeeOnDeposit = true
			r.client.offrampRate = tc.filled
			req := keepSurplusReq(t, r, tc.fiat)
			if _, err := r.svc.CreateSellForFiat(context.Background(), req, false, nil); !errors.Is(err, ErrSellRateMoved) {
				t.Fatalf("err = %v, want ErrSellRateMoved", err)
			}
			if r.client.offrampCalls != 1 {
				t.Fatalf("%d orders opened: a move past the window must not be reopened", r.client.offrampCalls)
			}
			want := map[string]int{models.PaycrestStatusCancelled: 1}
			if got := payOrderStatuses(t, r, req); !maps.Equal(got, want) {
				t.Fatalf("orders %v: want the one short order cancelled", got)
			}
		})
	}
}

// The one reopen is the last: an order that comes back short again is
// cancelled and refused, leaving nothing fundable.
func TestCreateSellForFiat_KeepSurplusReopensOnlyOnce(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	withRate(r, "1352.22")
	r.client.offrampReportsFeeOnDeposit = true
	r.client.offrampRates = []string{"1351.28", "1351.00"}
	req := keepSurplusReq(t, r, "5000.00")
	if _, err := r.svc.CreateSellForFiat(context.Background(), req, false, nil); !errors.Is(err, ErrSellRateMoved) {
		t.Fatalf("err = %v, want ErrSellRateMoved", err)
	}
	if r.client.offrampCalls != 2 {
		t.Fatalf("%d orders opened, want 2: one reopen, never a second", r.client.offrampCalls)
	}
	want := map[string]int{models.PaycrestStatusCancelled: 2}
	if got := payOrderStatuses(t, r, req); !maps.Equal(got, want) {
		t.Fatalf("orders %v: want both cancelled", got)
	}
}

// However far the rate falls, the fee gives up at most the window: past it the
// answer is short, at the lowest fee the window allows.
func TestKeepSurplusFee_GivesUpAtMostTheWindow(t *testing.T) {
	tokens := naira(payerDeposit("50000.00", "1500", "1"))
	fee, _, short := keepSurplusFee(tokens, "1490", "1", naira("50000"))
	if !short {
		t.Fatalf("fee %s%% covered a ₦10/USD fall", fee)
	}
	if gave := feeGivenUp(t, tokens, fee, "1", naira("1490")); gave.Cmp(payFeeAbsorbLimit) > 0 {
		t.Fatalf("floor fee %s%% gives up ₦%s, past the window", fee, gave.FloatString(2))
	}
	steps, _ := feePercentToSteps(fee, surplusFeeStepsPerPercent)
	below := stepsToFeePercent(steps-1, surplusFeeStepsPerPercent)
	if gave := feeGivenUp(t, tokens, below, "1", naira("1490")); gave.Cmp(payFeeAbsorbLimit) <= 0 {
		t.Fatalf("floor %s%% is not the lowest: %s%% still gives up only ₦%s", fee, below, gave.FloatString(2))
	}
}

func TestKeepSurplusFee_TakesTheHighestStepThatStillClears(t *testing.T) {
	tokens := naira("33.671134") // more than ₦50,000 at 1% needs: the excess is ours
	owed := naira("50000")
	got, capped, short := keepSurplusFee(tokens, "1500", "1", owed)
	steps, ok := feePercentToSteps(got, surplusFeeStepsPerPercent)
	if !ok || capped || short || steps <= 10_000 {
		t.Fatalf("fee = %q capped=%v short=%v", got, capped, short)
	}
	clears := func(s int) bool {
		f, _ := paycrestReportedSellFee(tokens, stepsToFeePercent(s, surplusFeeStepsPerPercent))
		return paycrestSellPayout(tokens, f, naira("1500")).Cmp(owed) >= 0
	}
	if !clears(steps) || clears(steps+1) {
		t.Fatalf("fee %s%% is not the boundary: clears=%v, next clears=%v", got, clears(steps), clears(steps+1))
	}
}

// With no rate to price at, the configured fee is sent and the payout check
// decides.
func TestKeepSurplusFee_KeepsTheConfiguredFeeWithNoRate(t *testing.T) {
	if got, _, short := keepSurplusFee(naira("40"), "", "1", naira("50000")); got != "1" || short {
		t.Fatalf("fee = %q short=%v, want the configured 1", got, short)
	}
}

// A deposit far larger than the invoice needs stops at the configured fee plus
// 0.5%, and says so — the excess is left to the merchant, never silently kept.
func TestKeepSurplusFee_IsCappedAtHalfAPercentAboveTheConfiguredFee(t *testing.T) {
	got, capped, short := keepSurplusFee(naira("67"), "1500", "1", naira("50000"))
	if got != "1.5" || !capped || short {
		t.Fatalf("fee = %q capped=%v short=%v, want 1.5 and capped", got, capped, short)
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

// A failed open is never retried at another fee: a refusal (4xx) created
// nothing and fails the attempt, and any other failure (5xx, a lost answer)
// may have created an order.
func TestCreateSellForFiat_KeepSurplusNeverResendsAFailedOrder(t *testing.T) {
	for _, failure := range []error{
		&PaycrestRequestError{Op: "offramp", Status: 400, Msg: "senderFeePercent: invalid"},
		&PaycrestRequestError{Op: "offramp", Status: 502, Msg: "bad gateway"},
	} {
		r := newOrderServiceWithFakes(t)
		r.client.offrampErrFirst = failure
		if _, err := r.svc.CreateSellForFiat(context.Background(), keepSurplusReq(t, r, "50000.00"), false, nil); err == nil {
			t.Fatalf("%v was swallowed", failure)
		}
		if r.client.offrampCalls != 1 {
			t.Fatalf("%v: %d calls, want 1", failure, r.client.offrampCalls)
		}
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
