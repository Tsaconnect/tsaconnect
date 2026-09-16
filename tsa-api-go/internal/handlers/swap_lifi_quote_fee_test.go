package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// Fee wallets are configured per chain at portal.li.fi. A chain with none set
// up makes LiFi reject the whole quote — which would take swapping on that
// chain down entirely rather than merely forgo the fee. Better to swap for free
// than not at all, and loudly, since it is uncollected revenue.
func TestGetQuote_RetriesWithoutFeeWhenChainHasNoFeeWallet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var integrators []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		integrator := r.URL.Query().Get("integrator")
		integrators = append(integrators, integrator)
		if integrator != "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"Integrator \"tsaconnect\" is not configured for collecting fees on chain \"20000000000001\". Please sign up on https://portal.li.fi/"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"estimate":{"toAmount":"158131196"}}`))
	}))
	defer srv.Close()

	h := newTestLiFiHandlerWithIntegrator(srv.URL)
	r := gin.New()
	r.GET("/swap/lifi/quote", h.GetQuote)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/swap/lifi/quote?fromChain=20000000000001&toChain=42161", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("the swap should still go through, got %d: %s", w.Code, w.Body.String())
	}
	if len(integrators) != 2 || integrators[0] == "" || integrators[1] != "" {
		t.Fatalf("want a fee attempt then a fee-less retry, got %v", integrators)
	}
}

// Any other rejection is about the route, not our portal setup. Retrying those
// without the fee would quietly give away the fee on ordinary failures.
func TestGetQuote_KeepsTheFeeOnUnrelatedErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"No available quotes for the requested transfer"}`))
	}))
	defer srv.Close()

	h := newTestLiFiHandlerWithIntegrator(srv.URL)
	r := gin.New()
	r.GET("/swap/lifi/quote", h.GetQuote)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/swap/lifi/quote?fromChain=1", nil))

	if calls != 1 {
		t.Fatalf("a route error must not be retried fee-less, got %d calls", calls)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want the upstream 400 passed through, got %d", w.Code)
	}
}
