package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// Where a service's booking form comes from (bookingFormSchema.FieldSource).
const (
	formSourceListing  = "listing"  // the merchant's own ServiceProfile.FieldConfig
	formSourceCategory = "category" // the category's resolved booking_fields
	formSourceNone     = "none"     // neither: the client's legacy per-serviceType flow
)

// serviceBookingForm resolves the booking form for a service: the listing's
// FieldConfig, else its category's resolved booking_fields expanded from the
// field catalogue, else none (legacy flow). It is the ONE resolver for both the
// form a buyer is served (GetBookingFormSchema) and the one their answers are
// validated against (booking request + checkout), so the two never differ.
func serviceBookingForm(db *gorm.DB, product *models.Product, profile *models.ServiceProfile) (*models.BookingFieldConfig, string) {
	if cfg := profile.GetFieldConfig(); cfg != nil && len(cfg.Fields) > 0 {
		return cfg, formSourceListing
	}
	if product.CategoryID == nil {
		return nil, formSourceNone
	}
	var cat models.Category
	if err := db.First(&cat, "id = ?", *product.CategoryID).Error; err != nil {
		return nil, formSourceNone
	}
	resolved, err := models.ResolveCategoryConfig(db, cat)
	if err != nil {
		log.Printf("[booking-form] category %s config unresolved for service %s: %v", cat.ID, product.ID, err)
		return nil, formSourceNone
	}
	if len(resolved.BookingFields.Value) == 0 {
		return nil, formSourceNone
	}
	fields := models.BookingFieldsOf(models.ExpandFieldRefs(resolved.BookingFields.Value, loadFieldCatalog(db)))
	if len(fields) == 0 {
		return nil, formSourceNone
	}
	return &models.BookingFieldConfig{Fields: fields}, formSourceCategory
}

// bookingFormSchema is the customer-facing schema the app renders the booking
// form from: the enabled fields plus the workflow/pricing/payment dimensions and
// the policy text. It is generated from the merchant's ServiceProfile config.
type bookingFormSchema struct {
	ProductID     uuid.UUID             `json:"productId"`
	ServiceType   string                `json:"serviceType"`
	BookingType   string                `json:"bookingType"`
	PricingMethod string                `json:"pricingMethod"`
	Fields        []models.BookingField `json:"fields"`
	// FieldSource says where Fields came from: listing | category | none.
	FieldSource string          `json:"fieldSource"`
	Policies    bookingPolicies `json:"policies"`
}

type bookingPolicies struct {
	Cancellation string `json:"cancellation,omitempty"`
	Reschedule   string `json:"reschedule,omitempty"`
	Refund       string `json:"refund,omitempty"`
	Rules        string `json:"rules,omitempty"`
}

// GetBookingFormSchema handles GET /api/services/:id/form-schema — the public
// schema the app uses to auto-generate a service's customer booking form.
// Fields come from serviceBookingForm: the listing's FieldConfig, else the
// category's booking_fields; when neither is set Fields is empty and the client
// falls back to its legacy per-serviceType flow.
func (h *Handlers) GetBookingFormSchema(c *gin.Context) {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid service ID")
		return
	}

	var product models.Product
	if err := config.DB.First(&product, "id = ?", productID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Service not found")
		return
	}

	var cat models.Category
	if product.CategoryID != nil {
		config.DB.First(&cat, "id = ?", *product.CategoryID)
	}
	serviceType := models.EffectiveServiceType(cat)

	var profile models.ServiceProfile
	config.DB.Where("product_id = ?", productID).First(&profile)

	schema := bookingFormSchema{
		ProductID:     productID,
		ServiceType:   serviceType,
		BookingType:   orDefault(profile.BookingType, models.BookingTypeInstant),
		PricingMethod: orDefault(profile.PricingMethod, models.PricingMethodFixed),
		Fields:        []models.BookingField{},
		Policies: bookingPolicies{
			Cancellation: profile.CancellationPolicy,
			Reschedule:   profile.ReschedulePolicy,
			Refund:       profile.RefundPolicy,
			Rules:        profile.BookingRules,
		},
	}
	cfg, source := serviceBookingForm(config.DB, &product, &profile)
	schema.FieldSource = source
	if cfg != nil {
		schema.Fields = cfg.Fields
	}

	utils.SuccessResponse(c, http.StatusOK, "Form schema fetched successfully", schema)
}

// GetBookingCatalog handles GET /api/services/booking-catalog — the static
// catalog of known booking fields plus the per-example presets, used by the
// merchant field-config builder to render toggles.
func (h *Handlers) GetBookingCatalog(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Catalog fetched successfully", gin.H{
		"catalog": models.BookingFieldCatalog,
		"presets": models.BookingFieldPresets,
	})
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
