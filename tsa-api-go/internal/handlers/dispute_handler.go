package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// dispute_handler.go — one complaint queue for every kind of order.
//
// Deliberately not for P2P trades or marketplace orders: resolving those moves
// funds through machinery of their own (the escrow's adminResolve, stock
// restore), so they are workflows rather than queue items and keep their
// existing endpoints.

// disputeService is the slice of services.DisputeService this handler needs.
// An interface rather than the concrete type so the routes can be exercised
// without a database — the same shape bill_handler.go uses.
type disputeService interface {
	File(ctx context.Context, userID uuid.UUID, in services.DisputeInput) (*models.Dispute, error)
	Get(ctx context.Context, userID, id uuid.UUID) (*models.Dispute, error)
	ForSubject(ctx context.Context, userID uuid.UUID, subjectType string, subjectID uuid.UUID) (*models.Dispute, error)
	List(ctx context.Context, userID uuid.UUID, limit int) ([]models.Dispute, error)
	ListAdmin(ctx context.Context, q services.DisputeQuery) ([]services.DisputeAdminRow, int64, error)
	GetAdmin(ctx context.Context, id uuid.UUID) (*services.DisputeAdminRow, error)
	Escalate(ctx context.Context, adminID, id uuid.UUID, externalRef, note string) (*models.Dispute, error)
	Resolve(ctx context.Context, adminID, id uuid.UUID, status, resolution string) (*models.Dispute, error)
}

// rampAdminService is the slice of the converted-buy treasury this handler
// needs for the two ramp admin routes. *services.ConversionOrchestrator
// satisfies it. An interface, like disputeService above, so the routes can be
// exercised without a live treasury.
type rampAdminService interface {
	LegsAndOwedByOrderID(ctx context.Context, orderID uuid.UUID) (*models.PaycrestOrderRecord, *services.RampLegsInfo, error)
	RefundFromTreasury(ctx context.Context, adminID, orderID uuid.UUID) (*models.RampLeg, error)
}

type DisputeHandler struct {
	disputes disputeService
	ramp     rampAdminService
}

func NewDisputeHandler(disputes *services.DisputeService) *DisputeHandler {
	if disputes == nil {
		return &DisputeHandler{}
	}
	return &DisputeHandler{disputes: disputes}
}

func (h *DisputeHandler) Enabled() bool { return h != nil && h.disputes != nil }

// WithRampAdmin wires the treasury refund tooling in. Nil-safe, and dormant
// (routes answer 503) when never called — same pattern as
// PaycrestHandler.WithSponsorship.
func (h *DisputeHandler) WithRampAdmin(orch *services.ConversionOrchestrator) *DisputeHandler {
	if h == nil || orch == nil {
		return h
	}
	h.ramp = orch
	return h
}

func (h *DisputeHandler) RampAdminEnabled() bool { return h != nil && h.ramp != nil }

// rampAdminError maps the refund tooling's sentinels onto status codes. The
// business-rule refusals — nothing owed, undetermined, in flight, not
// applicable, an invalid recipient — are all 409s (nothing about any of them
// is a malformed request) but each keeps its OWN distinct, explicit message:
// collapsing "nothing owed" and "undetermined" into one string is exactly the
// confusion Decision 2 exists to prevent, and the same reasoning extends to
// every other sentinel here.
func rampAdminError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
	case errors.Is(err, services.ErrNothingOwed):
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrOwedUndetermined):
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrRefundInFlight):
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrRefundNotApplicable):
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrTreasuryInvalidRecipient):
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrRelayerUnderfunded):
		utils.ErrorResponse(c, http.StatusServiceUnavailable,
			"The treasury cannot pay for gas on this chain. Top up its native balance, then retry the refund.")
	case errors.Is(err, services.ErrSponsorNotAvailable),
		errors.Is(err, services.ErrTreasuryKilled):
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Treasury is not available for this chain right now")
	default:
		utils.ErrorResponse(c, http.StatusInternalServerError, "Something went wrong. Please try again.")
	}
}

// GET /api/admin/ramp/orders/:id/legs
func (h *DisputeHandler) RampLegs(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order id")
		return
	}
	if h.ramp == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Ramp treasury is not configured")
		return
	}
	_, info, err := h.ramp.LegsAndOwedByOrderID(c.Request.Context(), id)
	if err != nil {
		rampAdminError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Ramp legs", info)
}

// POST /api/admin/ramp/orders/:id/refund
//
// The order id in the path is the ONLY input this endpoint reads. The
// destination (the order's own wallet), the token and the amount all come
// from RefundFromTreasury's own read of the order record and its leg
// ledger — never from anything an admin could type in a request body.
func (h *DisputeHandler) RampRefund(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order id")
		return
	}
	if h.ramp == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Ramp treasury is not configured")
		return
	}
	leg, err := h.ramp.RefundFromTreasury(c.Request.Context(), admin.ID, id)
	if err != nil {
		rampAdminError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Refund sent", leg)
}

// disputeError maps service sentinels onto status codes, surfacing the
// sentinel's own wording — these strings are written to be shown to users.
func disputeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrDisputeBadRequest):
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrDisputeAlreadyExists):
		utils.ErrorResponse(c, http.StatusConflict,
			"You have already opened a dispute on this order.")
	case errors.Is(err, services.ErrDisputeNotDisputable):
		// The common case is an order still awaiting the user's payment, where
		// cancelling is the right action — say so rather than just refusing.
		utils.ErrorResponse(c, http.StatusConflict,
			"There is nothing to dispute on this order yet. If you have not paid, cancel it instead.")
	case errors.Is(err, services.ErrDisputeSubjectNotFound), errors.Is(err, services.ErrOrderDisputeNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "Not found")
	case errors.Is(err, services.ErrDisputeClosed):
		utils.ErrorResponse(c, http.StatusConflict, "This dispute has already been closed.")
	default:
		utils.ErrorResponse(c, http.StatusInternalServerError, "Something went wrong. Please try again.")
	}
}

type fileDisputeRequest struct {
	SubjectType  string   `json:"subjectType" binding:"required"`
	SubjectID    string   `json:"subjectId" binding:"required"`
	Reason       string   `json:"reason" binding:"required"`
	EvidenceURLs []string `json:"evidenceUrls"`
}

// POST /api/disputes
func (h *DisputeHandler) File(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	var req fileDisputeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "subjectType, subjectId and reason are required")
		return
	}
	subjectID, err := uuid.Parse(req.SubjectID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid subjectId")
		return
	}
	d, err := h.disputes.File(c.Request.Context(), user.ID, services.DisputeInput{
		SubjectType: req.SubjectType,
		SubjectID:   subjectID,
		Reason:      req.Reason,
		Evidence:    req.EvidenceURLs,
	})
	if err != nil {
		disputeError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Dispute opened", d)
}

// GET /api/disputes
func (h *DisputeHandler) List(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "25"))
	rows, err := h.disputes.List(c.Request.Context(), user.ID, limit)
	if err != nil {
		disputeError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Disputes", gin.H{"disputes": rows})
}

// GET /api/disputes/:id
func (h *DisputeHandler) Get(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid dispute id")
		return
	}
	d, err := h.disputes.Get(c.Request.Context(), user.ID, id)
	if err != nil {
		disputeError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Dispute", d)
}

// GET /api/disputes/subject/:type/:id — "have I already disputed this order?"
//
// A 404 here is an ordinary answer, not an error: it is how the app decides
// between offering the dispute form and linking to the dispute already filed.
func (h *DisputeHandler) ForSubject(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	subjectID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid subject id")
		return
	}
	d, err := h.disputes.ForSubject(c.Request.Context(), user.ID, c.Param("type"), subjectID)
	if err != nil {
		disputeError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Dispute", d)
}

// ── Admin ─────────────────────────────────────────────────────────────────

// GET /api/admin/disputes
func (h *DisputeHandler) ListAdmin(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	rows, total, err := h.disputes.ListAdmin(c.Request.Context(), services.DisputeQuery{
		Status:      c.Query("status"),
		SubjectType: c.Query("subjectType"),
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		disputeError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Disputes", gin.H{
		"disputes": rows, "total": total, "limit": limit, "offset": offset,
	})
}

// GET /api/admin/disputes/:id
func (h *DisputeHandler) GetAdmin(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid dispute id")
		return
	}
	row, err := h.disputes.GetAdmin(c.Request.Context(), id)
	if err != nil {
		disputeError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Dispute", row)
}

type escalateRequest struct {
	ExternalRef string `json:"externalRef"`
	Note        string `json:"note"`
}

// POST /api/admin/disputes/:id/escalate
func (h *DisputeHandler) EscalateAdmin(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid dispute id")
		return
	}
	var req escalateRequest
	_ = c.ShouldBindJSON(&req)
	d, err := h.disputes.Escalate(c.Request.Context(), admin.ID, id, req.ExternalRef, req.Note)
	if err != nil {
		disputeError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Dispute escalated", d)
}

type resolveDisputeRequest struct {
	Status     string `json:"status" binding:"required"`
	Resolution string `json:"resolution"`
}

// POST /api/admin/disputes/:id/resolve
func (h *DisputeHandler) ResolveAdmin(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid dispute id")
		return
	}
	var req resolveDisputeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "status is required")
		return
	}
	d, err := h.disputes.Resolve(c.Request.Context(), admin.ID, id, req.Status, req.Resolution)
	if err != nil {
		disputeError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Dispute closed", d)
}
