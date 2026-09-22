package handlers

import (
	"bytes"
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

func validAddressBody() map[string]interface{} {
	return map[string]interface{}{
		"label":         "Home",
		"recipientName": "Ada Obi",
		"phone":         "+2348012345678",
		"street":        "12 Allen Avenue",
		"city":          "Ikeja",
		"state":         "Lagos",
		"country":       "Nigeria",
	}
}

func callAddressHandler(t *testing.T, user *models.User, method, id string, body interface{}, fn func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	var buf []byte
	if body != nil {
		buf, _ = json.Marshal(body)
	}
	c.Request, _ = http.NewRequest(method, "/", bytes.NewReader(buf))
	c.Request.Header.Set("Content-Type", "application/json")
	if id != "" {
		c.Params = gin.Params{{Key: "id", Value: id}}
	}
	fn(c)
	return w
}

func decodeAddress(t *testing.T, w *httptest.ResponseRecorder) models.DeliveryAddress {
	t.Helper()
	var resp struct {
		Data models.DeliveryAddress `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	return resp.Data
}

func listAddresses(t *testing.T, db *gorm.DB, userID uuid.UUID) []models.DeliveryAddress {
	t.Helper()
	var rows []models.DeliveryAddress
	db.Where("user_id = ?", userID).Order("created_at ASC").Find(&rows)
	return rows
}

func TestCreateDeliveryAddress_FirstBecomesDefault(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}
	user := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")

	w := callAddressHandler(t, user, "POST", "", validAddressBody(), h.CreateDeliveryAddress)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	got := decodeAddress(t, w)
	if !got.IsDefault || got.UserID != user.ID || got.Street != "12 Allen Avenue" {
		t.Errorf("unexpected address: %+v", got)
	}
}

func TestCreateDeliveryAddress_RejectsMissingFields(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}
	user := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")

	for _, field := range []string{"recipientName", "phone", "street", "city", "state", "country"} {
		body := validAddressBody()
		body[field] = "   "
		w := callAddressHandler(t, user, "POST", "", body, h.CreateDeliveryAddress)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s blank: want 400, got %d", field, w.Code)
		}
	}
	if n := len(listAddresses(t, db, user.ID)); n != 0 {
		t.Errorf("want nothing saved, got %d", n)
	}
}

func TestCreateDeliveryAddress_NewDefaultClearsOld(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}
	user := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")

	callAddressHandler(t, user, "POST", "", validAddressBody(), h.CreateDeliveryAddress)
	time.Sleep(time.Millisecond)
	second := validAddressBody()
	second["label"] = "Office"
	second["isDefault"] = true
	callAddressHandler(t, user, "POST", "", second, h.CreateDeliveryAddress)

	rows := listAddresses(t, db, user.ID)
	if len(rows) != 2 || rows[0].IsDefault || !rows[1].IsDefault {
		t.Fatalf("want only the newest default, got %+v", rows)
	}
}

func TestCreateDeliveryAddress_Cap(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}
	user := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	for i := 0; i < maxDeliveryAddresses; i++ {
		db.Create(&models.DeliveryAddress{ID: uuid.New(), UserID: user.ID, RecipientName: "A", Phone: "1", Street: "s", City: "c", State: "s", Country: "n"})
	}
	w := callAddressHandler(t, user, "POST", "", validAddressBody(), h.CreateDeliveryAddress)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 at the cap, got %d", w.Code)
	}
}

func TestListDeliveryAddresses_OnlyOwnDefaultFirst(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}
	user := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	other := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")

	callAddressHandler(t, user, "POST", "", validAddressBody(), h.CreateDeliveryAddress) // default
	time.Sleep(time.Millisecond)
	callAddressHandler(t, user, "POST", "", validAddressBody(), h.CreateDeliveryAddress)
	callAddressHandler(t, other, "POST", "", validAddressBody(), h.CreateDeliveryAddress)

	w := callAddressHandler(t, user, "GET", "", nil, h.ListDeliveryAddresses)
	var resp struct {
		Data []models.DeliveryAddress `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 2 {
		t.Fatalf("want 2 own addresses, got %d", len(resp.Data))
	}
	if !resp.Data[0].IsDefault {
		t.Errorf("want default first, got %+v", resp.Data)
	}
}

func TestUpdateDeliveryAddress_OwnerOnly(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}
	user := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	other := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	created := decodeAddress(t, callAddressHandler(t, user, "POST", "", validAddressBody(), h.CreateDeliveryAddress))

	body := validAddressBody()
	body["street"] = "5 New Road"
	w := callAddressHandler(t, other, "PUT", created.ID.String(), body, h.UpdateDeliveryAddress)
	if w.Code != http.StatusNotFound {
		t.Fatalf("other user: want 404, got %d", w.Code)
	}

	w = callAddressHandler(t, user, "PUT", created.ID.String(), body, h.UpdateDeliveryAddress)
	if w.Code != http.StatusOK {
		t.Fatalf("owner: want 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := decodeAddress(t, w); got.Street != "5 New Road" || !got.IsDefault {
		t.Errorf("want updated street and default kept, got %+v", got)
	}
}

func TestDeleteDeliveryAddress_PromotesNextDefault(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}
	user := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	first := decodeAddress(t, callAddressHandler(t, user, "POST", "", validAddressBody(), h.CreateDeliveryAddress))
	time.Sleep(time.Millisecond)
	second := decodeAddress(t, callAddressHandler(t, user, "POST", "", validAddressBody(), h.CreateDeliveryAddress))

	other := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	if w := callAddressHandler(t, other, "DELETE", first.ID.String(), nil, h.DeleteDeliveryAddress); w.Code != http.StatusNotFound {
		t.Fatalf("other user: want 404, got %d", w.Code)
	}

	if w := callAddressHandler(t, user, "DELETE", first.ID.String(), nil, h.DeleteDeliveryAddress); w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	rows := listAddresses(t, db, user.ID)
	if len(rows) != 1 || rows[0].ID != second.ID || !rows[0].IsDefault {
		t.Fatalf("want the remaining address promoted to default, got %+v", rows)
	}
}

// With the one-default index in place, switching the default must clear the
// old one before setting the new one — on create and on update alike.
func TestUpdateDeliveryAddress_MakeDefaultSwitches(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}
	user := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	first := decodeAddress(t, callAddressHandler(t, user, "POST", "", validAddressBody(), h.CreateDeliveryAddress))
	time.Sleep(time.Millisecond)
	second := decodeAddress(t, callAddressHandler(t, user, "POST", "", validAddressBody(), h.CreateDeliveryAddress))

	body := validAddressBody()
	body["isDefault"] = true
	if w := callAddressHandler(t, user, "PUT", second.ID.String(), body, h.UpdateDeliveryAddress); w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var oldDefault, newDefault models.DeliveryAddress
	db.First(&oldDefault, "id = ?", first.ID)
	db.First(&newDefault, "id = ?", second.ID)
	if oldDefault.IsDefault || !newDefault.IsDefault {
		t.Errorf("want the default moved: old=%v new=%v", oldDefault.IsDefault, newDefault.IsDefault)
	}
}
