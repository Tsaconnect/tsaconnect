package routes

import (
	"context"
	"math/big"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// idleFunding answers every funding call with "unavailable": these tests are
// about the routes (registered, authenticated, per-user limited), not the money.
type idleFunding struct{}

func (idleFunding) Enabled() bool { return true }
func (idleFunding) Quote(context.Context, *models.User, uuid.UUID, services.FundingSource) (*services.FundingQuote, error) {
	return nil, services.ErrFundingUnavailable
}
func (idleFunding) QuoteTotal(context.Context, *models.User, string, *big.Int, int, services.FundingSource) (*services.FundingQuote, error) {
	return nil, services.ErrFundingUnavailable
}
func (idleFunding) CanStartCheckout(context.Context, uuid.UUID) error { return nil }
func (idleFunding) Start(context.Context, *models.User, uuid.UUID, services.FundingSource) (*services.FundingStart, error) {
	return nil, services.ErrFundingUnavailable
}
func (idleFunding) Get(context.Context, uuid.UUID, uuid.UUID) (*models.RampSellIntent, error) {
	return nil, services.ErrSellIntentNotFound
}
func (idleFunding) Open(context.Context, uuid.UUID) (*models.RampSellIntent, error) { return nil, nil }
func (idleFunding) RecordSwap(context.Context, *models.User, uuid.UUID, string) error {
	return services.ErrSellIntentNotFound
}
func (idleFunding) Confirm(context.Context, *models.User, uuid.UUID) (*services.FundingStatus, error) {
	return nil, services.ErrSellIntentNotFound
}
func (idleFunding) Abandon(context.Context, *models.User, uuid.UUID) error {
	return services.ErrSellIntentNotFound
}
func (idleFunding) SourceGasTopUp(context.Context, *models.User, uuid.UUID) (services.GasTopUpResult, error) {
	return services.GasTopUpResult{}, services.ErrSellIntentNotFound
}
func (idleFunding) SonicGasTopUp(context.Context, *models.User, uuid.UUID) (services.GasTopUpResult, error) {
	return services.GasTopUpResult{}, services.ErrSellIntentNotFound
}

func fundingRouter(t *testing.T, withFunding bool) (*gin.Engine, *gorm.DB, *config.Config) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(payRoutesUsersDDL).Error; err != nil {
		t.Fatal(err)
	}
	config.DB = db
	cfg := &config.Config{JWTSecret: "funding-routes-test-secret"}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ch := handlers.NewCheckoutHandler(cfg, nil, nil, nil, nil)
	if withFunding {
		ch.WithFunding(idleFunding{})
	}
	SetupRoutes(r, cfg, &handlers.Handlers{}, ch, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	return r, db, cfg
}

// The funding routes sit beside /orders/:id without shadowing it, need a
// session, and exist only when the funding is wired.
func TestFundingRoutes_RegisteredAuthenticatedAndOnlyWhenWired(t *testing.T) {
	r, db, cfg := fundingRouter(t, true)
	alice := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	if w := payRoutesDo(r, http.MethodGet, "/api/orders/funding/intents/open", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
	if w := payRoutesDo(r, http.MethodGet, "/api/orders/funding/intents/open", alice, nil); w.Code != http.StatusOK {
		t.Fatalf("open funding: %d %s", w.Code, w.Body.String())
	}
	if w := payRoutesDo(r, http.MethodPost, "/api/orders/funding/intents/"+uuid.NewString()+"/confirm", alice, nil); w.Code != http.StatusNotFound {
		t.Fatalf("confirm an unknown funding: %d %s", w.Code, w.Body.String())
	}

	off, db2, cfg2 := fundingRouter(t, false)
	bob := payRoutesToken(t, cfg2.JWTSecret, payRoutesUser(t, db2, models.RoleUser).ID)
	if w := payRoutesDo(off, http.MethodPost, "/api/orders/funding/intents", bob, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unwired funding must have no route: %d", w.Code)
	}
}

// Starting a funding is limited per user like a sale's writes.
func TestFundingRoutes_WritesArePerUserLimited(t *testing.T) {
	r, db, cfg := fundingRouter(t, true)
	alice := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	bob := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	for i := 0; i < 10; i++ {
		if w := payRoutesDo(r, http.MethodPost, "/api/orders/funding/intents", alice, nil); w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d limited too early", i+1)
		}
	}
	if w := payRoutesDo(r, http.MethodPost, "/api/orders/funding/intents", alice, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("11th start in a minute: %d, want 429", w.Code)
	}
	if w := payRoutesDo(r, http.MethodPost, "/api/orders/funding/intents", bob, nil); w.Code == http.StatusTooManyRequests {
		t.Fatal("the limit must be per user")
	}
}
