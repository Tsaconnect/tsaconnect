package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// fakeDisputeSvc records what the handler passed down and returns a canned
// answer, so the routes can be exercised without a database.
type fakeDisputeSvc struct {
	fileIn      *services.DisputeInput
	fileUser    uuid.UUID
	escalateRef string
	resolveIn   [2]string
	dispute     models.Dispute
	err         error
}

func (f *fakeDisputeSvc) File(_ context.Context, userID uuid.UUID, in services.DisputeInput) (*models.Dispute, error) {
	f.fileIn, f.fileUser = &in, userID
	if f.err != nil {
		return nil, f.err
	}
	return &f.dispute, nil
}
func (f *fakeDisputeSvc) Get(context.Context, uuid.UUID, uuid.UUID) (*models.Dispute, error) {
	return &f.dispute, f.err
}
func (f *fakeDisputeSvc) ForSubject(context.Context, uuid.UUID, string, uuid.UUID) (*models.Dispute, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &f.dispute, nil
}
func (f *fakeDisputeSvc) List(context.Context, uuid.UUID, int) ([]models.Dispute, error) {
	return []models.Dispute{f.dispute}, f.err
}
func (f *fakeDisputeSvc) ListAdmin(_ context.Context, q services.DisputeQuery) ([]services.DisputeAdminRow, int64, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	return []services.DisputeAdminRow{{Dispute: f.dispute, Subject: "sell 50000 NGN"}}, 1, nil
}
func (f *fakeDisputeSvc) GetAdmin(context.Context, uuid.UUID) (*services.DisputeAdminRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &services.DisputeAdminRow{Dispute: f.dispute}, nil
}
func (f *fakeDisputeSvc) Escalate(_ context.Context, _, _ uuid.UUID, ref, _ string) (*models.Dispute, error) {
	f.escalateRef = ref
	if f.err != nil {
		return nil, f.err
	}
	return &f.dispute, nil
}
func (f *fakeDisputeSvc) Resolve(_ context.Context, _, _ uuid.UUID, status, resolution string) (*models.Dispute, error) {
	f.resolveIn = [2]string{status, resolution}
	if f.err != nil {
		return nil, f.err
	}
	return &f.dispute, nil
}

func disputeTestRouter(svc disputeService, user *models.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &DisputeHandler{disputes: svc}
	inject := func(c *gin.Context) {
		if user != nil {
			c.Set("user", user)
		}
	}
	g := r.Group("/disputes", inject)
	g.POST("", h.File)
	g.GET("", h.List)
	g.GET("/:id", h.Get)
	g.GET("/subject/:type/:id", h.ForSubject)
	a := r.Group("/admin/disputes", inject)
	a.GET("", h.ListAdmin)
	a.GET("/:id", h.GetAdmin)
	a.POST("/:id/escalate", h.EscalateAdmin)
	a.POST("/:id/resolve", h.ResolveAdmin)
	return r
}

func disputeDo(t *testing.T, r *gin.Engine, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func disputeUser() *models.User { return &models.User{ID: uuid.New()} }

// TestDisputeHandler_FileRequiresAuth: every route reads the caller from the
// context, so a missing user must 401 rather than file against the zero UUID.
func TestDisputeHandler_FileRequiresAuth(t *testing.T) {
	svc := &fakeDisputeSvc{}
	r := disputeTestRouter(svc, nil)

	w, _ := disputeDo(t, r, http.MethodPost, "/disputes", map[string]any{
		"subjectType": "paycrest_order", "subjectId": uuid.New().String(), "reason": "x",
	})

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if svc.fileIn != nil {
		t.Error("the service was called without an authenticated caller")
	}
}

func TestDisputeHandler_FilePassesTheCallersIdentityNotTheBody(t *testing.T) {
	// The body has no user field and must never gain one — a dispute is always
	// filed as the authenticated caller.
	user := disputeUser()
	svc := &fakeDisputeSvc{}
	r := disputeTestRouter(svc, user)
	subject := uuid.New()

	w, _ := disputeDo(t, r, http.MethodPost, "/disputes", map[string]any{
		"subjectType":  "paycrest_order",
		"subjectId":    subject.String(),
		"reason":       "Paid, never credited",
		"evidenceUrls": []string{"https://res.cloudinary.com/tsa/image/upload/a.jpg"},
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	if svc.fileUser != user.ID {
		t.Errorf("filed as %s, want the caller %s", svc.fileUser, user.ID)
	}
	if svc.fileIn.SubjectID != subject || len(svc.fileIn.Evidence) != 1 {
		t.Errorf("input not threaded through: %+v", svc.fileIn)
	}
}

func TestDisputeHandler_RejectsAMalformedSubjectID(t *testing.T) {
	svc := &fakeDisputeSvc{}
	r := disputeTestRouter(svc, disputeUser())

	w, _ := disputeDo(t, r, http.MethodPost, "/disputes", map[string]any{
		"subjectType": "paycrest_order", "subjectId": "not-a-uuid", "reason": "x",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if svc.fileIn != nil {
		t.Error("a malformed id reached the service")
	}
}

// TestDisputeHandler_ErrorMapping pins the status codes and the user-facing
// copy — these strings are shown in the app, not logged.
func TestDisputeHandler_ErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"already disputed", services.ErrDisputeAlreadyExists, http.StatusConflict},
		{"nothing to dispute yet", services.ErrDisputeNotDisputable, http.StatusConflict},
		{"not the caller's order", services.ErrDisputeSubjectNotFound, http.StatusNotFound},
		{"bad request", services.ErrDisputeBadRequest, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := disputeTestRouter(&fakeDisputeSvc{err: tc.err}, disputeUser())

			w, body := disputeDo(t, r, http.MethodPost, "/disputes", map[string]any{
				"subjectType": "paycrest_order", "subjectId": uuid.New().String(), "reason": "x",
			})

			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			if msg, _ := body["message"].(string); msg == "" {
				t.Error("no message for the app to show")
			}
		})
	}
}

// TestDisputeHandler_NotDisputablePointsAtCancel: the common cause is an order
// still awaiting payment, where cancelling is the right action — a bare
// refusal would leave the user stuck.
func TestDisputeHandler_NotDisputablePointsAtCancel(t *testing.T) {
	r := disputeTestRouter(&fakeDisputeSvc{err: services.ErrDisputeNotDisputable}, disputeUser())

	_, body := disputeDo(t, r, http.MethodPost, "/disputes", map[string]any{
		"subjectType": "paycrest_order", "subjectId": uuid.New().String(), "reason": "x",
	})

	msg, _ := body["message"].(string)
	if !bytes.Contains([]byte(msg), []byte("cancel")) {
		t.Errorf("message %q does not tell the user to cancel instead", msg)
	}
}

// TestDisputeHandler_ForSubjectIsA404WhenNothingFiled: the app treats this as
// the ordinary "no dispute" answer, so it must be a 404 and not a 200 with a
// null body.
func TestDisputeHandler_ForSubjectIsA404WhenNothingFiled(t *testing.T) {
	r := disputeTestRouter(&fakeDisputeSvc{err: services.ErrOrderDisputeNotFound}, disputeUser())

	w, _ := disputeDo(t, r, http.MethodGet, "/disputes/subject/paycrest_order/"+uuid.New().String(), nil)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDisputeHandler_AdminEscalateAndResolveThreadTheirInputs(t *testing.T) {
	svc := &fakeDisputeSvc{}
	r := disputeTestRouter(svc, disputeUser())
	id := uuid.New().String()

	if w, _ := disputeDo(t, r, http.MethodPost, "/admin/disputes/"+id+"/escalate",
		map[string]any{"externalRef": "PC-4471", "note": "emailed support"}); w.Code != http.StatusOK {
		t.Fatalf("escalate status = %d", w.Code)
	}
	if svc.escalateRef != "PC-4471" {
		t.Errorf("externalRef = %q", svc.escalateRef)
	}

	if w, _ := disputeDo(t, r, http.MethodPost, "/admin/disputes/"+id+"/resolve",
		map[string]any{"status": "resolved", "resolution": "Refunded"}); w.Code != http.StatusOK {
		t.Fatalf("resolve status = %d", w.Code)
	}
	if svc.resolveIn != [2]string{"resolved", "Refunded"} {
		t.Errorf("resolve input = %v", svc.resolveIn)
	}
}

func TestDisputeHandler_AdminResolveRequiresAStatus(t *testing.T) {
	svc := &fakeDisputeSvc{}
	r := disputeTestRouter(svc, disputeUser())

	w, _ := disputeDo(t, r, http.MethodPost, "/admin/disputes/"+uuid.New().String()+"/resolve",
		map[string]any{"resolution": "no status given"})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if svc.resolveIn[0] != "" {
		t.Error("a statusless resolve reached the service")
	}
}

// TestDisputeHandler_AdminListCarriesTheSubjectLabel: the queue is unreviewable
// if a row is only a type and a UUID.
func TestDisputeHandler_AdminListCarriesTheSubjectLabel(t *testing.T) {
	r := disputeTestRouter(&fakeDisputeSvc{}, disputeUser())

	_, body := disputeDo(t, r, http.MethodGet, "/admin/disputes?status=open", nil)

	data, _ := body["data"].(map[string]any)
	rows, _ := data["disputes"].([]any)
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	row, _ := rows[0].(map[string]any)
	if row["subject"] != "sell 50000 NGN" {
		t.Errorf("subject = %v, want the order label", row["subject"])
	}
}
