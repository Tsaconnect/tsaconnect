package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testSellQuote() *StableSellQuote {
	return &StableSellQuote{
		Asset:            "USDC",
		Currency:         "NGN",
		AssetAmount:      big.NewInt(100000000), // 100 USDC
		RatePerUnitMinor: big.NewInt(162467),
		GrossMinor:       big.NewInt(16502500),
		NetMinor:         big.NewInt(16246711),
		ExpiresAt:        time.Now().Add(15 * time.Minute),
	}
}

func testSellBank() *ITSellBank {
	return &ITSellBank{Code: "044", Number: "0690000031", Name: "Ada Okafor"}
}

// newStableSellDBService builds a DB-backed ITService with NO fee quoter.
//
// Deliberately not newITTestService: that fixture wires feeQuoterForTest(), and
// the exact kobo figures asserted here and in the confirm/e2e tests are
// spread-only. Fee behaviour is covered separately, in the quote tests, where a
// stubFeeQuoter is wired explicitly.
//
// Wires a default USDC/USDT token address (via SetStableVerifier, with no
// LiFi/chain verifier — the many callers here never confirm a deposit) so
// CreateStableSellIntent's fail-closed token-address check does not reject
// every test that doesn't care about verification. A test that DOES care
// calls SetStableVerifier again with its own map before creating the intent,
// which simply overwrites this default.
func newStableSellDBService(t *testing.T) *ITService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := NewITService(db, nil, stubFx{ngn: 1500})
	s.SetStableSpreadBps(155)
	s.SetStableRateLockTTL(15 * time.Minute)
	s.SetStableVerifier(nil, nil, map[string]string{"USDC": testToken, "USDT": testToken})
	return s
}

func TestCreateStableSellIntent_PointsTheUserAtFlutterwaveNotUs(t *testing.T) {
	// The platform custodies nothing on this flow. The deposit address handed
	// to the user must be Flutterwave's own — an address of ours here would
	// silently turn a brokerage into a custodial hop.
	const fwAddr = "0x742d35Cc6634C0532925a3b844Bc454e4438f44e"
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", fwAddr)

	_, instr, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "BSC", testSellBank())
	if err != nil {
		t.Fatalf("CreateStableSellIntent: %v", err)
	}
	if instr.Address != fwAddr {
		t.Fatalf("deposit address = %q, want Flutterwave's %q", instr.Address, fwAddr)
	}
	if instr.Chain != "POLYGON" {
		t.Fatalf("deposit chain = %q, want POLYGON", instr.Chain)
	}
}

func TestCreateStableSellIntent_PersistsTheDepositAddress(t *testing.T) {
	// An abandoned sell must be resumable without minting a second intent —
	// GET /it/intents/:id has to be able to show the user where to send their
	// funds again, which means the address has to survive a DB round trip,
	// not just live on the CreateStableSellIntent return value.
	const fwAddr = "0x742d35Cc6634C0532925a3b844Bc454e4438f44e"
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", fwAddr)

	intent, instr, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "BSC", testSellBank())
	if err != nil {
		t.Fatalf("CreateStableSellIntent: %v", err)
	}
	if intent.DepositAddress != fwAddr {
		t.Fatalf("intent.DepositAddress = %q, want %q", intent.DepositAddress, fwAddr)
	}
	if intent.DepositAddress != instr.Address {
		t.Fatalf("intent.DepositAddress (%q) diverges from the returned instructions (%q)", intent.DepositAddress, instr.Address)
	}

	got, err := s.GetIntent(context.Background(), intent.ID)
	if err != nil {
		t.Fatalf("GetIntent: %v", err)
	}
	if got.DepositAddress != fwAddr {
		t.Fatalf("re-read from DB: DepositAddress = %q, want %q", got.DepositAddress, fwAddr)
	}
}

func TestCreateStableSellIntent_PersistsTheLockedQuote(t *testing.T) {
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", "0x742d35Cc6634C0532925a3b844Bc454e4438f44e")

	q := testSellQuote()
	intent, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), q, "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("CreateStableSellIntent: %v", err)
	}
	if intent.Status != models.ITStatusPendingDeposit {
		t.Errorf("status = %q, want %q", intent.Status, models.ITStatusPendingDeposit)
	}
	if intent.Direction != models.ITDirectionSell {
		t.Errorf("direction = %q, want sell", intent.Direction)
	}
	if models.ITIntentAsset(intent) != "USDC" {
		t.Errorf("asset = %q, want USDC", models.ITIntentAsset(intent))
	}
	if intent.AssetAmount != "100000000" {
		t.Errorf("assetAmount = %q, want 100000000", intent.AssetAmount)
	}
	if intent.NgnKobo != "16502500" {
		t.Errorf("gross = %q, want 16502500", intent.NgnKobo)
	}
	if intent.NetNgnKobo != "16246711" {
		t.Errorf("net = %q, want 16246711", intent.NetNgnKobo)
	}
	if intent.LockedRate != "162467" {
		t.Errorf("lockedRate = %q, want 162467", intent.LockedRate)
	}
	if intent.RateExpiresAt == nil {
		t.Error("rateExpiresAt not persisted — the lock would never expire")
	}
	if intent.SourceChain != "POLYGON" {
		t.Errorf("sourceChain = %q, want POLYGON", intent.SourceChain)
	}
}

func TestCreateStableSellIntent_CarriesThePayoutDestination(t *testing.T) {
	// AutoPayout refuses an intent with no destination, so it must be captured
	// at creation — not asked for later, when the user's funds are already gone.
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", "0x742d35Cc6634C0532925a3b844Bc454e4438f44e")

	intent, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("CreateStableSellIntent: %v", err)
	}
	if intent.BankCode != "044" || intent.AccountNumber != "0690000031" {
		t.Fatalf("payout destination not captured: %+v", intent)
	}
}

func TestCreateStableSellIntent_RefusesWithoutADepositAddress(t *testing.T) {
	// With no configured Flutterwave address there is nowhere safe to send the
	// user. Fail closed rather than improvise a destination.
	s := newStableSellDBService(t)
	if _, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "POLYGON", testSellBank()); err == nil {
		t.Fatal("intent created with no deposit address configured")
	}
}

func TestCreateStableSellIntent_RefusesAnExpiredQuote(t *testing.T) {
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", "0x742d35Cc6634C0532925a3b844Bc454e4438f44e")

	q := testSellQuote()
	q.ExpiresAt = time.Now().Add(-time.Second)
	if _, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), q, "POLYGON", testSellBank()); err == nil {
		t.Fatal("expired quote accepted")
	}
}

func TestCreateStableSellIntent_RefusesWithoutAPayoutDestination(t *testing.T) {
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", "0x742d35Cc6634C0532925a3b844Bc454e4438f44e")
	if _, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "POLYGON", nil); err == nil {
		t.Fatal("intent created with no payout destination")
	}
}

func TestCreateStableSellIntent_ReturnsTheConfiguredTokenAddress(t *testing.T) {
	// The app must be handed the EXACT contract ConfirmStableDeposit will
	// verify the transfer against (s.stableTokens[asset]) — never left to
	// resolve it independently, which is exactly the fund-loss path Polygon's
	// native-vs-bridged USDC pair opens up.
	const usdcToken = "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359"
	const usdtToken = "0xc2132D05D31c914a87C6611C10748AEb04B58e8F"
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	s.SetStableVerifier(nil, nil, map[string]string{"USDC": usdcToken, "USDT": usdtToken})

	usdc := testSellQuote()
	_, instr, err := s.CreateStableSellIntent(context.Background(), uuid.New(), usdc, "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("CreateStableSellIntent (USDC): %v", err)
	}
	if instr.TokenAddress != usdcToken {
		t.Fatalf("USDC tokenAddress = %q, want %q", instr.TokenAddress, usdcToken)
	}

	usdt := testSellQuote()
	usdt.Asset = "USDT"
	_, instr, err = s.CreateStableSellIntent(context.Background(), uuid.New(), usdt, "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("CreateStableSellIntent (USDT): %v", err)
	}
	if instr.TokenAddress != usdtToken {
		t.Fatalf("USDT tokenAddress = %q, want %q", instr.TokenAddress, usdtToken)
	}
}

func TestCreateStableSellIntent_RefusesWithoutATokenAddress(t *testing.T) {
	// Handing out a deposit instruction with an empty tokenAddress is worse
	// than refusing the sell outright — the user could send to the wrong
	// contract with no automated way back. Must fail BEFORE the intent row is
	// created, not after.
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	// Wire USDT only — USDC is unconfigured, so a USDC sell must be refused.
	s.SetStableVerifier(nil, nil, map[string]string{"USDT": testToken})

	_, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "POLYGON", testSellBank())
	if !errors.Is(err, ErrStableNoTokenAddress) {
		t.Fatalf("err = %v, want ErrStableNoTokenAddress", err)
	}

	var n int64
	if err := s.db.WithContext(context.Background()).Model(&models.ITIntent{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("intents table has %d rows, want 0 — refused sell must not create an intent", n)
	}
}

// TestQuoteStableSell_RealFeeQuoteServiceStaysProportional guards against the
// exact denomination bug the plan text shipped once already (see
// applyStableSellFees's comment): FeeQuoteInput.Amount is major units, our
// internal figures are minor units. A stub fee quoter ignores its Amount
// input entirely and would pass either way, so this exercises the REAL
// FeeQuoteService end to end and asserts the fee lands in the right order of
// magnitude relative to the spread — not ~100x off it.
func TestQuoteStableSell_RealFeeQuoteServiceStaysProportional(t *testing.T) {
	q := &stubStableRateQuoter{rate: FWRate{
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1650.25},
	}}
	fq := NewFeeQuoteService(feeTestConfig(), nil)
	fq.SetBrokeredStableFlows(true)

	s := newStableSellTestService(t, q)
	// The real fee engine's stablecoin platform fee is a flat USD amount
	// (PlatformStableUSD) folded into the quote currency via
	// FeeQuoteInput.FxMinorPerUSD (fee_quote_service.go), resolved from
	// s.fx by ratePerUsdFor (it_fees.go). newStableSellTestService
	// deliberately wires no FxProvider so the spread-only arithmetic tests
	// stay isolated — but that means the real engine has no USD→NGN rate to
	// convert the flat fee with, and correctly refuses with
	// ErrFeeQuoteNeedFx. Wire one here, only for this test.
	s.fx = stubFx{ngn: 1500}
	s.SetFeeQuoter(fq)

	got, err := s.QuoteStableSell(context.Background(), "USDC", "100", "NGN")
	if err != nil {
		t.Fatalf("QuoteStableSell: %v", err)
	}
	if got.FeeMinor.Sign() <= 0 {
		t.Fatalf("fee = %s, want a positive fee from the real fee engine", got.FeeMinor)
	}
	if got.FeeMinor.Cmp(got.SpreadMinor) >= 0 {
		t.Fatalf("fee %s is not less than the spread %s — proportionality broken", got.FeeMinor, got.SpreadMinor)
	}
	// The legitimate fee here is a flat $2 platform fee (₦3,000 at the wired
	// ₦1500/$ rate) plus a 2% provider fallback on the ₦162,467.11 spread plus
	// VAT on the provider leg — roughly 4% of the spread, ~650,928 kobo. A
	// major/minor denomination error (passing SpreadMinor's kobo string where
	// a naira-major decimal is expected) inflates the AMOUNT the provider fee
	// is a percentage of by 100x, which inflates the total fee by very close
	// to 100x too — nowhere near this fee's actual ~4%. Assert well below
	// that: comfortably wide of the true ~4% so this isn't brittle to config
	// tweaks, but two full orders of magnitude short of what a 100x bug would
	// produce (that would push the fee past 100% of the spread — the widest
	// margin already checked above — well before it got anywhere near this
	// tighter bound).
	tenPercentOfSpread := new(big.Int).Div(got.SpreadMinor, big.NewInt(10))
	if got.FeeMinor.Cmp(tenPercentOfSpread) >= 0 {
		t.Fatalf("fee %s is >= 10%% of spread %s — looks like a major/minor unit bug (~100x)", got.FeeMinor, got.SpreadMinor)
	}
}

func TestCreateStableSellIntent_RejectsBelowProviderMinimum(t *testing.T) {
	// Defence in depth. PostStableSell re-quotes server-side, so a below-floor
	// quote cannot arrive here over HTTP — but this method is exported, and a
	// hand-built quote would otherwise mint an intent whose deposit address is
	// handed to a user for a trade Flutterwave will refuse to convert.
	s := newStableSellDBService(t)

	q := testSellQuote()
	q.AssetAmount = big.NewInt(9_999_999) // 9.999999 USDC — one unit under

	_, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), q, "POLYGON", testSellBank())
	if !errors.Is(err, ErrStableBelowMinimum) {
		t.Fatalf("err = %v, want ErrStableBelowMinimum", err)
	}
}

func TestCreateStableSellIntent_RejectsNilAssetAmount(t *testing.T) {
	// A nil amount must not be read as "zero and therefore fine" — it reaches
	// the floor comparison before any intent is written.
	s := newStableSellDBService(t)

	q := testSellQuote()
	q.AssetAmount = nil

	_, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), q, "POLYGON", testSellBank())
	if !errors.Is(err, ErrStableBelowMinimum) {
		t.Fatalf("err = %v, want ErrStableBelowMinimum", err)
	}
}
