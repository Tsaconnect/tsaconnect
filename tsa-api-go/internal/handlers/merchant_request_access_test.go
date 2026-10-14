package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
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
	h := pmRequestHandler(t, db)
	applicant := createMerchantTestUser(t, db, "user")
	db.Model(&models.User{}).Where("id = ?", applicant.ID).Update("wallet_address", "0x52908400098527886E0F7030069857D2E4169EE7")
	seedMerchantRequest(t, db, applicant.ID, models.BusinessTypeP2PMerchant, models.MerchantRequestStatusPending)
	db.Model(&models.MerchantRequest{}).Where("user_id = ?", applicant.ID).Updates(map[string]any{
		"settlement_bank_code": "058", "settlement_account_number": "0123456789", "settlement_account_name": "ACME LTD",
		"maker_wallet_address": "0x52908400098527886E0F7030069857D2E4169EE7"})

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
		t.Fatalf("support list = %d (%d rows), want 200 with the request", code, len(rows))
	}
	for _, f := range sensitive {
		if _, ok := rows[0][f]; ok {
			t.Errorf("support sees %s", f)
		}
	}
	// Review N1: nor through the applicant's user object.
	if u, _ := rows[0]["user"].(map[string]any); u == nil || u["walletAddress"] != nil {
		t.Errorf("support sees the applicant's wallet via user: %v", rows[0]["user"])
	}

	code, rows = listMerchantRequestsAs(h, createMerchantTestUser(t, db, "admin"))
	if code != http.StatusOK || len(rows) != 1 || rows[0]["businessName"] != "Seed Corp" {
		t.Fatalf("admin list = %d %v", code, rows)
	}
	if u, _ := rows[0]["user"].(map[string]any); u == nil || u["walletAddress"] == nil {
		t.Errorf("admin does not see the applicant's wallet: %v", rows[0]["user"])
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
			h := pmRequestHandler(t, db)
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
	h := pmRequestHandler(t, db)
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
	h := pmRequestHandler(t, db)
	id := requestIDOfType(t, db, models.BusinessTypeGeneralProducts)
	if code := approveAs(t, h, createMerchantTestUser(t, db, "merchant"), id); code != http.StatusForbidden {
		t.Fatalf("merchant approve = %d, want 403", code)
	}
	if code := approveAs(t, h, createMerchantTestUser(t, db, "support"), id); code != http.StatusOK {
		t.Fatalf("support approve of a marketplace request = %d, want 200", code)
	}
}

// Review N4: a non-staff caller learns nothing about which request ids exist.
func TestRejectMerchantRequest_NonStaffGets403BeforeTheLookup(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := pmRequestHandler(t, db)
	if code := rejectAs(h, createMerchantTestUser(t, db, "merchant"), "00000000-0000-0000-0000-000000000001"); code != http.StatusForbidden {
		t.Fatalf("merchant reject of an unknown id = %d, want 403", code)
	}
}

func pmRequestHandler(t *testing.T, db *gorm.DB) *MerchantRequestHandler {
	t.Helper()
	h := NewMerchantRequestHandlerWithPM(db, newTestEventBus(t), services.NewPMOnboardingService(db, nil))
	h.PMService = services.NewPMService(db)
	return h
}

func approveAs(t *testing.T, h *MerchantRequestHandler, caller *models.User, reqID string) int {
	t.Helper()
	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/approve", map[string]any{"note": "ok"}, caller)
	c.Params = gin.Params{{Key: "id", Value: reqID}}
	h.ApproveMerchantRequest(c)
	return w.Code
}

// The shared pool activates LPs itself (spec 2026-10-14 §5): a liquidity
// application left pending from the slice pool is never approved, and a new
// one is not a valid request type.
func TestApproveMerchantRequest_LegacyLPApplicationRefused(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := pmRequestHandler(t, db)
	id := requestIDOfType(t, db, models.BusinessTypeLiquidityProvider)
	if code := approveAs(t, h, createMerchantTestUser(t, db, "admin"), id); code != http.StatusConflict {
		t.Fatalf("approve = %d, want 409", code)
	}
	var rows int64
	db.Model(&models.PaymentMerchant{}).Count(&rows)
	if rows != 0 {
		t.Fatalf("payment_merchants rows = %d, want none", rows)
	}
	for _, bt := range models.ValidBusinessTypes {
		if bt == models.BusinessTypeLiquidityProvider {
			t.Fatal("liquidity_provider is still a valid request type")
		}
	}
}
