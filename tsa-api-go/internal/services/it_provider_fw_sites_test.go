package services

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// seedProviderIntent is seedTreasuryIntent with the provider and status chosen
// by the caller: a routed USDC buy on Base, ₦10,000 → 7.27815 USDC.
func seedProviderIntent(t *testing.T, s *ITService, userID uuid.UUID, provider, status string) *models.ITIntent {
	t.Helper()
	until := time.Now().Add(15 * time.Minute)
	i := &models.ITIntent{
		ID: uuid.New(), UserID: userID, Direction: models.ITDirectionBuy, Status: status,
		Provider: provider, Asset: models.ITAssetUSDC, AssetAmount: "7278150",
		Currency: "NGN", DestChain: "BASE", DestAddress: testRoutedEVM,
		LockedRate: "137398", DisplayRate: "135474", RateExpiresAt: &until,
		NgnKobo: "1000000", AmountMinor: "1000000", RateKobo: "137398", RateMinor: "137398",
		McgpAmount: "0", FeeNgnKobo: "0", NetNgnKobo: "1000000", PlatformKeptKobo: "0", ReceivedAmount: "1000000",
		Repriced: true,
	}
	if err := s.db.Create(i).Error; err != nil {
		t.Fatalf("seed %s intent: %v", provider, err)
	}
	return i
}

// countingTransfers proves a Finalize step never asked Flutterwave about a
// transfer it does not own.
type countingTransfers struct{ calls int }

func (c *countingTransfers) GetTransfer(context.Context, string) (FWTransfer, error) {
	c.calls++
	return FWTransfer{ID: "trf_x", Status: "SUCCESSFUL"}, nil
}

func TestFWPath_RefusesPoolIntent_Convert(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	conv := &stubConverter{}
	s.SetStableConverter(conv, "merchant_fixture")
	intent := seedProviderIntent(t, s, userID, "tsa_pool", models.ITStatusPaymentReceived)
	if err := s.ConvertStableBuy(context.Background(), intent.ID); !errors.Is(err, ErrUnknownITProvider) {
		t.Fatalf("err = %v, want ErrUnknownITProvider", err)
	}
	if len(conv.converted) != 0 {
		t.Fatalf("converted %d times at Flutterwave, want 0", len(conv.converted))
	}
}

func TestFWPath_RefusesPoolIntent_Deliver(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	d := &stubDeliverer{id: "trf_never"}
	s.SetStableDeliverer(d)
	intent := seedProviderIntent(t, s, userID, "tsa_pool", models.ITStatusDelivering)
	if err := s.DeliverStableBuy(context.Background(), intent.ID); !errors.Is(err, ErrUnknownITProvider) {
		t.Fatalf("err = %v, want ErrUnknownITProvider", err)
	}
	if d.calls != 0 {
		t.Fatalf("Flutterwave sent %d times, want 0", d.calls)
	}
}

func TestFWPath_RefusesPoolIntent_Pending(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	pool := seedProviderIntent(t, s, userID, "tsa_pool", models.ITStatusPaymentReceived)
	received, awaiting, converting, delivering, err := s.PendingStableBuys(context.Background())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if slices.Contains(slices.Concat(received, awaiting, converting, delivering), pool.ID) {
		t.Fatal("a pool intent is listed for the Flutterwave watcher")
	}
}

func TestFWPath_RefusesPoolIntent_FinalizeConversion(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	ct := &countingTransfers{}
	s.SetStableTransfers(ct)
	intent := seedProviderIntent(t, s, userID, "tsa_pool", models.ITStatusConverting)
	s.db.Model(intent).Update("conversion_id", "trf_c")
	if err := s.FinalizeBuyConversion(context.Background(), intent.ID); !errors.Is(err, ErrUnknownITProvider) {
		t.Fatalf("err = %v, want ErrUnknownITProvider", err)
	}
	if ct.calls != 0 {
		t.Fatalf("asked Flutterwave %d times, want 0", ct.calls)
	}
}

func TestFWPath_RefusesPoolIntent_FinalizeDelivery(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	ct := &countingTransfers{}
	s.SetStableTransfers(ct)
	intent := seedProviderIntent(t, s, userID, "tsa_pool", models.ITStatusDelivering)
	s.db.Model(intent).Update("delivery_id", "trf_d")
	if err := s.FinalizeBuyDelivery(context.Background(), intent.ID); !errors.Is(err, ErrUnknownITProvider) {
		t.Fatalf("err = %v, want ErrUnknownITProvider", err)
	}
	if ct.calls != 0 {
		t.Fatalf("asked Flutterwave %d times, want 0", ct.calls)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s, want it untouched", got.Status)
	}
}

// Review Focus 1: the IN list must keep the legacy "" rows.
func TestPendingStableBuys_LegacyEmptyProviderStillListed(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	legacy := seedProviderIntent(t, s, userID, "", models.ITStatusPaymentReceived)
	fw := seedProviderIntent(t, s, userID, models.ITProviderFlutterwave, models.ITStatusDelivering)
	cw := seedProviderIntent(t, s, userID, models.ITProviderTreasury, models.ITStatusPaymentReceived)
	received, _, _, delivering, err := s.PendingStableBuys(context.Background())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if !slices.Contains(received, legacy.ID) || !slices.Contains(delivering, fw.ID) || slices.Contains(received, cw.ID) {
		t.Fatalf("received = %v delivering = %v; want legacy + flutterwave, never the company wallet", received, delivering)
	}
}

// Review Focus 3: the text the console shows is unchanged.
func TestConvertStableBuy_CompanyWalletRefusalTextUnchanged(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	intent := seedProviderIntent(t, s, userID, models.ITProviderTreasury, models.ITStatusPaymentReceived)
	err := s.ConvertStableBuy(context.Background(), intent.ID)
	if err == nil || err.Error() != "invalid IT intent state: a company-wallet buy is never converted at Flutterwave" {
		t.Fatalf("err = %v", err)
	}
	err = s.DeliverStableBuy(context.Background(), intent.ID)
	if err == nil || err.Error() != "invalid IT intent state: a company-wallet buy is never sent by Flutterwave" {
		t.Fatalf("err = %v", err)
	}
}
