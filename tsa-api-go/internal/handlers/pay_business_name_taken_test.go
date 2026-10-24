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
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// A business name another merchant already shows is a 409 with its own machine
// code on every route that can set one, so clients stop parsing the words.
func TestBusinessNameTaken_IsA409WithItsOwnCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, fail := range map[string]func(*gin.Context, error){"owner": payOwnerFail, "admin": payMerchantAdminFail} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPut, "/x", nil)
			fail(c, services.ErrPayBusinessNameTaken)
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", w.Code)
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code != services.BusinessNameTakenCode {
				t.Fatalf("code = %q (%v), want %q", body.Code, err, services.BusinessNameTakenCode)
			}
		})
	}
}

// The owner view says which part of the shown name is theirs and whether the
// business is verified, so the app can explain the legal name beside it.
func TestOwnerView_CarriesTheBaseNameAndVerification(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PayMerchantNameChange{}); err != nil {
		t.Fatal(err)
	}
	h := &PayHandler{merchants: services.NewPayMerchantService(db, nil, nil, []byte(strings.Repeat("k", 32)), time.Now)}
	m := &models.PayMerchant{ID: uuid.New(), BusinessName: "Ojay store - Ademu Emmanuel", BusinessBaseName: "Ojay store"}
	v := h.payOwnerView(m)
	if v.BusinessName != "Ojay store - Ademu Emmanuel" || v.BusinessBaseName != "Ojay store" || v.BusinessVerified {
		t.Fatalf("view = %+v", v)
	}
	// A merchant from before names were composed has no base yet: its name is its base.
	legacy := h.payOwnerView(&models.PayMerchant{ID: uuid.New(), BusinessName: "Sky Air"})
	if legacy.BusinessBaseName != "Sky Air" {
		t.Fatalf("legacy base = %q", legacy.BusinessBaseName)
	}
}
