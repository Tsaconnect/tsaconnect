package handlers

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// Open links: the reusable kind of payment link. The owner makes one (a label
// the payer sees, optionally a narrower amount range) and prints it as a QR or
// shares it; each customer who opens it enters an amount and starts their own
// payment (source "open"), paid through the same checkout as any other.

// POST /api/pay/merchant/open-links?mode {label, minAmount?, maxAmount?} —
// answers 201 with the open link.
func (h *PayHandler) OwnerCreateOpenLink(c *gin.Context) {
	var in services.PayOpenLinkInput
	m, mode, ok := h.payOwnerLinkWrite(c, &in, true)
	if !ok {
		return
	}
	l, err := h.payments.CreateOpenLink(c.Request.Context(), m, mode, in)
	if err != nil {
		payLinkFail(c, err)
		return
	}
	h.answerOpenLink(c, http.StatusCreated, "Payment link created", m, l)
}

// GET /api/pay/merchant/open-links?mode&page&limit — the owner's open links in
// one mode, newest first, each with what has been paid through it:
// {items, total, page, limit}.
func (h *PayHandler) OwnerOpenLinks(c *gin.Context) {
	_, m, ok := h.payOwnerMerchant(c)
	if !ok {
		return
	}
	mode, ok := payOwnerMode(c)
	if !ok {
		return
	}
	page, limit := payOwnerPage(c)
	ls, total, err := h.payments.OpenLinks(c.Request.Context(), m.ID, mode, page, limit)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	views, err := h.payments.OpenLinkViews(c.Request.Context(), m, ls, h.liveEnabled)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment links", gin.H{"items": views, "total": total, "page": page, "limit": limit})
}

// GET /api/pay/merchant/open-links/:id?mode — one open link and the most recent
// payments made through it: {openLink, payments}.
func (h *PayHandler) OwnerOpenLink(c *gin.Context) {
	m, l, ok := h.payOwnerOpenLink(c)
	if !ok {
		return
	}
	view, ok := h.openLinkView(c, m, l)
	if !ok {
		return
	}
	ps, err := h.payments.OpenLinkPayments(c.Request.Context(), l.ID)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	payments, err := h.payments.Views(c.Request.Context(), ps)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment link", gin.H{"openLink": view, "payments": payments})
}

// PUT /api/pay/merchant/open-links/:id?mode {label, minAmount, maxAmount,
// active?, expectedVersion} — answers the open link.
func (h *PayHandler) OwnerUpdateOpenLink(c *gin.Context) {
	var in services.PayOpenLinkUpdate
	m, mode, ok := h.payOwnerLinkWrite(c, &in, false)
	if !ok {
		return
	}
	id, ok := payOpenLinkID(c)
	if !ok {
		return
	}
	l, err := h.payments.UpdateOpenLink(c.Request.Context(), m, mode, id, in)
	if err != nil {
		payLinkFail(c, err)
		return
	}
	h.answerOpenLink(c, http.StatusOK, "Payment link updated", m, l)
}

// POST /api/pay/merchant/open-links/:id/replace?mode — a new address for the
// link (a printed QR was copied, say); the old one stops existing. Answers the
// open link: the same id, a new slug and url.
func (h *PayHandler) OwnerReplaceOpenLink(c *gin.Context) {
	m, mode, ok := h.payOwnerLinkWrite(c, nil, false)
	if !ok {
		return
	}
	id, ok := payOpenLinkID(c)
	if !ok {
		return
	}
	l, err := h.payments.ReplaceOpenLink(c.Request.Context(), m, mode, id)
	if err != nil {
		payLinkFail(c, err)
		return
	}
	h.answerOpenLink(c, http.StatusOK, "Payment link replaced", m, l)
}

// payOwnerOpenLink is the owner's merchant and the open link :id names in the
// mode asked; another merchant's link, or one in the other mode, is not found.
// false means the response has been written.
func (h *PayHandler) payOwnerOpenLink(c *gin.Context) (*models.PayMerchant, *models.PayOpenLink, bool) {
	_, m, ok := h.payOwnerMerchant(c)
	if !ok {
		return nil, nil, false
	}
	mode, ok := payOwnerMode(c)
	if !ok {
		return nil, nil, false
	}
	id, ok := payOpenLinkID(c)
	if !ok {
		return nil, nil, false
	}
	l, err := h.payments.OpenLinkForMerchant(c.Request.Context(), m.ID, mode, id)
	if err != nil {
		payLinkFail(c, err)
		return nil, nil, false
	}
	return m, l, true
}

// payOpenLinkID is :id; a malformed one is not found, like any other.
func payOpenLinkID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		payLinkFail(c, services.ErrPayOpenLinkNotFound)
		return uuid.Nil, false
	}
	return id, true
}

// openLinkView is l as its owner reads it. false means the response has been
// written.
func (h *PayHandler) openLinkView(c *gin.Context, m *models.PayMerchant, l *models.PayOpenLink) (*services.PayOpenLinkView, bool) {
	views, err := h.payments.OpenLinkViews(c.Request.Context(), m, []models.PayOpenLink{*l}, h.liveEnabled)
	if err != nil {
		payOwnerFail(c, err)
		return nil, false
	}
	return &views[0], true
}

func (h *PayHandler) answerOpenLink(c *gin.Context, status int, message string, m *models.PayMerchant, l *models.PayOpenLink) {
	if view, ok := h.openLinkView(c, m, l); ok {
		utils.SuccessResponse(c, status, message, view)
	}
}

// GET /api/pay/public/open/:slug — what the payer's page shows before they
// enter an amount. No session.
func (h *PayHandler) PublicOpenLink(c *gin.Context) {
	v, err := h.payments.OpenLinkPublic(c.Request.Context(), c.Param("slug"), h.liveEnabled)
	if err != nil {
		payOpenPaymentFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment link", v)
}

// payOpenPaymentStarted is a payment a customer just started through an open
// link: its public view, plus the checkout page to send them on to.
type payOpenPaymentStarted struct {
	*services.PayPublicView
	CheckoutURL string `json:"checkoutUrl"`
}

// POST /api/pay/public/open/:slug/payments {amount, note} — a customer starts a
// payment through an open link. No session, and no Idempotency-Key: every
// customer (and every scan) is a new payment. Answers 201 with the payment's
// public view and checkoutUrl.
func (h *PayHandler) PublicOpenPayment(c *gin.Context) {
	var in services.PayOpenPaymentInput
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	p, err := h.payments.CreateFromOpenLink(c.Request.Context(), c.Param("slug"), in, h.liveEnabled)
	if err != nil {
		payOpenPaymentFail(c, err)
		return
	}
	v, err := h.payments.PublicView(c.Request.Context(), p.ID, h.liveEnabled)
	if err != nil {
		payOpenPaymentFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Payment started", payOpenPaymentStarted{PayPublicView: v, CheckoutURL: h.payments.CheckoutURL(p.ID)})
}

// payOpenPaymentFail maps a refusal on the payer's side of an open link. Every
// message is written for the customer; anything unrecognised is logged, never
// echoed.
func payOpenPaymentFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrPayOpenLinkNotFound):
		utils.ErrorResponseWithCode(c, http.StatusNotFound, "This payment link doesn't exist", "link_not_found")
	case errors.Is(err, services.ErrPayOpenLinkClosed):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "This link isn't taking payments right now", "link_closed")
	case errors.Is(err, services.ErrPayPaymentInvalid):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity, payLinkInvalidMessage(err))
	default:
		log.Printf("[tsa-pay] open link payment failed: %s %s: %v", c.Request.Method, c.FullPath(), err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
	}
}
