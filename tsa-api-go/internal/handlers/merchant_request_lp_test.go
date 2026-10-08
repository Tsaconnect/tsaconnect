package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func validLPSubmitInput() map[string]interface{} {
	return map[string]interface{}{
		"businessType": "liquidity_provider", "businessName": "Typed Name",
		"address": "1 Marina", "city": "Lagos", "state": "Lagos", "country": "Nigeria", "phone": "+2348000000000",
		"settlementBankCode": "058", "settlementAccountNumber": "0123456789", "settlementAccountName": "ADA OBI",
		"makerWalletAddress": "0x52908400098527886E0F7030069857D2E4169EE7", "requestedPerTradeCapUsd": 500,
	}
}

func verifyUser(t *testing.T, db *gorm.DB, u *models.User, legal string) {
	t.Helper()
	if err := db.Model(&models.User{}).Where("id = ?", u.ID).
		Updates(map[string]any{"verification_status": models.VerificationStatusVerified, "verified_legal_name": legal}).Error; err != nil {
		t.Fatal(err)
	}
}

// lpHandler: a merchant-request handler whose onboarding knows pool chain 97.
func lpHandler(t *testing.T, db *gorm.DB, poolOn bool) *MerchantRequestHandler {
	svc := services.NewPMOnboardingService(db, nil)
	if poolOn {
		svc.SetLiquidity(97, nil, nil)
	}
	h := NewMerchantRequestHandlerWithPM(db, newTestEventBus(t), svc)
	h.PMService = services.NewPMService(db)
	return h
}

func submitLP(t *testing.T, h *MerchantRequestHandler, u *models.User) (int, map[string]any) {
	w, c := newMerchantContext("POST", "/api/merchant-requests", validLPSubmitInput(), u)
	h.SubmitMerchantRequest(c)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

func TestSubmitLPRequest_RequiresVerifiedKYC(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "user")
	if code, body := submitLP(t, h, u); code != http.StatusForbidden || body["code"] != services.KYCVerificationRequiredCode {
		t.Fatalf("unverified = %d %v", code, body)
	}
}

func TestSubmitLPRequest_UsesTheVerifiedLegalNameAndZeroCaps(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "merchant") // a marketplace merchant may also apply
	verifyUser(t, db, u, "ADA OBI")
	if code, body := submitLP(t, h, u); code != http.StatusCreated {
		t.Fatalf("submit = %d %v", code, body)
	}
	var mr models.MerchantRequest
	db.Where("user_id = ?", u.ID).First(&mr)
	if mr.BusinessName != "ADA OBI" || mr.RequestedPerTradeCapUSD != 0 || mr.RequestedDailyCapUSD != 0 {
		t.Fatalf("request = %+v", mr)
	}
}

// Roadmap risk 4.
func TestSubmitLPRequest_RefusedWhenUserHasAPMRow(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "user")
	verifyUser(t, db, u, "ADA OBI")
	db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: u.ID, Name: "Ada PM", Status: models.PaymentMerchantStatusApproved})
	if code, _ := submitLP(t, h, u); code != http.StatusBadRequest {
		t.Fatalf("submit with a merchant row = %d, want 400", code)
	}
}

func TestSubmitPMRequest_RefusedForAnLP(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "user")
	db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: u.ID, Name: "Ada LP", Kind: models.PaymentMerchantKindLP, Status: models.PaymentMerchantStatusApproved})
	w, c := newMerchantContext("POST", "/api/merchant-requests", validPMSubmitInput(), u)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PM submit by an LP = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestSubmitLPRequest_DormantPoolIs503(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, false)
	u := createMerchantTestUser(t, db, "user")
	verifyUser(t, db, u, "ADA OBI")
	if code, body := submitLP(t, h, u); code != http.StatusServiceUnavailable || body["code"] != "liquidity_unavailable" {
		t.Fatalf("dormant pool = %d %v", code, body)
	}
}

func approveAs(t *testing.T, h *MerchantRequestHandler, caller *models.User, reqID string) int {
	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/approve", map[string]any{"note": "ok"}, caller)
	c.Params = gin.Params{{Key: "id", Value: reqID}}
	h.ApproveMerchantRequest(c)
	return w.Code
}

func lpRequestID(t *testing.T, db *gorm.DB, h *MerchantRequestHandler, u *models.User) string {
	verifyUser(t, db, u, "ADA OBI")
	if code, body := submitLP(t, h, u); code != http.StatusCreated {
		t.Fatalf("submit = %d %v", code, body)
	}
	var mr models.MerchantRequest
	db.Where("user_id = ?", u.ID).First(&mr)
	return mr.ID.String()
}

// Roadmap risk 5 (S35).
func TestApproveLPRequest_NeedsP2PAdmin(t *testing.T) {
	for _, role := range []string{"support", "merchant"} {
		t.Run(role, func(t *testing.T) {
			db := setupMerchantTestDB(t)
			h := lpHandler(t, db, true)
			id := lpRequestID(t, db, h, createMerchantTestUser(t, db, "user"))
			if code := approveAs(t, h, createMerchantTestUser(t, db, role), id); code != http.StatusForbidden {
				t.Fatalf("%s approve = %d, want 403", role, code)
			}
			var mr models.MerchantRequest
			db.First(&mr, "id = ?", id)
			var n int64
			db.Model(&models.PaymentMerchant{}).Count(&n)
			if mr.Status != models.MerchantRequestStatusPending || n != 0 {
				t.Fatalf("status=%s rows=%d: a refused approval changed state", mr.Status, n)
			}
		})
	}
}

func TestApproveLPRequest_CreatesLPRowOnPoolChainWithoutRoleFlip(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "user")
	id := lpRequestID(t, db, h, u)
	if code := approveAs(t, h, createMerchantTestUser(t, db, "admin"), id); code != http.StatusOK {
		t.Fatalf("approve = %d", code)
	}
	var pm models.PaymentMerchant
	db.Where("user_id = ?", u.ID).First(&pm)
	if !pm.IsLP() || pm.PoolChainID == nil || *pm.PoolChainID != 97 || pm.Status != models.PaymentMerchantStatusApproved {
		t.Fatalf("pm = %+v", pm)
	}
	var reloaded models.User
	db.First(&reloaded, "id = ?", u.ID)
	if reloaded.Role != "user" {
		t.Fatalf("role = %q: an LP never gains the merchant (console) role", reloaded.Role)
	}
}

func TestApproveLPRequest_KindConflictLeavesRequestPending(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "user")
	id := lpRequestID(t, db, h, u)
	// A merchant row appeared after the submit (e.g. an admin-created PM).
	db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: u.ID, Name: "Ada PM", Status: models.PaymentMerchantStatusApproved})
	if code := approveAs(t, h, createMerchantTestUser(t, db, "admin"), id); code != http.StatusConflict {
		t.Fatalf("approve = %d, want 409", code)
	}
	var mr models.MerchantRequest
	db.First(&mr, "id = ?", id)
	if mr.Status != models.MerchantRequestStatusPending {
		t.Fatalf("status = %s, want pending (nothing half-approved)", mr.Status)
	}
}

// Review I2: a REJECTED merchant row still occupies the user's slot; the LP
// approval must refuse before flipping anything.
func TestApproveLPRequest_RejectedMerchantRowNeverHalfApproves(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "user")
	id := lpRequestID(t, db, h, u)
	db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: u.ID, Name: "Ada PM", Status: models.PaymentMerchantStatusRejected})
	if code := approveAs(t, h, createMerchantTestUser(t, db, "admin"), id); code != http.StatusConflict {
		t.Fatalf("approve = %d, want 409", code)
	}
	var mr models.MerchantRequest
	db.First(&mr, "id = ?", id)
	var reloaded models.User
	db.First(&reloaded, "id = ?", u.ID)
	if mr.Status != models.MerchantRequestStatusPending || reloaded.Role != "user" {
		t.Fatalf("request=%s role=%s: half-approved", mr.Status, reloaded.Role)
	}
	// The mirror case: a rejected LP row blocks a p2p_merchant approval before the role flip.
	u2 := createMerchantTestUser(t, db, "user")
	w, c := newMerchantContext("POST", "/api/merchant-requests", validPMSubmitInput(), u2)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}
	db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: u2.ID, Name: "Ada LP", Kind: models.PaymentMerchantKindLP, Status: models.PaymentMerchantStatusRejected})
	var pmReq models.MerchantRequest
	db.Where("user_id = ?", u2.ID).First(&pmReq)
	if code := approveAs(t, h, createMerchantTestUser(t, db, "admin"), pmReq.ID.String()); code != http.StatusConflict {
		t.Fatalf("PM approve over a rejected LP row = %d, want 409", code)
	}
	// A fresh struct: re-reading into reloaded would add its old primary key
	// to the WHERE, miss, and leave u's role in place.
	var reloaded2 models.User
	if err := db.First(&reloaded2, "id = ?", u2.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded2.Role != "user" {
		t.Fatalf("role flipped to %s before the conflict", reloaded2.Role)
	}
}

// Review M7: one wallet, one profile.
func TestApproveLPRequest_WalletAlreadyRegisteredRefused(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	id := lpRequestID(t, db, h, createMerchantTestUser(t, db, "user"))
	db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: uuid.New(), Name: "Someone", Status: models.PaymentMerchantStatusActive,
		OperatorWalletAddress: "0x52908400098527886e0f7030069857d2e4169ee7"})
	if code := approveAs(t, h, createMerchantTestUser(t, db, "admin"), id); code != http.StatusConflict {
		t.Fatalf("approve with a taken wallet = %d, want 409", code)
	}
}

func TestListMerchantRequests_FiltersByBusinessType(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	lpRequestID(t, db, h, createMerchantTestUser(t, db, "user"))
	w0, c0 := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), createMerchantTestUser(t, db, "user"))
	h.SubmitMerchantRequest(c0)
	if w0.Code != http.StatusCreated {
		t.Fatal(w0.Body.String())
	}
	w, c := newMerchantContext("GET", "/api/admin/merchant-requests?businessType=liquidity_provider", nil, createMerchantTestUser(t, db, "admin"))
	h.ListMerchantRequests(c)
	var resp struct {
		Data struct {
			Requests []models.MerchantRequest `json:"requests"`
			Total    int64                    `json:"total"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Total != 1 || resp.Data.Requests[0].BusinessType != models.BusinessTypeLiquidityProvider {
		t.Fatalf("filtered list = %+v", resp.Data)
	}
	w, c = newMerchantContext("GET", "/api/admin/merchant-requests?businessType=nope", nil, createMerchantTestUser(t, db, "admin"))
	h.ListMerchantRequests(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown type = %d, want 400", w.Code)
	}
}

// O-B5: a pool chain unconfigured after the submit refuses the approval
// before any write — the request stays pending and no row appears.
func TestApproveLPRequest_DormantPoolIs503(t *testing.T) {
	db := setupMerchantTestDB(t)
	id := lpRequestID(t, db, lpHandler(t, db, true), createMerchantTestUser(t, db, "user"))
	if code := approveAs(t, lpHandler(t, db, false), createMerchantTestUser(t, db, "admin"), id); code != http.StatusServiceUnavailable {
		t.Fatalf("approve on a dormant pool = %d, want 503", code)
	}
	var mr models.MerchantRequest
	db.First(&mr, "id = ?", id)
	var n int64
	db.Model(&models.PaymentMerchant{}).Count(&n)
	if mr.Status != models.MerchantRequestStatusPending || n != 0 {
		t.Fatalf("status=%s rows=%d: a refused approval changed state", mr.Status, n)
	}
}
