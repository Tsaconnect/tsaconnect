package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Production, 2026-09-09: intent 898ce9ef failed conversion ten times. GET
// /transfers/{id} said why — provider_response code 0302, "Minimum: USDC
// 5.0" — and the decoder threw it away.
func TestFWTransfer_DecodesProviderResponse(t *testing.T) {
	var tr FWTransfer
	body := `{"id":"trf_1","status":"FAILED","provider_response":{"code":"0302","type":"transfer_amount_below_limit","message":"Minimum: USDC 5.0"}}`
	if err := json.Unmarshal([]byte(body), &tr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	pr := tr.ProviderResponse
	if pr.Code != "0302" || pr.Type != "transfer_amount_below_limit" || pr.Message != "Minimum: USDC 5.0" {
		t.Fatalf("provider response = %+v", pr)
	}
	if !pr.BelowLimit() {
		t.Fatal("0302 must read as below the provider limit")
	}
}

// Review Focus 3: the field is undocumented, so no shape of it may break a
// transfer read — sells and buys both poll through GetTransfer.
func TestFWTransfer_ProviderResponseNeverBreaksDecoding(t *testing.T) {
	for _, body := range []string{
		`{"id":"t","status":"FAILED","provider_response":"insufficient balance"}`,
		`{"id":"t","status":"FAILED","provider_response":null}`,
		`{"id":"t","status":"FAILED","provider_response":{"code":302,"message":"Minimum: USDC 5.0"}}`,
		`{"id":"t","status":"FAILED","provider_response":[1,2]}`,
		`{"id":"t","status":"FAILED"}`,
	} {
		var tr FWTransfer
		if err := json.Unmarshal([]byte(body), &tr); err != nil || tr.Status != "FAILED" || tr.ID != "t" {
			t.Fatalf("%s: err=%v transfer=%+v", body, err, tr)
		}
	}
	var numeric FWTransfer
	if err := json.Unmarshal([]byte(`{"id":"t","provider_response":{"code":302}}`), &numeric); err != nil {
		t.Fatalf("numeric code: %v", err)
	}
	if !numeric.ProviderResponse.BelowLimit() {
		t.Fatal("a numeric 302 is the same below-limit code")
	}
	var text FWTransfer
	if err := json.Unmarshal([]byte(`{"id":"t","provider_response":"insufficient balance"}`), &text); err != nil {
		t.Fatalf("string response: %v", err)
	}
	if text.ProviderResponse.Message != "insufficient balance" {
		t.Fatalf("a bare string is kept as the message: %+v", text.ProviderResponse)
	}
}

// convertingIntent is an old-path buy whose conversion is in flight.
func convertingIntent(t *testing.T, conversionID string) (*ITService, *models.ITIntent) {
	t.Helper()
	s, userID := newStableBuyDBService(t)
	intent, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.db.Model(intent).Updates(map[string]any{
		"status": models.ITStatusConverting, "conversion_id": conversionID,
	}).Error; err != nil {
		t.Fatalf("to converting: %v", err)
	}
	return s, intent
}

func TestFinalizeBuyConversion_BelowProviderLimitParksWithTheReason(t *testing.T) {
	s, intent := convertingIntent(t, "trf_0302")
	s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_0302", Status: "FAILED",
		ProviderResponse: FWProviderResponse{Code: "0302", Type: "transfer_amount_below_limit", Message: "Minimum: USDC 5.0"}}})
	logs := captureLog(t)

	if err := s.FinalizeBuyConversion(context.Background(), intent.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("status = %q, want buy_awaiting_rate — the same amount fails every time", got.Status)
	}
	if !strings.Contains(got.FailureReason, "code=0302") || !strings.Contains(got.FailureReason, `msg="Minimum: USDC 5.0"`) {
		t.Fatalf("failure_reason = %q, want the provider's code and message", got.FailureReason)
	}
	if got.ConversionAttempts != 1 {
		t.Fatalf("conversion_attempts = %d, want 1 — the parked reference is spent (I7)", got.ConversionAttempts)
	}
	out := logs.String()
	if !strings.Contains(out, "[it stable buy] conversion failed intent="+intent.ID.String()) || !strings.Contains(out, "code=0302") {
		t.Fatalf("log = %q", out)
	}
	received, awaiting, converting, delivering, err := s.PendingStableBuys(context.Background())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	for _, ids := range [][]uuid.UUID{received, awaiting, converting, delivering} {
		for _, id := range ids {
			if id == intent.ID {
				t.Fatal("the watcher would pick a parked intent up again")
			}
		}
	}
}

// I7: a 0302 park spends its reference, so an operator's resume
// (RepriceParkedStableBuy) converts under a fresh one instead of replaying
// Flutterwave's cached FAILED transfer.
func TestFinalizeBuyConversion_ResumeAfterA0302ParkUsesAFreshReference(t *testing.T) {
	s, intent := convertingIntent(t, "trf_0302")
	if err := s.db.Model(intent).Update("received_amount", "16502500").Error; err != nil {
		t.Fatalf("seed received: %v", err)
	}
	s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_0302", Status: "FAILED",
		ProviderResponse: FWProviderResponse{Code: "0302", Message: "Minimum: USDC 5.0"}}})
	ctx := context.Background()
	if err := s.FinalizeBuyConversion(ctx, intent.ID); err != nil {
		t.Fatalf("park: %v", err)
	}
	if _, err := s.RepriceParkedStableBuy(ctx, intent.ID); err != nil {
		t.Fatalf("operator resume: %v", err)
	}
	conv := &stubConverter{
		balances: []FWBalance{{Currency: "NGN", AvailableBalance: 200000}},
		transfer: FWTransfer{ID: "trf_retry", Status: "NEW"},
	}
	s.SetStableConverter(conv, "merchant_1")
	if err := s.ConvertStableBuy(ctx, intent.ID); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(conv.converted) != 1 || conv.converted[0].ref != itBuyConversionRef(intent.ID, 1) {
		t.Fatalf("converted = %+v, want one conversion under %s", conv.converted, itBuyConversionRef(intent.ID, 1))
	}
}

func TestFinalizeBuyConversion_OtherFailuresStillRetryWithTheReason(t *testing.T) {
	s, intent := convertingIntent(t, "trf_x")
	s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_x", Status: "FAILED",
		ProviderResponse: FWProviderResponse{Code: "0500", Message: "Service unavailable"}}})

	if err := s.FinalizeBuyConversion(context.Background(), intent.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusAwaitingFunds || got.ConversionAttempts != 1 {
		t.Fatalf("status=%q attempts=%d, want awaiting_funds/1 — a transient failure retries", got.Status, got.ConversionAttempts)
	}
	if !strings.Contains(got.FailureReason, "code=0500") {
		t.Fatalf("failure_reason = %q", got.FailureReason)
	}
}

func TestDeliverStableBuy_ShortFloatIsLoggedAndMarked(t *testing.T) {
	s, intent := deliveringIntent(t)
	s.SetStableConverter(&stubConverter{balances: []FWBalance{{Currency: "USDC", AvailableBalance: 1}}}, "merchant_1")
	logs := captureLog(t)

	err := s.DeliverStableBuy(context.Background(), intent.ID)
	if !errors.Is(err, ErrStableFloatShort) || !errors.Is(err, ErrStableCreditNotVisible) {
		t.Fatalf("err = %v, want ErrStableFloatShort and still ErrStableCreditNotVisible", err)
	}
	if !strings.Contains(logs.String(), "ramp_fw_float_short intent="+intent.ID.String()) {
		t.Fatalf("log = %q", logs.String())
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusDelivering || got.DeliveryAttempts != 0 {
		t.Fatalf("status=%q attempts=%d, want delivering/0 — waiting on a top-up burns nothing", got.Status, got.DeliveryAttempts)
	}
}

func TestFinalizeBuyDelivery_BelowProviderLimitStopsRetrying(t *testing.T) {
	s, intent := deliveringIntent(t)
	if err := s.db.Model(intent).Update("delivery_id", "trf_out_0302").Error; err != nil {
		t.Fatalf("seed delivery id: %v", err)
	}
	s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_out_0302", Status: "FAILED",
		ProviderResponse: FWProviderResponse{Code: "0302", Message: "Minimum: USDC 5.0"}}})

	if err := s.FinalizeBuyDelivery(context.Background(), intent.ID); err != nil {
		t.Fatalf("finalize delivery: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusDelivering || got.DeliveryID != "" || got.DeliveryAttempts != maxStableDeliveryAttempts {
		t.Fatalf("status=%q id=%q attempts=%d, want delivering, cleared, at the cap", got.Status, got.DeliveryID, got.DeliveryAttempts)
	}
	if !strings.Contains(got.FailureReason, "code=0302") {
		t.Fatalf("failure_reason = %q", got.FailureReason)
	}
	if err := s.DeliverStableBuy(context.Background(), intent.ID); !errors.Is(err, ErrStableDeliveryExhausted) {
		t.Fatalf("redeliver err = %v, want ErrStableDeliveryExhausted (parked for an operator)", err)
	}
}
