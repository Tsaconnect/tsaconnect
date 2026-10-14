package routes

import (
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type noLiquidity struct{ handlers.VaultLiquidity }

func hasRoute(r *gin.Engine, method, path string) bool {
	for _, rt := range r.Routes() {
		if rt.Method == method && rt.Path == path {
			return true
		}
	}
	return false
}

func liquidityRouter(withLiquidity bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	p2p := handlers.NewP2PHandler(nil, services.NewP2PTradeService(nil, nil, nil, nil, 146), nil, nil, nil)
	if withLiquidity {
		p2p.SetVaultLiquidity(noLiquidity{})
	}
	// p2p is SetupRoutes' 12th argument (after ch, mrh, sch, swh, slh, psh, wsHub, eh).
	SetupRoutes(r, &config.Config{}, &handlers.Handlers{}, nil, nil, nil, nil, nil, nil, nil, nil, p2p,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	return r
}

func TestLiquidityRoutes_RegisteredOnlyWithLiquidity(t *testing.T) {
	if hasRoute(liquidityRouter(false), "GET", "/api/liquidity/me") {
		t.Fatal("dormant: no /liquidity routes")
	}
	r := liquidityRouter(true)
	for _, rt := range [][2]string{{"GET", "/api/liquidity/me"}, {"GET", "/api/liquidity/me/activity"},
		{"POST", "/api/liquidity/activate"}, {"POST", "/api/liquidity/deposit/prepare"}, {"POST", "/api/liquidity/withdraw/prepare"},
		{"POST", "/api/liquidity/claim/prepare"}, {"POST", "/api/liquidity/claim-for"}} {
		if !hasRoute(r, rt[0], rt[1]) {
			t.Fatalf("missing %s %s", rt[0], rt[1])
		}
	}
}
