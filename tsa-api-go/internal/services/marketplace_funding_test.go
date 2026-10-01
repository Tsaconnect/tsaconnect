package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// marketplace_funding_test.go — paying a marketplace checkout with any token.
//
// The funding is sized by the server from the checkout's own orders, less what
// her wallet already holds, and lands in HER wallet on Sonic; the escrow is then
// paid from there as usual. These pin what can cost her money or strand an
// order: the amount, the price model, the one-open rule, the proof of delivery,
// the Sonic gas, and the orders' TTL while a slow bridge is in flight.

// ─── fakes ─────────────────────────────────────────────────────────────────

// fakeFundingQuoter is the funding's quoter AND the intents' quoter (one
// instance, as in production): every Quote it answers is recorded.
type fakeFundingQuoter struct {
	fakeSellQuoter
	refuse   error
	sPrice   *big.Rat
	priceErr error
}

// Quote answers the fixture route re-sized to what was asked, as LiFi does for
// an exact-output ask: it delivers ToAmount, worth its face value, with a floor
// 10 bps under it.
func (f *fakeFundingQuoter) Quote(ctx context.Context, in SellQuoteInput) (*SellSwapQuote, error) {
	q, err := f.fakeSellQuoter.Quote(ctx, in)
	if err != nil || q == nil || q.Route == nil || in.ToAmount == "" {
		return q, err
	}
	to, _ := new(big.Int).SetString(in.ToAmount, 10)
	cp, route := *q, *q.Route
	route.ToAmount = to
	route.ToAmountMin = new(big.Int).Div(new(big.Int).Mul(to, big.NewInt(9_990)), big.NewInt(10_000))
	route.ToAmountUSD = stableUSD(to)
	cp.Route = &route
	return &cp, nil
}

func (f *fakeFundingQuoter) RefuseSource(int64, string) error { return f.refuse }
func (f *fakeFundingQuoter) TokenPriceUSD(context.Context, int64, string) (*big.Rat, int, error) {
	return f.sPrice, 18, f.priceErr
}

type fakeOrderPulls struct{ gasFee int64 }

func (f fakeOrderPulls) GroupPull(_ context.Context, orders []models.Order) (*big.Int, error) {
	sum := new(big.Int)
	for _, o := range orders {
		total, _ := new(big.Int).SetString(o.TotalAmount, 10)
		sum.Add(sum, total.Add(total, big.NewInt(f.gasFee)))
	}
	return sum, nil
}

type fakeFundingChain struct {
	token, native, gasPrice *big.Int
	tokenErr                error
}

func (f *fakeFundingChain) TokenBalance(context.Context, string, string) (*big.Int, error) {
	return f.token, f.tokenErr
}
func (f *fakeFundingChain) NativeBalance(context.Context, string) (*big.Int, error) {
	return f.native, nil
}
func (f *fakeFundingChain) GasPrice(context.Context) (*big.Int, error) { return f.gasPrice, nil }

type fakeSourceBalances struct {
	token, native *big.Int
	err           error

	lastChain             int64
	lastToken, lastWallet string
}

func (f *fakeSourceBalances) TokenBalanceOn(_ context.Context, chainID int64, token, wallet string) (*big.Int, error) {
	f.lastChain, f.lastToken, f.lastWallet = chainID, token, wallet
	return f.token, f.err
}
func (f *fakeSourceBalances) NativeBalanceOn(_ context.Context, chainID int64, wallet string) (*big.Int, error) {
	f.lastChain, f.lastWallet = chainID, wallet
	return f.native, f.err
}

type fakeFundingSponsor struct {
	can                bool
	checkErr           error
	checked            *big.Int
	sourceCalls, dests int
}

func (f *fakeFundingSponsor) CheckTopUp(_ context.Context, _ int64, amount *big.Int) error {
	f.checked = amount
	return f.checkErr
}

func (f *fakeFundingSponsor) TopUpForIntent(context.Context, *models.User, *models.RampSellIntent, IntentTopUpStore) (GasTopUpResult, error) {
	f.sourceCalls++
	return GasTopUpResult{TxHash: "0xsrc"}, nil
}
func (f *fakeFundingSponsor) TopUpDestForIntent(context.Context, *models.User, *models.RampSellIntent, DestTopUpStore) (GasTopUpResult, error) {
	f.dests++
	return GasTopUpResult{TxHash: "0xdest"}, nil
}
func (f *fakeFundingSponsor) CanSponsorChain(int64) bool { return f.can }

// ─── rig ───────────────────────────────────────────────────────────────────

type fundingRig struct {
	svc     *MarketplaceFundingService
	intents *SellIntentService
	quoter  *fakeFundingQuoter
	status  *fakeStatusReader
	dest    *fakeTransferVerifier
	chain   *fakeFundingChain
	sponsor *fakeFundingSponsor
	sources *fakeSourceBalances
	db      *gorm.DB
	user    *models.User
	now     time.Time
}

const fundingWallet = "0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A"

func newFundingRig(t *testing.T) *fundingRig {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.RampSellIntent{}); err != nil {
		t.Fatal(err)
	}
	// Raw DDL: the Order model carries a Postgres-only default.
	if err := db.Exec(`CREATE TABLE orders (id TEXT PRIMARY KEY, buyer_id TEXT, seller_id TEXT, product_id TEXT,
		quantity INTEGER, kind TEXT, token TEXT, status TEXT, product_amount TEXT, shipping_amount TEXT,
		platform_fee TEXT, total_amount TEXT, payment_source TEXT DEFAULT '', checkout_group_id TEXT,
		fund_with_swap BOOLEAN NOT NULL DEFAULT 0,
		created_at DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	r := &fundingRig{db: db, now: time.Now(), // GORM stamps created_at with the real clock
		quoter: &fakeFundingQuoter{sPrice: big.NewRat(1, 2)},
		status: &fakeStatusReader{}, dest: &fakeTransferVerifier{amount: big.NewInt(1_000_000_000)},
		chain:   &fakeFundingChain{token: big.NewInt(50_000), native: big.NewInt(1e18), gasPrice: big.NewInt(50_000_000_000)},
		sponsor: &fakeFundingSponsor{can: true},
		sources: &fakeSourceBalances{token: big.NewInt(1e18), native: big.NewInt(1e18)},
		user:    &models.User{ID: uuid.New(), WalletAddress: fundingWallet, VerificationStatus: models.VerificationStatusVerified},
	}
	r.quoter.quote = &SellSwapQuote{
		Route: &LiFiRoute{TransactionID: "0xabc", GasLimit: 300_000, FromAmount: "160000000000000000",
			ToAmount: big.NewInt(1_000_000_000), ToAmountMin: big.NewInt(999_000_000), FromAmountUSD: 10, ToAmountUSD: 0.151, ETASeconds: 1080, Tool: "polymer"},
		LiFiQuote: json.RawMessage(`{"id":"q"}`), CostBps: 20, GasBps: 0}
	now := func() time.Time { return r.now }
	r.intents = NewSellIntentService(db, r.quoter, r.status, nil, nil, "", now)
	r.intents.SetDestVerifiers(map[string]ERC20TransferVerifier{"sonic": r.dest}, func(id int64) (string, bool) {
		return "sonic", id == 146
	})
	r.svc = NewMarketplaceFundingService(db, MarketplaceFundingConfig{
		ChainID: 146, Tokens: map[string]string{"USDC": fundingTestSonicUSDC, "USDT": fundingTestSonicUSDT},
		GasPerOrder: 400_000,
	}, r.quoter, r.intents, fakeOrderPulls{gasFee: 20_000}, r.chain, now)
	r.svc.SetSponsors(r.sponsor, r.sponsor)
	r.svc.SetSourceBalances(r.sources)
	return r
}

// order writes one pending USDC order of hers in group, totalling total, last
// touched `idle` ago.
func (r *fundingRig) order(t *testing.T, group uuid.UUID, buyer uuid.UUID, total string, mutate ...func(map[string]any)) uuid.UUID {
	t.Helper()
	id := uuid.New()
	row := map[string]any{"id": id.String(), "buyer_id": buyer.String(), "seller_id": uuid.NewString(), "product_id": uuid.NewString(),
		"quantity": 1, "kind": "product", "token": "USDC", "status": models.OrderStatusPendingPayment,
		"product_amount": total, "shipping_amount": "0", "platform_fee": "0", "total_amount": total, "payment_source": "",
		"checkout_group_id": group.String(), "created_at": r.now.Add(-time.Minute), "updated_at": r.now.Add(-50 * time.Minute)}
	for _, m := range mutate {
		m(row)
	}
	if err := r.db.Table("orders").Create(row).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func (r *fundingRig) group(t *testing.T, totals ...string) uuid.UUID {
	t.Helper()
	g := uuid.New()
	for _, total := range totals {
		r.order(t, g, r.user.ID, total)
	}
	return g
}

// Base USDT: an EVM token on another chain, from her own wallet.
var fundingSrc = FundingSource{ChainID: 56, Token: bscUSDT, Address: fundingWallet}

// ─── sizing ────────────────────────────────────────────────────────────────

func TestFunding_SizedFromTheServersOrdersLessHerBalancePadded(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000", "80000") // + 20000 escrow gas fee each = 200000 pulled
	q, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc)
	if err != nil {
		t.Fatal(err)
	}
	// She holds 50000: the swap covers 150000, asked for padded by 50 bps.
	if q.OrderTotal != "200000" || q.WalletBalance != "50000" || q.Shortfall != "150000" || q.ToAmount != "150754" {
		t.Fatalf("quote = %+v", q)
	}
	in := r.quoter.lastIn
	if in.ToAmount != "150754" || in.ToChainID != 146 || in.ToToken != fundingTestSonicUSDC || in.ToAddress != fundingWallet ||
		!in.Delivery || !in.PassThrough || in.FromAddress != fundingWallet {
		t.Fatalf("quote input = %+v", in)
	}
	// No share-of-the-order cap: the route may cost up to the whole swap, and
	// her balance of the token she pays with is what bounds it.
	if in.BudgetBps != 10_000 || q.BudgetBps != 10_000 {
		t.Fatalf("budget = %d, want the whole swap (10000 bps)", in.BudgetBps)
	}
	if q.ETASeconds != 1080 || q.Orders != 2 {
		t.Fatalf("eta/orders = %d/%d", q.ETASeconds, q.Orders)
	}
}

func TestFunding_NotNeededWhenHerWalletCoversIt(t *testing.T) {
	r := newFundingRig(t)
	r.chain.token = big.NewInt(100_000)
	g := r.group(t, "80000") // 100000 pulled
	if _, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc); !errors.Is(err, ErrFundingNotNeeded) {
		t.Fatalf("err = %v", err)
	}
	if r.quoter.calls != 0 {
		t.Fatal("nothing should be quoted when nothing needs swapping")
	}
}

func TestFunding_OnlyHerPendingStablecoinCheckout(t *testing.T) {
	r := newFundingRig(t)
	other := uuid.New()
	cases := map[string]uuid.UUID{
		"someone else's": func() uuid.UUID { g := uuid.New(); r.order(t, g, other, "80000"); return g }(),
		"already paid": func() uuid.UUID {
			g := uuid.New()
			r.order(t, g, r.user.ID, "80000", func(m map[string]any) { m["status"] = models.OrderStatusEscrowed })
			return g
		}(),
		"spendable MCGP": func() uuid.UUID {
			g := uuid.New()
			r.order(t, g, r.user.ID, "80000", func(m map[string]any) { m["token"] = "MCGP"; m["payment_source"] = "spendable_mcgp" })
			return g
		}(),
		"mixed tokens": func() uuid.UUID {
			g := uuid.New()
			r.order(t, g, r.user.ID, "80000")
			r.order(t, g, r.user.ID, "80000", func(m map[string]any) { m["token"] = "USDT" })
			return g
		}(),
		"no such group": uuid.New(),
	}
	for name, g := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc); !errors.Is(err, ErrFundingGroup) {
				t.Fatalf("err = %v, want ErrFundingGroup", err)
			}
		})
	}
}

func TestFunding_SourceIsHerOwnEVMWalletAndNotTheOrderToken(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	for name, tc := range map[string]struct {
		src  FundingSource
		want error
	}{
		"another address": {FundingSource{ChainID: 56, Token: bscUSDT, Address: sellOtherAddress}, ErrFundingSource},
		"a non-EVM chain": {FundingSource{ChainID: 1151111081099710, Token: "So11111111111111111111111111111111111111112", Address: fundingWallet}, ErrFundingSource},
		"the order token": {FundingSource{ChainID: 146, Token: strings.ToLower(fundingTestSonicUSDC), Address: fundingWallet}, ErrFundingSameToken},
		"wallet MCGP":     {fundingSrc, ErrSellWalletMCGP},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "wallet MCGP" {
				r.quoter.refuse = ErrSellWalletMCGP
				defer func() { r.quoter.refuse = nil }()
			}
			if _, err := r.svc.Quote(context.Background(), r.user, g, tc.src); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// Pass-through admission: a route costing more than the whole swap (fees
// larger than the order) is refused in the checkout's own words.
func TestFunding_TooCostlyIsRefusedPlainly(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	r.quoter.err = ErrSellRouteTooCostly
	_, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc)
	if !errors.Is(err, ErrFundingTooCostly) || strings.Contains(err.Error(), "2%") {
		t.Fatalf("err = %v", err)
	}
}

// Every refusal about what converting costs — fees, her balance, gas, or a
// route LiFi will not give — points her at Spendable MCGP, which needs neither.
func TestFunding_CostRefusalsPointAtSpendableMCGP(t *testing.T) {
	for _, err := range []error{ErrFundingTooCostly, ErrFundingShortBalance, ErrFundingNeedsGas, ErrFundingKYC, ErrFundingKeepS, ErrFundingUnavailable} {
		if !strings.Contains(err.Error(), fundingMCGPHint) {
			t.Errorf("%q does not offer Spendable MCGP", err)
		}
	}
}

// A route LiFi could not price is not "too costly" — nothing says it costs too
// much — so the checkout calls it unavailable.
func TestFunding_UnpricedRouteIsUnavailableNotTooCostly(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	r.quoter.err = ErrSellRouteUnpriced
	_, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc)
	if !errors.Is(err, ErrFundingUnavailable) || errors.Is(err, ErrFundingTooCostly) {
		t.Fatalf("err = %v, want ErrFundingUnavailable", err)
	}
}

// ─── her balance of the token she pays with ───────────────────────────────

// The route's FromAmount — the order plus every cost of the swap — must be in
// her wallet on the source chain; otherwise she is told before she signs.
func TestFunding_RefusedWhenHerSourceBalanceCannotCoverTheRoute(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	r.sources.token = big.NewInt(159_999_999_999_999_999) // route takes 0.16e18
	if _, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc); !errors.Is(err, ErrFundingShortBalance) {
		t.Fatalf("quote err = %v, want ErrFundingShortBalance", err)
	}
	if _, err := r.svc.Start(context.Background(), r.user, g, fundingSrc); !errors.Is(err, ErrFundingShortBalance) {
		t.Fatalf("start err = %v, want ErrFundingShortBalance", err)
	}
	// The refused start must not hold her one open-funding slot.
	if open, err := r.intents.OpenFor(context.Background(), r.user.ID, models.SellIntentPurposeMarketplace); err != nil || open != nil {
		t.Fatalf("open intent after refusal = %v, %v", open, err)
	}
	if r.sources.lastChain != fundingSrc.ChainID || r.sources.lastToken != fundingSrc.Token || r.sources.lastWallet != fundingWallet {
		t.Fatalf("read %d/%s/%s, want her source token on its chain", r.sources.lastChain, r.sources.lastToken, r.sources.lastWallet)
	}
}

func TestFunding_AdmittedWhenHerSourceBalanceCoversTheRoute(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	r.sources.token = big.NewInt(160_000_000_000_000_000) // exactly what the route takes
	if _, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc); err != nil {
		t.Fatalf("quote err = %v", err)
	}
}

// Paying with a chain's own coin spends from the balance that also pays the
// route's gas, so both must fit.
func TestFunding_NativeSourceMustAlsoCoverTheRouteGas(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	r.quoter.quote.Route.GasCostWei = big.NewInt(1_000_000)
	src := FundingSource{ChainID: 56, Token: evmNativeAddress, Address: fundingWallet}
	r.sources.native = big.NewInt(160_000_000_000_000_000) // the swap, but not its gas
	if _, err := r.svc.Quote(context.Background(), r.user, g, src); !errors.Is(err, ErrFundingShortBalance) {
		t.Fatalf("err = %v, want ErrFundingShortBalance", err)
	}
	r.sources.native = big.NewInt(160_000_000_001_000_000)
	if _, err := r.svc.Quote(context.Background(), r.user, g, src); err != nil {
		t.Fatalf("err = %v", err)
	}
}

// A balance we cannot read is not a refusal: the swap itself reverts if she is
// short, and an RPC outage must not take checkout down with it.
func TestFunding_UnreadableSourceBalanceIsNotARefusal(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	r.sources.err = errors.New("rpc down")
	if _, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc); err != nil {
		t.Fatalf("err = %v", err)
	}
}

// ─── Sonic gas ─────────────────────────────────────────────────────────────

func TestFunding_PricesSonicGasInWhenSheHoldsNone(t *testing.T) {
	r := newFundingRig(t)
	r.chain.native = big.NewInt(0)
	g := r.group(t, "80000", "80000")
	q, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc)
	if err != nil {
		t.Fatal(err)
	}
	// 2 orders x 400k gas x 50 gwei x 2 buffer = 0.08 S, at $0.50 = $0.04.
	if !q.DestGasTopUp || r.quoter.lastIn.DestGasUSD < 0.0399 || r.quoter.lastIn.DestGasUSD > 0.0401 {
		t.Fatalf("dest gas = %v (topup %v), want $0.04 priced into g", r.quoter.lastIn.DestGasUSD, q.DestGasTopUp)
	}
}

func TestFunding_NoSonicGasNeededWhenSheHasIt(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	q, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc)
	if err != nil || q.DestGasTopUp || r.quoter.lastIn.DestGasUSD != 0 {
		t.Fatalf("q=%+v err=%v", q, err)
	}
}

// With no Sonic lane she is refused BEFORE signing anything: after the swap
// she could not pay the seller.
func TestFunding_RefusedUpFrontWhenSonicGasCannotBeCovered(t *testing.T) {
	r := newFundingRig(t)
	r.chain.native = big.NewInt(0)
	r.sponsor.can = false
	g := r.group(t, "80000")
	if _, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc); !errors.Is(err, ErrFundingNeedsGas) {
		t.Fatalf("err = %v", err)
	}
}

// ─── the intent ────────────────────────────────────────────────────────────

func TestFunding_StartOpensAMarketplaceIntentForTheGroup(t *testing.T) {
	r := newFundingRig(t)
	r.chain.native = big.NewInt(0)
	g := r.group(t, "80000")
	started, err := r.svc.Start(context.Background(), r.user, g, fundingSrc)
	if err != nil {
		t.Fatal(err)
	}
	in := started.Intent
	if !in.IsMarketplace() || in.ReferenceID == nil || *in.ReferenceID != g || in.PayPaymentID != nil ||
		in.DestChainID != 146 || in.DestAddress != fundingWallet || in.DestAmount != "50000" ||
		in.SourceAmount != "160000000000000000" || in.Status != models.SellIntentSwapping {
		t.Fatalf("intent = %+v", in)
	}
	// Pass-through: nothing stored as our fee; the Sonic top-up budgeted, and
	// capped at the shortfall it was priced on (she held none).
	if in.FeeBps != 0 || !in.DestTopUpBudgeted || in.DestGasNeedWei != "40000000000000000" || in.DestTopUpMaxWei != "40000000000000000" {
		t.Fatalf("fee=%d destTopUp=%v need=%s max=%s", in.FeeBps, in.DestTopUpBudgeted, in.DestGasNeedWei, in.DestTopUpMaxWei)
	}
	// Starting earns her orders no time: only a swap LiFi has seen does.
	var touched int64
	r.db.Table("orders").Where("checkout_group_id = ? AND updated_at >= ?", g.String(), time.Now().Add(-time.Second)).Count(&touched)
	if touched != 0 {
		t.Fatalf("orders kept alive at start = %d, want 0", touched)
	}
}

// One open funding per user — its own slot, so an open SALE of hers neither
// blocks it nor is blocked by it.
func TestFunding_OneOpenFundingPerUserBesideASale(t *testing.T) {
	r := newFundingRig(t)
	sale := &models.RampSellIntent{UserID: r.user.ID, SourceAddress: fundingWallet, WalletAddress: fundingWallet,
		SourceChainID: 56, SourceTokenAddress: bscUSDT, SourceAmount: "1", FiatCode: "NGN", Status: models.SellIntentSwapping}
	if err := r.db.Create(sale).Error; err != nil {
		t.Fatal(err)
	}
	g1, g2 := r.group(t, "80000"), r.group(t, "80000")
	if _, err := r.svc.Start(context.Background(), r.user, g1, fundingSrc); err != nil {
		t.Fatalf("a checkout beside an open sale: %v", err)
	}
	if _, err := r.svc.Start(context.Background(), r.user, g2, fundingSrc); !errors.Is(err, ErrSellIntentOpen) {
		t.Fatalf("second funding: err = %v", err)
	}
	if open, _ := r.intents.Open(context.Background(), r.user.ID); open == nil || open.ID != sale.ID {
		t.Fatalf("the sale's own slot = %+v — a funding must never be offered as her sale", open)
	}
}

// A delivery must answer for something: the generic reference, not a TSA Pay
// payment, and only on a delivery.
func TestFunding_ExactOutputNeedsItsReference(t *testing.T) {
	ref := uuid.New()
	base := CreateSellIntentInput{ToAmount: "10", DestAddress: fundingWallet, DestChainID: 146, DestTokenAddress: fundingTestSonicUSDC, DestAmount: "9"}
	ok := base
	ok.Purpose, ok.ReferenceID = models.SellIntentPurposeMarketplace, &ref
	if _, err := exactOutputAsked(ok); err != nil {
		t.Fatalf("a referenced delivery: %v", err)
	}
	if _, err := exactOutputAsked(base); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("a delivery for nothing: err = %v", err)
	}
	noDelivery := ok
	noDelivery.DestAddress = ""
	if _, err := exactOutputAsked(noDelivery); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("a purpose on an offramp: err = %v", err)
	}
	both := ok
	pay := uuid.New()
	both.PayPaymentID = &pay
	if _, err := exactOutputAsked(both); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("a payment AND a reference: err = %v", err)
	}
}

// landedOnSonic is LiFi's COMPLETED status for in, delivered to her wallet.
func landedOnSonic(in *models.RampSellIntent) *LiFiTransferStatus {
	amt, _ := new(big.Int).SetString(in.SourceAmount, 10)
	return &LiFiTransferStatus{Status: LiFiStatusDone, Substatus: LiFiSubstatusCompleted, DestTxHash: "0xsonicdest",
		DestChainID: 146, ToAddress: in.DestAddress, ReceivingToken: in.DestTokenAddress,
		SendingChainID: in.SourceChainID, SendingToken: in.SourceTokenAddress, SendingAmount: amt,
		FromAddress: in.SourceAddress, TransactionID: "0xabc"}
}

func TestFunding_RecordConfirmDelivers(t *testing.T) {
	r := newFundingRig(t)
	r.chain.native = big.NewInt(0)
	g := r.group(t, "80000")
	started, err := r.svc.Start(context.Background(), r.user, g, fundingSrc)
	if err != nil {
		t.Fatal(err)
	}
	id := started.Intent.ID
	if err := r.svc.RecordSwap(context.Background(), r.user, id, testSwapHash); err != nil {
		t.Fatal(err)
	}
	r.status.st = &LiFiTransferStatus{Status: "PENDING"}
	if _, err := r.svc.Confirm(context.Background(), r.user, id); !errors.Is(err, ErrSwapNotLanded) {
		t.Fatalf("while bridging: err = %v", err)
	}
	cur, _ := r.intents.Get(context.Background(), r.user.ID, id)
	r.status.st = landedOnSonic(cur)
	st, err := r.svc.Confirm(context.Background(), r.user, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Intent.Status != models.SellIntentDelivered || st.DestTxHash != "0xsonicdest" || !st.NeedsGas {
		t.Fatalf("status = %+v", st)
	}
	// Once she has the S, no top-up is owed.
	r.chain.native = big.NewInt(1e18)
	if st, _ := r.svc.Confirm(context.Background(), r.user, id); st == nil || st.NeedsGas {
		t.Fatalf("a funded wallet still asked for gas: %+v", st)
	}
	if _, err := r.svc.SonicGasTopUp(context.Background(), r.user, id); err != nil || r.sponsor.dests != 1 {
		t.Fatalf("sonic top-up: err=%v calls=%d", err, r.sponsor.dests)
	}
}

// Delivery is proven on Sonic, to the unit: less than the shortfall is a
// mismatch, not a delivery.
func TestFunding_ConfirmRefusesAShortCredit(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	started, _ := r.svc.Start(context.Background(), r.user, g, fundingSrc)
	id := started.Intent.ID
	_ = r.svc.RecordSwap(context.Background(), r.user, id, testSwapHash)
	cur, _ := r.intents.Get(context.Background(), r.user.ID, id)
	r.status.st = landedOnSonic(cur)
	r.dest.amount = big.NewInt(49_999)
	if _, err := r.svc.Confirm(context.Background(), r.user, id); !errors.Is(err, ErrSwapMismatch) {
		t.Fatalf("err = %v", err)
	}
}

func TestFunding_ARefundedSwapClosesTheFunding(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	started, _ := r.svc.Start(context.Background(), r.user, g, fundingSrc)
	id := started.Intent.ID
	_ = r.svc.RecordSwap(context.Background(), r.user, id, testSwapHash)
	r.status.st = &LiFiTransferStatus{Status: LiFiStatusDone, Substatus: LiFiSubstatusRefunded}
	var ended *SwapEndedShortError
	if _, err := r.svc.Confirm(context.Background(), r.user, id); !errors.As(err, &ended) || ended.Substatus != LiFiSubstatusRefunded {
		t.Fatalf("err = %v", err)
	}
	if cur, _ := r.intents.Get(context.Background(), r.user.ID, id); cur.Status != models.SellIntentFailed {
		t.Fatalf("status = %s, want failed (she can start again, or pay directly)", cur.Status)
	}
}

// Once her swap is sent, she cannot abandon it (it may still land): only the
// reconciler ends it.
func TestFunding_AbandonOnlyBeforeTheSwap(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	started, _ := r.svc.Start(context.Background(), r.user, g, fundingSrc)
	_ = r.svc.RecordSwap(context.Background(), r.user, started.Intent.ID, testSwapHash)
	if err := r.svc.Abandon(context.Background(), r.user, started.Intent.ID); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("abandon after the swap: err = %v", err)
	}
	g2 := r.group(t, "80000")
	r.now = r.now.Add(time.Minute)
	if err := r.intents.AbandonForReference(context.Background(), started.Intent.ID, &[]string{testSwapHash}[0]); err != nil {
		t.Fatal(err)
	}
	fresh, err := r.svc.Start(context.Background(), r.user, g2, fundingSrc)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Abandon(context.Background(), r.user, fresh.Intent.ID); err != nil {
		t.Fatalf("abandon before the swap: %v", err)
	}
}

// A sale's or TSA Pay's intent is never driven through the funding routes.
func TestFunding_RoutesOnlyDriveFundingIntents(t *testing.T) {
	r := newFundingRig(t)
	sale := &models.RampSellIntent{UserID: r.user.ID, SourceAddress: fundingWallet, WalletAddress: fundingWallet,
		SourceChainID: 56, SourceTokenAddress: bscUSDT, SourceAmount: "1", FiatCode: "NGN", Status: models.SellIntentSwapping}
	r.db.Create(sale)
	if _, err := r.svc.Get(context.Background(), r.user.ID, sale.ID); !errors.Is(err, ErrSellIntentNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := r.svc.RecordSwap(context.Background(), r.user, sale.ID, testSwapHash); !errors.Is(err, ErrSellIntentNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// ─── the reconciler ────────────────────────────────────────────────────────

func TestFundingReconcile_ClosesAnUnsignedFunding(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	started, _ := r.svc.Start(context.Background(), r.user, g, fundingSrc)
	r.now = r.now.Add(fundingUnsignedTTL - time.Minute)
	if err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cur, _ := r.intents.Get(context.Background(), r.user.ID, started.Intent.ID); cur.Status != models.SellIntentSwapping {
		t.Fatalf("closed too early: %s", cur.Status)
	}
	r.now = r.now.Add(2 * time.Minute)
	if err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cur, _ := r.intents.Get(context.Background(), r.user.ID, started.Intent.ID); cur.Status != models.SellIntentFailed {
		t.Fatalf("status = %s, want failed", cur.Status)
	}
}

// A slow bridge (some take ~20 minutes) must not let the one-hour unpaid-order
// TTL cancel her orders under it: while in flight — for at most fundingHoldMax
// — each tick restarts their clock; delivery restarts it once more.
// ordersClock ages a group's orders by hand (TouchUnpaidGroup stamps the real
// clock) and reports whether something restarted it since.
func ordersClock(t *testing.T, r *fundingRig, g uuid.UUID) (age func(), touched func() bool) {
	stale := time.Now().Add(-59 * time.Minute)
	age = func() {
		t.Helper()
		if err := r.db.Table("orders").Where("checkout_group_id = ?", g.String()).Update("updated_at", stale).Error; err != nil {
			t.Fatal(err)
		}
	}
	touched = func() bool {
		var o struct{ UpdatedAt time.Time }
		r.db.Table("orders").Select("updated_at").Where("checkout_group_id = ?", g.String()).Scan(&o)
		return o.UpdatedAt.After(stale.Add(time.Minute))
	}
	return age, touched
}

// pendingFrom is LiFi following her swap: a live transfer sent by her address
// from the intent's source chain.
func pendingFrom(addr string) *LiFiTransferStatus {
	return &LiFiTransferStatus{Status: LiFiStatusPending, FromAddress: addr, SendingChainID: 56}
}

// A slow bridge (some take ~20 minutes) must not let the one-hour unpaid-order
// TTL cancel her orders under it: while LiFi follows her swap each tick
// restarts their clock — but only up to creation + fundingHoldMax −
// UnpaidOrderTTL, so the hold is really at most fundingHoldMax; delivery
// restarts it once more (she is paying the escrow now).
func TestFundingReconcile_KeepsOrdersAliveWhileInFlightAndDelivers(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	started, _ := r.svc.Start(context.Background(), r.user, g, fundingSrc)
	id := started.Intent.ID
	_ = r.svc.RecordSwap(context.Background(), r.user, id, testSwapHash)
	r.status.st = pendingFrom(fundingWallet)
	age, touched := ordersClock(t, r, g)

	age()
	r.now = r.now.Add(55 * time.Minute)
	if err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !touched() {
		t.Fatal("a swap LiFi follows must keep its checkout's orders from expiring")
	}
	if cur, _ := r.intents.Get(context.Background(), r.user.ID, id); cur.SwapSeenAt == nil {
		t.Fatal("the first sighting must be recorded")
	}
	// Past creation + fundingHoldMax − UnpaidOrderTTL no touch is made: the
	// orders expire by creation + fundingHoldMax at the latest.
	age()
	r.now = r.now.Add(10 * time.Minute) // 65 min in
	_ = r.svc.Reconcile(context.Background())
	if touched() {
		t.Fatal("an in-flight funding held its orders past fundingHoldMax")
	}

	cur, _ := r.intents.Get(context.Background(), r.user.ID, id)
	r.status.st = landedOnSonic(cur)
	r.now = r.now.Add(time.Hour) // past the backoff
	if err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cur, _ := r.intents.Get(context.Background(), r.user.ID, id); cur.Status != models.SellIntentDelivered {
		t.Fatalf("status = %s, want delivered by the reconciler", cur.Status)
	}
	if !touched() {
		t.Fatal("delivery must restart her orders' clock so she can pay the escrow")
	}
}

// A well-formed hash nobody broadcast earns no hold: LiFi never sees it, so
// her orders are touched only in the short grace after she reports it, and the
// funding is closed once it has gone unseen for fundingUnseenGiveUp.
func TestFundingReconcile_AnUnseenHashHoldsNothing(t *testing.T) {
	for name, st := range map[string]*LiFiTransferStatus{
		"LiFi has no record": nil,
		"someone else's":     pendingFrom(sellOtherAddress),
		"another chain":      {Status: LiFiStatusPending, FromAddress: fundingWallet, SendingChainID: 1},
	} {
		t.Run(name, func(t *testing.T) {
			r := newFundingRig(t)
			g := r.group(t, "80000")
			started, _ := r.svc.Start(context.Background(), r.user, g, fundingSrc)
			id := started.Intent.ID
			_ = r.svc.RecordSwap(context.Background(), r.user, id, testSwapHash)
			r.status.st = st
			age, touched := ordersClock(t, r, g)

			age()
			r.now = r.now.Add(5 * time.Minute) // inside the grace
			_ = r.svc.Reconcile(context.Background())
			if !touched() {
				t.Fatal("a just-reported swap gets the short grace")
			}
			age()
			r.now = r.now.Add(10 * time.Minute) // past the grace, still unseen
			_ = r.svc.Reconcile(context.Background())
			if touched() {
				t.Fatal("an unseen hash must not keep stock held")
			}
			r.now = r.now.Add(fundingUnseenGiveUp)
			_ = r.svc.Reconcile(context.Background())
			if cur, _ := r.intents.Get(context.Background(), r.user.ID, id); cur.Status != models.SellIntentFailed {
				t.Fatalf("status = %s, want failed: never seen from her in %s", cur.Status, fundingUnseenGiveUp)
			}
		})
	}
}

// Due intents are taken oldest-due first and each visit pushes its next check
// out: a stuck tail cannot keep a fresh funding from being looked at.
func TestFundingReconcile_BacksOffByAge(t *testing.T) {
	for age, want := range map[time.Duration]time.Duration{
		5 * time.Minute: 0, time.Hour: 3 * time.Minute, 5 * time.Hour: 15 * time.Minute, 20 * time.Hour: time.Hour,
	} {
		if got := fundingBackoff(age); got != want {
			t.Fatalf("backoff(%s) = %s, want %s", age, got, want)
		}
	}
	r := newFundingRig(t)
	g := r.group(t, "80000")
	started, _ := r.svc.Start(context.Background(), r.user, g, fundingSrc)
	_ = r.svc.RecordSwap(context.Background(), r.user, started.Intent.ID, testSwapHash)
	r.status.st = pendingFrom(fundingWallet)
	r.now = r.now.Add(40 * time.Minute)
	_ = r.svc.Reconcile(context.Background())
	calls := r.status.calls
	_ = r.svc.Reconcile(context.Background()) // same instant: not due again
	if r.status.calls != calls {
		t.Fatal("an intent checked this tick must not be asked of LiFi again before its backoff")
	}
	r.now = r.now.Add(4 * time.Minute)
	_ = r.svc.Reconcile(context.Background())
	if r.status.calls != calls+1 {
		t.Fatalf("LiFi asked %d more times, want 1 once due", r.status.calls-calls)
	}
}

func TestFundingReconcile_WritesOffAndClosesEndedSwaps(t *testing.T) {
	r := newFundingRig(t)
	g := r.group(t, "80000")
	started, _ := r.svc.Start(context.Background(), r.user, g, fundingSrc)
	id := started.Intent.ID
	_ = r.svc.RecordSwap(context.Background(), r.user, id, testSwapHash)

	r.status.st = &LiFiTransferStatus{Status: LiFiStatusFailed}
	if err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cur, _ := r.intents.Get(context.Background(), r.user.ID, id); cur.Status != models.SellIntentFailed {
		t.Fatalf("a FAILED swap: status = %s", cur.Status)
	}

	g2 := r.group(t, "80000")
	started2, err := r.svc.Start(context.Background(), r.user, g2, fundingSrc)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.svc.RecordSwap(context.Background(), r.user, started2.Intent.ID, testSwapHash2)
	r.status.st = &LiFiTransferStatus{Status: "PENDING"}
	r.now = r.now.Add(fundingGiveUp + time.Minute)
	if err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cur, _ := r.intents.Get(context.Background(), r.user.ID, started2.Intent.ID); cur.Status != models.SellIntentFailed {
		t.Fatalf("a swap never seen to land in a day: status = %s", cur.Status)
	}
}

// ─── Refused before she signs ──────────────────────────────────────────────

// We only sponsor gas for a verified account, so an unverified buyer who would
// need any top-up is refused with the KYC code — never charged for gas we
// would then refuse to send.
func TestFunding_UnverifiedBuyerNeedingGasIsRefusedBeforeSigning(t *testing.T) {
	t.Run("sonic top-up", func(t *testing.T) {
		r := newFundingRig(t)
		r.user.VerificationStatus = "pending"
		r.chain.native = big.NewInt(0)
		g := r.group(t, "80000")
		if _, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc); !errors.Is(err, ErrFundingKYC) {
			t.Fatalf("err = %v, want ErrFundingKYC", err)
		}
	})
	t.Run("source-chain top-up", func(t *testing.T) {
		r := newFundingRig(t)
		r.user.VerificationStatus = "pending"
		r.quoter.quote.GasTopUp = true
		g := r.group(t, "80000")
		if _, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc); !errors.Is(err, ErrFundingKYC) {
			t.Fatalf("quote: err = %v, want ErrFundingKYC", err)
		}
		if _, err := r.svc.Start(context.Background(), r.user, g, fundingSrc); !errors.Is(err, ErrFundingKYC) {
			t.Fatalf("start: err = %v, want ErrFundingKYC", err)
		}
		if open, _ := r.svc.Open(context.Background(), r.user.ID); open != nil {
			t.Fatal("a refused start must not leave an intent holding her slot")
		}
	})
	t.Run("no gas needed: not refused", func(t *testing.T) {
		r := newFundingRig(t)
		r.user.VerificationStatus = "pending"
		g := r.group(t, "80000")
		if _, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc); err != nil {
			t.Fatalf("an unverified buyer needing no gas: %v", err)
		}
	})
}

// The lane's own refusals (ceiling, float floor, daily gas budget) and her
// daily cap are asked at quote time, so she never signs a swap whose S top-up
// would then be refused.
func TestFunding_SonicLaneRefusalsAreAskedBeforeSigning(t *testing.T) {
	r := newFundingRig(t)
	r.chain.native = big.NewInt(1_000_000_000_000_000) // holds 0.001 S of the 0.08 needed
	g := r.group(t, "80000", "80000")
	r.sponsor.checkErr = ErrTopUpTooExpensive
	if _, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc); !errors.Is(err, ErrFundingNeedsGas) {
		t.Fatalf("over the lane ceiling: err = %v, want ErrFundingNeedsGas", err)
	}
	if r.sponsor.checked == nil || r.sponsor.checked.String() != "79000000000000000" {
		t.Fatalf("checked %v, want the shortfall need − have", r.sponsor.checked)
	}
	r.sponsor.checkErr = nil
	for i := 0; i < intentTopUpDailyCap; i++ {
		row := &models.RampSellIntent{UserID: r.user.ID, SourceAddress: fundingWallet, WalletAddress: fundingWallet,
			SourceChainID: 56, SourceTokenAddress: bscUSDT, SourceAmount: "1", FiatCode: "NGN", Status: models.SellIntentFailed}
		now := r.now
		row.DestToppedUpAt = &now
		if err := r.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.svc.Quote(context.Background(), r.user, g, fundingSrc); !errors.Is(err, ErrFundingNeedsGas) {
		t.Fatalf("her daily cap spent: err = %v, want ErrFundingNeedsGas", err)
	}
}

// Paying with S itself: the swap spends from the very balance the escrow txs
// need, so it is never topped up — it is checked against the route instead.
func TestFunding_NativeSOnSonicSource(t *testing.T) {
	src := FundingSource{ChainID: 146, Token: evmNativeAddress, Address: fundingWallet}
	t.Run("enough S left after the swap", func(t *testing.T) {
		r := newFundingRig(t)
		r.chain.native = big.NewInt(1e18) // 1 S; the route spends 0.16, the escrows need 0.04
		g := r.group(t, "80000")
		q, err := r.svc.Quote(context.Background(), r.user, g, src)
		if err != nil {
			t.Fatal(err)
		}
		if q.DestGasTopUp || r.quoter.lastIn.DestGasUSD != 0 {
			t.Fatal("paying with S is never topped up with S")
		}
	})
	t.Run("the swap would eat the escrow gas", func(t *testing.T) {
		r := newFundingRig(t)
		r.chain.native = big.NewInt(170_000_000_000_000_000) // 0.17 S; 0.17 − 0.16 < 0.04
		g := r.group(t, "80000")
		if _, err := r.svc.Quote(context.Background(), r.user, g, src); !errors.Is(err, ErrFundingKeepS) {
			t.Fatalf("err = %v, want ErrFundingKeepS", err)
		}
	})
}

// One funded checkout at a time: an open funding, or orders created for one
// that still await payment, refuse a new one (the cart quote answers first, so
// the app stops before placing orders).
func TestFunding_OneFundedCheckoutAtATime(t *testing.T) {
	r := newFundingRig(t)
	if err := r.svc.CanStartCheckout(context.Background(), r.user.ID); err != nil {
		t.Fatalf("nothing in progress: %v", err)
	}
	g := uuid.New()
	r.order(t, g, r.user.ID, "80000", func(m map[string]any) { m["fund_with_swap"] = true })
	if err := r.svc.CanStartCheckout(context.Background(), r.user.ID); !errors.Is(err, ErrFundingInProgress) {
		t.Fatalf("pending funded orders: err = %v", err)
	}
	if _, err := r.svc.QuoteTotal(context.Background(), r.user, "USDC", big.NewInt(100_000), 1, fundingSrc); !errors.Is(err, ErrFundingInProgress) {
		t.Fatalf("cart quote: err = %v, want ErrFundingInProgress", err)
	}
	r.db.Table("orders").Where("checkout_group_id = ?", g.String()).Update("status", models.OrderStatusCancelled)
	if _, err := r.svc.Start(context.Background(), r.user, r.group(t, "80000"), fundingSrc); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.CanStartCheckout(context.Background(), r.user.ID); !errors.Is(err, ErrFundingInProgress) {
		t.Fatalf("an open funding: err = %v", err)
	}
}
