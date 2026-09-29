package services

import (
	"context"
	"errors"
	"log"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

type fakePayBanks struct {
	name  string
	err   error
	calls int
}

func (f *fakePayBanks) ListBanks(context.Context) ([]NombaBank, error) {
	return []NombaBank{{Code: "044", Name: "Access Bank"}}, f.err
}
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
		m.SettlementMode != models.PaySettlementFiat || m.BankCode != "058" || m.WebhookSecretVersion != 1 || !m.LiveEnabled {
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

// A business with no website is approved with no website or return domain: it
// is paid through links, and no return URL is accepted until it adds a domain.
func TestPayMerchant_CreateFromRequestWithNoWebsite(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	req := payRequest()
	req.Website = "  "
	m, err := s.CreateFromRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if m.WebsiteDomain != "" || m.ReturnDomains != "" || len(m.ReturnDomainList()) != 0 || m.SettlementMode != models.PaySettlementFiat {
		t.Fatalf("merchant = %+v", m)
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

func (f fakePayChains) MainnetChainIDs() []int64 {
	ids := make([]int64, 0, len(f))
	for id := range f {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// fakePayListedTokens is the stablecoin listing the fake registry answers with.
var fakePayListedTokens = map[string]string{
	"base:USDC": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
	"bsc:USDT":  "0x55d398326f99059fF775485246999027B3197955",
}

func (f fakePayChains) TokenAddress(chain, symbol string) string {
	return fakePayListedTokens[chain+":"+symbol]
}

func payStr(s string) *string { return &s }
func payInt(i int) *int       { return &i }
func payI64(i int64) *int64   { return &i }

var payActor = uuid.MustParse("99999999-9999-9999-9999-999999999999")

func TestPayMerchant_UpdateResolvesTheBankAccountName(t *testing.T) {
	s, banks := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	banks.name = "SKY AIR NIGERIA"
	got, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{SettlementMode: payStr("fiat"), BankCode: payStr("044"), AccountNumber: payStr("9876543210")})
	if err != nil || got.AccountName != "SKY AIR NIGERIA" || got.BankCode != "044" {
		t.Fatalf("err %v merchant %+v", err, got)
	}
	banks.err = ErrNombaAccountNotFound
	if _, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{AccountNumber: payStr("1111111111")}); !errors.Is(err, ErrPayMerchantInvalid) {
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
	if _, _, err := s.Update(ctx, payActor, m.ID, PayMerchantUpdate{BankCode: payStr("044"), AccountNumber: payStr("9876543210")}); err != nil {
		t.Fatal(err)
	}
	before := banks.calls
	banks.name = "SKY AIR NIGERIA"
	got, _, err := s.Update(ctx, payActor, m.ID, PayMerchantUpdate{SettlementMode: payStr("fiat")})
	if err != nil || banks.calls != before+1 || got.AccountName != "SKY AIR NIGERIA" {
		t.Fatalf("err %v resolves %d->%d merchant %+v: fiat went live on an unverified account", err, before, banks.calls, got)
	}

	// An account that resolves to no name is not a verified account.
	s2, banks2 := newPayMerchantSvc(t)
	banks2.err = ErrNombaAccountNotFound
	m2, _ := s2.CreateFromRequest(ctx, payRequest())
	banks2.err, banks2.name = nil, "  "
	if _, _, err := s2.Update(ctx, payActor, m2.ID, PayMerchantUpdate{SettlementMode: payStr("fiat"), BankCode: payStr("044"), AccountNumber: payStr("9876543210")}); !errors.Is(err, ErrPayMerchantInvalid) {
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
	if _, _, err := s.Update(context.Background(), payActor, m.ID, good); err != nil {
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
		if _, _, err := s.Update(context.Background(), payActor, m.ID, u); !errors.Is(err, ErrPayMerchantInvalid) {
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
		if _, _, err := s.Update(context.Background(), payActor, m.ID, u); !errors.Is(err, ErrPayMerchantInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	got, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{ReturnDomains: []string{"SkyAir.ng", " pay.skyair.ng "}})
	if err != nil || got.ReturnDomains != "skyair.ng,pay.skyair.ng" {
		t.Fatalf("err %v domains %q", err, got.ReturnDomains)
	}
}

func TestPayMerchant_SensitiveUpdates(t *testing.T) {
	if (PayMerchantUpdate{CustomerFeeSharePercent: payInt(0), Status: payStr("suspended")}).Sensitive() {
		t.Fatal("share/status are not sensitive")
	}
	for _, u := range []PayMerchantUpdate{{BusinessName: payStr("x")}, {BankCode: payStr("044")}, {WalletAddress: payStr("0x1")}, {WebhookURL: payStr("https://x.ng")},
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
	if _, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{BusinessName: payStr("Renamed")}); err != nil {
		t.Fatal(err)
	}
	stale := 1
	_, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{ExpectedVersion: &stale, CustomerFeeSharePercent: payInt(10)})
	if !errors.Is(err, ErrPayMerchantStale) {
		t.Fatalf("err = %v, want ErrPayMerchantStale", err)
	}
	stored, _ := s.Get(context.Background(), m.ID)
	if stored.CustomerFeeSharePercent != 100 || stored.Version != 2 {
		t.Fatalf("a stale update wrote something: share %d version %d", stored.CustomerFeeSharePercent, stored.Version)
	}
	current := 2
	if _, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{ExpectedVersion: &current, CustomerFeeSharePercent: payInt(10)}); err != nil {
		t.Fatalf("the current version must be accepted: %v", err)
	}
}

func TestPayMerchant_UpdateIncrementsTheVersionAndReturnsIt(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	got, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{CustomerFeeSharePercent: payInt(50)})
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
	if _, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{Status: payStr(models.PayMerchantSuspended)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{ExpectedVersion: &old, CustomerFeeSharePercent: payInt(40)}); !errors.Is(err, ErrPayMerchantStale) {
		t.Fatalf("with the old version: err = %v, want stale", err)
	}
	got, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{CustomerFeeSharePercent: payInt(40)})
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
	if _, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{
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
	if _, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{CustomerFeeSharePercent: payInt(101)}); !errors.Is(err, ErrPayMerchantInvalid) {
		t.Fatalf("err = %v", err)
	}
	stale := 99
	if _, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{ExpectedVersion: &stale, CustomerFeeSharePercent: payInt(5)}); !errors.Is(err, ErrPayMerchantStale) {
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
		got := PayRedactURL(in)
		if got != want {
			t.Errorf("PayRedactURL(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(got, "SECRET") || strings.Contains(got, "pw@") {
			t.Errorf("PayRedactURL(%q) = %q leaks", in, got)
		}
	}
}

func TestPayMerchant_AuditRowAndLogNeverHoldAWebhookURLQuery(t *testing.T) {
	var logs strings.Builder
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	if _, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{
		WebhookURL: payStr("https://shop.ng/hooks/tsa?token=SECRET")}); err != nil {
		t.Fatal(err)
	}
	// A change that only alters the query must still be recorded.
	if _, _, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{
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
		if _, _, err := s.Update(context.Background(), payActor, m.ID, upd(chain)); !errors.Is(err, ErrPayMerchantInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if _, _, err := s.Update(context.Background(), payActor, m.ID, upd(56)); err != nil {
		t.Fatalf("bsc mainnet: %v", err)
	}
}

// ---- a merchant changing their own settlement ----

// fakeCodeMailer records the one email a payout change sends.
type fakeCodeMailer struct {
	to, message string
	sent        int
	err         error
}

func (f *fakeCodeMailer) Send(to, _, _, _, message, _, _ string) error {
	f.to, f.message = to, message
	f.sent++
	return f.err
}

// code is the six digits in the last email.
func (f *fakeCodeMailer) code(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`\b\d{6}\b`).FindString(f.message)
	if m == "" {
		t.Fatalf("no code in %q", f.message)
	}
	return m
}

type ownerFixture struct {
	s     *PayMerchantService
	banks *fakePayBanks
	mail  *fakeCodeMailer
	m     *models.PayMerchant
	owner uuid.UUID
}

// ownedMerchant is a merchant approved for an owner whose KYC status and legal
// name are as given (status "" = never verified; legalName "NULL" = the column
// is NULL, as on rows that predate it).
func ownedMerchant(t *testing.T, status, legalName string) *ownerFixture {
	t.Helper()
	s, banks := newPayMerchantSvc(t)
	s.chains = fakePayChains{8453: "base", 56: "bsc"}
	mail := &fakeCodeMailer{}
	s.SetMailer(mail)
	owner := uuid.New()
	var legal any = legalName
	if legalName == "NULL" {
		legal = nil
	}
	if err := s.db.Exec(`INSERT INTO users (id, name, email, wallet_address, verification_status, verified_legal_name) VALUES (?, ?, ?, ?, ?, ?)`,
		owner, "typed at signup", "owner@skyair.ng", "0x000000000000000000000000000000000000dEaD", status, legal).Error; err != nil {
		t.Fatal(err)
	}
	req := payRequest()
	req.UserID = owner
	m, err := s.CreateFromRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return &ownerFixture{s: s, banks: banks, mail: mail, m: m, owner: owner}
}

func bankChange(account string) PaySettlementChange {
	return PaySettlementChange{SettlementMode: models.PaySettlementFiat, BankCode: "044", AccountNumber: account}
}

const (
	ownerBaseUSDC = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	ownerWallet   = "0x52908400098527886E0F7030069857D2E4169EE7"
)

func walletChange() PaySettlementChange {
	return PaySettlementChange{SettlementMode: models.PaySettlementCrypto, ChainID: 8453, TokenAddress: ownerBaseUSDC, WalletAddress: ownerWallet}
}

func (f *ownerFixture) stored(t *testing.T) *models.PayMerchant {
	t.Helper()
	m, err := f.s.Get(context.Background(), f.m.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// requestWallet asks for the crypto payout change and returns what is waiting.
func (f *ownerFixture) requestWallet(t *testing.T) *PaySettlementResult {
	t.Helper()
	return f.request(t, walletChange())
}

// request asks for a payout change and returns what is waiting on its code.
func (f *ownerFixture) request(t *testing.T, ch PaySettlementChange) *PaySettlementResult {
	t.Helper()
	res, err := f.s.UpdateSettlementByOwner(context.Background(), f.owner, ch)
	if err != nil || res.Pending == nil {
		t.Fatalf("err %v result %+v", err, res)
	}
	return res
}

// nothingWaiting fails when a code was sent or a change is waiting on one.
func (f *ownerFixture) nothingWaiting(t *testing.T) {
	t.Helper()
	var n int64
	f.s.db.Model(&models.PayOwnerAction{}).Count(&n)
	if f.mail.sent != 0 || n != 0 {
		t.Fatalf("%d codes sent, %d changes waiting: a refused change must send no code", f.mail.sent, n)
	}
}

func TestPayMerchant_OwnerSettlementToAnAccountInTheBusinessName(t *testing.T) {
	f := ownedMerchant(t, "", "")
	f.banks.name = "SKY AIR LIMITED"
	res := f.request(t, bankChange("9876543210"))
	r, err := f.s.ConfirmOwnerAction(context.Background(), f.owner, res.Pending.ID, f.mail.code(t))
	if err != nil || r.Merchant.AccountNumber != "9876543210" || r.Merchant.AccountName != "SKY AIR LIMITED" {
		t.Fatalf("err %v result %+v", err, r)
	}
	var audit models.PayAuditEvent
	if err := f.s.db.Where("merchant_id = ?", f.m.ID).Order("created_at desc").First(&audit).Error; err != nil || audit.ActorID != f.owner {
		t.Fatalf("audit %+v err %v: the owner's change must be audited as theirs", audit, err)
	}
}

func TestPayMerchant_OwnerSettlementToAnAccountInTheirVerifiedName(t *testing.T) {
	f := ownedMerchant(t, models.VerificationStatusVerified, "Ada Chioma Obi")
	f.banks.name = "OBI ADA" // the bank dropped her middle name
	res := f.request(t, bankChange("9876543210"))
	if _, err := f.s.ConfirmOwnerAction(context.Background(), f.owner, res.Pending.ID, f.mail.code(t)); err != nil {
		t.Fatalf("an account in her verified name was refused: %v", err)
	}
}

func TestPayMerchant_OwnerSettlementRefusesAnAccountThatIsNotTheirs(t *testing.T) {
	for name, tc := range map[string]struct{ status, legal, bank string }{
		"someone else's name":                                 {models.VerificationStatusVerified, "Ada Chioma Obi", "EMEKA NWOSU"},
		"a longer business name that contains theirs":         {"", "", "SKY AIR CARGO LIMITED"},
		"their own name padded with another's":                {models.VerificationStatusVerified, "Ada Obi", "ADA OBI EMEKA"},
		"a verification that was rejected keeps no authority": {"rejected", "Emeka Nwosu", "EMEKA NWOSU"},
		"a legal name column that is NULL is simply no match": {models.VerificationStatusVerified, "NULL", "EMEKA NWOSU"},
	} {
		t.Run(name, func(t *testing.T) {
			f := ownedMerchant(t, tc.status, tc.legal)
			f.banks.name = tc.bank
			if _, err := f.s.UpdateSettlementByOwner(context.Background(), f.owner, bankChange("9876543210")); !errors.Is(err, ErrPaySettlementNameMismatch) {
				t.Fatalf("err = %v, want a name mismatch", err)
			}
			if got := f.stored(t); got.AccountNumber == "9876543210" || got.Version != f.m.Version {
				t.Fatalf("a refused change landed: %+v", got)
			}
			f.nothingWaiting(t)
		})
	}
}

func TestPayMerchant_OwnerSettlementHidesTheBanksOwnWords(t *testing.T) {
	f := ownedMerchant(t, "", "")
	f.banks.err = errors.New("nomba: 401 upstream token expired")
	_, err := f.s.UpdateSettlementByOwner(context.Background(), f.owner, bankChange("9876543210"))
	if !errors.Is(err, ErrPayAccountUnverifiable) || !errors.Is(err, ErrPayMerchantInvalid) {
		t.Fatalf("err = %v: an unresolvable account must be told apart from other refusals", err)
	}
	f.nothingWaiting(t)
}

func TestPayMerchant_OwnerBankChangeWaitsForTheEmailedCode(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	f.banks.name = "SKY AIR LIMITED"
	res := f.request(t, bankChange("9876543210"))
	if res.Pending.Kind != models.PayOwnerActionSettlement {
		t.Fatalf("pending kind %q", res.Pending.Kind)
	}
	if got := f.stored(t); got.AccountNumber == "9876543210" || got.Version != f.m.Version {
		t.Fatalf("the bank account was applied before the code was entered: %+v", got)
	}
	if f.mail.sent != 1 || !strings.Contains(f.mail.message, "…3210 (SKY AIR LIMITED)") || strings.Contains(f.mail.message, "9876543210") {
		t.Fatalf("the email must name the account by its last four digits and resolved name: %q", f.mail.message)
	}
	wrong := "000000"
	if f.mail.code(t) == wrong {
		wrong = "111111"
	}
	if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, wrong); !errors.Is(err, ErrPayCodeInvalid) {
		t.Fatalf("a wrong code: err = %v", err)
	}
	if got := f.stored(t); got.AccountNumber == "9876543210" {
		t.Fatal("a wrong code applied the bank account")
	}
	r, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, f.mail.code(t))
	if err != nil || r.Kind != models.PayOwnerActionSettlement || r.Merchant.SettlementMode != models.PaySettlementFiat ||
		r.Merchant.BankCode != "044" || r.Merchant.AccountNumber != "9876543210" || r.Merchant.AccountName != "SKY AIR LIMITED" {
		t.Fatalf("err %v result %+v", err, r)
	}
	if got := f.stored(t); got.AccountNumber != "9876543210" || got.Version != f.m.Version+1 {
		t.Fatalf("stored %+v", got)
	}
	if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, f.mail.code(t)); !errors.Is(err, ErrPayActionNoPending) {
		t.Fatalf("a used code: err = %v", err)
	}
}

func TestPayMerchant_OwnerBankChangeConfirmRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("the account resolving to another name at confirm", func(t *testing.T) {
		f := ownedMerchant(t, "", "")
		f.banks.name = "SKY AIR LIMITED"
		res := f.request(t, bankChange("9876543210"))
		f.banks.name = "SKY AIR LTD" // still the business, but not the name the owner confirmed
		if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, f.mail.code(t)); !errors.Is(err, ErrPayAccountUnverifiable) {
			t.Fatalf("err = %v", err)
		}
		if f.stored(t).AccountNumber == "9876543210" {
			t.Fatal("an account that changed name since the request was applied")
		}
	})
	t.Run("an account that stopped resolving", func(t *testing.T) {
		f := ownedMerchant(t, "", "")
		res := f.request(t, bankChange("9876543210"))
		f.banks.err = errors.New("nomba: down")
		if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, f.mail.code(t)); !errors.Is(err, ErrPayAccountUnverifiable) {
			t.Fatalf("err = %v", err)
		}
		if f.stored(t).AccountNumber == "9876543210" {
			t.Fatal("an unresolvable account was applied")
		}
	})
	t.Run("an edit since the request makes it stale", func(t *testing.T) {
		f := ownedMerchant(t, "", "")
		res := f.request(t, bankChange("9876543210"))
		if _, _, err := f.s.Update(ctx, payActor, f.m.ID, PayMerchantUpdate{BusinessName: payStr("Sky Air Nigeria")}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, f.mail.code(t)); !errors.Is(err, ErrPayMerchantStale) {
			t.Fatalf("err = %v", err)
		}
		if f.stored(t).AccountNumber == "9876543210" {
			t.Fatal("a stale confirmation applied the bank account")
		}
	})
}

func TestPayMerchant_OwnerPayoutChangesReplaceEachOther(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	wallet := f.requestWallet(t)
	walletCode := f.mail.code(t)
	bank := f.request(t, bankChange("9876543210"))
	if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, wallet.Pending.ID, walletCode); !errors.Is(err, ErrPayActionNoPending) {
		t.Fatalf("a wallet change the bank change replaced: err = %v", err)
	}
	if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, bank.Pending.ID, f.mail.code(t)); err != nil {
		t.Fatal(err)
	}
	if got := f.stored(t); got.SettlementMode != models.PaySettlementFiat || got.AccountNumber != "9876543210" {
		t.Fatalf("stored %+v", got)
	}
}

func TestPayMerchant_OwnerWalletChangeRequestedBeforeBankCodesStillConfirms(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	res := f.requestWallet(t)
	legacy := `{"chainId":8453,"tokenAddress":"` + ownerBaseUSDC + `","tokenSymbol":"USDC","walletAddress":"` + ownerWallet + `"}`
	if err := f.s.db.Model(&models.PayOwnerAction{}).Where("id = ?", res.Pending.ID).Update("payload", legacy).Error; err != nil {
		t.Fatal(err)
	}
	r, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, f.mail.code(t))
	if err != nil || r.Merchant.SettlementMode != models.PaySettlementCrypto || r.Merchant.WalletAddress != ownerWallet || r.Merchant.TokenSymbol != "USDC" {
		t.Fatalf("err %v result %+v", err, r)
	}
}

func TestPayMerchant_OwnerWalletChangeWaitsForTheEmailedCode(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	res, err := f.s.UpdateSettlementByOwner(ctx, f.owner, walletChange())
	if err != nil || res.Pending == nil {
		t.Fatalf("err %v result %+v", err, res)
	}
	if got := f.stored(t); got.SettlementMode == models.PaySettlementCrypto || got.Version != f.m.Version {
		t.Fatalf("the wallet was applied before the code was entered: %+v", got)
	}
	if f.mail.sent != 1 || f.mail.to != "owner@skyair.ng" || res.Pending.SentTo != "o***@skyair.ng" ||
		!strings.Contains(f.mail.message, "0x5290…9EE7") {
		t.Fatalf("mail %+v pending %+v", f.mail, res.Pending)
	}
	wrong := "000000"
	if f.mail.code(t) == wrong {
		wrong = "111111"
	}
	if res.Pending.Kind != models.PayOwnerActionSettlement {
		t.Fatalf("pending kind %q", res.Pending.Kind)
	}
	if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, wrong); !errors.Is(err, ErrPayCodeInvalid) {
		t.Fatalf("a wrong code: err = %v", err)
	}
	r, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, f.mail.code(t))
	if err != nil || r.Merchant.SettlementMode != models.PaySettlementCrypto || r.Merchant.WalletAddress != ownerWallet || r.Merchant.TokenSymbol != "USDC" {
		t.Fatalf("err %v result %+v", err, r)
	}
	if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, f.mail.code(t)); !errors.Is(err, ErrPayActionNoPending) {
		t.Fatalf("a used code: err = %v", err)
	}
}

func TestPayMerchant_OwnerWalletCodeRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("too many wrong codes end the change", func(t *testing.T) {
		f := ownedMerchant(t, "", "")
		res := f.requestWallet(t)
		wrong := "000000"
		if f.mail.code(t) == wrong {
			wrong = "111111"
		}
		for i := 0; i < EmailCodeAttempts; i++ {
			if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, wrong); !errors.Is(err, ErrPayCodeInvalid) {
				t.Fatalf("attempt %d: err = %v", i, err)
			}
		}
		if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, f.mail.code(t)); !errors.Is(err, ErrPayActionNoPending) {
			t.Fatalf("the right code after the attempts ran out: err = %v", err)
		}
	})
	t.Run("an expired code", func(t *testing.T) {
		f := ownedMerchant(t, "", "")
		res := f.requestWallet(t)
		f.s.now = func() time.Time { return payTestNow.Add(EmailCodeTTL) }
		if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, f.mail.code(t)); !errors.Is(err, ErrPayActionNoPending) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an edit since the request makes it stale", func(t *testing.T) {
		f := ownedMerchant(t, "", "")
		res := f.requestWallet(t)
		if _, _, err := f.s.Update(ctx, payActor, f.m.ID, PayMerchantUpdate{BusinessName: payStr("Sky Air Nigeria")}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, f.mail.code(t)); !errors.Is(err, ErrPayMerchantStale) {
			t.Fatalf("err = %v", err)
		}
		if f.stored(t).SettlementMode == models.PaySettlementCrypto {
			t.Fatal("a stale confirmation applied the wallet")
		}
	})
	t.Run("another user's code is no code", func(t *testing.T) {
		f := ownedMerchant(t, "", "")
		res := f.requestWallet(t)
		if _, err := f.s.ConfirmOwnerAction(ctx, uuid.New(), res.Pending.ID, f.mail.code(t)); !errors.Is(err, ErrPayMerchantNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no way to send a code refuses the change and leaves nothing waiting", func(t *testing.T) {
		f := ownedMerchant(t, "", "")
		f.mail.err = errors.New("smtp down")
		if _, err := f.s.UpdateSettlementByOwner(ctx, f.owner, walletChange()); !errors.Is(err, ErrPayCodeUnavailable) {
			t.Fatalf("err = %v", err)
		}
		var n int64
		f.s.db.Model(&models.PayOwnerAction{}).Count(&n)
		if n != 0 {
			t.Fatalf("%d changes left waiting on a code nobody received", n)
		}
		f.s.SetMailer(nil)
		if _, err := f.s.UpdateSettlementByOwner(ctx, f.owner, walletChange()); !errors.Is(err, ErrPayCodeUnavailable) {
			t.Fatalf("no mailer: err = %v", err)
		}
	})
}

func TestPayMerchant_OwnerWalletIsAListedStablecoinWithItsListedSymbol(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	for name, ch := range map[string]PaySettlementChange{
		"a token that isn't listed":            {SettlementMode: "crypto", ChainID: 8453, TokenAddress: "0x4200000000000000000000000000000000000006", WalletAddress: ownerWallet},
		"a listed token on the wrong chain":    {SettlementMode: "crypto", ChainID: 56, TokenAddress: ownerBaseUSDC, WalletAddress: ownerWallet},
		"a chain no delivery can be proven on": {SettlementMode: "crypto", ChainID: 999, TokenAddress: ownerBaseUSDC, WalletAddress: ownerWallet},
	} {
		if _, err := f.s.UpdateSettlementByOwner(ctx, f.owner, ch); !errors.Is(err, ErrPayMerchantInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	res := f.requestWallet(t)
	r, err := f.s.ConfirmOwnerAction(ctx, f.owner, res.Pending.ID, f.mail.code(t))
	if err != nil || r.Merchant.TokenSymbol != "USDC" {
		t.Fatalf("err %v result %+v: the symbol is the listing's, never the caller's", err, r)
	}
}

func TestPayMerchant_OwnerSettlementToWhereTheyArePaidAlreadyChangesNothing(t *testing.T) {
	f := ownedMerchant(t, "", "")
	f.banks.name = "SKY AIR LTD"
	same := PaySettlementChange{SettlementMode: models.PaySettlementFiat, BankCode: f.m.BankCode, AccountNumber: f.m.AccountNumber}
	res, err := f.s.UpdateSettlementByOwner(context.Background(), f.owner, same)
	if err != nil || res.Pending != nil || res.Merchant == nil {
		t.Fatalf("err %v result %+v", err, res)
	}
	f.nothingWaiting(t)
	var audits int64
	f.s.db.Model(&models.PayAuditEvent{}).Where("merchant_id = ?", f.m.ID).Count(&audits)
	if got := f.stored(t); got.Version != f.m.Version || audits != 0 {
		t.Fatalf("a no-op wrote version %d and %d audit rows", got.Version, audits)
	}
}

func TestPayMerchant_OwnerSettlementRefusals(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	f.banks.name = "SKY AIR LTD"
	if _, err := f.s.UpdateSettlementByOwner(ctx, uuid.New(), bankChange("9876543210")); !errors.Is(err, ErrPayMerchantNotFound) {
		t.Fatalf("someone else's change: err = %v", err)
	}
	if _, err := f.s.UpdateSettlementByOwner(ctx, f.owner, PaySettlementChange{SettlementMode: ""}); !errors.Is(err, ErrPayMerchantInvalid) {
		t.Fatalf("unconfiguring: err = %v", err)
	}
	stale := f.m.Version - 1
	ch := bankChange("9876543210")
	ch.ExpectedVersion = &stale
	if _, err := f.s.UpdateSettlementByOwner(ctx, f.owner, ch); !errors.Is(err, ErrPayMerchantStale) {
		t.Fatalf("from a stale screen: err = %v", err)
	}
	if err := f.s.db.Model(f.m).Update("status", models.PayMerchantSuspended).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.UpdateSettlementByOwner(ctx, f.owner, bankChange("9876543210")); !errors.Is(err, ErrPayMerchantSuspended) {
		t.Fatalf("suspended: err = %v", err)
	}
}

func TestPayMerchant_OwnerWithTwoMerchantsIsRefusedNotGuessed(t *testing.T) {
	f := ownedMerchant(t, "", "")
	second := payRequest()
	second.ID, second.UserID = uuid.New(), f.owner
	if _, err := f.s.CreateFromRequest(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GetByOwner(context.Background(), f.owner); !errors.Is(err, ErrPayMerchantAmbiguous) {
		t.Fatalf("err = %v", err)
	}
}

func TestPayMerchant_AConfirmedActionCannotBeConfirmedAgain(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	pending, err := f.s.RequestKeyRoll(ctx, f.owner, models.PayModeTest)
	if err != nil {
		t.Fatal(err)
	}
	code := f.mail.code(t)
	if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, pending.ID, code); err != nil {
		t.Fatal(err)
	}
	afterFirst := *f.stored(t).TestKeyHash
	if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, pending.ID, code); !errors.Is(err, ErrPayActionNoPending) {
		t.Fatalf("second confirm: err = %v", err)
	}
	if *f.stored(t).TestKeyHash != afterFirst {
		t.Fatal("a second confirm minted another key")
	}
}

func TestPayMerchant_OwnerKeyRollIsGuardedAndReturnsTheKeyOnce(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	if _, err := f.s.IssueKeys(ctx, payActor, f.m.ID, PayKeysAll); err != nil {
		t.Fatal(err)
	}
	before := f.stored(t)
	pending, err := f.s.RequestKeyRoll(ctx, f.owner, models.PayModeLive)
	if err != nil || pending.Kind != models.PayOwnerActionKeys {
		t.Fatalf("err %v pending %+v", err, pending)
	}
	if *f.stored(t).LiveKeyHash != *before.LiveKeyHash {
		t.Fatal("the key rolled before the code was entered")
	}
	res, err := f.s.ConfirmOwnerAction(ctx, f.owner, pending.ID, f.mail.code(t))
	if err != nil || res.Mode != models.PayModeLive || !strings.HasPrefix(res.Key, "tsapk_live_") {
		t.Fatalf("err %v res %+v", err, res)
	}
	if *f.stored(t).LiveKeyHash == *before.LiveKeyHash || *f.stored(t).TestKeyHash != *before.TestKeyHash {
		t.Fatal("rolling live must change the live key and only the live key")
	}
	if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, pending.ID, f.mail.code(t)); !errors.Is(err, ErrPayActionNoPending) {
		t.Fatalf("a second confirm: err = %v", err)
	}
	if _, err := f.s.RequestKeyRoll(ctx, f.owner, "staging"); !errors.Is(err, ErrPayMerchantInvalid) {
		t.Fatalf("unknown mode: err = %v", err)
	}
}

func TestPayMerchant_OwnerEndpointsAreValidatedBeforeACodeIsSent(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	if _, err := f.s.RequestEndpointsChange(ctx, f.owner, PayEndpointsChange{WebhookURL: "http://127.0.0.1/hook"}); !errors.Is(err, ErrPayMerchantInvalid) || f.mail.sent != 0 {
		t.Fatalf("err %v sent %d: a private webhook URL must be refused before any code", err, f.mail.sent)
	}
	pending, err := f.s.RequestEndpointsChange(ctx, f.owner, PayEndpointsChange{
		WebhookURL: "https://api.skyair.ng/tsa/webhook", ReturnDomains: []string{"skyair.ng"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.stored(t); got.WebhookURL != "" {
		t.Fatalf("the webhook changed before the code was entered: %+v", got)
	}
	res, err := f.s.ConfirmOwnerAction(ctx, f.owner, pending.ID, f.mail.code(t))
	if err != nil || res.Merchant.WebhookURL != "https://api.skyair.ng/tsa/webhook" || res.Merchant.ReturnDomains != "skyair.ng" {
		t.Fatalf("err %v merchant %+v", err, res.Merchant)
	}
}

func TestPayMerchant_OwnerWebhookSecretRevealAndRoll(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	reveal, err := f.s.RequestWebhookSecret(ctx, f.owner, "reveal")
	if err != nil {
		t.Fatal(err)
	}
	r1, err := f.s.ConfirmOwnerAction(ctx, f.owner, reveal.ID, f.mail.code(t))
	if err != nil || r1.WebhookSecretLive == "" || r1.WebhookSecretLive != f.s.WebhookSecret(f.stored(t), models.PayModeLive) {
		t.Fatalf("reveal: err %v res %+v", err, r1)
	}
	roll, err := f.s.RequestWebhookSecret(ctx, f.owner, "roll")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := f.s.ConfirmOwnerAction(ctx, f.owner, roll.ID, f.mail.code(t))
	if err != nil || r2.WebhookSecretLive == r1.WebhookSecretLive || r2.SecretAction != "roll" {
		t.Fatalf("roll: err %v — the secret must change", err)
	}
	if _, err := f.s.RequestWebhookSecret(ctx, f.owner, "delete"); !errors.Is(err, ErrPayMerchantInvalid) {
		t.Fatalf("unknown action: err = %v", err)
	}
}

func TestPayMerchant_OwnerActionsBelongToTheirOwnerAndKind(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	first, _ := f.s.RequestKeyRoll(ctx, f.owner, models.PayModeTest)
	firstCode := f.mail.code(t)
	second, _ := f.s.RequestKeyRoll(ctx, f.owner, models.PayModeTest)
	if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, first.ID, firstCode); !errors.Is(err, ErrPayActionNoPending) {
		t.Fatalf("a superseded request: err = %v", err)
	}
	if _, err := f.s.ConfirmOwnerAction(ctx, uuid.New(), second.ID, f.mail.code(t)); !errors.Is(err, ErrPayMerchantNotFound) {
		t.Fatalf("someone else's action: err = %v", err)
	}
}

func TestPayMerchant_SettlementOptionsAreTheListedStablecoins(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	if got := s.SettlementOptions(); len(got) != 0 {
		t.Fatalf("no chain registry: %+v", got)
	}
	s.chains = fakePayChains{8453: "base", 56: "bsc"}
	got := s.SettlementOptions()
	want := []PaySettlementOption{
		{ChainID: 56, Chain: "bsc", Symbol: "USDT", TokenAddress: fakePayListedTokens["bsc:USDT"]},
		{ChainID: 8453, Chain: "base", Symbol: "USDC", TokenAddress: fakePayListedTokens["base:USDC"]},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("options %+v, want %+v", got, want)
	}
}

func TestPayMerchant_BankDirectoryForOwners(t *testing.T) {
	ctx := context.Background()
	s, banks := newPayMerchantSvc(t)
	list, err := s.Banks(ctx)
	if err != nil || len(list) != 1 || list[0].Code != "044" {
		t.Fatalf("banks %+v err %v", list, err)
	}
	res, err := s.ResolveBank(ctx, "044", "0123456789")
	if err != nil || res.AccountName != "SKY AIR LIMITED" || banks.calls != 1 {
		t.Fatalf("resolve %+v err %v", res, err)
	}
	s.banks = nil
	if _, err := s.Banks(ctx); !errors.Is(err, ErrPayBanksUnavailable) {
		t.Fatalf("no directory, list: err = %v", err)
	}
	if _, err := s.ResolveBank(ctx, "044", "0123456789"); !errors.Is(err, ErrPayBanksUnavailable) {
		t.Fatalf("no directory, resolve: err = %v", err)
	}
}

// raceOwnerActionInsert makes another request's action of the same kind land
// between requestOwnerAction's delete and its insert, times times.
func raceOwnerActionInsert(t *testing.T, f *ownerFixture, kind string, times int) {
	t.Helper()
	left := times
	if err := f.s.db.Callback().Create().Before("gorm:create").Register("test:race_owner_action", func(tx *gorm.DB) {
		if tx.Statement.Table != "pay_owner_actions" || left == 0 {
			return
		}
		left--
		if err := tx.Session(&gorm.Session{NewDB: true}).Exec(
			`INSERT INTO pay_owner_actions (id, merchant_id, kind, owner_id, payload, base_version, code_hash, attempts, expires_at, created_at)
			 VALUES (?, ?, ?, ?, '{}', 0, 'x', 0, ?, ?)`, uuid.New(), f.m.ID, kind, f.owner, payTestNow.Add(time.Hour), payTestNow).Error; err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPayMerchant_AConcurrentRequestOfTheSameKindIsRetriedOnce(t *testing.T) {
	ctx := context.Background()
	f := ownedMerchant(t, "", "")
	raceOwnerActionInsert(t, f, models.PayOwnerActionKeys, 1)
	pending, err := f.s.RequestKeyRoll(ctx, f.owner, models.PayModeLive)
	if err != nil {
		t.Fatalf("one lost race must be retried: err = %v", err)
	}
	if _, err := f.s.ConfirmOwnerAction(ctx, f.owner, pending.ID, f.mail.code(t)); err != nil {
		t.Fatalf("the retried action: err = %v", err)
	}
}

func TestPayMerchant_ARequestThatKeepsLosingTheRaceIsBusy(t *testing.T) {
	f := ownedMerchant(t, "", "")
	raceOwnerActionInsert(t, f, models.PayOwnerActionKeys, 2)
	if _, err := f.s.RequestKeyRoll(context.Background(), f.owner, models.PayModeLive); !errors.Is(err, ErrPayActionBusy) {
		t.Fatalf("err = %v, want ErrPayActionBusy", err)
	}
	if f.mail.sent != 0 {
		t.Fatal("a code was sent for an action that was never stored")
	}
}

func TestPayMerchant_TheLiveSwitchIsSensitiveAndAudited(t *testing.T) {
	off := false
	if !(PayMerchantUpdate{LiveEnabled: &off}).Sensitive() {
		t.Fatal("the live switch must need a super admin")
	}
	s, _ := newPayMerchantSvc(t)
	m, _ := s.CreateFromRequest(context.Background(), payRequest())
	if !m.LiveEnabled {
		t.Fatal("an approved merchant must be live")
	}
	got, before, err := s.Update(context.Background(), payActor, m.ID, PayMerchantUpdate{LiveEnabled: &off})
	if err != nil || got.LiveEnabled || !before.LiveEnabled || before.Version != m.Version {
		t.Fatalf("switch off: %+v before %+v err %v", got, before, err)
	}
	stored, _ := s.Get(context.Background(), m.ID)
	if stored.LiveEnabled {
		t.Fatal("the switch did not persist")
	}
	rows := payAuditRows(t, s)
	if len(rows) != 1 || rows[0].ActorID != payActor || !strings.Contains(rows[0].Details, `"liveEnabled":{"after":false,"before":true}`) {
		t.Fatalf("audit rows = %+v", rows)
	}
}

func TestPayMerchant_RevokeKeysSwitchesOffOneCredentialAndMintsNothing(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	ctx := context.Background()
	m, _ := s.CreateFromRequest(ctx, payRequest())
	keys, _ := s.IssueKeys(ctx, payActor, m.ID, PayKeysAll)

	revoked, err := s.RevokeKeys(ctx, payActor, m.ID, PayKeysLive, "  leaked in a\n public   repo  ")
	if err != nil || revoked.LiveKeyHash != nil || revoked.LiveKeyPrefix != "" {
		t.Fatalf("revoke live: %+v %v", revoked, err)
	}
	if _, _, err := s.Authenticate(ctx, keys.LiveKey); !errors.Is(err, ErrPayKeyInvalid) {
		t.Fatalf("the revoked live key still works: %v", err)
	}
	if _, mode, err := s.Authenticate(ctx, keys.TestKey); err != nil || mode != models.PayModeTest {
		t.Fatalf("revoking live must leave the test key alone: %v", err)
	}

	if _, err := s.RevokeKeys(ctx, payActor, m.ID, PayKeysTest, "merchant asked"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, keys.TestKey); !errors.Is(err, ErrPayKeyInvalid) {
		t.Fatal("the revoked test key still works")
	}

	wh, err := s.RevokeKeys(ctx, payActor, m.ID, PayKeysWebhook, "secret posted in chat")
	if err != nil || s.WebhookSecret(wh, models.PayModeLive) == keys.WebhookSecretLive || s.WebhookSecret(wh, models.PayModeTest) == keys.WebhookSecretTest {
		t.Fatalf("webhook revoke must re-key both secrets: %v", err)
	}

	rows := payAuditRows(t, s)
	last := rows[len(rows)-1]
	if last.Action != models.PayAuditActionMerchantKeysRevoked || last.ActorID != payActor ||
		!strings.Contains(last.Details, `"scope":"webhook"`) || !strings.Contains(last.Details, `"reason":"secret posted in chat"`) {
		t.Fatalf("audit row = %+v", last)
	}
	var liveRow *models.PayAuditEvent
	for i := range rows {
		if rows[i].Action == models.PayAuditActionMerchantKeysRevoked && strings.Contains(rows[i].Details, `"scope":"live"`) {
			liveRow = &rows[i]
		}
	}
	if liveRow == nil || !strings.Contains(liveRow.Details, `"reason":"leaked in a public repo"`) {
		t.Fatalf("the reason must be stored trimmed: %+v", rows)
	}
}

func TestPayMerchant_RevokeKeysNeverKillsACredentialItDidNotRead(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	ctx := context.Background()
	m, err := s.CreateFromRequest(ctx, payRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueKeys(ctx, payActor, m.ID, PayKeysAll); err != nil {
		t.Fatal(err)
	}
	stale, _ := s.Get(ctx, m.ID)

	// The owner rolls their live key, and another admin re-keys the webhook
	// secrets, after this admin read the merchant.
	fresh, err := s.IssueKeys(ctx, payActor, m.ID, PayKeysLive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeKeys(ctx, payActor, m.ID, PayKeysWebhook, "first admin"); err != nil {
		t.Fatal(err)
	}
	before := len(payAuditRows(t, s))

	for _, scope := range []PayKeyScope{PayKeysLive, PayKeysWebhook} {
		if _, err := s.revoke(ctx, payActor, stale, scope, "stale read"); !errors.Is(err, ErrPayMerchantStale) {
			t.Fatalf("%s from a stale read: err = %v, want ErrPayMerchantStale", scope, err)
		}
	}
	if _, mode, err := s.Authenticate(ctx, fresh.LiveKey); err != nil || mode != models.PayModeLive {
		t.Fatalf("the owner's new live key was revoked by a stale read: %v", err)
	}
	stored, _ := s.Get(ctx, m.ID)
	if stored.WebhookSecretVersion != stale.WebhookSecretVersion+1 {
		t.Fatalf("webhook version = %d, want one bump", stored.WebhookSecretVersion)
	}
	if after := len(payAuditRows(t, s)); after != before {
		t.Fatalf("a refused revoke wrote %d audit rows", after-before)
	}
}

func TestPayMerchant_RevokeKeysRefusesBadInput(t *testing.T) {
	s, _ := newPayMerchantSvc(t)
	ctx := context.Background()
	m, _ := s.CreateFromRequest(ctx, payRequest())
	s.IssueKeys(ctx, payActor, m.ID, PayKeysTest)

	for name, tc := range map[string]struct {
		scope  PayKeyScope
		reason string
	}{
		"no reason":          {PayKeysTest, "   "},
		"reason too long":    {PayKeysTest, strings.Repeat("x", 281)},
		"all is not revoked": {PayKeysAll, "leaked"},
		"unknown scope":      {"everything", "leaked"},
		"no live key yet":    {PayKeysLive, "leaked"},
		"a link":             {PayKeysTest, "make a new key at https://tsa-help.support/keys"},
		"a bare domain":      {PayKeysTest, "make a new key at tsa-help.support"},
		"www":                {PayKeysTest, "see www.example"},
		"bidi override":      {PayKeysTest, "leaked \u202egnp.exe"},
		"zero-width space":   {PayKeysTest, "leaked\u200b"},
	} {
		if _, err := s.RevokeKeys(ctx, payActor, m.ID, tc.scope, tc.reason); !errors.Is(err, ErrPayMerchantInvalid) {
			t.Fatalf("%s: err = %v, want ErrPayMerchantInvalid", name, err)
		}
	}
	if _, err := s.RevokeKeys(ctx, payActor, uuid.New(), PayKeysTest, "leaked"); !errors.Is(err, ErrPayMerchantNotFound) {
		t.Fatalf("unknown merchant: err = %v", err)
	}
	if rows := payAuditRows(t, s); len(rows) != 1 {
		t.Fatalf("a refused revoke must write no audit row: %+v", rows)
	}
}
