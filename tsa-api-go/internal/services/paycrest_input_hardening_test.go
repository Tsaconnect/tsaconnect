package services

import (
	"context"
	"strings"
	"testing"
)

// ParseFloat accepts "NaN", "Inf", hex floats and digit separators. None of
// those is an amount or a rate, and NaN in particular slips past every "<= 0"
// check and on into order amounts. These tests pin the boundary.

func TestParsePositiveDecimal(t *testing.T) {
	good := map[string]float64{"800": 800, " 800 ": 800, "999.99": 999.99, "0.5": 0.5, "1360.05": 1360.05, "800.": 800, ".5": 0.5}
	for in, want := range good {
		if got, ok := parsePositiveDecimal(in); !ok || got != want {
			t.Errorf("parsePositiveDecimal(%q) = (%v, %v), want (%v, true)", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "NaN", "nan", "Inf", "-Inf", "0", "0.00", "-5", "1e3", "0x1p10", "1_000", "1,000", ".", "abc", "1e309"} {
		if got, ok := parsePositiveDecimal(in); ok {
			t.Errorf("parsePositiveDecimal(%q) = (%v, true), want refused", in, got)
		}
	}
}

// Quote is public, so this is reachable without an account.
func TestPaycrestQuote_RefusesNonDecimalAmounts(t *testing.T) {
	for _, amt := range []string{"NaN", "nan", "0x1p10", "1_000", "1e3"} {
		q := &stubQuoter{market: 1370.15, rates: map[string]*PaycrestRates{"base|USDC": sell("1360.05")}}
		s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})
		if res, err := s.Quote(context.Background(), "sell", "NGN", amt); err == nil {
			t.Errorf("Quote(%q) = rate %s token %v, want refused", amt, res.Best.Rate, res.Best.TokenAmount)
		}
	}
}

// Paycrest is an external system; a rate it sends is data, not truth.
func TestPaycrestQuote_IgnoresNonFiniteProviderRates(t *testing.T) {
	for _, r := range []string{"NaN", "Inf"} {
		q := &stubQuoter{market: 1370.15, rates: map[string]*PaycrestRates{"base|USDC": sell(r)}}
		s := NewPaycrestQuoteService(q, []string{"USDC"}, []string{"base"})
		s.SetSenderFeePercent("0.5")
		if res, err := s.Quote(context.Background(), "sell", "NGN", "800"); err == nil {
			t.Errorf("provider rate %q quoted as %s / token %v, want the venue dropped", r, res.Best.Rate, res.Best.TokenAmount)
		}
		if _, _, err := quotedRate("sell", r, 0.5); err == nil {
			t.Errorf("quotedRate(%q) accepted a non-finite rate", r)
		}
	}
}

func TestGrossDownForSenderFee_RefusesNonDecimalAmounts(t *testing.T) {
	for _, amt := range []string{"NaN", "Inf", "0x1p10"} {
		if got, applied := grossDownForSenderFee(amt, "0.5"); applied {
			t.Errorf("grossDownForSenderFee(%q) = %q, applied; want passed through", amt, got)
		}
	}
}

// The column is varchar(16) in Postgres. An odd value from Paycrest must not
// fail the insert after the order already exists upstream.
func TestPaycrestOrderService_StoresOnlyACanonicalChargedFee(t *testing.T) {
	cases := map[string]string{
		"0.50":                           "0.5",
		"garbage-value-longer-than-16ch": "0.5", // unusable: fall back to ours
	}
	for reported, want := range cases {
		pc := &capturingOrderer{rate: "1360.05", feePercent: reported}
		svc := newFeeTestService(t, "0.5", pc, "sell", "1360.05")
		logs := captureLog(t)
		row, err := svc.CreateSell(context.Background(), sellRequest())
		if err != nil {
			t.Fatalf("CreateSell with reported fee %q: %v", reported, err)
		}
		if row.SenderFeePercent != want {
			t.Errorf("reported %q stored as %q, want %q", reported, row.SenderFeePercent, want)
		}
		if reported == "0.50" && strings.Contains(logs.String(), "ALARM") {
			t.Errorf("0.50 equals 0.5; no alarm expected, got %s", logs.String())
		}
		if reported != "0.50" && !strings.Contains(logs.String(), "ALARM") {
			t.Errorf("unusable reported fee %q should alarm", reported)
		}
	}
}

// A typo in the fee address sends every fee somewhere nobody controls, and
// nothing on-chain would ever say so. Refuse it at boot, loudly.
func TestValidSenderFeeAddress(t *testing.T) {
	good := []string{
		"0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96", // checksummed (the live fee wallet)
		"0x62ba9b24e72e4f1f93f50f2308ace932066c1b96", // all lower: no checksum to verify
		" 0x62BA9B24E72E4F1F93F50F2308ACE932066C1B96 ",
	}
	for _, a := range good {
		if _, ok := validSenderFeeAddress(a); !ok {
			t.Errorf("validSenderFeeAddress(%q) refused a valid address", a)
		}
	}
	bad := []string{
		"0x62bA9b24e72E4f1F93f50F2308aCE932066c1b97", // one hex digit off, checksum breaks
		"0x62bA9b24e72E4f1F93f50F2308aCE932066c1b9",  // 39 hex digits
		"62bA9b24e72E4f1F93f50F2308aCE932066c1b96",   // no 0x
		"0x0000000000000000000000000000000000000000", // burns the fee
		"0xZZbA9b24e72E4f1F93f50F2308aCE932066c1b96",
		"not-an-address",
	}
	for _, a := range bad {
		if _, ok := validSenderFeeAddress(a); ok {
			t.Errorf("validSenderFeeAddress(%q) accepted an unusable address", a)
		}
	}
}

func TestNewPaycrestOrderService_RefusesABadFeeAddress(t *testing.T) {
	logs := captureLog(t)
	base, _ := newPaycrestOrderTestService(t)
	quotes := NewPaycrestQuoteService(&stubQuoter{}, nil, nil)
	svc := NewPaycrestOrderService(base.db, &capturingOrderer{}, quotes, base.dir, "0.5",
		"0x62bA9b24e72E4f1F93f50F2308aCE932066c1b97")
	if svc.senderFeeAddress != "" {
		t.Errorf("senderFeeAddress = %q, want refused", svc.senderFeeAddress)
	}
	if !strings.Contains(logs.String(), "PAYCREST_SENDER_FEE_ADDRESS") {
		t.Errorf("want a refusal logged, got %q", logs.String())
	}

	ok := NewPaycrestOrderService(base.db, &capturingOrderer{}, quotes, base.dir, "0.5",
		" 0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96 ")
	if ok.senderFeeAddress != "0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96" {
		t.Errorf("senderFeeAddress = %q, want the trimmed checksummed address", ok.senderFeeAddress)
	}
}
