package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakeLPActivator struct {
	out   *models.PaymentMerchant
	err   error
	calls int
}

func (f *fakeLPActivator) ActivateLP(_ context.Context, _, _ uuid.UUID) (*models.PaymentMerchant, error) {
	f.calls++
	return f.out, f.err
}
func (f *fakeLPActivator) LiquidityChainID() (int64, bool) { return 97, true }

func approvedLPRow(userID uuid.UUID) *models.PaymentMerchant {
	chain := int64(97)
	return &models.PaymentMerchant{ID: uuid.New(), UserID: userID, Kind: models.PaymentMerchantKindLP, PoolChainID: &chain, Status: models.PaymentMerchantStatusApproved}
}

// An LP never sends a BVN/NIN, even while merchants still activate with one.
func TestPMHandler_ActivateLP_EmptyBodyNoIDEvenInVAMode(t *testing.T) {
	user := testRegularUser()
	merchantAct := &fakePMActivator{out: &models.PaymentMerchant{}} // NeedsIdentity() = true
	lpAct := &fakeLPActivator{out: &models.PaymentMerchant{ID: uuid.New(), Status: models.PaymentMerchantStatusActive, Kind: models.PaymentMerchantKindLP}}
	h := &PMHandler{auto: &fakePMAutomation{}}
	h.pmStatus = &fakePMStatusGetter{pm: approvedLPRow(user.ID)}
	h.SetActivator(merchantAct)
	h.SetLPActivator(lpAct)

	w := activatePost(buildActivateRouter(h, user), ``)
	if w.Code != http.StatusOK || lpAct.calls != 1 || merchantAct.calls != 0 {
		t.Fatalf("code=%d lp=%d merchant=%d: %s", w.Code, lpAct.calls, merchantAct.calls, w.Body.String())
	}
}

func TestPMHandler_ActivateLP_MismatchIs409WithCode(t *testing.T) {
	user := testRegularUser()
	h := &PMHandler{auto: &fakePMAutomation{}}
	h.pmStatus = &fakePMStatusGetter{pm: approvedLPRow(user.ID)}
	h.SetLPActivator(&fakeLPActivator{err: services.ErrSettlementNameMismatch})
	w := activatePost(buildActivateRouter(h, user), `{}`)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusConflict || body["code"] != "settlement_name_mismatch" {
		t.Fatalf("code=%d body=%v", w.Code, body)
	}
}

func TestPMHandler_ActivateLP_ErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		err      error
		status   int
		code     string
		noLPWire bool
	}{
		{err: services.ErrSettlementAccountUnresolved, status: http.StatusBadGateway, code: "settlement_account_unresolved"},
		{err: services.ErrLiquidityUnavailable, status: http.StatusServiceUnavailable, code: "liquidity_unavailable"},
		{err: services.ErrKYCVerificationRequired, status: http.StatusForbidden, code: services.KYCVerificationRequiredCode},
		{noLPWire: true, status: http.StatusServiceUnavailable, code: "liquidity_unavailable"},
	} {
		user := testRegularUser()
		lpAct := &fakeLPActivator{err: tc.err}
		merchantAct := &fakePMActivator{out: &models.PaymentMerchant{}, noIdentity: true}
		h := &PMHandler{auto: &fakePMAutomation{}}
		h.pmStatus = &fakePMStatusGetter{pm: approvedLPRow(user.ID)}
		h.SetActivator(merchantAct)
		if !tc.noLPWire {
			h.SetLPActivator(lpAct)
		}
		w := activatePost(buildActivateRouter(h, user), `{}`)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != tc.status || body["code"] != tc.code || merchantAct.calls != 0 {
			t.Fatalf("err=%v: code=%d body=%v merchant=%d", tc.err, w.Code, body, merchantAct.calls)
		}
	}
}

func TestGetMyPMStatus_ReportsKindAndPoolChain(t *testing.T) {
	user := testRegularUser()
	lp := approvedLPRow(user.ID)
	lp.Status = models.PaymentMerchantStatusActive
	h := &PMHandler{auto: &fakePMAutomation{}}
	h.pmStatus = &fakePMStatusGetter{pm: lp}
	h.SetLPActivator(&fakeLPActivator{})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/p2p/pm/me", nil)
	buildPMStatusRouter(h, user).ServeHTTP(w, req)
	var resp struct {
		Data struct {
			IsPm                           bool   `json:"isPm"`
			Status                         string `json:"status"`
			Kind                           string `json:"kind"`
			PoolChainID                    *int64 `json:"poolChainId"`
			ActivationNeedsID              bool   `json:"activationNeedsId"`
			ActivationNeedsSettlementMatch bool   `json:"activationNeedsSettlementMatch"`
			LiquidityAvailable             bool   `json:"liquidityAvailable"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	d := resp.Data
	if d.IsPm || d.Status != "active" || d.Kind != "lp" || d.PoolChainID == nil || *d.PoolChainID != 97 ||
		d.ActivationNeedsID || !d.ActivationNeedsSettlementMatch || !d.LiquidityAvailable {
		t.Fatalf("status = %+v (an active LP is not a PM; it needs no BVN)", d)
	}
}

// O-B5: with no LP activator wired (no pool chain), the app shows no LP CTA.
func TestGetMyPMStatus_LiquidityUnavailableWhenDormant(t *testing.T) {
	user := testRegularUser()
	for _, h := range []*PMHandler{
		{auto: &fakePMAutomation{}},
		{auto: &fakePMAutomation{}, pmStatus: &fakePMStatusGetter{err: services.ErrPMNotFound}},
	} {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/p2p/pm/me", nil)
		buildPMStatusRouter(h, user).ServeHTTP(w, req)
		var resp struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if w.Code != http.StatusOK || resp.Data["liquidityAvailable"] != false {
			t.Fatalf("code=%d data=%v", w.Code, resp.Data)
		}
	}
}

// lpActivationAccounts resolves every settlement account to one bank name.
type lpActivationAccounts struct{ name string }

func (a lpActivationAccounts) Resolve(context.Context, string, string) (services.NombaLookupResult, error) {
	return services.NombaLookupResult{AccountName: a.name}, nil
}

type lpActivationGate bool

func (g lpActivationGate) Open() bool { return bool(g) }

// Security fix of 36c4a991: once an LP is active, its old request must not be
// re-approvable. ApproveFromRequest copies the applicant-typed settlement name
// over the bank-verified one, so a retry after activation would undo S34.
func TestApproveLPRequest_ActivatedLPCannotBeReapproved(t *testing.T) {
	db := setupMerchantTestDB(t)
	svc := services.NewPMOnboardingService(db, nil)
	svc.SetLiquidity(97, lpActivationAccounts{name: "OBI ADA"}, lpActivationGate(true))
	h := NewMerchantRequestHandlerWithPM(db, newTestEventBus(t), svc)
	h.PMService = services.NewPMService(db)

	u := createMerchantTestUser(t, db, "user")
	id := lpRequestID(t, db, h, u)
	admin := createMerchantTestUser(t, db, "admin")
	if code := approveAs(t, h, admin, id); code != http.StatusOK {
		t.Fatalf("approve = %d", code)
	}
	var pm models.PaymentMerchant
	db.Where("user_id = ?", u.ID).First(&pm)
	if _, err := svc.ActivateLP(context.Background(), pm.ID, u.ID); err != nil {
		t.Fatalf("ActivateLP: %v", err)
	}

	if code := approveAs(t, h, admin, id); code == http.StatusOK {
		t.Fatalf("re-approve after activation = %d, want a refusal", code)
	}
	var raw models.PaymentMerchant
	db.First(&raw, "id = ?", pm.ID)
	if raw.SettlementAccountName != "OBI ADA" || raw.Status != models.PaymentMerchantStatusActive {
		t.Fatalf("row = %+v: the re-approval overwrote the verified settlement name", raw)
	}
}

// Defence in depth: a live row is never a retry even if its provisioning_status
// is not active (a legacy or hand-edited row).
func TestApproveLPRequest_LiveRowIsNeverARetry(t *testing.T) {
	db := setupMerchantTestDB(t)
	svc := services.NewPMOnboardingService(db, nil)
	svc.SetLiquidity(97, lpActivationAccounts{name: "OBI ADA"}, lpActivationGate(true))
	h := NewMerchantRequestHandlerWithPM(db, newTestEventBus(t), svc)
	h.PMService = services.NewPMService(db)

	u := createMerchantTestUser(t, db, "user")
	id := lpRequestID(t, db, h, u)
	admin := createMerchantTestUser(t, db, "admin")
	if code := approveAs(t, h, admin, id); code != http.StatusOK {
		t.Fatalf("approve = %d", code)
	}
	var pm models.PaymentMerchant
	db.Where("user_id = ?", u.ID).First(&pm)
	if _, err := svc.ActivateLP(context.Background(), pm.ID, u.ID); err != nil {
		t.Fatalf("ActivateLP: %v", err)
	}
	db.Model(&models.PaymentMerchant{}).Where("id = ?", pm.ID).Update("provisioning_status", models.PMProvisioningPending)

	// 400 "not pending": the retry path is never entered (the service's own
	// refusal would answer 409).
	if code := approveAs(t, h, admin, id); code != http.StatusBadRequest {
		t.Fatalf("re-approve of a live row = %d, want 400", code)
	}
	var raw models.PaymentMerchant
	db.First(&raw, "id = ?", pm.ID)
	if raw.SettlementAccountName != "OBI ADA" || raw.Status != models.PaymentMerchantStatusActive {
		t.Fatalf("row = %+v: the re-approval overwrote the verified settlement name", raw)
	}
}

// Owner 2026-10-13: the top level describes the merchant row, and the lp block
// the LP row, for a user who holds both.
func TestGetMyPMStatus_DualRoleTopLevelIsMerchantAndLPBlockIsLP(t *testing.T) {
	user := testRegularUser()
	lp := approvedLPRow(user.ID)
	merchant := &models.PaymentMerchant{ID: uuid.New(), UserID: user.ID, Kind: models.PaymentMerchantKindMerchant, Status: models.PaymentMerchantStatusActive}
	h := &PMHandler{auto: &fakePMAutomation{}}
	h.pmStatus = &fakePMStatusGetter{pm: merchant, lp: lp}
	h.SetLPActivator(&fakeLPActivator{})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/p2p/pm/me", nil)
	buildPMStatusRouter(h, user).ServeHTTP(w, req)
	var resp struct {
		Data struct {
			IsPm   bool   `json:"isPm"`
			Status string `json:"status"`
			Kind   string `json:"kind"`
			LP     *struct {
				Status      string `json:"status"`
				PoolChainID *int64 `json:"poolChainId"`
			} `json:"lp"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	d := resp.Data
	if !d.IsPm || d.Status != "active" || d.Kind != "merchant" || d.LP == nil || d.LP.Status != "approved" || d.LP.PoolChainID == nil {
		t.Fatalf("status = %+v lp = %+v", d, d.LP)
	}
}

func TestActivatePM_KindQueryPicksTheRole(t *testing.T) {
	user := testRegularUser()
	lp := approvedLPRow(user.ID)
	merchant := &models.PaymentMerchant{ID: uuid.New(), UserID: user.ID, Kind: models.PaymentMerchantKindMerchant, Status: models.PaymentMerchantStatusApproved}
	for _, tc := range []struct {
		query           string
		wantLP, wantMer int
		status          int
	}{
		{"?kind=lp", 1, 0, http.StatusOK},
		{"?kind=merchant", 0, 1, http.StatusOK},
		{"", 0, 1, http.StatusOK}, // no kind: the merchant row, as today
		{"?kind=bogus", 0, 0, http.StatusBadRequest},
	} {
		lpAct := &fakeLPActivator{out: lp}
		merAct := &fakePMActivator{out: merchant, noIdentity: true}
		h := &PMHandler{auto: &fakePMAutomation{}}
		h.pmStatus = &fakePMStatusGetter{pm: merchant, lp: lp}
		h.SetActivator(merAct)
		h.SetLPActivator(lpAct)
		w := activatePostPath(buildActivateRouter(h, user), "/p2p/pm/activate"+tc.query, `{}`)
		if w.Code != tc.status || lpAct.calls != tc.wantLP || merAct.calls != tc.wantMer {
			t.Fatalf("%q: code=%d lp=%d merchant=%d", tc.query, w.Code, lpAct.calls, merAct.calls)
		}
	}
}
