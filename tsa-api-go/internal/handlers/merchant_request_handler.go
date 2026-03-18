package handlers

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

type MerchantRequestHandler struct {
	DB *gorm.DB
}

func NewMerchantRequestHandler(db *gorm.DB) *MerchantRequestHandler {
	return &MerchantRequestHandler{DB: db}
}

type submitMerchantRequestInput struct {
	BusinessType        string `json:"businessType" binding:"required"`
	BusinessName        string `json:"businessName" binding:"required"`
	BusinessDescription string `json:"businessDescription"`
	Address             string `json:"address" binding:"required"`
	City                string `json:"city" binding:"required"`
	State               string `json:"state" binding:"required"`
	Country             string `json:"country" binding:"required"`
	Phone               string `json:"phone" binding:"required"`
	RegistrationNumber  string `json:"registrationNumber"`
}

type adminNoteInput struct {
	Note string `json:"note"`
}

// SubmitMerchantRequest handles POST /api/merchant-requests
func (h *MerchantRequestHandler) SubmitMerchantRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	if user.Role == models.RoleMerchant {
		utils.ErrorResponse(c, http.StatusBadRequest, "You are already a merchant")
		return
	}

	var input submitMerchantRequestInput
	if err := c.ShouldBindJSON(&input); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	if !slices.Contains(models.ValidBusinessTypes, input.BusinessType) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid business type")
		return
	}

	var existing models.MerchantRequest
	err := h.DB.Where("user_id = ? AND status = ?", user.ID, models.MerchantRequestStatusPending).First(&existing).Error
	if err == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "You already have a pending merchant request")
		return
	}

	req := models.MerchantRequest{
		UserID:              user.ID,
		BusinessType:        input.BusinessType,
		BusinessName:        input.BusinessName,
		BusinessDescription: input.BusinessDescription,
		Address:             input.Address,
		City:                input.City,
		State:               input.State,
		Country:             input.Country,
		Phone:               input.Phone,
		RegistrationNumber:  input.RegistrationNumber,
		Status:              models.MerchantRequestStatusPending,
	}

	if err := h.DB.Create(&req).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create merchant request")
		return
	}

	utils.SuccessResponse(c, http.StatusCreated, "Merchant request submitted successfully", req)
}

// GetMyMerchantRequest handles GET /api/merchant-requests/my-request
func (h *MerchantRequestHandler) GetMyMerchantRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var req models.MerchantRequest
	err := h.DB.Where("user_id = ?", user.ID).Order("created_at DESC").First(&req).Error
	if err != nil {
		utils.SuccessResponse(c, http.StatusOK, "Merchant request retrieved", nil)
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Merchant request retrieved", req)
}

// ListMerchantRequests handles GET /api/admin/merchant-requests
func (h *MerchantRequestHandler) ListMerchantRequests(c *gin.Context) {
	status := c.Query("status")
	page := 1
	limit := 20

	if p := c.Query("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	if l := c.Query("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}

	query := h.DB.Model(&models.MerchantRequest{})
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	query.Count(&total)

	var requests []models.MerchantRequest
	query.Preload("User").Preload("Reviewer").
		Order("created_at DESC").
		Offset((page - 1) * limit).
		Limit(limit).
		Find(&requests)

	utils.SuccessResponse(c, http.StatusOK, "Merchant requests retrieved", gin.H{
		"requests": requests,
		"total":    total,
		"page":     page,
		"limit":    limit,
	})
}

// ApproveMerchantRequest handles POST /api/admin/merchant-requests/:id/approve
func (h *MerchantRequestHandler) ApproveMerchantRequest(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request ID")
		return
	}

	var input adminNoteInput
	c.ShouldBindJSON(&input)

	var req models.MerchantRequest
	if err := h.DB.First(&req, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Merchant request not found")
		return
	}

	if req.Status != models.MerchantRequestStatusPending {
		utils.ErrorResponse(c, http.StatusBadRequest, "Request is not in pending status")
		return
	}

	now := time.Now()

	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&req).Updates(map[string]interface{}{
			"status":      models.MerchantRequestStatusApproved,
			"reviewed_by": admin.ID,
			"reviewed_at": now,
			"admin_note":  input.Note,
		}).Error; err != nil {
			return err
		}

		if err := tx.Model(&models.User{}).Where("id = ?", req.UserID).
			Update("role", models.RoleMerchant).Error; err != nil {
			return err
		}

		return nil
	})

	if txErr != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to approve merchant request")
		return
	}

	h.DB.Preload("User").First(&req, "id = ?", id)

	utils.SuccessResponse(c, http.StatusOK, "Merchant request approved", req)
}

// RejectMerchantRequest handles POST /api/admin/merchant-requests/:id/reject
func (h *MerchantRequestHandler) RejectMerchantRequest(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request ID")
		return
	}

	var input adminNoteInput
	if err := c.ShouldBindJSON(&input); err != nil || input.Note == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Note is required for rejection")
		return
	}

	var req models.MerchantRequest
	if err := h.DB.First(&req, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Merchant request not found")
		return
	}

	if req.Status != models.MerchantRequestStatusPending {
		utils.ErrorResponse(c, http.StatusBadRequest, "Request is not in pending status")
		return
	}

	now := time.Now()
	if err := h.DB.Model(&req).Updates(map[string]interface{}{
		"status":      models.MerchantRequestStatusRejected,
		"reviewed_by": admin.ID,
		"reviewed_at": now,
		"admin_note":  input.Note,
	}).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to reject merchant request")
		return
	}

	h.DB.Preload("User").First(&req, "id = ?", id)

	utils.SuccessResponse(c, http.StatusOK, "Merchant request rejected", req)
}
