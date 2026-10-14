package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Restored from merchant_request_lp_test.go (deleted with the slice pool in
// 4cf9bb39). The type filters are still live; the fixtures use request types
// that stay valid rather than liquidity_provider.

func TestGetMyMerchantRequest_FiltersByType(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := NewMerchantRequestHandler(db, newTestEventBus(t))
	u := createMerchantTestUser(t, db, "user")
	seedMerchantRequest(t, db, u.ID, models.BusinessTypeP2PMerchant, models.MerchantRequestStatusPending)
	seedMerchantRequest(t, db, u.ID, models.BusinessTypeGeneralProducts, models.MerchantRequestStatusApproved) // newer
	db.Exec(`UPDATE merchant_requests SET created_at = datetime('now', '+1 minute') WHERE user_id = ? AND business_type = ?`,
		u.ID.String(), models.BusinessTypeGeneralProducts)

	// Unfiltered, the newest request wins: the filter is what selects the older one.
	w, c := newMerchantContext("GET", "/api/merchant-requests/my-request", nil, u)
	h.GetMyMerchantRequest(c)
	var body struct{ Data models.MerchantRequest }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusOK || body.Data.BusinessType != models.BusinessTypeGeneralProducts {
		t.Fatalf("my-request = %d %s, want the newest (general_products)", w.Code, w.Body.String())
	}

	w, c = newMerchantContext("GET", "/api/merchant-requests/my-request?type=p2p_merchant", nil, u)
	h.GetMyMerchantRequest(c)
	body = struct{ Data models.MerchantRequest }{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusOK || body.Data.BusinessType != models.BusinessTypeP2PMerchant {
		t.Fatalf("my-request?type=p2p_merchant = %d %s", w.Code, w.Body.String())
	}

	w, c = newMerchantContext("GET", "/api/merchant-requests/my-request?type=bogus", nil, u)
	h.GetMyMerchantRequest(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bogus type = %d, want 400", w.Code)
	}
}

func TestListMerchantRequests_FiltersByBusinessType(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := NewMerchantRequestHandler(db, newTestEventBus(t))
	seedMerchantRequest(t, db, createMerchantTestUser(t, db, "user").ID, models.BusinessTypeP2PMerchant, models.MerchantRequestStatusPending)
	seedMerchantRequest(t, db, createMerchantTestUser(t, db, "user").ID, models.BusinessTypeGeneralProducts, models.MerchantRequestStatusPending)

	w, c := newMerchantContext("GET", "/api/admin/merchant-requests?businessType=p2p_merchant", nil, createMerchantTestUser(t, db, "admin"))
	h.ListMerchantRequests(c)
	var resp struct {
		Data struct {
			Requests []models.MerchantRequest `json:"requests"`
			Total    int64                    `json:"total"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if w.Code != http.StatusOK || resp.Data.Total != 1 || len(resp.Data.Requests) != 1 || resp.Data.Requests[0].BusinessType != models.BusinessTypeP2PMerchant {
		t.Fatalf("filtered list = %d %+v", w.Code, resp.Data)
	}

	w, c = newMerchantContext("GET", "/api/admin/merchant-requests?businessType=nope", nil, createMerchantTestUser(t, db, "admin"))
	h.ListMerchantRequests(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown type = %d, want 400", w.Code)
	}
}
