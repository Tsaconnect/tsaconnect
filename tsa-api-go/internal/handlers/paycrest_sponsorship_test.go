package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakeSponsorPort struct {
	enabled  bool
	routes   []services.SponsorRoute
	offer    services.SponsorshipOffer
	relayIn  services.TransferAuthorization
	relayOut services.AuthorizationRelayResult
	topUpOut services.GasTopUpResult
	err      error

	intentTopUpCalls  int
	intentTopUpIntent *models.RampSellIntent
	intentTopUpStore  services.IntentTopUpStore
}

func (f *fakeSponsorPort) Enabled() bool                               { return f.enabled }
func (f *fakeSponsorPort) Routes(*models.User) []services.SponsorRoute { return f.routes }
func (f *fakeSponsorPort) Offer(context.Context, *models.User, uuid.UUID) (services.SponsorshipOffer, error) {
	return f.offer, f.err
}
func (f *fakeSponsorPort) RelayAuthorization(_ context.Context, _ *models.User, _ uuid.UUID, a services.TransferAuthorization) (services.AuthorizationRelayResult, error) {
	f.relayIn = a
	return f.relayOut, f.err
}
func (f *fakeSponsorPort) TopUp(context.Context, *models.User, uuid.UUID) (services.GasTopUpResult, error) {
	return f.topUpOut, f.err
}
func (f *fakeSponsorPort) TopUpForIntent(_ context.Context, _ *models.User, in *models.RampSellIntent, store services.IntentTopUpStore) (services.GasTopUpResult, error) {
	f.intentTopUpCalls++
	f.intentTopUpIntent, f.intentTopUpStore = in, store
	return f.topUpOut, f.err
}

func sponsorTestRouter(h *PaycrestHandler, user *models.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", user); c.Next() })
	r.GET("/paycrest/sponsorship", h.SponsorshipRoutes)
	r.GET("/paycrest/orders/:id/sponsorship", h.SponsorshipOffer)
	r.POST("/paycrest/orders/:id/relay-authorization", h.RelayAuthorization)
	r.POST("/paycrest/orders/:id/gas-topup", h.GasTopUp)
	return r
}

func sponsorDo(t *testing.T, r *gin.Engine, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

var sponsorHandlerUser = &models.User{ID: uuid.New(), WalletAddress: "0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A"}

func TestPaycrestSponsorship_RoutesReportsDisabledWithoutPort(t *testing.T) {
	w, out := sponsorDo(t, sponsorTestRouter(&PaycrestHandler{}, sponsorHandlerUser), http.MethodGet, "/paycrest/sponsorship", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if data := out["data"].(map[string]any); data["enabled"] != false {
		t.Fatalf("data = %v, want enabled=false", data)
	}
}

func TestPaycrestSponsorship_RelayDecodesNonceAndSignature(t *testing.T) {
	port := &fakeSponsorPort{enabled: true, relayOut: services.AuthorizationRelayResult{TxHash: "0xtx"}}
	h := (&PaycrestHandler{}).WithSponsorship(port)
	body := `{"validAfter":0,"validBefore":1790000000,"nonce":"0x` + strings.Repeat("ab", 32) +
		`","signature":"0xa484f5c66a0d60049bdef52b6f804bcbcb48ddf0f86c125202d2e60cbe5806c245510cbb6624175e65fb4b6c73c72a395aa56be2bc4194468039fd8605f9e4061c"}`
	w, out := sponsorDo(t, sponsorTestRouter(h, sponsorHandlerUser), http.MethodPost, "/paycrest/orders/"+uuid.NewString()+"/relay-authorization", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if port.relayIn.Nonce[0] != 0xab || len(port.relayIn.Signature) != 65 || port.relayIn.ValidBefore != 1790000000 {
		t.Fatalf("port received %+v", port.relayIn)
	}
	if out["data"].(map[string]any)["txHash"] != "0xtx" {
		t.Fatalf("data = %v", out["data"])
	}
}

func TestPaycrestSponsorship_RelayRejectsShortNonce(t *testing.T) {
	h := (&PaycrestHandler{}).WithSponsorship(&fakeSponsorPort{enabled: true})
	body := `{"validBefore":1790000000,"nonce":"0xabcd","signature":"0x` + strings.Repeat("00", 65) + `"}`
	w, _ := sponsorDo(t, sponsorTestRouter(h, sponsorHandlerUser), http.MethodPost, "/paycrest/orders/"+uuid.NewString()+"/relay-authorization", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestPaycrestSponsorship_TopUpKYCRequiredIs403(t *testing.T) {
	h := (&PaycrestHandler{}).WithSponsorship(&fakeSponsorPort{enabled: true, err: services.ErrKYCRequired})
	w, _ := sponsorDo(t, sponsorTestRouter(h, sponsorHandlerUser), http.MethodPost, "/paycrest/orders/"+uuid.NewString()+"/gas-topup", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestPaycrestSponsorship_OfferForeignOrderIs404(t *testing.T) {
	h := (&PaycrestHandler{}).WithSponsorship(&fakeSponsorPort{enabled: true, err: services.ErrPaycrestOrderNotYours})
	w, _ := sponsorDo(t, sponsorTestRouter(h, sponsorHandlerUser), http.MethodGet, "/paycrest/orders/"+uuid.NewString()+"/sponsorship", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// A token domain that fails verification is a temporary on-chain or config
// problem, not a server bug.
func TestPaycrestSponsorship_DomainMismatchIs503(t *testing.T) {
	port := &fakeSponsorPort{enabled: true, err: fmt.Errorf("%w: x", services.ErrSponsorDomainMismatch)}
	h := (&PaycrestHandler{}).WithSponsorship(port)
	body := `{"validAfter":0,"validBefore":1790000000,"nonce":"0x` + strings.Repeat("ab", 32) +
		`","signature":"0xa484f5c66a0d60049bdef52b6f804bcbcb48ddf0f86c125202d2e60cbe5806c245510cbb6624175e65fb4b6c73c72a395aa56be2bc4194468039fd8605f9e4061c"}`
	w, _ := sponsorDo(t, sponsorTestRouter(h, sponsorHandlerUser), http.MethodPost, "/paycrest/orders/"+uuid.NewString()+"/relay-authorization", body)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
}

// The 503 shares one generic message across five causes, and an RPC failure
// during simulation logs nothing on its own — so the handler must log the cause,
// without echoing a keyed provider URL.
func TestPaycrestSponsorship_ServerSideRefusalLogsRedactedCause(t *testing.T) {
	var buf strings.Builder
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	cause := fmt.Errorf("%w: authorizationState: Post \"https://base-mainnet.g.alchemy.com/v2/SeCrEtKeY123\": dial tcp: i/o timeout",
		services.ErrSimulationUnavailable)
	h := (&PaycrestHandler{}).WithSponsorship(&fakeSponsorPort{enabled: true, err: cause})
	body := `{"validAfter":0,"validBefore":1790000000,"nonce":"0x` + strings.Repeat("ab", 32) +
		`","signature":"0x` + strings.Repeat("11", 65) + `"}`
	w, _ := sponsorDo(t, sponsorTestRouter(h, sponsorHandlerUser), http.MethodPost, "/paycrest/orders/"+uuid.NewString()+"/relay-authorization", body)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	got := buf.String()
	if !strings.Contains(got, "relay-authorization -> 503") || !strings.Contains(got, "authorizationState") {
		t.Fatalf("log does not name the route and cause: %q", got)
	}
	if strings.Contains(got, "SeCrEtKeY123") {
		t.Fatalf("log leaked the provider key: %q", got)
	}
}
