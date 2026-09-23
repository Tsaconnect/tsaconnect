package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// paycrest_sell_intents_test.go — Task 13: the sale routes.
//
// The service owns every decision about money; what is pinned here is the two
// things only the handler can get wrong — WHOSE wallet the USDC is promised
// to, and whether a "still swapping" reads to the app as a wait rather than a
// failure.

type stubSellIntents struct {
	last     services.CreateSellIntentInput
	lastUser *models.User
	intent   *models.RampSellIntent
	quote    *services.SellSwapQuote
	err      error
	open     *models.RampSellIntent
}

func (s *stubSellIntents) Create(_ context.Context, user *models.User, in services.CreateSellIntentInput) (*models.RampSellIntent, *services.SellSwapQuote, error) {
	s.last, s.lastUser = in, user
	if s.err != nil {
		return nil, nil, s.err
	}
	return s.intent, s.quote, nil
}

func (s *stubSellIntents) RecordSwap(context.Context, uuid.UUID, uuid.UUID, string) error {
	return s.err
}

func (s *stubSellIntents) OpenOrder(context.Context, *models.User, uuid.UUID) (*models.PaycrestOrderRecord, error) {
	return nil, s.err
}

func (s *stubSellIntents) Abandon(context.Context, uuid.UUID, uuid.UUID) error { return s.err }

func (s *stubSellIntents) Open(context.Context, uuid.UUID) (*models.RampSellIntent, error) {
	return s.open, nil
}

func postSellIntent(t *testing.T, h *PaycrestHandler, user *models.User, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/paycrest/sell-intents", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", user)
	h.CreateSellIntent(c)
	return w
}

func sellIntentBody() map[string]any {
	return map[string]any{
		"sourceChainId": 56, "sourceToken": "0x0000000000000000000000000000000000000000",
		"sourceAmount": "16000000000000000", "sourceAddress": sellSwapUserWallet,
		"bankCode": "044", "accountNumber": "0123456789", "currency": "NGN",
	}
}

// The sale is opened for the account's OWN wallet. A device that asks for the
// USDC to land somewhere another account holds is refused outright — a sale
// delivered to a stranger's wallet cannot be undone.
func TestCreateSellIntent_SellsToTheReconciledWallet(t *testing.T) {
	db := newCallerWalletTestDB(t)
	user := &models.User{ID: uuid.New(), Username: "seller", WalletAddress: sellSwapUserWallet,
		AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, user)
	insertTestUser(t, db, &models.User{ID: uuid.New(), Username: "someone-else",
		WalletAddress: sellSwapOtherWallet, AccountStatus: models.AccountStatusActive})

	stub := &stubSellIntents{
		intent: &models.RampSellIntent{ID: uuid.New()},
		quote:  &services.SellSwapQuote{LiFiQuote: json.RawMessage(`{"ok":true}`)},
	}
	h := (&PaycrestHandler{}).WithSellIntents(stub)

	if w := postSellIntent(t, h, user, sellIntentBody()); w.Code != http.StatusCreated {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}
	if stub.lastUser == nil || stub.lastUser.WalletAddress != sellSwapUserWallet {
		t.Fatalf("sale opened for wallet %+v", stub.lastUser)
	}

	body := sellIntentBody()
	body["walletAddress"] = sellSwapOtherWallet
	if w := postSellIntent(t, h, user, body); w.Code != http.StatusConflict {
		t.Fatalf("claiming another account's wallet: code = %d", w.Code)
	}
}

// A sale still swapping is a WAIT. Answering 500 (or anything the app reads
// as broken) would send her back to start a second swap for money she has
// already sent.
func TestSellIntentRoutes_MapTheServiceSentinels(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"still swapping", services.ErrSwapNotLanded, http.StatusConflict},
		{"not her swap", services.ErrSwapMismatch, http.StatusConflict},
		{"wrong step", services.ErrSellIntentState, http.StatusConflict},
		{"already selling", services.ErrSellIntentOpen, http.StatusConflict},
		{"no such sale", services.ErrSellIntentNotFound, http.StatusNotFound},
		{"too costly", services.ErrSellRouteTooCostly, http.StatusUnprocessableEntity},
		{"unavailable", services.ErrSellUnavailable, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := &models.User{ID: uuid.New(), WalletAddress: sellSwapUserWallet}
			h := (&PaycrestHandler{}).WithSellIntents(&stubSellIntents{err: tc.err})
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/paycrest/sell-intents/x/order", nil)
			c.Params = gin.Params{{Key: "id", Value: uuid.NewString()}}
			c.Set("user", user)
			h.OpenSellIntentOrder(c)
			if w.Code != tc.want {
				t.Fatalf("code = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// Without RAMP_CONVERSION_SELL nothing is wired, and the routes are not even
// registered — a handler reached anyway must not pretend to have a sale.
func TestSellIntentRoutes_DormantWithoutTheService(t *testing.T) {
	user := &models.User{ID: uuid.New(), WalletAddress: sellSwapUserWallet}
	h := &PaycrestHandler{}
	if h.SellIntentsEnabled() {
		t.Fatal("sales enabled with no service")
	}
	if w := postSellIntent(t, h, user, sellIntentBody()); w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}
