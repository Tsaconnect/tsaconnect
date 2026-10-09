package routes

import (
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type noLiquidity struct{ handlers.LiquidityReader }

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
		p2p.SetLiquidity(noLiquidity{})
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
	if !hasRoute(r, "GET", "/api/liquidity/me") || !hasRoute(r, "GET", "/api/liquidity/me/activity") {
		t.Fatal("liquidity routes missing")
	}
}
