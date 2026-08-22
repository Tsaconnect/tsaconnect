package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// admin_user_handler.go — moderation of the account itself.
//
// AccountStatus was already enforced everywhere: middleware.Auth re-reads the
// user on every request and rejects anything but "active", so a suspension
// takes effect on the very next call. What was missing was any way to set it —
// the constant existed and nothing wrote it.

// inFlightOrderStatuses are the states where funds are committed on-chain and
// the seller still has something to do. Suspending a seller locks them out of
// shipping and confirming, so the admin is shown a count of these first.
var inFlightOrderStatuses = []string{
	models.OrderStatusEscrowed,
	models.OrderStatusShipped,
	models.OrderStatusDelivered,
	models.OrderStatusRefundRequested,
	models.OrderStatusCancelRequested,
}

// requireTargetUser loads the user named by :id, writing the error response
// itself when it cannot.
func requireTargetUser(c *gin.Context) (*models.User, bool) {
	userID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid user ID")
		return nil, false
	}
	var user models.User
	if err := config.DB.First(&user, "id = ?", userID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "User not found")
		return nil, false
	}
	return &user, true
}

// countInFlightOrders returns how many orders the user is the SELLER on that
// still need action from them.
func countInFlightOrders(userID uuid.UUID) int64 {
	var n int64
	config.DB.Model(&models.Order{}).
		Where("seller_id = ? AND status IN ?", userID, inFlightOrderStatuses).
		Count(&n)
	return n
}

// userStats are the counts the console shows alongside the profile.
type userStats struct {
	Products       int64 `json:"products"`
	ActiveProducts int64 `json:"activeProducts"`
	OrdersAsBuyer  int64 `json:"ordersAsBuyer"`
	OrdersAsSeller int64 `json:"ordersAsSeller"`
	InFlightOrders int64 `json:"inFlightOrders"`
}

// AdminGetUser handles GET /api/admin/users/:id — the profile plus the counts
// an admin needs to judge an account, and the name of whoever suspended it.
func (h *Handlers) AdminGetUser(c *gin.Context) {
	if _, ok := requireAdmin(c); !ok {
		return
	}
	user, ok := requireTargetUser(c)
	if !ok {
		return
	}

	var stats userStats
	config.DB.Model(&models.Product{}).Where("user_id = ?", user.ID).Count(&stats.Products)
	config.DB.Model(&models.Product{}).
		Where("user_id = ? AND status = ?", user.ID, models.ProductStatusActive).
		Count(&stats.ActiveProducts)
	config.DB.Model(&models.Order{}).Where("buyer_id = ?", user.ID).Count(&stats.OrdersAsBuyer)
	config.DB.Model(&models.Order{}).Where("seller_id = ?", user.ID).Count(&stats.OrdersAsSeller)
	stats.InFlightOrders = countInFlightOrders(user.ID)

	var suspendedByName string
	if user.SuspendedBy != nil {
		config.DB.Model(&models.User{}).
			Select("name").Where("id = ?", *user.SuspendedBy).Scan(&suspendedByName)
	}

	var referrerName string
	if user.ReferredBy != nil {
		config.DB.Model(&models.User{}).
			Select("name").Where("id = ?", *user.ReferredBy).Scan(&referrerName)
	}

	utils.SuccessResponse(c, http.StatusOK, "User fetched successfully", gin.H{
		"user":            user,
		"stats":           stats,
		"suspendedByName": suspendedByName,
		"referrerName":    referrerName,
	})
}

// SuspendUser handles POST /api/admin/users/:id/suspend.
//
// This locks the account out of every authenticated request and hides their
// listings from buyers (see models.VisibleToPublic), so the reason is mandatory
// and the response reports how many orders were left in flight.
func (h *Handlers) SuspendUser(c *gin.Context) {
	admin, ok := requireAdmin(c)
	if !ok {
		return
	}

	var body struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&body)
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "A suspension reason is required")
		return
	}

	user, ok := requireTargetUser(c)
	if !ok {
		return
	}
	if user.ID == admin.ID {
		utils.ErrorResponse(c, http.StatusBadRequest, "You cannot suspend your own account")
		return
	}
	// Suspending an admin would let one admin lock out another; account
	// moderation is for user accounts, and a role change is the correct tool.
	if isAdminUser(user) {
		utils.ErrorResponse(c, http.StatusForbidden, "Administrator accounts cannot be suspended")
		return
	}
	if user.AccountStatus == models.AccountStatusSuspended {
		utils.ErrorResponse(c, http.StatusBadRequest, "Account is already suspended")
		return
	}
	if user.AccountStatus == models.AccountStatusDeleted {
		utils.ErrorResponse(c, http.StatusBadRequest, "Account is deleted")
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"account_status":        models.AccountStatusSuspended,
		"suspension_reason":     reason,
		"suspended_at":          now,
		"suspended_by":          admin.ID,
		"suspended_from_status": user.AccountStatus,
		"updated_at":            now,
	}
	if err := config.DB.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to suspend account")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Account suspended successfully", gin.H{
		"id":             user.ID,
		"accountStatus":  models.AccountStatusSuspended,
		"inFlightOrders": countInFlightOrders(user.ID),
	})
}

// UnsuspendUser handles POST /api/admin/users/:id/unsuspend — restores the
// status the account held before, rather than defaulting to active.
func (h *Handlers) UnsuspendUser(c *gin.Context) {
	if _, ok := requireAdmin(c); !ok {
		return
	}
	user, ok := requireTargetUser(c)
	if !ok {
		return
	}
	if user.AccountStatus != models.AccountStatusSuspended {
		utils.ErrorResponse(c, http.StatusBadRequest, "Account is not suspended")
		return
	}

	restored := user.SuspendedFromStatus
	if !models.SuspendableAccountStatuses[restored] {
		restored = models.AccountStatusActive
	}

	updates := map[string]interface{}{
		"account_status":        restored,
		"suspension_reason":     "",
		"suspended_at":          nil,
		"suspended_by":          nil,
		"suspended_from_status": "",
		"updated_at":            time.Now(),
	}
	if err := config.DB.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to reactivate account")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Account reactivated successfully", gin.H{
		"id":            user.ID,
		"accountStatus": restored,
	})
}
