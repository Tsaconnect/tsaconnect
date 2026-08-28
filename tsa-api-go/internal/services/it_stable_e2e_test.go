package services

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// TestStableSell_EndToEnd walks one sell from quote to payout-ready and asserts
// the property the whole design rests on: the platform never advances money.
// Every step is gated on the user's own funds being provably present, and the
// amount paid out is the amount that actually arrived — not the amount quoted.
//
// The conversion leg is asynchronous (ConvertAndQueuePayout stops at
// `converting`; FinalizeConversion promotes to `debited` only once Flutterwave
// reports the stablecoin→fiat conversion actually SETTLED — see
// it_stable_sell.go), so this test drives both steps rather than expecting
// ConvertAndQueuePayout alone to reach `debited`.
func TestStableSell_EndToEnd(t *testing.T) {
	s := newStableSellDBService(t)
	s.SetStableSpreadBps(155)
	s.SetStableRateLockTTL(15 * time.Minute)
	s.SetRateQuoter(&stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1650.25},
	}})
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	s.SetStableVerifier(
		newStubLiFi(t, "DONE", "0xdest", "99884217"),
		&stubVerifier{amount: big.NewInt(99884217)},
		map[string]string{"USDC": testToken},
	)
	conv := &stubConverter{
		balances: []FWBalance{{Currency: "USDC", AvailableBalance: 99.884217}},
		transfer: FWTransfer{ID: "trf_1", Status: "NEW"},
	}
	s.SetStableConverter(conv, "mrc_test")
	// ConvertAndQueuePayout only ever sees the conversion as accepted (NEW);
	// FinalizeConversion is what learns it settled. Wire that read
	// independently, as SetStableConverter's doc comment says tests should.
	s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_1", Status: "SUCCESSFUL"}})
	gw := &fakeNombaGateway{payoutID: "pay_stable_1"}
	s.SetPayoutGateway(gw)

	ctx := context.Background()

	q, err := s.QuoteStableSell(ctx, "USDC", "100", "NGN")
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	intent, deposit, err := s.CreateStableSellIntent(ctx, uuid.New(), q, "BSC", testSellBank())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if deposit.Address != testFWAddr {
		t.Fatalf("deposit address = %q, want Flutterwave's", deposit.Address)
	}
	// ReportStableDeposit is scoped to the intent's owner (IDOR guard) — the
	// security fix in e2d2a98 added the userID parameter.
	if err := s.ReportStableDeposit(ctx, intent.UserID, intent.ID, "0xsource"); err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := s.ConfirmStableDeposit(ctx, intent.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if err := s.ConvertAndQueuePayout(ctx, intent.ID); err != nil {
		t.Fatalf("convert: %v", err)
	}

	mid, _ := s.GetIntent(ctx, intent.ID)
	if mid.Status != models.ITStatusConverting {
		t.Fatalf("status after convert = %q, want converting (conversion is asynchronous)", mid.Status)
	}

	if err := s.FinalizeConversion(ctx, intent.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	// `debited` is NOT the finish line — it is where the fiat exists and has
	// not yet left for the user. Asserting the status alone is what let this
	// ship with no payout wired at all, so drive the last hop and assert the
	// gateway was actually asked for money.
	//
	// Reached the way production reaches it: the watcher's debited bucket. If
	// PendingStableSells stops returning these, this test fails here rather
	// than silently exercising a path nothing calls.
	_, _, _, debited, pErr := s.PendingStableSells(ctx)
	if pErr != nil {
		t.Fatalf("PendingStableSells: %v", pErr)
	}
	if len(debited) != 1 || debited[0] != intent.ID {
		t.Fatalf("settled sell is not in the watcher's payout bucket: %v", debited)
	}
	if err := s.PayoutStableSell(ctx, intent.ID); err != nil {
		t.Fatalf("payout: %v", err)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("payout gateway received %d requests, want exactly 1 — the user was never paid", len(gw.calls))
	}
	if gw.calls[0].AccountNumber != testSellBank().Number {
		t.Fatalf("payout went to %q, want the user's account", gw.calls[0].AccountNumber)
	}

	got, _ := s.GetIntent(ctx, intent.ID)
	if got.Status != models.ITStatusDebited {
		t.Fatalf("status = %q, want debited", got.Status)
	}
	if got.PayoutID != "pay_stable_1" {
		t.Fatalf("payoutId = %q, want the gateway's id recorded on the intent", got.PayoutID)
	}

	// Re-running the watcher step must not pay twice. This is what makes the
	// polling model safe: a redelivered tick is free.
	if err := s.PayoutStableSell(ctx, intent.ID); err != nil {
		t.Fatalf("second payout call: %v", err)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("payout fired %d times — a redelivered tick paid the user twice", len(gw.calls))
	}

	// The payout follows the RECEIVED amount, not the 100 USDC quoted. A
	// bridge delivers less than it was given (99.884217 of 100 USDC here);
	// the payout must scale down proportionally rather than the platform
	// absorbing the shortfall.
	want := new(big.Int).Quo(
		new(big.Int).Mul(q.NetMinor, big.NewInt(99884217)),
		q.AssetAmount,
	)
	if got.NetNgnKobo != want.String() {
		t.Fatalf("net = %q, want %s", got.NetNgnKobo, want)
	}
	if got.NetNgnKobo == q.NetMinor.String() {
		t.Fatal("payout matched the QUOTED net — the bridge shortfall was absorbed by the platform")
	}
	// And the money that actually left follows that same received-scaled net,
	// not the quote.
	wantMajor, err := koboToNairaString(want.String())
	if err != nil {
		t.Fatalf("koboToNairaString: %v", err)
	}
	if gw.calls[0].Amount != wantMajor {
		t.Fatalf("payout amount = %q, want %q (the received-scaled net)", gw.calls[0].Amount, wantMajor)
	}
}
