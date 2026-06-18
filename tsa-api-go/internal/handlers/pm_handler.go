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

// PMStatusGetter is the narrow interface used by GetMyPMStatus to look up an
// active PaymentMerchant by user ID. *services.PMService satisfies it; tests
// can inject a fake without the full service graph.
type PMStatusGetter interface {
	GetActiveByUserID(ctx context.Context, userID uuid.UUID) (*models.PaymentMerchant, error)
}

// PMHandler serves the PM operator endpoints (manual-release queue, release
// action, sell-payout kick, and the read-only PM list) plus the dormant-safe
// user-facing PM-status read.
type PMHandler struct {
	auto     PMAutomation
	pm       *services.PMService
	pmStatus PMStatusGetter // nil when PM stack is unconfigured (dormant)
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

// pmStatusResponse is the JSON payload for GetMyPMStatus.
type pmStatusResponse struct {
	IsPm   bool   `json:"isPm"`
	Status string `json:"status"`
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
	if h.pmStatus == nil {
		utils.SuccessResponse(c, http.StatusOK, "PM status", pmStatusResponse{IsPm: false, Status: ""})
		return
	}

	pm, err := h.pmStatus.GetActiveByUserID(c.Request.Context(), user.ID)
	if err == nil {
		// Active PM found.
		_ = pm
		utils.SuccessResponse(c, http.StatusOK, "PM status", pmStatusResponse{IsPm: true, Status: "active"})
		return
	}
	if errors.Is(err, services.ErrPMNotFound) {
		utils.SuccessResponse(c, http.StatusOK, "PM status", pmStatusResponse{IsPm: false, Status: ""})
		return
	}
	// Unexpected error — log and fail open (UX-safe; never 503).
	log.Printf("[pm handler] GetMyPMStatus user=%s: %v", user.ID, err)
	utils.SuccessResponse(c, http.StatusOK, "PM status", pmStatusResponse{IsPm: false, Status: ""})
}

// pmListItem is the safe projection returned by ListPMs — excludes secret
// fields (NombaCredsRef, raw private keys, etc.).
type pmListItem struct {
	ID                      string  `json:"id"`
	Name                    string  `json:"name"`
	Status                  string  `json:"status"`
	ProvisioningStatus      string  `json:"provisioningStatus"`
	InboundAccountNumber    string  `json:"inboundAccountNumber"`
	NombaWalletID           string  `json:"nombaWalletId"`
	NombaAccountRef         string  `json:"nombaAccountRef"`
	AvailableNGN            string  `json:"availableNgn"`
	ReservedNGN             string  `json:"reservedNgn"`
	PerTradeCapUSD          float64 `json:"perTradeCapUsd"`
	DailyCapUSD             float64 `json:"dailyCapUsd"`
	SettlementBankCode      string  `json:"settlementBankCode"`
	SettlementAccountNumber string  `json:"settlementAccountNumber"`
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
	for _, p := range pms {
		items = append(items, pmListItem{
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
		})
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
		case errors.Is(err, services.ErrInvalidTransition):
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
