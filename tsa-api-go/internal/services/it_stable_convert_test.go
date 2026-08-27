package services

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

type stubTransfers struct {
	tr  FWTransfer
	err error
}

func (s *stubTransfers) GetTransfer(context.Context, string) (FWTransfer, error) {
	return s.tr, s.err
}

type stubConverter struct {
	balances  []FWBalance
	transfer  FWTransfer
	convErr   error
	balErr    error
	converted []struct{ src, dst string }
}

func (s *stubConverter) FetchBalances(_ context.Context) ([]FWBalance, error) {
	return s.balances, s.balErr
}

func (s *stubConverter) ConvertBalance(_ context.Context, _, src, dst string, _ FWAmount, _ string) (FWTransfer, error) {
	s.converted = append(s.converted, struct{ src, dst string }{src, dst})
	return s.transfer, s.convErr
}

func confirmedSellIntent(t *testing.T, s *ITService) *models.ITIntent {
	t.Helper()
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	s.SetStableVerifier(
		newStubLiFi(t, "DONE", "0xdest", "99884217"),
		&stubVerifier{amount: big.NewInt(99884217)},
		map[string]string{"USDC": testToken},
	)
	intent, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "BSC", testSellBank())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.ReportStableDeposit(context.Background(), intent.ID, "0xsource"); err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := s.ConfirmStableDeposit(context.Background(), intent.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	got, _ := s.GetIntent(context.Background(), intent.ID)
	return got
}

func TestConvertAndQueuePayout_WaitsUntilFlutterwaveShowsTheCredit(t *testing.T) {
	// On-chain arrival is not the same as a credited provider balance.
	// Converting before the credit lands would spend someone else's money —
	// precisely what a brokerage must never do.
	s := newStableSellDBService(t)
	conv := &stubConverter{balances: []FWBalance{{Currency: "USDC", AvailableBalance: 0}}}
	s.SetStableConverter(conv, "mrc_test")
	intent := confirmedSellIntent(t, s)

	err := s.ConvertAndQueuePayout(context.Background(), intent.ID)
	if !errors.Is(err, ErrStableCreditNotVisible) {
		t.Fatalf("err = %v, want ErrStableCreditNotVisible", err)
	}
	if len(conv.converted) != 0 {
		t.Fatal("converted before the credit was visible")
	}
	got, _ := s.GetIntent(context.Background(), intent.ID)
	if got.Status != models.ITStatusDepositConfirmed {
		t.Fatalf("status advanced to %q despite no credit", got.Status)
	}
}

func TestConvertAndQueuePayout_ConvertsAssetToFiatInTheRightDirection(t *testing.T) {
	s := newStableSellDBService(t)
	conv := &stubConverter{
		balances: []FWBalance{{Currency: "USDC", AvailableBalance: 150}},
		transfer: FWTransfer{ID: "trf_1", Status: "NEW"},
	}
	s.SetStableConverter(conv, "mrc_test")
	intent := confirmedSellIntent(t, s)

	if err := s.ConvertAndQueuePayout(context.Background(), intent.ID); err != nil {
		t.Fatalf("ConvertAndQueuePayout: %v", err)
	}
	if len(conv.converted) != 1 {
		t.Fatalf("conversions = %d, want 1", len(conv.converted))
	}
	if conv.converted[0].src != "USDC" || conv.converted[0].dst != "NGN" {
		t.Fatalf("converted %s→%s, want USDC→NGN", conv.converted[0].src, conv.converted[0].dst)
	}
}

func TestConvertAndQueuePayout_StopsAtConvertingNotDebited(t *testing.T) {
	// ConvertBalance returns as soon as Flutterwave ACCEPTS the conversion
	// (status NEW) — the fiat is credited later. `debited` is what makes
	// AutoPayout fire, so advancing here would disburse fiat that does not
	// exist yet. Verified against the sandbox: a USDC→NGN conversion returns
	// HTTP 201 {"status":"NEW"} even on an empty balance.
	s := newStableSellDBService(t)
	conv := &stubConverter{
		balances: []FWBalance{{Currency: "USDC", AvailableBalance: 150}},
		transfer: FWTransfer{ID: "trf_1", Status: "NEW"},
	}
	s.SetStableConverter(conv, "mrc_test")
	intent := confirmedSellIntent(t, s)

	if err := s.ConvertAndQueuePayout(context.Background(), intent.ID); err != nil {
		t.Fatalf("ConvertAndQueuePayout: %v", err)
	}
	got, _ := s.GetIntent(context.Background(), intent.ID)
	if got.Status != models.ITStatusConverting {
		t.Fatalf("status = %q, want %q", got.Status, models.ITStatusConverting)
	}
	if got.Status == models.ITStatusDebited {
		t.Fatal("advanced to debited on an unsettled conversion — AutoPayout would fire unfunded")
	}
	if got.ConversionID != "trf_1" {
		t.Fatalf("conversionId = %q, want trf_1", got.ConversionID)
	}
	if got.NetNgnKobo == "" {
		t.Fatal("net not set — AutoPayout would disburse the gross")
	}
}

func TestFinalizeConversion_PromotesToDebitedOnlyWhenSettled(t *testing.T) {
	s := newStableSellDBService(t)
	conv := &stubConverter{
		balances: []FWBalance{{Currency: "USDC", AvailableBalance: 150}},
		transfer: FWTransfer{ID: "trf_1", Status: "NEW"},
	}
	s.SetStableConverter(conv, "mrc_test")
	s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_1", Status: "SUCCESSFUL"}})
	intent := confirmedSellIntent(t, s)
	_ = s.ConvertAndQueuePayout(context.Background(), intent.ID)

	if err := s.FinalizeConversion(context.Background(), intent.ID); err != nil {
		t.Fatalf("FinalizeConversion: %v", err)
	}
	got, _ := s.GetIntent(context.Background(), intent.ID)
	if got.Status != models.ITStatusDebited {
		t.Fatalf("status = %q, want debited", got.Status)
	}
}

func TestFinalizeConversion_HoldsWhileTheConversionIsPending(t *testing.T) {
	s := newStableSellDBService(t)
	conv := &stubConverter{
		balances: []FWBalance{{Currency: "USDC", AvailableBalance: 150}},
		transfer: FWTransfer{ID: "trf_1", Status: "NEW"},
	}
	s.SetStableConverter(conv, "mrc_test")
	s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_1", Status: "PENDING"}})
	intent := confirmedSellIntent(t, s)
	_ = s.ConvertAndQueuePayout(context.Background(), intent.ID)

	if err := s.FinalizeConversion(context.Background(), intent.ID); err == nil {
		t.Fatal("a pending conversion was treated as settled")
	}
	got, _ := s.GetIntent(context.Background(), intent.ID)
	if got.Status != models.ITStatusConverting {
		t.Fatalf("status = %q, want it to stay converting", got.Status)
	}
}

func TestFinalizeConversion_RetriesAFailedConversion(t *testing.T) {
	// The user's stablecoin is still at Flutterwave, so dropping back to
	// deposit_confirmed lets the next tick retry — the correct response to a
	// transient provider failure, and never a silent loss.
	s := newStableSellDBService(t)
	conv := &stubConverter{
		balances: []FWBalance{{Currency: "USDC", AvailableBalance: 150}},
		transfer: FWTransfer{ID: "trf_1", Status: "NEW"},
	}
	s.SetStableConverter(conv, "mrc_test")
	s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_1", Status: "FAILED"}})
	intent := confirmedSellIntent(t, s)
	_ = s.ConvertAndQueuePayout(context.Background(), intent.ID)

	if err := s.FinalizeConversion(context.Background(), intent.ID); err != nil {
		t.Fatalf("FinalizeConversion: %v", err)
	}
	got, _ := s.GetIntent(context.Background(), intent.ID)
	if got.Status != models.ITStatusDepositConfirmed {
		t.Fatalf("status = %q, want deposit_confirmed so the next tick retries", got.Status)
	}
}

func TestConvertAndQueuePayout_IsIdempotent(t *testing.T) {
	s := newStableSellDBService(t)
	conv := &stubConverter{
		balances: []FWBalance{{Currency: "USDC", AvailableBalance: 150}},
		transfer: FWTransfer{ID: "trf_1", Status: "NEW"},
	}
	s.SetStableConverter(conv, "mrc_test")
	intent := confirmedSellIntent(t, s)

	if err := s.ConvertAndQueuePayout(context.Background(), intent.ID); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := s.ConvertAndQueuePayout(context.Background(), intent.ID); err != nil {
		t.Fatalf("second must be a no-op, got: %v", err)
	}
	if len(conv.converted) != 1 {
		t.Fatalf("converted %d times, want exactly 1 — a double conversion spends real money", len(conv.converted))
	}
}

func TestConvertAndQueuePayout_RefusesAnUnconfirmedIntent(t *testing.T) {
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	s.SetStableConverter(&stubConverter{}, "mrc_test")
	intent, _, _ := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "POLYGON", testSellBank())

	if err := s.ConvertAndQueuePayout(context.Background(), intent.ID); err == nil {
		t.Fatal("converted for an intent whose deposit was never confirmed")
	}
}
