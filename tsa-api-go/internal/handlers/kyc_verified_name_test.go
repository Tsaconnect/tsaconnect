package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ---------- fake Didit decision endpoint ----------

// diditDecisionStub serves GET /session/{id}/decision/ from a per-session map of
// canned bodies. Sessions absent from the map 404 the way Didit does for a
// session it no longer holds — the case the Aug 2026 outage left behind.
func diditDecisionStub(t *testing.T, bodies map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" {
			t.Errorf("decision request sent without x-api-key")
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 3 || parts[0] != "session" || parts[2] != "decision" {
			t.Errorf("unexpected decision path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, ok := bodies[parts[1]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"detail":"Not found."}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func approvedDecision(fullName string) string {
	return fmt.Sprintf(`{
		"session_id":"s","status":"Approved",
		"id_verifications":[
			{"node_id":"id_verification_1","status":"Approved","full_name":"%s","first_name":"X","last_name":"Y"}
		]
	}`, fullName)
}

// ---------- name capture on approval ----------

func TestKYCWebhookStoresVerifiedLegalNameOnApproval(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	didit := diditDecisionStub(t, map[string]string{
		"sess-1": approvedDecision("ADEBAYO CHUKWUEMEKA OKONKWO"),
	})
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{
		DiditAPIKey: "key", DiditWebhookSecret: secret, DiditBaseURL: didit.URL,
	}}

	body := webhookBodyFor(user, "Approved")
	w := postKYCWebhook(h, body, signedHeaders(body, secret))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got := reloadUser(t, db, user.ID)
	if got.VerifiedLegalName != "ADEBAYO CHUKWUEMEKA OKONKWO" {
		t.Fatalf("verified legal name not stored, got %q", got.VerifiedLegalName)
	}
	if got.VerifiedLegalNameAt == nil {
		t.Fatal("expected verified_legal_name_at to be dated")
	}
}

// The approval is the part that gates the user's account. A decision lookup
// that fails must never cost them their verification — the backfill exists to
// pick the name up later.
func TestKYCWebhookApprovalSurvivesNameLookupFailure(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	didit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"detail":"boom"}`)
	}))
	defer didit.Close()
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{
		DiditAPIKey: "key", DiditWebhookSecret: secret, DiditBaseURL: didit.URL,
	}}

	body := webhookBodyFor(user, "Approved")
	w := postKYCWebhook(h, body, signedHeaders(body, secret))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got := reloadUser(t, db, user.ID)
	if got.VerificationStatus != models.VerificationStatusVerified {
		t.Fatalf("a failed name lookup blocked the approval: status %q", got.VerificationStatus)
	}
	if got.VerifiedLegalName != "" {
		t.Fatalf("expected no name from a failed lookup, got %q", got.VerifiedLegalName)
	}
}

// Identity data must never reach the log, on either path.
func TestKYCWebhookNeverLogsVerifiedName(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	didit := diditDecisionStub(t, map[string]string{
		"sess-1": approvedDecision("ADEBAYO CHUKWUEMEKA OKONKWO"),
	})
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{
		DiditAPIKey: "key", DiditWebhookSecret: secret, DiditBaseURL: didit.URL,
	}}

	body := webhookBodyFor(user, "Approved")
	postKYCWebhook(h, body, signedHeaders(body, secret))

	if strings.Contains(logs.String(), "ADEBAYO") || strings.Contains(logs.String(), "OKONKWO") {
		t.Fatalf("identity data reached the log: %s", logs.String())
	}
}

// The verification log records that a name was captured; it must not record the
// name itself.
func TestKYCWebhookVerificationLogCarriesNoIdentityData(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	didit := diditDecisionStub(t, map[string]string{
		"sess-1": approvedDecision("ADEBAYO CHUKWUEMEKA OKONKWO"),
	})
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{
		DiditAPIKey: "key", DiditWebhookSecret: secret, DiditBaseURL: didit.URL,
	}}

	body := webhookBodyFor(user, "Approved")
	postKYCWebhook(h, body, signedHeaders(body, secret))

	var entries []models.VerificationLog
	db.Where("user_id = ?", user.ID).Find(&entries)
	for _, e := range entries {
		blob := e.Notes + string(e.Metadata)
		if strings.Contains(blob, "ADEBAYO") || strings.Contains(blob, "OKONKWO") {
			t.Fatalf("verification log stored identity data: %s", blob)
		}
	}
}

// ---------- data.updated ----------

// A reviewer can correct a name after approval; Didit signals that with
// data.updated. The corrected name is what money will be matched against, so it
// must replace the stored one — without touching the verification status.
func TestKYCWebhookDataUpdatedRefreshesName(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusVerified)
	db.Model(user).Updates(map[string]interface{}{
		"didit_session_id":    "sess-1",
		"verified_legal_name": "OLD MISREAD NAME",
	})
	didit := diditDecisionStub(t, map[string]string{
		"sess-1": approvedDecision("CORRECTED LEGAL NAME"),
	})
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{
		DiditAPIKey: "key", DiditWebhookSecret: secret, DiditBaseURL: didit.URL,
	}}

	body := []byte(fmt.Sprintf(
		`{"event_id":"e-data","webhook_type":"data.updated","timestamp":%d,"session_id":"sess-1","status":"Approved","vendor_data":"%s"}`,
		time.Now().Unix(), user.ID.String()))
	w := postKYCWebhook(h, body, signedHeaders(body, secret))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got := reloadUser(t, db, user.ID)
	if got.VerifiedLegalName != "CORRECTED LEGAL NAME" {
		t.Fatalf("expected the corrected name, got %q", got.VerifiedLegalName)
	}
	if got.VerificationStatus != models.VerificationStatusVerified {
		t.Fatalf("data.updated changed the verification status to %q", got.VerificationStatus)
	}
}

// A name only means something once the session it came from was approved.
func TestKYCWebhookDataUpdatedIgnoredForUnverifiedUser(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	db.Model(user).Update("didit_session_id", "sess-1")
	didit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Didit was called for an unverified user's data.updated event")
	}))
	defer didit.Close()
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{
		DiditAPIKey: "key", DiditWebhookSecret: secret, DiditBaseURL: didit.URL,
	}}

	body := []byte(fmt.Sprintf(
		`{"event_id":"e-data","webhook_type":"data.updated","timestamp":%d,"session_id":"sess-1","status":"In Review","vendor_data":"%s"}`,
		time.Now().Unix(), user.ID.String()))
	w := postKYCWebhook(h, body, signedHeaders(body, secret))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID).VerifiedLegalName; got != "" {
		t.Fatalf("expected no name for an unverified user, got %q", got)
	}
}

func TestKYCWebhookStillIgnoresUnknownEventTypes(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: secret}}

	body := []byte(fmt.Sprintf(
		`{"event_id":"e-x","webhook_type":"transaction.created","timestamp":%d,"session_id":"sess-1","status":"Approved","vendor_data":"%s"}`,
		time.Now().Unix(), user.ID.String()))
	w := postKYCWebhook(h, body, signedHeaders(body, secret))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID).VerificationStatus; got != models.VerificationStatusInReview {
		t.Fatalf("an unrelated event type changed the status to %q", got)
	}
}

// ---------- fetchDiditVerifiedName ----------

func TestFetchDiditVerifiedNameFallsBackToFirstAndLast(t *testing.T) {
	didit := diditDecisionStub(t, map[string]string{
		"s1": `{"status":"Approved","id_verifications":[
			{"node_id":"id_verification_1","status":"Approved","first_name":"Ngozi","last_name":"Eze"}
		]}`,
	})

	name, err := fetchDiditVerifiedName(context.Background(), didit.URL, "key", "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "Ngozi Eze" {
		t.Fatalf("expected the first+last fallback, got %q", name)
	}
}

// V3 workflows are graphs: every feature block is an array and a workflow can
// run ID verification more than once. Only an approved node's name is usable.
func TestFetchDiditVerifiedNamePrefersApprovedNode(t *testing.T) {
	didit := diditDecisionStub(t, map[string]string{
		"s1": `{"status":"Approved","id_verifications":[
			{"node_id":"id_verification_1","status":"Declined","full_name":"REJECTED READ"},
			{"node_id":"id_verification_2","status":"Approved","full_name":"ACCEPTED READ"}
		]}`,
	})

	name, err := fetchDiditVerifiedName(context.Background(), didit.URL, "key", "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "ACCEPTED READ" {
		t.Fatalf("expected the approved node's name, got %q", name)
	}
}

// A workflow can be configured to withhold name attributes, which come back as
// null. That is an absent name, not an error to retry forever.
func TestFetchDiditVerifiedNameUnresolvedWhenAttributesWithheld(t *testing.T) {
	didit := diditDecisionStub(t, map[string]string{
		"s1": `{"status":"Approved","id_verifications":[
			{"node_id":"id_verification_1","status":"Approved","full_name":null,"first_name":null,"last_name":null}
		]}`,
	})

	if _, err := fetchDiditVerifiedName(context.Background(), didit.URL, "key", "s1"); err != errDiditNameUnresolved {
		t.Fatalf("expected errDiditNameUnresolved, got %v", err)
	}
}

func TestFetchDiditVerifiedNameUnresolvedOnMissingSession(t *testing.T) {
	didit := diditDecisionStub(t, map[string]string{})

	if _, err := fetchDiditVerifiedName(context.Background(), didit.URL, "key", "gone"); err != errDiditNameUnresolved {
		t.Fatalf("expected errDiditNameUnresolved for a 404, got %v", err)
	}
}

// Error strings are logged. The decision body is identity data, so it can never
// be part of one.
func TestFetchDiditVerifiedNameErrorCarriesNoBody(t *testing.T) {
	didit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"full_name":"ADEBAYO OKONKWO","detail":"leaky"}`)
	}))
	defer didit.Close()

	_, err := fetchDiditVerifiedName(context.Background(), didit.URL, "key", "s1")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "ADEBAYO") {
		t.Fatalf("error leaked the decision body: %v", err)
	}
}

// ---------- backfill ----------

func adminBackfillRequest(h *Handlers, admin *models.User, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", admin)
	c.Request, _ = http.NewRequest("POST", "/api/kyc/admin/backfill-legal-names",
		strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.AdminBackfillVerifiedNames(c)
	return w
}

type backfillResult struct {
	Data struct {
		Scanned    int      `json:"scanned"`
		Updated    int      `json:"updated"`
		Unresolved int      `json:"unresolved"`
		Failed     int      `json:"failed"`
		Remaining  int64    `json:"remaining"`
		UserIDs    []string `json:"unresolvedUserIds"`
	} `json:"data"`
}

func verifiedUserWithSession(t *testing.T, db *gorm.DB, sessionID string) *models.User {
	t.Helper()
	u := createKYCTestUser(t, db, models.VerificationStatusVerified)
	if err := db.Model(u).Update("didit_session_id", sessionID).Error; err != nil {
		t.Fatalf("failed to set session id: %v", err)
	}
	return u
}

// The Aug 2026 Didit outage left users verified against sessions that no longer
// resolve. One of those must not stop the run — it is reported and skipped.
func TestAdminBackfillToleratesUnresolvableSessions(t *testing.T) {
	db := setupKYCTestDB(t, true)
	resolvable := verifiedUserWithSession(t, db, "sess-ok")
	gone := verifiedUserWithSession(t, db, "sess-gone")
	sessionless := verifiedUserWithSession(t, db, "")
	// The admin runs the backfill but is not part of the population it repairs.
	admin := createKYCTestUser(t, db, models.VerificationStatusPending)

	didit := diditDecisionStub(t, map[string]string{
		"sess-ok": approvedDecision("ADAEZE NWANKWO"),
	})
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditBaseURL: didit.URL}}

	w := adminBackfillRequest(h, admin, `{"limit":50}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var res backfillResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("bad response: %v", err)
	}
	if res.Data.Updated != 1 {
		t.Fatalf("expected 1 update, got %+v", res.Data)
	}
	if res.Data.Unresolved != 2 {
		t.Fatalf("expected the 404 and the sessionless user to be unresolved, got %+v", res.Data)
	}
	if got := reloadUser(t, db, resolvable.ID).VerifiedLegalName; got != "ADAEZE NWANKWO" {
		t.Fatalf("resolvable user not backfilled, got %q", got)
	}
	if got := reloadUser(t, db, gone.ID).VerifiedLegalName; got != "" {
		t.Fatalf("unresolvable user got a name: %q", got)
	}
	if got := reloadUser(t, db, sessionless.ID).VerifiedLegalName; got != "" {
		t.Fatalf("sessionless user got a name: %q", got)
	}
	if len(res.Data.UserIDs) != 2 {
		t.Fatalf("expected the unresolved users to be reported, got %+v", res.Data.UserIDs)
	}
}

// Re-running the backfill must not re-pay for names already held, and must not
// reach for users who never passed verification.
func TestAdminBackfillSkipsNamedAndUnverifiedUsers(t *testing.T) {
	db := setupKYCTestDB(t, true)
	named := verifiedUserWithSession(t, db, "sess-named")
	db.Model(named).Update("verified_legal_name", "ALREADY KNOWN")
	pending := createKYCTestUser(t, db, models.VerificationStatusInReview)
	db.Model(pending).Update("didit_session_id", "sess-pending")
	// The admin runs the backfill but is not part of the population it repairs.
	admin := createKYCTestUser(t, db, models.VerificationStatusPending)

	didit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Didit was called for a user that needed no backfill: %s", r.URL.Path)
	}))
	defer didit.Close()
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditBaseURL: didit.URL}}

	w := adminBackfillRequest(h, admin, `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var res backfillResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("bad response: %v", err)
	}
	if res.Data.Scanned != 0 || res.Data.Updated != 0 {
		t.Fatalf("expected nobody to need a backfill, got %+v", res.Data)
	}
	if got := reloadUser(t, db, named.ID).VerifiedLegalName; got != "ALREADY KNOWN" {
		t.Fatalf("an already-named user was overwritten: %q", got)
	}
}

func TestAdminBackfillUnavailableWhenDiditDormant(t *testing.T) {
	db := setupKYCTestDB(t, true)
	// The admin runs the backfill but is not part of the population it repairs.
	admin := createKYCTestUser(t, db, models.VerificationStatusPending)
	h := &Handlers{Config: &config.Config{}}

	w := adminBackfillRequest(h, admin, `{}`)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when Didit is unconfigured, got %d: %s", w.Code, w.Body.String())
	}
}

// An operator who gives up on a run — or the proxy that times the request out
// long before the loop is done — has to actually stop the work. Until the
// request context reached the outbound call, an abandoned run kept spending
// provider calls for a client that was no longer listening.
func TestAdminBackfillStopsWhenTheRequestIsCancelled(t *testing.T) {
	db := setupKYCTestDB(t, true)
	for i := 0; i < 3; i++ {
		verifiedUserWithSession(t, db, fmt.Sprintf("sess-%d", i))
	}
	admin := createKYCTestUser(t, db, models.VerificationStatusPending)

	var hits int32
	didit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, approvedDecision("ADAEZE NWANKWO"))
	}))
	defer didit.Close()
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditBaseURL: didit.URL}}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", admin)
	req, _ := http.NewRequest("POST", "/api/kyc/admin/backfill-legal-names", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	c.Request = req.WithContext(ctx)

	h.AdminBackfillVerifiedNames(c)

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("a cancelled run still made %d Didit calls", n)
	}
	var named int64
	db.Model(&models.User{}).Where("verified_legal_name != ''").Count(&named)
	if named != 0 {
		t.Fatalf("a cancelled run still wrote %d names", named)
	}
}

// The endpoint is designed to be re-run a page at a time, so the ceiling only
// has to be a number a single request can reach the end of: every user in a
// run costs one sequential provider call at a 15s timeout, and a cap in the
// hundreds is hours of work behind a request nobody is still holding open.
func TestAdminBackfillCapsOneRunToWhatARequestCanFinish(t *testing.T) {
	db := setupKYCTestDB(t, true)
	const population = 60
	bodies := make(map[string]string, population)
	for i := 0; i < population; i++ {
		session := fmt.Sprintf("sess-%d", i)
		verifiedUserWithSession(t, db, session)
		bodies[session] = approvedDecision("ADAEZE NWANKWO")
	}
	admin := createKYCTestUser(t, db, models.VerificationStatusPending)

	didit := diditDecisionStub(t, bodies)
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditBaseURL: didit.URL}}

	w := adminBackfillRequest(h, admin, `{"limit":500}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var res backfillResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("bad response: %v", err)
	}
	if res.Data.Scanned > 50 {
		t.Fatalf("one run took on %d users, more than a request can finish: %+v", res.Data.Scanned, res.Data)
	}
	// The rest is left for the next run, and the operator is told how many.
	if res.Data.Remaining != population-int64(res.Data.Updated) {
		t.Fatalf("remaining count does not account for the users left behind: %+v", res.Data)
	}
}

// ---------- deliberate exposure ----------

// The user is told which name a transfer has to arrive under; they cannot pay
// from the right account otherwise.
func TestGetKYCStatusExposesOwnVerifiedName(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusVerified)
	db.Model(user).Update("verified_legal_name", "ADAEZE NWANKWO")
	user.VerifiedLegalName = "ADAEZE NWANKWO"

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	c.Request, _ = http.NewRequest("GET", "/api/kyc/status", nil)

	(&Handlers{Config: &config.Config{}}).GetKYCStatus(c)

	var resp struct {
		Data struct {
			VerifiedLegalName string `json:"verifiedLegalName"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response: %v", err)
	}
	if resp.Data.VerifiedLegalName != "ADAEZE NWANKWO" {
		t.Fatalf("expected the user's own verified name, got %q", resp.Data.VerifiedLegalName)
	}
}

// Admins adjudicating a held credit compare the sender's account name against
// this exact string, so the queue has to show it.
func TestGetAllVerificationsExposesVerifiedName(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusVerified)
	db.Model(user).Update("verified_legal_name", "ADAEZE NWANKWO")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("GET", "/api/kyc/admin/verifications", nil)

	(&Handlers{Config: &config.Config{}}).GetAllVerifications(c)

	if !strings.Contains(w.Body.String(), "ADAEZE NWANKWO") {
		t.Fatalf("admin verification listing hides the verified name: %s", w.Body.String())
	}
	var resp struct {
		Data []struct {
			VerifiedLegalName string `json:"verifiedLegalName"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].VerifiedLegalName != "ADAEZE NWANKWO" {
		t.Fatalf("expected verifiedLegalName on the listing row, got %+v", resp.Data)
	}
}

// ---------- admin override ----------

func adminOverrideRequest(h *Handlers, admin *models.User, userID uuid.UUID, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", admin)
	c.Params = gin.Params{{Key: "id", Value: userID.String()}}
	c.Request, _ = http.NewRequest("POST", "/api/kyc/admin/override/"+userID.String(),
		strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.AdminOverrideKYC(c)
	return w
}

// overrideAdmin is an admin; the route only admits admin and super_admin.
func overrideAdmin(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	admin := createKYCTestUser(t, db, models.VerificationStatusVerified)
	admin.Role = models.RoleAdmin
	return admin
}

func storedIdentity(t *testing.T, db *gorm.DB, id uuid.UUID) (status, name string) {
	t.Helper()
	var row struct {
		VerificationStatus string
		VerifiedLegalName  sql.NullString
	}
	db.Table("users").Select("verification_status", "verified_legal_name").Where("id = ?", id).Scan(&row)
	return row.VerificationStatus, row.VerifiedLegalName.String
}

// An approval that stores no name leaves the user "verified" yet refused at
// every pay-in, so the override refuses rather than half-approve.
func TestAdminOverrideRefusesWithoutAnyLegalName(t *testing.T) {
	db := setupKYCTestDB(t, true)
	admin := createKYCTestUser(t, db, models.VerificationStatusVerified)
	user := createKYCTestUser(t, db, models.VerificationStatusRejected)

	w := adminOverrideRequest(&Handlers{Config: &config.Config{}}, admin, user.ID, `{"notes":"ok"}`)

	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "legal_name_required") {
		t.Fatalf("want 400 legal_name_required, got %d %s", w.Code, w.Body.String())
	}
	if status, _ := storedIdentity(t, db, user.ID); status != models.VerificationStatusRejected {
		t.Fatalf("refused override still changed status to %q", status)
	}
}

func TestAdminOverrideStoresTypedLegalName(t *testing.T) {
	db := setupKYCTestDB(t, true)
	admin := overrideAdmin(t, db)
	user := createKYCTestUser(t, db, models.VerificationStatusRejected)

	w := adminOverrideRequest(&Handlers{Config: &config.Config{}}, admin, user.ID,
		`{"notes":"ok","legalName":"  ADEBAYO   OKONKWO "}`)

	if w.Code != http.StatusOK {
		t.Fatalf("override: %d %s", w.Code, w.Body.String())
	}
	status, name := storedIdentity(t, db, user.ID)
	if status != models.VerificationStatusVerified || name != "ADEBAYO OKONKWO" {
		t.Fatalf("got status %q name %q", status, name)
	}
	var entries []models.VerificationLog
	db.Where("user_id = ?", user.ID).Find(&entries)
	for _, e := range entries {
		if strings.Contains(e.Notes+string(e.Metadata), "ADEBAYO") {
			t.Fatalf("verification log stored identity data: %s", e.Metadata)
		}
	}
}

func TestAdminOverrideReadsNameFromDiditSession(t *testing.T) {
	db := setupKYCTestDB(t, true)
	admin := createKYCTestUser(t, db, models.VerificationStatusVerified)
	user := createKYCTestUser(t, db, models.VerificationStatusRejected)
	db.Model(user).Update("didit_session_id", "sess-1")
	user.DiditSessionID = "sess-1"
	didit := diditDecisionStub(t, map[string]string{"sess-1": approvedDecision("CHIOMA EZE")})
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditBaseURL: didit.URL}}

	if w := adminOverrideRequest(h, admin, user.ID, `{}`); w.Code != http.StatusOK {
		t.Fatalf("override: %d %s", w.Code, w.Body.String())
	}
	if _, name := storedIdentity(t, db, user.ID); name != "CHIOMA EZE" {
		t.Fatalf("got name %q", name)
	}
}

// Re-approving must not become a back door around the name-change review.
func TestAdminOverrideNeverReplacesADifferentStoredName(t *testing.T) {
	db := setupKYCTestDB(t, true)
	admin := overrideAdmin(t, db)
	user := createKYCTestUser(t, db, models.VerificationStatusVerified)
	db.Model(user).Update("verified_legal_name", "CHIOMA EZE")

	w := adminOverrideRequest(&Handlers{Config: &config.Config{}}, admin, user.ID, `{"legalName":"SOMEONE ELSE"}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", w.Code, w.Body.String())
	}
	if _, name := storedIdentity(t, db, user.ID); name != "CHIOMA EZE" {
		t.Fatalf("stored name replaced with %q", name)
	}
}
