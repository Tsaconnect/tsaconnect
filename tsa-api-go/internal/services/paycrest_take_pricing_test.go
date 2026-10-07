package services

import (
	"context"
	"math"
	"math/big"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

// paycrest_take_pricing_test.go — owner ruling 2026-10-07: every Paycrest ramp
// order, buy or sell, direct, converted or swapped, earns only the company take
// (RAMP_COMPANY_TAKE_BPS), sent to Paycrest as the sender fee. Third-party
// costs — the LiFi round trip and the gas we pay — pass on at cost, as a fee
// line, never carved out of a fixed markup. TSA Pay's payouts keep their own
// split.

// takeRig is the conversion rig with the take set on both services, the way
// NewPaycrestOrderService sets it: quotes price in what orders send.
func takeRig(t *testing.T, takeBps int64) *orderConversionRig {
	t.Helper()
	r := newOrderServiceWithFakes(t)
	take := SenderFeePercentFromBps(takeBps)
	r.svc.senderFeePercent = take
	r.quotes.SetSenderFeePercent(take)
	r.svc.SetConversionEnabled(true)
	r.svc.SetTreasury("0xTreasury")
	return r
}

func TestPaycrestTake_DirectBuyPricesAtTheTakeOnly(t *testing.T) {
	r := takeRig(t, 50)
	res, err := r.quotes.Quote(context.Background(), "buy", "NGN", "50000")
	if err != nil {
		t.Fatal(err)
	}
	v := res.Best
	// 1500 × (1 + 0.5%): the take is the only thing in the rate, and a direct
	// venue passes no third-party cost on.
	if v.Rate != "1507.50" || len(v.Fees) != 0 || v.EffectiveRate != "" {
		t.Fatalf("venue = %+v, want rate 1507.50 with no fee lines", v)
	}
	user := seedVerifiedUser(t, r.db)
	row, err := r.svc.CreateBuy(context.Background(), r.buyRequest(user, "base", "USDC", "base", "USDC"))
	if err != nil {
		t.Fatal(err)
	}
	if r.client.lastCreate.SenderFeePercent != "0.5" || row.SenderFeePercent != "0.5" || row.Rate != "1507.50" {
		t.Fatalf("sender fee sent %q, row fee %q rate %q; want the 0.5%% take and 1507.50",
			r.client.lastCreate.SenderFeePercent, row.SenderFeePercent, row.Rate)
	}
}

func TestPaycrestTake_DirectSellPricesAtTheTakeOnly(t *testing.T) {
	r := takeRig(t, 50)
	res, err := r.quotes.Quote(context.Background(), "sell", "NGN", "50000")
	if err != nil {
		t.Fatal(err)
	}
	if v := res.Best; v.Rate != "1492.50" || len(v.Fees) != 0 {
		t.Fatalf("venue = %+v, want rate 1500 × (1 − 0.5%%) = 1492.50 with no fee lines", v)
	}
	req := payoutReq(t, r)
	row, err := r.svc.CreateSell(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// ₦50,000 at 1492.50 is a 33.500838 USDC deposit; Paycrest takes the 0.5%
	// out of it and pays the naira she entered.
	if r.client.lastOfframp.SenderFeePercent != "0.5" || r.client.lastOfframp.AmountToken != "33.500838" || row.Rate != "1492.50" {
		t.Fatalf("offramp %+v row rate %s; want fee 0.5, deposit 33.500838, rate 1492.50", r.client.lastOfframp, row.Rate)
	}
}

// A converted buy is sent the take, delivers D at the take's rate, and owes
// her T = D·(1 − B′), B′ being what the route really costs plus 10 bps
// headroom — shown as "Conversion (LiFi)", with the §14 reconciliation:
// amount ÷ rate − fees = tokenAmount, to the base unit.
func TestPaycrestTake_ConvertedBuyChargesTheTakePlusTheRoutesCost(t *testing.T) {
	r := takeRig(t, 50)
	venues, err := r.quotes.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
		[]RequestedAsset{{Chain: "optimism", Token: "USDT", Recipient: orderTestUserWallet}})
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v err = %v", venues, err)
	}
	v := venues[0]
	// The rig's route costs l = 20 bps (99.8 of 100) and no gas: B′ = 30.
	if v.MarkupBps != 30 || v.CostBps != 20 || v.Rate != "1507.50" {
		t.Fatalf("budget %d cost %d rate %s; want 30, 20 and the take-only 1507.50", v.MarkupBps, v.CostBps, v.Rate)
	}
	d := big.NewInt(99_502487) // floor(150000 / 1507.50, 6 dp)
	owed := ConversionOwed(d, 30)
	if v.TokenAmountBaseUnits != owed.String() || owed.String() != "99203979" {
		t.Fatalf("T = %s, want D·(1 − 0.3%%) = 99203979", v.TokenAmountBaseUnits)
	}
	if len(v.Fees) != 1 {
		t.Fatalf("fees = %+v, want one Conversion (LiFi) line", v.Fees)
	}
	f := v.Fees[0]
	if f.Kind != FeeKindConversion || f.Label != ConversionLiFiLabel || f.Provider != FeeProviderLiFi ||
		f.Token != "0.298508" || f.Ngn != "450.00" {
		t.Fatalf("fee line = %+v, want 0.298508 USDC (₦450.00 at 1507.50)", f)
	}
	if v.EffectiveRate != "1512.04" {
		t.Fatalf("effective rate = %s, want 150000 ÷ 99.203979 = 1512.036…, rounded up to 1512.04", v.EffectiveRate)
	}
	gross, _ := venueBaseUnits("150000", v.Rate, 6)
	fee, _ := DecimalToBaseUnits(f.Token, 6)
	if diff := new(big.Int).Sub(new(big.Int).Sub(gross, fee), owed); diff.CmpAbs(big.NewInt(1)) > 0 {
		t.Fatalf("§14: amount ÷ rate − fees = %s, tokenAmount %s", new(big.Int).Sub(gross, fee), owed)
	}

	// The order is sent the take, and she is owed T from what Paycrest itself
	// delivers (the fake's 100 USDC), on the same formula.
	user := seedVerifiedUser(t, r.db)
	req := r.buyRequest(user, "base", "USDC", "optimism", "USDT")
	req.FiatAmount = "150000"
	row, err := r.svc.CreateBuy(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if r.client.lastCreate.SenderFeePercent != "0.5" || row.MarkupBps != 30 ||
		row.QuotedAmount != ConversionOwed(big.NewInt(100_000000), 30).String() {
		t.Fatalf("sent fee %q, row budget %d, owed %s; want 0.5, 30 and 99700000",
			r.client.lastCreate.SenderFeePercent, row.MarkupBps, row.QuotedAmount)
	}
}

// A swapped sell is sent the take and passes the round trip on at cost: our
// LiFi fee is the gas we pay (g), never a budget's unspent rest, and the
// route's whole cost is its "Conversion (LiFi)" line.
func TestPaycrestTake_SwappedSellChargesTheTakePlusTheRoundTripAtCost(t *testing.T) {
	// l = 20 (9.98 of $10), g = the $0.02 relay = 20 bps; the fee-carrying
	// route lands 9.96 (l2 = 40, our 20 inside it).
	q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.96))
	in := sellIn
	in.TSAPay = false
	res, err := q.Quote(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.CostBps != 20 || res.GasBps != 20 || res.FeeBps != 20 || !res.PassThrough {
		t.Fatalf("cost/gas/fee = %d/%d/%d pass-through %v; want 20/20/20 at cost", res.CostBps, res.GasBps, res.FeeBps, res.PassThrough)
	}
	if last := lifi.lastRequest(); last.FeeBps != 20 {
		t.Fatalf("our LiFi fee = %d bps, want the 20 bps of gas we pay and nothing more", last.FeeBps)
	}
	if res.Rate != "1492.50" || res.USDCAmount != "9.96" || res.FiatEstimate != "14865.30" {
		t.Fatalf("rate %s usdc %s fiat %s; want the take-only 1492.50 on 9.96 USDC", res.Rate, res.USDCAmount, res.FiatEstimate)
	}
	// 9.96 is 40 bps short of the 10 USDC a free route lands: 0.04 USDC, ₦59.70.
	if len(res.Fees) != 1 || res.Fees[0].Label != ConversionLiFiLabel || res.Fees[0].Token != "0.04" ||
		res.Fees[0].Ngn != "59.70" || res.EffectiveRate != "1486.53" {
		t.Fatalf("fees %+v effective %s; want one 0.04 USDC / ₦59.70 line and 1486.53 all in", res.Fees, res.EffectiveRate)
	}
}

// The take is our margin and nothing else: moving it moves the rate by
// exactly the take and the sender fee with it, and leaves every third-party
// cost where it was.
func TestPaycrestTake_ChangingTheTakeChangesOnlyOurMargin(t *testing.T) {
	type priced struct {
		rate, sent, fee string
		budget, cost    int
	}
	buyAt := func(takeBps int64) priced {
		r := takeRig(t, takeBps)
		venues, err := r.quotes.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
			[]RequestedAsset{{Chain: "optimism", Token: "USDT", Recipient: orderTestUserWallet}})
		if err != nil || len(venues) != 1 {
			t.Fatalf("take %d: venues = %+v err = %v", takeBps, venues, err)
		}
		user := seedVerifiedUser(t, r.db)
		if _, err := r.svc.CreateBuy(context.Background(), r.buyRequest(user, "base", "USDC", "optimism", "USDT")); err != nil {
			t.Fatal(err)
		}
		v := venues[0]
		d, _ := venueBaseUnits("150000", v.Rate, 6)
		owed, _ := new(big.Int).SetString(v.TokenAmountBaseUnits, 10)
		// The fee line, as a share of D, is the route's cost whatever the take.
		share := new(big.Int).Quo(new(big.Int).Mul(new(big.Int).Sub(d, owed), big.NewInt(10_000)), d)
		return priced{rate: v.Rate, sent: r.client.lastCreate.SenderFeePercent, fee: share.String(), budget: v.MarkupBps, cost: v.CostBps}
	}
	lo, hi := buyAt(50), buyAt(100)
	if lo.rate != "1507.50" || hi.rate != "1515.00" || lo.sent != "0.5" || hi.sent != "1" {
		t.Fatalf("take 50: %+v, take 100: %+v; the rate and sender fee must follow the take", lo, hi)
	}
	if lo.budget != hi.budget || lo.cost != hi.cost || lo.fee != hi.fee {
		t.Fatalf("take 50: %+v, take 100: %+v; the route's cost must not move with the take", lo, hi)
	}

	sellAt := func(take string) *SellSwapQuote {
		q, _ := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.96))
		q.take = take
		in := sellIn
		in.TSAPay = false
		res, err := q.Quote(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	s1, s2 := sellAt("0.5"), sellAt("1")
	if s1.Rate != "1492.50" || s2.Rate != "1485.00" {
		t.Fatalf("sell rates %s / %s, want 1492.50 / 1485.00", s1.Rate, s2.Rate)
	}
	if s1.CostBps != s2.CostBps || s1.FeeBps != s2.FeeBps || s1.Fees[0].Token != s2.Fees[0].Token {
		t.Fatalf("take 0.5: %+v, take 1: %+v; the round trip must not move with the take", s1, s2)
	}
}

// TSA Pay's payouts are priced on the payment's own split, whatever the ramp
// take is: the same deposit, the same F − B sender fee and the same LiFi fee
// (B − l − g) as before the ruling.
func TestPaycrestTake_TSAPayPayoutIsUnchanged(t *testing.T) {
	r := takeRig(t, 25) // a take TSA Pay must never see
	dep, err := r.svc.SellDepositFor(context.Background(), "NGN", "50000.00", "base", "USDC", true)
	if err != nil {
		t.Fatal(err)
	}
	if dep.String() != "33501933" {
		t.Fatalf("converted payout deposit = %s, want the pre-ruling 33501933 (F − B = 0.5%%)", dep)
	}
	if _, err := r.svc.CreateSellForFiat(context.Background(), payoutReq(t, r), true, nil); err != nil {
		t.Fatal(err)
	}
	if got := r.client.lastOfframp.SenderFeePercent; got != "0.5" {
		t.Fatalf("converted payout sent fee %q, want TSA Pay's F − B = 0.5, not the 0.25 take", got)
	}
	direct := takeRig(t, 25)
	if _, err := direct.svc.CreateSellForFiat(context.Background(), keepSurplusReq(t, direct, "50000.00"), false, nil); err != nil {
		t.Fatal(err)
	}
	if fee, ok := parseSenderFeePercent(direct.client.lastOfframp.SenderFeePercent); !ok || fee < 1 {
		t.Fatalf("direct payout sent fee %q, want TSA Pay's F = 1%% (KeepSurplus may only raise it)", direct.client.lastOfframp.SenderFeePercent)
	}
	if bps, ok := PayFeeBps(r.svc.pay.FeePercent); !ok || bps != 100 {
		t.Fatalf("platform fee = %d bps, want TSA Pay's 100", bps)
	}

	q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.97))
	q.take = "0.25"
	res, err := q.Quote(context.Background(), sellIn) // sellIn is a TSA Pay payout
	if err != nil {
		t.Fatal(err)
	}
	if res.FeeBps != 10 || res.PassThrough || lifi.lastRequest().FeeBps != 10 || res.Rate != "1492.50" || len(res.Fees) != 0 {
		t.Fatalf("TSA Pay quote = %+v; want the pre-ruling B − l − g = 10 bps fee at F − B's 1492.50", res)
	}
}

// I-1: one RAMP_COMPANY_TAKE_BPS, one rule. Whatever the environment says,
// the company wallet and Flutterwave (ITService) and Paycrest (orders and
// quotes) read the same resolved take — so no quote and order can disagree,
// and Paycrest never falls back to its dashboard fee.
func TestCompanyTake_EveryProviderSeesTheSameResolvedTake(t *testing.T) {
	for raw, want := range map[string]int{"0": 50, "-5": 50, "501": 50, "abc": 50, "": 50, "120": 120, "500": 500} {
		t.Setenv("RAMP_COMPANY_TAKE_BPS", raw)
		cfg := config.Load()

		it := NewITService(nil, nil, nil)
		if err := it.SetRampBuyTerms(RampBuyTerms{CompanyTakeBps: cfg.RampCompanyTakeBps}); err != nil {
			t.Fatalf("%q: in-house refused the resolved take %d: %v", raw, cfg.RampCompanyTakeBps, err)
		}
		base, _ := newPaycrestOrderTestService(t)
		quotes := NewPaycrestQuoteService(&stubQuoter{market: 1500}, []string{"USDC"}, []string{"base"})
		orders := NewPaycrestOrderService(base.db, stubPaycrestOrderer{}, quotes, base.dir,
			SenderFeePercentFromBps(cfg.RampCompanyTakeBps), "")
		paycrest, ok := feePercentToBps(orders.TakePercent())
		if !ok || !orders.ConversionPriced() {
			t.Fatalf("%q: Paycrest has no usable take (%q)", raw, orders.TakePercent())
		}
		quoted := int(math.Round(quotes.senderFeePercent * 100))
		if got := int(it.rampTerms().takeBps); got != want || paycrest != want || quoted != want {
			t.Fatalf("%q: in-house %d, Paycrest orders %d, Paycrest quotes %d; want all %d", raw, got, paycrest, quoted, want)
		}
	}
	// And the in-house setter holds the same rule if handed a take directly.
	if err := NewITService(nil, nil, nil).SetRampBuyTerms(RampBuyTerms{CompanyTakeBps: 501}); err == nil {
		t.Fatal("in-house accepted a 501 bps take that Paycrest cannot send")
	}
}
