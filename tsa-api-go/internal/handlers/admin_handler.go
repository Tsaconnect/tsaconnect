package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// GetAdminStats handles GET /api/admin/stats.
func (h *Handlers) GetAdminStats(c *gin.Context) {
	var totalUsers int64
	config.DB.Model(&models.User{}).Count(&totalUsers)

	var totalProducts int64
	config.DB.Model(&models.Product{}).Count(&totalProducts)

	var pendingVerifications int64
	config.DB.Model(&models.User{}).Where("verification_status = ?", models.VerificationStatusPending).Count(&pendingVerifications)

	var pendingDeposits int64
	config.DB.Model(&models.Deposit{}).Where("status = ?", models.DepositStatusPending).Count(&pendingDeposits)

	var pendingAdverts int64
	config.DB.Model(&models.Product{}).Where("is_featured = ?", false).Count(&pendingAdverts)

	var totalOrders int64
	config.DB.Model(&models.Order{}).Count(&totalOrders)

	// Revenue calculation from completed orders
	var revenue float64
	config.DB.Model(&models.Order{}).Where("status = ?", models.OrderStatusCompleted).Select("COALESCE(SUM(total), 0)").Scan(&revenue)

	var pendingPrivateSales int64
	config.DB.Model(&models.PrivateSaleSubmission{}).Where("status = ?", "pending").Count(&pendingPrivateSales)

	utils.SuccessResponse(c, http.StatusOK, "Admin stats fetched", gin.H{
		"totalUsers":           totalUsers,
		"totalProducts":        totalProducts,
		"pendingVerifications": pendingVerifications,
		"pendingDeposits":      pendingDeposits,
		"pendingAdverts":       pendingAdverts,
		"totalOrders":          totalOrders,
		"revenue":              revenue,
		"pendingPrivateSales":  pendingPrivateSales,
	})
}
