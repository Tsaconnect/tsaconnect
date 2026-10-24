package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakeEnroller struct {
	pm    *models.PaymentMerchant
	err   error
	calls []services.PMEnrollInput
}

func (f *fakeEnroller) EnrollApprovedMerchant(_ context.Context, in services.PMEnrollInput) (*models.PaymentMerchant, error) {
	f.calls = append(f.calls, in)
	return f.pm, f.err
}

func postEnroll(t *testing.T, h *PMHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	user := testAdminUser()
	r.Use(func(c *gin.Context) { c.Set("user", user); c.Next() })
	r.POST("/p2p/pm/enroll", h.EnrollPM)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/p2p/pm/enroll", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

const validEnrollBody = `{"makerWalletAddress":"0x00000000000000000000000000000000000000aa"}`

func TestEnrollPM_Outcomes(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		enroller  *fakeEnroller
		wantCode  int
		wantCalls int
	}{
		{"enrolled", validEnrollBody, &fakeEnroller{pm: &models.PaymentMerchant{Status: models.PaymentMerchantStatusApproved}}, http.StatusOK, 1},
		{"zero wallet", `{"makerWalletAddress":"0x0000000000000000000000000000000000000000"}`, &fakeEnroller{}, http.StatusBadRequest, 0},
		{"no payout account", validEnrollBody, &fakeEnroller{err: services.ErrPayoutAccountRequired}, http.StatusConflict, 1},
		{"not a merchant", validEnrollBody, &fakeEnroller{err: services.ErrNotAMerchant}, http.StatusForbidden, 1},
		{"admin decided", validEnrollBody, &fakeEnroller{err: services.ErrPMEnrollDecided}, http.StatusConflict, 1},
		{"second role off", validEnrollBody, &fakeEnroller{err: services.ErrSecondRoleNotEnabled}, http.StatusConflict, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewPMHandler(&fakePMAutomation{}, nil)
			h.SetEnroller(tc.enroller)
			w := postEnroll(t, h, tc.body)
			if w.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}
			if len(tc.enroller.calls) != tc.wantCalls {
				t.Fatalf("enroller calls = %d, want %d", len(tc.enroller.calls), tc.wantCalls)
			}
		})
	}
}

func TestEnrollPM_UnwiredIs503(t *testing.T) {
	h := NewPMHandler(&fakePMAutomation{}, nil)
	if w := postEnroll(t, h, validEnrollBody); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", w.Code)
	}
}
