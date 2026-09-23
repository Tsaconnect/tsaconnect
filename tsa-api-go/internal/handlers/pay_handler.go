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

// payPaymentAPI is the narrow slice of *services.PayPaymentService that
// CreatePayment/GetPayment need — kept as an interface (rather than the
// concrete type) so PayHandler can still be built in tests without a real
// PayPaymentService. payWebhookAPI stays an empty placeholder, widened by
// the task that builds *services.PayWebhookSender. buildTSAPay passes an
// untyped nil for webhooks until then, which is fine: Enabled() only checks
// payments — see the package doc on PayHandler.
type payPaymentAPI interface {
	Create(ctx context.Context, m *models.PayMerchant, mode string, in services.CreatePayPaymentInput) (*models.PayPayment, bool, error)
	GetForMerchant(ctx context.Context, merchantID uuid.UUID, mode string, id uuid.UUID) (*models.PayPayment, error)
	View(p *models.PayPayment) services.PayPaymentView
}
type payWebhookAPI interface{}

// payCheckoutStarter is declared now and widened in Task 13.
type payCheckoutStarter interface{}

// PayHandler serves every TSA Pay HTTP surface: merchant administration
// (this task), the merchant API and checkout/test-mode flow (later tasks),
// and webhook delivery admin (later tasks).
//
// payments and webhooks stay nil until the tasks that build
// PayPaymentService/PayWebhookSender wire them through buildTSAPay — until
// then Enabled() is false, so main.go/routes.go never mount a route that
// would call through a nil field.
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
// found -> 404, an invalid request or an unconfigured merchant -> 422, a
// reference reused with different details -> 409, anything else -> a
// generic 500 with the detail only in the log.
func payPaymentFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrPayPaymentNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, err.Error())
	case errors.Is(err, services.ErrPayPaymentInvalid), errors.Is(err, services.ErrPayMerchantNotReady):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, services.ErrPayReferenceConflict):
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	default:
		log.Printf("[tsa-pay] merchant api request failed: %s %s: %v", c.Request.Method, c.FullPath(), err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
	}
}
