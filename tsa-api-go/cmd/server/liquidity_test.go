package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/routes"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestWireLiquidity_DormantWithoutPoolChain(t *testing.T) {
	reg := services.NewP2PEscrowRegistryFromMap(map[int64]services.P2PEscrowAdapter{}, "2")
	trade := services.NewP2PTradeService(nil, reg, nil, nil, 146)
	onboarding := services.NewPMOnboardingService(nil, nil)
	if got := wireLiquidity(nil, nil, reg, trade, nil, nil, "", onboarding, nil); got != nil {
		t.Fatal("no pool chain: nothing to wire (production today)")
	}
	if _, ok := onboarding.LiquidityChainID(); ok {
		t.Fatal("dormant: LP onboarding stays off")
	}
	if got := wireLiquidity(nil, nil, reg, nil, nil, nil, "", nil, nil); got != nil {
		t.Fatal("P2P dormant: nil")
	}
}

func TestWireLiquidity_PoolChainTurnsOnOnboardingAndReads(t *testing.T) {
	trade, onboarding, reg := poolChainTrade()
	addr := func(int64, string) string { return "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7" }
	liq := wireLiquidity(nil, services.NewPMService(nil), reg, trade, nil, addr, "BNB Smart Chain Testnet", onboarding, nil)
	if liq == nil {
		t.Fatal("pool chain present: the read side is wired")
	}
	if id, ok := onboarding.LiquidityChainID(); !ok || id != 97 {
		t.Fatalf("onboarding chain = %d %v", id, ok)
	}
}

func TestPoolChainName(t *testing.T) {
	cfg := &config.Config{
		P2PChains: map[int64]config.P2PChainConfig{97: {ChainID: 97, ChainKey: "bsc", Network: "testnet", Purpose: config.P2PPurposePool, Pool: &config.PoolChainConfig{}}},
		Networks:  map[string]config.NetworkConfig{"testnet": {Chains: map[string]config.ChainConfig{"bsc": {Name: "BNB Smart Chain Testnet"}}}},
	}
	if got := poolChainName(cfg); got != "BNB Smart Chain Testnet" {
		t.Fatalf("name = %q", got)
	}
	if got := poolChainName(&config.Config{}); got != "" {
		t.Fatalf("dormant = %q", got)
	}
}

func poolChainTrade() (*services.P2PTradeService, *services.PMOnboardingService, *services.P2PEscrowRegistry) {
	reg := services.NewP2PEscrowRegistryFromEntries(map[int64]services.P2PRegistryEntry{
		97: {ChainKey: "bsc", Purpose: config.P2PPurposePool, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 18}},
	}, "2")
	return services.NewP2PTradeService(nil, reg, nil, nil, 146), services.NewPMOnboardingService(nil, nil), reg
}

// bootLiquidityRouter runs the liquidity wiring exactly as main does, then the
// real SetupRoutes, and reports what the scheduler would start with.
func bootLiquidityRouter(t *testing.T, reg *services.P2PEscrowRegistry, trade *services.P2PTradeService,
	onboarding *services.PMOnboardingService) (*gin.Engine, *jobs.Scheduler, *handlers.PMHandler) {
	t.Helper()
	scheduler := jobs.NewScheduler()
	p2pHandler := handlers.NewP2PHandler(nil, trade, nil, nil, nil)
	pmh := handlers.NewPMHandler(nil, nil) // no PM read: /p2p/pm/me answers from wiring alone
	liq := wireLiquidity(nil, services.NewPMService(nil), reg, trade, nil, nil, "", onboarding, nil)
	attachLiquidity(liq, p2pHandler, pmh, onboarding, trade, scheduler)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	routes.SetupRoutes(r, &config.Config{JWTSecret: "liquidity-boot-test"}, &handlers.Handlers{}, nil, nil, nil, nil, nil, nil, nil, nil, p2pHandler,
		nil, nil, nil, nil, nil, nil, pmh, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	return r, scheduler, pmh
}

func get(r *gin.Engine, path string) int {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code
}

// lpCTAShown is /p2p/pm/me's liquidityAvailable: whether the app offers LP
// onboarding (O-B5).
func lpCTAShown(t *testing.T, pmh *handlers.PMHandler) bool {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/p2p/pm/me", nil)
	c.Set("user", &models.User{ID: uuid.New()})
	pmh.GetMyPMStatus(c)
	var body struct {
		Data struct {
			LiquidityAvailable bool `json:"liquidityAvailable"`
		} `json:"data"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("/p2p/pm/me = %d %s", w.Code, w.Body.String())
	}
	return body.Data.LiquidityAvailable
}

// Production today: no pool row. P2P is up, but nothing of the LP side
// exists — no /liquidity routes, no renewal job, onboarding refuses.
func TestBootLiquidity_DormantWithoutPoolRow(t *testing.T) {
	reg := services.NewP2PEscrowRegistryFromMap(map[int64]services.P2PEscrowAdapter{}, "2")
	trade := services.NewP2PTradeService(nil, reg, nil, nil, 146)
	onboarding := services.NewPMOnboardingService(nil, nil)
	r, scheduler, pmh := bootLiquidityRouter(t, reg, trade, onboarding)

	if code := get(r, "/api/p2p/chains"); code != http.StatusUnauthorized {
		t.Fatalf("P2P route = %d, want 401 (registered, behind auth)", code)
	}
	for _, path := range []string{"/api/liquidity/me", "/api/liquidity/me/activity"} {
		if code := get(r, path); code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404 (not registered while dormant)", path, code)
		}
	}
	if n := scheduler.Count(); n != 0 {
		t.Fatalf("dormant registered %d worker(s)", n)
	}
	if _, ok := onboarding.LiquidityChainID(); ok {
		t.Fatal("dormant: LP onboarding stays off")
	}
	if lpCTAShown(t, pmh) {
		t.Fatal("dormant: the app's LP CTA stays hidden")
	}
}

// A pool row: the routes exist behind auth, onboarding knows the chain, and
// the voucher renewal job is registered with a failure-streak alert.
func TestBootLiquidity_WiredWithPoolRow(t *testing.T) {
	trade, onboarding, reg := poolChainTrade()
	r, scheduler, pmh := bootLiquidityRouter(t, reg, trade, onboarding)

	for _, path := range []string{"/api/liquidity/me", "/api/liquidity/me/activity"} {
		if code := get(r, path); code != http.StatusUnauthorized {
			t.Fatalf("%s = %d, want 401 (registered, behind auth)", path, code)
		}
	}
	if id, ok := onboarding.LiquidityChainID(); !ok || id != 97 {
		t.Fatalf("onboarding chain = %d %v", id, ok)
	}
	if !lpCTAShown(t, pmh) {
		t.Fatal("pool chain present: the app's LP CTA shows")
	}
	if n := scheduler.Count(); n != 1 {
		t.Fatalf("scheduler has %d worker(s), want the renewal job", n)
	}

	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // report the watch list only; no tick runs against a live store
	scheduler.Start(ctx)
	scheduler.Stop()
	if !strings.Contains(logs.String(), "liquidity-voucher-renewal(") {
		t.Fatalf("renewal job is not alert-watched:\n%s", logs.String())
	}
}

// pmOnboarding is nil when no fiat provider is configured: the read side and
// the job still wire, and nothing panics on the nil activator.
func TestBootLiquidity_WiredWithoutOnboarding(t *testing.T) {
	trade, _, reg := poolChainTrade()
	r, scheduler, pmh := bootLiquidityRouter(t, reg, trade, nil)
	if code := get(r, "/api/liquidity/me"); code != http.StatusUnauthorized {
		t.Fatalf("/api/liquidity/me = %d, want 401", code)
	}
	if lpCTAShown(t, pmh) {
		t.Fatal("no onboarding service: nobody can apply, so no LP CTA")
	}
	if n := scheduler.Count(); n != 1 {
		t.Fatalf("scheduler has %d worker(s), want 1", n)
	}
}
