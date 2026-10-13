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

// Owner 2026-10-13: a merchant may also apply as an LP; only an LP row that is
// not rejected blocks an LP application.
func TestSubmitLPRequest_AllowedForAMerchant(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "merchant")
	verifyUser(t, db, u, "ADA OBI")
	db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: u.ID, Name: "Ada PM", Status: models.PaymentMerchantStatusActive})
	if code, body := submitLP(t, h, u); code != http.StatusCreated {
		t.Fatalf("submit with an active merchant row = %d %v, want 201", code, body)
	}

	lpUser := createMerchantTestUser(t, db, "user")
	verifyUser(t, db, lpUser, "ADA OBI")
	db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: lpUser.ID, Name: "ADA OBI", Kind: models.PaymentMerchantKindLP, Status: models.PaymentMerchantStatusApproved})
	if code, _ := submitLP(t, h, lpUser); code != http.StatusBadRequest {
		t.Fatalf("submit with an approved LP row = %d, want 400", code)
	}
}

// Inventory #9: an active LP is not "already a payment merchant".
func TestSubmitPMRequest_AllowedForAnActiveLP(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "user")
	db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: u.ID, Name: "Ada LP", Kind: models.PaymentMerchantKindLP, Status: models.PaymentMerchantStatusActive})
	w, c := newMerchantContext("POST", "/api/merchant-requests", validPMSubmitInput(), u)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("PM submit by an active LP = %d, want 201: %s", w.Code, w.Body.String())
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

func TestApproveLPRequest_ForAMerchantKeepsMerchantRowAndRole(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "merchant")
	m := models.PaymentMerchant{ID: uuid.New(), UserID: u.ID, Name: "Ada PM", Status: models.PaymentMerchantStatusActive}
	db.Create(&m)
	id := lpRequestID(t, db, h, u)
	if code := approveAs(t, h, createMerchantTestUser(t, db, "admin"), id); code != http.StatusOK {
		t.Fatalf("approve = %d", code)
	}
	var rows []models.PaymentMerchant
	db.Where("user_id = ?", u.ID).Order("kind").Find(&rows)
	var role string
	db.Model(&models.User{}).Where("id = ?", u.ID).Pluck("role", &role)
	if len(rows) != 2 || rows[0].IsLP() == rows[1].IsLP() || role != models.RoleMerchant {
		t.Fatalf("rows=%+v role=%s", rows, role)
	}
	var after models.PaymentMerchant
	db.First(&after, "id = ?", m.ID)
	if after.Status != models.PaymentMerchantStatusActive {
		t.Fatalf("merchant status = %s", after.Status)
	}
}

func TestApprovePMRequest_ForAnLPLeavesTheLPRowAlone(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "user")
	lp := models.PaymentMerchant{ID: uuid.New(), UserID: u.ID, Name: "ADA OBI", Kind: models.PaymentMerchantKindLP,
		Status: models.PaymentMerchantStatusPaused, ProvisioningStatus: models.PMProvisioningActive}
	db.Create(&lp)
	w, c := newMerchantContext("POST", "/api/merchant-requests", validPMSubmitInput(), u)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}
	var pmReq models.MerchantRequest
	db.Where("user_id = ? AND business_type = ?", u.ID, models.BusinessTypeP2PMerchant).First(&pmReq)
	if code := approveAs(t, h, createMerchantTestUser(t, db, "admin"), pmReq.ID.String()); code != http.StatusOK {
		t.Fatalf("approve = %d", code)
	}
	var after models.PaymentMerchant
	db.First(&after, "id = ?", lp.ID)
	if after.Status != models.PaymentMerchantStatusPaused || !after.IsLP() {
		t.Fatalf("LP row changed by the merchant approval: %+v", after)
	}
	// Re-approving the merchant request (isPMRetry) finds the merchant row, not the live LP row.
	if code := approveAs(t, h, createMerchantTestUser(t, db, "admin"), pmReq.ID.String()); code != http.StatusOK {
		t.Fatalf("retry approve = %d", code)
	}
}

func TestGetMyMerchantRequest_FiltersByType(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "user")
	seedMerchantRequest(t, db, u.ID, models.BusinessTypeLiquidityProvider, models.MerchantRequestStatusPending)
	seedMerchantRequest(t, db, u.ID, models.BusinessTypeP2PMerchant, models.MerchantRequestStatusApproved) // newer
	db.Exec(`UPDATE merchant_requests SET created_at = datetime('now', '+1 minute') WHERE user_id = ? AND business_type = ?`,
		u.ID.String(), models.BusinessTypeP2PMerchant)
	w, c := newMerchantContext("GET", "/api/merchant-requests/my-request?type=liquidity_provider", nil, u)
	h.GetMyMerchantRequest(c)
	var body struct{ Data models.MerchantRequest }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusOK || body.Data.BusinessType != models.BusinessTypeLiquidityProvider {
		t.Fatalf("my-request?type=lp = %d %s", w.Code, w.Body.String())
	}
	w, c = newMerchantContext("GET", "/api/merchant-requests/my-request?type=bogus", nil, u)
	h.GetMyMerchantRequest(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bogus type = %d", w.Code)
	}
}

// Between the deploy and the one-off the old (user_id) index still stands: a
// second-role approval fails whole with its own code, never half-applied.
// (The request predates the merchant row here; a submit while both stand is
// refused up front, below.)
func TestApproveLPRequest_BeforeOneOffIs409SecondRoleNotEnabled(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "merchant")
	id := lpRequestID(t, db, h, u)
	if err := db.Exec(`CREATE UNIQUE INDEX uq_payment_merchants_user ON payment_merchants (user_id)`).Error; err != nil {
		t.Fatal(err)
	}
	db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: u.ID, Name: "Ada PM", Status: models.PaymentMerchantStatusActive})
	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+id+"/approve", map[string]any{"note": "ok"}, createMerchantTestUser(t, db, "admin"))
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.ApproveMerchantRequest(c)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusConflict || body["code"] != "second_role_not_enabled" {
		t.Fatalf("approve = %d %v, want 409 second_role_not_enabled", w.Code, body)
	}
	var mr models.MerchantRequest
	db.First(&mr, "id = ?", id)
	var n int64
	db.Model(&models.PaymentMerchant{}).Where("user_id = ?", u.ID).Count(&n)
	if mr.Status != models.MerchantRequestStatusPending || n != 1 {
		t.Fatalf("request=%s rows=%d: half-applied", mr.Status, n)
	}
}

// Before the one-off, a merchant's LP application could never be approved, so
// it is refused at submit instead of sitting pending; a single-role user is
// unaffected.
func TestSubmitLPRequest_BeforeOneOffRefusesASecondRole(t *testing.T) {
	db := setupMerchantTestDB(t)
	if err := db.Exec(`CREATE UNIQUE INDEX uq_payment_merchants_user ON payment_merchants (user_id)`).Error; err != nil {
		t.Fatal(err)
	}
	h := lpHandler(t, db, true)
	pm := createMerchantTestUser(t, db, "merchant")
	db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: pm.ID, Name: "Ada PM", Status: models.PaymentMerchantStatusActive})
	verifyUser(t, db, pm, "ADA OBI")
	if code, body := submitLP(t, h, pm); code != http.StatusConflict || body["code"] != "second_role_not_enabled" {
		t.Fatalf("merchant submit = %d %v, want 409 second_role_not_enabled", code, body)
	}
	lpRequestID(t, db, h, createMerchantTestUser(t, db, "user")) // a single-role user is unaffected
	if err := db.Exec(`DROP INDEX uq_payment_merchants_user`).Error; err != nil {
		t.Fatal(err)
	}
	if code, body := submitLP(t, h, pm); code != http.StatusCreated {
		t.Fatalf("merchant submit after the one-off = %d %v, want 201", code, body)
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

// Security review of 6b250c31: deciding an LP application is P2P-admin work
// (S35) whichever way it goes. AdminAuth alone admits support and every
// marketplace merchant, who must not be able to turn LP applicants away.
func TestRejectLPRequest_NeedsP2PAdmin(t *testing.T) {
	for _, role := range []string{"support", "merchant", "admin"} {
		t.Run(role, func(t *testing.T) {
			db := setupMerchantTestDB(t)
			h := lpHandler(t, db, true)
			id := lpRequestID(t, db, h, createMerchantTestUser(t, db, "user"))
			w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+id+"/reject", map[string]any{"note": "no"}, createMerchantTestUser(t, db, role))
			c.Params = gin.Params{{Key: "id", Value: id}}
			h.RejectMerchantRequest(c)
			var mr models.MerchantRequest
			db.First(&mr, "id = ?", id)
			want, wantStatus := http.StatusForbidden, models.MerchantRequestStatusPending
			if role == "admin" {
				want, wantStatus = http.StatusOK, models.MerchantRequestStatusRejected
			}
			if w.Code != want || mr.Status != wantStatus {
				t.Fatalf("%s reject = %d (request %s), want %d (%s)", role, w.Code, mr.Status, want, wantStatus)
			}
		})
	}
}

// Review M5: the company PM's user cannot apply as, or be approved as, an LP.
func TestLPRequest_CompanyUserRefused(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := lpHandler(t, db, true)
	u := createMerchantTestUser(t, db, "merchant")
	verifyUser(t, db, u, "ADA OBI")
	company := &models.PaymentMerchant{ID: uuid.New(), UserID: u.ID, Name: "TSA", Status: models.PaymentMerchantStatusActive}
	db.Create(company)
	h.PMOnboarding.SetCompanyPMID(company.ID.String())

	if code, body := submitLP(t, h, u); code != http.StatusForbidden || body["code"] != "company_account_not_eligible" {
		t.Fatalf("submit = %d %v, want 403 company_account_not_eligible", code, body)
	}

	// A request that predates IT_PM_ID naming this user is refused at approval.
	id := pgRequest(t, db, u.ID, models.BusinessTypeLiquidityProvider)
	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+id+"/approve", map[string]any{"note": "ok"}, createMerchantTestUser(t, db, "admin"))
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.ApproveMerchantRequest(c)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusConflict || body["code"] != "company_account_not_eligible" {
		t.Fatalf("approve = %d %v, want 409 company_account_not_eligible", w.Code, body)
	}
	var mr models.MerchantRequest
	db.First(&mr, "id = ?", id)
	var n int64
	db.Model(&models.PaymentMerchant{}).Where("user_id = ?", u.ID).Count(&n)
	if mr.Status != models.MerchantRequestStatusPending || n != 1 {
		t.Fatalf("request=%s rows=%d: half-applied", mr.Status, n)
	}
}
