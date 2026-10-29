package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// serviceCategory creates a Service category with an explicit service type.
func serviceCategory(t *testing.T, db *gorm.DB, serviceType string) *models.Category {
	t.Helper()
	cat := &models.Category{
		ID:          uuid.New(),
		Title:       "Cat " + uuid.New().String()[:8],
		Type:        models.CategoryTypeService,
		ServiceType: serviceType,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.Create(cat).Error; err != nil {
		t.Fatalf("failed to create category: %v", err)
	}
	return cat
}

// getServiceContact invokes GetServiceContact for a buyer viewing a service.
func getServiceContactResp(t *testing.T, h *ServiceContactHandler, buyer *models.User, serviceID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: serviceID.String()}}
	c.Request, _ = http.NewRequest("GET", "/", nil)
	h.GetServiceContact(c)
	return w
}

// The contact fee is a pay-to-unlock gate for contact services only. Order and
// booking services are fixed-price and must report as ungated (no fee) so the
// buyer flow never hits the 402 path.
func TestGetServiceContact_OrderBooking_NoFee(t *testing.T) {
	for _, serviceType := range []string{models.ServiceTypeOrder, models.ServiceTypeBooking} {
		t.Run(serviceType, func(t *testing.T) {
			db := setupCheckoutTestDB(t)
			h := &ServiceContactHandler{}

			buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
			seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
			cat := serviceCategory(t, db, serviceType)
			svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)

			w := getServiceContactResp(t, h, buyer, svc.ID)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
			var resp struct {
				Data struct {
					Paid      bool        `json:"paid"`
					FeeAmount string      `json:"feeAmount"`
					Contact   interface{} `json:"contact"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("bad json: %v", err)
			}
			if !resp.Data.Paid {
				t.Errorf("expected paid=true, got false: %s", w.Body.String())
			}
			if resp.Data.FeeAmount != "0" {
				t.Errorf("expected feeAmount \"0\", got %q", resp.Data.FeeAmount)
			}
			if resp.Data.Contact != nil {
				t.Errorf("expected contact nil for %s, got %v", serviceType, resp.Data.Contact)
			}
		})
	}
}

// A contact service must NOT be short-circuited by the guard: a buyer who has
// paid still gets the provider's contact details back.
func TestGetServiceContact_Contact_NotShortCircuited(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &ServiceContactHandler{}

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeContact)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)
	payContactFee(t, db, buyer.ID, svc.ID)

	w := getServiceContactResp(t, h, buyer, svc.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Paid    bool `json:"paid"`
			Contact *struct {
				Name string `json:"name"`
			} `json:"contact"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if !resp.Data.Paid {
		t.Fatalf("expected paid=true: %s", w.Body.String())
	}
	if resp.Data.Contact == nil || resp.Data.Contact.Name == "" {
		t.Errorf("expected provider contact details for contact service, got %s", w.Body.String())
	}
}

// One fee tx unlocks one contact: a hash that already paid (in any case) is a 409 before any chain
// read, so a caller cannot reuse someone else's — or their own — payment for another contact.
func TestSubmitContactFee_ReusedFeeTxIs409(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &ServiceContactHandler{}
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeContact)
	paid := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)
	other := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)
	if err := db.Create(&models.ServiceContactPayment{ID: uuid.New(), CallerID: buyer.ID, ServiceProviderID: seller.ID,
		ServiceID: paid.ID, Token: "USDC", FeeAmount: "100000", PayFeeTxHash: "0xABC", CreatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: other.ID.String()}}
	c.Request, _ = http.NewRequest("POST", "/", strings.NewReader(`{"approveTxHash":"0x1","payFeeTxHash":"0xabc"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.SubmitContactFee(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("reused fee tx: %d %s, want 409", w.Code, w.Body.String())
	}

	// The index is the backstop when two submits race past the pre-check.
	dup := models.ServiceContactPayment{ID: uuid.New(), CallerID: buyer.ID, ServiceProviderID: seller.ID,
		ServiceID: other.ID, PayFeeTxHash: "0xAbC", CreatedAt: time.Now()}
	if err := db.Create(&dup).Error; err == nil || !isDuplicateKeyErr(err) {
		t.Fatalf("second row for one fee tx: err=%v, want a unique violation", err)
	}
}
