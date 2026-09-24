package services

import (
	"context"
	"errors"
	"log"
	"os"
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

func (f fakePayChains) MainnetChainNameForID(id int64) (string, bool) { n, ok := f[id]; return n, ok }

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

// Bank fields saved while unconfigured, then the mode switched on its own: the
// account must still be resolved before the merchant goes live on it.
func TestPayMerchant_SwitchingIntoFiatAlwaysResolvesTheAccount(t *testing.T) {
	ctx := context.Background()
	s, banks := newPayMerchantSvc(t)
	banks.err = ErrNombaAccountNotFound
	m, err := s.CreateFromRequest(ctx, payRequest())
	if err != nil || m.SettlementMode != "" {
		t.Fatalf("an unresolved applicant account should leave settlement unconfigured: %v %+v", err, m)
	}
	banks.err = nil
	if _, err := s.Update(ctx, payActor, m.ID, PayMerchantUpdate{BankCode: payStr("044"), AccountNumber: payStr("9876543210")}); err != nil {
		t.Fatal(err)
	}
	before := banks.calls
	banks.name = "SKY AIR NIGERIA"
	got, err := s.Update(ctx, payActor, m.ID, PayMerchantUpdate{SettlementMode: payStr("fiat")})
	if err != nil || banks.calls != before+1 || got.AccountName != "SKY AIR NIGERIA" {
		t.Fatalf("err %v resolves %d->%d merchant %+v: fiat went live on an unverified account", err, before, banks.calls, got)
	}

	// An account that resolves to no name is not a verified account.
	s2, banks2 := newPayMerchantSvc(t)
	banks2.err = ErrNombaAccountNotFound
	m2, _ := s2.CreateFromRequest(ctx, payRequest())
	banks2.err, banks2.name = nil, "  "
	if _, err := s2.Update(ctx, payActor, m2.ID, PayMerchantUpdate{SettlementMode: payStr("fiat"), BankCode: payStr("044"), AccountNumber: payStr("9876543210")}); !errors.Is(err, ErrPayMerchantInvalid) {
		t.Fatalf("err = %v, want a refusal for a nameless account", err)
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

func TestPayMerchant_UpdateRefusesAStaleExpectedVersionAndWritesNothing(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	if m.Version != 1 {
		t.Fatalf("a new merchant must start at version 1, got %d", m.Version)
	}
	if _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{BusinessName: payStr("Renamed")}); err != nil {
		t.Fatal(err)
	}
	stale := 1
	_, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{ExpectedVersion: &stale, CustomerFeeSharePercent: payInt(10)})
	if !errors.Is(err, ErrPayMerchantStale) {
		t.Fatalf("err = %v, want ErrPayMerchantStale", err)
	}
	stored, _ := s.Get(context.Background(), m.ID)
	if stored.CustomerFeeSharePercent != 100 || stored.Version != 2 {
		t.Fatalf("a stale update wrote something: share %d version %d", stored.CustomerFeeSharePercent, stored.Version)
	}
	current := 2
	if _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{ExpectedVersion: &current, CustomerFeeSharePercent: payInt(10)}); err != nil {
		t.Fatalf("the current version must be accepted: %v", err)
	}
}

func TestPayMerchant_UpdateIncrementsTheVersionAndReturnsIt(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	got, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{CustomerFeeSharePercent: payInt(50)})
	if err != nil || got.Version != m.Version+1 {
		t.Fatalf("err %v version %d, want %d", err, got.Version, m.Version+1)
	}
	stored, _ := s.Get(context.Background(), m.ID)
	if stored.Version != got.Version {
		t.Fatalf("stored version %d, returned %d", stored.Version, got.Version)
	}
}

// A share-only edit must write only the share: it may not resurrect the
// status (or anything else) it read before another admin changed it.
func TestPayMerchant_PartialUpdateLeavesOtherColumnsAlone(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	old := m.Version
	if _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{Status: payStr(models.PayMerchantSuspended)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{ExpectedVersion: &old, CustomerFeeSharePercent: payInt(40)}); !errors.Is(err, ErrPayMerchantStale) {
		t.Fatalf("with the old version: err = %v, want stale", err)
	}
	got, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{CustomerFeeSharePercent: payInt(40)})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Get(context.Background(), m.ID)
	if got.CustomerFeeSharePercent != 40 || stored.CustomerFeeSharePercent != 40 || stored.Status != models.PayMerchantSuspended ||
		stored.BankCode != "058" || stored.AccountName != "SKY AIR LIMITED" {
		t.Fatalf("stored = %+v", stored)
	}
}

// casUpdate is the compare-and-set every Update write goes through: a write
// claiming a version the row no longer has matches nothing.
func TestPayMerchant_CompareAndSetOnlyLandsAtTheCurrentVersion(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	ok, err := s.casUpdate(s.db, m.ID, m.Version+5, map[string]interface{}{"business_name": "x"})
	if err != nil || ok {
		t.Fatalf("a write at the wrong version must not land: ok %v err %v", ok, err)
	}
	ok, err = s.casUpdate(s.db, m.ID, m.Version, map[string]interface{}{"business_name": "x"})
	if err != nil || !ok {
		t.Fatalf("a write at the current version must land: ok %v err %v", ok, err)
	}
	stored, _ := s.Get(context.Background(), m.ID)
	if stored.BusinessName != "x" || stored.Version != m.Version+1 {
		t.Fatalf("stored = %+v", stored)
	}
}

func payAuditRows(t *testing.T, s *PayMerchantService) []models.PayAuditEvent {
	t.Helper()
	var rows []models.PayAuditEvent
	s.db.Order("created_at").Find(&rows)
	return rows
}

func TestPayMerchant_UpdateWritesOneAuditRowWithBeforeAndAfter(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	if _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{
		WebhookURL: payStr("https://skyair.ng/hook"), AccountNumber: payStr("9876543210"), BankCode: payStr("044")}); err != nil {
		t.Fatal(err)
	}
	rows := payAuditRows(t, s)
	if len(rows) != 1 || rows[0].Action != "merchant.update" || rows[0].MerchantID != m.ID || rows[0].ActorID != payActor {
		t.Fatalf("rows = %+v", rows)
	}
	d := rows[0].Details
	for _, want := range []string{"webhookUrl", "https://skyair.ng/…", "…3210", "…6789", "bankCode"} {
		if !strings.Contains(d, want) {
			t.Fatalf("details %s lack %q", d, want)
		}
	}
	if strings.Contains(d, "9876543210") || strings.Contains(d, "0123456789") {
		t.Fatalf("details hold a full account number: %s", d)
	}
}

func TestPayMerchant_RefusedUpdatesWriteNoAuditRow(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	if _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{CustomerFeeSharePercent: payInt(101)}); !errors.Is(err, ErrPayMerchantInvalid) {
		t.Fatalf("err = %v", err)
	}
	stale := 99
	if _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{ExpectedVersion: &stale, CustomerFeeSharePercent: payInt(5)}); !errors.Is(err, ErrPayMerchantStale) {
		t.Fatalf("err = %v", err)
	}
	if rows := payAuditRows(t, s); len(rows) != 0 {
		t.Fatalf("refused updates wrote audit rows: %+v", rows)
	}
}

func TestPayMerchant_IssueKeysWritesAnAuditRowWithTheScopeAndNoSecrets(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	keys, err := s.IssueKeys(context.Background(), payActor, m.ID, PayKeysAll)
	if err != nil {
		t.Fatal(err)
	}
	rows := payAuditRows(t, s)
	if len(rows) != 1 || rows[0].Action != "merchant.keys" || !strings.Contains(rows[0].Details, `"scope":"all"`) {
		t.Fatalf("rows = %+v", rows)
	}
	stored, _ := s.Get(context.Background(), m.ID)
	for _, secret := range []string{keys.LiveKey, keys.TestKey, keys.WebhookSecretLive, keys.WebhookSecretTest, *stored.LiveKeyHash, *stored.TestKeyHash} {
		if secret == "" || strings.Contains(rows[0].Details, secret) {
			t.Fatalf("details %q hold secret material %q", rows[0].Details, secret)
		}
	}
}

func TestPayMerchant_AuthenticateSeparatesADatabaseFailureFromABadKey(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	k, _ := s.IssueKeys(context.Background(), payActor, m.ID, PayKeysAll)
	if _, _, err := s.Authenticate(context.Background(), "tsapk_live_"+strings.Repeat("0", 40)); !errors.Is(err, ErrPayKeyInvalid) {
		t.Fatalf("unknown key: err = %v", err)
	}
	s.db.Migrator().DropTable(&models.PayMerchant{})
	_, _, err := s.Authenticate(context.Background(), k.LiveKey)
	if !errors.Is(err, ErrPayAuthUnavailable) || errors.Is(err, ErrPayKeyInvalid) {
		t.Fatalf("broken store: err = %v, want ErrPayAuthUnavailable", err)
	}
}

func TestPayRedactURL(t *testing.T) {
	cases := map[string]string{
		"https://shop.ng/hooks/tsa?token=SECRET":         "https://shop.ng/…",
		"https://shop.ng/hooks/tsa#frag":                 "https://shop.ng/…",
		"https://shop.ng/hooks/T000/B000/SECRET":         "https://shop.ng/…",
		"https://shop.ng/hooks/tsa":                      "https://shop.ng/…",
		"https://user:pw@shop.ng/hooks/tsa?token=SECRET": "https://shop.ng/…",
		"https://shop.ng":                                "https://shop.ng",
		"https://shop.ng/":                               "https://shop.ng",
		"":                                               "",
		"http://[::1:bad":                                "[unparseable url]",
		"://no-scheme?token=SECRET":                      "[unparseable url]",
		"not a url?token=SECRET":                         "[unparseable url]",
	}
	for in, want := range cases {
		got := payRedactURL(in)
		if got != want {
			t.Errorf("payRedactURL(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(got, "SECRET") || strings.Contains(got, "pw@") {
			t.Errorf("payRedactURL(%q) = %q leaks", in, got)
		}
	}
}

func TestPayMerchant_AuditRowAndLogNeverHoldAWebhookURLQuery(t *testing.T) {
	var logs strings.Builder
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	if _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{
		WebhookURL: payStr("https://shop.ng/hooks/tsa?token=SECRET")}); err != nil {
		t.Fatal(err)
	}
	// A change that only alters the query must still be recorded.
	if _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{
		WebhookURL: payStr("https://shop.ng/hooks/tsa?token=OTHERSECRET")}); err != nil {
		t.Fatal(err)
	}
	rows := payAuditRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	for _, r := range rows {
		if strings.Contains(r.Details, "SECRET") {
			t.Fatalf("audit details hold the query secret: %s", r.Details)
		}
		if !strings.Contains(r.Details, "webhookUrl") || !strings.Contains(r.Details, "https://shop.ng/…") {
			t.Fatalf("audit details lost the host/path change: %s", r.Details)
		}
	}
	if strings.Contains(logs.String(), "SECRET") || !strings.Contains(logs.String(), "https://shop.ng/…") || strings.Contains(logs.String(), "hooks/tsa") {
		t.Fatalf("log output: %s", logs.String())
	}
	var stored models.PayMerchant
	s.db.First(&stored, "id = ?", m.ID)
	if stored.WebhookURL != "https://shop.ng/hooks/tsa?token=OTHERSECRET" {
		t.Fatalf("the merchant's real URL must stay intact, got %q", stored.WebhookURL)
	}
}

func TestValidPayDomain(t *testing.T) {
	for host, want := range map[string]bool{
		"skyair.ng": true, "www.sky-air.co.uk": true, "a1.b2.example": true,
		"": false, "localhost": false, "203.0.113.5": false, "::1": false,
		"skyair.ng,evil.example": false, "skyair.ng;evil.example": false, "sky air.ng": false,
		"Skyair.ng": false, "-a.ng": false, "skyair.ng.": false, strings.Repeat("a", 250) + ".ng": false,
	} {
		if got := ValidPayDomain(host); got != want {
			t.Errorf("ValidPayDomain(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestPayMerchant_CreateFromRequestRefusesAWebsiteThatSmugglesDomains(t *testing.T) {
	for _, site := range []string{"https://shop.ng,phish.example/", "https://shop.ng;phish.example/", "https://203.0.113.5/", "https://localhost/"} {
		s, _ := newPayMerchantSvc(t)
		req := payRequest()
		req.Website = site
		if _, err := s.CreateFromRequest(context.Background(), req); !errors.Is(err, ErrPayMerchantInvalid) {
			t.Errorf("website %q: err = %v, want ErrPayMerchantInvalid", site, err)
		}
	}
}

// The admin may only point a merchant at a chain a delivery can be CONFIRMED
// on: a mainnet chain with a client (so a verifier). The real registry also
// knows testnets, and a testnet key ("bsc" on chain 97) is the same string as
// its mainnet's — which would prove a testnet delivery on the mainnet verifier.
func TestPayMerchant_UpdateRefusesAChainNoDeliveryCouldBeConfirmedOn(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	s.chains = testnetAwareRegistry()
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	upd := func(chain int64) PayMerchantUpdate {
		return PayMerchantUpdate{SettlementMode: payStr("crypto"), ChainID: payI64(chain),
			TokenAddress: payStr("0x55d398326f99059fF775485246999027B3197955"), TokenSymbol: payStr("USDT"),
			WalletAddress: payStr("0x52908400098527886E0F7030069857D2E4169EE7")}
	}
	for name, chain := range map[string]int64{"bsc testnet": 97, "mainnet chain with no client": 10, "unknown": 999} {
		if _, err := s.Update(context.Background(), payActor, m.ID, upd(chain)); !errors.Is(err, ErrPayMerchantInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if _, err := s.Update(context.Background(), payActor, m.ID, upd(56)); err != nil {
		t.Fatalf("bsc mainnet: %v", err)
	}
}

// ---- a merchant changing their own settlement ----

// ownedMerchant is a merchant approved for owner, whose verified legal name is
// legalName ("" = not KYC-verified).
func ownedMerchant(t *testing.T, legalName string) (*PayMerchantService, *fakePayBanks, *models.PayMerchant, uuid.UUID) {
	t.Helper()
	s, banks := newPayMerchantSvc(t)
	s.chains = fakePayChains{8453: "base"}
	owner := uuid.New()
	status := models.VerificationStatusVerified
	if legalName == "" {
		status = "pending"
	}
	if err := s.db.Exec(`INSERT INTO users (id, name, email, wallet_address, verification_status, verified_legal_name) VALUES (?, ?, ?, ?, ?, ?)`,
		owner, "typed at signup", owner.String()+"@test.com", "0x000000000000000000000000000000000000dEaD", status, legalName).Error; err != nil {
		t.Fatal(err)
	}
	req := payRequest()
	req.UserID = owner
	m, err := s.CreateFromRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return s, banks, m, owner
}

func bankChange(account string) PaySettlementChange {
	return PaySettlementChange{SettlementMode: models.PaySettlementFiat, BankCode: "044", AccountNumber: account}
}

func TestPayMerchant_OwnerSettlementToAnAccountInTheBusinessName(t *testing.T) {
	ctx := context.Background()
	s, banks, m, owner := ownedMerchant(t, "")
	banks.name = "SKY AIR LIMITED"
	got, err := s.UpdateSettlementByOwner(ctx, owner, bankChange("9876543210"))
	if err != nil || got.AccountNumber != "9876543210" || got.BankCode != "044" || got.AccountName != "SKY AIR LIMITED" {
		t.Fatalf("err %v merchant %+v", err, got)
	}
	var audit models.PayAuditEvent
	if err := s.db.Where("merchant_id = ?", m.ID).Order("created_at desc").First(&audit).Error; err != nil || audit.ActorID != owner {
		t.Fatalf("audit %+v err %v: the owner's change must be audited as theirs", audit, err)
	}
}

func TestPayMerchant_OwnerSettlementToAnAccountInTheirVerifiedName(t *testing.T) {
	s, banks, _, owner := ownedMerchant(t, "Ada Chioma Obi")
	banks.name = "OBI ADA"
	if _, err := s.UpdateSettlementByOwner(context.Background(), owner, bankChange("9876543210")); err != nil {
		t.Fatalf("an account in her verified name was refused: %v", err)
	}
}

func TestPayMerchant_OwnerSettlementRefusesAnAccountInSomeoneElsesName(t *testing.T) {
	for name, legal := range map[string]string{"KYC-verified": "Ada Chioma Obi", "not verified": ""} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, banks, m, owner := ownedMerchant(t, legal)
			banks.name = "EMEKA NWOSU"
			if _, err := s.UpdateSettlementByOwner(ctx, owner, bankChange("9876543210")); !errors.Is(err, ErrPaySettlementNameMismatch) {
				t.Fatalf("err = %v", err)
			}
			stored, _ := s.Get(ctx, m.ID)
			if stored.AccountNumber == "9876543210" || stored.Version != m.Version {
				t.Fatalf("a refused change landed: %+v", stored)
			}
		})
	}
}

func TestPayMerchant_OwnerSettlementToAWallet(t *testing.T) {
	s, _, _, owner := ownedMerchant(t, "")
	got, err := s.UpdateSettlementByOwner(context.Background(), owner, PaySettlementChange{
		SettlementMode: models.PaySettlementCrypto, ChainID: 8453,
		TokenAddress: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", TokenSymbol: "USDC",
		WalletAddress: "0x52908400098527886E0F7030069857D2E4169EE7",
	})
	if err != nil || got.SettlementMode != models.PaySettlementCrypto || got.ChainID != 8453 {
		t.Fatalf("err %v merchant %+v", err, got)
	}
	// The chain rules still apply: a chain no delivery can be proven on is refused.
	if _, err := s.UpdateSettlementByOwner(context.Background(), owner, PaySettlementChange{
		SettlementMode: models.PaySettlementCrypto, ChainID: 999, TokenAddress: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
		TokenSymbol: "USDC", WalletAddress: "0x52908400098527886E0F7030069857D2E4169EE7",
	}); !errors.Is(err, ErrPayMerchantInvalid) {
		t.Fatalf("unsupported chain: err = %v", err)
	}
}

func TestPayMerchant_OwnerSettlementRefusals(t *testing.T) {
	ctx := context.Background()
	s, banks, m, owner := ownedMerchant(t, "")
	banks.name = "SKY AIR LTD"
	if _, err := s.UpdateSettlementByOwner(ctx, uuid.New(), bankChange("9876543210")); !errors.Is(err, ErrPayMerchantNotFound) {
		t.Fatalf("someone else's change: err = %v", err)
	}
	if _, err := s.UpdateSettlementByOwner(ctx, owner, PaySettlementChange{SettlementMode: ""}); !errors.Is(err, ErrPayMerchantInvalid) {
		t.Fatalf("unconfiguring: err = %v", err)
	}
	if err := s.db.Model(m).Update("status", models.PayMerchantSuspended).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateSettlementByOwner(ctx, owner, bankChange("9876543210")); !errors.Is(err, ErrPayMerchantSuspended) {
		t.Fatalf("suspended: err = %v", err)
	}
}
