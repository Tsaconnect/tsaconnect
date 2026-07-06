package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
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
				Workflow:           models.WorkflowAutoAccept,
				PaymentTiming:      models.PaymentTimingBefore,
			})
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Profile fetched successfully", profile)
}

type serviceProfileRequest struct {
	AccessFeeUSD        float64 `json:"accessFeeUsd"`
	CalloutFeeUSD       float64 `json:"calloutFeeUsd"`
	SlotDurationMinutes int     `json:"slotDurationMinutes"`
	MaxCapacityPerSlot  int     `json:"maxCapacityPerSlot"`
	ServiceAddress      string  `json:"serviceAddress"`
	CoverageArea        string  `json:"coverageArea"`
	OperatingCompany    string  `json:"operatingCompany"`
	CancellationPolicy  string  `json:"cancellationPolicy"`
	ReschedulePolicy    string  `json:"reschedulePolicy"`
	RefundPolicy        string  `json:"refundPolicy"`
	BookingRules        string  `json:"bookingRules"`

	// Unified booking-engine config (optional). Empty enum values keep the
	// existing/default value; an invalid value is rejected.
	BookingType    string                     `json:"bookingType"`
	PricingMethod  string                     `json:"pricingMethod"`
	Workflow       string                     `json:"workflow"`
	PaymentTiming  string                     `json:"paymentTiming"`
	DepositPercent int                        `json:"depositPercent"`
	BookingFeeUSD  float64                    `json:"bookingFeeUsd"`
	FieldConfig    *models.BookingFieldConfig `json:"fieldConfig"`
}

// UpsertServiceProfile handles PUT /api/services/:id/profile — the owning
// merchant sets/updates the service configuration.
func (h *Handlers) UpsertServiceProfile(c *gin.Context) {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid service ID")
		return
	}
	if ownedServiceProduct(c, productID) == nil {
		return
	}
	var req serviceProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid profile")
		return
	}
	if req.AccessFeeUSD < 0 || req.CalloutFeeUSD < 0 || req.SlotDurationMinutes < 0 || req.MaxCapacityPerSlot < 0 || req.BookingFeeUSD < 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Fees, duration and capacity cannot be negative")
		return
	}
	if req.DepositPercent < 0 || req.DepositPercent > 100 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Deposit percent must be between 0 and 100")
		return
	}
	// Reject unknown enum values; an empty string keeps the existing/default.
	if req.BookingType != "" && !models.ValidBookingType(req.BookingType) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid booking type")
		return
	}
	if req.PricingMethod != "" && !models.ValidPricingMethod(req.PricingMethod) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid pricing method")
		return
	}
	if req.Workflow != "" && !models.ValidWorkflow(req.Workflow) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid workflow")
		return
	}
	if req.PaymentTiming != "" && !models.ValidPaymentTiming(req.PaymentTiming) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid payment timing")
		return
	}
	if err := models.ValidateFieldConfig(req.FieldConfig); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}

	var profile models.ServiceProfile
	err = config.DB.Where("product_id = ?", productID).First(&profile).Error
	profile.ProductID = productID
	profile.AccessFeeUSD = req.AccessFeeUSD
	profile.CalloutFeeUSD = req.CalloutFeeUSD
	profile.SlotDurationMinutes = req.SlotDurationMinutes
	profile.MaxCapacityPerSlot = req.MaxCapacityPerSlot
	profile.ServiceAddress = req.ServiceAddress
	profile.CoverageArea = req.CoverageArea
	profile.OperatingCompany = req.OperatingCompany
	profile.CancellationPolicy = req.CancellationPolicy
	profile.ReschedulePolicy = req.ReschedulePolicy
	profile.RefundPolicy = req.RefundPolicy
	profile.BookingRules = req.BookingRules
	profile.DepositPercent = req.DepositPercent
	profile.BookingFeeUSD = req.BookingFeeUSD
	if req.BookingType != "" {
		profile.BookingType = req.BookingType
	}
	if req.PricingMethod != "" {
		profile.PricingMethod = req.PricingMethod
	}
	if req.Workflow != "" {
		profile.Workflow = req.Workflow
	}
	if req.PaymentTiming != "" {
		profile.PaymentTiming = req.PaymentTiming
	}
	// Guarantee non-empty enums (new profiles start zero-valued; don't rely on
	// the DB default tag filling them on insert).
	if profile.BookingType == "" {
		profile.BookingType = models.BookingTypeInstant
	}
	if profile.PricingMethod == "" {
		profile.PricingMethod = models.PricingMethodFixed
	}
	if profile.Workflow == "" {
		profile.Workflow = models.WorkflowAutoAccept
	}
	if profile.PaymentTiming == "" {
		profile.PaymentTiming = models.PaymentTimingBefore
	}
	profile.SetFieldConfig(req.FieldConfig)

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
