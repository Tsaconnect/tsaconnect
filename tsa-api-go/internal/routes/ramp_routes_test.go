package routes

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/services/ramp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// countingRamp answers every converted quote with no venues and counts them.
type countingRamp struct{ quotes int }

func (f *countingRamp) Quote(context.Context, ramp.Request) (*ramp.QuoteResult, error) {
	f.quotes++
	return &ramp.QuoteResult{Venues: []ramp.Venue{}}, nil
}
func (f *countingRamp) Open(context.Context, ramp.OpenRequest) (*ramp.Order, error) {
	return nil, ramp.ErrOrderNotFound
}
func (f *countingRamp) OpenConverted(context.Context, ramp.OpenRequest) (*ramp.Order, error) {
	return nil, ramp.ErrOrderNotFound
}
func (f *countingRamp) Get(context.Context, uuid.UUID, string) (*ramp.Order, error) {
	return nil, ramp.ErrOrderNotFound
}
func (f *countingRamp) List(context.Context, uuid.UUID, int) ([]ramp.Order, error) { return nil, nil }
func (f *countingRamp) ClaimPayment(context.Context, uuid.UUID, string) (*ramp.Order, error) {
	return nil, ramp.ErrOrderNotFound
}
func (f *countingRamp) Cancel(context.Context, uuid.UUID, string) (*ramp.Order, error) {
	return nil, ramp.ErrOrderNotFound
}

// /api/ramp/converted-quote prices a LiFi route for the asset she names: it
// is mounted behind auth, with its own 30/min per-user limit (spec §15.8).
func TestRampRoutes_ConvertedQuoteIsAuthedAndPerUserLimited(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(payRoutesUsersDDL).Error; err != nil {
		t.Fatal(err)
	}
	config.DB = db
	cfg := &config.Config{JWTSecret: "ramp-routes-test-secret"}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ch := handlers.NewCheckoutHandler(cfg, nil, nil, nil, nil)
	pch := handlers.NewPaycrestHandler(&services.PaycrestOrderService{}, &services.PaycrestQuoteService{}, nil)
	f := &countingRamp{}
	pch.SetRamp(handlers.NewRampHandler(f))
	ith := handlers.NewITHandler(db, services.NewITService(db, nil, nil), nil) // the ramp mounts inside the IT block
	SetupRoutes(r, cfg, &handlers.Handlers{}, ch, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, ith, nil, nil, nil, nil, nil, nil, nil, pch, nil, nil, nil, nil)

	const path = "/api/ramp/converted-quote?amount=10000&currency=NGN&requestedChainId=10&requestedTokenAddress=0xOpt&requestedToken=USDT&requestedChain=optimism"
	if w := payRoutesDo(r, http.MethodGet, path, "", nil); w.Code != http.StatusUnauthorized || f.quotes != 0 {
		t.Fatalf("anonymous: %d, quotes=%d", w.Code, f.quotes)
	}
	alice := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	bob := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	for i := 0; i < 30; i++ {
		if w := payRoutesDo(r, http.MethodGet, path, alice, nil); w.Code != http.StatusOK {
			t.Fatalf("request %d: %d %s", i+1, w.Code, w.Body)
		}
	}
	if w := payRoutesDo(r, http.MethodGet, path, alice, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("31st: %d, want 429", w.Code)
	}
	if w := payRoutesDo(r, http.MethodGet, path, bob, nil); w.Code != http.StatusOK {
		t.Fatalf("another user: %d, want their own budget", w.Code)
	}
	if f.quotes != 31 {
		t.Fatalf("quotes = %d, want 31", f.quotes)
	}
}
