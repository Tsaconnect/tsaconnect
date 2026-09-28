package routes

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The endpoints that resolve a Spendable MCGP hold (each costs the caller
// nothing and sends operator transactions) share one per-user budget, so a
// pay→refund or pay→confirm loop is throttled whichever endpoint it uses.
func TestOrderRoutes_HoldResolvingEndpointsShareAPerUserLimit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(payRoutesUsersDDL).Error; err != nil {
		t.Fatal(err)
	}
	config.DB = db
	cfg := &config.Config{JWTSecret: "order-routes-test-secret"}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ch := handlers.NewCheckoutHandler(cfg, nil, nil, nil, nil)
	SetupRoutes(r, cfg, &handlers.Handlers{}, ch, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	alice := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	bob := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	paths := []string{"request-refund", "cancel", "approve-cancel", "confirm-receipt"}
	order := "/api/orders/" + uuid.NewString() + "/"

	// 20 across the four endpoints get through to the handler (no such
	// order, so 404); the 21st, on any of them, is refused.
	for i := 0; i < 20; i++ {
		if w := payRoutesDo(r, http.MethodPost, order+paths[i%len(paths)], alice, nil); w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d limited too early", i+1)
		}
	}
	for _, p := range paths {
		if w := payRoutesDo(r, http.MethodPost, order+p, alice, nil); w.Code != http.StatusTooManyRequests {
			t.Fatalf("%s past the shared budget: %d, want 429", p, w.Code)
		}
	}
	// Another user has their own budget.
	if w := payRoutesDo(r, http.MethodPost, order+"request-refund", bob, nil); w.Code == http.StatusTooManyRequests {
		t.Fatal("the limit must be per user")
	}
}
