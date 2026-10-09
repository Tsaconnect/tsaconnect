package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// payRefundAPI is the slice of *services.PayRefundService the refund routes
// need. Nil — refunds off (TSA_PAY_REFUNDS) or unwired — makes every refund
// route answer 503 refunds_unavailable (refundsOn).
type payRefundAPI interface {
	Request(ctx context.Context, m *models.PayMerchant, mode string, paymentID uuid.UUID, in services.PayRefundRequest) (*models.PayRefund, error)
	Cancel(ctx context.Context, merchantID, id uuid.UUID) (*models.PayRefund, error)
	Submit(ctx context.Context, m *models.PayMerchant, id uuid.UUID, ownerWallet string, in services.PayRefundSubmit) (*models.PayRefund, error)
	Detail(ctx context.Context, m *models.PayMerchant, id uuid.UUID) (*services.PayRefundDetail, error)
	List(ctx context.Context, f services.PayRefundFilter) ([]services.PayRefundListItem, int64, error)
	ListForPayment(ctx context.Context, p *models.PayPayment) (*services.PayPaymentRefunds, error)
	AdminList(ctx context.Context, f services.PayRefundAdminFilter) ([]services.PayRefundAdminItem, int64, error)
	Reevaluate(ctx context.Context, id uuid.UUID) (*models.PayRefund, error)
	View(r *models.PayRefund) services.PayRefundView
}

// WithRefunds turns the refund routes on (cmd/server does only when
// TSA_PAY_REFUNDS is set). Pass a real service or nothing — never a typed-nil
// pointer, which would read as wired.
func (h *PayHandler) WithRefunds(r payRefundAPI) *PayHandler {
	h.refunds = r
	return h
}

// RefundsEnabled reports whether the refund routes do anything but refuse.
func (h *PayHandler) RefundsEnabled() bool { return h != nil && h.refunds != nil }

// refundsOn is false, with 503 refunds_unavailable written, while refunds are
// off. Every refund route asks it first: the routes stay mounted either way, so
// a client never reads a 404 as "no such refund".
func (h *PayHandler) refundsOn(c *gin.Context) bool {
	if h.RefundsEnabled() {
		return true
	}
	utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "Refunds aren't available yet.", "refunds_unavailable")
	return false
}

// POST /api/pay/merchant/payments/:id/refunds?mode {amount, reason} — the owner
// asks for a refund of one of their succeeded payments. Live: it waits for the
// owner's app to sign it. Test: it is confirmed at once ("Simulate refund").
func (h *PayHandler) OwnerCreateRefund(c *gin.Context) {
	if !h.refundsOn(c) {
		return
	}
	m, mode, p, ok := h.payOwnerPayment(c)
	if !ok {
		return
	}
	var req struct {
		Amount string `json:"amount"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	via := models.PayRefundViaApp
	if middleware.IsWebSession(c) {
		via = models.PayRefundViaWeb
	}
	r, err := h.refunds.Request(c.Request.Context(), m, mode, p.ID, services.PayRefundRequest{Amount: req.Amount, Reason: req.Reason, Via: via})
	if err != nil {
		payRefundFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Refund requested", gin.H{"refund": h.refunds.View(r)})
}

// GET /api/pay/merchant/payments/:id/refunds?mode — the payment's refunds, its
// cap and what is left, and whether another can be asked for now.
func (h *PayHandler) OwnerPaymentRefunds(c *gin.Context) {
	if !h.refundsOn(c) {
		return
	}
	_, _, p, ok := h.payOwnerPayment(c)
	if !ok {
		return
	}
	out, err := h.refunds.ListForPayment(c.Request.Context(), p)
	if err != nil {
		payRefundFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Refunds", out)
}

// GET /api/pay/merchant/refunds?mode&status&from&to&page&limit — the owner's
// refunds in one mode, open first, by the day they were asked for.
func (h *PayHandler) OwnerRefunds(c *gin.Context) {
	if !h.refundsOn(c) {
		return
	}
	_, m, ok := h.payOwnerMerchant(c)
	if !ok {
		return
	}
	mode, ok := payOwnerMode(c)
	if !ok {
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && !models.IsPayRefundStatus(status) {
		utils.ErrorResponse(c, http.StatusBadRequest, "status must be requested, submitted, confirmed, failed, needs_review or cancelled")
		return
	}
	from, to, ok := payOwnerDateRange(c)
	if !ok {
		return
	}
	page, limit := payOwnerPage(c)
	items, total, err := h.refunds.List(c.Request.Context(), services.PayRefundFilter{MerchantID: m.ID, Mode: mode, Status: status,
		From: from, To: to, Page: page, Limit: limit})
	if err != nil {
		payRefundFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Refunds", gin.H{"refunds": items, "total": total, "page": page, "limit": limit})
}

// payOwnerRefund is the signed-in owner, their merchant and the refund id :id
// names. No mode: the refund row has its own, and every lookup by it is scoped
// to the merchant, so another merchant's refund is not found. false means the
// response has been written.
func (h *PayHandler) payOwnerRefund(c *gin.Context) (*models.User, *models.PayMerchant, uuid.UUID, bool) {
	user, m, ok := h.payOwnerMerchant(c)
	if !ok {
		return nil, nil, uuid.Nil, false
	}
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Refund not found")
		return nil, nil, uuid.Nil, false
	}
	return user, m, id, true
}

// GET /api/pay/merchant/refunds/:id — the refund, its payment and payer, the
// wallet it must be signed with, and the networks it can be sent on.
func (h *PayHandler) OwnerRefund(c *gin.Context) {
	if !h.refundsOn(c) {
		return
	}
	_, m, id, ok := h.payOwnerRefund(c)
	if !ok {
		return
	}
	d, err := h.refunds.Detail(c.Request.Context(), m, id)
	if err != nil {
		payRefundFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Refund", d)
}

// POST /api/pay/merchant/refunds/:id/cancel — withdraw a refund nobody has sent yet.
func (h *PayHandler) OwnerCancelRefund(c *gin.Context) {
	if !h.refundsOn(c) {
		return
	}
	_, m, id, ok := h.payOwnerRefund(c)
	if !ok {
		return
	}
	r, err := h.refunds.Cancel(c.Request.Context(), m.ID, id)
	if err != nil {
		payRefundFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Refund cancelled", gin.H{"refund": h.refunds.View(r)})
}

// payRefundSubmitMaxBytes bounds the owner's submit body.
const payRefundSubmitMaxBytes = 16 << 10

// POST /api/pay/merchant/refunds/:id/submit {chainId, tokenSymbol, signedTx} —
// the owner's app hands over the transfer it signed (not broadcast). It must be
// signed by the owner's proven wallet; the server checks it, records it, then
// broadcasts it.
func (h *PayHandler) OwnerSubmitRefund(c *gin.Context) {
	if !h.refundsOn(c) {
		return
	}
	user, m, id, ok := h.payOwnerRefund(c)
	if !ok {
		return
	}
	// A signed ERC-20 transfer is a few hundred bytes; the body is capped before
	// it is decoded.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, payRefundSubmitMaxBytes)
	var req services.PayRefundSubmit
	if err := c.ShouldBindJSON(&req); err != nil || req.ChainID == 0 || strings.TrimSpace(req.TokenSymbol) == "" || strings.TrimSpace(req.SignedTx) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "chainId, tokenSymbol and signedTx are required")
		return
	}
	wallet, ok := requireProvenWallet(c, user)
	if !ok {
		return
	}
	r, err := h.refunds.Submit(c.Request.Context(), m, id, wallet, req)
	if err != nil {
		payRefundFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Refund sent", gin.H{"refund": h.refunds.View(r)})
}

// payRefundFail maps a refund error to its status, machine code and words for
// the owner. Anything unrecognised — a database or RPC failure — is logged
// redacted and answered as a plain 500, never echoed.
func payRefundFail(c *gin.Context, err error) {
	var over *services.PayRefundOverCapError
	var badTx *services.PayRefundTxError
	switch {
	case errors.As(err, &over):
		utils.ErrorResponseWithCodeData(c, http.StatusUnprocessableEntity, "That's more than can still be refunded on this payment",
			"refund_over_cap", gin.H{"remaining": services.FormatMicroUSD(over.RemainingMicroUSD)})
	case errors.As(err, &badTx):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "This transaction can't be used for this refund: "+badTx.Why, "refund_tx_invalid")
	case errors.Is(err, services.ErrPayPaymentNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "Payment not found")
	case errors.Is(err, services.ErrPayRefundNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "Refund not found")
	case errors.Is(err, services.ErrPayRefundInvalid):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "Enter an amount in USD, more than 0, with at most 6 decimal places", "refund_invalid")
	case errors.Is(err, services.ErrPayRefundNotSucceeded):
		utils.ErrorResponseWithCode(c, http.StatusConflict, services.PayRefundRefusal(err), "payment_not_succeeded")
	case errors.Is(err, services.ErrPayRefundOpen):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "This payment already has a refund in progress — finish or cancel it first", "refund_open")
	case errors.Is(err, services.ErrPayRefundNotRefundable):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, services.PayRefundRefusal(err), "not_refundable")
	case errors.Is(err, services.ErrPayRefundPayerDeleted):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, services.PayRefundRefusal(err), "payer_deleted")
	case errors.Is(err, services.ErrPayRefundState):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "This refund can't be changed now — reload it", "refund_state")
	case errors.Is(err, services.ErrPayRefundNonceUsed):
		utils.ErrorResponseWithCode(c, http.StatusConflict, services.PayRefundRefusal(err), "refund_nonce_used")
	case errors.Is(err, services.ErrPayRefundNonceFar):
		utils.ErrorResponseWithCode(c, http.StatusConflict, services.PayRefundRefusal(err), "refund_nonce_far")
	case errors.Is(err, services.ErrPayRefundTxUsed):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "That transaction is already attached to another refund", "refund_tx_used")
	case errors.Is(err, services.ErrPayRefundUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, services.PayRefundRefusal(err), "refunds_unavailable")
	default:
		payInternalFail(c, err)
	}
}

// GET /api/admin/pay/refunds?status&mode&paymentId&page&limit — every merchant's
// refunds, open first: what support watches (submitted, needs_review).
func (h *PayHandler) AdminListRefunds(c *gin.Context) {
	if !h.refundsOn(c) {
		return
	}
	f := services.PayRefundAdminFilter{Status: strings.TrimSpace(c.Query("status")),
		Page: clamp(atoiDefault(c.Query("page"), 1), 1, 1<<30), Limit: clamp(atoiDefault(c.Query("limit"), 20), 1, 100)}
	if f.Status != "" && !models.IsPayRefundStatus(f.Status) {
		utils.ErrorResponse(c, http.StatusBadRequest, "status must be requested, submitted, confirmed, failed, needs_review or cancelled")
		return
	}
	switch mode := strings.TrimSpace(c.Query("mode")); mode {
	case "", models.PayModeLive, models.PayModeTest:
		f.Mode = mode
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "mode must be live or test")
		return
	}
	if raw := strings.TrimSpace(c.Query("paymentId")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid payment id")
			return
		}
		f.PaymentID = &id
	}
	items, total, err := h.refunds.AdminList(c.Request.Context(), f)
	if err != nil {
		payInternalFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Refunds", gin.H{"refunds": items, "total": total, "page": f.Page, "limit": f.Limit})
}

// POST /api/admin/pay/refunds/:id/reevaluate (super_admin) — re-read the chain
// for a submitted or needs_review refund now, and close it the way the
// evidence points (the exit from needs_review).
func (h *PayHandler) AdminReevaluateRefund(c *gin.Context) {
	if !h.refundsOn(c) {
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Refund not found")
		return
	}
	r, err := h.refunds.Reevaluate(c.Request.Context(), id)
	switch {
	case errors.Is(err, services.ErrPayRefundNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "Refund not found")
		return
	case errors.Is(err, services.ErrPayRefundState):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Only a submitted or needs-review refund can be re-checked", "refund_state")
		return
	case errors.Is(err, services.ErrPayRefundChainUnreadable):
		log.Printf("[tsa-pay] admin reevaluate refund %s: %s", id, services.RedactAlertText(err.Error()))
		utils.ErrorResponseWithCode(c, http.StatusBadGateway, "Couldn't read the chain — try again", "chain_unreadable")
		return
	case err != nil:
		payInternalFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Refund re-checked", gin.H{"refund": h.refunds.View(r)})
}
