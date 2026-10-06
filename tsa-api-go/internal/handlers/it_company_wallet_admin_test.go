package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakeWalletAdmin struct {
	err            error
	lastAdmin      uuid.UUID
	lastNote, last string
}

func (f *fakeWalletAdmin) CompanyWalletStatus(_ context.Context, low float64) (*services.CompanyWalletStatus, error) {
	return &services.CompanyWalletStatus{Address: "0xw", LowBalanceUSD: low}, f.err
}
func (f *fakeWalletAdmin) ListCompanyWalletOrders(context.Context) ([]services.CompanyWalletOrder, error) {
	return []services.CompanyWalletOrder{{ID: uuid.New(), Actions: []string{"deliver", "refund"}}}, f.err
}
func (f *fakeWalletAdmin) act(name string, admin uuid.UUID, note string) (*models.ITIntent, error) {
	f.last, f.lastAdmin, f.lastNote = name, admin, note
	if f.err != nil {
		return nil, f.err
	}
	return &models.ITIntent{ID: uuid.New(), Status: models.ITStatusPaymentReceived}, nil
}
func (f *fakeWalletAdmin) DeliverParkedTreasuryBuy(_ context.Context, a, _ uuid.UUID, n string) (*models.ITIntent, error) {
	return f.act("deliver", a, n)
}
func (f *fakeWalletAdmin) MarkStableBuyRefunded(_ context.Context, a, _ uuid.UUID, n string) (*models.ITIntent, error) {
	return f.act("refund", a, n)
}
func (f *fakeWalletAdmin) ResendTreasuryBuy(_ context.Context, a, _ uuid.UUID, n string) (*models.ITIntent, error) {
	return f.act("resend", a, n)
}

func walletAdminServer(f *fakeWalletAdmin, admin uuid.UUID) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewITHandler(nil, nil, nil)
	h.SetCompanyWalletAdmin(f, 50)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", &models.User{ID: admin}) })
	r.GET("/w", h.AdminCompanyWallet)
	r.GET("/w/orders", h.AdminCompanyWalletOrders)
	r.POST("/w/orders/:id/deliver", h.AdminDeliverTreasuryBuy)
	r.POST("/w/orders/:id/refund", h.AdminRefundStableBuy)
	r.POST("/w/orders/:id/resend", h.AdminResendTreasuryBuy)
	return r
}

func walletDo(r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
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

func TestCompanyWalletAdmin_ReadsAndActs(t *testing.T) {
	admin := uuid.New()
	f := &fakeWalletAdmin{}
	r := walletAdminServer(f, admin)
	if w := walletDo(r, "GET", "/w", nil); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"lowBalanceUsd":50`)) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	if w := walletDo(r, "GET", "/w/orders", nil); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"orders":[`)) {
		t.Fatalf("orders: %d %s", w.Code, w.Body)
	}
	for _, action := range []string{"deliver", "refund", "resend"} {
		w := walletDo(r, "POST", "/w/orders/"+uuid.NewString()+"/"+action, map[string]string{"note": "checked basescan"})
		if w.Code != 200 || f.last != action || f.lastAdmin != admin || f.lastNote != "checked basescan" {
			t.Fatalf("%s: %d %s (last=%s)", action, w.Code, w.Body, f.last)
		}
	}
	if w := walletDo(r, "POST", "/w/orders/not-a-uuid/refund", map[string]string{"note": "x"}); w.Code != 400 {
		t.Fatalf("bad id: %d", w.Code)
	}
}

func TestCompanyWalletAdmin_RefusalCodes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{services.ErrITOperatorNoteRequired, 400, "note_required"},
		{services.ErrITBadState, 409, "bad_state"},
		{fmt.Errorf("%w: needs 216.99", services.ErrTreasuryStockShort), 409, "stock_short"},
		{fmt.Errorf("%w: pending tx", services.ErrTreasuryResendRefused), 409, "resend_refused"},
		{services.ErrStableIntentNotFound, 404, "not_found"},
	} {
		w := walletDo(walletAdminServer(&fakeWalletAdmin{err: tc.err}, uuid.New()), "POST",
			"/w/orders/"+uuid.NewString()+"/deliver", map[string]string{"note": "n"})
		var env struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if w.Code != tc.status || env.Code != tc.code {
			t.Errorf("%v → %d %s", tc.err, w.Code, w.Body)
		}
	}
}

func TestCompanyWalletAdmin_DisabledWithoutAWallet(t *testing.T) {
	if NewITHandler(nil, nil, nil).CompanyWalletEnabled() {
		t.Fatal("enabled with no wallet admin")
	}
	var nilAdmin *fakeWalletAdmin
	h := NewITHandler(nil, nil, nil)
	h.SetCompanyWalletAdmin(nilAdmin, 50)
	if h.CompanyWalletEnabled() {
		t.Fatal("a typed nil must not enable the routes")
	}
}

func TestCompanyWalletAdmin_BlankNoteRefusedBeforeActing(t *testing.T) {
	f := &fakeWalletAdmin{}
	r := walletAdminServer(f, uuid.New())
	for _, note := range []string{"", "   "} {
		w := walletDo(r, "POST", "/w/orders/"+uuid.NewString()+"/refund", map[string]string{"note": note})
		if w.Code != 400 || !bytes.Contains(w.Body.Bytes(), []byte(`"note_required"`)) || f.last != "" {
			t.Fatalf("note %q: %d %s (acted: %q)", note, w.Code, w.Body, f.last)
		}
	}
}

func TestCompanyWalletAdmin_UnexpectedErrorsAreRedacted(t *testing.T) {
	const secret = "https://base-mainnet.g.alchemy.com/v2/abcdef0123456789abcdef0123456789"
	f := &fakeWalletAdmin{err: fmt.Errorf("dial %s: refused", secret)}
	r := walletAdminServer(f, uuid.New())
	for _, w := range []*httptest.ResponseRecorder{
		walletDo(r, "GET", "/w", nil),
		walletDo(r, "POST", "/w/orders/"+uuid.NewString()+"/resend", map[string]string{"note": "n"}),
	} {
		if w.Code != 500 || bytes.Contains(w.Body.Bytes(), []byte("abcdef0123456789")) {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
}
