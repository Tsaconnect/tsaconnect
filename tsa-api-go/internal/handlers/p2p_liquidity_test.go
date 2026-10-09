package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakeLiquidity struct {
	lastUser uuid.UUID
	err      error
}

func (f *fakeLiquidity) Me(_ context.Context, u uuid.UUID) (*services.LiquidityMe, error) {
	f.lastUser = u
	return &services.LiquidityMe{Status: services.LiquidityStatusNone}, f.err
}
func (f *fakeLiquidity) Activity(_ context.Context, u uuid.UUID, typ, _ string, _ int) (*services.LiquidityActivityPage, error) {
	f.lastUser = u
	return &services.LiquidityActivityPage{}, f.err
}

func liquidityGet(h *P2PHandler, u *models.User, path string, fn gin.HandlerFunc) int {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if u != nil {
		r.Use(func(c *gin.Context) { c.Set("user", u); c.Next() })
	}
	r.GET("/liquidity/me", fn)
	r.GET("/liquidity/me/activity", fn)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", path, nil)
	r.ServeHTTP(w, req)
	return w.Code
}

// §15: every /liquidity/me* read is the caller's own; no id is accepted.
func TestLiquidityHandlers_ScopedToTheCaller(t *testing.T) {
	f := &fakeLiquidity{}
	h := &P2PHandler{}
	h.SetLiquidity(f)
	me := &models.User{ID: uuid.New()}
	if code := liquidityGet(h, me, "/liquidity/me?userId="+uuid.New().String(), h.GetLiquidityMe); code != http.StatusOK || f.lastUser != me.ID {
		t.Fatalf("code=%d user=%s", code, f.lastUser)
	}
	if code := liquidityGet(h, nil, "/liquidity/me", h.GetLiquidityMe); code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", code)
	}
	f.err = services.ErrLiquidityActivityType
	if code := liquidityGet(h, me, "/liquidity/me/activity?type=fills", h.ListLiquidityActivity); code != http.StatusBadRequest {
		t.Fatalf("bad type = %d", code)
	}
	f.err = services.ErrNotLiquidityProvider
	if code := liquidityGet(h, me, "/liquidity/me/activity?type=deposits", h.ListLiquidityActivity); code != http.StatusNotFound {
		t.Fatalf("non-LP = %d", code)
	}
}
