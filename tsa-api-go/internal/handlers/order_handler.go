package handlers

import (
	"math"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// GetOrders handles GET /api/orders - admin paginated order list.
func (h *Handlers) GetOrders(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	status := c.Query("status")
	search := c.Query("search")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := config.DB.Model(&models.Order{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if search != "" {
		query = query.Where("id::text ILIKE ?", "%"+search+"%")
	}

	var total int64
	query.Count(&total)

	var orders []models.Order
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&orders).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch orders")
		return
	}

	totalPages := int(math.Ceil(float64(total) / float64(limit)))

	utils.SuccessResponse(c, http.StatusOK, "Orders fetched successfully", gin.H{
		"orders": orders,
		"pagination": gin.H{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": totalPages,
		},
	})
}

// GetOrderByID handles GET /api/orders/:id - admin order detail.
func (h *Handlers) GetOrderByID(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Order fetched successfully", order)
}

// UpdateOrderStatus handles PATCH /api/orders/:id/status.
func (h *Handlers) UpdateOrderStatus(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var body struct {
		Status string `json:"status" binding:"required"`
		Notes  string `json:"notes"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Status is required")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	// Validate state transition
	validNext := models.ValidNextStatuses(order.Status)
	allowed := false
	for _, s := range validNext {
		if s == body.Status {
			allowed = true
			break
		}
	}
	if !allowed {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid status transition from "+order.Status+" to "+body.Status)
		return
	}

	updates := map[string]interface{}{"status": body.Status}
	if body.Notes != "" {
		updates["notes"] = body.Notes
	}

	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order status")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Order status updated", gin.H{
		"orderId": order.ID,
		"status":  body.Status,
	})
}
