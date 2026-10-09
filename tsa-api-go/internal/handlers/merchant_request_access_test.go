package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Review I3: the admin merchant-request queue is staff work. A marketplace
// merchant (whom AdminAuth admits) gets 403 on list, approve and reject;
// support may list and decide marketplace requests but never sees bank
// details, wallets or an LP's verified legal name, and deciding a payment
// merchant, LP or TSA Pay request needs a P2P admin.

func listMerchantRequestsAs(h *MerchantRequestHandler, caller *models.User) (int, []map[string]any) {
	w, c := newMerchantContext("GET", "/api/admin/merchant-requests", nil, caller)
	h.ListMerchantRequests(c)
	var resp struct {
		Data struct {
			Requests []map[string]any `json:"requests"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp.Data.Requests
}

func rejectAs(h *MerchantRequestHandler, caller *models.User, reqID string) int {
	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/reject", map[string]any{"note": "no"}, caller)
	c.Params = gin.Params{{Key: "id", Value: reqID}}
	h.RejectMerchantRequest(c)
	return w.Code
}

func requestIDOfType(t *testing.T, db *gorm.DB, businessType string) string {
	t.Helper()
	u := createMerchantTestUser(t, db, "user")
	seedMerchantRequest(t, db, u.ID, businessType, models.MerchantRequestStatusPending)
	var mr models.MerchantRequest
	if err := db.Where("user_id = ?", u.ID).First(&mr).Error; err != nil {
		t.Fatal(err)
	}
	return mr.ID.String()
}

func TestListMerchantRequests_StaffOnlyAndRedactedForSupport(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	lpRequestID(t, db, h, createMerchantTestUser(t, db, "user"))

	if code, _ := listMerchantRequestsAs(h, nil); code != http.StatusUnauthorized {
		t.Fatalf("no caller = %d, want 401", code)
	}
	for _, role := range []string{"user", "merchant"} {
		if code, rows := listMerchantRequestsAs(h, createMerchantTestUser(t, db, role)); code != http.StatusForbidden || rows != nil {
			t.Fatalf("%s list = %d (%d rows), want 403", role, code, len(rows))
		}
	}
	sensitive := []string{"settlementBankCode", "settlementAccountNumber", "settlementAccountName", "makerWalletAddress"}

	code, rows := listMerchantRequestsAs(h, createMerchantTestUser(t, db, "support"))
	if code != http.StatusOK || len(rows) != 1 {
		t.Fatalf("support list = %d (%d rows), want 200 with the LP request", code, len(rows))
	}
	for _, f := range sensitive {
		if _, ok := rows[0][f]; ok {
			t.Errorf("support sees %s", f)
		}
	}
	if rows[0]["businessName"] == "ADA OBI" {
		t.Error("support sees the LP's verified legal name")
	}

	code, rows = listMerchantRequestsAs(h, createMerchantTestUser(t, db, "admin"))
	if code != http.StatusOK || len(rows) != 1 || rows[0]["businessName"] != "ADA OBI" {
		t.Fatalf("admin list = %d %v", code, rows)
	}
	for _, f := range sensitive {
		if rows[0][f] == nil {
			t.Errorf("admin does not see %s", f)
		}
	}
}

func TestRejectMerchantRequest_StaffAndAdminGates(t *testing.T) {
	cases := []struct {
		businessType string
		role         string
		want         int
	}{
		{models.BusinessTypeGeneralProducts, "merchant", http.StatusForbidden},
		{models.BusinessTypeGeneralProducts, "user", http.StatusForbidden},
		{models.BusinessTypeGeneralProducts, "support", http.StatusOK},
		{models.BusinessTypeGeneralProducts, "admin", http.StatusOK},
		{models.BusinessTypeP2PMerchant, "merchant", http.StatusForbidden},
		{models.BusinessTypeP2PMerchant, "support", http.StatusForbidden},
		{models.BusinessTypeP2PMerchant, "admin", http.StatusOK},
		{models.BusinessTypePaymentAcceptance, "merchant", http.StatusForbidden},
		{models.BusinessTypePaymentAcceptance, "support", http.StatusForbidden},
		{models.BusinessTypePaymentAcceptance, "super_admin", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.businessType+"/"+tc.role, func(t *testing.T) {
			db := setupMerchantTestDB(t)
			h := lpHandler(t, db, true)
			id := requestIDOfType(t, db, tc.businessType)
			if code := rejectAs(h, createMerchantTestUser(t, db, tc.role), id); code != tc.want {
				t.Fatalf("reject = %d, want %d", code, tc.want)
			}
			var mr models.MerchantRequest
			db.First(&mr, "id = ?", id)
			if tc.want != http.StatusOK && mr.Status != models.MerchantRequestStatusPending {
				t.Fatalf("a refused reject changed the request to %s", mr.Status)
			}
		})
	}
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	id := requestIDOfType(t, db, models.BusinessTypeGeneralProducts)
	w, c := newMerchantContext("POST", "/", map[string]any{"note": "no"}, nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.RejectMerchantRequest(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no caller = %d, want 401", w.Code)
	}
}

func TestApproveMerchantRequest_MarketplaceMerchantForbidden(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	id := requestIDOfType(t, db, models.BusinessTypeGeneralProducts)
	if code := approveAs(t, h, createMerchantTestUser(t, db, "merchant"), id); code != http.StatusForbidden {
		t.Fatalf("merchant approve = %d, want 403", code)
	}
	if code := approveAs(t, h, createMerchantTestUser(t, db, "support"), id); code != http.StatusOK {
		t.Fatalf("support approve of a marketplace request = %d, want 200", code)
	}
}
