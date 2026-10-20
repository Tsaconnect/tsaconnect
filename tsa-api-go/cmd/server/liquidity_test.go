package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestPoolChainName(t *testing.T) {
	cfg := &config.Config{
		LiquidityPoolRow: &config.LiquidityPoolConfig{ChainID: 97, ChainKey: "bsc", Network: "testnet", Vault: testPoolVault},
		Networks:         map[string]config.NetworkConfig{"testnet": {Chains: map[string]config.ChainConfig{"bsc": {Name: "BNB Smart Chain Testnet"}}}},
	}
	if got := poolChainName(cfg); got != "BNB Smart Chain Testnet" {
		t.Fatalf("name = %q", got)
	}
	if got := poolChainName(&config.Config{}); got != "" {
		t.Fatalf("dormant = %q", got)
	}
}

func TestWireVaultLiquidity_DormantWithoutAVault(t *testing.T) {
	if got := wireVaultLiquidity(testConfig(t), nil, services.NewPMService(nil), fakeSender{}, nil); got != nil {
		t.Fatal("no vault: nothing to wire")
	}
}

// bootLiquidityRouter attaches the LP side as main does, then runs the real
// SetupRoutes, and reports what the scheduler would start with.
func bootLiquidityRouter(t *testing.T, lp *services.VaultLPService) (*gin.Engine, *jobs.Scheduler, *handlers.PMHandler) {
	t.Helper()
	scheduler := jobs.NewScheduler()
	p2pHandler := handlers.NewP2PHandler(nil, services.NewP2PTradeService(nil, services.NewP2PEscrowRegistryFromMap(nil, "2"), nil, nil, 146), nil, nil, nil)
	pmh := handlers.NewPMHandler(nil, nil)
	attachVaultLiquidity(lp, p2pHandler, pmh, scheduler)
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

// lpCTAShown is /p2p/pm/me's liquidityAvailable: whether the app offers
// "Provide liquidity".
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

func TestBootLiquidity_DormantWithoutAVault(t *testing.T) {
	r, scheduler, pmh := bootLiquidityRouter(t, nil)
	for _, path := range []string{"/api/liquidity/me", "/api/liquidity/me/activity"} {
		if code := get(r, path); code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404 (not registered while dormant)", path, code)
		}
	}
	if n := scheduler.Count(); n != 0 {
		t.Fatalf("dormant registered %d worker(s)", n)
	}
	if lpCTAShown(t, pmh) {
		t.Fatal("dormant: the app's LP CTA stays hidden")
	}
}

// A wired LP side: the routes exist behind auth, the CTA shows and the
// allow-list sync job runs alert-watched.
func TestBootLiquidity_WiredWithAVault(t *testing.T) {
	cfg, bs, _ := poolDeskWorld(t)
	lp := wireVaultLiquidity(cfg, bs, services.NewPMService(config.DB), fakeSender{}, nil)
	if lp == nil {
		t.Fatal("a vault row with a client and an operator key wires the LP side")
	}
	r, scheduler, pmh := bootLiquidityRouter(t, lp)
	for _, path := range []string{"/api/liquidity/me", "/api/liquidity/me/activity"} {
		if code := get(r, path); code != http.StatusUnauthorized {
			t.Fatalf("%s = %d, want 401 (registered, behind auth)", path, code)
		}
	}
	if !lpCTAShown(t, pmh) {
		t.Fatal("the app's LP CTA shows")
	}
	if n := scheduler.Count(); n != 1 {
		t.Fatalf("scheduler has %d worker(s), want the allow-list sync", n)
	}
	assertAlertWatched(t, scheduler, "liquidity-depositor-sync")
}
