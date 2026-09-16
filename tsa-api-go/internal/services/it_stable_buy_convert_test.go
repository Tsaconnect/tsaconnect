package services

import (
	"context"
	"errors"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestConvertStableBuy_WaitsUntilTheNairaIsSpendable(t *testing.T) {
	// The brokerage guarantee: money moves only once Flutterwave shows it
	// actually holds spendable funds. A charge arriving is NOT that — VA
	// credits may sit in collection balance awaiting settlement.
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(intent).Updates(map[string]any{
		"status": models.ITStatusPaymentReceived, "received_amount": "16502500",
	})

	// Balance shows nothing spendable yet. There is no separate balances
	// dependency — stableConverter supplies both FetchBalances and
	// ConvertBalance, exactly as the sell leg uses it.
	s.SetStableConverter(&stubConverter{
		balances: []FWBalance{{Currency: "NGN", AvailableBalance: 0}},
	}, "merchant_1")

	err := s.ConvertStableBuy(context.Background(), intent.ID)
	if !errors.Is(err, ErrStableCreditNotVisible) {
		t.Fatalf("err = %v, want ErrStableCreditNotVisible", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusAwaitingFunds {
		t.Fatalf("status = %q, want awaiting_funds", got.Status)
	}
}

func TestConvertStableBuy_ConvertsOnceFundsAreSpendable(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(intent).Updates(map[string]any{
		"status": models.ITStatusPaymentReceived, "received_amount": "16502500",
	})
	conv := &stubConverter{
		balances: []FWBalance{{Currency: "NGN", AvailableBalance: 200000}},
		transfer: FWTransfer{ID: "trf_buy_1", Status: "NEW"},
	}
	s.SetStableConverter(conv, "merchant_1")

	if err := s.ConvertStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusConverting {
		t.Fatalf("status = %q, want converting", got.Status)
	}
	if got.ConversionID != "trf_buy_1" {
		t.Fatalf("conversion id = %q", got.ConversionID)
	}
	// Accepted is NOT settled: nothing may be delivered yet.
	if got.DeliveryID != "" {
		t.Fatal("delivery started against an unsettled conversion")
	}
}

func TestConvertStableBuy_ClaimStopsASecondConcurrentConvert(t *testing.T) {
	// The database CAS in ConvertStableBuy (mirroring ConvertAndQueuePayout's
	// s.claim) must be what stops a second tick from converting again, not
	// merely the DB ending up in the right place. Assert on the stub's call
	// count, not just status: a DB-only check would still pass even if a
	// second ConvertBalance fired and its result was simply overwritten.
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(intent).Updates(map[string]any{
		"status": models.ITStatusPaymentReceived, "received_amount": "16502500",
	})
	conv := &stubConverter{
		balances: []FWBalance{{Currency: "NGN", AvailableBalance: 200000}},
		transfer: FWTransfer{ID: "trf_buy_race", Status: "NEW"},
	}
	s.SetStableConverter(conv, "merchant_1")

	if err := s.ConvertStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("first convert: %v", err)
	}
	if err := s.ConvertStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("second (concurrent-style) convert: %v", err)
	}
	if len(conv.converted) != 1 {
		t.Fatalf("ConvertBalance called %d times, want 1 — a double conversion moves real money twice", len(conv.converted))
	}
}

func TestFinalizeBuyConversion_OnlySuccessfulAdvances(t *testing.T) {
	for _, tc := range []struct {
		name, fwStatus, wantStatus string
		wantErr                    error
	}{
		{"successful", "SUCCESSFUL", models.ITStatusDelivering, nil},
		{"still pending", "PENDING", models.ITStatusConverting, ErrStableConversionPending},
		{"new is not settled", "NEW", models.ITStatusConverting, ErrStableConversionPending},
		{"failed retries", "FAILED", models.ITStatusAwaitingFunds, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, userID := newStableBuyDBService(t)
			intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
			s.db.Model(intent).Updates(map[string]any{
				"status": models.ITStatusConverting, "conversion_id": "trf_buy_2",
			})
			s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_buy_2", Status: tc.fwStatus}})

			err := s.FinalizeBuyConversion(context.Background(), intent.ID)
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got models.ITIntent
			s.db.First(&got, "id = ?", intent.ID)
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tc.wantStatus)
			}
		})
	}
}

// C1 regression. The claim and the price used to be two writes with an HTTP
// round-trip between them, so anything that interrupted that window — a
// rolling deploy, a panic, a cancelled context — left the intent at
// payment_received with Repriced == false, still carrying the asset_amount it
// was quoted at CREATION. The watcher runs every 30s and got there before the
// webhook redelivery that was supposed to finish the job, converting the real
// received_amount while delivering the stale figure.
//
// This drives the whole machine — convert, finalize, deliver — and asserts on
// what actually reached Flutterwave's outbound transfer, because a DB-only
// check would pass on an intent that was repriced but delivered from a stale
// in-memory amount.
func TestStableBuy_UnpricedClaimIsNeverDeliveredOnCreationTimeTerms(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	intent, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Quoted 98.45 USDC at creation.
	if intent.AssetAmount != "98450000" {
		t.Fatalf("fixture drift: creation-time asset amount = %q, want 98450000", intent.AssetAmount)
	}

	// The interrupted claim: the charge landed and was recorded, but pricing
	// never ran. Only HALF the quoted naira actually arrived, so the honest
	// delivery is ~49.225 USDC — the creation-time 98.45 would be paid for out
	// of the pooled merchant balance, i.e. another user's stablecoin.
	if err := s.db.Model(intent).Updates(map[string]any{
		"status":          models.ITStatusPaymentReceived,
		"payment_ref":     "chg_unpriced",
		"received_amount": "8251250",
		"repriced":        false,
	}).Error; err != nil {
		t.Fatalf("simulate interrupted claim: %v", err)
	}

	conv := &stubConverter{
		balances: []FWBalance{
			{Currency: "NGN", AvailableBalance: 200000},
			// Comfortably enough for EITHER figure, so the balance gate is not
			// what makes this test pass — the reprice is.
			{Currency: "USDC", AvailableBalance: 1000},
		},
		transfer: FWTransfer{ID: "trf_unpriced", Status: "NEW"},
	}
	s.SetStableConverter(conv, "merchant_1")
	s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_unpriced", Status: "SUCCESSFUL"}})
	d := &stubDeliverer{id: "trf_unpriced_out"}
	s.SetStableDeliverer(d)

	ctx := context.Background()
	if err := s.ConvertStableBuy(ctx, intent.ID); err != nil {
		t.Fatalf("convert: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if !got.Repriced {
		t.Fatal("converted an intent that was never priced")
	}
	if got.AssetAmount == "98450000" {
		t.Fatal("delivery still carries the creation-time asset amount; the rate lock is back")
	}
	if got.AssetAmount != "49225000" {
		t.Fatalf("asset amount = %q, want 49225000 — priced on what actually landed", got.AssetAmount)
	}

	if err := s.FinalizeBuyConversion(ctx, intent.ID); err != nil {
		t.Fatalf("finalize conversion: %v", err)
	}
	if err := s.DeliverStableBuy(ctx, intent.ID); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got, want := d.got.PaymentInstruction.Amount.Value, 49.225; got != want {
		t.Fatalf("sent %v USDC, want %v — the outbound transfer used the stale amount", got, want)
	}
}

// C2 regression. priceStableBuy's write is status-guarded, so a price that
// lands after a watcher tick has already spent the naira cannot drag the
// intent backwards out of `converting` — which is what made the next tick
// re-enter ConvertStableBuy and convert a second time with the same reference.
func TestPriceStableBuy_WillNotDragAnIntentBackwardsOutOfConverting(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	intent, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// The watcher won the race: money already spent, conversion in flight.
	if err := s.db.Model(intent).Updates(map[string]any{
		"status":        models.ITStatusConverting,
		"conversion_id": "trf_inflight",
	}).Error; err != nil {
		t.Fatalf("simulate in-flight conversion: %v", err)
	}

	// The late price write still believes the intent is payment_received.
	intent.ReceivedAmount = "16502500"
	applied, err := s.priceStableBuy(context.Background(), intent, intent.ReceivedAmount,
		models.ITStatusPaymentReceived, models.ITStatusPaymentReceived)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied {
		t.Fatal("the late price write claimed to have applied")
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusConverting {
		t.Fatalf("status = %q, want converting — the intent was dragged backwards after the money was spent", got.Status)
	}
}
