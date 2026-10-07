package services

import (
	"errors"
	"fmt"
)

// MaxPayAmountKobo bounds a payment so the kobo arithmetic below cannot
// overflow int64 (1e12 × 1e6 < 9.2e18).
const MaxPayAmountKobo int64 = 1_000_000_000_000

var ErrPayAmountInvalid = errors.New("amount must be a positive whole number of kobo")

// PaySplit is one payment's money, in kobo.
type PaySplit struct {
	CustomerTotalKobo int64 // what the customer's deposit is worth (an estimate on screen; the order sizes it)
	MerchantNetKobo   int64 // exactly what the merchant receives
	FeeKobo           int64 // customer-facing fee; not our revenue on converted routes
}

// PayAmounts splits a merchant's price P by the platform fee F (bps) and the
// share of it the customer bears (0–100%).
//
// Paycrest takes a sell's fee out of the deposit, so a deposit worth C pays
// out C × (1 − F). The merchant's net is fixed first — the full price when the
// customer bears the fee — and C is what it takes to deliver it:
//
//	N = P × (1 − F × (1 − share))      C = ceil(N / (1 − F))
func PayAmounts(amountKobo int64, feeBps, customerSharePct int) (PaySplit, error) {
	if amountKobo <= 0 || amountKobo > MaxPayAmountKobo {
		return PaySplit{}, ErrPayAmountInvalid
	}
	if feeBps < 0 || feeBps >= 10_000 || customerSharePct < 0 || customerSharePct > 100 {
		return PaySplit{}, fmt.Errorf("pay: fee %d bps / customer share %d%% out of range", feeBps, customerSharePct)
	}
	n := amountKobo * int64(1_000_000-feeBps*(100-customerSharePct)) / 1_000_000
	den := int64(10_000 - feeBps)
	c := (n*10_000 + den - 1) / den
	return PaySplit{CustomerTotalKobo: c, MerchantNetKobo: n, FeeKobo: c - n}, nil
}

// PayFeeBps reads TSA_PAY_FEE_PERCENT the way a payment's payout order does.
func PayFeeBps(pct string) (int, bool) { return feePercentToBps(pct) }
