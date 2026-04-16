package services

import (
	"encoding/json"
	"log"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/ws"
)

// emailEvents defines which event types also trigger an email.
var emailEvents = map[string]bool{
	events.TransactionCompleted:     true,
	events.TransactionFailed:        true,
	events.TransactionReceived:      true,
	events.OrderEscrowed:            true,
	events.OrderShipped:             true,
	events.OrderDelivered:           true,
	events.OrderCompleted:           true,
	events.OrderRefundRequested:     true,
	events.OrderRefundApproved:      true,
	events.OrderRefundRejected:      true,
	events.OrderRefunded:            true,
	events.SecurityLoginNewDevice:   true,
	events.SecurityFailedAttempts:   true,
	events.SecurityPasswordChanged:  true,
	events.SecurityAccountLocked:    true,
	events.VerificationApproved:     true,
	events.VerificationRejected:     true,
	events.MerchantApproved:         true,
	events.MerchantRejected:         true,
	events.ProductApproved:          true,
	events.ProductRejected:          true,
}

type NotificationService struct {
	db           *gorm.DB
	hub          *ws.Hub
	emailService *EmailService
}

func NewNotificationService(db *gorm.DB, bus *events.Bus, hub *ws.Hub, emailService *EmailService) *NotificationService {
	ns := &NotificationService{
		db:           db,
		hub:          hub,
		emailService: emailService,
	}
	bus.Subscribe("*", ns.handleEvent)
	return ns
}

func (ns *NotificationService) handleEvent(event events.Event) {
	// Determine channel
	channel := "in_app"
	if emailEvents[event.Type] {
		channel = "both"
	}

	// Marshal data
	dataJSON, _ := json.Marshal(event.Data)

	// Create notification record (always, even if muted)
	notification := models.Notification{
		UserID:  event.UserID,
		Type:    event.Type,
		Title:   event.Title,
		Message: event.Message,
		Data:    dataJSON,
		Channel: channel,
	}

	if err := ns.db.Create(&notification).Error; err != nil {
		log.Printf("[NotificationService] failed to save notification: %v", err)
		return
	}

	// Check user preferences
	var user models.User
	if err := ns.db.Select("mute_notifications", "mute_email", "email", "name").First(&user, "id = ?", event.UserID).Error; err != nil {
		log.Printf("[NotificationService] failed to fetch user %s: %v", event.UserID, err)
		return
	}

	// Push via WebSocket (unless muted)
	if !user.MuteNotifications {
		wsPayload, _ := json.Marshal(map[string]interface{}{
			"id":        notification.ID,
			"type":      notification.Type,
			"title":     notification.Title,
			"message":   notification.Message,
			"data":      event.Data,
			"createdAt": notification.CreatedAt,
		})
		ns.hub.SendToUser(event.UserID, wsPayload)
	}

	// Send email (if applicable and not muted)
	if channel == "both" && !user.MuteNotifications && !user.MuteEmail {
		go ns.emailService.Send(
			user.Email,
			user.Name,
			event.Title,
			event.Title,
			event.Message,
			"", "",
		)
	}
}

// GetUnreadCount returns the count of unread notifications for a user.
func (ns *NotificationService) GetUnreadCount(userID uuid.UUID) (int64, error) {
	var count int64
	err := ns.db.Model(&models.Notification{}).
		Where("user_id = ? AND is_read = ? AND deleted_at IS NULL", userID, false).
		Count(&count).Error
	return count, err
}
