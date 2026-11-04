package services

import (
	"bytes"
	"context"
	"log"
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The sender fee is priced into the rate on both sides, so every rate the user
// sees is the rate they get and every amount on screen is the amount that
// moves. Before this, a sell of ₦800 said "You receive ₦800" and paid ₦796.06
// (PCS-14a0c0e46450-8aafca3ee9, 2026-09-14): Paycrest takes the fee out of the
// deposited token before converting it.

func TestEffectiveRate(t *testing.T) {
	cases := []struct {
		name string
		side string
		rate float64
		pct  float64
		want float64
	}{
		{"buy costs more naira per token", "buy", 1390.64, 0.5, 1397.5932},
		{"sell pays fewer naira per token", "sell", 1360.05, 0.5, 1353.24975},
		{"no fee leaves the rate alone", "sell", 1360.05, 0, 1360.05},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveRate(tc.side, tc.rate, tc.pct); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("effectiveRate(%s, %v, %v) = %v, want %v", tc.side, tc.rate, tc.pct, got, tc.want)
			}
		})
	}
}

func TestPaycrestQuote_SenderFeeIsPricedIntoTheSellRate(t *testing.T) {
	q := &stubQuoter{market: 1370.15, rates: map[string]*PaycrestRates{"base|USDC": sell("1360.05")}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})
	s.SetSenderFeePercent("0.5")

	res, err := s.Quote(context.Background(), "sell", "NGN", "800")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if res.Best.Rate != "1353.25" {
		t.Errorf("rate = %s, want 1353.25 (1360.05 less 0.5%%)", res.Best.Rate)
	}
	if want := 800 / 1353.24975; math.Abs(res.Best.TokenAmount-want) > 1e-9 {
		t.Errorf("tokenAmount = %v, want %v", res.Best.TokenAmount, want)
	}
	if want := (1353.24975/1370.15 - 1) * 100; math.Abs(res.Best.SpreadPercent-want) > 1e-9 {
		t.Errorf("spread = %v, want %v (the user's whole cost, fee included)", res.Best.SpreadPercent, want)
	}
}

func TestPaycrestQuote_SenderFeeIsPricedIntoTheBuyRate(t *testing.T) {
	q := &stubQuoter{market: 1370.68, rates: map[string]*PaycrestRates{"base|USDC": buy("1390.64")}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})
	s.SetSenderFeePercent("0.5")

	// ₦8,000: 0.5% of the ~5.7 USDC is more than Paycrest's network fee, so
	// our take is the charge and Paycrest's fee comes out of it.
	res, err := s.Quote(context.Background(), "buy", "NGN", "8000")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if res.Best.Rate != "1397.59" || len(res.Best.Fees) != 0 {
		t.Errorf("venue = %+v, want rate 1397.59 (1390.64 plus 0.5%%) and no fee line", res.Best)
	}
	// Must agree with what the order will deliver once CreateBuy grosses the
	// naira down: 7960.20 / 1390.64.
	if delivered := 7960.20 / 1390.64; math.Abs(res.Best.TokenAmount-delivered) > 1e-5 {
		t.Errorf("tokenAmount = %v, but the order delivers %v", res.Best.TokenAmount, delivered)
	}
}

// Live 2026-11-04 (PCB-14a0c0e46450-981b954d07): a ₦1,500 buy was asked for
// ₦1,512.88. On a small buy Paycrest's network fee (0.02 USDC) outweighs our
// take and is charged instead. She still pays what she entered, at the
// provider's own rate, and receives that less the fee, shown as a line.
func TestPaycrestQuote_SmallBuyChargesTheNetworkFeeAsALine(t *testing.T) {
	q := &stubQuoter{market: 1370.68, rates: map[string]*PaycrestRates{"base|USDC": buy("1390.64")}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})
	s.SetSenderFeePercent("0.5")

	res, err := s.Quote(context.Background(), "buy", "NGN", "800")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	v := res.Best
	if want := 800/1390.64 - 0.02; v.Rate != "1390.64" || math.Abs(v.TokenAmount-want) > 1e-6 {
		t.Fatalf("venue = %+v, want the provider's 1390.64 and %.6f USDC", v, want)
	}
	if len(v.Fees) != 1 || v.Fees[0].Kind != FeeKindNetwork || v.Fees[0].Label != "Network fee" ||
		v.Fees[0].Token != "0.02" || v.Fees[0].Ngn != "27.81" {
		t.Fatalf("fees = %+v, want one 0.02 USDC (₦27.81) network fee line", v.Fees)
	}
	if eff, _ := strconv.ParseFloat(v.EffectiveRate, 64); math.Abs(eff-800/v.TokenAmount) > 0.02 { // rounded up to the kobo
		t.Errorf("effective rate %s, want ₦800 ÷ %.6f", v.EffectiveRate, v.TokenAmount)
	}
	if want := (800/v.TokenAmount/1370.68 - 1) * 100; math.Abs(v.SpreadPercent-want) > 1e-3 {
		t.Errorf("spread %.4f%%, want %.4f%% — her whole cost, fee included", v.SpreadPercent, want)
	}
}

func TestPaycrestQuote_NoSenderFeeQuotesTheProviderRateVerbatim(t *testing.T) {
	q := &stubQuoter{market: 1370.15, rates: map[string]*PaycrestRates{"base|USDC": sell("1360.05")}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})

	res, err := s.Quote(context.Background(), "sell", "NGN", "800")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if res.Best.Rate != "1360.05" {
		t.Errorf("rate = %s, want the provider's 1360.05 untouched", res.Best.Rate)
	}
}

func TestPaycrestVenueFor_BindsTheFeeInclusiveRate(t *testing.T) {
	q := &stubQuoter{market: 1370.15, rates: map[string]*PaycrestRates{"base|USDC": sell("1360.05")}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})
	s.SetSenderFeePercent("0.5")

	v, err := s.VenueFor(context.Background(), "sell", "NGN", "800", "base", "USDC")
	if err != nil {
		t.Fatalf("VenueFor: %v", err)
	}
	if v.Rate != "1353.25" {
		t.Errorf("rate = %s, want 1353.25", v.Rate)
	}
	if want := 800 / 1353.24975; math.Abs(v.TokenAmount-want) > 1e-9 {
		t.Errorf("tokenAmount = %v, want %v", v.TokenAmount, want)
	}
}

// capturingOrderer records each create request and answers the way Paycrest
// does: the rate it quoted, the fee rounded to 4 places of token, and the fee
// percent it applied. On a buy it charges max(sender fee, network fee) on top
// and pays us the sender fee less the network fee (live 2026-11-04).
type capturingOrderer struct {
	stubPaycrestOrderer
	rate       string
	feePercent string
	onramp     PaycrestOnrampRequest
	offramp    PaycrestOfframpRequest
}

func (c *capturingOrderer) CreateOnrampOrder(_ context.Context, req PaycrestOnrampRequest) (*PaycrestOrder, error) {
	c.onramp = req
	fiat, _ := strconv.ParseFloat(req.AmountNGN, 64)
	rate, _ := strconv.ParseFloat(c.rate, 64)
	pct, _ := strconv.ParseFloat(c.feePercent, 64)
	amount := math.Round(fiat/rate*1e6) / 1e6
	fee := math.Round(amount*pct/100*1e4) / 1e4
	const network = 0.02
	return &PaycrestOrder{
		ID:               "pc-buy",
		Amount:           strconv.FormatFloat(amount, 'f', 6, 64),
		Rate:             c.rate,
		SenderFee:        strconv.FormatFloat(math.Max(fee-network, 0), 'f', 4, 64),
		SenderFeePercent: c.feePercent,
		TransactionFee:   "0.02",
		ProviderAccount: &PaycrestProviderAccount{
			Institution:       "OPAYNGPC",
			AccountIdentifier: "0000000000",
			AmountToTransfer:  strconv.FormatFloat((amount+math.Max(fee, network))*rate, 'f', 2, 64),
			ValidUntil:        "2026-09-14T01:33:24Z",
		},
	}, nil
}

func (c *capturingOrderer) CreateOfframpOrder(_ context.Context, req PaycrestOfframpRequest) (*PaycrestOrder, error) {
	c.offramp = req
	amt, _ := strconv.ParseFloat(req.AmountToken, 64)
	pct, _ := strconv.ParseFloat(c.feePercent, 64)
	return &PaycrestOrder{
		ID:               "pc-sell",
		Amount:           req.AmountToken,
		Rate:             c.rate,
		SenderFee:        strconv.FormatFloat(math.Round(amt*pct/100*1e4)/1e4, 'f', 4, 64),
		SenderFeePercent: c.feePercent,
		ProviderAccount: &PaycrestProviderAccount{
			ReceiveAddress: "0xdeposit",
			ValidUntil:     "2026-09-14T01:33:24Z",
		},
	}, nil
}

// newFeeTestService wires the order service the way main does — through the
// constructor, so the quote service gets its fee from the same place.
func newFeeTestService(t *testing.T, configuredPct string, pc *capturingOrderer, side, rate string) *PaycrestOrderService {
	t.Helper()
	base, _ := newPaycrestOrderTestService(t)
	book := sell(rate)
	if side == "buy" {
		book = buy(rate)
	}
	quotes := NewPaycrestQuoteService(
		&stubQuoter{market: 1370.15, rates: map[string]*PaycrestRates{"base|USDC": book}},
		[]string{"USDC"}, []string{"base"})
	svc := NewPaycrestOrderService(base.db, pc, quotes, base.dir, configuredPct, "")
	if svc == nil {
		t.Fatal("NewPaycrestOrderService returned nil")
	}
	return svc
}

func sellRequest() PaycrestSellRequest {
	return PaycrestSellRequest{
		UserID:        uuid.New(),
		WalletAddress: "0xabc",
		FiatCode:      "NGN",
		FiatAmount:    "800",
		ChainKey:      "base",
		Token:         "USDC",
		BankCode:      "044",
		AccountNo:     "0123456789",
		AccountName:   "Test User",
	}
}

// captureLog swaps the standard logger's output for the test's duration.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// The bug itself, replayed: the user asks to receive ₦800 and, after Paycrest
// takes the fee out of the deposit, receives ₦800 — not ₦796.
func TestPaycrestOrderService_CreateSell_UserReceivesTheAmountTheyEntered(t *testing.T) {
	pc := &capturingOrderer{rate: "1360.05", feePercent: "0.5"}
	svc := newFeeTestService(t, "0.5", pc, "sell", "1360.05")
	logs := captureLog(t)

	row, err := svc.CreateSell(context.Background(), sellRequest())
	if err != nil {
		t.Fatalf("CreateSell: %v", err)
	}

	deposited, _ := strconv.ParseFloat(pc.offramp.AmountToken, 64)
	fee := math.Round(deposited*0.005*1e4) / 1e4 // Paycrest's rounding, as observed
	received := (deposited - fee) * 1360.05
	// Paycrest rounds the fee to 4 places of token; that is worth up to ~₦0.07.
	if math.Abs(received-800) > 0.10 {
		t.Errorf("deposit %s pays out ₦%.2f, want ₦800", pc.offramp.AmountToken, received)
	}
	if pc.offramp.SenderFeePercent != "0.5" {
		t.Errorf("order sent fee %q, want 0.5", pc.offramp.SenderFeePercent)
	}
	if row.Rate != "1353.25" {
		t.Errorf("stored rate = %s, want the fee-inclusive 1353.25 the user was quoted", row.Rate)
	}
	if row.SenderFeePercent != "0.5" {
		t.Errorf("stored fee percent = %q, want 0.5 so the provider rate stays recoverable", row.SenderFeePercent)
	}
	if row.FiatAmount != "800" || row.TokenAmount != pc.offramp.AmountToken {
		t.Errorf("row amount=%s token=%s, want 800 and the submitted %s", row.FiatAmount, row.TokenAmount, pc.offramp.AmountToken)
	}
	if strings.Contains(logs.String(), "ALARM") {
		t.Errorf("unexpected alarm on a consistent order: %s", logs.String())
	}
}

func TestPaycrestOrderService_CreateBuy_StoresTheFeeInclusiveRate(t *testing.T) {
	pc := &capturingOrderer{rate: "1390.64", feePercent: "0.5"}
	svc := newFeeTestService(t, "0.5", pc, "buy", "1390.64")

	row, err := svc.CreateBuy(context.Background(), PaycrestBuyRequest{
		BuyerID:           testBuyerID(),
		UserID:            seedVerifiedUser(t, svc.db),
		WalletAddress:     "0xabc",
		FiatCode:          "NGN",
		FiatAmount:        "8000",
		ChainKey:          "base",
		Token:             "USDC",
		RefundBankCode:    "044",
		RefundAccount:     "0123456789",
		RefundAccountName: "Test User",
	})
	if err != nil {
		t.Fatalf("CreateBuy: %v", err)
	}
	if pc.onramp.AmountNGN != "7960.20" {
		t.Errorf("submitted ₦%s, want the grossed-down 7960.20", pc.onramp.AmountNGN)
	}
	if row.Rate != "1397.59" {
		t.Errorf("stored rate = %s, want the fee-inclusive 1397.59", row.Rate)
	}
	if row.SenderFeePercent != "0.5" {
		t.Errorf("stored fee percent = %q, want 0.5", row.SenderFeePercent)
	}
	// Two cross-checks of one story: the user transfers what they typed, and
	// that transfer at the stored rate buys the tokens delivered.
	if pay, _ := strconv.ParseFloat(row.PayAmount, 64); math.Abs(pay-8000) > 0.10 {
		t.Errorf("user told to transfer ₦%s, want ₦8000", row.PayAmount)
	}
	if tokens, _ := strconv.ParseFloat(row.TokenAmount, 64); math.Abs(tokens*1397.5932-8000) > 0.01 {
		t.Errorf("%s tokens at the stored rate is ₦%.4f, want ₦8000", row.TokenAmount, tokens*1397.5932)
	}
}

// The live case: on a small buy she still transfers exactly what she entered,
// receives it at the provider's rate less Paycrest's network fee, and the row
// says so.
func TestPaycrestOrderService_CreateBuy_SmallBuyPaysTheAmountEntered(t *testing.T) {
	pc := &capturingOrderer{rate: "1390.64", feePercent: "0.5"}
	svc := newFeeTestService(t, "0.5", pc, "buy", "1390.64")
	logs := captureLog(t)

	row, err := svc.CreateBuy(context.Background(), PaycrestBuyRequest{
		BuyerID: testBuyerID(), UserID: seedVerifiedUser(t, svc.db), WalletAddress: "0xabc",
		FiatCode: "NGN", FiatAmount: "800", ChainKey: "base", Token: "USDC",
		RefundBankCode: "044", RefundAccount: "0123456789", RefundAccountName: "Test User",
	})
	if err != nil {
		t.Fatalf("CreateBuy: %v", err)
	}
	if pay, _ := strconv.ParseFloat(row.PayAmount, 64); math.Abs(pay-800) > 0.01 {
		t.Errorf("told to transfer ₦%s, want ₦800", row.PayAmount)
	}
	if row.NetworkFee != "0.02" || row.Rate != "1390.64" {
		t.Errorf("row network fee %q rate %s, want 0.02 and the provider's 1390.64", row.NetworkFee, row.Rate)
	}
	if strings.Contains(logs.String(), "ALARM") || strings.Contains(logs.String(), "drift") {
		t.Errorf("unexpected alarm or drift: %s", logs.String())
	}
}

// Env unset, dashboard still charging 0.5%: the quote showed no fee but the
// user was charged one. The row must record the truth, and someone must hear.
func TestPaycrestOrderService_FeeMismatchIsRecordedAndAlarmed(t *testing.T) {
	pc := &capturingOrderer{rate: "1360.05", feePercent: "0.5"}
	svc := newFeeTestService(t, "", pc, "sell", "1360.05")
	logs := captureLog(t)

	row, err := svc.CreateSell(context.Background(), sellRequest())
	if err != nil {
		t.Fatalf("CreateSell: %v", err)
	}
	if row.SenderFeePercent != "0.5" {
		t.Errorf("stored fee percent = %q, want Paycrest's 0.5, not our empty config", row.SenderFeePercent)
	}
	if row.Rate != "1353.25" {
		t.Errorf("stored rate = %s, want 1353.25 — the rate the user actually got", row.Rate)
	}
	if !strings.Contains(logs.String(), "ALARM sender fee mismatch") {
		t.Errorf("want a mismatch alarm, got logs: %q", logs.String())
	}
}

func TestNewPaycrestOrderService_RefusesAnUnusableFee(t *testing.T) {
	for _, pct := range []string{"NaN", "Inf", "-0.5", "0", "50", "abc"} {
		t.Run(pct, func(t *testing.T) {
			logs := captureLog(t)
			svc := newFeeTestService(t, pct, &capturingOrderer{}, "sell", "1360.05")
			if svc.senderFeePercent != "" {
				t.Errorf("senderFeePercent = %q, want it refused", svc.senderFeePercent)
			}
			if svc.quotes.senderFeePercent != 0 {
				t.Errorf("quote fee = %v, want 0", svc.quotes.senderFeePercent)
			}
			if !strings.Contains(logs.String(), "refused") {
				t.Errorf("want a refusal logged for %q, got %q", pct, logs.String())
			}
		})
	}
}

func TestNewPaycrestOrderService_CanonicalisesAndSharesTheFee(t *testing.T) {
	svc := newFeeTestService(t, " 0.50 ", &capturingOrderer{}, "sell", "1360.05")
	if svc.senderFeePercent != "0.5" {
		t.Errorf("senderFeePercent = %q, want canonical 0.5 so it compares equal to Paycrest's", svc.senderFeePercent)
	}
	if svc.quotes.senderFeePercent != 0.5 {
		t.Errorf("quote fee = %v, want 0.5 from the same constructor call", svc.quotes.senderFeePercent)
	}
}

// The naira route prices a merchant's crypto from the rate an order will
// actually be opened at. That is the provider's own sell rate — not the sender
// fee's effect on it, and not the cached market mid.
func TestPaycrestProviderRate_IsTheProvidersSellRateWithNoFee(t *testing.T) {
	q := &stubQuoter{market: 1370.15, rates: map[string]*PaycrestRates{"base|USDC": sell("1360.05")}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})
	s.SetSenderFeePercent("0.5")

	rate, err := s.ProviderRate(context.Background(), "NGN")
	if err != nil {
		t.Fatal(err)
	}
	if rate.Cmp(big.NewRat(136005, 100)) != 0 {
		t.Fatalf("rate = %s, want exactly 136005/100 (1360.05) — not the fee-inclusive 1353.25 nor the mid 1370.15", rate.FloatString(4))
	}
}

func TestPaycrestProviderRate_NoSellVenueIsAnError(t *testing.T) {
	q := &stubQuoter{market: 1370.15, rates: map[string]*PaycrestRates{}}
	s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})
	if rate, err := s.ProviderRate(context.Background(), "NGN"); err == nil {
		t.Fatalf("rate %s from a book with no Base USDC sell venue", rate)
	}
}
