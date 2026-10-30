package services

import (
	"errors"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

// The rules every user-authored chat message follows, P2P trade threads and
// conversations alike, so the two chats cannot drift apart.

var (
	ErrMessageEmpty    = errors.New("chat: message body required")
	ErrMessageTooLong  = errors.New("chat: message exceeds maximum length")
	ErrImageURLInvalid = errors.New("chat: image must be uploaded in the app")
)

// maxBodyLen caps text messages to a sensible width. Larger payloads should
// be uploads.
const maxBodyLen = 4000

// chatText trims a text message and checks it is non-empty and within
// maxBodyLen. The cap counts runes, not bytes — emojis and CJK are real input
// and shouldn't artificially halve the user-facing limit.
func chatText(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", ErrMessageEmpty
	}
	if utf8.RuneCountInString(body) > maxBodyLen {
		return "", ErrMessageTooLong
	}
	return body, nil
}

// chatImageURL accepts only images on the platform's upload CDN: arbitrary
// hosts would be tracking pixels or worse in the other party's app.
func chatImageURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !isUploadedImageURL(raw) {
		return "", ErrImageURLInvalid
	}
	return raw, nil
}

// messagePage orders a thread's message query and bounds it to f.Limit
// (default 100, max 200) — one page rule for every chat.
func messagePage(q *gorm.DB, f MessageFilter, order string) *gorm.DB {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	return q.Order(order).Limit(limit)
}
