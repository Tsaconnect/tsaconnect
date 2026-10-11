// p2p_message_service.go implements the per-trade chat thread for Phase 1
// P2P trades. Two source types:
//
//   - User messages (text or image) — buyer or seller posts during the trade
//   - System messages — auto-generated on every trade state transition (e.g.
//     "Buyer marked payment sent at 14:32"). The trade service calls
//     PostSystem on transitions; users see them inline in the chat.
//
// Image uploads happen out-of-band via the existing /api/upload endpoint
// (Cloudinary). Clients pass the returned URL into SendImage; the service
// only persists the URL.
//
// Phase 1 has no WebSocket broadcast — clients poll GET /messages?since=...
// or the notification dispatcher delivers via existing channels. Real-time
// WS dispatch comes with the frontend trade-detail UI in Step 15.
package services

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var (
	ErrMessageEmpty    = errors.New("p2p chat: message body required")
	ErrMessageTooLong  = errors.New("p2p chat: message exceeds maximum length")
	ErrImageURLInvalid = errors.New("p2p chat: image must be uploaded in the app")
)

// maxBodyLen caps text messages to a sensible width. Larger payloads should
// be uploads.
const maxBodyLen = 4000

type P2PMessageService struct {
	db    *gorm.DB
	bus   EventPublisher
	clock Clock
}

func NewP2PMessageService(db *gorm.DB, bus EventPublisher) *P2PMessageService {
	return &P2PMessageService{db: db, bus: bus, clock: SystemClock}
}

func (m *P2PMessageService) WithClock(c Clock) *P2PMessageService {
	cp := *m
	cp.clock = c
	return &cp
}

// ─── Send ───────────────────────────────────────────────────────────────────

// SendText posts a buyer- or seller-authored text message to the trade
// thread. Validates participant membership and length.
func (m *P2PMessageService) SendText(ctx context.Context, tradeID, senderID uuid.UUID, body string) (*models.P2PMessage, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, ErrMessageEmpty
	}
	// Cap by rune count, not byte length — emojis and CJK are real input
	// in P2P chats and shouldn't artificially halve the user-facing limit.
	if utf8.RuneCountInString(body) > maxBodyLen {
		return nil, ErrMessageTooLong
	}
	if err := m.assertParticipant(ctx, tradeID, senderID); err != nil {
		return nil, err
	}
	msg := &models.P2PMessage{
		ID:        uuid.New(),
		TradeID:   tradeID,
		SenderID:  &senderID,
		Type:      models.P2PMessageTypeText,
		Body:      body,
		CreatedAt: m.clock.Now(),
	}
	if err := m.db.WithContext(ctx).Create(msg).Error; err != nil {
		return nil, fmt.Errorf("persist message: %w", err)
	}
	m.notifyParticipants(ctx, tradeID, senderID, msg.ID, "New message in P2P trade")
	return msg, nil
}

// SendImage posts an image message. ImageURL must point at the platform's
// Cloudinary CDN — client uploads via /api/upload first.
func (m *P2PMessageService) SendImage(ctx context.Context, tradeID, senderID uuid.UUID, imageURL string) (*models.P2PMessage, error) {
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return nil, ErrImageURLInvalid
	}
	// Only images from the platform's upload CDN: arbitrary hosts would be
	// tracking pixels or worse in the counterparty's app.
	if !isUploadedImageURL(imageURL) {
		return nil, ErrImageURLInvalid
	}
	if err := m.assertParticipant(ctx, tradeID, senderID); err != nil {
		return nil, err
	}
	msg := &models.P2PMessage{
		ID:        uuid.New(),
		TradeID:   tradeID,
		SenderID:  &senderID,
		Type:      models.P2PMessageTypeImage,
		ImageURL:  imageURL,
		CreatedAt: m.clock.Now(),
	}
	if err := m.db.WithContext(ctx).Create(msg).Error; err != nil {
		return nil, fmt.Errorf("persist message: %w", err)
	}
	m.notifyParticipants(ctx, tradeID, senderID, msg.ID, "Image attached to P2P trade")
	return msg, nil
}

// PostSystem writes an automated system message — sender_id is null, type is
// `system`. Called by P2PTradeService on every state transition so the chat
// thread is a complete narrative.
func (m *P2PMessageService) PostSystem(ctx context.Context, tradeID uuid.UUID, body string) error {
	if m == nil {
		return nil // optional dependency; nil-safe
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return ErrMessageEmpty
	}
	// Cap length defensively — system messages can interpolate user input
	// (e.g. dispute reason), and a byte-slice in the middle of a multi-byte
	// rune would produce invalid UTF-8 that Postgres' text column rejects.
	// Truncate by RUNE, not byte, to preserve UTF-8 validity.
	if utf8.RuneCountInString(body) > maxBodyLen {
		runes := []rune(body)
		body = string(runes[:maxBodyLen-1]) + "…"
	}
	msg := &models.P2PMessage{
		ID:        uuid.New(),
		TradeID:   tradeID,
		Type:      models.P2PMessageTypeSystem,
		Body:      body,
		CreatedAt: m.clock.Now(),
	}
	return m.db.WithContext(ctx).Create(msg).Error
}

// ─── Read ───────────────────────────────────────────────────────────────────

type MessageFilter struct {
	Since *time.Time // optional — only messages created after this point
	Limit int
}

// List returns messages for a trade in chronological order. Requestor must
// be a trade participant (buyer or seller); operators use AdminList.
func (m *P2PMessageService) List(ctx context.Context, tradeID, requestor uuid.UUID, f MessageFilter) ([]models.P2PMessage, error) {
	if err := m.assertParticipant(ctx, tradeID, requestor); err != nil {
		return nil, err
	}
	// The oldest `limit` messages after f.Since, oldest-first, so a poll with
	// since=<last seen> pages forward.
	return m.queryThread(ctx, tradeID, f, "created_at ASC, id ASC")
}

// AdminList is List without the participant check, for dispute review: the
// NEWEST `limit` messages (a long thread's latest messages are what a
// reviewer needs), returned oldest-first for display. The handler route must
// be admin-gated.
func (m *P2PMessageService) AdminList(ctx context.Context, tradeID uuid.UUID, f MessageFilter) ([]models.P2PMessage, error) {
	var n int64
	if err := m.db.WithContext(ctx).Model(&models.P2PTrade{}).Where("id = ?", tradeID).Count(&n).Error; err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrTradeNotFound
	}
	out, err := m.queryThread(ctx, tradeID, f, "created_at DESC, id DESC")
	if err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out, nil
}

// queryThread reads up to f.Limit (default 100, max 200) of a trade's
// messages after f.Since in the given order. The id tiebreak keeps messages
// written in the same instant in a stable order across pages.
func (m *P2PMessageService) queryThread(ctx context.Context, tradeID uuid.UUID, f MessageFilter, order string) ([]models.P2PMessage, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	q := m.db.WithContext(ctx).Model(&models.P2PMessage{}).
		Where("trade_id = ?", tradeID).
		Order(order).
		Limit(limit)
	if f.Since != nil {
		q = q.Where("created_at > ?", *f.Since)
	}
	var out []models.P2PMessage
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ─── Internals ──────────────────────────────────────────────────────────────

// assertParticipant returns nil if `userID` is the buyer or seller of the
// trade. Otherwise ErrForbidden / ErrTradeNotFound.
func (m *P2PMessageService) assertParticipant(ctx context.Context, tradeID, userID uuid.UUID) error {
	var trade models.P2PTrade
	if err := m.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTradeNotFound
		}
		return err
	}
	if trade.BuyerID != userID && trade.SellerID != userID {
		return ErrForbidden
	}
	return nil
}

// notifyParticipants emits a chat-message event for the COUNTER-party so
// they get a notification. The sender already knows they sent it.
func (m *P2PMessageService) notifyParticipants(ctx context.Context, tradeID, senderID, messageID uuid.UUID, title string) {
	if m.bus == nil {
		return
	}
	var trade models.P2PTrade
	if err := m.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		return
	}
	other := trade.SellerID
	if senderID == trade.SellerID {
		other = trade.BuyerID
	}
	m.bus.Publish(events.Event{
		Type:    "p2p.message.new",
		UserID:  other,
		Title:   title,
		Message: "",
		Data: map[string]interface{}{
			"tradeId":   tradeID.String(),
			"messageId": messageID.String(),
			"senderId":  senderID.String(),
		},
	})
}
