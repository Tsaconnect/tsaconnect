package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// setupCategoryConfigDB is the checkout schema plus a seeded field catalogue.
func setupCategoryConfigDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupCheckoutTestDB(t)
	if err := db.Exec(`CREATE TABLE field_defs (id TEXT PRIMARY KEY, key TEXT NOT NULL UNIQUE, type TEXT NOT NULL,
		label TEXT NOT NULL, field_group TEXT, options TEXT, scope TEXT NOT NULL, is_system INTEGER NOT NULL DEFAULT 0,
		is_active INTEGER NOT NULL DEFAULT 1, created_by TEXT, created_at DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := models.SeedFieldDefs(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// formCtx builds a gin context with a urlencoded form body (category writes
// are form posts), optional params and user.
func formCtx(method string, form url.Values, params gin.Params, user *models.User) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(method, "/", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Params = params
	if user != nil {
		c.Set("user", user)
	}
	return c, w
}

func jsonCtx(method, body string, params gin.Params, user *models.User) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(method, "/", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	if user != nil {
		c.Set("user", user)
	}
	return c, w
}

func catParam(id uuid.UUID) gin.Params { return gin.Params{{Key: "categoryId", Value: id.String()}} }

func updateCategoryForm(t *testing.T, user *models.User, id uuid.UUID, form url.Values) (int, string) {
	t.Helper()
	c, w := formCtx(http.MethodPut, form, catParam(id), user)
	(&Handlers{}).UpdateCategory(c)
	return w.Code, w.Body.String()
}

func setCategoryColumn(t *testing.T, db *gorm.DB, id uuid.UUID, column string, value interface{}) {
	t.Helper()
	if err := db.Model(&models.Category{}).Where("id = ?", id).UpdateColumn(column, value).Error; err != nil {
		t.Fatal(err)
	}
}

func reloadCategory(t *testing.T, db *gorm.DB, id uuid.UUID) models.Category {
	t.Helper()
	var cat models.Category
	if err := db.First(&cat, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return cat
}

func fieldDefByKey(t *testing.T, db *gorm.DB, key string) models.FieldDef {
	t.Helper()
	var d models.FieldDef
	if err := db.First(&d, "key = ?", key).Error; err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCategoryConfigWrites_RefuseNonAdmins(t *testing.T) {
	for _, role := range []string{models.RoleMerchant, models.RoleSupport, models.RoleUser} {
		t.Run(role, func(t *testing.T) {
			db := setupCategoryConfigDB(t)
			user := seedUser(t, db, role)
			cat := serviceCategory(t, db, models.ServiceTypeBooking)
			brand := fieldDefByKey(t, db, "brand")
			idParam := gin.Params{{Key: "id", Value: brand.ID.String()}}
			h := &Handlers{}

			calls := map[string]func() int{
				"AdminListFieldDefs": func() int {
					c, w := jsonCtx(http.MethodGet, "", nil, user)
					h.AdminListFieldDefs(c)
					return w.Code
				},
				"CreateFieldDef": func() int {
					c, w := jsonCtx(http.MethodPost, `{"key":"pets","type":"boolean","label":"Pets","scope":"listing"}`, nil, user)
					h.CreateFieldDef(c)
					return w.Code
				},
				"UpdateFieldDef": func() int {
					c, w := jsonCtx(http.MethodPut, `{"label":"Hacked","isActive":false}`, idParam, user)
					h.UpdateFieldDef(c)
					return w.Code
				},
				"DeleteFieldDef": func() int {
					c, w := jsonCtx(http.MethodDelete, "", idParam, user)
					h.DeleteFieldDef(c)
					return w.Code
				},
				"CreateCategory": func() int {
					c, w := formCtx(http.MethodPost, url.Values{"title": {"Hostels"}, "type": {"Service"},
						"bookingMode": {"date_range"}}, nil, user)
					h.CreateCategory(c)
					return w.Code
				},
				"UpdateCategory": func() int {
					code, _ := updateCategoryForm(t, user, cat.ID, url.Values{
						"bookingMode":   {"quantity"},
						"bookingFields": {`[{"key":"guests","required":true}]`},
					})
					return code
				},
			}
			for name, call := range calls {
				if code := call(); code != http.StatusForbidden {
					t.Errorf("%s by a %s: status %d, want 403", name, role, code)
				}
			}

			if got := fieldDefByKey(t, db, "brand"); got.Label != "Brand" || !got.IsActive {
				t.Errorf("a %s changed a field def: %+v", role, got)
			}
			var n int64
			db.Model(&models.FieldDef{}).Where("key = ?", "pets").Count(&n)
			if n != 0 {
				t.Errorf("a %s created a field def", role)
			}
			if got := reloadCategory(t, db, cat.ID); got.BookingMode != nil {
				t.Errorf("a %s changed category config", role)
			}
			db.Model(&models.Category{}).Where("title = ?", "Hostels").Count(&n)
			if n != 0 {
				t.Errorf("a %s created a category", role)
			}
		})
	}
}

func TestUpdateCategory_ValidatesConfig(t *testing.T) {
	db := setupCategoryConfigDB(t)
	admin := seedUser(t, db, models.RoleAdmin)
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	db.Model(&models.FieldDef{}).Where("key = ?", "children").Update("is_active", false)

	bad := []url.Values{
		{"listingFields": {`[{"key":"made_up","required":true}]`}},     // unknown
		{"bookingFields": {`[{"key":"children"}]`}},                    // inactive
		{"listingFields": {`[{"key":"checkin"}]`}},                     // booking-only on listing
		{"bookingFields": {`[{"key":"brand"}]`}},                       // listing-only on booking
		{"listingFields": {`[{"key":"brand","options":["x"]}]`}},       // options on text
		{"bookingFields": {`[{"key":"guests","filterable":true}]`}},    // filterable on booking
		{"listingFields": {`[{"key":"brand","colour":"red"}]`}},        // unknown property
		{"listingFields": {`{"key":"brand"}`}},                         // not an array
		{"bookingMode": {"hourly"}},                                    // unknown mode
		{"priceUnits": {`["per_week"]`}},                               // unknown unit
		{"slug": {"Not A Slug"}},                                       // bad slug
		{"listingFields": {`[{"key":"institution","required":true}]`}}, // select with no options anywhere
	}
	for _, form := range bad {
		if code, body := updateCategoryForm(t, admin, cat.ID, form); code != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400: %s", form, code, body)
		}
	}
	if got := reloadCategory(t, db, cat.ID); got.BookingMode != nil || !(len(got.ListingFields) == 0 || string(got.ListingFields) == "null") {
		t.Fatalf("a refused write changed the category: %+v", got)
	}

	code, body := updateCategoryForm(t, admin, cat.ID, url.Values{
		"bookingMode":   {"date_range"},
		"priceUnits":    {`["per_night","per_month"]`},
		"listingFields": {`[{"key":"institution","required":true,"filterable":true,"options":["UNILAG","UI"]},{"key":"gender","filterable":true},{"key":"room_type","options":["single"]}]`},
		"bookingFields": {`[{"key":"checkin","required":true},{"key":"checkout","required":true}]`},
		"slug":          {"student-hostel"},
	})
	if code != http.StatusOK {
		t.Fatalf("valid config refused: %d %s", code, body)
	}
	got := reloadCategory(t, db, cat.ID)
	if got.BookingMode == nil || *got.BookingMode != models.BookingModeDateRange || got.Slug == nil || *got.Slug != "student-hostel" {
		t.Fatalf("config not stored: %+v", got)
	}
	if units, ok := got.GetPriceUnits(); !ok || len(units) != 2 {
		t.Fatalf("price units not stored: %s", got.PriceUnits)
	}
	if refs, ok := got.GetListingFields(); !ok || len(refs) != 3 || !refs[0].Filterable {
		t.Fatalf("listing fields not stored: %s", got.ListingFields)
	}

	// "null" clears back to inherit.
	if code, body := updateCategoryForm(t, admin, cat.ID, url.Values{"bookingMode": {"null"}, "listingFields": {"null"}}); code != http.StatusOK {
		t.Fatalf("clear refused: %d %s", code, body)
	}
	got = reloadCategory(t, db, cat.ID)
	if _, ok := got.GetListingFields(); ok || got.BookingMode != nil {
		t.Fatalf("null did not clear: mode=%v listing=%s", got.BookingMode, got.ListingFields)
	}
	if _, ok := got.GetBookingFields(); !ok {
		t.Fatal("an untouched column was cleared")
	}
}

func TestUpdateCategory_RefusesParentCycle(t *testing.T) {
	db := setupCategoryConfigDB(t)
	admin := seedUser(t, db, models.RoleAdmin)
	root := serviceCategory(t, db, models.ServiceTypeBooking)
	mid := serviceCategory(t, db, models.ServiceTypeBooking)
	leaf := serviceCategory(t, db, models.ServiceTypeBooking)
	setCategoryColumn(t, db, mid.ID, "parent_category_id", root.ID)
	setCategoryColumn(t, db, leaf.ID, "parent_category_id", mid.ID)

	code, body := updateCategoryForm(t, admin, root.ID, url.Values{"parentCategory": {leaf.ID.String()}})
	if code != http.StatusBadRequest || !strings.Contains(body, "category_cycle") {
		t.Fatalf("cycle accepted: %d %s", code, body)
	}
	if got := reloadCategory(t, db, root.ID); got.ParentCategoryID != nil {
		t.Fatal("root was given a parent")
	}
	// A legitimate move still works, and "null" makes a root again.
	if code, body := updateCategoryForm(t, admin, leaf.ID, url.Values{"parentCategory": {root.ID.String()}}); code != http.StatusOK {
		t.Fatalf("move refused: %d %s", code, body)
	}
	if code, body := updateCategoryForm(t, admin, leaf.ID, url.Values{"parentCategory": {"null"}}); code != http.StatusOK {
		t.Fatalf("un-parent refused: %d %s", code, body)
	}
	if got := reloadCategory(t, db, leaf.ID); got.ParentCategoryID != nil {
		t.Fatal(`"null" did not clear the parent`)
	}
}

func TestCreateCategory_DerivesUniqueSlugAndStoresConfig(t *testing.T) {
	db := setupCategoryConfigDB(t)
	admin := seedUser(t, db, models.RoleAdmin)
	h := &Handlers{}
	create := func(form url.Values) (int, models.Category) {
		c, w := formCtx(http.MethodPost, form, nil, admin)
		h.CreateCategory(c)
		var resp struct {
			Data models.Category `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		if w.Code != http.StatusCreated {
			t.Logf("create: %s", w.Body.String())
		}
		return w.Code, resp.Data
	}

	code, first := create(url.Values{"title": {"Student Hostel"}, "type": {"Service"},
		"priceUnits": {`["per_session"]`}, "listingFields": {`[{"key":"gender","filterable":true}]`}})
	if code != http.StatusCreated || first.Slug == nil || *first.Slug != "student-hostel" {
		t.Fatalf("first create: %d %+v", code, first.Slug)
	}
	if units, ok := first.GetPriceUnits(); !ok || units[0] != models.PriceUnitPerSession {
		t.Fatalf("config not stored on create: %s", first.PriceUnits)
	}
	code, second := create(url.Values{"title": {"Student-Hostel!"}, "type": {"Service"}})
	if code != http.StatusCreated || second.Slug == nil || *second.Slug != "student-hostel-2" {
		t.Fatalf("second create: %d %v", code, second.Slug)
	}
	if code, _ := create(url.Values{"title": {"Taken"}, "type": {"Service"}, "slug": {"student-hostel"}}); code != http.StatusConflict {
		t.Fatalf("taken slug: %d, want 409", code)
	}
	if code, _ := create(url.Values{"title": {"Bad"}, "type": {"Service"}, "bookingFields": {`[{"key":"brand"}]`}}); code != http.StatusBadRequest {
		t.Fatalf("invalid config on create: %d, want 400", code)
	}
}

// Three levels: root sets everything, mid overrides price units, leaf overrides
// listing fields. GET of the leaf reports each value and where it came from,
// and /fields expands the inherited booking form from the catalogue.
func TestGetCategory_ResolvedConfigAndFields(t *testing.T) {
	db := setupCategoryConfigDB(t)
	root := serviceCategory(t, db, models.ServiceTypeBooking)
	mid := serviceCategory(t, db, models.ServiceTypeBooking)
	leaf := serviceCategory(t, db, models.ServiceTypeBooking)
	setCategoryColumn(t, db, mid.ID, "parent_category_id", root.ID)
	setCategoryColumn(t, db, leaf.ID, "parent_category_id", mid.ID)
	setCategoryColumn(t, db, root.ID, "booking_mode", models.BookingModeSlot)
	setCategoryColumn(t, db, root.ID, "price_units", datatypes.JSON(`["fixed"]`))
	setCategoryColumn(t, db, root.ID, "booking_fields", datatypes.JSON(`[{"key":"appointment","required":true}]`))
	setCategoryColumn(t, db, mid.ID, "price_units", datatypes.JSON(`["per_session"]`))
	setCategoryColumn(t, db, leaf.ID, "listing_fields", datatypes.JSON(`[{"key":"gender","required":true,"filterable":true}]`))

	c, w := formCtx(http.MethodGet, nil, catParam(leaf.ID), nil)
	(&Handlers{}).GetCategoryByID(c)
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			BookingMode    *string               `json:"bookingMode"`
			ResolvedConfig models.CategoryConfig `json:"resolvedConfig"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	rc := resp.Data.ResolvedConfig
	if resp.Data.BookingMode != nil {
		t.Error("the leaf's own bookingMode should be null (inherits)")
	}
	if rc.BookingMode.Value == nil || *rc.BookingMode.Value != models.BookingModeSlot || rc.BookingMode.Source.ID != root.ID {
		t.Errorf("bookingMode: %+v", rc.BookingMode)
	}
	if len(rc.PriceUnits.Value) != 1 || rc.PriceUnits.Value[0] != models.PriceUnitPerSession || rc.PriceUnits.Source.ID != mid.ID {
		t.Errorf("priceUnits: %+v", rc.PriceUnits)
	}
	if rc.ListingFields.Source == nil || rc.ListingFields.Source.ID != leaf.ID || rc.ListingFields.Source.Inherited {
		t.Errorf("listingFields: %+v", rc.ListingFields)
	}

	c, w = formCtx(http.MethodGet, nil, catParam(leaf.ID), nil)
	(&Handlers{}).GetCategoryFields(c)
	var fields struct {
		Data struct {
			ListingFields []models.FormField `json:"listingFields"`
			BookingFields []models.FormField `json:"bookingFields"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &fields)
	lf, bf := fields.Data.ListingFields, fields.Data.BookingFields
	if len(lf) != 1 || lf[0].Type != models.BookingFieldSelect || lf[0].Label != "Gender" || !lf[0].Filterable || len(lf[0].Options) != 3 {
		t.Errorf("listing fields not expanded: %+v", lf)
	}
	if len(bf) != 1 || bf[0].Key != "appointment" || bf[0].Type != models.BookingFieldDateTime || !bf[0].Required {
		t.Errorf("inherited booking fields not expanded: %+v", bf)
	}
}

func TestFieldDefAdmin_CreateUpdateDeactivateDelete(t *testing.T) {
	db := setupCategoryConfigDB(t)
	admin := seedUser(t, db, models.RoleAdmin)
	cat := serviceCategory(t, db, models.ServiceTypeContact)
	h := &Handlers{}

	c, w := jsonCtx(http.MethodPost, `{"key":"pet_friendly","type":"boolean","label":"Pet friendly","scope":"listing"}`, nil, admin)
	h.CreateFieldDef(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	c, w = jsonCtx(http.MethodPost, `{"key":"pet_friendly","type":"text","label":"Again","scope":"listing"}`, nil, admin)
	h.CreateFieldDef(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate key: %d, want 409", w.Code)
	}
	c, w = jsonCtx(http.MethodPost, `{"key":"bad","type":"text","label":"x","scope":"listing","options":["a"]}`, nil, admin)
	h.CreateFieldDef(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("options on text: %d, want 400", w.Code)
	}

	// The new field is immediately usable by a category (a catalogue key, not custom).
	if code, body := updateCategoryForm(t, admin, cat.ID, url.Values{"listingFields": {`[{"key":"pet_friendly","filterable":true}]`}}); code != http.StatusOK {
		t.Fatalf("admin-added field refused on a category: %d %s", code, body)
	}

	def := fieldDefByKey(t, db, "pet_friendly")
	params := gin.Params{{Key: "id", Value: def.ID.String()}}
	put := func(body string) (int, string) {
		c, w := jsonCtx(http.MethodPut, body, params, admin)
		h.UpdateFieldDef(c)
		return w.Code, w.Body.String()
	}
	if code, _ := put(`{"type":"text"}`); code != http.StatusConflict {
		t.Errorf("type change of a referenced field: %d, want 409", code)
	}
	if code, _ := put(`{"key":"pets"}`); code != http.StatusBadRequest {
		t.Errorf("key change: %d, want 400", code)
	}
	if code, _ := put(`{"scope":"booking"}`); code != http.StatusConflict {
		t.Errorf("narrowing a referenced field's scope: %d, want 409", code)
	}
	if code, body := put(`{"label":"Pets allowed","isActive":false}`); code != http.StatusOK {
		t.Fatalf("relabel + deactivate: %d %s", code, body)
	}
	if got := fieldDefByKey(t, db, "pet_friendly"); got.Label != "Pets allowed" || got.IsActive || got.Type != models.BookingFieldBoolean {
		t.Fatalf("update not applied: %+v", got)
	}

	// Deactivated: off the category's form, and off the public catalogue.
	c, w = formCtx(http.MethodGet, nil, catParam(cat.ID), nil)
	h.GetCategoryFields(c)
	var served struct {
		Data struct {
			ListingFields []models.FormField `json:"listingFields"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &served)
	if len(served.Data.ListingFields) != 0 {
		t.Errorf("an inactive field is still served: %+v", served.Data.ListingFields)
	}
	c, w = jsonCtx(http.MethodGet, "", nil, nil)
	c.Request.URL.RawQuery = "scope=listing"
	h.ListFieldDefs(c)
	if strings.Contains(w.Body.String(), "pet_friendly") || !strings.Contains(w.Body.String(), `"room_type"`) || strings.Contains(w.Body.String(), `"checkin"`) {
		t.Errorf("public listing catalogue wrong: %s", w.Body.String())
	}

	del := func(id uuid.UUID) (int, string) {
		c, w := jsonCtx(http.MethodDelete, "", gin.Params{{Key: "id", Value: id.String()}}, admin)
		h.DeleteFieldDef(c)
		return w.Code, w.Body.String()
	}
	if code, body := del(def.ID); code != http.StatusConflict {
		t.Errorf("delete of a referenced field: %d, want 409: %s", code, body)
	}
	if code, _ := del(fieldDefByKey(t, db, "brand").ID); code != http.StatusConflict {
		t.Errorf("delete of a system field: %d, want 409", code)
	}
	c, w = jsonCtx(http.MethodPost, `{"key":"unused_one","type":"text","label":"Unused","scope":"both"}`, nil, admin)
	h.CreateFieldDef(c)
	if code, body := del(fieldDefByKey(t, db, "unused_one").ID); code != http.StatusOK {
		t.Errorf("delete of an unused custom field: %d %s", code, body)
	}

	// Admin list shows inactive fields with usage.
	c, w = jsonCtx(http.MethodGet, "", nil, admin)
	h.AdminListFieldDefs(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"pet_friendly"`) || !strings.Contains(w.Body.String(), `"usage":{"categories":1,"services":0}`) {
		t.Errorf("admin list: %d %s", w.Code, w.Body.String())
	}
}

type formSchemaResp struct {
	Data struct {
		FieldSource string                `json:"fieldSource"`
		Fields      []models.BookingField `json:"fields"`
	} `json:"data"`
}

func formSchema(t *testing.T, id uuid.UUID) formSchemaResp {
	t.Helper()
	w := getFormSchema(t, &Handlers{}, id)
	if w.Code != http.StatusOK {
		t.Fatalf("form schema: %d %s", w.Code, w.Body.String())
	}
	var r formSchemaResp
	json.Unmarshal(w.Body.Bytes(), &r)
	return r
}

// The form is the listing's FieldConfig, else the category's resolved
// booking_fields (inherited from an ancestor too), else none (legacy).
func TestGetBookingFormSchema_FallbackOrder(t *testing.T) {
	db := setupCategoryConfigDB(t)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	parent := serviceCategory(t, db, models.ServiceTypeBooking)
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	setCategoryColumn(t, db, cat.ID, "parent_category_id", parent.ID)

	svc := createTestService(t, db, seller.ID, &cat.ID, 20, 3)
	if r := formSchema(t, svc.ID); r.Data.FieldSource != formSourceNone || len(r.Data.Fields) != 0 {
		t.Fatalf("neither set: %+v", r.Data)
	}

	setCategoryColumn(t, db, parent.ID, "booking_fields",
		datatypes.JSON(`[{"key":"room_type","required":true,"options":["single","double"]},{"key":"guests"}]`))
	r := formSchema(t, svc.ID)
	if r.Data.FieldSource != formSourceCategory || len(r.Data.Fields) != 2 {
		t.Fatalf("inherited category form: %+v", r.Data)
	}
	if f := r.Data.Fields[0]; f.Key != "room_type" || f.Type != models.BookingFieldSelect || f.Label != "Room type" ||
		f.Group != models.BookingGroupServiceOptions || !f.Required || len(f.Options) != 2 {
		t.Errorf("category field not expanded: %+v", f)
	}

	// The child's explicit [] overrides the parent: no form.
	setCategoryColumn(t, db, cat.ID, "booking_fields", datatypes.JSON(`[]`))
	if r := formSchema(t, svc.ID); r.Data.FieldSource != formSourceNone {
		t.Fatalf("explicit empty override: %+v", r.Data)
	}
	setCategoryColumn(t, db, cat.ID, "booking_fields", nil)

	writeFieldConfig(t, db, svc.ID, &models.BookingFieldConfig{Fields: []models.BookingField{
		{Key: "checkin", Type: models.BookingFieldDate, Label: "Check-in", Required: true},
	}})
	if r := formSchema(t, svc.ID); r.Data.FieldSource != formSourceListing || len(r.Data.Fields) != 1 || r.Data.Fields[0].Key != "checkin" {
		t.Fatalf("listing config must win: %+v", r.Data)
	}
}

// Checkout validates answers against the category form the buyer was served.
func TestCreateServiceOrder_ValidatesAgainstCategoryForm(t *testing.T) {
	db := setupCategoryConfigDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	setCategoryColumn(t, db, cat.ID, "booking_fields", datatypes.JSON(`[{"key":"checkin","required":true},{"key":"guests"}]`))
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)
	writeBookingProfile(t, db, svc.ID, models.BookingTypeInstant, models.PricingMethodFixed)

	if w := postServiceOrder(t, ch, buyer, map[string]interface{}{"productId": svc.ID.String()}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing required category answer: %d %s", w.Code, w.Body.String())
	}
	if w := postServiceOrder(t, ch, buyer, map[string]interface{}{"productId": svc.ID.String(),
		"answers": map[string]interface{}{"checkin": "2026-11-01", "guests": "two"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric counter: %d %s", w.Code, w.Body.String())
	}
	if w := postServiceOrder(t, ch, buyer, map[string]interface{}{"productId": svc.ID.String(),
		"answers": map[string]interface{}{"checkin": "2026-11-01", "guests": 2}}); w.Code != http.StatusCreated {
		t.Fatalf("valid answers: %d %s", w.Code, w.Body.String())
	}
}

// A booking request validates against the same category form.
func TestCreateBookingRequest_ValidatesAgainstCategoryForm(t *testing.T) {
	db := setupCategoryConfigDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	setCategoryColumn(t, db, cat.ID, "booking_fields", datatypes.JSON(`[{"key":"guests","required":true}]`))
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writeBookingProfile(t, db, svc.ID, models.BookingTypeRequest, models.PricingMethodFixed)
	params := gin.Params{{Key: "id", Value: svc.ID.String()}}

	if w := postJSON(t, buyer, params, map[string]interface{}{"token": "USDC"}, ch.CreateBookingRequest); w.Code != http.StatusBadRequest {
		t.Fatalf("missing required category answer: %d %s", w.Code, w.Body.String())
	}
	w := postJSON(t, buyer, params, map[string]interface{}{"token": "USDC", "answers": map[string]interface{}{"guests": 3}}, ch.CreateBookingRequest)
	if w.Code != http.StatusCreated {
		t.Fatalf("valid answers: %d %s", w.Code, w.Body.String())
	}
}
