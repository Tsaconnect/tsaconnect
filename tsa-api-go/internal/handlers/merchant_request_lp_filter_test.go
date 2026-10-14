package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// A liquidity_provider row left by the slice pool stays readable through the
// type filters (the app's my-request lookup, the admin console's list), while
// submitting a new one stays refused.
func TestMerchantRequests_RetiredLPTypeReadableNotSubmittable(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := NewMerchantRequestHandler(db, newTestEventBus(t))
	u := createMerchantTestUser(t, db, "user")
	seedMerchantRequest(t, db, u.ID, models.BusinessTypeLiquidityProvider, models.MerchantRequestStatusPending)
	seedMerchantRequest(t, db, u.ID, models.BusinessTypeGeneralProducts, models.MerchantRequestStatusPending)

	w, c := newMerchantContext("GET", "/api/merchant-requests/my-request?type=liquidity_provider", nil, u)
	h.GetMyMerchantRequest(c)
	var mine struct{ Data models.MerchantRequest }
	_ = json.Unmarshal(w.Body.Bytes(), &mine)
	if w.Code != http.StatusOK || mine.Data.BusinessType != models.BusinessTypeLiquidityProvider {
		t.Fatalf("my-request?type=liquidity_provider = %d %s", w.Code, w.Body.String())
	}

	w, c = newMerchantContext("GET", "/api/admin/merchant-requests?businessType=liquidity_provider", nil, createMerchantTestUser(t, db, "admin"))
	h.ListMerchantRequests(c)
	var list struct {
		Data struct {
			Requests []models.MerchantRequest `json:"requests"`
			Total    int64                    `json:"total"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != http.StatusOK || list.Data.Total != 1 || list.Data.Requests[0].BusinessType != models.BusinessTypeLiquidityProvider {
		t.Fatalf("admin ?businessType=liquidity_provider = %d %s", w.Code, w.Body.String())
	}

	in := validMerchantInput()
	in["businessType"] = models.BusinessTypeLiquidityProvider
	w, c = newMerchantContext("POST", "/api/merchant-requests", in, createMerchantTestUser(t, db, "user"))
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("submit liquidity_provider = %d %s, want 400", w.Code, w.Body.String())
	}
}
