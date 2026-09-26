package routes

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
)

// Every cart call the app makes must reach a handler. The cart screen's
// "Clear All" returned 404 because ClearCart existed but was never routed.
func TestCartRoutesTheAppCallsAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r, &config.Config{}, &handlers.Handlers{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	registered := map[string]bool{}
	for _, rt := range r.Routes() {
		registered[rt.Method+" "+rt.Path] = true
	}
	for _, want := range []string{
		"GET /api/cart",
		"GET /api/cart/summary",
		"POST /api/cart/items",
		"PUT /api/cart/items/:id",
		"DELETE /api/cart/items/:id",
		"DELETE /api/cart/clear",
	} {
		if !registered[want] {
			t.Errorf("route not registered: %s", want)
		}
	}
}
