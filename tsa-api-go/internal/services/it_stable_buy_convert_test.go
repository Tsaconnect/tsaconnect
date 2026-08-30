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
