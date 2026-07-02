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
		// No profile yet — return a zero-valued one so the client can render defaults.
		utils.SuccessResponse(c, http.StatusOK, "Profile fetched successfully",
			models.ServiceProfile{ProductID: productID, MaxCapacityPerSlot: 1})
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
	if req.AccessFeeUSD < 0 || req.CalloutFeeUSD < 0 || req.SlotDurationMinutes < 0 || req.MaxCapacityPerSlot < 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Fees, duration and capacity cannot be negative")
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
