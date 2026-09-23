package services

import (
	"context"
	"errors"
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

type fakePayChains map[int64]string

func (f fakePayChains) ChainNameForID(id int64) (string, bool) { n, ok := f[id]; return n, ok }

func payStr(s string) *string { return &s }
func payInt(i int) *int       { return &i }
func payI64(i int64) *int64   { return &i }

var payActor = uuid.MustParse("99999999-9999-9999-9999-999999999999")

func TestPayMerchant_UpdateResolvesTheBankAccountName(t *testing.T) {
	s, banks := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	banks.name = "SKY AIR NIGERIA"
	got, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{SettlementMode: payStr("fiat"), BankCode: payStr("044"), AccountNumber: payStr("9876543210")})
	if err != nil || got.AccountName != "SKY AIR NIGERIA" || got.BankCode != "044" {
		t.Fatalf("err %v merchant %+v", err, got)
	}
	banks.err = ErrNombaAccountNotFound
	if _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{AccountNumber: payStr("1111111111")}); !errors.Is(err, ErrPayMerchantInvalid) {
		t.Fatalf("unknown account: err = %v", err)
	}
	stored, _ := s.Get(context.Background(), m.ID)
	if stored.AccountNumber != "9876543210" {
		t.Fatal("a refused update partially persisted")
	}
}

func TestPayMerchant_UpdateValidatesCryptoSettlement(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	s.chains = fakePayChains{56: "bsc"}
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	good := PayMerchantUpdate{SettlementMode: payStr("crypto"), ChainID: payI64(56),
		TokenAddress: payStr("0x55d398326f99059fF775485246999027B3197955"), TokenSymbol: payStr("USDT"),
		WalletAddress: payStr("0x52908400098527886E0F7030069857D2E4169EE7")}
	if _, err := s.Update(context.Background(), payActor, m.ID, good); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PayMerchantUpdate){
		"unknown chain": func(u *PayMerchantUpdate) { u.ChainID = payI64(999) },
		"native coin":   func(u *PayMerchantUpdate) { u.TokenAddress = payStr(evmNativeAddress) },
		"native alias":  func(u *PayMerchantUpdate) { u.TokenAddress = payStr(evmNativeAliasAddress) },
		"bad wallet":    func(u *PayMerchantUpdate) { u.WalletAddress = payStr("not-an-address") },
		"zero wallet":   func(u *PayMerchantUpdate) { u.WalletAddress = payStr("0x0000000000000000000000000000000000000000") },
	} {
		u := good
		mutate(&u)
		if _, err := s.Update(context.Background(), payActor, m.ID, u); !errors.Is(err, ErrPayMerchantInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestPayMerchant_UpdateGuardsShareLimitsURLsAndStatus(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	for name, u := range map[string]PayMerchantUpdate{
		"share over 100":  {CustomerFeeSharePercent: payInt(101)},
		"negative share":  {CustomerFeeSharePercent: payInt(-1)},
		"min zero":        {MinAmountKobo: payI64(0)},
		"min over max":    {MinAmountKobo: payI64(600_000_000)},
		"max over bound":  {MaxAmountKobo: payI64(MaxPayAmountKobo + 1)},
		"http webhook":    {WebhookURL: payStr("http://skyair.ng/hook")},
		"loopback test":   {TestWebhookURL: payStr("https://127.0.0.1/hook")},
		"bad domain":      {ReturnDomains: []string{"https://skyair.ng"}},
		"bad status":      {Status: payStr("closed")},
		"9-digit account": {AccountNumber: payStr("123456789")},
	} {
		if _, err := s.Update(context.Background(), payActor, m.ID, u); !errors.Is(err, ErrPayMerchantInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	got, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{ReturnDomains: []string{"SkyAir.ng", " pay.skyair.ng "}})
	if err != nil || got.ReturnDomains != "skyair.ng,pay.skyair.ng" {
		t.Fatalf("err %v domains %q", err, got.ReturnDomains)
	}
}

func TestPayMerchant_SensitiveUpdates(t *testing.T) {
	if (PayMerchantUpdate{BusinessName: payStr("x"), CustomerFeeSharePercent: payInt(0), Status: payStr("suspended")}).Sensitive() {
		t.Fatal("name/share/status are not sensitive")
	}
	for _, u := range []PayMerchantUpdate{{BankCode: payStr("044")}, {WalletAddress: payStr("0x1")}, {WebhookURL: payStr("https://x.ng")},
		{TestWebhookURL: payStr("https://x.ng")}, {ReturnDomains: []string{"x.ng"}}, {SettlementMode: payStr("crypto")}} {
		if !u.Sensitive() {
			t.Fatalf("%+v must need a super admin", u)
		}
	}
}

func TestPayMerchant_IssueKeysByScope(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	all, err := s.IssueKeys(context.Background(), payActor, m.ID, PayKeysAll)
	if err != nil || all.LiveKey == "" || all.TestKey == "" || all.WebhookSecretLive == "" || all.WebhookSecretTest == "" ||
		all.WebhookSecretLive == all.WebhookSecretTest {
		t.Fatalf("all: %+v %v", all, err)
	}
	test, _ := s.IssueKeys(context.Background(), payActor, m.ID, PayKeysTest)
	if test.LiveKey != "" || test.WebhookSecretLive != "" {
		t.Fatal("rotating the test key must return only the test key")
	}
	if _, _, err := s.Authenticate(context.Background(), all.TestKey); !errors.Is(err, ErrPayKeyInvalid) {
		t.Fatal("the rotated-out test key still works")
	}
	if got, mode, err := s.Authenticate(context.Background(), all.LiveKey); err != nil || got.ID != m.ID || mode != models.PayModeLive {
		t.Fatalf("rotating test must leave live alone: %v", err)
	}
	wh, _ := s.IssueKeys(context.Background(), payActor, m.ID, PayKeysWebhook)
	stored, _ := s.Get(context.Background(), m.ID)
	if wh.WebhookSecretLive == all.WebhookSecretLive || s.WebhookSecret(stored, models.PayModeLive) != wh.WebhookSecretLive {
		t.Fatal("webhook rotation must re-key, and WebhookSecret must derive the current secret")
	}
}

func TestPayMerchant_AuthenticateRefusesSuspendedAndGarbage(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	k, _ := s.IssueKeys(context.Background(), payActor, m.ID, PayKeysAll)
	for _, raw := range []string{"", "tsapk_live_nope", "Bearer " + k.LiveKey, strings.Replace(k.LiveKey, "live", "test", 1)} {
		if _, _, err := s.Authenticate(context.Background(), raw); !errors.Is(err, ErrPayKeyInvalid) {
			t.Fatalf("%q: err = %v", raw, err)
		}
	}
	s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{Status: payStr(models.PayMerchantSuspended)})
	if _, _, err := s.Authenticate(context.Background(), k.LiveKey); !errors.Is(err, ErrPayMerchantSuspended) {
		t.Fatalf("suspended: err = %v", err)
	}
}
