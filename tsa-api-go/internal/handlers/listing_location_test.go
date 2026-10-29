package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func createListingAt(t *testing.T, cat models.Category, user *models.User, name string, fields url.Values) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{
		"name": {name}, "description": {"Rooms"}, "price": {"100"},
		"phoneNumber": {"0800"}, "email": {"m@example.com"}, "type": {"Service"},
		"category": {cat.ID.String()}, "listingValues": {`{"institution":"UNN"}`},
	}
	for k, v := range fields {
		form[k] = v
	}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/products", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user", user)
	(&Handlers{EventBus: events.NewBus()}).CreateProduct(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", name, w.Code, w.Body.String())
	}
	return w
}

func names(products []models.Product) string {
	out := make([]string, len(products))
	for i, p := range products {
		out[i] = p.Name
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestCreateProduct_StoresStructuredLocation(t *testing.T) {
	db, cat, user := setupListingValuesDB(t)

	createListingAt(t, cat, user, "picked", url.Values{
		"country": {"Nigeria"}, "state": {"Enugu State"}, "city": {"Nsukka"}, "area": {"Odenigbo"},
	})
	// An older app sends only the display string; it is parsed.
	createListingAt(t, cat, user, "legacy", url.Values{"location": {"Awka South, Anambra State, Nigeria"}})
	// One that cannot be split stays text only.
	createListingAt(t, cat, user, "free", url.Values{"location": {"Abuja Nigeria"}})

	var got []models.Product
	db.Where("category_id = ?", cat.ID).Order("name").Find(&got)
	byName := map[string]models.Product{}
	for _, p := range got {
		byName[p.Name] = p
	}
	if p := byName["picked"]; p.Location != "Nsukka, Enugu State, Nigeria" || p.StateKey != "enugu" || p.Area != "Odenigbo" {
		t.Errorf("picked: %+v", p)
	}
	if p := byName["legacy"]; p.Location != "Awka South, Anambra State, Nigeria" || p.City != "Awka South" || p.StateKey != "anambra" {
		t.Errorf("legacy: %+v", p)
	}
	if p := byName["free"]; p.Location != "Abuja Nigeria" || p.CountryKey != "" {
		t.Errorf("free: %+v", p)
	}
}

func TestGetProductsByCategory_FiltersOnLocation(t *testing.T) {
	_, cat, user := setupListingValuesDB(t)
	createListingAt(t, cat, user, "nsukka", url.Values{"country": {"Nigeria"}, "state": {"Enugu State"}, "city": {"Nsukka"}, "area": {"Odenigbo"}})
	createListingAt(t, cat, user, "enugu-east", url.Values{"location": {"Enugu East, Enugu State, Nigeria"}})
	createListingAt(t, cat, user, "kubwa", url.Values{"location": {"Kubwa extension, Federal Capital Territory, Nigeria"}})
	createListingAt(t, cat, user, "douala", url.Values{"location": {"Douala, Littoral, Cameroon"}})

	cases := map[string]string{
		"state=Enugu":                         "enugu-east,nsukka",
		"state=enugu%20state":                 "enugu-east,nsukka",
		"state=Enugu&city=NSUKKA":             "nsukka",
		"country=Cameroon":                    "douala",
		"area=kubwa":                          "kubwa",
		"area=odenigbo":                       "nsukka",
		"state=Federal%20Capital%20Territory": "kubwa",
		// LIKE metacharacters match only themselves.
		"area=%25":                    "",
		"area=_":                      "",
		"country=Nigeria&state=Lagos": "",
	}
	for query, want := range cases {
		code, products := listByCategory(t, cat, query)
		if code != http.StatusOK || names(products) != want {
			t.Errorf("%s: %d %q, want %q", query, code, names(products), want)
		}
	}
}

func TestUpdateProduct_LocationReplacesEveryPart(t *testing.T) {
	db, cat, user := setupListingValuesDB(t)
	createListingAt(t, cat, user, "moving", url.Values{"country": {"Nigeria"}, "state": {"Enugu State"}, "city": {"Nsukka"}, "area": {"Odenigbo"}})
	var p models.Product
	db.Where("name = ?", "moving").First(&p)

	form := url.Values{"location": {"Abuja Nigeria"}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/products/"+p.ID.String(), strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Params = gin.Params{{Key: "id", Value: p.ID.String()}}
	c.Set("user", user)
	(&Handlers{EventBus: events.NewBus()}).UpdateProduct(c)
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	db.First(&p, "id = ?", p.ID)
	if p.Location != "Abuja Nigeria" || p.StateKey != "" || p.CityKey != "" || p.Area != "" {
		t.Fatalf("stale parts kept: %+v", p)
	}
}

func TestBackfillListingLocations_FillsOnlyUnsetParseableRows(t *testing.T) {
	db, cat, user := setupListingValuesDB(t)
	createListingAt(t, cat, user, "picked", url.Values{"country": {"Nigeria"}, "state": {"Enugu State"}, "city": {"Nsukka"}})
	// Rows an older release wrote: display string only.
	for name, loc := range map[string]string{"old": "Mushin, Lagos State, Nigeria", "text": "Enugu Nigeria"} {
		p := models.Product{UserID: user.ID, Name: name, Location: loc, CategoryID: &cat.ID, Type: "Service", Status: models.ProductStatusActive}
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Model(&models.Product{}).Where("name = ?", "picked").Update("area", "Odenigbo")

	n, err := models.BackfillListingLocations(db)
	if err != nil || n != 1 {
		t.Fatalf("first backfill: n=%d err=%v", n, err)
	}
	if n, _ := models.BackfillListingLocations(db); n != 0 {
		t.Fatalf("second backfill touched %d rows", n)
	}
	var old, text, picked models.Product
	db.Where("name = ?", "old").First(&old)
	db.Where("name = ?", "text").First(&text)
	db.Where("name = ?", "picked").First(&picked)
	if old.StateKey != "lagos" || old.City != "Mushin" || old.Location != "Mushin, Lagos State, Nigeria" {
		t.Errorf("old: %+v", old)
	}
	if text.CountryKey != "" || text.Location != "Enugu Nigeria" {
		t.Errorf("text: %+v", text)
	}
	if picked.Area != "Odenigbo" {
		t.Errorf("picked area rewritten: %+v", picked)
	}
}
