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
	events.OrderPlaced:              true,
	events.OrderEscrowed:            true,
	events.OrderShipped:             true,
	events.OrderDelivered:           true,
	events.OrderCompleted:           true,
	events.OrderRefundRequested:     true,
	events.OrderRefundApproved:      true,
	events.OrderRefundRejected:      true,
	events.OrderRefunded:            true,
	events.OrderCancelRequested:     true,
	events.OrderCancelApproved:      true,
	events.OrderCancelRejected:      true,
	events.OrderDisputed:            true,
	events.OrderDisputeResolved:     true,
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

// pushEvents is the curated allowlist of event types that also fire an Expo
// push (out-of-app). Counterparty-actionable + urgent events only — self-
// triggered ones (p2p.ad.*, product.listed, *.submitted, transaction.pending)
// are intentionally excluded so the tray isn't noisy.
var pushEvents = map[string]bool{
	events.TransactionReceived:  true,
	events.TransactionCompleted: true,
	events.TransactionFailed:    true,
	events.OrderPlaced:          true,
	events.OrderEscrowed:        true,
	events.OrderShipped:         true,
	events.OrderDelivered:       true,
	events.OrderCompleted:       true,
	events.OrderRefundRequested: true,
	events.OrderRefundApproved:  true,
	events.OrderRefundRejected:  true,
	events.OrderRefunded:        true,
	events.OrderCancelRequested: true,
	events.OrderCancelApproved:  true,
	events.OrderCancelRejected:  true,
	events.OrderDisputed:        true,
	events.OrderDisputeResolved: true,
	events.SecurityLoginNewDevice:  true,
	events.SecurityFailedAttempts:  true,
	events.SecurityPasswordChanged: true,
	events.SecurityAccountLocked:   true,
	events.VerificationApproved: true,
	events.VerificationRejected: true,
	events.MerchantApproved:     true,
	events.MerchantRejected:     true,
	events.MerchantSuspended:    true,
	events.ProductApproved:      true,
	events.ProductRejected:      true,
	events.P2PTradeOpened:    true,
	events.P2PTradePaid:      true,
	events.P2PTradeReleasing: true,
	events.P2PTradeReleased:  true,
	events.P2PTradeCancelled: true,
	events.P2PTradeDisputed:  true,
	events.P2PTradeResolved:  true,
	events.P2PTradeExpired:   true,
	"p2p.message.new":        true, // raw string in p2p_message_service.go (not a const)
	events.P2PDepositConfirmed:  true,
	events.P2PWithdrawConfirmed: true,
}

// pushWorthy reports whether an event type should also be delivered as a push.
func pushWorthy(eventType string) bool { return pushEvents[eventType] }

type NotificationService struct {
	db           *gorm.DB
	hub          *ws.Hub
	emailService *EmailService
	push         *ExpoPushService // nil → push delivery disabled
}

func NewNotificationService(db *gorm.DB, bus *events.Bus, hub *ws.Hub, emailService *EmailService, push *ExpoPushService) *NotificationService {
	ns := &NotificationService{
		db:           db,
		hub:          hub,
		emailService: emailService,
		push:         push,
	}
	bus.Subscribe("*", ns.handleEvent)
	return ns
}

func (ns *NotificationService) handleEvent(event events.Event) {
	// Determine channel. P2P trade milestones also email (via a dedicated
	// role-aware composer below), so mark them "both" on the record too.
	channel := "in_app"
	if emailEvents[event.Type] || p2pTradeEmailEvents[event.Type] {
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

		// Out-of-app Expo push for the curated set (async; best-effort).
		if ns.push != nil && pushWorthy(event.Type) {
			go ns.sendPush(event)
		}
	}

	// Email delivery (if applicable and not muted).
	if !user.MuteNotifications && !user.MuteEmail && user.Email != "" && ns.emailService != nil {
		switch {
		case p2pTradeEmailEvents[event.Type]:
			// Rich, role-aware P2P trade email (loads the trade + composes per
			// buyer/seller). Kept off the generic path so it isn't sent twice.
			go ns.sendP2PTradeEmail(event, user)
		case channel == "both":
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
}

// sendPush delivers the event to a user's registered Expo device tokens and
// prunes any the push service reports as dead. The push `data` carries the
// event `type` (+ ids) so the app can deep-link on tap. Best-effort: runs in a
// goroutine and never blocks the bus.
// sendP2PTradeEmail loads the trade referenced by the event and sends a rich,
// role-aware milestone email. Best-effort (logs + returns on any failure);
// runs in its own goroutine so a slow mail send never blocks event handling.
func (ns *NotificationService) sendP2PTradeEmail(event events.Event, user models.User) {
	raw, ok := event.Data["tradeId"]
	if !ok {
		return
	}
	var tradeID uuid.UUID
	switch v := raw.(type) {
	case uuid.UUID:
		tradeID = v
	case string:
		parsed, err := uuid.Parse(v)
		if err != nil {
			return
		}
		tradeID = parsed
	default:
		return
	}

	var trade models.P2PTrade
	if err := ns.db.First(&trade, "id = ?", tradeID).Error; err != nil {
		log.Printf("[NotificationService] p2p email: load trade %s: %v", tradeID, err)
		return
	}

	content, send := buildP2PTradeEmail(event, &trade)
	if !send {
		return
	}
	if err := ns.emailService.Send(
		user.Email, user.Name,
		content.Subject, content.Subject, content.Body,
		"", "",
	); err != nil {
		log.Printf("[NotificationService] p2p email to %s failed: %v", user.Email, err)
	}
}

func (ns *NotificationService) sendPush(event events.Event) {
	var tokens []models.DeviceToken
	if err := ns.db.Where("user_id = ?", event.UserID).Find(&tokens).Error; err != nil {
		log.Printf("[NotificationService] device-token lookup for %s: %v", event.UserID, err)
		return
	}
	if len(tokens) == 0 {
		return
	}
	toks := make([]string, len(tokens))
	for i := range tokens {
		toks[i] = tokens[i].Token
	}
	data := map[string]interface{}{"type": event.Type}
	for k, v := range event.Data {
		data[k] = v
	}
	dead := ns.push.Send(toks, event.Title, event.Message, data)
	if len(dead) > 0 {
		if err := ns.db.Where("token IN ?", dead).Delete(&models.DeviceToken{}).Error; err != nil {
			log.Printf("[NotificationService] prune dead tokens: %v", err)
		}
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
