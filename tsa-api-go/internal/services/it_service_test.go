package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// stubFx is a fixed NGN/USD source for tests; fields are set directly on the
// service (s.fx) to simulate stale or unavailable feeds.
//
// It also satisfies FxRateProvider, but PER CURRENCY rather than by returning
// the same number for every code. That distinction is the whole point: a stub
// that ignores its currency argument cannot fail on the bug worth catching —
// an amount denominated in one currency converted at another's rate. With
// NGN≈1400 and GHS≈12 that is a ~100x error, and a single-rate stub would let
// it pass every test.
type stubFx struct {
	ngn   float64
	stale bool
	err   error
	// rates optionally overrides per currency. When nil, only NGN resolves —
	// so a test that reaches for a second currency without saying what it is
	// worth fails loudly instead of silently reusing the naira rate.
	rates map[string]float64
}

func (f stubFx) NgnPerUsd(context.Context) (float64, bool, error) { return f.ngn, f.stale, f.err }

func (f stubFx) RatePerUsd(_ context.Context, currency string) (float64, bool, error) {
	if f.err != nil {
		return 0, f.stale, f.err
	}
	code := strings.ToUpper(strings.TrimSpace(currency))
	if r, ok := f.rates[code]; ok {
		return r, f.stale, nil
	}
	if code == "NGN" {
		return f.ngn, f.stale, nil
	}
	return 0, false, fmt.Errorf("%w: stubFx has no rate for %s", ErrNoMarketAnchor, code)
}

func newITTestService(t *testing.T) *ITService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}, &models.ITRate{}, &models.PaymentMerchant{}, &models.FiatVirtualAccount{}, &models.ITAgreementAcceptance{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	swap := NewMCGPSwapService(nil, &config.Config{Env: "development", MCGPSwapBAddress: "0x00000000000000000000000000000000000bEEF0"})
	swap.SetOperatorSigner(&fakeReleaseSigner{addr: "0x000000000000000000000000000000000000A11CE", hash: "0xdead"})
	svc := NewITService(db, swap, stubFx{ngn: 1500}) // FX = ₦1500/$
	// Fees are deducted on both legs, so a service with no quoter refuses every
	// transfer (ErrITFeeQuoteUnavailable). Wire the live-config engine here so
	// the existing pricing tests exercise the real deduction; tests that care
	// about the unpriced path build their own service.
	svc.SetFeeQuoter(feeQuoterForTest())
	return svc
}

func TestRateMath_RoundsDown(t *testing.T) {
	e18 := oneE18
	// buy: ngnKobo * 1e18 / buyRateKobo. 5000 kobo (₦50) at 100 kobo/MCGP = 50 MCGP.
	got := mcgpForNgn(big.NewInt(5000), big.NewInt(100))
	want := new(big.Int).Mul(big.NewInt(50), e18)
	if got.Cmp(want) != 0 {
		t.Fatalf("mcgpForNgn = %s want %s", got, want)
	}
	// non-divisible rounds down: 101 kobo / 100 = 1.01 MCGP -> floor to 1.01e18 - dust.
	g2 := mcgpForNgn(big.NewInt(101), big.NewInt(100))
	exp := new(big.Int).Div(new(big.Int).Mul(big.NewInt(101), e18), big.NewInt(100))
	if g2.Cmp(exp) != 0 {
		t.Fatalf("rounding mismatch %s vs %s", g2, exp)
	}
	// sell: mcgpWei * sellRateKobo / 1e18. 50 MCGP at 90 kobo = 4500 kobo (₦45).
	sell := ngnForMcgp(new(big.Int).Mul(big.NewInt(50), e18), big.NewInt(90))
	if sell.Cmp(big.NewInt(4500)) != 0 {
		t.Fatalf("ngnForMcgp = %s want 4500", sell)
	}
}

func TestSetRate_Validation(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	admin := uuid.New()

	if _, err := s.SetRate(ctx, admin, 0, 500); err == nil {
		t.Fatal("expected invalid buy price")
	}
	if _, err := s.SetRate(ctx, admin, 20000, 10000); err == nil {
		t.Fatal("expected discount >= 100% rejection")
	}
	if _, err := s.SetRate(ctx, admin, 20000, -1); err == nil {
		t.Fatal("expected negative discount rejection")
	}
	if _, err := s.SetRate(ctx, admin, maxBuyPriceUsdMicros+1, 0); err == nil {
		t.Fatal("expected over-max buy price rejection")
	}
	r, err := s.SetRate(ctx, admin, 20000, 500) // $0.02, 5% discount
	if err != nil {
		t.Fatalf("set rate: %v", err)
	}
	if !r.Active {
		t.Fatal("rate should be active")
	}
	// Setting again deactivates the old one.
	if _, err := s.SetRate(ctx, admin, 21000, 300); err != nil {
		t.Fatalf("set rate 2: %v", err)
	}
	active, err := s.GetActiveRate(ctx)
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if active.BuyPriceUsdMicros != 21000 || active.SellDiscountBps != 300 {
		t.Fatalf("active = %d/%d want 21000/300", active.BuyPriceUsdMicros, active.SellDiscountBps)
	}
	var count int64
	s.db.Model(&models.ITRate{}).Where("active = ?", true).Count(&count)
	if count != 1 {
		t.Fatalf("exactly one active rate expected, got %d", count)
	}
}

func TestEffectiveKoboRates(t *testing.T) {
	s := newITTestService(t) // FX = ₦1500/$
	ctx := context.Background()

	// No rate yet -> ErrITNoRate.
	if _, _, _, err := s.EffectiveKoboRates(ctx); !errors.Is(err, ErrITNoRate) {
		t.Fatalf("expected ErrITNoRate, got %v", err)
	}

	// $0.02 buy, 5% discount: buy = 0.02*1500*100 = 3000 kobo; sell = 0.019*1500*100 = 2850.
	if _, err := s.SetRate(ctx, uuid.New(), 20000, 500); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	buy, sell, fx, err := s.EffectiveKoboRates(ctx)
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if buy.String() != "3000" || sell.String() != "2850" {
		t.Fatalf("got buy=%s sell=%s want 3000/2850", buy, sell)
	}
	if fx.NgnPerUsd != 1500 || fx.Stale {
		t.Fatalf("fx meta = %+v", fx)
	}

	// Zero discount: sell == buy.
	if _, err := s.SetRate(ctx, uuid.New(), 20000, 0); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	if _, sell0, _, _ := s.EffectiveKoboRates(ctx); sell0.String() != "3000" {
		t.Fatalf("zero-discount sell = %s want 3000", sell0)
	}

	// Stale FX is served but flagged.
	s.fx = stubFx{ngn: 1500, stale: true}
	if _, _, fx2, err := s.EffectiveKoboRates(ctx); err != nil || !fx2.Stale {
		t.Fatalf("stale should serve+flag, got err=%v fx=%+v", err, fx2)
	}

	// Unavailable FX blocks pricing.
	s.fx = stubFx{err: errors.New("feed down")}
	if _, _, _, err := s.EffectiveKoboRates(ctx); !errors.Is(err, ErrITFxUnavailable) {
		t.Fatalf("expected ErrITFxUnavailable, got %v", err)
	}
}

func TestCreateBuyIntent_LocksRateAndComputesMcgp(t *testing.T) {
	s := newITTestService(t) // FX = ₦1500/$
	ctx := context.Background()
	if _, err := s.SetRate(ctx, uuid.New(), 20000, 500); err != nil { // $0.02 -> ₦30/MCGP (3000 kobo)
		t.Fatalf("set rate: %v", err)
	}
	seedCompanyPM(t, s) // IT requires a configured pay-into PM
	user := uuid.New()
	intent, err := s.CreateBuyIntent(ctx, user, "0xWALLET", "5000", nil) // ₦50
	if err != nil {
		t.Fatalf("create buy: %v", err)
	}
	// ₦50 in, minus deposit fees: platform 0.5% = 25 kobo, provider 2% = 100
	// kobo, then tax on the provider fee — VAT 7.5% floors to 7 kobo and the
	// 0.5% platform tax floors to 0, so 132 kobo total (each component floors
	// independently, never the sum). ₦48.68 buys MCGP at 3000 kobo/MCGP. The
	// withheld ₦1.32 buys nothing — that MCGP is never minted and stays with
	// the contract float, which is where the platform's 25 kobo is retained.
	if intent.NetNgnKobo != "4868" || intent.FeeNgnKobo != "132" {
		t.Fatalf("fee/net recorded as %s/%s, want 132/4868", intent.FeeNgnKobo, intent.NetNgnKobo)
	}
	if intent.PlatformKeptKobo != "25" {
		t.Fatalf("platformKept = %s, want 25 (the 0.5%% NGN platform fee)", intent.PlatformKeptKobo)
	}
	want := new(big.Int).Div(new(big.Int).Mul(big.NewInt(4868), oneE18), big.NewInt(3000)).String()
	if intent.McgpAmount != want {
		t.Fatalf("mcgp = %s want %s (net-priced, not gross)", intent.McgpAmount, want)
	}
	gross := new(big.Int).Div(new(big.Int).Mul(big.NewInt(5000), oneE18), big.NewInt(3000)).String()
	if intent.McgpAmount == gross {
		t.Fatal("credited the gross-priced amount — the deposit fee was not applied")
	}
	if intent.NgnKobo != "5000" {
		t.Fatalf("ngnKobo = %s, want the gross 5000 (what the user transfers)", intent.NgnKobo)
	}
	if intent.RateKobo != "3000" {
		t.Fatalf("rate not locked: %s want 3000", intent.RateKobo)
	}
	if intent.Status != models.ITStatusPendingPayment {
		t.Fatalf("status = %s", intent.Status)
	}
	if len(intent.PaymentRef) == 0 {
		t.Fatal("paymentRef missing")
	}
}

func TestCreateBuyIntent_BlocksOnStaleFx(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	if _, err := s.SetRate(ctx, uuid.New(), 20000, 500); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	seedCompanyPM(t, s)
	// Stale FX must not lock a real buy quote — the money path refuses it.
	s.fx = stubFx{ngn: 1500, stale: true}
	if _, err := s.CreateBuyIntent(ctx, uuid.New(), "0xWALLET", "5000", nil); err != ErrITFxUnavailable {
		t.Fatalf("expected ErrITFxUnavailable on stale FX, got %v", err)
	}
	var n int64
	s.db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 {
		t.Fatalf("no intent should be created on stale FX, got %d", n)
	}
}

func TestItRef_DomainSeparated(t *testing.T) {
	id := uuid.New()
	credit := itRef("IT:credit:", id)
	debit := itRef("IT:debit:", id)
	if credit == debit {
		t.Fatal("credit and debit refs must differ for the same intent")
	}
	if itRef("IT:credit:", id) != credit {
		t.Fatal("itRef must be deterministic")
	}
}

func TestCreateBuyIntent_NoRate(t *testing.T) {
	s := newITTestService(t)
	if _, err := s.CreateBuyIntent(context.Background(), uuid.New(), "0xWALLET", "1000", nil); err == nil {
		t.Fatal("expected ErrITNoRate when no rate set")
	}
}

// seedCompanyPM inserts an active PM with an inbound account and pins it as the
// IT company PM (IT_PM_ID). Returns the PM id.
func seedCompanyPM(t *testing.T, s *ITService) uuid.UUID {
	t.Helper()
	pm := &models.PaymentMerchant{
		ID: uuid.New(), UserID: uuid.New(), Name: "Company PM",
		Status: models.PaymentMerchantStatusActive, InboundAccountNumber: "2998287753", InboundBank: "Nomba",
	}
	if err := s.db.Create(pm).Error; err != nil {
		t.Fatalf("seed pm: %v", err)
	}
	s.SetCompanyPMID(pm.ID.String())
	return pm.ID
}

func TestCompanyPM_AlwaysConfiguredNoFallback(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	// Another active PM exists, but IT must NOT pick it.
	other := &models.PaymentMerchant{ID: uuid.New(), UserID: uuid.New(), Name: "Other", Status: models.PaymentMerchantStatusActive, InboundAccountNumber: "111"}
	s.db.Create(other)

	// IT_PM_ID unset -> nil (no fallback to `other`).
	pm, err := s.CompanyPM(ctx)
	if err != nil || pm != nil {
		t.Fatalf("expected nil when IT_PM_ID unset, got pm=%v err=%v", pm, err)
	}
	// IT_PM_ID pinned -> exactly that PM.
	id := seedCompanyPM(t, s)
	pm, err = s.CompanyPM(ctx)
	if err != nil || pm == nil || pm.ID != id {
		t.Fatalf("expected the configured PM, got pm=%v err=%v", pm, err)
	}
}

// ─── Sell: destination currency + rail (Task 2.4) ──────────────────────────
//
// CreateSellIntent's happy path calls the live chain (PremiumCredited, then
// OperatorDebitPremium/VerifyOperatorEvent) before it ever persists the
// intent, and nothing in this package mocks that client (client=nil errors
// "blockchain client not available" — true of every IT sell path today, not
// new to this task). So the mapping CreateSellIntent uses to build the
// intent's destination fields is extracted into applySellBank (in
// it_automation.go) and exercised directly here; the fail-fast rail
// validation runs before any chain call and IS exercised end-to-end through
// CreateSellIntent below.

func TestCreateSellIntent_MobileMoneyValidationFailsFast(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	// Missing MobileCountry/MobileMsisdn — must be rejected before any chain
	// call or DB write (no orphan intent, no wasted operator gas).
	bank := &ITSellBank{RailType: "mobile_money", MobileNetwork: "mpesa"}
	if _, err := s.CreateSellIntent(ctx, uuid.New(), "0xWALLET", "1000000000000000000", bank); err == nil {
		t.Fatal("expected error for incomplete mobile-money destination")
	}
	var n int64
	s.db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 {
		t.Fatalf("no intent should be created on failed rail validation, got %d", n)
	}
}

func TestCreateSellIntent_UnknownRailValidationFailsFast(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	bank := &ITSellBank{RailType: "carrier_pigeon"}
	if _, err := s.CreateSellIntent(ctx, uuid.New(), "0xWALLET", "1000000000000000000", bank); err == nil {
		t.Fatal("expected error for unknown payout rail")
	}
}

func TestCreateSellIntent_AppliesMobileMoneyDestination(t *testing.T) {
	intent := &models.ITIntent{}
	bank := &ITSellBank{
		DestinationCurrency: "KES",
		RailType:            "mobile_money",
		MobileNetwork:       "mpesa",
		MobileCountry:       "KE",
		MobileMsisdn:        "254712345678",
	}
	applySellBank(intent, bank)
	if intent.DestinationCurrency != "KES" || intent.RailType != "mobile_money" ||
		intent.MobileNetwork != "mpesa" || intent.MobileCountry != "KE" || intent.MobileMsisdn != "254712345678" {
		t.Fatalf("rail fields not persisted onto intent: %+v", intent)
	}
	if intent.BankCode != "" || intent.AccountNumber != "" || intent.AccountName != "" {
		t.Fatalf("mobile-money sell must not populate legacy bank fields: %+v", intent)
	}
}

// TestCreateSellIntent_NormalizesRailAndCurrencyCase guards against
// case-sensitive downstream consumers (FlutterwaveGateway.InitiatePayout
// switches on rail with a raw string match and checks destCur == "NGN";
// SupportedWithdrawalQuoteCurrencies keys on uppercase codes) silently
// falling through to the wrong branch/allowlist miss when the caller sends
// mixed-case input. applySellBank is the single point of truth for
// normalization, so mixed case in must come out normalized.
func TestCreateSellIntent_NormalizesRailAndCurrencyCase(t *testing.T) {
	intent := &models.ITIntent{}
	bank := &ITSellBank{
		RailType:            "Mobile_Money",
		DestinationCurrency: "ghs",
		MobileCountry:       "sn",
	}
	applySellBank(intent, bank)
	if intent.RailType != "mobile_money" {
		t.Fatalf("RailType not lowercased: got %q", intent.RailType)
	}
	if intent.DestinationCurrency != "GHS" {
		t.Fatalf("DestinationCurrency not uppercased: got %q", intent.DestinationCurrency)
	}
	if intent.MobileCountry != "SN" {
		t.Fatalf("MobileCountry not uppercased: got %q", intent.MobileCountry)
	}
}

func TestCreateSellIntent_AppliesLegacyBankDestination(t *testing.T) {
	intent := &models.ITIntent{}
	bank := &ITSellBank{Code: "044", Number: "0123456789", Name: "JOHN DOE"}
	applySellBank(intent, bank)
	if intent.BankCode != "044" || intent.AccountNumber != "0123456789" || intent.AccountName != "JOHN DOE" {
		t.Fatalf("legacy bank fields not persisted onto intent: %+v", intent)
	}
	if intent.DestinationCurrency != "" || intent.RailType != "" || intent.MobileNetwork != "" ||
		intent.MobileCountry != "" || intent.MobileMsisdn != "" {
		t.Fatalf("NGN-bank sell must not populate rail fields — legacy behavior must stay byte-for-byte unchanged: %+v", intent)
	}
}

func TestCreateSellIntent_NilBankLeavesDestinationEmpty(t *testing.T) {
	intent := &models.ITIntent{}
	applySellBank(intent, nil)
	if intent.BankCode != "" || intent.RailType != "" || intent.DestinationCurrency != "" {
		t.Fatalf("nil bank must leave all destination fields empty: %+v", intent)
	}
}

func TestOpenBuyOrder_WorksWithoutPMAndSupersedesPrior(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()
	if _, err := s.SetRate(ctx, uuid.New(), 20000, 500); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	// Spec 1 standing-VA buys do not require the company PM.
	user := uuid.New()
	first, err := s.OpenBuyOrder(ctx, user, "0xWALLET", "1000")
	if err != nil {
		t.Fatalf("open buy without PM: %v", err)
	}
	second, err := s.OpenBuyOrder(ctx, user, "0xWALLET", "2000")
	if err != nil {
		t.Fatalf("open second buy: %v", err)
	}
	var prior models.ITIntent
	if err := s.db.First(&prior, "id = ?", first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if prior.Status != models.ITStatusCancelled {
		t.Fatalf("prior status = %s, want cancelled", prior.Status)
	}
	if second.Status != models.ITStatusPendingPayment || second.NgnKobo != "2000" {
		t.Fatalf("second = %+v", second)
	}
}
