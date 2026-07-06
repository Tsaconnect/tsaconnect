package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// bookingFormSchema is the customer-facing schema the app renders the booking
// form from: the enabled fields plus the workflow/pricing/payment dimensions and
// the policy text. It is generated from the merchant's ServiceProfile config.
type bookingFormSchema struct {
	ProductID     uuid.UUID             `json:"productId"`
	ServiceType   string                `json:"serviceType"`
	BookingType   string                `json:"bookingType"`
	PricingMethod string                `json:"pricingMethod"`
	Workflow      string                `json:"workflow"`
	PaymentTiming string                `json:"paymentTiming"`
	Fields        []models.BookingField `json:"fields"`
	Policies      bookingPolicies       `json:"policies"`
}

type bookingPolicies struct {
	Cancellation string `json:"cancellation,omitempty"`
	Reschedule   string `json:"reschedule,omitempty"`
	Refund       string `json:"refund,omitempty"`
	Rules        string `json:"rules,omitempty"`
}

// GetBookingFormSchema handles GET /api/services/:id/form-schema — the public
// schema the app uses to auto-generate a service's customer booking form. When
// the merchant has not configured a FieldConfig, Fields is empty and the client
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
		Workflow:      orDefault(profile.Workflow, models.WorkflowAutoAccept),
		PaymentTiming: orDefault(profile.PaymentTiming, models.PaymentTimingBefore),
		Fields:        []models.BookingField{},
		Policies: bookingPolicies{
			Cancellation: profile.CancellationPolicy,
			Reschedule:   profile.ReschedulePolicy,
			Refund:       profile.RefundPolicy,
			Rules:        profile.BookingRules,
		},
	}
	if cfg := profile.GetFieldConfig(); cfg != nil {
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
