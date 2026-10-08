package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// PMAutomation is the narrow interface PMHandler depends on. The real
// *services.PMAutomationService satisfies it — the interface boundary exists
// so handler tests can inject a fake without the full service graph.
type PMAutomation interface {
	ListBuyManualQueue(ctx context.Context) ([]services.ManualQueueItem, error)
	ManualRelease(ctx context.Context, tradeID, adminID uuid.UUID) (*models.P2PTrade, error)
	InitiateSellPayout(ctx context.Context, tradeID uuid.UUID) (string, error)
	WithdrawToSettlement(ctx context.Context, pmID uuid.UUID, amount string) (string, error)
}

// PMStatusGetter is the narrow interface used by GetMyPMStatus / ActivatePM to
// look up a PaymentMerchant by user ID. *services.PMService satisfies it; tests
// can inject a fake without the full service graph.
type PMStatusGetter interface {
	GetByUserID(ctx context.Context, userID uuid.UUID) (*models.PaymentMerchant, error)
}

// PMApprover approves a pending PaymentMerchant by id (admin approval from the
// Payment Merchants page): pending → approved. It does NOT create the virtual
// account — that is deferred to merchant KYC activation (see PMActivator).
// *services.PMOnboardingService satisfies it. nil when no fiat provider is
// configured — approval then returns 503.
type PMApprover interface {
	ApprovePM(ctx context.Context, pmID, adminID uuid.UUID) (*models.PaymentMerchant, error)
}

// PMActivator creates the PM's virtual account from the merchant's own BVN/NIN
// and flips it to active. *services.PMOnboardingService satisfies it. nil when
// no fiat provider is configured — activation then returns 503.
type PMActivator interface {
	ActivateWithKYC(ctx context.Context, pmID, userID uuid.UUID, idType, idNumber string) (*models.PaymentMerchant, error)
	// NeedsIdentity is false under central inbound, where activation mints no
	// virtual account and so has no use for a BVN/NIN.
	NeedsIdentity() bool
}

// LPActivator takes an approved LP live (S34) and says whether LP onboarding
// is on at all. *services.PMOnboardingService satisfies it.
type LPActivator interface {
	ActivateLP(ctx context.Context, pmID, userID uuid.UUID) (*models.PaymentMerchant, error)
	LiquidityChainID() (int64, bool)
}

// PMHandler serves the PM operator endpoints (manual-release queue, release
// action, sell-payout kick, and the read-only PM list) plus the dormant-safe
// user-facing PM-status read.
type PMHandler struct {
	auto      PMAutomation
	pm        *services.PMService
	pmStatus  PMStatusGetter // nil when PM stack is unconfigured (dormant)
	approver  PMApprover     // nil when no fiat provider: approve returns 503
	activator PMActivator    // nil when no fiat provider: activate returns 503
	// lpActivator is nil until boot wires LP onboarding; an LP's activation
	// then 503s liquidity_unavailable and /p2p/pm/me reports no liquidity.
	lpActivator LPActivator
}

// NewPMHandler creates a PMHandler backed by the given PMAutomation and an
// optional PMService for read-only PM list queries and the GetMyPMStatus read.
// Pass nil for pm to keep the handler dormant for those operations.
func NewPMHandler(auto PMAutomation, pm *services.PMService) *PMHandler {
	h := &PMHandler{auto: auto, pm: pm}
	if pm != nil {
		h.pmStatus = pm
	}
	return h
}

// SetApprover wires the PM onboarding service used by ApprovePM. Left nil
// (the default) when no fiat provider is configured, in which case ApprovePM 503s.
func (h *PMHandler) SetApprover(a PMApprover) { h.approver = a }

// SetActivator wires the PM onboarding service used by ActivatePM. Left nil
// (the default) when no fiat provider is configured, in which case ActivatePM 503s.
func (h *PMHandler) SetActivator(a PMActivator) { h.activator = a }

// SetLPActivator wires LP activation. Pass only a non-nil service.
func (h *PMHandler) SetLPActivator(a LPActivator) { h.lpActivator = a }

// liquidityAvailable reports whether LP onboarding is on (a pool chain is
// configured), which gates the app's "Provide liquidity" CTA (O-B5).
func (h *PMHandler) liquidityAvailable() bool {
	if h.lpActivator == nil {
		return false
	}
	_, ok := h.lpActivator.LiquidityChainID()
	return ok
}

// pmStatusResponse is the JSON payload for GetMyPMStatus. Status is the raw PM
// status ("pending"|"approved"|"active"|"paused"|"rejected" or "" when none) so
// the app can show the KYC-activation CTA on "approved".
type pmStatusResponse struct {
	IsPm   bool   `json:"isPm"`
	Status string `json:"status"`
	// ActivationNeedsID tells the app whether to ask for a BVN/NIN.
	ActivationNeedsID bool `json:"activationNeedsId"`
	// TSA Liquidity (spec §15): the row's kind and pool chain; an LP activates
	// with a settlement-name match, never an id; liquidityAvailable gates the
	// app's "Provide liquidity" CTA.
	Kind                           string `json:"kind,omitempty"`
	PoolChainID                    *int64 `json:"poolChainId,omitempty"`
	ActivationNeedsSettlementMatch bool   `json:"activationNeedsSettlementMatch"`
	LiquidityAvailable             bool   `json:"liquidityAvailable"`
}

// activationNeedsID is true unless an activator is wired and says otherwise.
// With no activator, activation 503s; reporting "no id needed" would send the
// app to a form that cannot succeed either way.
func (h *PMHandler) activationNeedsID() bool {
	return h.activator == nil || h.activator.NeedsIdentity()
}

// GetMyPMStatus handles GET /p2p/pm/me — returns the caller's PM status.
// Always responds 200; {isPm:false, status:""} is the safe default for any
// non-active / unconfigured / error case so the UI can render without
// special-casing errors.
func (h *PMHandler) GetMyPMStatus(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}

	// Dormant path: PM stack is not configured — return safe default.
	none := pmStatusResponse{ActivationNeedsID: h.activationNeedsID(), LiquidityAvailable: h.liquidityAvailable()}
	if h.pmStatus == nil {
		utils.SuccessResponse(c, http.StatusOK, "PM status", none)
		return
	}

	pm, err := h.pmStatus.GetByUserID(c.Request.Context(), user.ID)
	if err == nil {
		// isPm is true only for a live (active) merchant — an active LP is not a
		// PM; "approved" reports back so the app can prompt activation.
		resp := none
		resp.IsPm = pm.Status == models.PaymentMerchantStatusActive && !pm.IsLP()
		resp.Status = pm.Status
		resp.Kind = pm.Kind
		if pm.IsLP() {
			resp.ActivationNeedsID = false
			resp.PoolChainID = pm.PoolChainID
			resp.ActivationNeedsSettlementMatch = true
		}
		utils.SuccessResponse(c, http.StatusOK, "PM status", resp)
		return
	}
	if errors.Is(err, services.ErrPMNotFound) {
		utils.SuccessResponse(c, http.StatusOK, "PM status", none)
		return
	}
	// Unexpected error — log and fail open (UX-safe; never 503).
	log.Printf("[pm handler] GetMyPMStatus user=%s: %v", user.ID, err)
	utils.SuccessResponse(c, http.StatusOK, "PM status", none)
}

// pmListItem is the safe projection returned by ListPMs and the lifecycle
// actions — excludes secret/internal fields (NombaCredsRef, NombaAccountID,
// operator wallet, raw private keys, etc.).
type pmListItem struct {
	ID                      string     `json:"id"`
	Name                    string     `json:"name"`
	Status                  string     `json:"status"`
	ProvisioningStatus      string     `json:"provisioningStatus"`
	InboundAccountNumber    string     `json:"inboundAccountNumber"`
	NombaWalletID           string     `json:"nombaWalletId"`
	NombaAccountRef         string     `json:"nombaAccountRef"`
	AvailableNGN            string     `json:"availableNgn"`
	ReservedNGN             string     `json:"reservedNgn"`
	PerTradeCapUSD          float64    `json:"perTradeCapUsd"`
	DailyCapUSD             float64    `json:"dailyCapUsd"`
	SettlementBankCode      string     `json:"settlementBankCode"`
	SettlementAccountNumber string     `json:"settlementAccountNumber"`
	ReviewNote              string     `json:"reviewNote"`
	ReviewedAt              *time.Time `json:"reviewedAt"`
	Kind                    string     `json:"kind"`
	PoolChainID             *int64     `json:"poolChainId,omitempty"`
}

// toPMListItem maps a PaymentMerchant to the safe projection. Single source of
// truth so ListPMs and every lifecycle action expose the exact same fields.
func toPMListItem(p *models.PaymentMerchant) pmListItem {
	return pmListItem{
		ID:                      p.ID.String(),
		Name:                    p.Name,
		Status:                  p.Status,
		ProvisioningStatus:      p.ProvisioningStatus,
		InboundAccountNumber:    p.InboundAccountNumber,
		NombaWalletID:           p.NombaWalletID,
		NombaAccountRef:         p.NombaAccountRef,
		AvailableNGN:            p.AvailableNGN,
		ReservedNGN:             p.ReservedNGN,
		PerTradeCapUSD:          p.PerTradeCapUSD,
		DailyCapUSD:             p.DailyCapUSD,
		SettlementBankCode:      p.SettlementBankCode,
		SettlementAccountNumber: p.SettlementAccountNumber,
		ReviewNote:              p.ReviewNote,
		ReviewedAt:              p.ReviewedAt,
		Kind:                    p.Kind,
		PoolChainID:             p.PoolChainID,
	}
}

// ListPMs handles GET /api/admin/pm — returns a JSON array of all PM rows
// (safe fields only; NombaCredsRef and any secret references are omitted).
// When h.pm is nil (dormant), returns an empty array with 200 OK.
func (h *PMHandler) ListPMs(c *gin.Context) {
	if h.pm == nil {
		utils.SuccessResponse(c, http.StatusOK, "Payment merchants", []pmListItem{})
		return
	}
	pms, err := h.pm.ListAll(c.Request.Context())
	if err != nil {
		log.Printf("[pm handler] ListPMs: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load payment merchants")
		return
	}
	items := make([]pmListItem, 0, len(pms))
	for i := range pms {
		items = append(items, toPMListItem(&pms[i]))
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment merchants", items)
}

// GetManualQueue handles GET /api/admin/pm/manual-queue — lists PM buy trades
// stuck awaiting manual review (no matched inbound, on-chain open confirmed).
func (h *PMHandler) GetManualQueue(c *gin.Context) {
	items, err := h.auto.ListBuyManualQueue(c.Request.Context())
	if err != nil {
		log.Printf("[pm handler] ListBuyManualQueue: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load manual queue")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Manual queue", items)
}

// ReleaseTrade handles POST /api/admin/pm/trades/:id/release — operator
// manually verifies a PM buy trade and triggers the system markPaid + release.
func (h *PMHandler) ReleaseTrade(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	// Admin identity comes from the authenticated user in context (set by JWT
	// middleware, same pattern as getUserFromContext in all handlers).
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	adminID := user.ID

	trade, err := h.auto.ManualRelease(c.Request.Context(), id, adminID)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrTradeNotFound):
			utils.ErrorResponse(c, http.StatusNotFound, err.Error())
		case errors.Is(err, services.ErrInvalidTransition),
			errors.Is(err, services.ErrSettleViaInboundAssign):
			utils.ErrorResponse(c, http.StatusConflict, err.Error())
		default:
			log.Printf("[pm handler] ManualRelease trade=%s: %v", id, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Release failed")
		}
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Trade released", trade)
}

// InitiatePayout handles POST /api/admin/pm/trades/:id/payout — operator
// kicks the sell-payout for a confirmed deposited sell trade.
func (h *PMHandler) InitiatePayout(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	payoutID, err := h.auto.InitiateSellPayout(c.Request.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrTradeNotFound):
			utils.ErrorResponse(c, http.StatusNotFound, err.Error())
		case errors.Is(err, services.ErrInvalidTransition):
			utils.ErrorResponse(c, http.StatusConflict, err.Error())
		default:
			log.Printf("[pm handler] InitiateSellPayout trade=%s: %v", id, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Payout initiation failed")
		}
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payout initiated", gin.H{"payoutId": payoutID})
}

// DeclineTrade handles POST /api/admin/pm/trades/:id/decline — operator marks
// a manual-queue trade as declined. For this phase we audit-log the decision
// and return immediately; the timeout watchdog cancels+refunds on expiry.
//
// TODO(pm-phase2): explicit operator-driven cancel/refund
func (h *PMHandler) DeclineTrade(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	user := getUserFromContext(c)
	adminID := uuid.Nil
	if user != nil {
		adminID = user.ID
	}

	log.Printf("[pm handler] DeclineTrade: operator=%s declined trade=%s; timeout watchdog will cancel+refund on expiry", adminID, id)
	utils.SuccessResponse(c, http.StatusOK, "declined; will be cancelled by timeout", gin.H{"tradeId": id})
}

// withdrawRequest is the JSON body for the Withdraw endpoint.
type withdrawRequest struct {
	Amount string `json:"amount"`
}

// Withdraw handles POST /api/admin/pm/:id/withdraw — admin withdraws a PM's
// available NGN (balance minus open sell reservations) to its settlement bank
// via Nomba. The debit is atomic: never dips below open exposure.
//
// Responses:
//   - 200 OK: payout initiated successfully → {"payoutId": "..."}
//   - 400 Bad Request: invalid PM UUID, missing/empty amount, or PM not
//     configured (no sub-account or settlement bank)
//   - 409 Conflict: amount exceeds the PM's free (withdrawable) balance
//   - 500 Internal Server Error: gateway or DB failure
func (h *PMHandler) Withdraw(c *gin.Context) {
	pmID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var req withdrawRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Amount) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "amount is required")
		return
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(req.Amount), 64); err != nil || v <= 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "amount must be greater than zero")
		return
	}

	payoutID, err := h.auto.WithdrawToSettlement(c.Request.Context(), pmID, req.Amount)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInsufficientWithdrawable):
			utils.ErrorResponse(c, http.StatusConflict, "Amount exceeds withdrawable balance (available minus open sell reservations)")
		case errors.Is(err, services.ErrPMNotFound):
			utils.ErrorResponse(c, http.StatusBadRequest, "Payment merchant not found")
		default:
			// Surface configuration errors (missing sub-account, settlement bank)
			// as 400 — they are operator-fixable and not a 500.
			msg := err.Error()
			if strings.Contains(msg, "no Nomba sub-account configured") ||
				strings.Contains(msg, "no settlement bank configured") {
				utils.ErrorResponse(c, http.StatusBadRequest, "PM not configured for withdrawal: "+msg)
				return
			}
			log.Printf("[pm handler] Withdraw pm=%s: %v", pmID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Withdrawal failed")
		}
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Withdrawal initiated", gin.H{"payoutId": payoutID})
}

// ApprovePM handles POST /api/admin/pm/:id/approve — approves a pending PM
// (pending → approved). It does NOT create the virtual account; the merchant
// completes onboarding by activating with their BVN/NIN (POST /p2p/pm/activate).
// Only valid from pending. 503 when no fiat provider is configured.
func (h *PMHandler) ApprovePM(c *gin.Context) {
	pmID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	if h.approver == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "PM approval unavailable: no fiat provider configured")
		return
	}

	pm, err := h.approver.ApprovePM(c.Request.Context(), pmID, admin.ID)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrPMProvisioningUnavailable):
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "PM approval unavailable: no fiat provider configured")
		case errors.Is(err, services.ErrPMNotFound):
			utils.ErrorResponse(c, http.StatusNotFound, "Payment merchant not found")
		case errors.Is(err, services.ErrInvalidPMTransition):
			utils.ErrorResponse(c, http.StatusConflict, "Only a pending payment merchant can be approved")
		default:
			log.Printf("[pm handler] ApprovePM pm=%s: %v", pmID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Approval failed")
		}
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment merchant approved", toPMListItem(pm))
}

// activateRequest is the JSON body for ActivatePM. idNumber is the merchant's
// BVN or NIN — forwarded to the fiat provider and NEVER stored or logged.
type activateRequest struct {
	IDType   string `json:"idType"`   // "BVN" | "NIN"
	IDNumber string `json:"idNumber"` // exactly 11 digits
}

// ActivatePM handles POST /api/p2p/pm/activate — the merchant completes their own
// PM onboarding by supplying a BVN/NIN, which is forwarded to the fiat provider to
// mint the NGN virtual account. The id number is passed straight through and never
// persisted or logged. Only valid on the caller's own 'approved' PM. Under central
// inbound no account is minted and no id is asked for (see PMActivator).
func (h *PMHandler) ActivatePM(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	if h.pmStatus == nil || (h.activator == nil && h.lpActivator == nil) {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "PM activation unavailable: no fiat provider configured")
		return
	}

	// Resolve the caller's PM (never trust a client-supplied id — activation is
	// always for the authenticated user's own PM).
	pm, err := h.pmStatus.GetByUserID(c.Request.Context(), user.ID)
	if err != nil {
		if errors.Is(err, services.ErrPMNotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, "No payment merchant to activate")
			return
		}
		log.Printf("[pm handler] ActivatePM lookup user=%s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Activation failed")
		return
	}
	if pm.IsLP() {
		h.activateLP(c, pm, user.ID)
		return
	}
	if h.activator == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "PM activation unavailable: no fiat provider configured")
		return
	}

	// Under central inbound there is no account to mint, so the body is not
	// read at all: an id an older app still sends goes no further than here.
	var idType, idNumber string
	if h.activator.NeedsIdentity() {
		var req activateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
			return
		}
		idType = strings.ToUpper(strings.TrimSpace(req.IDType))
		if idType != "BVN" && idType != "NIN" {
			utils.ErrorResponse(c, http.StatusBadRequest, "idType must be BVN or NIN")
			return
		}
		idNumber = strings.TrimSpace(req.IDNumber)
		if !utils.IsValidNgIDNumber(idNumber) {
			utils.ErrorResponse(c, http.StatusBadRequest, idType+" must be exactly 11 digits")
			return
		}
	}

	out, err := h.activator.ActivateWithKYC(c.Request.Context(), pm.ID, user.ID, idType, idNumber)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrPMProvisioningUnavailable):
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "PM activation unavailable: no fiat provider configured")
		case errors.Is(err, services.ErrPMKYCRequired):
			utils.ErrorResponse(c, http.StatusBadRequest, "A BVN or NIN is required")
		case services.KYCRefusalCode(err) != "":
			utils.ErrorResponseWithCode(c, http.StatusForbidden,
				"Verify your identity before activating as a payment merchant", services.KYCRefusalCode(err))
		case errors.Is(err, services.ErrPMNotFound):
			utils.ErrorResponse(c, http.StatusNotFound, "No payment merchant to activate")
		case errors.Is(err, services.ErrInvalidPMTransition):
			utils.ErrorResponse(c, http.StatusConflict, "Payment merchant is not awaiting activation")
		default:
			// NB: never log the request body / id number — only the PM id + error.
			log.Printf("[pm handler] ActivatePM pm=%s: %v", pm.ID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Activation failed — please try again")
		}
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment merchant activated", toPMListItem(out))
}

// activateLP maps ActivateLP's outcomes (spec §8.2 step 2). The body is never
// read: an LP activates with a settlement-name match, never a BVN/NIN.
func (h *PMHandler) activateLP(c *gin.Context, pm *models.PaymentMerchant, userID uuid.UUID) {
	if h.lpActivator == nil {
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "Providing liquidity isn't available yet", "liquidity_unavailable")
		return
	}
	out, err := h.lpActivator.ActivateLP(c.Request.Context(), pm.ID, userID)
	switch {
	case err == nil:
		utils.SuccessResponse(c, http.StatusOK, "Liquidity provider activated", toPMListItem(out))
	case errors.Is(err, services.ErrSettlementNameMismatch):
		utils.ErrorResponseWithCode(c, http.StatusConflict,
			"The name on your settlement account doesn't match your verified name. Apply again with an account in your own name.", "settlement_name_mismatch")
	case errors.Is(err, services.ErrSettlementAccountUnresolved):
		utils.ErrorResponseWithCode(c, http.StatusBadGateway, "We couldn't verify your settlement account right now. Try again shortly.", "settlement_account_unresolved")
	case errors.Is(err, services.ErrLiquidityUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "Providing liquidity isn't available right now", "liquidity_unavailable")
	case services.KYCRefusalCode(err) != "":
		utils.ErrorResponseWithCode(c, http.StatusForbidden, "Verify your identity before activating", services.KYCRefusalCode(err))
	case errors.Is(err, services.ErrPMNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "No liquidity provider profile to activate")
	case errors.Is(err, services.ErrInvalidPMTransition):
		utils.ErrorResponse(c, http.StatusConflict, "Your liquidity provider profile is not awaiting activation")
	default:
		// err may carry provider text; ActivateLP already redacts its resolve error.
		log.Printf("[pm handler] ActivateLP pm=%s: %v", pm.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Activation failed — please try again")
	}
}

// RejectPM handles POST /api/admin/pm/:id/reject — declines a pending PM. A
// note explaining the reason is required.
func (h *PMHandler) RejectPM(c *gin.Context) {
	pmID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	if h.pm == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Payment merchants unavailable")
		return
	}
	var input adminNoteInput
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.Note) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Note is required for rejection")
		return
	}
	if len(input.Note) > 512 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Note is too long (max 512 characters)")
		return
	}

	pm, err := h.pm.RejectPM(c.Request.Context(), pmID, admin.ID, input.Note)
	if err != nil {
		h.respondLifecycleErr(c, "RejectPM", pmID, err, "Only a pending payment merchant can be rejected")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment merchant rejected", toPMListItem(pm))
}

// PausePM handles POST /api/admin/pm/:id/pause — kill switch for an active PM.
func (h *PMHandler) PausePM(c *gin.Context) {
	pmID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	if h.pm == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Payment merchants unavailable")
		return
	}
	pm, err := h.pm.PausePM(c.Request.Context(), pmID, admin.ID)
	if err != nil {
		h.respondLifecycleErr(c, "PausePM", pmID, err, "Only an active payment merchant can be paused")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment merchant paused", toPMListItem(pm))
}

// ResumePM handles POST /api/admin/pm/:id/resume — re-activates a paused PM.
func (h *PMHandler) ResumePM(c *gin.Context) {
	pmID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	if h.pm == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Payment merchants unavailable")
		return
	}
	pm, err := h.pm.ResumePM(c.Request.Context(), pmID, admin.ID)
	if err != nil {
		h.respondLifecycleErr(c, "ResumePM", pmID, err, "Only a paused payment merchant can be resumed")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment merchant resumed", toPMListItem(pm))
}

// respondLifecycleErr maps the shared PMService lifecycle errors to HTTP
// responses. invalidMsg is the 409 message for an invalid status transition.
func (h *PMHandler) respondLifecycleErr(c *gin.Context, op string, pmID uuid.UUID, err error, invalidMsg string) {
	switch {
	case errors.Is(err, services.ErrReviewNoteRequired):
		utils.ErrorResponse(c, http.StatusBadRequest, "Note is required for rejection")
	case errors.Is(err, services.ErrPMNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "Payment merchant not found")
	case errors.Is(err, services.ErrInvalidPMTransition):
		utils.ErrorResponse(c, http.StatusConflict, invalidMsg)
	default:
		log.Printf("[pm handler] %s pm=%s: %v", op, pmID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Action failed")
	}
}
