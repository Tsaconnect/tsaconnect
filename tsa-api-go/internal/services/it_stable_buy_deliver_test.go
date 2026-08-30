package services

import (
	"context"
	"errors"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

type stubDeliverer struct {
	got   FWDirectTransferRequest
	id    string
	err   error
	calls int // asserted directly: checking only the DB would pass even if we sent twice
}

func (d *stubDeliverer) DirectTransfer(_ context.Context, req FWDirectTransferRequest) (FWTransfer, error) {
	d.calls++
	d.got = req
	if d.err != nil {
		return FWTransfer{}, d.err
	}
	return FWTransfer{ID: d.id, Status: "NEW"}, nil
}

func deliveringIntent(t *testing.T) (*ITService, *models.ITIntent) {
	t.Helper()
	s, userID := newStableBuyDBService(t)
	// Default: enough USDC to deliver. Tests that care about the balance gate
	// call SetStableConverter again with their own figure and override this.
	s.SetStableConverter(&stubConverter{
		balances: []FWBalance{{Currency: "USDC", AvailableBalance: 1000}},
	}, "merchant_1")
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(intent).Update("status", models.ITStatusDelivering)
	s.db.First(intent, "id = ?", intent.ID)
	return s, intent
}

func TestDeliverStableBuy_SendsCryptoToTheUsersAddress(t *testing.T) {
	s, intent := deliveringIntent(t)
	d := &stubDeliverer{id: "trf_deliver_1"}
	s.SetStableDeliverer(d)

	if err := s.DeliverStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if d.got.Type != "crypto" {
		t.Fatalf("type = %q, want crypto", d.got.Type)
	}
	c := d.got.PaymentInstruction.Recipient.Crypto
	if c == nil {
		t.Fatal("no crypto recipient set")
	}
	if c.Address != testBuyEVM {
		t.Fatalf("address = %q, want the user's own address", c.Address)
	}
	if c.Network != "POLYGON" {
		t.Fatalf("network = %q, want POLYGON", c.Network)
	}
	// The user is delivered what they bought, in the destination asset.
	if d.got.PaymentInstruction.DestinationCurrency != "USDC" {
		t.Fatalf("destination = %q, want USDC", d.got.PaymentInstruction.DestinationCurrency)
	}

	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.DeliveryID != "trf_deliver_1" {
		t.Fatalf("delivery id = %q", got.DeliveryID)
	}
	if got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %q, want delivering (accepted is not settled)", got.Status)
	}
}

func TestDeliverStableBuy_IsIdempotent(t *testing.T) {
	// A second call must not send a second transfer — that would deliver twice.
	s, intent := deliveringIntent(t)
	d := &stubDeliverer{id: "trf_deliver_2"}
	s.SetStableDeliverer(d)

	if err := s.DeliverStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("first: %v", err)
	}
	d.id = "trf_deliver_SHOULD_NOT_HAPPEN"
	if err := s.DeliverStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("second: %v", err)
	}
	// Assert the STUB, not just the DB: a DB-only check passes even if the
	// second DirectTransfer fired and its result was simply not written.
	if d.calls != 1 {
		t.Fatalf("DirectTransfer called %d times, want 1 — the user was paid twice", d.calls)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.DeliveryID != "trf_deliver_2" {
		t.Fatalf("delivery id = %q, want the first transfer", got.DeliveryID)
	}
}

func TestDeliverStableBuy_RefusesWithoutTheStablecoinInHand(t *testing.T) {
	// The conversion executes at its own rate minus an uncalibrated fee, so the
	// balance can come in under the preview-derived AssetAmount. Sending anyway
	// means a rejected transfer and a stuck intent.
	s, intent := deliveringIntent(t)
	s.SetStableConverter(&stubConverter{
		balances: []FWBalance{{Currency: "USDC", AvailableBalance: 1}}, // need ~98.45
	}, "merchant_1")
	d := &stubDeliverer{id: "trf_should_not_send"}
	s.SetStableDeliverer(d)

	err := s.DeliverStableBuy(context.Background(), intent.ID)
	if !errors.Is(err, ErrStableCreditNotVisible) {
		t.Fatalf("err = %v, want ErrStableCreditNotVisible", err)
	}
	if d.calls != 0 {
		t.Fatalf("sent %d transfers against an insufficient balance, want 0", d.calls)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.DeliveryAttempts != 0 {
		t.Fatal("burned a delivery attempt on a wait condition")
	}
}

func TestFinalizeBuyDelivery_OnlySuccessfulSettles(t *testing.T) {
	for _, tc := range []struct {
		name, fwStatus, wantStatus string
	}{
		{"successful", "SUCCESSFUL", models.ITStatusSettled},
		{"pending holds", "PENDING", models.ITStatusDelivering},
		{"failed retries", "FAILED", models.ITStatusDelivering},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, intent := deliveringIntent(t)
			s.db.Model(intent).Update("delivery_id", "trf_deliver_3")
			s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_deliver_3", Status: tc.fwStatus}})

			_ = s.FinalizeBuyDelivery(context.Background(), intent.ID)

			var got models.ITIntent
			s.db.First(&got, "id = ?", intent.ID)
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if tc.fwStatus == "FAILED" && got.DeliveryID != "" {
				t.Fatal("failed delivery kept its id; the retry would reuse the reference")
			}
		})
	}
}

func TestDeliverStableBuy_StopsAfterMaxAttempts(t *testing.T) {
	// Bounded retries. The intent stays non-terminal so it remains visible in
	// the stuck list — the money is ours to recover by hand.
	s, intent := deliveringIntent(t)
	s.db.Model(intent).Update("delivery_attempts", maxStableDeliveryAttempts)
	s.SetStableDeliverer(&stubDeliverer{id: "nope"})

	if err := s.DeliverStableBuy(context.Background(), intent.ID); !errors.Is(err, ErrStableDeliveryExhausted) {
		t.Fatalf("err = %v, want ErrStableDeliveryExhausted", err)
	}
}
