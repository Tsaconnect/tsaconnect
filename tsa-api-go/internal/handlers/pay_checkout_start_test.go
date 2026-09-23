package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakePayStarter struct {
	err       error
	lastUser  *models.User
	direct    services.PayDirectInput
	converted services.PayConvertedInput
	calls     int
}

func (f *fakePayStarter) Quote(_ context.Context, u *models.User, _ uuid.UUID, in services.PayConvertedInput) (*services.PayQuote, error) {
	f.calls, f.lastUser, f.converted = f.calls+1, u, in
	if f.err != nil {
		return nil, f.err
	}
	return &services.PayQuote{FromAmount: "17000000000000000", USDCTarget: "33835000"}, nil
}

func (f *fakePayStarter) PayDirect(_ context.Context, u *models.User, _ uuid.UUID, in services.PayDirectInput) (*models.PaycrestOrderRecord, error) {
	f.calls, f.lastUser, f.direct = f.calls+1, u, in
	if f.err != nil {
		return nil, f.err
	}
	return &models.PaycrestOrderRecord{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Status: models.PaycrestStatusAwaitingDeposit,
		DepositAddress: "0xdeposit", FiatAmount: "50000.00", Token: "USDC", ChainKey: "base"}, nil
}

func (f *fakePayStarter) PayConverted(_ context.Context, u *models.User, _ uuid.UUID, in services.PayConvertedInput) (*models.RampSellIntent, *services.SellSwapQuote, error) {
	f.calls, f.lastUser, f.converted = f.calls+1, u, in
	if f.err != nil {
		return nil, nil, f.err
	}
	return &models.RampSellIntent{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Status: models.SellIntentSwapping},
		&services.SellSwapQuote{LiFiQuote: json.RawMessage(`{"id":"q"}`), GasTopUp: true}, nil
}

func (f *fakePayStarter) CryptoQuote(_ context.Context, u *models.User, _ uuid.UUID, in services.PayConvertedInput) (*services.PayCryptoQuote, error) {
	f.calls, f.lastUser, f.converted = f.calls+1, u, in
	if f.err != nil {
		return nil, f.err
	}
	return &services.PayCryptoQuote{FromAmount: "17000000000000000", DestAmount: "33323336332433603252", DestDecimals: 18}, nil
}

func (f *fakePayStarter) PayCrypto(ctx context.Context, u *models.User, id uuid.UUID, in services.PayConvertedInput) (*models.RampSellIntent, *services.SellSwapQuote, error) {
	return f.PayConverted(ctx, u, id, in)
}

const payStartWallet = "0x00000000000000000000000000000000000000aa"

func payStartRouter(t *testing.T, starter payCheckoutStarter) (*gin.Engine, string) {
	t.Helper()
	h, _, _ := newPayCheckoutRig(t)
	if starter != nil {
		h.WithCheckout(starter)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	u := &models.User{ID: uuid.New(), WalletAddress: payStartWallet}
	r.Use(func(c *gin.Context) { c.Set("user", u); c.Next() })
	r.GET("/pay/checkout/:id/quote", h.PayQuote)
	r.POST("/pay/checkout/:id/direct", h.PayDirect)
	r.POST("/pay/checkout/:id/converted", h.PayConverted)
	r.GET("/pay/checkout/:id/crypto-quote", h.PayCryptoQuote)
	r.POST("/pay/checkout/:id/crypto", h.PayCrypto)
	return r, "/pay/checkout/" + uuid.New().String()
}

func payDecode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	return out
}

func TestPayStart_UnwiredRoutesAreServiceUnavailable(t *testing.T) {
	r, base := payStartRouter(t, nil)
	for _, tc := range []struct{ method, path string }{
		{"GET", base + "/quote?sourceChainId=56&sourceToken=0x0&sourceAddress=0xa"},
		{"POST", base + "/direct"},
		{"POST", base + "/converted"},
		{"GET", base + "/crypto-quote?sourceChainId=56&sourceToken=0x0&sourceAddress=0xa"},
		{"POST", base + "/crypto"},
	} {
		w := payDo(r, tc.method, tc.path, map[string]any{"chainKey": "base", "token": "USDC"}, nil)
		got := payDecode(t, w.Body.Bytes())
		if w.Code != http.StatusServiceUnavailable || got["message"] != "Live payments aren't available right now" {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestPayStart_DirectReturnsTheOrderTheSellRouteReturns(t *testing.T) {
	f := &fakePayStarter{}
	r, base := payStartRouter(t, f)
	w := payDo(r, "POST", base+"/direct", map[string]any{"chainKey": "base", "token": "USDC"}, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	data := payDecode(t, w.Body.Bytes())["data"].(map[string]any)
	order := data["order"].(map[string]any)
	if order["id"] != "11111111-1111-1111-1111-111111111111" || order["depositAddress"] != "0xdeposit" || order["amount"] != "50000.00" {
		t.Fatalf("order = %v", order)
	}
	if f.direct.ChainKey != "base" || f.direct.Token != "USDC" || f.lastUser.WalletAddress != payStartWallet {
		t.Fatalf("input %+v user %+v", f.direct, f.lastUser)
	}
	for _, bad := range []map[string]any{{"chainKey": "base"}, {"token": "USDC"}, {}} {
		if w := payDo(r, "POST", base+"/direct", bad, nil); w.Code != http.StatusBadRequest {
			t.Fatalf("body %v: %d", bad, w.Code)
		}
	}
}

func TestPayStart_ConvertedReturnsTheSellIntentShape(t *testing.T) {
	f := &fakePayStarter{}
	r, base := payStartRouter(t, f)
	body := map[string]any{"sourceChainId": 56, "sourceToken": "0x0000000000000000000000000000000000000000", "sourceAddress": "0xabc"}
	w := payDo(r, "POST", base+"/converted", body, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	data := payDecode(t, w.Body.Bytes())["data"].(map[string]any)
	if data["intent"].(map[string]any)["id"] != "22222222-2222-2222-2222-222222222222" || data["gasTopUp"] != true || data["lifiQuote"].(map[string]any)["id"] != "q" {
		t.Fatalf("data = %v", data)
	}
	if f.converted.SourceChainID != 56 || f.converted.SourceAddress != "0xabc" {
		t.Fatalf("input %+v", f.converted)
	}
	for _, bad := range []map[string]any{{"sourceToken": "0x0", "sourceAddress": "0xa"}, {"sourceChainId": 56, "sourceAddress": "0xa"}, {"sourceChainId": 56, "sourceToken": "0x0"}} {
		if w := payDo(r, "POST", base+"/converted", bad, nil); w.Code != http.StatusBadRequest {
			t.Fatalf("body %v: %d", bad, w.Code)
		}
	}
}

func TestPayStart_QuoteAnswersFromAmountAndTarget(t *testing.T) {
	f := &fakePayStarter{}
	r, base := payStartRouter(t, f)
	w := payDo(r, "GET", base+"/quote?sourceChainId=56&sourceToken=0x0&sourceAddress=0xabc", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	data := payDecode(t, w.Body.Bytes())["data"].(map[string]any)
	if data["fromAmount"] != "17000000000000000" || data["usdcTarget"] != "33835000" {
		t.Fatalf("data = %v", data)
	}
	for _, q := range []string{"", "?sourceChainId=x&sourceToken=0x0&sourceAddress=0xa", "?sourceChainId=56&sourceAddress=0xa", "?sourceChainId=56&sourceToken=0x0"} {
		if w := payDo(r, "GET", base+"/quote"+q, nil, nil); w.Code != http.StatusBadRequest {
			t.Fatalf("query %q: %d", q, w.Code)
		}
	}
	if w := payDo(r, "GET", "/pay/checkout/not-a-uuid/quote?sourceChainId=56&sourceToken=0x0&sourceAddress=0xa", nil, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", w.Code)
	}
}

func TestPayStart_ErrorsMapToTheirStatusesAndCodes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{services.ErrPayNotPayable, http.StatusConflict, ""},
		{services.ErrPayInProgress, http.StatusConflict, "pay_in_progress"},
		{services.ErrPaycrestOrderOpen, http.StatusConflict, ""},
		{services.ErrSellIntentOpen, http.StatusConflict, ""},
		{services.ErrSellRateMoved, http.StatusConflict, "rate_moved"},
		{services.ErrKYCVerificationRequired, http.StatusForbidden, "kyc_verification_required"},
		{services.ErrKYCLegalNameMissing, http.StatusForbidden, "kyc_legal_name_missing"},
		{services.ErrPayWrongRoute, http.StatusUnprocessableEntity, ""},
		{services.ErrPaySameToken, http.StatusUnprocessableEntity, ""},
		{services.ErrSellRouteTooCostly, http.StatusUnprocessableEntity, ""},
		{services.ErrSellBadSourceAddress, http.StatusBadRequest, ""},
		{services.ErrSellUnavailable, http.StatusServiceUnavailable, ""},
		{services.ErrPayPaymentNotFound, http.StatusNotFound, ""},
		{fmt.Errorf("database exploded: %w", errors.New("boom")), http.StatusBadGateway, ""},
	} {
		f := &fakePayStarter{err: tc.err}
		r, base := payStartRouter(t, f)
		for _, route := range []struct{ method, path string }{
			{"POST", base + "/direct"},
			{"POST", base + "/converted"},
			{"GET", base + "/quote?sourceChainId=56&sourceToken=0x0&sourceAddress=0xa"},
			{"POST", base + "/crypto"},
			{"GET", base + "/crypto-quote?sourceChainId=56&sourceToken=0x0&sourceAddress=0xa"},
		} {
			w := payDo(r, route.method, route.path, map[string]any{"chainKey": "base", "token": "USDC", "sourceChainId": 56, "sourceToken": "0x0", "sourceAddress": "0xa"}, nil)
			got := payDecode(t, w.Body.Bytes())
			code, _ := got["code"].(string)
			if w.Code != tc.status || code != tc.code || got["success"] != false {
				t.Fatalf("%v on %s: %d %s", tc.err, route.path, w.Code, w.Body.String())
			}
		}
	}
}

func TestPayStart_CryptoReturnsTheSameShapeAsConverted(t *testing.T) {
	f := &fakePayStarter{}
	r, base := payStartRouter(t, f)
	body := map[string]any{"sourceChainId": 1, "sourceToken": "0x0000000000000000000000000000000000000000", "sourceAddress": "0xabc"}
	w := payDo(r, "POST", base+"/crypto", body, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	data := payDecode(t, w.Body.Bytes())["data"].(map[string]any)
	if data["intent"].(map[string]any)["id"] != "22222222-2222-2222-2222-222222222222" || data["gasTopUp"] != true || data["lifiQuote"].(map[string]any)["id"] != "q" {
		t.Fatalf("data = %v", data)
	}
	if f.converted.SourceChainID != 1 || f.converted.SourceAddress != "0xabc" || f.lastUser.WalletAddress != payStartWallet {
		t.Fatalf("input %+v user %+v", f.converted, f.lastUser)
	}
	for _, bad := range []map[string]any{{"sourceToken": "0x0", "sourceAddress": "0xa"}, {"sourceChainId": 1, "sourceAddress": "0xa"}, {"sourceChainId": 1, "sourceToken": "0x0"}} {
		if w := payDo(r, "POST", base+"/crypto", bad, nil); w.Code != http.StatusBadRequest {
			t.Fatalf("body %v: %d", bad, w.Code)
		}
	}
}

func TestPayStart_CryptoQuoteAnswersWhatTheMerchantReceives(t *testing.T) {
	f := &fakePayStarter{}
	r, base := payStartRouter(t, f)
	w := payDo(r, "GET", base+"/crypto-quote?sourceChainId=1&sourceToken=0x0&sourceAddress=0xabc", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	data := payDecode(t, w.Body.Bytes())["data"].(map[string]any)
	if data["fromAmount"] != "17000000000000000" || data["destAmount"] != "33323336332433603252" || data["destDecimals"] != float64(18) {
		t.Fatalf("data = %v", data)
	}
	if _, ok := data["usdcTarget"]; ok {
		t.Fatalf("a crypto quote has no USDC target: %v", data)
	}
	for _, q := range []string{"", "?sourceChainId=x&sourceToken=0x0&sourceAddress=0xa", "?sourceChainId=1&sourceAddress=0xa"} {
		if w := payDo(r, "GET", base+"/crypto-quote"+q, nil, nil); w.Code != http.StatusBadRequest {
			t.Fatalf("query %q: %d", q, w.Code)
		}
	}
}
