package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// The IT-backed refusals on the shared mapping: existing codes where one
// exists, their own words otherwise.
func TestPaycrestError_ITRefusalsHaveTheirOwnWords(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		err    error
		status int
		code   string
		part   string
	}{
		{services.ErrITAgreementRequired, http.StatusForbidden, "it_agreement_required", "trading agreement"},
		{fmt.Errorf("%w: you have no POLYGON address to receive on", services.ErrBadDeliveryAddress), http.StatusBadRequest, "", "no wallet address saved"},
		{&services.StableBuyOpenError{IntentID: uuid.New()}, http.StatusConflict, "", "awaiting payment"},
		{services.ErrStableBuyBelowMinimum, http.StatusBadRequest, "", "below the minimum"},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		paycrestError(c, tc.err)
		var env struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if w.Code != tc.status || env.Code != tc.code || !strings.Contains(env.Message, tc.part) {
			t.Errorf("%v → %d %s", tc.err, w.Code, w.Body)
		}
	}
}
