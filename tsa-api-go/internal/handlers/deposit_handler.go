package handlers

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// GetDeposits handles GET /api/deposits - admin paginated deposit list.
func (h *Handlers) GetDeposits(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	status := c.Query("status")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := config.DB.Model(&models.Deposit{})
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	query.Count(&total)

	var deposits []models.Deposit
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&deposits).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch deposits")
		return
	}

	totalPages := int(math.Ceil(float64(total) / float64(limit)))

	utils.SuccessResponse(c, http.StatusOK, "Deposits fetched successfully", gin.H{
		"deposits": deposits,
		"pagination": gin.H{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": totalPages,
		},
	})
}

// UpdateDepositStatus handles PATCH /api/deposits/:id/status.
func (h *Handlers) UpdateDepositStatus(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.Role != models.RoleAdmin && user.Role != models.RoleSuperAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Only admins can approve or reject deposits")
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid deposit ID")
		return
	}

	var body struct {
		Status string `json:"status" binding:"required"`
		Note   string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Status is required")
		return
	}

	if body.Status != models.DepositStatusApproved && body.Status != models.DepositStatusRejected {
		utils.ErrorResponse(c, http.StatusBadRequest, "Status must be 'approved' or 'rejected'")
		return
	}

	var deposit models.Deposit
	if err := config.DB.First(&deposit, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Deposit not found")
		return
	}

	if deposit.Status != models.DepositStatusPending {
		utils.ErrorResponse(c, http.StatusBadRequest, "Deposit has already been reviewed")
		return
	}

	now := time.Now()

	updates := map[string]interface{}{
		"status":      body.Status,
		"reviewed_by": user.ID,
		"reviewed_at": now,
	}
	if body.Note != "" {
		updates["admin_note"] = body.Note
	}

	if err := config.DB.Model(&deposit).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update deposit")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Deposit "+body.Status+" successfully", gin.H{
		"depositId": deposit.ID,
		"status":    body.Status,
	})
}
