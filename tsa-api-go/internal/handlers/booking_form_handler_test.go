package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

func writeFieldConfig(t *testing.T, db *gorm.DB, productID uuid.UUID, cfg *models.BookingFieldConfig) {
	t.Helper()
	profile := models.ServiceProfile{
		ID:            uuid.New(),
		ProductID:     productID,
		BookingType:   models.BookingTypeInstant,
		PricingMethod: models.PricingMethodFixed,
		Workflow:      models.WorkflowAutoAccept,
		PaymentTiming: models.PaymentTimingBefore,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	profile.SetFieldConfig(cfg)
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}
}

func getFormSchema(t *testing.T, h *Handlers, serviceID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: serviceID.String()}}
	c.Request, _ = http.NewRequest("GET", "/", nil)
	h.GetBookingFormSchema(c)
	return w
}

// The form-schema endpoint returns the merchant's enabled fields plus the
// workflow/pricing/payment dimensions so the app can auto-generate the form.
func TestGetBookingFormSchema(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)
	writeFieldConfig(t, db, svc.ID, &models.BookingFieldConfig{Fields: []models.BookingField{
		{Key: "checkin", Type: models.BookingFieldDate, Label: "Check-in", Required: true},
		{Key: "guests", Type: models.BookingFieldCounter, Label: "Guests", Required: true},
	}})

	w := getFormSchema(t, h, svc.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			ServiceType   string `json:"serviceType"`
			BookingType   string `json:"bookingType"`
			PricingMethod string `json:"pricingMethod"`
			Fields        []struct {
				Key      string `json:"key"`
				Required bool   `json:"required"`
			} `json:"fields"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp.Data.ServiceType != models.ServiceTypeBooking {
		t.Errorf("expected booking, got %s", resp.Data.ServiceType)
	}
	if resp.Data.BookingType != models.BookingTypeInstant || resp.Data.PricingMethod != models.PricingMethodFixed {
		t.Errorf("unexpected defaults: %+v", resp.Data)
	}
	if len(resp.Data.Fields) != 2 || resp.Data.Fields[0].Key != "checkin" {
		t.Errorf("expected 2 fields starting with checkin, got %+v", resp.Data.Fields)
	}
}

// A service with no FieldConfig returns an empty field list (client falls back
// to its legacy per-serviceType flow) and default dimensions.
func TestGetBookingFormSchema_NoConfig(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)

	w := getFormSchema(t, h, svc.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			BookingType string        `json:"bookingType"`
			Fields      []interface{} `json:"fields"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.BookingType != models.BookingTypeInstant {
		t.Errorf("expected default instant, got %s", resp.Data.BookingType)
	}
	if len(resp.Data.Fields) != 0 {
		t.Errorf("expected no fields, got %d", len(resp.Data.Fields))
	}
}

// CreateServiceOrder validates submitted answers against the field config and
// rejects a missing required answer; a complete submission is stored on the order.
func TestCreateServiceOrder_ValidatesAnswers(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)
	writeFieldConfig(t, db, svc.ID, &models.BookingFieldConfig{Fields: []models.BookingField{
		{Key: "checkin", Type: models.BookingFieldDate, Label: "Check-in", Required: true},
	}})

	// Missing required answer → 400.
	if w := postServiceOrder(t, ch, buyer, map[string]interface{}{"productId": svc.ID.String()}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing answer, got %d: %s", w.Code, w.Body.String())
	}

	// Complete answer → 201, stored on the order.
	w := postServiceOrder(t, ch, buyer, map[string]interface{}{
		"productId": svc.ID.String(),
		"answers":   map[string]interface{}{"checkin": "2026-08-01"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var order models.Order
	if err := db.Where("buyer_id = ?", buyer.ID).First(&order).Error; err != nil {
		t.Fatalf("order not found: %v", err)
	}
	if got := order.GetServiceAnswers()["checkin"]; got != "2026-08-01" {
		t.Errorf("expected stored checkin answer, got %v", got)
	}
}
