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

func TestResolveFiatVA_ByCustomerThenReference(t *testing.T) {
	s := newITTestService(t)
	userID := uuid.New()
	row := &models.FiatVirtualAccount{
		UserID: userID, Currency: "NGN", Provider: models.FiatVAProviderFlutterwave,
		FwCustomerID: "cus_abc", Reference: "resp-ref-1",
		AccountNumber: "1234567890", BankName: "Test Bank", AccountName: "Ada L",
		Status: models.FiatVAStatusActive,
	}
	if err := s.db.Create(row).Error; err != nil {
		t.Fatal(err)
	}

	got, err := s.resolveFiatVA(context.Background(), "cus_abc", "other")
	if err != nil || got.AccountNumber != "1234567890" {
		t.Fatalf("by customer: %v %+v", err, got)
	}
	got, err = s.resolveFiatVA(context.Background(), "cus_missing", "resp-ref-1")
	if err != nil || got.FwCustomerID != "cus_abc" {
		t.Fatalf("by reference fallback: %v %+v", err, got)
	}
	_, err = s.resolveFiatVA(context.Background(), "cus_x", "ref_x")
	if !errors.Is(err, ErrITVANotFound) {
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
