package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestSplitPersonName(t *testing.T) {
	first, last := splitPersonName("Ada Lovelace")
	if first != "Ada" || last != "Lovelace" {
		t.Fatalf("got %q %q", first, last)
	}
	first, last = splitPersonName("Madonna")
	if first != "Madonna" || last != "Madonna" {
		t.Fatalf("single name: got %q %q", first, last)
	}
}

func TestGetFiatVA_NotFound(t *testing.T) {
	s := newITTestService(t)
	_, err := s.GetFiatVA(context.Background(), uuid.New(), "NGN")
	if !errors.Is(err, ErrITVANotFound) {
		t.Fatalf("err = %v, want ErrITVANotFound", err)
	}
}

func TestResolveFiatVA_PrefersVerifiedReference(t *testing.T) {
	s := newITTestService(t)
	row := &models.FiatVirtualAccount{
		UserID: uuid.New(), Currency: "NGN", Provider: models.FiatVAProviderFlutterwave,
		FwCustomerID: "cus_abc", Reference: "resp-ref-1",
		AccountNumber: "1234567890", BankName: "Test Bank", AccountName: "Ada L",
		Status: models.FiatVAStatusActive,
	}
	other := &models.FiatVirtualAccount{
		UserID: uuid.New(), Currency: "NGN", Provider: models.FiatVAProviderFlutterwave,
		FwCustomerID: "cus_mallory", Reference: "resp-ref-2",
		AccountNumber: "9999999999", BankName: "Test Bank", AccountName: "Mallory",
		Status: models.FiatVAStatusActive,
	}
	for _, r := range []*models.FiatVirtualAccount{row, other} {
		if err := s.db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}

	// The reference is verified against the Flutterwave API upstream; the
	// customer id is not. When they disagree, the verified key must win —
	// otherwise a payload pairing one account's reference with another's
	// customer id would credit the wrong user.
	got, err := s.resolveFiatVA(context.Background(), "cus_mallory", "resp-ref-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.AccountNumber != "1234567890" {
		t.Fatalf("resolved to %q — customer id overrode the verified reference", got.AccountNumber)
	}

	// Customer id still resolves when the reference is unknown (the response
	// reference may not match what we sent).
	got, err = s.resolveFiatVA(context.Background(), "cus_abc", "unknown-ref")
	if err != nil || got.FwCustomerID != "cus_abc" {
		t.Fatalf("by customer fallback: %v %+v", err, got)
	}

	if _, err := s.resolveFiatVA(context.Background(), "cus_x", "ref_x"); !errors.Is(err, ErrITVANotFound) {
		t.Fatalf("want ErrITVANotFound, got %v", err)
	}
}

func TestQuoteBuyAmount_Basic(t *testing.T) {
	s := newITTestService(t)
	if _, err := s.SetRate(context.Background(), uuid.New(), 20000, 0); err != nil {
		t.Fatal(err)
	}
	mcgp, rate, err := s.QuoteBuyAmount(context.Background(), "100000") // ₦1000
	if err != nil {
		t.Fatalf("QuoteBuyAmount: %v", err)
	}
	if mcgp.Sign() <= 0 || rate.Sign() <= 0 {
		t.Fatalf("mcgp=%s rate=%s", mcgp, rate)
	}
}
