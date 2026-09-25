package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
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
	List(ctx context.Context, f services.PayListFilter) ([]models.PayPayment, int64, error)
	Deliveries(ctx context.Context, paymentID uuid.UUID) ([]models.PayWebhookDelivery, error)
	Payer(ctx context.Context, p *models.PayPayment) (*services.PayPayerView, error)
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

// payCheckoutStarter is the slice of *services.PayCheckoutService the naira
// start routes need. Nil (no Paycrest order service, or ramp sell off) means
// those routes answer 503 — see WithCheckout.
type payCheckoutStarter interface {
	Quote(ctx context.Context, user *models.User, id uuid.UUID, in services.PayConvertedInput) (*services.PayQuote, error)
	PayDirect(ctx context.Context, user *models.User, id uuid.UUID, in services.PayDirectInput) (*models.PaycrestOrderRecord, error)
	PayConverted(ctx context.Context, user *models.User, id uuid.UUID, in services.PayConvertedInput) (*models.RampSellIntent, *services.SellSwapQuote, error)
	CryptoQuote(ctx context.Context, user *models.User, id uuid.UUID, in services.PayConvertedInput) (*services.PayCryptoQuote, error)
	PayCrypto(ctx context.Context, user *models.User, id uuid.UUID, in services.PayConvertedInput) (*models.RampSellIntent, *services.SellSwapQuote, error)
}

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
	events      *events.Bus
	liveEnabled bool
}

// NewPayHandler constructs a PayHandler. Any of merchants/payments/webhooks
// may be nil — see PayHandler's doc comment and Enabled().
func NewPayHandler(merchants *services.PayMerchantService, payments payPaymentAPI, webhooks payWebhookAPI, liveEnabled bool) *PayHandler {
	return &PayHandler{merchants: merchants, payments: payments, webhooks: webhooks, liveEnabled: liveEnabled}
}

// WithCheckout turns the naira start routes on. Callers pass a real starter
// or nothing at all — never a typed-nil pointer, which would read as wired.
func (h *PayHandler) WithCheckout(c payCheckoutStarter) *PayHandler {
	h.checkout = c
	return h
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
	if in.Sensitive() && !hasSuperAdminPrivilege(c, admin) {
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
	deliveries, err := h.payments.Deliveries(c.Request.Context(), id)
	if err != nil {
		log.Printf("[tsa-pay] admin list deliveries for payment %s failed: %v", id, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to list deliveries")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Deliveries", gin.H{"deliveries": deliveries})
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
	case errors.Is(err, services.ErrPayForbidden):
		utils.ErrorResponse(c, http.StatusForbidden, err.Error())
	case errors.Is(err, services.ErrPayInProgress):
		// The app resumes the attempt it already has rather than start another.
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "pay_in_progress")
	case errors.Is(err, services.ErrPayNotPayable), errors.Is(err, services.ErrPayTransition):
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

	if _, err := h.payments.SimulateTestOutcome(c.Request.Context(), actingUser(c, user), id, approve); err != nil {
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

// payStartBody is what the naira start routes read besides the payment id:
// direct names the stablecoin the customer funds the order with, converted and
// quote name the token she pays with and where it is. WalletAddress is
// optional and goes through callerWallet like every other money route.
type payStartBody struct {
	ChainKey      string `json:"chainKey"`
	Token         string `json:"token"`
	SourceChainID int64  `json:"sourceChainId"`
	SourceToken   string `json:"sourceToken"`
	SourceAddress string `json:"sourceAddress"`
	WalletAddress string `json:"walletAddress"`
}

// payStartCaller is the preamble the start and quote routes share: the
// routes exist only when a starter is wired, then a signed-in caller and a
// payment id.
func (h *PayHandler) payStartCaller(c *gin.Context) (*models.User, uuid.UUID, bool) {
	if h.checkout == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Live payments aren't available right now")
		return nil, uuid.Nil, false
	}
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil, uuid.Nil, false
	}
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid payment id")
		return nil, uuid.Nil, false
	}
	return user, id, true
}

// payConvertedSource validates the token-she-pays-with fields and reconciles
// her wallet. sourceAddress is where the swap is SENT from — possibly not an
// EVM address — so only its presence is checked here; the quoter validates it
// for the chain's own family.
func payConvertedSource(c *gin.Context, user *models.User, chainID int64, token, address, claimedWallet string) (services.PayConvertedInput, bool) {
	token, address = strings.TrimSpace(token), strings.TrimSpace(address)
	if chainID <= 0 || token == "" || address == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "sourceChainId, sourceToken and sourceAddress are required")
		return services.PayConvertedInput{}, false
	}
	// The USDC receiver is the account's own reconciled wallet, never the
	// request's word for it.
	wallet, ok := callerWallet(c, user, claimedWallet)
	if !ok {
		return services.PayConvertedInput{}, false
	}
	user.WalletAddress = wallet
	return services.PayConvertedInput{SourceChainID: chainID, SourceToken: token, SourceAddress: address}, true
}

// payQuoteRequest is the preamble both quote routes share: the caller, the
// payment id and the token she would pay with, from the query string. The
// wallet is the account's own; a walletAddress query is ignored.
func (h *PayHandler) payQuoteRequest(c *gin.Context) (*models.User, uuid.UUID, services.PayConvertedInput, bool) {
	user, id, ok := h.payStartCaller(c)
	if !ok {
		return nil, uuid.Nil, services.PayConvertedInput{}, false
	}
	chainID, _ := strconv.ParseInt(strings.TrimSpace(c.Query("sourceChainId")), 10, 64)
	// A GET prices; it must not persist a wallet claim before the payment is even
	// validated. The account's stored wallet is used, whatever walletAddress says.
	in, ok := payConvertedSource(c, user, chainID, c.Query("sourceToken"), c.Query("sourceAddress"), "")
	return user, id, in, ok
}

// GET /api/pay/checkout/:id/quote?sourceChainId=&sourceToken=&sourceAddress=
// — what paying with this token would cost. A price: it locks nothing.
func (h *PayHandler) PayQuote(c *gin.Context) {
	user, id, in, ok := h.payQuoteRequest(c)
	if !ok {
		return
	}
	q, err := h.checkout.Quote(c.Request.Context(), user, id, in)
	if err != nil {
		payStartFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment quote", q)
}

// GET /api/pay/checkout/:id/crypto-quote?sourceChainId=&sourceToken=&sourceAddress=
// — the crypto merchant's twin of /quote: what the token costs and how much of
// the merchant's own token they receive ({fromAmount, destAmount, destDecimals}).
func (h *PayHandler) PayCryptoQuote(c *gin.Context) {
	user, id, in, ok := h.payQuoteRequest(c)
	if !ok {
		return
	}
	q, err := h.checkout.CryptoQuote(c.Request.Context(), user, id, in)
	if err != nil {
		payStartFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment quote", q)
}

// POST /api/pay/checkout/:id/direct {chainKey, token} — pay with a stablecoin
// Paycrest takes as it is. Answers the payout order she then funds.
func (h *PayHandler) PayDirect(c *gin.Context) {
	user, id, ok := h.payStartCaller(c)
	if !ok {
		return
	}
	var body payStartBody
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if strings.TrimSpace(body.ChainKey) == "" || strings.TrimSpace(body.Token) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "chainKey and token are required")
		return
	}
	wallet, ok := callerWallet(c, user, body.WalletAddress)
	if !ok {
		return
	}
	user.WalletAddress = wallet
	order, err := h.checkout.PayDirect(c.Request.Context(), user, id, services.PayDirectInput{ChainKey: body.ChainKey, Token: body.Token})
	if err != nil {
		payStartFail(c, err)
		return
	}
	// The order is the record POST /api/paycrest/sell returns, under "order" —
	// with the merchant's bank details masked (payOrderView).
	utils.SuccessResponse(c, http.StatusCreated, "Send the amount shown to complete your payment", gin.H{"order": payOrderView(order)})
}

// POST /api/pay/checkout/:id/converted {sourceChainId, sourceToken,
// sourceAddress} — pay with any other token: starts the payment's sell intent
// and answers the executable quote she signs, in the shape
// POST /api/paycrest/sell-intents answers.
func (h *PayHandler) PayConverted(c *gin.Context) {
	h.startIntent(c, payCheckoutStarter.PayConverted)
}

// POST /api/pay/checkout/:id/crypto {sourceChainId, sourceToken, sourceAddress}
// — pay a crypto merchant with any token: starts the delivery sale and answers
// the same {intent, lifiQuote, gasTopUp} as /converted.
func (h *PayHandler) PayCrypto(c *gin.Context) {
	h.startIntent(c, payCheckoutStarter.PayCrypto)
}

// startIntent is the body of both intent-starting routes: read the token she
// pays with, start the intent through start, and answer the sell-intent shape.
func (h *PayHandler) startIntent(c *gin.Context, start func(payCheckoutStarter, context.Context, *models.User, uuid.UUID, services.PayConvertedInput) (*models.RampSellIntent, *services.SellSwapQuote, error)) {
	user, id, ok := h.payStartCaller(c)
	if !ok {
		return
	}
	var body payStartBody
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	in, ok := payConvertedSource(c, user, body.SourceChainID, body.SourceToken, body.SourceAddress, body.WalletAddress)
	if !ok {
		return
	}
	intent, quote, err := start(h.checkout, c.Request.Context(), user, id, in)
	if err != nil {
		payStartFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Payment started — sign the swap", gin.H{
		"intent": payIntentView(intent), "lifiQuote": quote.LiFiQuote, "gasTopUp": quote.GasTopUp,
	})
}

// The answer for a start that may have left a payout order open: the same on the
// pay start routes and on the ordinary sell-intent order route.
const (
	payAttemptUncertainCode    = "pay_attempt_uncertain"
	payAttemptUncertainMessage = "We couldn't safely start this payment — it has been paused for review. Please contact support before trying again."
)

// payStartFail maps a start route's error. The payment's own refusals go
// through payPaymentFail; a sell that cannot be opened is the ramp's own
// vocabulary (paycrestError / sellSwapError), so those sentinels are mapped
// exactly as the app already receives them from the sell routes.
func payStartFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrSellAttemptMayExist):
		// Checked before rate_moved, which it may wrap: the payment is failed
		// for support, so this is not a retry.
		utils.ErrorResponseWithCode(c, http.StatusConflict, payAttemptUncertainMessage, payAttemptUncertainCode)
	case errors.Is(err, services.ErrPayInternal):
		// Ours, not the provider's: never say "could not reach the transfer provider".
		log.Printf("[tsa-pay] start route failed: %s %s: %v", c.Request.Method, c.FullPath(), err)
		utils.ErrorResponse(c, http.StatusInternalServerError, services.ErrPayInternal.Error())
	case errors.Is(err, services.ErrPayWrongRoute), errors.Is(err, services.ErrPaySameToken):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, services.ErrSellRateMoved):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "rate_moved")
	case errors.Is(err, services.ErrSellIntentOpen):
		utils.ErrorResponse(c, http.StatusConflict, "You already have a sale in progress")
	case services.IsPayServiceError(err):
		payPaymentFail(c, err)
	default:
		sellSwapError(c, err)
	}
}
