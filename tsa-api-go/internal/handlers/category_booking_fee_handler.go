package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GetCategoryBookingFees handles GET /api/products/category/:categoryId/booking-fees
// (public). Returns the admin-set merged call-out+booking fare schedule for a
// booking-mode category, as a tier → USD amount map covering all six tiers
// (absent tiers reported as 0). Used by the buyer checkout to price a booking.
func (h *Handlers) GetCategoryBookingFees(c *gin.Context) {
	categoryID, err := uuid.Parse(c.Param("categoryId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
		return
	}

	var rows []models.CategoryBookingFee
	if err := config.DB.Where("category_id = ?", categoryID).Find(&rows).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load booking fees")
		return
	}

	fees := map[string]float64{}
	for _, tier := range models.BookingFeeTiers {
		fees[tier] = 0
	}
	for _, r := range rows {
		fees[r.Tier] = r.AmountUSD
	}

	utils.SuccessResponse(c, http.StatusOK, "Booking fees retrieved", gin.H{
		"categoryId": categoryID,
		"fees":       fees,
	})
}

type upsertBookingFeesRequest struct {
	// Fees maps an area tier (residential … outside_country) → fare in USD. Any of
	// the six tiers may be omitted; omitted tiers are stored as 0 so the schedule
	// is always complete and authoritative.
	Fees map[string]float64 `json:"fees" binding:"required"`
}

// UpsertCategoryBookingFees handles PUT /api/admin/categories/:categoryId/booking-fees
// (admin only). Replaces the full six-tier fare schedule for a booking-mode
// category. Unknown tier keys are rejected; negative amounts are clamped to 0.
func (h *Handlers) UpsertCategoryBookingFees(c *gin.Context) {
	categoryID, err := uuid.Parse(c.Param("categoryId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category ID")
		return
	}

	var req upsertBookingFeesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "fees map is required")
		return
	}

	valid := map[string]bool{}
	for _, t := range models.BookingFeeTiers {
		valid[t] = true
	}
	for tier := range req.Fees {
		if !valid[tier] {
			utils.ErrorResponse(c, http.StatusBadRequest, "Unknown booking tier: "+tier)
			return
		}
	}

	// Verify the category exists (and is a service-capable, booking-mode one).
	var category models.Category
	if err := config.DB.First(&category, "id = ?", categoryID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Category not found")
		return
	}

	now := time.Now()
	err = config.DB.Transaction(func(tx *gorm.DB) error {
		for _, tier := range models.BookingFeeTiers {
			amount := req.Fees[tier] // absent → 0
			if amount < 0 {
				amount = 0
			}
			row := models.CategoryBookingFee{
				ID:         uuid.New(),
				CategoryID: categoryID,
				Tier:       tier,
				AmountUSD:  amount,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			// Upsert on the (category_id, tier) unique index.
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "category_id"}, {Name: "tier"}},
				DoUpdates: clause.Assignments(map[string]interface{}{"amount_usd": amount, "updated_at": now}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to save booking fees")
		return
	}

	h.GetCategoryBookingFees(c)
}
