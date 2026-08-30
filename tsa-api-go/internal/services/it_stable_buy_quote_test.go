package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"
)

// buyRateQuoter echoes a fixed NGN->asset probe: "to receive 1 USDC, pay 1650.25 NGN".
type buyRateQuoter struct {
	ngnPerUnit float64
	err        error
}

func (q *buyRateQuoter) QuoteRate(_ context.Context, src, dst string, destAmount float64) (FWRate, error) {
	if q.err != nil {
		return FWRate{}, q.err
	}
	return FWRate{
		Source:      FWMoney{Currency: src, Amount: q.ngnPerUnit * destAmount},
		Destination: FWMoney{Currency: dst, Amount: destAmount},
	}, nil
}

// Mirrors newStableSellTestService, but wires fx: checkRatePlausible skips
// entirely when s.fx is nil, so a service built as &ITService{} would make the
// implausible-rate test pass vacuously — worse than failing.
func newBuyQuoteTestService(t *testing.T, q FXRateQuoter) *ITService {
	t.Helper()
	s := NewITService(nil, nil, stubFx{ngn: 1650})
	s.SetRateQuoter(q)
	s.SetStableSpreadBps(155)
	s.SetStableRateLockTTL(15 * time.Minute)
	return s
}

func TestQuoteStableBuy_PricesAndAppliesSpread(t *testing.T) {
	s := newBuyQuoteTestService(t, &buyRateQuoter{ngnPerUnit: 1650.25})

	// 165,025.00 NGN = 16,502,500 kobo. At 1650.25 NGN/USDC that is exactly
	// 100 USDC gross; the 155bps spread leaves 98.45 USDC.
	q, err := s.QuoteStableBuy(context.Background(), "USDC", "16502500", "NGN")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := big.NewInt(100_000_000); q.GrossAssetUnits.Cmp(want) != 0 {
		t.Fatalf("gross = %s, want %s", q.GrossAssetUnits, want)
	}
	if want := big.NewInt(98_450_000); q.AssetUnits.Cmp(want) != 0 {
		t.Fatalf("net = %s, want %s", q.AssetUnits, want)
	}
	if q.AssetUnits.Cmp(q.GrossAssetUnits) >= 0 {
		t.Fatal("spread did not reduce what the user receives")
	}
}

func TestQuoteStableBuy_RejectsImplausibleRate(t *testing.T) {
	// The sandbox returned a rate wrong by ~six orders of magnitude. Pricing
	// must refuse rather than transact on it.
	s := newBuyQuoteTestService(t, &buyRateQuoter{ngnPerUnit: 2})
	_, err := s.QuoteStableBuy(context.Background(), "USDC", "16502500", "NGN")
	if !errors.Is(err, ErrStableRateImplausible) {
		t.Fatalf("err = %v, want ErrStableRateImplausible", err)
	}
}

func TestQuoteStableBuy_EnforcesDeliveryMinimum(t *testing.T) {
	// Flutterwave's floor is on the OUTBOUND leg, so it binds what the user
	// receives — after the spread, not the naira they pay.
	s := newBuyQuoteTestService(t, &buyRateQuoter{ngnPerUnit: 1650.25})
	_, err := s.QuoteStableBuy(context.Background(), "USDC", "825125", "NGN") // ~5 USDC
	if !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("err = %v, want ErrStableBuyBelowMinimum", err)
	}
}

// TestQuoteStableBuy_RejectedSpreadStillChargesMargin proves the fix for the
// zero-margin hole: a rejected SetStableSpreadBps call must not leave the
// service quoting at 0bps.
func TestQuoteStableBuy_RejectedSpreadStillChargesMargin(t *testing.T) {
	s := NewITService(nil, nil, stubFx{ngn: 1650})
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	s.SetStableRateLockTTL(15 * time.Minute)
	if err := s.SetStableSpreadBps(10000); err == nil {
		t.Fatal("spread of 10000bps accepted")
	}

	q, err := s.QuoteStableBuy(context.Background(), "USDC", "16502500", "NGN")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.AssetUnits.Cmp(q.GrossAssetUnits) >= 0 {
		t.Fatalf("net = %s, gross = %s: a rejected spread configuration must not fall back to zero margin",
			q.AssetUnits, q.GrossAssetUnits)
	}
}

func TestQuoteStableBuy_RejectsBadInput(t *testing.T) {
	s := newBuyQuoteTestService(t, &buyRateQuoter{ngnPerUnit: 1650.25})
	for _, tc := range []struct {
		name, asset, fiat string
		wantErr           error
	}{
		{"not an asset", "MCGP", "16502500", ErrStableNotAnAsset},
		{"zero fiat", "USDC", "0", ErrStableBuyBelowMinimum},
		{"negative fiat", "USDC", "-1", ErrStableBuyBelowMinimum},
		{"garbage fiat", "USDC", "abc", ErrStableBuyBelowMinimum},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.QuoteStableBuy(context.Background(), tc.asset, tc.fiat, "NGN"); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
