package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// chatCaller is the signed-in user and the chat service, or false with the
// response written.
func (h *Handlers) chatCaller(c *gin.Context) (*models.User, *services.ChatService, bool) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Unauthorized")
		return nil, nil, false
	}
	if h.Chat == nil {
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "Chat is not available right now", "chat_unavailable")
		return nil, nil, false
	}
	return user, h.Chat, true
}

// chatError writes the response for a chat service error.
func chatError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrMessageEmpty), errors.Is(err, services.ErrImageURLInvalid):
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrMessageTooLong):
		utils.ErrorResponse(c, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, services.ErrChatThreadNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, err.Error())
	case errors.Is(err, services.ErrChatNotEntitled):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, err.Error(), "contact_fee_required")
	case errors.Is(err, services.ErrChatClosed):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "chat_closed")
	case errors.Is(err, services.ErrChatSubjectUnknown):
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
	default:
		utils.ErrorResponse(c, http.StatusInternalServerError, "Chat failed, please try again")
	}
}

// OpenServiceChat handles POST /api/services/:id/chat — the caller's
// conversation with the provider of a contact service whose fee they paid.
func (h *Handlers) OpenServiceChat(c *gin.Context) {
	user, svc, ok := h.chatCaller(c)
	if !ok {
		return
	}
	serviceID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	thread, err := svc.Open(c.Request.Context(), user.ID, models.ChatSubjectServiceContact, serviceID)
	if err != nil {
		chatError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Conversation", thread)
}

// ListChatThreads handles GET /api/chat/threads — the caller's conversations.
func (h *Handlers) ListChatThreads(c *gin.Context) {
	user, svc, ok := h.chatCaller(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	threads, err := svc.Inbox(c.Request.Context(), user.ID, limit)
	if err != nil {
		chatError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Conversations", threads)
}

// GetChatThread handles GET /api/chat/threads/:id.
func (h *Handlers) GetChatThread(c *gin.Context) {
	user, svc, ok := h.chatCaller(c)
	if !ok {
		return
	}
	threadID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	thread, err := svc.Get(c.Request.Context(), threadID, user.ID)
	if err != nil {
		chatError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Conversation", thread)
}

// ListChatMessages handles GET /api/chat/threads/:id/messages?since=&limit=.
func (h *Handlers) ListChatMessages(c *gin.Context) {
	user, svc, ok := h.chatCaller(c)
	if !ok {
		return
	}
	threadID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	var since *time.Time
	if s := c.Query("since"); s != "" {
		parsed, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "since must be an RFC 3339 time")
			return
		}
		since = &parsed
	}
	msgs, err := svc.List(c.Request.Context(), threadID, user.ID, services.MessageFilter{Since: since, Limit: limit})
	if err != nil {
		chatError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Messages", msgs)
}

type sendChatMessageRequest struct {
	Type     string `json:"type" binding:"required"`
	Body     string `json:"body"`
	ImageURL string `json:"imageUrl"`
}

// SendChatMessage handles POST /api/chat/threads/:id/messages.
func (h *Handlers) SendChatMessage(c *gin.Context) {
	user, svc, ok := h.chatCaller(c)
	if !ok {
		return
	}
	threadID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req sendChatMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "type must be text or image")
		return
	}
	var msg *models.ChatMessage
	var err error
	switch req.Type {
	case models.ChatMessageTypeText:
		msg, err = svc.SendText(c.Request.Context(), threadID, user.ID, req.Body)
	case models.ChatMessageTypeImage:
		msg, err = svc.SendImage(c.Request.Context(), threadID, user.ID, req.ImageURL)
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "type must be text or image")
		return
	}
	if err != nil {
		chatError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Message sent", msg)
}

// MarkChatRead handles POST /api/chat/threads/:id/read.
func (h *Handlers) MarkChatRead(c *gin.Context) {
	user, svc, ok := h.chatCaller(c)
	if !ok {
		return
	}
	threadID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := svc.MarkRead(c.Request.Context(), threadID, user.ID); err != nil {
		chatError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Read", nil)
}
