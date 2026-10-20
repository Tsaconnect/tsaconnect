package services

import (
	"context"
	"errors"
	"testing"
)

type officialFx struct {
	rate  float64
	stale bool
	err   error
}

func (o officialFx) NgnPerUsd(context.Context) (float64, bool, error) {
	return 0, false, errors.New("n/a")
}
func (o officialFx) OfficialRatePerUsd(context.Context, string) (float64, bool, error) {
	return o.rate, o.stale, o.err
}

// A currency with no market is priced from the official feed; stale, missing
// or unsupported answers are refused rather than guessed.
func TestOfficialFxReference(t *testing.T) {
	ctx := context.Background()
	if r, ok := (&ITService{fx: officialFx{rate: 0.92}}).OfficialFxReference(ctx, "eur"); !ok || r.FloatString(2) != "0.92" {
		t.Fatalf("fresh official rate: %v %v", r, ok)
	}
	if _, ok := (&ITService{fx: officialFx{rate: 0.92, stale: true}}).OfficialFxReference(ctx, "EUR"); ok {
		t.Fatal("a stale rate must be refused")
	}
	if _, ok := (&ITService{fx: officialFx{err: errors.New("down")}}).OfficialFxReference(ctx, "EUR"); ok {
		t.Fatal("an error must be refused")
	}
	if _, ok := (&ITService{fx: officialFx{rate: 0}}).OfficialFxReference(ctx, "EUR"); ok {
		t.Fatal("a zero rate must be refused")
	}
	if _, ok := (&ITService{}).OfficialFxReference(ctx, "EUR"); ok {
		t.Fatal("no provider must be refused")
	}
}
