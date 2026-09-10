package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// The profile PUT merges: a field the merchant did not send keeps its stored
// value. Assigning unconditionally made omission destructive, and the worst
// case was silent — MaxCapacityPerSlot 0 means "unlimited", so a body that left
// it out removed the listing's capacity limit without saying anything.

func putProfile(t *testing.T, h *Handlers, seller *models.User, productID uuid.UUID, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", seller)
	c.Params = gin.Params{{Key: "id", Value: productID.String()}}
	raw, _ := json.Marshal(body)
	c.Request, _ = http.NewRequest("PUT", "/", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpsertServiceProfile(c)
	return w
}

func loadProfile(t *testing.T, db *gorm.DB, productID uuid.UUID) models.ServiceProfile {
	t.Helper()
	var p models.ServiceProfile
	if err := db.Where("product_id = ?", productID).First(&p).Error; err != nil {
		t.Fatalf("failed to load profile: %v", err)
	}
	return p
}

// A partial update must leave every omitted field alone.
func TestUpsertServiceProfile_PartialUpdatePreservesOmittedFields(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	// Full configuration first.
	if w := putProfile(t, h, seller, svc.ID, map[string]interface{}{
		"slotDurationMinutes": 60,
		"maxCapacityPerSlot":  4,
		"accessFeeUsd":        2.5,
		"calloutFeeUsd":       1.5,
		"bookingFeeUsd":       3.0,
		"serviceAddress":      "12 Adeola Odeku",
		"coverageArea":        "Lagos Island",
		"cancellationPolicy":  "24h notice",
		"refundPolicy":        "50% within 24h",
		"timezone":            "Africa/Lagos",
	}); w.Code != http.StatusOK {
		t.Fatalf("initial save: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Now change one unrelated field only.
	if w := putProfile(t, h, seller, svc.ID, map[string]interface{}{
		"coverageArea": "Lagos Mainland",
	}); w.Code != http.StatusOK {
		t.Fatalf("partial save: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	got := loadProfile(t, db, svc.ID)
	if got.CoverageArea != "Lagos Mainland" {
		t.Errorf("coverageArea: want the updated value, got %q", got.CoverageArea)
	}
	if got.MaxCapacityPerSlot != 4 {
		t.Errorf("maxCapacityPerSlot: want 4 preserved, got %d (0 would silently mean unlimited)", got.MaxCapacityPerSlot)
	}
	if got.SlotDurationMinutes != 60 {
		t.Errorf("slotDurationMinutes: want 60 preserved, got %d (0 would disable booking)", got.SlotDurationMinutes)
	}
	if got.AccessFeeUSD != 2.5 {
		t.Errorf("accessFeeUsd: want 2.5 preserved, got %v", got.AccessFeeUSD)
	}
	if got.CalloutFeeUSD != 1.5 {
		t.Errorf("calloutFeeUsd: want 1.5 preserved, got %v", got.CalloutFeeUSD)
	}
	if got.BookingFeeUSD != 3.0 {
		t.Errorf("bookingFeeUsd: want 3.0 preserved, got %v", got.BookingFeeUSD)
	}
	if got.ServiceAddress != "12 Adeola Odeku" {
		t.Errorf("serviceAddress: want preserved, got %q", got.ServiceAddress)
	}
	if got.CancellationPolicy != "24h notice" {
		t.Errorf("cancellationPolicy: want preserved, got %q", got.CancellationPolicy)
	}
	if got.RefundPolicy != "50% within 24h" {
		t.Errorf("refundPolicy: want preserved, got %q", got.RefundPolicy)
	}
}

// Merging must not make a field unclearable: an explicitly sent zero or empty
// string still writes, so a merchant can remove a fee or a policy.
func TestUpsertServiceProfile_ExplicitZeroStillClears(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	if w := putProfile(t, h, seller, svc.ID, map[string]interface{}{
		"accessFeeUsd":       2.5,
		"cancellationPolicy": "24h notice",
	}); w.Code != http.StatusOK {
		t.Fatalf("initial save: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w := putProfile(t, h, seller, svc.ID, map[string]interface{}{
		"accessFeeUsd":       0,
		"cancellationPolicy": "",
	}); w.Code != http.StatusOK {
		t.Fatalf("clearing save: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	got := loadProfile(t, db, svc.ID)
	if got.AccessFeeUSD != 0 {
		t.Errorf("accessFeeUsd: want cleared to 0, got %v", got.AccessFeeUSD)
	}
	if got.CancellationPolicy != "" {
		t.Errorf("cancellationPolicy: want cleared, got %q", got.CancellationPolicy)
	}
}

// The booking form is the most destructive thing an omitted key used to clear:
// a profile save that left fieldConfig out wiped the merchant's whole form and
// dropped the listing back to the legacy flow. Omission now preserves, and an
// explicit empty Fields list is how it is cleared on purpose.
func TestUpsertServiceProfile_FieldConfigPreservedUnlessSent(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	if w := putProfile(t, h, seller, svc.ID, map[string]interface{}{
		"fieldConfig": map[string]interface{}{
			"fields": []map[string]interface{}{
				{"key": "departure", "type": "location", "label": "From", "required": true},
				{"key": "passengers", "type": "counter", "label": "Passengers", "required": true},
			},
		},
	}); w.Code != http.StatusOK {
		t.Fatalf("initial save: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// An unrelated edit must not disturb the form.
	if w := putProfile(t, h, seller, svc.ID, map[string]interface{}{
		"coverageArea": "Lagos Island",
	}); w.Code != http.StatusOK {
		t.Fatalf("partial save: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	saved := loadProfile(t, db, svc.ID)
	cfg := saved.GetFieldConfig()
	if cfg == nil || len(cfg.Fields) != 2 {
		t.Fatalf("want the 2 configured fields preserved, got %+v", cfg)
	}

	// Explicitly emptying the field list clears it.
	if w := putProfile(t, h, seller, svc.ID, map[string]interface{}{
		"fieldConfig": map[string]interface{}{"fields": []map[string]interface{}{}},
	}); w.Code != http.StatusOK {
		t.Fatalf("clearing save: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	cleared := loadProfile(t, db, svc.ID)
	if cfg := cleared.GetFieldConfig(); cfg != nil {
		t.Errorf("want the form cleared, got %+v", cfg)
	}
}

// Merging preserves a stored slot duration, so switching a slotted listing to
// request mode now collides with the request-plus-slot rule. That refusal is
// correct — silently wiping the slot config is what the merge fix removed — but
// the merchant has to be told which field to clear, and clearing it has to work.
func TestUpsertServiceProfile_SwitchToRequestModeIsActionable(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	if w := putProfile(t, h, seller, svc.ID, map[string]interface{}{
		"bookingType":         models.BookingTypeInstant,
		"slotDurationMinutes": 60,
	}); w.Code != http.StatusOK {
		t.Fatalf("initial save: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Switching mode alone collides with the preserved slot duration.
	w := putProfile(t, h, seller, svc.ID, map[string]interface{}{
		"bookingType": models.BookingTypeRequest,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "slot duration") {
		t.Errorf("error must name the field to clear, got %s", w.Body.String())
	}

	// Clearing it explicitly is the documented way through.
	if w := putProfile(t, h, seller, svc.ID, map[string]interface{}{
		"bookingType":         models.BookingTypeRequest,
		"slotDurationMinutes": 0,
	}); w.Code != http.StatusOK {
		t.Fatalf("clearing the slot duration should allow the switch, got %d: %s", w.Code, w.Body.String())
	}
	got := loadProfile(t, db, svc.ID)
	if got.BookingType != models.BookingTypeRequest || got.SlotDurationMinutes != 0 {
		t.Errorf("want request mode with no slot duration, got %q / %d", got.BookingType, got.SlotDurationMinutes)
	}
}

// An unloadable timezone is rejected at write time rather than silently
// falling back at read time, so a typo surfaces to the merchant setting it.
func TestUpsertServiceProfile_RejectsUnknownTimezone(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	w := putProfile(t, h, seller, svc.ID, map[string]interface{}{"timezone": "Africa/Lagoss"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown timezone, got %d: %s", w.Code, w.Body.String())
	}
}

// A new profile with no timezone in the body still gets the platform default,
// so slot maths never runs against an empty zone.
func TestUpsertServiceProfile_DefaultsTimezone(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	if w := putProfile(t, h, seller, svc.ID, map[string]interface{}{"slotDurationMinutes": 30}); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := loadProfile(t, db, svc.ID); got.Timezone != models.DefaultServiceTimezone {
		t.Errorf("timezone: want %q, got %q", models.DefaultServiceTimezone, got.Timezone)
	}
}
