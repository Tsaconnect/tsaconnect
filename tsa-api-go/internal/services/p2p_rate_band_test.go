package services

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/google/uuid"
)

func TestCreateAd_FixedRateOutsideBandRefused(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db) // mockRates MidRate 1300; default band 2000 bps
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	seller := uuid.New()
	pm := seedPaymentMethod(t, db, seller, "NGN")
	_, addr := newSellerKey(t)
	p := makeCreateAdParams(pm)
	p.Rate = "1600"
	_, err := svc.CreateAd(context.Background(), seller, addr, p)
	var band *RateBandError
	if !errors.As(err, &band) || !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("err = %v", err)
	}
	if band.ReferenceRate != "1300" || band.MinRate != "1040" || band.MaxRate != "1560" || band.BandBps != 2000 {
		t.Fatalf("band = %+v", band)
	}
	p.Rate = "1560"
	if _, err := svc.CreateAd(context.Background(), seller, addr, p); err != nil {
		t.Fatalf("edge of band must pass: %v", err)
	}
}

func TestCreateBuyAd_FixedRateBandApplies(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	svc.SetFixedRateBandBps(1000)
	merchant := uuid.New()
	pm := seedPaymentMethod(t, db, merchant, "NGN")
	_, addr := newSellerKey(t)
	p := makeBuyAdParams(pm)
	p.Rate = "1100" // below 1170
	var band *RateBandError
	if _, err := svc.CreateAd(context.Background(), merchant, addr, p); !errors.As(err, &band) || band.MinRate != "1170" {
		t.Fatalf("err = %v", err)
	}
}

func TestFixedRateBand_SkipsFloatNonStableAndMissingReference(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	off := 0
	cases := map[string]CreateAdParams{
		"float":      {RateType: "float", FloatOffsetBps: &off, Token: "USDT", FiatCurrency: "NGN", Rate: "1"},
		"non-stable": {RateType: "fixed", Token: "ETH", FiatCurrency: "NGN", Rate: "1"},
		// mockRates has no GHS row: no anchor, so the ad is allowed (logged).
		"no-reference": {RateType: "fixed", Token: "USDT", FiatCurrency: "GHS", Rate: "1"},
	}
	for name, p := range cases {
		if err := svc.checkFixedRateBand(p); err != nil {
			t.Errorf("%s: want nil, got %v", name, err)
		}
	}
}

func TestSetFixedRateBandBps_NonPositiveFallsBackToDefault(t *testing.T) {
	svc := &P2PTradeService{}
	svc.SetFixedRateBandBps(-5)
	if svc.fixedRateBand() != defaultFixedRateBandBps {
		t.Fatalf("band = %d", svc.fixedRateBand())
	}
}
