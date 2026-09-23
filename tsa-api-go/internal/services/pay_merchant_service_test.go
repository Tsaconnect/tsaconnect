package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

type fakePayBanks struct {
	name  string
	err   error
	calls int
}

func (f *fakePayBanks) ListBanks(context.Context) ([]NombaBank, error) { return nil, nil }
func (f *fakePayBanks) Resolve(_ context.Context, _, acct string) (NombaLookupResult, error) {
	f.calls++
	return NombaLookupResult{AccountNumber: acct, AccountName: f.name}, f.err
}

func newPayMerchantSvc(t *testing.T) (*PayMerchantService, *fakePayBanks) {
	t.Helper()
	banks := &fakePayBanks{name: "SKY AIR LIMITED"}
	return NewPayMerchantService(newPayTestDB(t), banks, nil, []byte(strings.Repeat("k", 32)),
		func() time.Time { return payTestNow }), banks
}

func payRequest() *models.MerchantRequest {
	return &models.MerchantRequest{ID: uuid.New(), UserID: uuid.New(), BusinessType: models.BusinessTypePaymentAcceptance,
		BusinessName: "Sky Air", Website: "https://www.SkyAir.ng/book", SettlementBankCode: "058",
		SettlementAccountNumber: "0123456789", SettlementAccountName: "whatever the applicant typed"}
}

func TestPayMerchant_CreateFromRequestAppliesTheApprovalDefaults(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, err := s.CreateFromRequest(context.Background(), payRequest())
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != models.PayMerchantActive || m.CustomerFeeSharePercent != 100 || m.MinAmountKobo != 100_000 ||
		m.MaxAmountKobo != 500_000_000 || m.WebsiteDomain != "www.skyair.ng" || m.ReturnDomains != "www.skyair.ng" ||
		m.SettlementMode != models.PaySettlementFiat || m.BankCode != "058" || m.WebhookSecretVersion != 1 {
		t.Fatalf("merchant = %+v", m)
	}
	if m.AccountName != "SKY AIR LIMITED" {
		t.Fatalf("account name = %q; it must come from the name enquiry, never the applicant", m.AccountName)
	}
	if m.LiveKeyHash != nil || m.TestKeyHash != nil {
		t.Fatal("approval must not mint keys")
	}
}

func TestPayMerchant_CreateFromRequestLeavesAnUnverifiableBankUnset(t *testing.T) {
	s, banks := newPayMerchantSvc(t)
	banks.err = ErrNombaAccountNotFound
	m, err := s.CreateFromRequest(context.Background(), payRequest())
	if err != nil || m.SettlementMode != "" || m.BankCode != "" || m.AccountNumber != "" {
		t.Fatalf("err %v merchant %+v", err, m)
	}
}

// TestPayMerchant_CreateFromRequestTrimsAWhitespacePaddedWebsite guards
// against a nil-pointer panic: url.Parse errors (returns a nil *url.URL) on
// a leading/trailing-whitespace URL like "  https://…  " — SubmitMerchantRequest
// is expected to store the trimmed value, but CreateFromRequest must still
// handle a padded (or otherwise malformed) Website defensively rather than
// dereferencing a nil URL.
func TestPayMerchant_CreateFromRequestTrimsAWhitespacePaddedWebsite(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	req := payRequest()
	req.Website = "  https://www.SkyAir.ng/book  "
	m, err := s.CreateFromRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if m.WebsiteDomain != "www.skyair.ng" || m.ReturnDomains != "www.skyair.ng" {
		t.Fatalf("domain = %q / %q, want www.skyair.ng trimmed and lowercased", m.WebsiteDomain, m.ReturnDomains)
	}
}

func TestPayMerchant_CreateFromRequestIsIdempotent(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	req := payRequest()
	a, _ := s.CreateFromRequest(context.Background(), req)
	b, err := s.CreateFromRequest(context.Background(), req)
	if err != nil || a.ID != b.ID {
		t.Fatalf("second call: %v, ids %s / %s", err, a.ID, b.ID)
	}
}
