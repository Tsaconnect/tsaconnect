package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// formNull is the form value that clears a nullable config column, so the
// category inherits it from its parent again.
const formNull = "null"

// parseCategoryConfigForm reads the optional configuration fields of a category
// create/update form into column → value updates:
//
//	bookingMode    slot | date_range | quantity
//	priceUnits     JSON array of price units
//	listingFields  JSON array of {key, required, filterable?, options?}
//	bookingFields  JSON array of {key, required, options?}
//
// A field that is absent or empty is left alone; the literal "null" clears it
// (inherit). Field references are validated against the active catalogue. The
// error is a client-facing message.
func parseCategoryConfigForm(c *gin.Context, db *gorm.DB) (map[string]interface{}, error) {
	updates := map[string]interface{}{}

	if v := strings.TrimSpace(c.PostForm("bookingMode")); v != "" {
		if v == formNull {
			updates["booking_mode"] = gorm.Expr("NULL")
		} else if models.ValidBookingMode(v) {
			updates["booking_mode"] = v
		} else {
			return nil, fmt.Errorf("bookingMode must be slot, date_range or quantity")
		}
	}

	if v := strings.TrimSpace(c.PostForm("priceUnits")); v != "" {
		if v == formNull {
			updates["price_units"] = gorm.Expr("NULL")
		} else {
			var units []string
			if err := json.Unmarshal([]byte(v), &units); err != nil {
				return nil, fmt.Errorf("priceUnits must be a JSON array of price units")
			}
			if units == nil {
				units = []string{}
			}
			if err := models.ValidatePriceUnits(units); err != nil {
				return nil, err
			}
			b, _ := json.Marshal(units)
			updates["price_units"] = datatypes.JSON(b)
		}
	}

	var catalog models.FieldCatalog
	for _, f := range []struct{ form, column, usage string }{
		{"listingFields", "listing_fields", models.FieldUsageListing},
		{"bookingFields", "booking_fields", models.FieldUsageBooking},
	} {
		v := strings.TrimSpace(c.PostForm(f.form))
		if v == "" {
			continue
		}
		if v == formNull {
			updates[f.column] = gorm.Expr("NULL")
			continue
		}
		var refs []models.CategoryFieldRef
		dec := json.NewDecoder(strings.NewReader(v))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&refs); err != nil {
			return nil, fmt.Errorf("%s must be a JSON array of {key, required, filterable, options}", f.form)
		}
		if refs == nil {
			refs = []models.CategoryFieldRef{}
		}
		if catalog == nil {
			catalog = loadFieldCatalog(db)
		}
		if err := models.ValidateCategoryFieldRefs(refs, f.usage, catalog); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(refs)
		updates[f.column] = datatypes.JSON(b)
	}
	return updates, nil
}

// slugTaken reports whether slug belongs to a category other than except.
func slugTaken(db *gorm.DB, slug string, except *uuid.UUID) bool {
	q := db.Model(&models.Category{}).Where("slug = ?", slug)
	if except != nil {
		q = q.Where("id <> ?", *except)
	}
	var n int64
	q.Count(&n)
	return n > 0
}

// resolvedFields is the client-facing form definition of a category: the
// resolved field refs expanded with their catalogue entries.
type resolvedFields struct {
	CategoryID          uuid.UUID             `json:"categoryId"`
	ListingFields       []models.FormField    `json:"listingFields"`
	ListingFieldsSource *models.ConfigSource  `json:"listingFieldsSource"`
	BookingFields       []models.FormField    `json:"bookingFields"`
	BookingFieldsSource *models.ConfigSource  `json:"bookingFieldsSource"`
	Config              models.CategoryConfig `json:"resolvedConfig"`
}

// GetCategoryFields handles GET /api/products/category/:categoryId/fields — the
// category's resolved listing and booking forms, each field merged from the
// catalogue (type/label/group) and the category (required/filterable/options).
// Inactive fields are left out.
func (h *Handlers) GetCategoryFields(c *gin.Context) {
	categoryID, err := uuid.Parse(c.Param("categoryId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
		return
	}
	var cat models.Category
	if err := config.DB.First(&cat, "id = ?", categoryID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Category not found")
		return
	}
	cfg, err := models.ResolveCategoryConfig(config.DB, cat)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to resolve category configuration")
		return
	}
	catalog := loadFieldCatalog(config.DB)
	utils.SuccessResponse(c, http.StatusOK, "Category fields fetched successfully", resolvedFields{
		CategoryID:          cat.ID,
		ListingFields:       models.ExpandFieldRefs(cfg.ListingFields.Value, catalog),
		ListingFieldsSource: cfg.ListingFields.Source,
		BookingFields:       models.ExpandFieldRefs(cfg.BookingFields.Value, catalog),
		BookingFieldsSource: cfg.BookingFields.Source,
		Config:              cfg,
	})
}
