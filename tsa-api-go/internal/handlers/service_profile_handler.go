package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// GetServiceProfile handles GET /api/services/:id/profile — public read of a
// service's configuration (fees, slot config, policies, coverage). Returns an
// empty (zero-valued) profile if the merchant has not configured one yet.
func (h *Handlers) GetServiceProfile(c *gin.Context) {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid service ID")
		return
	}
	var profile models.ServiceProfile
	if err := config.DB.Where("product_id = ?", productID).First(&profile).Error; err != nil {
		// No profile yet — return a defaulted one so the client can render defaults.
		utils.SuccessResponse(c, http.StatusOK, "Profile fetched successfully",
			models.ServiceProfile{
				ProductID:          productID,
				MaxCapacityPerSlot: 1,
				BookingType:        models.BookingTypeInstant,
				PricingMethod:      models.PricingMethodFixed,
				Timezone:           models.DefaultServiceTimezone,
			})
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Profile fetched successfully", profile)
}

// serviceProfileRequest is a partial update: every field is a pointer so an
// omitted key keeps its stored value while an explicitly sent zero or empty
// string still writes. Plain values made omission destructive, and silently so
// — MaxCapacityPerSlot 0 means "unlimited", so a body that left it out removed
// the listing's capacity limit without any signal to the merchant.
type serviceProfileRequest struct {
	AccessFeeUSD        *float64 `json:"accessFeeUsd"`
	CalloutFeeUSD       *float64 `json:"calloutFeeUsd"`
	SlotDurationMinutes *int     `json:"slotDurationMinutes"`
	MaxCapacityPerSlot  *int     `json:"maxCapacityPerSlot"`
	ServiceAddress      *string  `json:"serviceAddress"`
	CoverageArea        *string  `json:"coverageArea"`
	OperatingCompany    *string  `json:"operatingCompany"`
	CancellationPolicy  *string  `json:"cancellationPolicy"`
	ReschedulePolicy    *string  `json:"reschedulePolicy"`
	RefundPolicy        *string  `json:"refundPolicy"`
	BookingRules        *string  `json:"bookingRules"`

	// Timezone is the IANA zone the availability windows are written in.
	// Rejected at write time if it will not load, so a typo surfaces here
	// rather than silently falling back every time slots are computed.
	Timezone *string `json:"timezone"`

	// Unified booking-engine config (optional). Empty enum values keep the
	// existing/default value; an invalid value is rejected.
	BookingType   *string                    `json:"bookingType"`
	PricingMethod *string                    `json:"pricingMethod"`
	BookingFeeUSD *float64                   `json:"bookingFeeUsd"`
	FieldConfig   *models.BookingFieldConfig `json:"fieldConfig"`
	// PriceUnit must be one of the category's price_units; "" clears it.
	PriceUnit *string `json:"priceUnit"`
}

// derefOr returns the pointed-to value, or fallback when the field was omitted.
func derefOr[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}

// UpsertServiceProfile handles PUT /api/services/:id/profile — the owning
// merchant sets/updates the service configuration.
func (h *Handlers) UpsertServiceProfile(c *gin.Context) {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid service ID")
		return
	}
	product := ownedServiceProduct(c, productID)
	if product == nil {
		return
	}
	var req serviceProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid profile")
		return
	}
	if derefOr(req.AccessFeeUSD, 0) < 0 || derefOr(req.CalloutFeeUSD, 0) < 0 ||
		derefOr(req.SlotDurationMinutes, 0) < 0 || derefOr(req.MaxCapacityPerSlot, 0) < 0 ||
		derefOr(req.BookingFeeUSD, 0) < 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Fees, duration and capacity cannot be negative")
		return
	}
	// Reject unknown enum values; an empty string keeps the existing/default.
	if bt := derefOr(req.BookingType, ""); bt != "" && !models.ValidBookingType(bt) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid booking type")
		return
	}
	if pm := derefOr(req.PricingMethod, ""); pm != "" && !models.ValidPricingMethod(pm) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid pricing method")
		return
	}
	if !models.ValidTimezone(derefOr(req.Timezone, "")) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid timezone. Use an IANA name such as Africa/Lagos")
		return
	}
	if err := models.ValidateFieldConfigAgainst(req.FieldConfig, loadFieldCatalog(config.DB)); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	if msg := checkPriceUnit(config.DB, product, derefOr(req.PriceUnit, "")); msg != "" {
		utils.ErrorResponse(c, http.StatusBadRequest, msg)
		return
	}
	var profile models.ServiceProfile
	err = config.DB.Where("product_id = ?", productID).First(&profile).Error
	isNew := err != nil
	profile.ProductID = productID
	// Merge: only fields the merchant actually sent are written.
	profile.AccessFeeUSD = derefOr(req.AccessFeeUSD, profile.AccessFeeUSD)
	profile.CalloutFeeUSD = derefOr(req.CalloutFeeUSD, profile.CalloutFeeUSD)
	profile.SlotDurationMinutes = derefOr(req.SlotDurationMinutes, profile.SlotDurationMinutes)
	profile.MaxCapacityPerSlot = derefOr(req.MaxCapacityPerSlot, profile.MaxCapacityPerSlot)
	profile.ServiceAddress = derefOr(req.ServiceAddress, profile.ServiceAddress)
	profile.CoverageArea = derefOr(req.CoverageArea, profile.CoverageArea)
	profile.OperatingCompany = derefOr(req.OperatingCompany, profile.OperatingCompany)
	profile.CancellationPolicy = derefOr(req.CancellationPolicy, profile.CancellationPolicy)
	profile.ReschedulePolicy = derefOr(req.ReschedulePolicy, profile.ReschedulePolicy)
	profile.RefundPolicy = derefOr(req.RefundPolicy, profile.RefundPolicy)
	profile.BookingRules = derefOr(req.BookingRules, profile.BookingRules)
	profile.BookingFeeUSD = derefOr(req.BookingFeeUSD, profile.BookingFeeUSD)
	profile.Timezone = derefOr(req.Timezone, profile.Timezone)
	profile.PriceUnit = derefOr(req.PriceUnit, profile.PriceUnit)
	if bt := derefOr(req.BookingType, ""); bt != "" {
		profile.BookingType = bt
	}
	if pm := derefOr(req.PricingMethod, ""); pm != "" {
		profile.PricingMethod = pm
	}
	// Guarantee non-empty enums (new profiles start zero-valued; don't rely on
	// the DB default tag filling them on insert).
	if profile.BookingType == "" {
		profile.BookingType = models.BookingTypeInstant
	}
	if profile.PricingMethod == "" {
		profile.PricingMethod = models.PricingMethodFixed
	}
	if profile.Timezone == "" {
		profile.Timezone = models.DefaultServiceTimezone
	}
	// Capacity defaults to one per slot only on create. On an update a stored 0
	// is a deliberate "unlimited", so forcing 1 here would silently cap a
	// listing the merchant had opened up.
	if isNew && req.MaxCapacityPerSlot == nil {
		profile.MaxCapacityPerSlot = 1
	}
	// Reject invalid combinations on the EFFECTIVE (merged) values, so a partial
	// update can't slip a bad combo through preserve-existing semantics.
	//
	// instant + on_request: can't prepay a price that's only named on approval.
	if profile.BookingType == models.BookingTypeInstant && profile.PricingMethod == models.PricingMethodOnRequest {
		utils.ErrorResponse(c, http.StatusBadRequest, "Instant booking requires a fixed price; use 'request' for price-on-request")
		return
	}
	// request + slot: a request is priced and paid later without a slot pick, so
	// the pay step can't reserve a slot — the combination would strand the buyer.
	if profile.BookingType != models.BookingTypeInstant && profile.SlotDurationMinutes > 0 {
		// The collision often comes from a preserved value rather than anything
		// in this body — switching a slotted listing to request mode keeps its
		// stored slot duration. Say which field to clear, or the merchant is
		// told "no" about a field they did not send.
		msg := "Request bookings cannot reserve a time slot. Set the slot duration to 0 to switch this listing to request mode."
		if req.SlotDurationMinutes != nil {
			msg = "Request bookings cannot reserve a time slot. Use instant booking for slot-based services."
		}
		utils.ErrorResponse(c, http.StatusBadRequest, msg)
		return
	}
	// Omitting fieldConfig preserves the stored form; sending an empty Fields
	// list is how a merchant clears it back to the legacy flow.
	if req.FieldConfig != nil {
		profile.SetFieldConfig(req.FieldConfig)
	}

	if err != nil {
		// Not found → create.
		profile.ID = uuid.New()
		if err := config.DB.Create(&profile).Error; err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to save profile")
			return
		}
	} else if err := config.DB.Save(&profile).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to save profile")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Profile saved successfully", profile)
}

// checkPriceUnit says why unit cannot price product, or "" when it can. Empty
// is always allowed (no unit). A quote is not a unit: price-on-request is the
// pricing method. A category with no price_units configured accepts any unit.
func checkPriceUnit(db *gorm.DB, product *models.Product, unit string) string {
	if unit == "" {
		return ""
	}
	if !models.ValidPriceUnit(unit) || unit == models.PriceUnitOnRequest {
		return "Invalid price unit"
	}
	cfg, ok := serviceCategoryConfig(db, product)
	if !ok || cfg.PriceUnits.Value == nil {
		return ""
	}
	for _, u := range cfg.PriceUnits.Value {
		if u == unit {
			return ""
		}
	}
	return "This category does not offer that price unit"
}
