package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/services/ramp"
)

type fakeRampRoutes struct {
	quote    *ramp.QuoteResult
	quoteErr error
	openReq  ramp.OpenRequest
	order    *ramp.Order
	openErr  error
	getErr   error
}

func (f *fakeRampRoutes) Quote(context.Context, ramp.Request) (*ramp.QuoteResult, error) {
	return f.quote, f.quoteErr
}
func (f *fakeRampRoutes) Open(_ context.Context, r ramp.OpenRequest) (*ramp.Order, error) {
	f.openReq = r
	return f.order, f.openErr
}
func (f *fakeRampRoutes) Get(context.Context, uuid.UUID, string) (*ramp.Order, error) {
	return f.order, f.getErr
}
func (f *fakeRampRoutes) List(context.Context, uuid.UUID, int) ([]ramp.Order, error) {
	return []ramp.Order{*f.order}, nil
}
func (f *fakeRampRoutes) ClaimPayment(context.Context, uuid.UUID, string) (*ramp.Order, error) {
	return f.order, f.getErr
}
func (f *fakeRampRoutes) Cancel(context.Context, uuid.UUID, string) (*ramp.Order, error) {
	return f.order, f.getErr
}

// rampTestServer mounts the handler like routes.go, with an auth stub that
// sets `user` (when non-nil) on the context the way the real middleware does.
func rampTestServer(t *testing.T, f *fakeRampRoutes, user *models.User) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewRampHandler(f)
	r := gin.New()
	auth := func(c *gin.Context) {
		if user == nil {
			c.AbortWithStatusJSON(401, gin.H{"success": false})
			return
		}
		c.Set("user", user) // getUserFromContext reads this key (handlers/common.go)
		c.Next()
	}
	g := r.Group("/api/ramp")
	g.GET("/quote", h.Quote)
	g.Use(auth)
	g.POST("/buy", h.Buy)
	g.POST("/sell", h.Sell)
	g.GET("/orders", h.ListOrders)
	g.GET("/orders/:id", h.GetOrder)
	g.POST("/orders/:id/cancel", h.Cancel)
	return r
}

func rampDoJSON(r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func rampUser() *models.User {
	return &models.User{ID: uuid.New(), WalletAddress: "0xAbC0000000000000000000000000000000000001"}
}

func rampBuyJSON() map[string]any {
	return map[string]any{
		"amount": "1000", "currency": "NGN", "chain": "base", "token": "USDC",
		"walletAddress": "", "refundBankCode": "044", "refundAccount": "0123456789",
		"quotedRate": "1397.59",
	}
}

func TestRampQuote_PublicAndShaped(t *testing.T) {
	f := &fakeRampRoutes{quote: &ramp.QuoteResult{Side: "buy", Venues: []ramp.Venue{{Handle: "h", ChainKey: "base", Token: "USDC", Rate: "1397.59", TokenAmount: 0.7}}}}
	w := rampDoJSON(rampTestServer(t, f, nil), "GET", "/api/ramp/quote?side=buy&amount=1000", nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var env struct {
		Data struct {
			Venues []map[string]any `json:"venues"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	v := env.Data.Venues[0]
	if _, isNum := v["tokenAmount"].(float64); !isNum || v["chain"] != "base" || v["handle"] != "h" || v["provider"] != nil {
		t.Fatalf("venue json = %v", v)
	}
}

func TestRampBuy_ForwardsTheQuoteAndReturnsTheBareOrder(t *testing.T) {
	id := uuid.New()
	f := &fakeRampRoutes{order: &ramp.Order{Provider: ramp.ProviderPaycrest, Paycrest: &models.PaycrestOrderRecord{ID: id}}}
	user := rampUser()
	body := rampBuyJSON()
	body["handle"] = "abc"
	w := rampDoJSON(rampTestServer(t, f, user), "POST", "/api/ramp/buy", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if f.openReq.QuotedRate != "1397.59" || f.openReq.Handle != "abc" || f.openReq.Request.Side != ramp.Buy ||
		f.openReq.Input.UserID != user.ID || f.openReq.Input.RefundAccount != "0123456789" {
		t.Fatalf("open request = %+v", f.openReq)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"id":"`+id.String()+`"`)) {
		t.Fatalf("order id must stay the bare uuid: %s", w.Body)
	}
}

func TestRampBuy_RequiresAQuotedRate(t *testing.T) {
	body := rampBuyJSON()
	delete(body, "quotedRate")
	w := rampDoJSON(rampTestServer(t, &fakeRampRoutes{}, rampUser()), "POST", "/api/ramp/buy", body)
	if w.Code != 400 || !bytes.Contains(w.Body.Bytes(), []byte("invalid_quote")) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestRampBuy_RejectsConvertedFields(t *testing.T) {
	for _, extra := range []map[string]any{
		{"requestedChainId": 8453}, {"requestedTokenAddress": "0x1"}, {"requestedToken": "USDT"}, {"destinationAddress": "So1"},
	} {
		body := rampBuyJSON()
		for k, v := range extra {
			body[k] = v
		}
		w := rampDoJSON(rampTestServer(t, &fakeRampRoutes{}, rampUser()), "POST", "/api/ramp/buy", body)
		if w.Code != 400 || !bytes.Contains(w.Body.Bytes(), []byte("use_paycrest_route")) {
			t.Fatalf("%v: %d %s", extra, w.Code, w.Body)
		}
	}
}

func TestRampBuy_PriceMovedCarriesTheFreshQuote(t *testing.T) {
	fresh := &ramp.QuoteResult{Side: "buy", Venues: []ramp.Venue{{Rate: "1500"}}}
	f := &fakeRampRoutes{openErr: &ramp.PriceMovedError{Quote: fresh}}
	w := rampDoJSON(rampTestServer(t, f, rampUser()), "POST", "/api/ramp/buy", rampBuyJSON())
	if w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte(`"code":"price_moved"`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"rate":"1500"`)) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestRampErrors_MapToTodaysWordingOrRampCodes(t *testing.T) {
	cases := map[error]int{
		errors.Join(ramp.ErrRefusedForUser, services.ErrPaycrestOrderOpen): 409,
		ramp.ErrUnavailable:   503,
		ramp.ErrLegClosed:     409,
		ramp.ErrNoVenue:       409,
		ramp.ErrInvalidQuote:  400,
		ramp.ErrOrderNotFound: 404,
	}
	for err, want := range cases {
		w := rampDoJSON(rampTestServer(t, &fakeRampRoutes{openErr: err}, rampUser()), "POST", "/api/ramp/buy", rampBuyJSON())
		if w.Code != want {
			t.Errorf("%v → %d %s, want %d", err, w.Code, w.Body, want)
		}
	}
}

// paycrestError shows the KYC refusal verbatim: the ramp classes must not
// change a byte of it.
func TestRampBuy_KYCRefusalKeepsTodaysWordingAndCode(t *testing.T) {
	refused := ramp.WrapClass(ramp.ErrRefusedForUser, services.ErrKYCVerificationRequired)
	w := rampDoJSON(rampTestServer(t, &fakeRampRoutes{openErr: refused}, rampUser()), "POST", "/api/ramp/buy", rampBuyJSON())
	var env struct{ Message, Code string }
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if w.Code != 403 || env.Message != services.ErrKYCVerificationRequired.Error() || env.Code != services.KYCRefusalCode(services.ErrKYCVerificationRequired) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestRampSell_UnsupportedBankKeepsItsOwnMessage(t *testing.T) {
	err := ramp.WrapClass(ramp.ErrUnavailable, ramp.WrapClass(ramp.ErrVenueUnavailable, services.ErrPaycrestBankUnsupported))
	body := map[string]any{"amount": "1000", "chain": "base", "token": "USDC", "bankCode": "044",
		"accountNumber": "0123456789", "quotedRate": "1353.25"}
	w := rampDoJSON(rampTestServer(t, &fakeRampRoutes{openErr: err}, rampUser()), "POST", "/api/ramp/sell", body)
	if w.Code != 400 || !bytes.Contains(w.Body.Bytes(), []byte("That bank can't receive payouts")) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// TSA Pay orders are masked exactly as on /api/paycrest (orderForCaller).
func TestRampOrders_TSAPayOrdersAreMasked(t *testing.T) {
	pay := uuid.New()
	rec := &models.PaycrestOrderRecord{ID: uuid.New(), PayPaymentID: &pay, PayoutAccount: "0123456789", PayoutBankCode: "044"}
	f := &fakeRampRoutes{order: &ramp.Order{Provider: ramp.ProviderPaycrest, Paycrest: rec}}
	for _, path := range []string{"/api/ramp/orders/" + rec.ID.String(), "/api/ramp/orders"} {
		w := rampDoJSON(rampTestServer(t, f, rampUser()), "GET", path, nil)
		if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("0123456789")) {
			t.Fatalf("%s: %d %s — the merchant's account must be masked", path, w.Code, w.Body)
		}
	}
}

func TestRampOrders_AuthAndNotFound(t *testing.T) {
	if w := rampDoJSON(rampTestServer(t, &fakeRampRoutes{}, nil), "GET", "/api/ramp/orders/"+uuid.NewString(), nil); w.Code != 401 {
		t.Fatalf("no auth: %d", w.Code)
	}
	f := &fakeRampRoutes{getErr: services.ErrPaycrestOrderNotFound}
	if w := rampDoJSON(rampTestServer(t, f, rampUser()), "GET", "/api/ramp/orders/"+uuid.NewString(), nil); w.Code != 404 {
		t.Fatalf("another user's order: %d %s", w.Code, w.Body)
	}
}

// The currency reaches log lines and provider calls verbatim: anything but
// three letters is refused before the router sees it.
func TestRamp_RejectsAMalformedCurrency(t *testing.T) {
	f := &fakeRampRoutes{quote: &ramp.QuoteResult{}}
	user := rampUser()
	if w := rampDoJSON(rampTestServer(t, f, user), "GET", "/api/ramp/quote?side=buy&amount=1000&currency=NG%0AX", nil); w.Code != 400 {
		t.Fatalf("quote: %d %s", w.Code, w.Body)
	}
	buy := rampBuyJSON()
	buy["currency"] = "NGNX"
	if w := rampDoJSON(rampTestServer(t, f, user), "POST", "/api/ramp/buy", buy); w.Code != 400 {
		t.Fatalf("buy: %d %s", w.Code, w.Body)
	}
	sell := map[string]any{"amount": "1000", "currency": "N1N", "chain": "base", "token": "USDC", "bankCode": "044",
		"accountNumber": "0123456789", "quotedRate": "1353.25"}
	if w := rampDoJSON(rampTestServer(t, f, user), "POST", "/api/ramp/sell", sell); w.Code != 400 {
		t.Fatalf("sell: %d %s", w.Code, w.Body)
	}
	if f.openReq.QuotedRate != "" {
		t.Fatalf("the router was asked to open: %+v", f.openReq)
	}
	// Lower case and an omitted currency are still fine.
	buy["currency"] = "ngn"
	f.order = &ramp.Order{Provider: ramp.ProviderPaycrest, Paycrest: &models.PaycrestOrderRecord{ID: uuid.New()}}
	if w := rampDoJSON(rampTestServer(t, f, user), "POST", "/api/ramp/buy", buy); w.Code != http.StatusCreated {
		t.Fatalf("lower-case buy: %d %s", w.Code, w.Body)
	}
	if w := rampDoJSON(rampTestServer(t, f, nil), "GET", "/api/ramp/quote?side=buy&amount=1000", nil); w.Code != 200 {
		t.Fatalf("default currency: %d %s", w.Code, w.Body)
	}
}
