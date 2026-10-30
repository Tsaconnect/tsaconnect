package models

import (
	"time"

	"github.com/google/uuid"
)

// ChatThread is a two-person conversation ABOUT something: a customer and the
// provider of the thing they are asking about. The subject says what it is and
// decides who may open it (services/chat_service.go chatSubjects); one customer
// has one thread per subject.
//
// P2P trade chat predates this and keeps its own per-trade thread
// (P2PMessage); both share the message rules in services/chat_message_rules.go.
type ChatThread struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	SubjectType string    `gorm:"size:32;not null;uniqueIndex:idx_chat_thread_subject,priority:1" json:"subjectType"`
	SubjectID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_chat_thread_subject,priority:2" json:"subjectId"`
	CustomerID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_chat_thread_subject,priority:3;index" json:"customerId"`
	ProviderID  uuid.UUID `gorm:"type:uuid;not null;index" json:"providerId"`
	// Denormalised for the inbox: one row per thread, newest first, without
	// reading every thread's messages.
	LastMessageAt      *time.Time `gorm:"index" json:"lastMessageAt"`
	LastMessagePreview string     `gorm:"size:140" json:"lastMessagePreview"`
	LastSenderID       *uuid.UUID `gorm:"type:uuid" json:"lastSenderId,omitempty"`
	CustomerReadAt     *time.Time `json:"customerReadAt"`
	ProviderReadAt     *time.Time `json:"providerReadAt"`
	CreatedAt          time.Time  `json:"createdAt"`
}

func (ChatThread) TableName() string { return "chat_threads" }

// ChatSubjectServiceContact is a contact-type service whose contact fee the
// customer has paid; SubjectID is the service (product) id.
const ChatSubjectServiceContact = "service_contact"

// ChatMessage is one user-authored message in a ChatThread.
type ChatMessage struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	ThreadID  uuid.UUID `gorm:"type:uuid;not null;index:idx_chat_msg_thread_created,priority:1" json:"threadId"`
	SenderID  uuid.UUID `gorm:"type:uuid;not null" json:"senderId"`
	Type      string    `gorm:"size:16;not null" json:"type"` // text | image
	Body      string    `gorm:"type:text" json:"body,omitempty"`
	ImageURL  string    `gorm:"type:text" json:"imageUrl,omitempty"`
	CreatedAt time.Time `gorm:"index:idx_chat_msg_thread_created,priority:2" json:"createdAt"`
}

func (ChatMessage) TableName() string { return "chat_messages" }

const (
	ChatMessageTypeText  = "text"
	ChatMessageTypeImage = "image"
)
