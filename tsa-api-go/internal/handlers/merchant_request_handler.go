package handlers

import (
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

type MerchantRequestHandler struct {
	DB       *gorm.DB
	EventBus *events.Bus
}

func NewMerchantRequestHandler(db *gorm.DB, bus *events.Bus) *MerchantRequestHandler {
	return &MerchantRequestHandler{DB: db, EventBus: bus}
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

var validStatuses = []string{
	models.MerchantRequestStatusPending,
	models.MerchantRequestStatusApproved,
	models.MerchantRequestStatusRejected,
}

// SubmitMerchantRequest handles POST /api/merchant-requests.
// Rejects if the caller is already a merchant or has a pending/approved request.
// Users with previously rejected requests may re-apply.
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
	err := h.DB.Where("user_id = ? AND status IN ?", user.ID,
		[]string{models.MerchantRequestStatusPending, models.MerchantRequestStatusApproved}).
		First(&existing).Error
	if err == nil {
		if existing.Status == models.MerchantRequestStatusApproved {
			utils.ErrorResponse(c, http.StatusBadRequest, "You already have an approved merchant request")
		} else {
			utils.ErrorResponse(c, http.StatusBadRequest, "You already have a pending merchant request")
		}
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("SubmitMerchantRequest: failed to check existing requests: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to check existing requests")
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
		log.Printf("SubmitMerchantRequest: failed to create: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create merchant request")
		return
	}

	utils.SuccessResponse(c, http.StatusCreated, "Merchant request submitted successfully", req)
}

// GetMyMerchantRequest handles GET /api/merchant-requests/my-request.
// Returns the most recent request for the caller, or 200 with nil data if none exists.
func (h *MerchantRequestHandler) GetMyMerchantRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var req models.MerchantRequest
	err := h.DB.Where("user_id = ?", user.ID).Order("created_at DESC").First(&req).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.SuccessResponse(c, http.StatusOK, "No merchant request found", nil)
			return
		}
		log.Printf("GetMyMerchantRequest: DB error: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to retrieve merchant request")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Merchant request retrieved", req)
}

// ListMerchantRequests handles GET /api/admin/merchant-requests.
// Supports optional status filter and pagination via page/limit query params.
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
		if !slices.Contains(validStatuses, status) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid status filter")
			return
		}
		query = query.Where("status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		log.Printf("ListMerchantRequests: count error: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to count merchant requests")
		return
	}

	var requests []models.MerchantRequest
	if err := query.Preload("User").Preload("Reviewer").
		Order("created_at DESC").
		Offset((page - 1) * limit).
		Limit(limit).
		Find(&requests).Error; err != nil {
		log.Printf("ListMerchantRequests: find error: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to retrieve merchant requests")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Merchant requests retrieved", gin.H{
		"requests": requests,
		"total":    total,
		"page":     page,
		"limit":    limit,
	})
}

// ApproveMerchantRequest handles POST /api/admin/merchant-requests/:id/approve.
// Atomically marks the request as approved and upgrades the user's role to merchant.
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
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&input); err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
			return
		}
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
		log.Printf("ApproveMerchantRequest: transaction error: %v", txErr)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to approve merchant request")
		return
	}

	if err := h.DB.Preload("User").First(&req, "id = ?", id).Error; err != nil {
		log.Printf("ApproveMerchantRequest: reload error: %v", err)
	}

	h.EventBus.Publish(events.Event{
		Type:    events.MerchantApproved,
		UserID:  req.UserID,
		Title:   "Merchant Request Approved",
		Message: "Your merchant application has been approved. You can now list products.",
		Data: map[string]interface{}{
			"requestId":    req.ID,
			"businessName": req.BusinessName,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Merchant request approved", req)
}

// RejectMerchantRequest handles POST /api/admin/merchant-requests/:id/reject.
// A note explaining the rejection reason is required.
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
		log.Printf("RejectMerchantRequest: update error: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to reject merchant request")
		return
	}

	if err := h.DB.Preload("User").First(&req, "id = ?", id).Error; err != nil {
		log.Printf("RejectMerchantRequest: reload error: %v", err)
	}

	h.EventBus.Publish(events.Event{
		Type:    events.MerchantRejected,
		UserID:  req.UserID,
		Title:   "Merchant Request Rejected",
		Message: "Your merchant application has been rejected: " + input.Note,
		Data: map[string]interface{}{
			"requestId":    req.ID,
			"businessName": req.BusinessName,
			"reason":       input.Note,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Merchant request rejected", req)
}
