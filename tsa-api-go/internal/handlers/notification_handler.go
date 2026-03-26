package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func (h *Handlers) GetNotifications(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	filter := c.Query("filter")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 50 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := config.DB.Where("user_id = ? AND deleted_at IS NULL", user.ID)

	switch filter {
	case "read":
		query = query.Where("is_read = ?", true)
	case "unread":
		query = query.Where("is_read = ?", false)
	}

	var total int64
	query.Model(&models.Notification{}).Count(&total)

	var notifications []models.Notification
	query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&notifications)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Notifications retrieved",
		"data": gin.H{
			"notifications": notifications,
			"pagination": gin.H{
				"page":  page,
				"limit": limit,
				"total": total,
			},
		},
	})
}

func (h *Handlers) MarkAsRead(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	notifID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid notification ID"})
		return
	}

	now := time.Now()
	result := config.DB.Model(&models.Notification{}).
		Where("id = ? AND user_id = ?", notifID, user.ID).
		Updates(map[string]interface{}{"is_read": true, "read_at": now})

	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Notification not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Notification marked as read"})
}

func (h *Handlers) MarkAllAsRead(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	now := time.Now()
	config.DB.Model(&models.Notification{}).
		Where("user_id = ? AND is_read = ? AND deleted_at IS NULL", user.ID, false).
		Updates(map[string]interface{}{"is_read": true, "read_at": now})

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "All notifications marked as read"})
}

func (h *Handlers) GetUnreadCount(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	var count int64
	config.DB.Model(&models.Notification{}).
		Where("user_id = ? AND is_read = ? AND deleted_at IS NULL", user.ID, false).
		Count(&count)

	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"count": count}})
}

func (h *Handlers) GetNotificationPreferences(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"muteNotifications": user.MuteNotifications,
			"muteEmail":         user.MuteEmail,
		},
	})
}

func (h *Handlers) UpdateNotificationPreferences(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	var body struct {
		MuteNotifications *bool `json:"muteNotifications"`
		MuteEmail         *bool `json:"muteEmail"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid request body"})
		return
	}

	updates := map[string]interface{}{}
	if body.MuteNotifications != nil {
		updates["mute_notifications"] = *body.MuteNotifications
	}
	if body.MuteEmail != nil {
		updates["mute_email"] = *body.MuteEmail
	}

	if len(updates) > 0 {
		config.DB.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Preferences updated"})
}
