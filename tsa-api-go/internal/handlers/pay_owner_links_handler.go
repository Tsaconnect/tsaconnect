package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// Payment links: a payment the owner makes by hand, in the app or on the web,
// and sends to a customer — for a merchant with no website or server of its
// own. Each is an ordinary TSA Pay payment (source "link") on the owner's own
// merchant, paid through the same checkout as any other. The reusable kind,
// open links, is in pay_open_links_handler.go.

// POST /api/pay/merchant/links?mode=live|test {amount, description,
// customerName, reference, expiresInDays?} — the owner asks a customer to pay.
// With no expiresInDays the link stays open until it is paid or cancelled.
// Answers 201 with the payment; its checkoutUrl is the link to send.
func (h *PayHandler) OwnerCreateLink(c *gin.Context) {
	var in services.CreatePayLinkInput
	m, mode, ok := h.payOwnerLinkCreate(c, &in)
	if !ok {
		return
	}
	p, err := h.payments.CreateLink(c.Request.Context(), m, mode, in)
	if err != nil {
		payLinkFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Payment link created", h.payments.View(p))
}

// payOwnerLinkEdit is the preamble of every owner change to a payment link of
// either kind: the owner's merchant and the mode asked, the body read into req
// (nil: no body), and a merchant that isn't suspended. false means the response
// has been written.
func (h *PayHandler) payOwnerLinkEdit(c *gin.Context, req any) (*models.PayMerchant, string, bool) {
	_, m, ok := h.payOwnerMerchant(c)
	if !ok {
		return nil, "", false
	}
	mode, ok := payOwnerMode(c)
	if !ok {
		return nil, "", false
	}
	if req != nil {
		if err := c.ShouldBindJSON(req); err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
			return nil, "", false
		}
	}
	if m.Status != models.PayMerchantActive {
		payOwnerFail(c, services.ErrPayMerchantSuspended)
		return nil, "", false
	}
	return m, mode, true
}

// payOwnerLinkCreate is payOwnerLinkEdit for a new link, which in live mode
// also needs live payments switched on — refused here rather than at checkout,
// where the customer would be the first to find out it can't be paid.
func (h *PayHandler) payOwnerLinkCreate(c *gin.Context, req any) (*models.PayMerchant, string, bool) {
	m, mode, ok := h.payOwnerLinkEdit(c, req)
	if !ok {
		return nil, "", false
	}
	if mode == models.PayModeLive && !m.LiveAllowed(h.liveEnabled) {
		utils.ErrorResponseWithCode(c, http.StatusForbidden, "Live payments aren't switched on for your business yet. Try a test link first.", "live_not_enabled")
		return nil, "", false
	}
	return m, mode, true
}

// GET /api/pay/merchant/links?mode&status&q&page&limit — the owner's payment
// links in one mode, newest first, in the shape GET /payments answers (and with
// its status=cancelled / status=expired split).
func (h *PayHandler) OwnerLinks(c *gin.Context) {
	h.ownerPaymentList(c, models.PaySourceLink)
}

// POST /api/pay/merchant/links/:id/cancel?mode — the owner calls off a link
// nobody has started paying. Answers the payment, now expired and cancelled.
func (h *PayHandler) OwnerCancelLink(c *gin.Context) {
	m, mode, p, ok := h.payOwnerPayment(c)
	if !ok {
		return
	}
	if err := h.payments.CancelLink(c.Request.Context(), m.ID, mode, p.ID); err != nil {
		payLinkFail(c, err)
		return
	}
	cancelled, err := h.payments.GetForMerchant(c.Request.Context(), m.ID, mode, p.ID)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment link cancelled", h.payments.View(cancelled))
}

// payLinkFail maps a payment link refusal to its status and, where the web or
// app acts on it, a machine code. Anything else is an owner-route failure.
func payLinkFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrPayMerchantNotReady):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Set where you want to be paid before you request a payment", "settlement_not_set")
	case errors.Is(err, services.ErrPayLinkReferenceTaken):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "You've already used this reference — use another or leave it blank", "reference_taken")
	case errors.Is(err, services.ErrPayPaymentInvalid):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity, payLinkInvalidMessage(err))
	case errors.Is(err, services.ErrPayTransition):
		// Not pending, or not a link: CancelLink's guard matched nothing.
		utils.ErrorResponseWithCode(c, http.StatusConflict, "This link can't be cancelled — it's already being paid, paid, or closed", "not_cancellable")
	case errors.Is(err, services.ErrPayOpenLinkNotFound):
		utils.ErrorResponseWithCode(c, http.StatusNotFound, "Payment link not found", "link_not_found")
	case errors.Is(err, services.ErrPayOpenLinkStale):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "This link changed since you opened it — reload and try again", "link_stale")
	default:
		payOwnerFail(c, err)
	}
}

// payLinkInvalidMessage is why a link was refused, as a sentence for the owner.
// Every reason an owner's link can hit carries one (services.PayInvalidError);
// the fallback covers the few only an API request can reach.
func payLinkInvalidMessage(err error) string {
	var invalid *services.PayInvalidError
	if errors.As(err, &invalid) && invalid.Human != "" {
		return invalid.Human
	}
	return "We couldn't make this link — check the details and try again"
}
