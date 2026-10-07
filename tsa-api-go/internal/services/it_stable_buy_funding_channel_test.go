package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// it_stable_buy_funding_channel_test.go — the funding fee line is the actual
// cost of the channel her naira arrives through: 1.40% into the central
// account (wallet funding), 2.15% into her virtual account (a collection).
// The quote, the order and every live re-price agree on it.

// channelRig is the routed buy rig with both measured funding fees in force.
func channelRig(t *testing.T) (*ITService, uuid.UUID) {
	t.Helper()
	s, userID := newRoutedBuyService(t)
	if err := s.SetRampBuyTerms(RampBuyTerms{VAFundingFeeBps: DefaultRampVAFundingFeeBps}); err != nil {
		t.Fatal(err)
	}
	return s, userID
}

func fundingLine(t *testing.T, q *StableBuyQuote) StableBuyFee {
	t.Helper()
	for _, f := range q.Fees {
		if f.Kind == FeeKindFunding {
			return f
		}
	}
	t.Fatalf("quote has no funding line: %+v", q.Fees)
	return StableBuyFee{}
}

// Both in-house providers quote the funding fee of the channel she would pay
// through: her virtual account (2.15%) until the central account is live,
// then the central account (1.40%) — and the line is exactly that share.
func TestFundingFee_QuotesTheChannelSheWillPayThrough(t *testing.T) {
	s, _ := channelRig(t)
	quote := func() (fw, tr *StableBuyQuote) {
		fw, err := s.QuoteRoutedStableBuy("USDC", "165025", "NGN", routedFWRate)
		if err != nil {
			t.Fatal(err)
		}
		tr, err = s.QuoteTreasuryBuy("USDC", "10000", "NGN", treasuryMarket)
		if err != nil {
			t.Fatal(err)
		}
		return fw, tr
	}
	vaFW, vaTR := quote()
	s.SetInboundCentral(true, testCentralAccount())
	cFW, cTR := quote()

	for _, c := range []struct {
		name     string
		q        *StableBuyQuote
		ch       PayInChannel
		label    string
		otherNet *big.Int
	}{
		{"flutterwave via VA", vaFW, PayInVA, "Funding fee (2.15%)", cFW.AssetUnits},
		{"company wallet via VA", vaTR, PayInVA, "Funding fee (2.15%)", cTR.AssetUnits},
		{"flutterwave via central", cFW, PayInCentral, "Funding fee (1.4%)", nil},
		{"company wallet via central", cTR, PayInCentral, "Funding fee (1.4%)", nil},
	} {
		if f := fundingLine(t, c.q); c.q.PayIn != c.ch || f.Label != c.label {
			t.Fatalf("%s: channel %q line %q, want %q %q", c.name, c.q.PayIn, f.Label, c.ch, c.label)
		}
		if c.otherNet != nil && c.q.AssetUnits.Cmp(c.otherNet) >= 0 {
			t.Fatalf("%s: delivers %s, not less than the central account's %s", c.name, c.q.AssetUnits, c.otherNet)
		}
	}
	// The funding fee is the first cost, taken off the same base: the two
	// lines stand 215 : 140, to the base unit.
	va, _ := new(big.Int).SetString(fundingLine(t, vaTR).Units, 10)
	central, _ := new(big.Int).SetString(fundingLine(t, cTR).Units, 10)
	cross := new(big.Int).Sub(new(big.Int).Mul(va, big.NewInt(140)), new(big.Int).Mul(central, big.NewInt(215)))
	if cross.CmpAbs(big.NewInt(215)) > 0 {
		t.Fatalf("funding lines %s (VA) and %s (central) are not 2.15%% : 1.40%% of one base", va, central)
	}
}

// An order is paid through one channel; a quote priced on the other is not
// her price, so the open refuses it rather than open at the wrong cost.
func TestFundingFee_AnOrderMustBePaidThroughTheQuotedChannel(t *testing.T) {
	s, userID := channelRig(t)
	s.SetInboundCentral(true, testCentralAccount())
	q := routedTestQuote(t, s) // central, 1.40%
	s.SetInboundCentral(false, CentralAccount{})
	if _, _, err := s.OpenRoutedStableBuy(context.Background(), userID, q, "POLYGON"); !errors.Is(err, ErrStableQuoteUnavailable) {
		t.Fatalf("err = %v, want a refusal: the order would be paid into her VA at 2.15%%", err)
	}
	// Quoted and opened on the same channel, it opens.
	if _, _, err := s.OpenRoutedStableBuy(context.Background(), userID, routedTestQuote(t, s), "POLYGON"); err != nil {
		t.Fatalf("VA quote, VA order: %v", err)
	}
}

// A live re-price passes on the cost of the channel the money ACTUALLY came
// through — a central-account credit (its NIBSS session id is the payment
// ref) or her virtual account — not whichever channel is switched on now.
func TestFundingFee_ARepriceUsesTheChannelTheMoneyCameThrough(t *testing.T) {
	s, userID := channelRig(t)
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	s.SetInboundCentral(true, testCentralAccount()) // what is live now must not matter
	if err := s.db.Exec(`INSERT INTO inbound_credits (id, provider, provider_ref, currency, amount_kobo, gross_kobo,
		sender_raw, transaction_date, seen_at, status) VALUES (?, 'flutterwave', 'NIP-SESSION-1', 'NGN', 16502500, 16502500,
		'ADA OKAFOR', ?, ?, 'matched')`, uuid.NewString(), time.Now(), time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Hour) // a routed buy, past its honour window
	priced := func(ref string) StableBuyFee {
		intent := &models.ITIntent{ID: uuid.New(), UserID: userID, Direction: models.ITDirectionBuy,
			Provider: models.ITProviderFlutterwave, Asset: models.ITAssetUSDC, Currency: "NGN", PaymentRef: ref,
			RateExpiresAt: &expired}
		q, err := s.quoteForIntent(context.Background(), intent, "16502500")
		if err != nil {
			t.Fatalf("ref %q: %v", ref, err)
		}
		if ch, _ := s.paidThrough(context.Background(), intent); ch != q.PayIn {
			t.Fatalf("ref %q: priced on %q, paid through %q", ref, q.PayIn, ch)
		}
		return fundingLine(t, q)
	}
	if f := priced("NIP-SESSION-1"); f.Label != "Funding fee (1.4%)" {
		t.Fatalf("central credit priced with %q", f.Label)
	}
	if f := priced("chg_va_1"); f.Label != "Funding fee (2.15%)" {
		t.Fatalf("VA charge priced with %q, want the VA's 2.15%% though central is live now", f.Label)
	}
}

// A channel with no known cost — empty or anything else — is never priced at
// either fee: every in-house price refuses it.
func TestFundingFee_AnUnknownChannelIsRefused(t *testing.T) {
	s, _ := channelRig(t)
	for _, ch := range []PayInChannel{"", "card", "CENTRAL"} {
		if _, err := s.fundingCost(ch); !errors.Is(err, ErrStableQuoteUnavailable) {
			t.Fatalf("%q: fundingCost err = %v, want a refusal", ch, err)
		}
		if q, err := s.routedQuoteAt("USDC", "NGN", big.NewInt(16_502_500), routedFWRate, false, ch); !errors.Is(err, ErrStableQuoteUnavailable) {
			t.Fatalf("%q: flutterwave priced it: %+v %v", ch, q, err)
		}
		if q, err := s.treasuryQuoteAt("USDC", "NGN", big.NewInt(1_000_000), treasuryMarket, false, ch); !errors.Is(err, ErrStableQuoteUnavailable) {
			t.Fatalf("%q: company wallet priced it: %+v %v", ch, q, err)
		}
	}
}
