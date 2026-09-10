package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type inboundAssignCall struct {
	adminID, creditID uuid.UUID
	claimType         string
	claimID           uuid.UUID
	note              string
}

type inboundResolveCall struct {
	adminID, creditID uuid.UUID
	note              string
}

// fakeInboundAdminSvc implements inboundAdminService in-memory.
type fakeInboundAdminSvc struct {
	query   *services.InboundCreditQuery
	credits []models.InboundCredit
	total   int64
	view    *services.InboundCandidatesView
	credit  models.InboundCredit
	assign  *inboundAssignCall
	refund  *inboundResolveCall
	ignore  *inboundResolveCall
	err     error
}

func (f *fakeInboundAdminSvc) ListCredits(_ context.Context, q services.InboundCreditQuery) ([]models.InboundCredit, int64, error) {
	f.query = &q
	if f.err != nil {
		return nil, 0, f.err
	}
	return f.credits, f.total, nil
}

func (f *fakeInboundAdminSvc) Candidates(_ context.Context, _ uuid.UUID) (*services.InboundCandidatesView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.view, nil
}

func (f *fakeInboundAdminSvc) Assign(_ context.Context, adminID, creditID uuid.UUID, claimType string, claimID uuid.UUID, note string) (*models.InboundCredit, error) {
	f.assign = &inboundAssignCall{adminID, creditID, claimType, claimID, note}
	if f.err != nil {
		return nil, f.err
	}
	return &f.credit, nil
}

func (f *fakeInboundAdminSvc) MarkRefunded(_ context.Context, adminID, creditID uuid.UUID, note string) (*models.InboundCredit, error) {
	f.refund = &inboundResolveCall{adminID, creditID, note}
	if f.err != nil {
		return nil, f.err
	}
	return &f.credit, nil
}

func (f *fakeInboundAdminSvc) MarkIgnored(_ context.Context, adminID, creditID uuid.UUID, note string) (*models.InboundCredit, error) {
	f.ignore = &inboundResolveCall{adminID, creditID, note}
	if f.err != nil {
		return nil, f.err
	}
	return &f.credit, nil
}

func inboundAdminRouter(svc *fakeInboundAdminSvc, admin *models.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewInboundAdminHandler(svc)
	inject := func(c *gin.Context) {
		if admin != nil {
			c.Set("user", admin)
		}
	}
	g := r.Group("/admin/inbound", inject)
	g.GET("/credits", h.ListCredits)
	g.GET("/credits/:id/candidates", h.GetCandidates)
	g.POST("/credits/:id/assign", h.AssignCredit)
	g.POST("/credits/:id/refund", h.MarkRefunded)
	g.POST("/credits/:id/ignore", h.MarkIgnored)
	return r
}

func inboundAdminDo(t *testing.T, r *gin.Engine, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func inboundTestAdmin() *models.User {
	return &models.User{ID: uuid.New(), Role: "super_admin", Email: "admin@tsa.test"}
}

func TestInboundAdminHandlerListCredits(t *testing.T) {
	svc := &fakeInboundAdminSvc{
		total: 1,
		credits: []models.InboundCredit{{
			ID:              uuid.New(),
			Currency:        "NGN",
			GrossKobo:       200000,
			SenderRaw:       "Received Money from GTBANK|ADEMU EMMANUEL O.",
			SenderBank:      "GTBANK",
			SenderName:      "ADEMU EMMANUEL O.",
			TransactionDate: time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC),
			Status:          models.InboundCreditHeld,
			HoldReason:      "two open claims matched",
		}},
	}
	w, out := inboundAdminDo(t, inboundAdminRouter(svc, inboundTestAdmin()), http.MethodGet, "/admin/inbound/credits", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if out["success"] != true {
		t.Fatalf("envelope = %v", out)
	}
	data, _ := out["data"].(map[string]any)
	rows, _ := data["credits"].([]any)
	if len(rows) != 1 {
		t.Fatalf("credits = %v", data["credits"])
	}
	row, _ := rows[0].(map[string]any)
	for _, field := range []string{"senderRaw", "senderBank", "senderName", "grossKobo", "transactionDate", "holdReason"} {
		if _, present := row[field]; !present {
			t.Errorf("listed credit is missing %q — an operator cannot act without it: %v", field, row)
		}
	}
	if data["total"] != float64(1) {
		t.Errorf("total = %v, want 1", data["total"])
	}
}

func TestInboundAdminHandlerListCreditsPassesFilters(t *testing.T) {
	svc := &fakeInboundAdminSvc{}
	r := inboundAdminRouter(svc, inboundTestAdmin())

	w, _ := inboundAdminDo(t, r, http.MethodGet,
		"/admin/inbound/credits?status=refunded&currency=ngn&from=2026-09-01&to=2026-09-10&limit=10&offset=20", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	q := svc.query
	if q == nil {
		t.Fatal("service was not called")
	}
	if q.Status != "refunded" || q.Currency != "ngn" || q.Limit != 10 || q.Offset != 20 {
		t.Errorf("query = %+v", q)
	}
	if !q.From.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("from = %s, want 2026-09-01T00:00:00Z", q.From)
	}
	// A bare `to` date means "through that day". Taking it as midnight would
	// silently hide every credit that landed on the day being reconciled.
	if !q.To.Equal(time.Date(2026, 9, 10, 23, 59, 59, int(time.Second-time.Nanosecond), time.UTC)) {
		t.Errorf("to = %s, want the end of 2026-09-10", q.To)
	}
}

func TestInboundAdminHandlerListCreditsBadDate(t *testing.T) {
	w, _ := inboundAdminDo(t, inboundAdminRouter(&fakeInboundAdminSvc{}, inboundTestAdmin()),
		http.MethodGet, "/admin/inbound/credits?from=last-tuesday", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestInboundAdminHandlerCandidates(t *testing.T) {
	creditID := uuid.New()
	svc := &fakeInboundAdminSvc{view: &services.InboundCandidatesView{
		Credit:           models.InboundCredit{ID: creditID, SenderName: "ADEMU EMMANUEL"},
		SenderNameTokens: []string{"ADEMU", "EMMANUEL"},
		MatchedNairaKobo: 200000,
		Candidates: []services.InboundCandidate{{
			ClaimType:        services.InboundClaimTypeITMCGP,
			ClaimID:          uuid.New(),
			AmountKobo:       150000,
			AmountDeltaKobo:  -50000,
			NameStrength:     "exact",
			KYCNameTokens:    []string{"ADEMU", "EMMANUEL"},
			SharedNameTokens: []string{"ADEMU", "EMMANUEL"},
			Rejection:        services.InboundRejectAmount,
		}},
	}}
	w, out := inboundAdminDo(t, inboundAdminRouter(svc, inboundTestAdmin()),
		http.MethodGet, "/admin/inbound/credits/"+creditID.String()+"/candidates", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	data, _ := out["data"].(map[string]any)
	cands, _ := data["candidates"].([]any)
	if len(cands) != 1 {
		t.Fatalf("candidates = %v", data["candidates"])
	}
	c, _ := cands[0].(map[string]any)
	if c["rejection"] != services.InboundRejectAmount {
		t.Errorf("rejection = %v, want %q", c["rejection"], services.InboundRejectAmount)
	}
	if c["nameStrength"] != "exact" {
		t.Errorf("nameStrength = %v", c["nameStrength"])
	}
}

func TestInboundAdminHandlerBadCreditID(t *testing.T) {
	r := inboundAdminRouter(&fakeInboundAdminSvc{}, inboundTestAdmin())
	for _, path := range []string{
		"/admin/inbound/credits/not-a-uuid/candidates",
	} {
		w, _ := inboundAdminDo(t, r, http.MethodGet, path, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", path, w.Code)
		}
	}
}

func TestInboundAdminHandlerAssign(t *testing.T) {
	svc := &fakeInboundAdminSvc{credit: models.InboundCredit{Status: models.InboundCreditMatched}}
	admin := inboundTestAdmin()
	creditID, claimID := uuid.New(), uuid.New()

	w, out := inboundAdminDo(t, inboundAdminRouter(svc, admin), http.MethodPost,
		"/admin/inbound/credits/"+creditID.String()+"/assign",
		map[string]any{"claimType": "it_mcgp", "claimId": claimID.String(), "note": "sender confirmed by phone"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if out["success"] != true {
		t.Errorf("envelope = %v", out)
	}
	if svc.assign == nil {
		t.Fatal("Assign was not called")
	}
	// The acting admin has to reach the service — the audit trail is the whole
	// reason this endpoint exists.
	if svc.assign.adminID != admin.ID {
		t.Errorf("adminID = %s, want %s", svc.assign.adminID, admin.ID)
	}
	if svc.assign.creditID != creditID || svc.assign.claimID != claimID || svc.assign.claimType != "it_mcgp" {
		t.Errorf("assign call = %+v", svc.assign)
	}
	if svc.assign.note != "sender confirmed by phone" {
		t.Errorf("note = %q", svc.assign.note)
	}
}

func TestInboundAdminHandlerAssignBadClaimID(t *testing.T) {
	svc := &fakeInboundAdminSvc{}
	w, _ := inboundAdminDo(t, inboundAdminRouter(svc, inboundTestAdmin()), http.MethodPost,
		"/admin/inbound/credits/"+uuid.New().String()+"/assign",
		map[string]any{"claimType": "it_mcgp", "claimId": "nope"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if svc.assign != nil {
		t.Error("a malformed claim id must not reach the bind path")
	}
}

func TestInboundAdminHandlerRefundAndIgnore(t *testing.T) {
	admin := inboundTestAdmin()
	creditID := uuid.New()

	svc := &fakeInboundAdminSvc{credit: models.InboundCredit{Status: models.InboundCreditRefunded}}
	w, _ := inboundAdminDo(t, inboundAdminRouter(svc, admin), http.MethodPost,
		"/admin/inbound/credits/"+creditID.String()+"/refund", map[string]any{"note": "returned via TRF-99"})
	if w.Code != http.StatusOK {
		t.Fatalf("refund status = %d, body %s", w.Code, w.Body)
	}
	if svc.refund == nil || svc.refund.adminID != admin.ID || svc.refund.note != "returned via TRF-99" {
		t.Errorf("refund call = %+v", svc.refund)
	}

	svc = &fakeInboundAdminSvc{credit: models.InboundCredit{Status: models.InboundCreditIgnored}}
	w, _ = inboundAdminDo(t, inboundAdminRouter(svc, admin), http.MethodPost,
		"/admin/inbound/credits/"+creditID.String()+"/ignore", map[string]any{"note": "own float top-up"})
	if w.Code != http.StatusOK {
		t.Fatalf("ignore status = %d, body %s", w.Code, w.Body)
	}
	if svc.ignore == nil || svc.ignore.adminID != admin.ID {
		t.Errorf("ignore call = %+v", svc.ignore)
	}
}

// An unauthenticated caller has no id to record, so there is nobody to hold
// responsible for the money moving.
func TestInboundAdminHandlerRequiresAnActor(t *testing.T) {
	svc := &fakeInboundAdminSvc{}
	w, _ := inboundAdminDo(t, inboundAdminRouter(svc, nil), http.MethodPost,
		"/admin/inbound/credits/"+uuid.New().String()+"/refund", map[string]any{"note": "x"})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if svc.refund != nil {
		t.Error("an anonymous caller must not reach the service")
	}
}

func TestInboundAdminHandlerErrorMapping(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"not found":        {services.ErrInboundCreditNotFound, http.StatusNotFound},
		"already resolved": {services.ErrInboundCreditNotHeld, http.StatusConflict},
		"bad request":      {services.ErrInboundBadRequest, http.StatusBadRequest},
		"matcher unwired":  {services.ErrInboundBinderUnavailable, http.StatusServiceUnavailable},
		"unexpected":       {errors.New("pq: syntax error near SELECT"), http.StatusInternalServerError},
	}
	for name, tc := range cases {
		svc := &fakeInboundAdminSvc{err: tc.err}
		w, out := inboundAdminDo(t, inboundAdminRouter(svc, inboundTestAdmin()), http.MethodPost,
			"/admin/inbound/credits/"+uuid.New().String()+"/refund", map[string]any{"note": "x"})
		if w.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", name, w.Code, tc.want)
		}
		if out["success"] != false {
			t.Errorf("%s: envelope = %v", name, out)
		}
		// An unexpected error can embed SQL or a provider URL; the client gets
		// a generic line and the detail goes to the log.
		if name == "unexpected" {
			if msg, _ := out["message"].(string); msg == tc.err.Error() {
				t.Errorf("%s: leaked internal error to the client: %q", name, msg)
			}
		}
	}
}
