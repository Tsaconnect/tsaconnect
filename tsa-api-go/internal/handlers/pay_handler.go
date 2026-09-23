package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// payPaymentAPI is the narrow slice of *services.PayPaymentService that the
// merchant/checkout/admin payment handlers need — kept as an interface
// (rather than the concrete type) so PayHandler can still be built in tests
// without a real PayPaymentService.
type payPaymentAPI interface {
	Create(ctx context.Context, m *models.PayMerchant, mode string, in services.CreatePayPaymentInput) (*models.PayPayment, bool, error)
	GetForMerchant(ctx context.Context, merchantID uuid.UUID, mode string, id uuid.UUID) (*models.PayPayment, error)
	View(p *models.PayPayment) services.PayPaymentView
	CheckoutView(ctx context.Context, user *models.User, id uuid.UUID, liveEnabled bool) (*services.PayCheckoutView, error)
	PublicView(ctx context.Context, id uuid.UUID) (*services.PayPublicView, error)
	SimulateTestOutcome(ctx context.Context, user *models.User, id uuid.UUID, approve bool) (*models.PayPayment, error)
	Cancel(ctx context.Context, merchantID uuid.UUID, mode string, id uuid.UUID) error
	ExpirePendingForMerchant(ctx context.Context, merchantID uuid.UUID) (int, error)
	AdminList(ctx context.Context, merchantID *uuid.UUID, mode, status string, page, limit int) ([]models.PayPayment, int64, error)
	Deliveries(ctx context.Context, paymentID uuid.UUID) ([]models.PayWebhookDelivery, error)
}

// payWebhookAPI is the narrow slice of *services.PayWebhookSender the admin
// resend endpoint needs — the same narrow-interface pattern payPaymentAPI
// uses, rather than depending on the concrete type, so it stays testable
// with a fake. buildTSAPay now always builds a real sender alongside
// payments (Task 8); a nil payWebhookAPI is still tolerated by Enabled(),
// same as an untested payments-only construction.
type payWebhookAPI interface {
	DeliverDue(ctx context.Context, limit int) (int, error)
	Resend(ctx context.Context, deliveryID uuid.UUID) error
}

// payCheckoutStarter is declared now and widened in Task 13.
type payCheckoutStarter interface{}

// PayHandler serves every TSA Pay HTTP surface: merchant administration,
// the merchant API, the app's checkout/test-mode flow, and the admin
// payments/webhook-delivery console.
//
// payments and webhooks stay nil until buildTSAPay wires a real
// PayPaymentService/PayWebhookSender — until then Enabled() is false, so
// main.go/routes.go never mount a route that would call through a nil
// field.
type PayHandler struct {
	merchants   *services.PayMerchantService
	payments    payPaymentAPI
	webhooks    payWebhookAPI
	checkout    payCheckoutStarter
	liveEnabled bool
}

// NewPayHandler constructs a PayHandler. Any of merchants/payments/webhooks
// may be nil — see PayHandler's doc comment and Enabled().
func NewPayHandler(merchants *services.PayMerchantService, payments payPaymentAPI, webhooks payWebhookAPI, liveEnabled bool) *PayHandler {
	return &PayHandler{merchants: merchants, payments: payments, webhooks: webhooks, liveEnabled: liveEnabled}
}

// Enabled reports whether TSA Pay has enough wired up to serve a request.
// Nil-safe on the receiver itself: buildTSAPay returns a nil *PayHandler
// when TSA Pay is dormant, and routes.go calls payh.Enabled() before ever
// registering a route, so a dormant handler must not panic here.
func (h *PayHandler) Enabled() bool {
	return h != nil && h.merchants != nil && h.payments != nil
}

// MerchantService exposes the merchant provisioning/administration service
// for main.go to wire into MerchantRequestHandler.PayMerchants. Nil-safe.
func (h *PayHandler) MerchantService() *services.PayMerchantService {
	if h == nil {
		return nil
	}
	return h.merchants
}

// LiveEnabled reports whether TSA Pay may authorize live-mode activity.
// Nil-safe.
func (h *PayHandler) LiveEnabled() bool {
	return h != nil && h.liveEnabled
}

// payMerchantAdminView is the JSON shape an admin read of a merchant gets:
// the service's PayMerchantView (parsed return domains + key-presence
// booleans) with BOTH key prefixes hidden. A prefix is a truncated but real
// piece of the issued key, so IssueKeys' one-time response is the only
// place either is ever returned — see
// TestPayAdmin_IssueKeysShowsThemOnceAndNeverInReads.
//
// A json:"-" tag here would NOT shadow the embedded model's own
// liveKeyPrefix/testKeyPrefix fields: encoding/json drops a "-"-tagged
// field before its depth is even considered, so the deeper (real) field
// would still win. Re-declaring the same JSON key at depth 0 instead — left
// at its zero value — wins the depth tie-break and marshals as "", which
// omitempty then drops entirely.
type payMerchantAdminView struct {
	services.PayMerchantView
	LiveKeyPrefix string `json:"liveKeyPrefix,omitempty"`
	TestKeyPrefix string `json:"testKeyPrefix,omitempty"`
}

func newPayMerchantAdminView(v services.PayMerchantView) payMerchantAdminView {
	return payMerchantAdminView{PayMerchantView: v}
}

// payMerchantAdminViewFor builds the redacted read shape directly from a
// model, for the two handlers (Get, Update) that have one in hand rather
// than a services.PayMerchantView from List.
func payMerchantAdminViewFor(m *models.PayMerchant) payMerchantAdminView {
	return newPayMerchantAdminView(services.PayMerchantView{
		PayMerchant:   *m,
		ReturnDomains: m.ReturnDomainList(),
		HasLiveKey:    m.LiveKeyHash != nil,
		HasTestKey:    m.TestKeyHash != nil,
	})
}

// GET /admin/pay/merchants?page=&limit=
func (h *PayHandler) AdminListMerchants(c *gin.Context) {
	page := clamp(atoiDefault(c.Query("page"), 1), 1, 1<<30)
	limit := clamp(atoiDefault(c.Query("limit"), 20), 1, 100)

	merchants, total, err := h.merchants.List(c.Request.Context(), page, limit)
	if err != nil {
		log.Printf("[tsa-pay] admin list merchants failed: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to list merchants")
		return
	}
	views := make([]payMerchantAdminView, len(merchants))
	for i, v := range merchants {
		views[i] = newPayMerchantAdminView(v)
	}
	utils.SuccessResponse(c, http.StatusOK, "Merchants", gin.H{
		"merchants": views,
		"total":     total,
		"page":      page,
		"limit":     limit,
	})
}

// GET /admin/pay/merchants/:id
func (h *PayHandler) AdminGetMerchant(c *gin.Context) {
	id, ok := payMerchantIDParam(c)
	if !ok {
		return
	}
	m, err := h.merchants.Get(c.Request.Context(), id)
	if err != nil {
		payMerchantAdminFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Merchant", payMerchantAdminViewFor(m))
}

// PUT /admin/pay/merchants/:id — a plain admin may change business name,
// status and fee share; changing where money settles or events go
// (settlement, crypto target, webhooks, return domains) needs super_admin.
func (h *PayHandler) AdminUpdateMerchant(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	id, ok := payMerchantIDParam(c)
	if !ok {
		return
	}
	var in services.PayMerchantUpdate
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if in.Sensitive() && admin.Role != models.RoleSuperAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Only a super admin can change where a merchant is paid or notified")
		return
	}

	m, err := h.merchants.Update(c.Request.Context(), admin.ID, id, in)
	if err != nil {
		payMerchantAdminFail(c, err)
		return
	}
	// A suspended merchant can no longer be paid, so nothing should sit
	// around pretending it still can be. Best-effort: the merchant update
	// itself already landed, and a failure here still leaves the pending
	// payments refusable at Lock time (a suspended merchant fails Lock too).
	if in.Status != nil && *in.Status == models.PayMerchantSuspended {
		if _, err := h.payments.ExpirePendingForMerchant(c.Request.Context(), m.ID); err != nil {
			log.Printf("[tsa-pay] expire pending payments for suspended merchant %s: %v", m.ID, err)
		}
	}
	utils.SuccessResponse(c, http.StatusOK, "Merchant updated", payMerchantAdminViewFor(m))
}

type payIssueKeysRequest struct {
	Scope string `json:"scope"`
}

// POST /admin/pay/merchants/:id/keys {"scope": "all"|"live"|"test"|"webhook"}
func (h *PayHandler) AdminIssueKeys(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	id, ok := payMerchantIDParam(c)
	if !ok {
		return
	}
	var req payIssueKeysRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	scope := services.PayKeyScope(strings.TrimSpace(req.Scope))
	switch scope {
	case services.PayKeysAll, services.PayKeysLive, services.PayKeysTest, services.PayKeysWebhook:
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid key scope")
		return
	}

	keys, err := h.merchants.IssueKeys(c.Request.Context(), admin.ID, id, scope)
	if err != nil {
		payMerchantAdminFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Keys issued", keys)
}

// payAdminPaymentView is one row of the admin payments listing: the same
// read shape a merchant gets, plus the merchant's name (an admin looking
// across every merchant has no other way to tell payments apart).
type payAdminPaymentView struct {
	services.PayPaymentView
	MerchantName string `json:"merchantName"`
}

// GET /admin/pay/payments?merchantId=&mode=&status=&page=&limit=
func (h *PayHandler) AdminListPayments(c *gin.Context) {
	var merchantID *uuid.UUID
	if raw := strings.TrimSpace(c.Query("merchantId")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid merchant id")
			return
		}
		merchantID = &id
	}
	mode := strings.TrimSpace(c.Query("mode"))
	status := strings.TrimSpace(c.Query("status"))
	page := clamp(atoiDefault(c.Query("page"), 1), 1, 1<<30)
	limit := clamp(atoiDefault(c.Query("limit"), 20), 1, 100)

	rows, total, err := h.payments.AdminList(c.Request.Context(), merchantID, mode, status, page, limit)
	if err != nil {
		log.Printf("[tsa-pay] admin list payments failed: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to list payments")
		return
	}

	// Merchant names are looked up once per distinct merchant id in the
	// page, not once per row — a page is at most `limit` (<=100) rows, so
	// this stays cheap without needing a join in the service layer.
	names := make(map[uuid.UUID]string, len(rows))
	views := make([]payAdminPaymentView, len(rows))
	for i := range rows {
		p := &rows[i]
		name, looked := names[p.MerchantID]
		if !looked {
			if m, err := h.merchants.Get(c.Request.Context(), p.MerchantID); err == nil {
				name = m.BusinessName
			}
			names[p.MerchantID] = name
		}
		views[i] = payAdminPaymentView{PayPaymentView: h.payments.View(p), MerchantName: name}
	}
	utils.SuccessResponse(c, http.StatusOK, "Payments", gin.H{
		"payments": views,
		"total":    total,
		"page":     page,
		"limit":    limit,
	})
}

// GET /admin/pay/payments/:id/deliveries
func (h *PayHandler) AdminPaymentDeliveries(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid payment id")
		return
	}
	rows, err := h.payments.Deliveries(c.Request.Context(), id)
	if err != nil {
		log.Printf("[tsa-pay] admin list deliveries for payment %s failed: %v", id, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to list deliveries")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Deliveries", gin.H{"deliveries": rows})
}

// POST /admin/pay/deliveries/:id/resend
func (h *PayHandler) AdminResendWebhook(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid delivery id")
		return
	}
	if err := h.webhooks.Resend(c.Request.Context(), id); err != nil {
		if errors.Is(err, services.ErrPayWebhookDeliveryNotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, err.Error())
			return
		}
		log.Printf("[tsa-pay] admin resend webhook delivery %s failed: %v", id, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to resend webhook")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Webhook queued for redelivery", nil)
}

func payMerchantIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid merchant id")
		return uuid.Nil, false
	}
	return id, true
}

// payMerchantAdminFail logs the real error and answers with the sanitized
// mapping the brief specifies: not-found -> 404, an invalid-settings
// refusal -> 422 with its (already user-facing) message, anything else ->
// a generic 500 with the detail only in the log.
func payMerchantAdminFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrPayMerchantNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, err.Error())
	case errors.Is(err, services.ErrPayMerchantInvalid):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, services.ErrPayMerchantStale):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "This merchant was changed by someone else — reload and try again", "merchant_stale")
	default:
		log.Printf("[tsa-pay] admin request failed: %s %s: %v", c.Request.Method, c.FullPath(), err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
	}
}

// payMerchantFromContext reads the *models.PayMerchant and mode
// middleware.PayMerchantAuth put on the context. false means the request
// reached this handler without going through that middleware — a routing
// bug, not a caller error.
func payMerchantFromContext(c *gin.Context) (*models.PayMerchant, string, bool) {
	mv, ok := c.Get("payMerchant")
	if !ok {
		return nil, "", false
	}
	m, ok := mv.(*models.PayMerchant)
	if !ok || m == nil {
		return nil, "", false
	}
	mode, _ := c.Get("payMode")
	modeStr, _ := mode.(string)
	return m, modeStr, true
}

// POST /api/pay/v1/payments — create (or, for a retried reference, read)
// one checkout under the authenticated merchant's own mode.
func (h *PayHandler) CreatePayment(c *gin.Context) {
	m, mode, ok := payMerchantFromContext(c)
	if !ok {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
		return
	}
	var in services.CreatePayPaymentInput
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	p, created, err := h.payments.Create(c.Request.Context(), m, mode, in)
	if err != nil {
		payPaymentFail(c, err)
		return
	}
	if created {
		utils.SuccessResponse(c, http.StatusCreated, "Payment created", h.payments.View(p))
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment", h.payments.View(p))
}

// GET /api/pay/v1/payments/:id — a merchant reads only its own payments,
// and only in the mode of the key it authenticated with.
func (h *PayHandler) GetPayment(c *gin.Context) {
	m, mode, ok := payMerchantFromContext(c)
	if !ok {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid payment id")
		return
	}

	p, err := h.payments.GetForMerchant(c.Request.Context(), m.ID, mode, id)
	if err != nil {
		payPaymentFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment", h.payments.View(p))
}

// payPaymentFail maps a PayPaymentService error to the wire response: not
// found -> 404, unverified (live money-in) -> 403 with a machine code the
// app routes on, "can't be paid right now" / already in progress / a
// transition the payment's current state refuses -> 409, an invalid request
// or an unconfigured merchant -> 422, a reference reused with different
// details -> 409, anything else -> a generic 500 with the detail only in
// the log.
func payPaymentFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrPayPaymentNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, err.Error())
	case errors.Is(err, services.ErrKYCVerificationRequired):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, err.Error(), kycRefusalCode(err))
	case errors.Is(err, services.ErrPayNotPayable), errors.Is(err, services.ErrPayInProgress), errors.Is(err, services.ErrPayTransition):
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrPayPaymentInvalid), errors.Is(err, services.ErrPayMerchantNotReady):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, services.ErrPayReferenceConflict):
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	default:
		log.Printf("[tsa-pay] merchant api request failed: %s %s: %v", c.Request.Method, c.FullPath(), err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
	}
}

// GET /api/pay/checkout/:id — a signed-in user viewing or resuming a
// checkout.
func (h *PayHandler) Checkout(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid payment id")
		return
	}

	v, err := h.payments.CheckoutView(c.Request.Context(), user, id, h.liveEnabled)
	if err != nil {
		payPaymentFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Checkout", v)
}

// GET /api/pay/public/:id — the unauthenticated status page a customer's
// browser (or the merchant's own site) can poll without a session.
func (h *PayHandler) Public(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid payment id")
		return
	}

	v, err := h.payments.PublicView(c.Request.Context(), id)
	if err != nil {
		payPaymentFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment", v)
}

// payTestOutcomeRequest is TestOutcome's body.
type payTestOutcomeRequest struct {
	Outcome string `json:"outcome"`
}

// POST /api/pay/checkout/:id/test-outcome — the app's "simulate" button for
// a test-mode payment: {"outcome":"approve"|"decline"}.
func (h *PayHandler) TestOutcome(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid payment id")
		return
	}
	var in payTestOutcomeRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	var approve bool
	switch in.Outcome {
	case "approve":
		approve = true
	case "decline":
		approve = false
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, `outcome must be "approve" or "decline"`)
		return
	}

	if _, err := h.payments.SimulateTestOutcome(c.Request.Context(), user, id, approve); err != nil {
		payPaymentFail(c, err)
		return
	}
	// The response must be the same payer-facing PayCheckoutView shape
	// Checkout itself returns (what the app types as PayCheckout and
	// substitutes directly into checkout state) — not the merchant-facing
	// PayPaymentView (h.payments.View), which lacks merchantName/payable/
	// youArePaying/settlementMode and leaks merchant-only fields like
	// reference/settlementRef to the payer.
	v, err := h.payments.CheckoutView(c.Request.Context(), user, id, h.liveEnabled)
	if err != nil {
		payPaymentFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment", v)
}

// POST /api/pay/v1/payments/:id/cancel — a merchant cancels its own pending
// payment.
func (h *PayHandler) CancelPayment(c *gin.Context) {
	m, mode, ok := payMerchantFromContext(c)
	if !ok {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid payment id")
		return
	}

	if err := h.payments.Cancel(c.Request.Context(), m.ID, mode, id); err != nil {
		payPaymentFail(c, err)
		return
	}
	p, err := h.payments.GetForMerchant(c.Request.Context(), m.ID, mode, id)
	if err != nil {
		payPaymentFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment cancelled", h.payments.View(p))
}
