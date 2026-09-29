package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// nameChangeHandlerDB is the KYC test database plus what a name change reads:
// its own table and the (empty) money-in-flight tables.
func nameChangeHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupKYCTestDB(t, true)
	if err := db.AutoMigrate(&models.UserNameChange{}); err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE p2p_trades (id TEXT PRIMARY KEY, buyer_id TEXT, seller_id TEXT, status TEXT)`,
		`CREATE TABLE it_intents (id TEXT PRIMARY KEY, user_id TEXT, direction TEXT, status TEXT)`,
		`CREATE TABLE pay_merchants (id TEXT PRIMARY KEY, user_id TEXT)`,
		`CREATE TABLE pay_payments (id TEXT PRIMARY KEY, merchant_id TEXT, status TEXT, mode TEXT)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// freeNameChanges is the service with no fee, so the tests are about the
// verification and the account, not the money (the service tests cover that).
func freeNameChanges(db *gorm.DB) *services.NameChangeService {
	return services.NewNameChangeService(db, services.NameChangeConfig{Enabled: true, FeeUSD: "0", RequireFaceMatch: true}, nil, time.Now)
}

func verifiedUser(t *testing.T, db *gorm.DB, legal string) *models.User {
	t.Helper()
	u := createKYCTestUser(t, db, models.VerificationStatusVerified)
	u.DiditSessionID = "sess-first-kyc"
	u.VerifiedLegalName = legal
	if err := db.Save(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

// openVerifyingChange is a paid change whose session sessionID is in flight.
func openVerifyingChange(t *testing.T, db *gorm.DB, user *models.User, sessionID string) *models.UserNameChange {
	t.Helper()
	ch := &models.UserNameChange{ID: uuid.New(), UserID: user.ID, Status: models.NameChangeStatusVerifying, FeeUSD: "0.00",
		PayMethod: models.NameChangePayFree, FeeStatus: models.NameChangeFeeNone, Attempts: 1, DiditSessionID: sessionID,
		DiditSessionURL: "https://verify.didit.me/" + sessionID, OldName: user.Name, OldVerifiedName: user.VerifiedLegalName,
		OriginalDiditSessionID: user.DiditSessionID, ExpiresAt: time.Now().Add(24 * time.Hour)}
	if err := db.Create(ch).Error; err != nil {
		t.Fatal(err)
	}
	return ch
}

// identityDecision is a passed decision with the fields the same-person check
// reads: document name, date of birth, number, country, and the earlier
// sessions Didit's face search matched.
func identityDecision(fullName, dob, country string, faceMatches ...string) string {
	matches := make([]string, len(faceMatches))
	for i, s := range faceMatches {
		matches[i] = fmt.Sprintf(`{"session_id":"%s","similarity_percentage":96.4}`, s)
	}
	return fmt.Sprintf(`{
		"session_id":"s","status":"Approved",
		"id_verifications":[{"status":"Approved","full_name":"%s","date_of_birth":"%s","document_number":"X1","issuing_state":"%s"}],
		"liveness_checks":[{"status":"Approved","matches":[%s]}]
	}`, fullName, dob, country, strings.Join(matches, ","))
}

func nameChangeWebhookBody(sessionID, status, vendorData string) []byte {
	return []byte(fmt.Sprintf(
		`{"event_id":"","webhook_type":"status.updated","timestamp":%d,"session_id":"%s","status":"%s","vendor_data":"%s"}`,
		time.Now().Unix(), sessionID, status, vendorData))
}

func dataUpdatedBody(user *models.User, sessionID string) []byte {
	return []byte(fmt.Sprintf(
		`{"event_id":"","webhook_type":"data.updated","timestamp":%d,"session_id":"%s","status":"Approved","vendor_data":"%s"}`,
		time.Now().Unix(), sessionID, user.ID))
}

// A re-verification's approval goes to its change — the same-person gate — and
// never through first-time KYC's approval, which would write the new
// document's name straight onto the account.
func TestKYCWebhook_NameChangeApprovalGoesThroughTheChange(t *testing.T) {
	db := nameChangeHandlerDB(t)
	user := verifiedUser(t, db, "ADA CHIOMA OBI")
	ch := openVerifyingChange(t, db, user, "sess-nc")
	didit := diditDecisionStub(t, map[string]string{
		"sess-nc":        identityDecision("ADA OBI", "1990-05-17", "NGA", "sess-first-kyc"),
		"sess-first-kyc": identityDecision("ADA CHIOMA OBI", "1990-05-17", "NGA"),
	})
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: secret, DiditBaseURL: didit.URL},
		NameChanges: freeNameChanges(db)}

	body := nameChangeWebhookBody("sess-nc", "Approved", nameChangeVendorPrefix+ch.ID.String())
	if w := postKYCWebhook(h, body, signedHeaders(body, secret)); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got := reloadUser(t, db, user.ID)
	if got.Name != "ADA OBI" || got.VerifiedLegalName != "ADA OBI" || got.VerificationStatus != models.VerificationStatusVerified ||
		got.DiditSessionID != "sess-first-kyc" {
		t.Fatalf("user %+v", got)
	}
	var stored models.UserNameChange
	db.First(&stored, "id = ?", ch.ID)
	if stored.Status != models.NameChangeStatusApproved || stored.DOBMatch == nil || !*stored.DOBMatch || stored.FaceMatch == nil || !*stored.FaceMatch {
		t.Fatalf("change %+v", stored)
	}

	// R3: a reviewer's later edit of the ORIGINAL session must not undo it.
	body = dataUpdatedBody(user, "sess-first-kyc")
	if w := postKYCWebhook(h, body, signedHeaders(body, secret)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Name kept") {
		t.Fatalf("data.updated on the original session: %d %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID); got.VerifiedLegalName != "ADA OBI" {
		t.Fatalf("the original session's name overwrote the changed one: %q", got.VerifiedLegalName)
	}
	// Nor a redelivered first-time approval of it.
	body = webhookBodyForSession(user, "Approved", "sess-first-kyc")
	if w := postKYCWebhook(h, body, signedHeaders(body, secret)); w.Code != http.StatusOK {
		t.Fatalf("redelivered approval: %d %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID); got.VerifiedLegalName != "ADA OBI" {
		t.Fatalf("a redelivered original approval overwrote the changed name: %q", got.VerifiedLegalName)
	}
}

// A new document whose date of birth differs from the original is held — the
// account keeps its name, whatever the names say.
func TestKYCWebhook_NameChangeWithAnotherDateOfBirthIsHeld(t *testing.T) {
	db := nameChangeHandlerDB(t)
	user := verifiedUser(t, db, "ADA CHIOMA OBI")
	ch := openVerifyingChange(t, db, user, "sess-nc")
	didit := diditDecisionStub(t, map[string]string{
		"sess-nc":        identityDecision("ADA OBI", "1985-01-01", "NGA", "sess-first-kyc"),
		"sess-first-kyc": identityDecision("ADA CHIOMA OBI", "1990-05-17", "NGA"),
	})
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: secret, DiditBaseURL: didit.URL},
		NameChanges: freeNameChanges(db)}
	body := nameChangeWebhookBody("sess-nc", "Approved", nameChangeVendorPrefix+ch.ID.String())
	if w := postKYCWebhook(h, body, signedHeaders(body, secret)); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID); got.VerifiedLegalName != "ADA CHIOMA OBI" || got.Name != user.Name {
		t.Fatalf("a held name reached the account: %+v", got)
	}
	var stored models.UserNameChange
	db.First(&stored, "id = ?", ch.ID)
	if stored.Status != models.NameChangeStatusHeldForReview || stored.DOBMatch == nil || *stored.DOBMatch ||
		!strings.Contains(stored.HoldReason, "date of birth") || stored.OriginalDOB != "1990-05-17" {
		t.Fatalf("change %+v", stored)
	}
}

// An event marked as a name change whose session is no change's current one is
// acknowledged and dropped — not looked up as a user (which would 404 forever),
// and never applied to anyone.
func TestKYCWebhook_NameChangeEventForNoCurrentSessionIsIgnored(t *testing.T) {
	db := nameChangeHandlerDB(t)
	user := verifiedUser(t, db, "ADA CHIOMA OBI")
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: secret, DiditBaseURL: "http://unused"},
		NameChanges: freeNameChanges(db)}

	body := nameChangeWebhookBody("sess-unknown", "Approved", nameChangeVendorPrefix+uuid.New().String())
	w := postKYCWebhook(h, body, signedHeaders(body, secret))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Ignored") {
		t.Fatalf("expected an ignored 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID); got.VerifiedLegalName != "ADA CHIOMA OBI" {
		t.Fatalf("user %+v", got)
	}
}

// R6: with name changes wired, first-time KYC's own Approved and Declined events
// still reach the user exactly as before.
func TestKYCWebhook_FirstTimeKYCUnchangedWithNameChangesWired(t *testing.T) {
	db := nameChangeHandlerDB(t)
	didit := diditDecisionStub(t, map[string]string{"sess-1": approvedDecision("ADEBAYO OKONKWO")})
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: secret, DiditBaseURL: didit.URL},
		NameChanges: freeNameChanges(db)}

	approved := createKYCTestUser(t, db, models.VerificationStatusInReview)
	body := webhookBodyFor(approved, "Approved")
	if w := postKYCWebhook(h, body, signedHeaders(body, secret)); w.Code != http.StatusOK {
		t.Fatalf("approved: %d %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, approved.ID); got.VerificationStatus != models.VerificationStatusVerified || got.VerifiedLegalName != "ADEBAYO OKONKWO" {
		t.Fatalf("approved user %+v", got)
	}

	// Its own session: sharing "sess-1" with the approved user would let the
	// webhook's session lookup resolve either user, whichever row came first.
	declined := createKYCTestUser(t, db, models.VerificationStatusInReview)
	declined.DiditSessionID = "sess-2"
	db.Save(declined)
	body = webhookBodyForSession(declined, "Declined", "sess-2")
	if w := postKYCWebhook(h, body, signedHeaders(body, secret)); w.Code != http.StatusOK {
		t.Fatalf("declined: %d %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, declined.ID); got.VerificationStatus != models.VerificationStatusRejected {
		t.Fatalf("declined user %+v", got)
	}
}

// start-verification buys a session marked as the change's, with a callback
// the app can tell apart; first-time KYC keeps refusing a verified user.
func TestStartNameChangeVerification_BuysASessionForTheChange(t *testing.T) {
	db := nameChangeHandlerDB(t)
	user := verifiedUser(t, db, "ADA CHIOMA OBI")
	var vendorData, callback string
	didit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		vendorData, callback = body["vendor_data"], body["callback"]
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"session_id":"sess-nc","url":"https://verify.didit.me/sess-nc"}`)
	}))
	defer didit.Close()
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWorkflowID: "wf", DiditBaseURL: didit.URL,
		DiditCallbackURL: "tsaconnect://kyc/callback"}, NameChanges: freeNameChanges(db)}

	if w := createKYCSessionFor(h, user); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Already verified") {
		t.Fatalf("first-time KYC must still refuse a verified user: %d %s", w.Code, w.Body.String())
	}

	created, err := h.NameChanges.Create(t.Context(), user.ID, services.NameChangeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	c.Request, _ = http.NewRequest("POST", "/api/users/me/name-change/start-verification", nil)
	h.StartNameChangeVerification(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"url":"https://verify.didit.me/sess-nc"`) {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if vendorData != nameChangeVendorPrefix+created.Change.ID.String() || callback != "tsaconnect://kyc/callback?flow=name_change" {
		t.Fatalf("vendor_data = %q callback = %q", vendorData, callback)
	}
	var stored models.UserNameChange
	db.First(&stored, "id = ?", created.Change.ID)
	if stored.Status != models.NameChangeStatusVerifying || stored.Attempts != 1 || stored.DiditSessionID != "sess-nc" {
		t.Fatalf("change %+v", stored)
	}
	if got := reloadUser(t, db, user.ID); got.DiditSessionID != "sess-first-kyc" || got.VerificationStatus != models.VerificationStatusVerified {
		t.Fatalf("buying a re-verification session must not touch the account's KYC state: %+v", got)
	}
}

// Opening a change asks for the account password again, counted toward the lockout.
func TestCreateNameChange_AsksForThePassword(t *testing.T) {
	db := nameChangeHandlerDB(t)
	user := verifiedUser(t, db, "ADA CHIOMA OBI")
	hashed, _ := models.HashPassword("correct-horse")
	db.Model(user).Update("password", hashed)
	user.Password = hashed
	h := &Handlers{Config: &config.Config{}, NameChanges: freeNameChanges(db)}

	post := func(password string) *httptest.ResponseRecorder {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("user", user)
		body, _ := json.Marshal(map[string]string{"password": password})
		c.Request, _ = http.NewRequest("POST", "/api/users/me/name-change", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.CreateNameChange(c)
		return w
	}
	if w := post("wrong"); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"password_incorrect"`) {
		t.Fatalf("a wrong password: %d %s", w.Code, w.Body.String())
	}
	var n int64
	db.Model(&models.UserNameChange{}).Count(&n)
	if n != 0 {
		t.Fatal("a wrong password opened a change")
	}
	if w := post("correct-horse"); w.Code != http.StatusCreated {
		t.Fatalf("the right password: %d %s", w.Code, w.Body.String())
	}
}

func putProfileName(h *Handlers, user *models.User, name string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	body, _ := json.Marshal(map[string]string{"name": name})
	c.Request, _ = http.NewRequest("PUT", "/api/users/profile", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpdateProfile(c)
	return w
}

func TestUpdateProfile_VerifiedNameEditPointsAtNameChange(t *testing.T) {
	db := nameChangeHandlerDB(t)
	h := &Handlers{Config: &config.Config{}}

	verified := verifiedUser(t, db, "ADA CHIOMA OBI")
	w := putProfileName(h, verified, "Someone Else")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"use_name_change"`) {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, verified.ID); got.Name != verified.Name {
		t.Fatalf("a verified name was edited: %q", got.Name)
	}

	unverified := createKYCTestUser(t, db, models.VerificationStatusPending)
	if w := putProfileName(h, unverified, "New Name"); w.Code != http.StatusOK {
		t.Fatalf("an unverified user's edit: %d %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, unverified.ID); got.Name != "New Name" {
		t.Fatalf("name = %q", got.Name)
	}
}

// The owner's rename request, end to end over HTTP: a guarded change
// (password, then the emailed code), refused from a stale screen, and — once
// confirmed — only pending: the business keeps its name until an admin
// approves it.
func TestPayOwner_BusinessNameRenameRequest(t *testing.T) {
	o := newOwnerRig(t)
	stale := o.do(http.MethodPut, "/api/pay/merchant/business-name", map[string]any{
		"name": "Obaino4Christ", "password": "correct-horse", "expectedVersion": o.m.Version + 1})
	if stale.status != http.StatusConflict || stale.Code != "merchant_stale" {
		t.Fatalf("a stale screen: status %d body %s", stale.status, stale.raw)
	}
	if wrong := o.do(http.MethodPut, "/api/pay/merchant/business-name", map[string]any{"name": "Obaino4Christ", "password": "nope"}); wrong.Code != "password_incorrect" {
		t.Fatalf("a wrong password: status %d body %s", wrong.status, wrong.raw)
	}

	res := o.do(http.MethodPut, "/api/pay/merchant/business-name", map[string]any{
		"name": "Obaino4Christ", "password": "correct-horse", "expectedVersion": o.m.Version})
	if res.status != http.StatusAccepted || res.Data.Confirmation == nil || res.Data.Confirmation.Kind != models.PayOwnerActionBusinessName {
		t.Fatalf("status %d body %s", res.status, res.raw)
	}
	ok := o.confirm(t, res)
	if ok.status != http.StatusOK || ok.Data.Merchant == nil {
		t.Fatalf("status %d body %s", ok.status, ok.raw)
	}
	if v := ok.Data.Merchant; v.BusinessName != "Sky Air" || v.PendingBusinessName != "Obaino4Christ" || v.PendingSince == nil {
		t.Fatalf("merchant view %+v", v)
	}
	if got := o.stored(t); got.BusinessName != "Sky Air" {
		t.Fatalf("a request renamed the merchant: %+v", got)
	}
	var announced bool
	for _, e := range o.events() {
		announced = announced || e.Type == "pay_business_name_requested"
	}
	if !announced {
		t.Fatal("the owner must be told their request was received")
	}
}

// oneStableRail is a treasury offering one USDC lane, for the handler tests
// that need a paid fee.
type oneStableRail struct{}

func (oneStableRail) Available() bool { return true }
func (oneStableRail) To() string      { return "0x000000000000000000000000000000000000fEE5" }
func (oneStableRail) Options(context.Context) []services.StableFeeOption {
	return []services.StableFeeOption{{Chain: "base", ChainID: 8453, Token: "USDC",
		TokenAddress: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", Decimals: 6}}
}
func (oneStableRail) PrepareTransfer(string, string, string, *big.Int) ([]byte, error) {
	return nil, fmt.Errorf("not built in tests")
}
func (oneStableRail) VerifyPayment(context.Context, string, string, string, string, string) (*services.StablePayment, error) {
	return nil, services.ErrTransferNotFound
}
func (oneStableRail) Refund(context.Context, string, string, string, *big.Int, float64) (string, bool, error) {
	return "", false, fmt.Errorf("no refunds in tests")
}
func (oneStableRail) TransactionKnown(context.Context, string, string) (bool, error) {
	return true, nil
}

func paidNameChanges(db *gorm.DB) *services.NameChangeService {
	svc := services.NewNameChangeService(db, services.NameChangeConfig{Enabled: true, FeeUSD: "1.00", RequireFaceMatch: true}, nil, time.Now)
	svc.SetStableRail(oneStableRail{})
	return svc
}

func nameChangeRequest(h *Handlers, user *models.User, method, target string, body any) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	c.Request, _ = http.NewRequest(method, target, reader)
	c.Request.Header.Set("Content-Type", "application/json")
	if method == http.MethodPost {
		h.CreateNameChange(c)
	} else {
		h.GetNameChange(c)
	}
	return w
}

// A paid change is paid from the account's linked wallet, and only once the
// account has proven it owns it (main's wallet-link proof): until then the
// create is refused with WALLET_PROOF_REQUIRED — which the app answers by
// proving the wallet and retrying — and nothing is written. A wallet named in
// the body is never read.
func TestCreateNameChange_PaysOnlyFromAProvenLinkedWallet(t *testing.T) {
	db := nameChangeHandlerDB(t)
	ensureWalletLinkChallenges(t, db)
	user := verifiedUser(t, db, "ADA CHIOMA OBI")
	hashed, _ := models.HashPassword("correct-horse")
	linked := canonicalWalletAddress("0x87c2422be9a11b71a5c530ecd156134d3b04e3ce")
	db.Model(user).Updates(map[string]any{"password": hashed, "wallet_address": linked})
	user.Password, user.WalletAddress = hashed, linked
	h := &Handlers{Config: &config.Config{}, NameChanges: paidNameChanges(db)}
	body := map[string]any{"payMethod": "stablecoin", "chain": "base", "token": "USDC", "password": "correct-horse",
		// An old client's fields, and another wallet: all ignored.
		"walletAddress": "0x1111111111111111111111111111111111111111", "nonce": "x", "signature": "0x00"}

	w := nameChangeRequest(h, user, http.MethodPost, "/api/users/me/name-change", body)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"`+WalletProofRequiredCode+`"`) {
		t.Fatalf("an unproven wallet: %d %s", w.Code, w.Body.String())
	}
	var n int64
	db.Model(&models.UserNameChange{}).Count(&n)
	if n != 0 {
		t.Fatal("an unproven wallet opened a change")
	}

	mustRecordWalletProof(t, db, user.ID, linked)
	w = nameChangeRequest(h, user, http.MethodPost, "/api/users/me/name-change", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("a proven wallet: %d %s", w.Code, w.Body.String())
	}
	var stored models.UserNameChange
	db.First(&stored, "user_id = ?", user.ID)
	if stored.PayFrom != linked || !strings.Contains(w.Body.String(), `"from":"`+linked+`"`) {
		t.Fatalf("paid from %q, want the linked wallet: %s", stored.PayFrom, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID); got.WalletAddress != linked {
		t.Fatalf("a create re-pointed the account's wallet: %q", got.WalletAddress)
	}
}

// The overview's wallet is a read: the account's own wallet (any case) adds its
// balance, any other is refused — and never linked.
func TestGetNameChange_ReadsOnlyTheAccountsOwnWallet(t *testing.T) {
	db := nameChangeHandlerDB(t)
	user := verifiedUser(t, db, "ADA CHIOMA OBI")
	linked := canonicalWalletAddress("0x87c2422be9a11b71a5c530ecd156134d3b04e3ce")
	db.Model(user).Update("wallet_address", linked)
	user.WalletAddress = linked
	h := &Handlers{Config: &config.Config{}, NameChanges: paidNameChanges(db)}

	if w := nameChangeRequest(h, user, http.MethodGet, "/api/users/me/name-change?walletAddress="+strings.ToLower(linked), nil); w.Code != http.StatusOK {
		t.Fatalf("own wallet: %d %s", w.Code, w.Body.String())
	}
	if w := nameChangeRequest(h, user, http.MethodGet, "/api/users/me/name-change", nil); w.Code != http.StatusOK {
		t.Fatalf("no wallet named: %d %s", w.Code, w.Body.String())
	}
	other := "0x1111111111111111111111111111111111111111"
	w := nameChangeRequest(h, user, http.MethodGet, "/api/users/me/name-change?walletAddress="+other, nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), WalletProofRequiredCode) {
		t.Fatalf("another wallet: %d %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID); got.WalletAddress != linked {
		t.Fatalf("a GET re-pointed the account's wallet: %q", got.WalletAddress)
	}
}

// L5: the owner's merchant view says whether a rename can be asked for, so the
// web and app hide the form while the feature is off.
func TestPayOwner_MerchantViewSaysWhetherRenamesAreOn(t *testing.T) {
	o := newOwnerRig(t)
	read := func() map[string]any {
		w := payDo(o.r, http.MethodGet, "/api/pay/merchant", nil, nil)
		var body struct {
			Data map[string]any `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &body)
		return body.Data
	}
	if v, ok := read()["businessNameChangeEnabled"]; !ok || v != false {
		t.Fatalf("switched off: %v (present %v)", v, ok)
	}
	o.h.WithBusinessNameChange(true)
	if v := read()["businessNameChangeEnabled"]; v != true {
		t.Fatalf("switched on: %v", v)
	}
}
