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

type DisputeHandler struct {
	disputes disputeService
}

func NewDisputeHandler(disputes *services.DisputeService) *DisputeHandler {
	if disputes == nil {
		return &DisputeHandler{}
	}
	return &DisputeHandler{disputes: disputes}
}

func (h *DisputeHandler) Enabled() bool { return h != nil && h.disputes != nil }

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
