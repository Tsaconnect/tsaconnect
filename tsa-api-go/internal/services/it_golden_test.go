package services

import (
	"context"
	"encoding/json"
	"flag"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── Characterization ("golden") baseline for the IT/PM money paths ──
//
// WHY THIS EXISTS. The multi-currency refactor repeatedly promises that NGN
// behaviour stays "byte-identical". Before this file there was nothing in the
// repo to compare against: the nearest thing, TestFlutterwaveGateway_NGNBankUnchanged,
// asserts six hand-picked keys and would not notice a NEW key appearing, an
// existing key vanishing, "1500.00" collapsing to "1500", or a field gaining
// omitempty. it_fx.go had no test file at all.
//
// These tests pin the WHOLE serialized shape and full numeric outputs, so any
// drift shows up as a diff rather than as silence.
//
// Regenerate deliberately, never reflexively:
//
//	go test ./internal/services -run TestGolden -update
//
// A diff here during the multi-currency work is a QUESTION, not a nuisance:
// either the change is intended (update the fixture in the same commit that
// justifies it) or it is the regression this file was written to catch.

var updateGolden = flag.Bool("update", false, "rewrite golden fixtures under testdata/golden")

// goldenAssert compares got against the fixture at testdata/golden/<name>.json.
func goldenAssert(t *testing.T, name string, got any) {
	t.Helper()

	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	encoded = append(encoded, '\n')

	path := filepath.Join("testdata", "golden", name+".json")

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden: %v", err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("golden updated: %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\n\nRun: go test ./internal/services -run TestGolden -update", path, err)
	}
	if string(want) != string(encoded) {
		t.Errorf("golden mismatch for %s\n\n--- want ---\n%s\n--- got ---\n%s", name, want, encoded)
	}
}

// TestGoldenPayoutBodyNGN pins the ENTIRE /direct-transfers request body for a
// legacy NGN→NGN bank payout — every key, every value, including the ones no
// hand-written assertion currently covers.
func TestGoldenPayoutBodyNGN(t *testing.T) {
	g, gotBody := fwCaptureTransferServer(t)

	if _, err := g.InitiatePayout(context.Background(), PayoutRequest{
		BankCode:      "044",
		AccountNumber: "0690000031",
		AmountNGN:     "1500.00",
		Reference:     "trade-ref-123456",
		AccountName:   "Ada Obi",
	}); err != nil {
		t.Fatalf("payout: %v", err)
	}

	goldenAssert(t, "payout_body_ngn", *gotBody)
}

// goldenFeeRow is one priced IT withdrawal leg. Amounts are decimal strings so
// the fixture stays readable and a big.Int formatting change is visible.
type goldenFeeRow struct {
	GrossKobo    string `json:"grossKobo"`
	FeeKobo      string `json:"feeKobo"`
	NetKobo      string `json:"netKobo"`
	PlatformKept string `json:"platformKeptKobo"`
}

// TestGoldenITFeesNGN pins the fee engine across the range that matters: the
// PM's own receipt value, both sides of the ₦100 Flutterwave floor, and values
// chosen so rounding cannot be flattered by round numbers.
func TestGoldenITFeesNGN(t *testing.T) {
	s := newITTestService(t)
	s.SetFeeQuoter(feeQuoterForTest())

	grosses := []int64{
		9_999,   // ₦99.99  — just under the ₦100 floor
		10_000,  // ₦100.00 — exactly the floor
		10_001,  // ₦100.01 — just over
		13_785,  // the PM's quoted receipt (5 MCGP @ ₦27.57)
		13_787,  // awkward rounding neighbour
		50_000,  // ₦500
		99_999,  // ₦999.99
		100_000, // ₦1,000
		333_333, // non-round, exercises every bps division
		1_000_000,
	}

	rows := make([]goldenFeeRow, 0, len(grosses))
	for _, g := range grosses {
		br, err := s.quoteITFees(context.Background(), FeeFlowWithdraw, big.NewInt(g), "NGN")
		if err != nil {
			t.Fatalf("quote %d: %v", g, err)
		}
		rows = append(rows, goldenFeeRow{
			GrossKobo:    br.Gross.String(),
			FeeKobo:      br.Fee.String(),
			NetKobo:      br.Net.String(),
			PlatformKept: br.PlatformKept.String(),
		})
	}

	goldenAssert(t, "it_fees_ngn_withdraw", rows)
}

// goldenConvRow pins one MCGP↔NGN conversion in both directions. The round-down
// behaviour is only spot-checked elsewhere; here it is pinned across a table.
type goldenConvRow struct {
	NgnKobo   string `json:"ngnKobo"`
	RateKobo  string `json:"rateKobo"`
	McgpWei   string `json:"mcgpWei"`
	BackToNgn string `json:"backToNgnKobo"`
}

// TestGoldenMcgpNgnConversions pins mcgpForNgn / ngnForMcgp, including the
// asymmetry introduced by integer round-down on the return leg.
func TestGoldenMcgpNgnConversions(t *testing.T) {
	cases := []struct{ ngnKobo, rateKobo int64 }{
		{10_000, 2_757},   // ₦100 at ₦27.57
		{13_785, 2_757},   // exactly 5 MCGP
		{13_786, 2_757},   // one kobo more — must round down
		{1, 2_757},        // dust
		{999_999, 2_757},  // large, non-round
		{10_000, 1},       // degenerate cheap rate
		{10_000, 100_000}, // degenerate expensive rate
		{123_456, 7_919},  // both prime-ish, worst case for rounding
	}

	rows := make([]goldenConvRow, 0, len(cases))
	for _, c := range cases {
		ngn := big.NewInt(c.ngnKobo)
		rate := big.NewInt(c.rateKobo)
		wei := mcgpForNgn(ngn, rate)
		back := ngnForMcgp(wei, rate)
		rows = append(rows, goldenConvRow{
			NgnKobo:   ngn.String(),
			RateKobo:  rate.String(),
			McgpWei:   wei.String(),
			BackToNgn: back.String(),
		})
	}

	goldenAssert(t, "mcgp_ngn_conversions", rows)
}

// goldenUsdRow pins the kobo→USD conversion the fee carve depends on.
type goldenUsdRow struct {
	Kobo      string  `json:"kobo"`
	NgnPerUsd float64 `json:"ngnPerUsd"`
	USD       float64 `json:"usd"`
}

// TestGoldenKoboToUSD pins the conversion that decides how much cashback and
// how many TradePoints a trade is worth. A silent change here moves real value.
func TestGoldenKoboToUSD(t *testing.T) {
	rates := []float64{1_000, 1_388.72, 1_500.5, 1_650}
	kobos := []int64{1, 985, 13_785, 100_000, 5_000_000}

	rows := make([]goldenUsdRow, 0, len(rates)*len(kobos))
	for _, r := range rates {
		for _, k := range kobos {
			rows = append(rows, goldenUsdRow{
				Kobo:      big.NewInt(k).String(),
				NgnPerUsd: r,
				USD:       koboToUSD(big.NewInt(k), r),
			})
		}
	}

	goldenAssert(t, "kobo_to_usd", rows)
}

// TestGoldenFxProviderNgnPerUsd covers p2pFxProvider, which had NO test file at
// all before this — despite being the single source of the NGN/USD rate every
// IT price and every fee carve is derived from.
//
// The snapshot is injected into the cache directly (the pattern
// p2p_service_test.go already uses) so the assertion is about the adapter's
// mapping, not about upstream availability.
func TestGoldenFxProviderNgnPerUsd(t *testing.T) {
	type fxCase struct {
		Name      string  `json:"name"`
		NgnPerUsd float64 `json:"ngnPerUsd"`
		Stale     bool    `json:"stale"`
		Err       string  `json:"err"`
	}

	build := func(rates map[string]P2PRate, age time.Duration) FxProvider {
		svc := NewP2PService("test-app-id", []string{"NGN", "USD"}, []string{"NGN"}, nil)
		svc.cache.Store("all", &p2pCacheEntry{
			rates:     rates,
			cachedAt:  time.Now().Add(-age),
			expiresAt: time.Now().Add(-age + time.Hour),
		})
		return NewP2PFxProvider(svc)
	}

	run := func(name string, fx FxProvider) fxCase {
		rate, stale, err := fx.NgnPerUsd(context.Background())
		c := fxCase{Name: name, NgnPerUsd: rate, Stale: stale}
		if err != nil {
			c.Err = err.Error()
		}
		return c
	}

	rows := []fxCase{
		run("fresh", build(map[string]P2PRate{
			"NGN": {Currency: "NGN", MidRate: 1_388.72},
		}, 0)),
		run("row_marked_stale", build(map[string]P2PRate{
			"NGN": {Currency: "NGN", MidRate: 1_400, Stale: true},
		}, 0)),
		run("ngn_absent", build(map[string]P2PRate{
			"USD": {Currency: "USD", MidRate: 1},
		}, 0)),
		run("ngn_zero_rate", build(map[string]P2PRate{
			"NGN": {Currency: "NGN", MidRate: 0},
		}, 0)),
	}

	goldenAssert(t, "fx_provider_ngn_per_usd", rows)
}
