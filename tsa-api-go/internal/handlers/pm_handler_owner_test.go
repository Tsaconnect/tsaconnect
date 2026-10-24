package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// The admin list shows who owns a merchant and which admin last actioned it.
func TestPMHandler_ListPMs_OwnerAndReviewer(t *testing.T) {
	db := newSQLiteForPMHandler(t)
	owner, admin := uuid.New(), uuid.New()
	if err := db.Exec(`INSERT INTO users (id, name, email, phone_number, verified_legal_name) VALUES
		(?, 'Obinna', 'owner@example.com', '+2348100000000', 'Obinna Sunday Uzorchukwu'),
		(?, 'Admin One', 'admin@example.com', '', '')`, owner.String(), admin.String()).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	reviewedAt := time.Now().UTC().Truncate(time.Second)
	reviewed := &models.PaymentMerchant{UserID: owner, Name: "Reviewed Ltd", Status: models.PaymentMerchantStatusActive,
		ReviewedBy: admin, ReviewedAt: &reviewedAt}
	unreviewed := &models.PaymentMerchant{UserID: uuid.New(), Name: "No Reviewer Ltd", Status: models.PaymentMerchantStatusActive}
	for _, pm := range []*models.PaymentMerchant{reviewed, unreviewed} {
		if err := db.Create(pm).Error; err != nil {
			t.Fatalf("create pm: %v", err)
		}
	}

	router := buildPMRouter(NewPMHandler(&fakePMAutomation{}, services.NewPMService(db)), testAdminUser())
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/pm", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []pmListItem `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	byName := map[string]pmListItem{}
	for _, it := range resp.Data {
		byName[it.Name] = it
	}

	got := byName["Reviewed Ltd"]
	if got.Owner == nil || got.Owner.Email != "owner@example.com" || got.Owner.LegalName != "Obinna Sunday Uzorchukwu" || got.Owner.PhoneNumber != "+2348100000000" {
		t.Errorf("owner = %+v", got.Owner)
	}
	if got.Reviewer == nil || got.Reviewer.Email != "admin@example.com" {
		t.Errorf("reviewer = %+v", got.Reviewer)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("createdAt/updatedAt missing: %v %v", got.CreatedAt, got.UpdatedAt)
	}
	if none := byName["No Reviewer Ltd"]; none.Reviewer != nil {
		t.Errorf("a merchant nobody reviewed must have no reviewer, got %+v", none.Reviewer)
	}
}
