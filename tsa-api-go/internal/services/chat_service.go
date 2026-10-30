// chat_service.go — two-person conversations about a subject (a paid service
// contact today). A customer opens the thread once they are entitled to (the
// subject decides); both parties then send text or images, and each new message
// notifies the other side (in-app row + WS frame + push, never email).
//
// Message rules are shared with the P2P trade chat (chat_message_rules.go).
// Adding a subject (e.g. a product enquiry) is one entry in chatSubjects.
package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrChatThreadNotFound = errors.New("chat: conversation not found")
	ErrChatSubjectUnknown = errors.New("chat: unknown conversation subject")
	// ErrChatNotEntitled: the caller may not open a conversation about this
	// subject (for a service contact: the contact fee is not paid).
	ErrChatNotEntitled = errors.New("chat: pay the contact fee to chat with this provider")
	// ErrChatClosed: the listing or its provider is no longer available, so the
	// conversation takes no new messages.
	ErrChatClosed = errors.New("chat: this provider is not available right now")
)

// chatSubject decides who the provider is for a customer asking about a
// subject, and whether that customer may open the conversation at all.
// live reports whether the subject still takes new messages: moderation
// (a suspended listing or provider) closes the conversation to new writes,
// while its history stays readable.
type chatSubject struct {
	provider func(ctx context.Context, db *gorm.DB, customerID, subjectID uuid.UUID) (providerID uuid.UUID, err error)
	live     func(ctx context.Context, db *gorm.DB, subjectID, providerID uuid.UUID) error
}

var chatSubjects = map[string]chatSubject{
	models.ChatSubjectServiceContact: {provider: serviceContactSubject, live: serviceContactLive},
}

// serviceContactSubject: only a customer who paid the service's contact fee may
// chat, with the provider that payment named.
func serviceContactSubject(ctx context.Context, db *gorm.DB, customerID, serviceID uuid.UUID) (uuid.UUID, error) {
	var pay models.ServiceContactPayment
	err := db.WithContext(ctx).Select("service_provider_id").
		Where("caller_id = ? AND service_id = ?", customerID, serviceID).
		First(&pay).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return uuid.Nil, ErrChatNotEntitled
	}
	if err != nil {
		return uuid.Nil, err
	}
	return pay.ServiceProviderID, nil
}

// serviceContactLive: an admin has not suspended the service, and its
// provider's account is active. A merchant pausing their own listing
// (inactive, sold out) does not cut off customers who already paid.
func serviceContactLive(ctx context.Context, db *gorm.DB, serviceID, providerID uuid.UUID) error {
	var n int64
	err := db.WithContext(ctx).Model(&models.Product{}).
		Joins("JOIN users ON users.id = ?", providerID).
		Where("products.id = ? AND products.status IS DISTINCT FROM ? AND users.account_status = ?",
			serviceID, models.ProductStatusSuspended, models.AccountStatusActive).
		Count(&n).Error
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrChatClosed
	}
	return nil
}

// previewLen bounds the inbox preview (runes), inside the column's 140.
const previewLen = 120

type ChatService struct {
	db    *gorm.DB
	bus   EventPublisher
	clock Clock
}

func NewChatService(db *gorm.DB, bus EventPublisher) *ChatService {
	return &ChatService{db: db, bus: bus, clock: SystemClock}
}

func (s *ChatService) WithClock(c Clock) *ChatService {
	cp := *s
	cp.clock = c
	return &cp
}

// ChatThreadView is a thread as one participant sees it: the other person and
// what the conversation is about, with the participant's own unread flag.
type ChatThreadView struct {
	models.ChatThread
	Role            string `json:"role"` // customer | provider
	CounterpartID   string `json:"counterpartId"`
	CounterpartName string `json:"counterpartName"`
	SubjectTitle    string `json:"subjectTitle"`
	SubjectImage    string `json:"subjectImage,omitempty"`
	Unread          bool   `json:"unread"`
}

// Open returns the customer's conversation about the subject, creating it on
// first use. Only the customer side opens a thread; the provider answers from
// the inbox once it exists.
func (s *ChatService) Open(ctx context.Context, customerID uuid.UUID, subjectType string, subjectID uuid.UUID) (*ChatThreadView, error) {
	subject, ok := chatSubjects[subjectType]
	if !ok {
		return nil, ErrChatSubjectUnknown
	}
	providerID, err := subject.provider(ctx, s.db, customerID, subjectID)
	if err != nil {
		return nil, err
	}
	if providerID == customerID {
		return nil, ErrChatNotEntitled
	}
	if err := subject.live(ctx, s.db, subjectID, providerID); err != nil {
		return nil, err
	}
	thread := models.ChatThread{
		ID:          uuid.New(),
		SubjectType: subjectType,
		SubjectID:   subjectID,
		CustomerID:  customerID,
		ProviderID:  providerID,
		CreatedAt:   s.clock.Now(),
	}
	// Two taps racing to open both land on the one row.
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&thread).Error; err != nil {
		return nil, fmt.Errorf("open conversation: %w", err)
	}
	var stored models.ChatThread
	if err := s.db.WithContext(ctx).
		Where("subject_type = ? AND subject_id = ? AND customer_id = ?", subjectType, subjectID, customerID).
		First(&stored).Error; err != nil {
		return nil, err
	}
	views, err := s.views(ctx, customerID, []models.ChatThread{stored})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// Get returns one thread the caller takes part in.
func (s *ChatService) Get(ctx context.Context, threadID, userID uuid.UUID) (*ChatThreadView, error) {
	thread, err := s.participantThread(ctx, threadID, userID)
	if err != nil {
		return nil, err
	}
	views, err := s.views(ctx, userID, []models.ChatThread{*thread})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// Inbox lists the caller's conversations, newest activity first. A thread
// nobody has written in yet is shown to its customer only (the provider has
// nothing to answer).
func (s *ChatService) Inbox(ctx context.Context, userID uuid.UUID, limit int) ([]ChatThreadView, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var threads []models.ChatThread
	err := s.db.WithContext(ctx).
		Where("customer_id = ? OR (provider_id = ? AND last_message_at IS NOT NULL)", userID, userID).
		Order("COALESCE(last_message_at, created_at) DESC, id DESC").
		Limit(limit).
		Find(&threads).Error
	if err != nil {
		return nil, err
	}
	return s.views(ctx, userID, threads)
}

// SendText posts a text message from a participant.
func (s *ChatService) SendText(ctx context.Context, threadID, senderID uuid.UUID, body string) (*models.ChatMessage, error) {
	body, err := chatText(body)
	if err != nil {
		return nil, err
	}
	return s.send(ctx, threadID, senderID, models.ChatMessage{Type: models.ChatMessageTypeText, Body: body}, previewOf(body))
}

// SendImage posts an image (already uploaded to the platform CDN).
func (s *ChatService) SendImage(ctx context.Context, threadID, senderID uuid.UUID, imageURL string) (*models.ChatMessage, error) {
	imageURL, err := chatImageURL(imageURL)
	if err != nil {
		return nil, err
	}
	return s.send(ctx, threadID, senderID, models.ChatMessage{Type: models.ChatMessageTypeImage, ImageURL: imageURL}, "Photo")
}

func (s *ChatService) send(ctx context.Context, threadID, senderID uuid.UUID, msg models.ChatMessage, preview string) (*models.ChatMessage, error) {
	thread, err := s.participantThread(ctx, threadID, senderID)
	if err != nil {
		return nil, err
	}
	if subject, ok := chatSubjects[thread.SubjectType]; ok {
		if err := subject.live(ctx, s.db, thread.SubjectID, thread.ProviderID); err != nil {
			return nil, err
		}
	}
	now := s.clock.Now()
	msg.ID = uuid.New()
	msg.ThreadID = threadID
	msg.SenderID = senderID
	msg.CreatedAt = now
	// Sending reads the thread for the sender, so their own message never
	// shows as unread to them.
	readCol := "customer_read_at"
	if senderID == thread.ProviderID {
		readCol = "provider_read_at"
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&msg).Error; err != nil {
			return fmt.Errorf("persist message: %w", err)
		}
		return tx.Model(&models.ChatThread{}).Where("id = ?", threadID).Updates(map[string]interface{}{
			"last_message_at":      now,
			"last_message_preview": preview,
			"last_sender_id":       senderID,
			readCol:                now,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	s.notify(ctx, thread, senderID, msg.ID, preview)
	return &msg, nil
}

// List returns a thread's messages oldest first. Without since it is the
// NEWEST page (what opening a conversation must show); with since it pages
// forward from there. since is inclusive — a message sharing the last one's
// timestamp is never skipped; the client drops ids it already holds.
func (s *ChatService) List(ctx context.Context, threadID, userID uuid.UUID, f MessageFilter) ([]models.ChatMessage, error) {
	if _, err := s.participantThread(ctx, threadID, userID); err != nil {
		return nil, err
	}
	q := s.db.WithContext(ctx).Where("thread_id = ?", threadID)
	var out []models.ChatMessage
	if f.Since != nil {
		err := messagePage(q.Where("created_at >= ?", *f.Since), f, "created_at ASC, id ASC").Find(&out).Error
		return out, err
	}
	if err := messagePage(q, f, "created_at DESC, id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out, nil
}

// MarkRead records that the caller has seen the thread up to now.
func (s *ChatService) MarkRead(ctx context.Context, threadID, userID uuid.UUID) error {
	thread, err := s.participantThread(ctx, threadID, userID)
	if err != nil {
		return err
	}
	col := "customer_read_at"
	if userID == thread.ProviderID {
		col = "provider_read_at"
	}
	return s.db.WithContext(ctx).Model(&models.ChatThread{}).Where("id = ?", threadID).
		Update(col, s.clock.Now()).Error
}

// participantThread loads the thread if userID is its customer or provider.
// A thread the caller is not in answers as not found, so ids cannot be probed.
func (s *ChatService) participantThread(ctx context.Context, threadID, userID uuid.UUID) (*models.ChatThread, error) {
	var thread models.ChatThread
	err := s.db.WithContext(ctx).
		Where("id = ? AND (customer_id = ? OR provider_id = ?)", threadID, userID, userID).
		First(&thread).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrChatThreadNotFound
	}
	if err != nil {
		return nil, err
	}
	return &thread, nil
}

// notify tells the other participant about a new message. The preview rides
// as the notification message, which is what the push shows.
func (s *ChatService) notify(ctx context.Context, thread *models.ChatThread, senderID, messageID uuid.UUID, preview string) {
	if s.bus == nil {
		return
	}
	recipient := thread.ProviderID
	if senderID == thread.ProviderID {
		recipient = thread.CustomerID
	}
	var sender models.User
	title := "New message"
	if err := s.db.WithContext(ctx).Select("name").First(&sender, "id = ?", senderID).Error; err == nil && sender.Name != "" {
		title = "New message from " + sender.Name
	}
	s.bus.Publish(events.Event{
		Type:    events.ChatMessageNew,
		UserID:  recipient,
		ActorID: senderID,
		Title:   title,
		Message: preview,
		Data: map[string]interface{}{
			"threadId":  thread.ID.String(),
			"messageId": messageID.String(),
			"senderId":  senderID.String(),
		},
	})
}

// views decorates threads for viewer: the other person's name, the subject's
// title and image, and the viewer's unread flag. Two batch reads, not N.
func (s *ChatService) views(ctx context.Context, viewer uuid.UUID, threads []models.ChatThread) ([]ChatThreadView, error) {
	out := make([]ChatThreadView, len(threads))
	if len(threads) == 0 {
		return out, nil
	}
	userIDs := make([]uuid.UUID, 0, len(threads))
	productIDs := make([]uuid.UUID, 0, len(threads))
	for _, t := range threads {
		userIDs = append(userIDs, counterpart(t, viewer))
		productIDs = append(productIDs, t.SubjectID) // every subject today is a product
	}
	var users []models.User
	if err := s.db.WithContext(ctx).Select("id", "name").Where("id IN ?", dedupeUUIDs(userIDs)).Find(&users).Error; err != nil {
		return nil, err
	}
	names := make(map[uuid.UUID]string, len(users))
	for _, u := range users {
		names[u.ID] = u.Name
	}
	var products []models.Product
	if err := s.db.WithContext(ctx).Select("id", "name", "images").Where("id IN ?", dedupeUUIDs(productIDs)).Find(&products).Error; err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]*models.Product, len(products))
	for i := range products {
		byID[products[i].ID] = &products[i]
	}
	for i, t := range threads {
		other := counterpart(t, viewer)
		v := ChatThreadView{
			ChatThread:      t,
			Role:            "customer",
			CounterpartID:   other.String(),
			CounterpartName: names[other],
		}
		readAt := t.CustomerReadAt
		if viewer == t.ProviderID {
			v.Role = "provider"
			readAt = t.ProviderReadAt
		}
		if p, ok := byID[t.SubjectID]; ok {
			v.SubjectTitle = p.Name
			if imgs := p.GetImages(); len(imgs) > 0 {
				v.SubjectImage = imgs[0].URL
			}
		}
		v.Unread = t.LastMessageAt != nil && t.LastSenderID != nil && *t.LastSenderID != viewer &&
			(readAt == nil || readAt.Before(*t.LastMessageAt))
		out[i] = v
	}
	return out, nil
}

func counterpart(t models.ChatThread, viewer uuid.UUID) uuid.UUID {
	if viewer == t.ProviderID {
		return t.CustomerID
	}
	return t.ProviderID
}

func dedupeUUIDs(ids []uuid.UUID) []uuid.UUID {
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return slices.Compact(ids)
}

// previewOf cuts a message to the inbox preview length on a rune boundary,
// on one line.
func previewOf(body string) string {
	body = strings.Join(strings.Fields(body), " ")
	if utf8.RuneCountInString(body) <= previewLen {
		return body
	}
	return string([]rune(body)[:previewLen-1]) + "…"
}
