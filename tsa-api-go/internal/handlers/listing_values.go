package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

var errListingValuesShape = errors.New("listingValues must be a JSON object of field answers")

// listingValuesForm reads the optional `listingValues` form field: a JSON
// object of answers keyed by catalogue key. present is false when the field
// was not sent, so an update leaves the stored values alone.
func listingValuesForm(c *gin.Context) (values map[string]interface{}, present bool, err error) {
	raw := strings.TrimSpace(c.PostForm("listingValues"))
	if raw == "" {
		return nil, false, nil
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
		return nil, true, errListingValuesShape
	}
	return values, true, nil
}

// encodeListingValues validates values against the category's listing form
// and returns what to store. No category means no form, so nothing is kept.
func encodeListingValues(db *gorm.DB, categoryID *uuid.UUID, values map[string]interface{}) (datatypes.JSON, error) {
	if categoryID == nil {
		return nil, nil
	}
	var cat models.Category
	if err := db.First(&cat, "id = ?", *categoryID).Error; err != nil {
		return nil, err
	}
	form, err := models.CategoryListingForm(db, cat, loadFieldCatalog(db))
	if err != nil {
		return nil, err
	}
	clean, err := models.ValidateListingValues(form, values)
	if err != nil {
		return nil, err
	}
	return models.EncodeListingValues(clean), nil
}

// applyListingValueFilter narrows a listing query to the attr[key]=value
// filters the category marks filterable. It answers 400 itself on a malformed
// value and returns ok=false; an unreadable category only skips the filter.
func applyListingValueFilter(c *gin.Context, db, query *gorm.DB, categoryID uuid.UUID) (*gorm.DB, bool) {
	params := c.QueryMap("attr")
	if len(params) == 0 {
		return query, true
	}
	var cat models.Category
	if err := db.First(&cat, "id = ?", categoryID).Error; err != nil {
		return query, true
	}
	form, err := models.CategoryListingForm(db, cat, loadFieldCatalog(db))
	if err != nil {
		return query, true
	}
	match, err := models.ListingValueFilter(form, params)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return query, false
	}
	if match == "" {
		return query, true
	}
	return query.Where("listing_values @> CAST(? AS jsonb)", match), true
}
