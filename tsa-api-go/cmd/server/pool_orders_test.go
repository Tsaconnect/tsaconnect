package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Re-review N8 / review I3: a mainnet pool row (set for Plan B's LP
// onboarding) builds the gates LP onboarding uses and no order desk.
func TestWirePoolOrders_MainnetRowKeepsGatesNoDesk(t *testing.T) {
	cfg := poolTestConfig(t, "mainnet") // pool row on chain 56
	reg := services.NewP2PEscrowRegistryFromEntries(map[int64]services.P2PRegistryEntry{
		56: {ChainKey: "bsc", Purpose: config.P2PPurposePool, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 18}},
	}, "2")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	trades := services.NewP2PTradeService(nil, reg, nil, nil, 146)
	w := wirePoolOrders(cfg, nil, reg, trades, services.NewPMService(nil), nil, services.NewITService(nil, nil, nil), nil)
	if w.gates == nil || w.desk != nil || w.nonces != nil {
		t.Fatalf("mainnet wiring = %+v, want gates and no desk", w)
	}
	if !strings.Contains(buf.String(), "pool orders wait for plan C2") {
		t.Fatalf("the mainnet row must be refused by the C2 guard, not by a missing part:\n%s", buf.String())
	}
}

// Production today: no pool row → no desk, no provider, nothing to wire. The
// pool_order_max ceiling still follows the treasury's per-order cap: the
// admin console can edit pool settings while the pool is dormant.
func TestWirePoolOrders_DormantWithoutPoolChain(t *testing.T) {
	t.Cleanup(func() { services.SetPoolOrderMaxCeilingUSD(2000) })
	cfg := &config.Config{P2PChains: map[int64]config.P2PChainConfig{}, RampTreasuryMaxInflightUSDPerOrder: 500}
	w := wirePoolOrders(cfg, nil, services.NewP2PEscrowRegistryFromMap(nil, "2"), nil, nil, nil, nil, nil)
	if w.desk != nil || w.gates != nil || w.nonces != nil {
		t.Fatalf("dormant wiring = %+v", w)
	}
	err := services.ValidatePoolSetting(nil, models.FeaturePoolOrderMax, 0, 600)
	if err == nil || !strings.Contains(err.Error(), "at most 500 USD") {
		t.Fatalf("pool_order_max 600 over a 500 USD treasury cap: %v", err)
	}
	s := jobs.NewScheduler()
	registerPoolOrderJobs(s, w)
	if s.Count() != 0 {
		t.Fatalf("dormant registered %d job(s)", s.Count())
	}
}

// windowEscrow is the pool escrow as the desk sees it: an address and its
// paymentWindow(). Nothing else is called while the desk is built.
type windowEscrow struct {
	services.P2PEscrowAdapter
	w time.Duration
}

func (windowEscrow) ContractAddress() string                     { return testPoolEscrow }
func (e windowEscrow) ReadPaymentWindow() (time.Duration, error) { return e.w, nil }

// poolDeskWorld is a BSC testnet pool row with every part the desk needs: an
// RPC client (lazy, never dialled), a router-less treasury lane, an active
// IT_PM_ID merchant and an escrow reporting window.
func poolDeskWorld(t *testing.T, window time.Duration) (*config.Config, *services.BlockchainService, *services.P2PEscrowRegistry, *services.P2PTradeService, *services.ITService) {
	t.Helper()
	cfg := poolTestConfig(t, "testnet")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PaymentMerchant{}); err != nil {
		t.Fatal(err)
	}
	pm := models.PaymentMerchant{ID: uuid.New(), UserID: uuid.New(), Name: "TSA", Status: models.PaymentMerchantStatusActive}
	if err := db.Create(&pm).Error; err != nil {
		t.Fatal(err)
	}
	cfg.ITPMID = pm.ID.String()
	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })
	reg := services.NewP2PEscrowRegistryFromEntries(map[int64]services.P2PRegistryEntry{
		97: {Escrow: windowEscrow{w: window}, ChainKey: "bsc", Purpose: config.P2PPurposePool, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 18}},
	}, "2")
	return cfg, services.NewBlockchainService(cfg), reg, services.NewP2PTradeService(db, reg, nil, nil, 97), services.NewITService(db, nil, nil)
}

// The rehearsal chain (BSC testnet, 97) with every part: the desk, its nonce
// watch (alert-watched) and the pinned card are all wired.
func TestWirePoolOrders_TestnetRowBuildsTheDesk(t *testing.T) {
	cfg, bs, reg, trades, it := poolDeskWorld(t, 60*time.Minute)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	w := wirePoolOrders(cfg, bs, reg, trades, services.NewPMService(config.DB), fakeSender{}, it, nil)
	if w.treasury == nil || w.gates == nil || w.desk == nil || w.nonces == nil {
		t.Fatalf("testnet wiring = %+v, want every part\n%s", w, buf.String())
	}
	if !it.HasPool() {
		t.Fatal("the desk must be handed to ITService (the pool provider and the card read it)")
	}
	if lane := w.treasury.Lane("bsc"); lane == nil || len(lane.EscrowCalls) != 1 || lane.EscrowCalls[0] != common.HexToAddress(testPoolEscrow) {
		t.Fatalf("the pool lane may call the pool escrow and nothing else: %+v", lane)
	}
	if !strings.Contains(buf.String(), "pool orders ready chain=bsc(97)") {
		t.Fatalf("no ready line:\n%s", buf.String())
	}

	s := jobs.NewScheduler()
	registerPoolOrderJobs(s, w)
	if s.Count() != 1 {
		t.Fatalf("registered %d, want the treasury nonce watch", s.Count())
	}
	assertAlertWatched(t, s, "liquidity-treasury-nonce-watch")

	if got := poolCardEnabled(t, w, it); !got {
		t.Fatal("a wired desk shows the pinned card")
	}
}

// Re-review N7: an escrow deployed with another paymentWindow builds no desk,
// so the pool provider and the card stay off.
func TestWirePoolOrders_PaymentWindowMismatchBuildsNoDesk(t *testing.T) {
	cfg, bs, reg, trades, it := poolDeskWorld(t, 30*time.Minute)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	w := wirePoolOrders(cfg, bs, reg, trades, services.NewPMService(config.DB), fakeSender{}, it, nil)
	if w.gates == nil || w.desk != nil || w.nonces != nil || it.HasPool() {
		t.Fatalf("mismatched window wiring = %+v hasPool=%t", w, it.HasPool())
	}
	if !strings.Contains(buf.String(), "liquidity_pool_payment_window_mismatch") {
		t.Fatalf("no mismatch alert:\n%s", buf.String())
	}
	if poolCardEnabled(t, w, it) {
		t.Fatal("no desk: the card stays hidden (O-C1-1)")
	}
}

// poolCardEnabled boots the P2P handler as main does and reads the pinned card.
func poolCardEnabled(t *testing.T, w *poolWiring, it *services.ITService) bool {
	t.Helper()
	h := handlers.NewP2PHandler(nil, nil, nil, nil, nil)
	attachPoolCard(w, h, it)
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/p2p/pool-card?currency=NGN", nil)
	c.Set("user", &models.User{ID: uuid.New(), VerificationStatus: "verified"})
	h.GetPoolCard(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("pool card = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data services.PoolCard `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Data.Enabled
}

// assertAlertWatched starts the scheduler on a cancelled context: it reports
// its alert watch list and runs no tick.
func assertAlertWatched(t *testing.T, s *jobs.Scheduler, name string) {
	t.Helper()
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Start(ctx)
	s.Stop()
	if !strings.Contains(logs.String(), name+"(") {
		t.Fatalf("%s is not alert-watched:\n%s", name, logs.String())
	}
}

// S24 / decision 17: the canceller runs with a release key, pool or not, and
// a silent death of it alerts.
func TestRegisterExpiryCanceller_AlwaysWithAReleaseKey(t *testing.T) {
	s := jobs.NewScheduler()
	trades := services.NewP2PTradeService(nil, services.NewP2PEscrowRegistryFromMap(nil, "2"), nil, nil, 146)
	registerExpiryCanceller(s, trades, &fakeSender{}, nil, nil, nil)
	if s.Count() != 1 {
		t.Fatalf("registered %d, want the canceller", s.Count())
	}
	assertAlertWatched(t, s, "p2p-expiry-canceller")
	s2 := jobs.NewScheduler()
	registerExpiryCanceller(s2, trades, nil, nil, nil, nil)
	if s2.Count() != 0 {
		t.Fatal("a canceller with no release key cannot send; it must not be registered")
	}
}

// The canceller's statement-freshness source is the inbound poller, and a
// missing poller is a true nil interface (the typed-nil lesson): the canceller
// then runs without the freshness pause instead of calling a nil poller.
func TestStatementFreshness_InboundPollerOrTrueNil(t *testing.T) {
	if f := statementFreshness(nil); f != nil {
		t.Fatalf("no poller = %#v, want a nil interface", f)
	}
	p := jobs.NewInboundCreditPoller(nil, time.Minute)
	if f := statementFreshness(p); f == nil || f.(*jobs.InboundCreditPoller) != p {
		t.Fatalf("the poller must be the freshness source, got %#v", f)
	}
}

// §25 step 12 (S46): unconfiguring the pool chain with live slices refuses boot.
// Review I-6: the decision is the configuration's (the row was removed), never
// the runtime registry's — a pool RPC that fails to dial at boot leaves the
// chain out of the registry and must not crash-loop the whole API.
// Postgres (review I7): P2PTrade's default:gen_random_uuid() does not migrate
// on SQLite (the codebase notes it at p2p_merchant_stats_service_test.go:104).
func TestCheckPoolUnconfigure_FatalWithLiveSlicesOffThePoolChain_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "liq_c1_unconf_", &models.P2PTrade{})
	unconfigured := &config.Config{P2PChains: map[int64]config.P2PChainConfig{}}
	dialFailed := services.NewP2PEscrowRegistryFromMap(nil, "2") // the pool chain's RPC never dialled
	if err := checkPoolUnconfigure(db, unconfigured, dialFailed); err != nil {
		t.Fatalf("no slices: %v", err)
	}
	intent := uuid.New()
	db.Create(&models.P2PTrade{ID: uuid.New(), AdID: uuid.New(), BuyerID: uuid.New(), SellerID: uuid.New(), Token: "USDT",
		ChainID: 56, TokenAmount: "1", FiatAmount: "1", FiatCurrency: "NGN", Rate: "1",
		Status: models.P2PTradeStatusPendingPayment, ExpiresAt: time.Now(), ITIntentID: &intent})
	if err := checkPoolUnconfigure(db, unconfigured, dialFailed); err == nil || !strings.Contains(err.Error(), "liquidity_pool_unconfigure_refused") {
		t.Fatalf("live slice on an unconfigured chain: %v", err)
	}
	configured := &config.Config{P2PChains: map[int64]config.P2PChainConfig{56: {Purpose: config.P2PPurposePool}}}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if err := checkPoolUnconfigure(db, configured, dialFailed); err != nil {
		t.Fatalf("a configured pool chain whose RPC is down must boot: %v", err)
	}
	if !strings.Contains(buf.String(), "WARN liquidity_pool_chain_not_running") {
		t.Fatalf("no warning for a configured pool chain the registry is not running:\n%s", buf.String())
	}
}

type fakeSender struct{}

func (fakeSender) Address() string                                          { return "0x00000000000000000000000000000000000000a1" }
func (fakeSender) SignAndBroadcast(context.Context, []byte) (string, error) { return "", nil }
func (fakeSender) Send(context.Context, int64, func() ([]byte, error)) (string, error) {
	return "", nil
}
