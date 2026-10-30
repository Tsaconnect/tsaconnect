package handlers

import (
	"log"
	"net/http"
	"sort"

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
//
// A date_range listing always asks for its stay: see withStayFields.
func serviceBookingForm(db *gorm.DB, product *models.Product, profile *models.ServiceProfile) (*models.BookingFieldConfig, string) {
	cfg, source := listingOrCategoryForm(db, product, profile)
	if serviceBookingMode(db, product) == models.BookingModeDateRange {
		if source == formSourceNone {
			source = formSourceCategory
		}
		return withStayFields(cfg), source
	}
	return cfg, source
}

func listingOrCategoryForm(db *gorm.DB, product *models.Product, profile *models.ServiceProfile) (*models.BookingFieldConfig, string) {
	if cfg := profile.GetFieldConfig(); cfg != nil && len(cfg.Fields) > 0 {
		return cfg, formSourceListing
	}
	resolved, ok := serviceCategoryConfig(db, product)
	if !ok || len(resolved.BookingFields.Value) == 0 {
		return nil, formSourceNone
	}
	fields := models.BookingFieldsOf(models.ExpandFieldRefs(resolved.BookingFields.Value, loadFieldCatalog(db)))
	if len(fields) == 0 {
		return nil, formSourceNone
	}
	return &models.BookingFieldConfig{Fields: fields}, formSourceCategory
}

// serviceCategoryConfig resolves a listing's category configuration; ok is
// false when the listing has no category or it cannot be read.
func serviceCategoryConfig(db *gorm.DB, product *models.Product) (models.CategoryConfig, bool) {
	if product.CategoryID == nil {
		return models.CategoryConfig{}, false
	}
	var cat models.Category
	if err := db.First(&cat, "id = ?", *product.CategoryID).Error; err != nil {
		return models.CategoryConfig{}, false
	}
	cfg, err := models.ResolveCategoryConfig(db, cat)
	if err != nil {
		log.Printf("[booking] category %s config unresolved for service %s: %v", cat.ID, product.ID, err)
		return models.CategoryConfig{}, false
	}
	return cfg, true
}

// serviceBookingMode is the booking mode a listing's category resolves to,
// slot when none is set.
func serviceBookingMode(db *gorm.DB, product *models.Product) string {
	if cfg, ok := serviceCategoryConfig(db, product); ok && cfg.BookingMode.Value != nil {
		return *cfg.BookingMode.Value
	}
	return models.BookingModeSlot
}

// bookingFormSchema is the customer-facing schema the app renders the booking
// form from: the enabled fields plus the workflow/pricing/payment dimensions and
// the policy text. It is generated from the merchant's ServiceProfile config.
type bookingFormSchema struct {
	ProductID     uuid.UUID `json:"productId"`
	ServiceType   string    `json:"serviceType"`
	BookingType   string    `json:"bookingType"`
	PricingMethod string    `json:"pricingMethod"`
	// BookingMode is the category's booking mode (slot | date_range | quantity);
	// in quantity mode the buyer picks a quantity up to MaxQuantity.
	BookingMode string `json:"bookingMode"`
	PriceUnit   string `json:"priceUnit"`
	// ChargesPerUnit says the price is multiplied by the quantity picked.
	ChargesPerUnit bool                  `json:"chargesPerUnit"`
	MaxQuantity    int                   `json:"maxQuantity,omitempty"`
	Fields         []models.BookingField `json:"fields"`
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
	schema.BookingMode = serviceBookingMode(config.DB, &product)
	schema.PriceUnit = profile.PriceUnit
	// A quote is the provider's total, whatever the unit.
	schema.ChargesPerUnit = schema.PricingMethod != models.PricingMethodOnRequest &&
		models.ChargesPerUnit(schema.BookingMode, profile.PriceUnit)
	if schema.BookingMode == models.BookingModeQuantity {
		schema.MaxQuantity = maxBookingUnits
		// No booking can take more places than a slot, a day or the event holds.
		if profile.MaxCapacityPerSlot > 0 {
			schema.MaxQuantity = min(schema.MaxQuantity, profile.MaxCapacityPerSlot)
		}
	}
	cfg, source := serviceBookingForm(config.DB, &product, &profile)
	schema.FieldSource = source
	if cfg != nil {
		schema.Fields = cfg.Fields
	}

	utils.SuccessResponse(c, http.StatusOK, "Form schema fetched successfully", schema)
}

// bookingCatalogField is one field the merchant form builder can switch on.
type bookingCatalogField struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	Group   string   `json:"group"`
	Options []string `json:"options,omitempty"`
}

// GetBookingCatalog handles GET /api/services/booking-catalog — the fields the
// merchant form builder renders as toggles: every active field_defs entry a
// booking form may use (built-in ones in their usual order, then the admin's
// own by group and label), with the catalogue's options so a switched-on
// select can be saved. Plus the per-example presets.
func (h *Handlers) GetBookingCatalog(c *gin.Context) {
	live := loadFieldCatalog(config.DB)
	var out []bookingCatalogField
	seen := map[string]bool{}
	add := func(def models.FieldDef) {
		if seen[def.Key] || !def.Scope.Allows(models.FieldUsageBooking) {
			return
		}
		seen[def.Key] = true
		out = append(out, bookingCatalogField{Key: def.Key, Type: def.Type, Label: def.Label, Group: def.Group, Options: def.Options})
	}
	for _, f := range models.BookingFieldCatalog {
		if def, ok := live[f.Key]; ok {
			add(def)
		}
	}
	rest := make([]models.FieldDef, 0, len(live))
	for _, def := range live {
		rest = append(rest, def)
	}
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].Group != rest[j].Group {
			return rest[i].Group < rest[j].Group
		}
		return rest[i].Label < rest[j].Label
	})
	for _, def := range rest {
		add(def)
	}
	utils.SuccessResponse(c, http.StatusOK, "Catalog fetched successfully", gin.H{
		"catalog": out,
		"presets": models.BookingFieldPresets,
	})
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
