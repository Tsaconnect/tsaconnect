package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

// hostelCategory is a Student-hostel-like category whose listing form asks
// for a required, filterable institution and a filterable gender.
func setupListingValuesDB(t *testing.T) (*gorm.DB, models.Category, *models.User) {
	t.Helper()
	db := testutil.TempPostgres(t, "listing_values_",
		&models.User{}, &models.Category{}, &models.FieldDef{}, &models.Product{})
	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })
	if err := models.SeedFieldDefs(db); err != nil {
		t.Fatalf("seed field defs: %v", err)
	}
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_products_listing_values
		ON products USING GIN (listing_values) WHERE listing_values IS NOT NULL`).Error; err != nil {
		t.Fatalf("gin index: %v", err)
	}

	fields, _ := json.Marshal([]models.CategoryFieldRef{
		{Key: "institution", Required: true, Filterable: true, Options: []string{"UNN", "UNILAG"}},
		{Key: "gender", Filterable: true},
	})
	cat := models.Category{
		Title: "Student hostel " + uuid.NewString()[:6], Type: models.CategoryTypeService,
		ServiceType: models.ServiceTypeContact, IsActive: true, ListingFields: datatypes.JSON(fields),
	}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatalf("category: %v", err)
	}
	user := &models.User{Name: "Merchant", Username: "m_" + uuid.NewString()[:8],
		Email: uuid.NewString()[:8] + "@example.com", Password: "x", Role: "merchant"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	return db, cat, user
}

func createListing(t *testing.T, cat models.Category, user *models.User, listingValues string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{
		"name": {"Hostel"}, "description": {"Rooms"}, "price": {"100"}, "location": {"Nsukka"},
		"phoneNumber": {"0800"}, "email": {"m@example.com"}, "type": {"Service"},
		"category": {cat.ID.String()},
	}
	if listingValues != "" {
		form.Set("listingValues", listingValues)
	}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/products", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user", user)
	(&Handlers{EventBus: events.NewBus()}).CreateProduct(c)
	return w
}

func listByCategory(t *testing.T, cat models.Category, query string) (int, []models.Product) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/products/public/category?categoryId="+cat.ID.String()+"&"+query, nil)
	(&Handlers{}).GetProductsByCategory(c)
	var body struct {
		Data struct {
			Products []models.Product `json:"products"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body.Data.Products
}

func TestCreateProduct_ListingValuesValidatedAgainstTheCategoryForm(t *testing.T) {
	db, cat, user := setupListingValuesDB(t)

	if w := createListing(t, cat, user, `{"gender":"male"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("missing required institution: got %d %s", w.Code, w.Body.String())
	}
	if w := createListing(t, cat, user, `{"institution":"UI"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("institution outside the category's options: got %d", w.Code)
	}
	if w := createListing(t, cat, user, `not json`); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed listingValues: got %d", w.Code)
	}
	w := createListing(t, cat, user, `{"institution":"UNN","gender":"female","gone_field":"x"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("valid values: got %d %s", w.Code, w.Body.String())
	}
	var p models.Product
	if err := db.Where("category_id = ?", cat.ID).First(&p).Error; err != nil {
		t.Fatal(err)
	}
	var stored map[string]interface{}
	_ = json.Unmarshal(p.ListingValues, &stored)
	if stored["institution"] != "UNN" || stored["gender"] != "female" || stored["gone_field"] != nil {
		t.Fatalf("stored %v", stored)
	}

	// An app that does not send listingValues still lists (older versions).
	if w := createListing(t, cat, user, ""); w.Code != http.StatusCreated {
		t.Fatalf("no listingValues: got %d %s", w.Code, w.Body.String())
	}
}

func TestGetProductsByCategory_FiltersOnFilterableListingValues(t *testing.T) {
	_, cat, user := setupListingValuesDB(t)
	for _, v := range []string{
		`{"institution":"UNN","gender":"female"}`,
		`{"institution":"UNN","gender":"male"}`,
		`{"institution":"UNILAG","gender":"female"}`,
	} {
		if w := createListing(t, cat, user, v); w.Code != http.StatusCreated {
			t.Fatalf("seed %s: %d %s", v, w.Code, w.Body.String())
		}
	}

	cases := []struct {
		query string
		want  int
	}{
		{"", 3},
		{"attr[institution]=UNN", 2},
		{"attr[institution]=UNN&attr[gender]=female", 1},
		{"attr[institution]=UI", 0},
		{"attr[made_up]=x", 3}, // not on the form: ignored, never an error
		{"attr[institution]=UNN'%20OR%20'1'='1", 0},
	}
	for _, tc := range cases {
		code, products := listByCategory(t, cat, tc.query)
		if code != http.StatusOK || len(products) != tc.want {
			t.Errorf("%q: got %d listings (HTTP %d), want %d", tc.query, len(products), code, tc.want)
		}
	}
}
