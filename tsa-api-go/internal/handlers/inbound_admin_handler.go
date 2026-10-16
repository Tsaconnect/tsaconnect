package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// inboundAdminService is the subset of *services.InboundAdminService the
// handler uses; an interface so the HTTP layer is testable without a database.
type inboundAdminService interface {
	ListCredits(ctx context.Context, q services.InboundCreditQuery) ([]models.InboundCredit, int64, error)
	Candidates(ctx context.Context, creditID uuid.UUID) (*services.InboundCandidatesView, error)
	Assign(ctx context.Context, adminID, creditID uuid.UUID, claimType string, claimID uuid.UUID, note string) (*models.InboundCredit, error)
	MarkRefunded(ctx context.Context, adminID, creditID uuid.UUID, note string) (*models.InboundCredit, error)
	MarkIgnored(ctx context.Context, adminID, creditID uuid.UUID, note string) (*models.InboundCredit, error)
}

// InboundAdminHandler serves /api/admin/inbound/* — the operator queue for
// credits into the central account that could not be attributed automatically.
//
// Every held credit is customer money nobody can be shown to own, and by design
// decision 9 nothing resolves it but a human. These endpoints are that human's
// only tools, which is why each mutating one carries the acting admin through
// to an audit row.
type InboundAdminHandler struct {
	inbound inboundAdminService
	payers  *services.InboundPayerService
}

func NewInboundAdminHandler(inbound inboundAdminService) *InboundAdminHandler {
	return &InboundAdminHandler{inbound: inbound}
}

// GET /api/admin/inbound/credits?status=&currency=&from=&to=&limit=&offset=
//
// Defaults to the hold queue. `status=all` lifts the filter; any single status
// (matched, refunded, ignored, unmatched) narrows to it, which is what an
// operator reconciling a past decision needs.
func (h *InboundAdminHandler) ListCredits(c *gin.Context) {
	from, err := parseInboundBound(c.Query("from"), false)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid `from` date — use YYYY-MM-DD or RFC3339")
		return
	}
	to, err := parseInboundBound(c.Query("to"), true)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid `to` date — use YYYY-MM-DD or RFC3339")
		return
	}

	q := services.InboundCreditQuery{
		Status:   strings.TrimSpace(c.Query("status")),
		Currency: strings.TrimSpace(c.Query("currency")),
		From:     from,
		To:       to,
		Limit:    clamp(atoiDefault(c.Query("limit"), defaultPageSize), 1, maxPageSize),
		Offset:   clamp(atoiDefault(c.Query("offset"), 0), 0, 1<<30),
	}

	credits, total, err := h.inbound.ListCredits(c.Request.Context(), q)
	if err != nil {
		inboundAdminFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Inbound credits", gin.H{
		"credits": credits,
		"total":   total,
		"limit":   q.Limit,
		"offset":  q.Offset,
	})
}

// GET /api/admin/inbound/credits/:id/candidates — why this credit is held.
//
// Returns the open claims that nearly matched and the reason each was rejected,
// scored with the matcher's own name rules. An operator about to decide where
// money goes needs the evidence, not just the verdict.
func (h *InboundAdminHandler) GetCandidates(c *gin.Context) {
	creditID, ok := inboundCreditIDParam(c)
	if !ok {
		return
	}
	view, err := h.inbound.Candidates(c.Request.Context(), creditID)
	if err != nil {
		inboundAdminFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Candidate claims", view)
}

type inboundAssignReq struct {
	ClaimType string `json:"claimType"`
	ClaimID   string `json:"claimId"`
	Note      string `json:"note"`
}

// POST /api/admin/inbound/credits/:id/assign — attribute a held credit by hand.
//
// The service delegates to the matcher rather than writing the status here, so
// a manual match and an automatic one take the same route into the product's
// settle path.
func (h *InboundAdminHandler) AssignCredit(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	creditID, ok := inboundCreditIDParam(c)
	if !ok {
		return
	}
	var req inboundAssignReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	claimID, err := uuid.Parse(strings.TrimSpace(req.ClaimID))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid claim id")
		return
	}

	credit, err := h.inbound.Assign(c.Request.Context(), admin.ID, creditID, req.ClaimType, claimID, req.Note)
	if err != nil {
		inboundAdminFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Credit assigned", credit)
}

type inboundResolveReq struct {
	Note string `json:"note"`
}

// POST /api/admin/inbound/credits/:id/refund — money was returned to the sender.
func (h *InboundAdminHandler) MarkRefunded(c *gin.Context) {
	h.resolve(c, func(ctx context.Context, adminID, creditID uuid.UUID, note string) (*models.InboundCredit, error) {
		return h.inbound.MarkRefunded(ctx, adminID, creditID, note)
	}, "Credit marked refunded")
}

// POST /api/admin/inbound/credits/:id/ignore — not customer money.
func (h *InboundAdminHandler) MarkIgnored(c *gin.Context) {
	h.resolve(c, func(ctx context.Context, adminID, creditID uuid.UUID, note string) (*models.InboundCredit, error) {
		return h.inbound.MarkIgnored(ctx, adminID, creditID, note)
	}, "Credit marked ignored")
}

// resolve is the shared shape of the two terminal operator actions. Both need
// an authenticated actor before anything else happens: an anonymous caller has
// no id to record, and an unattributable decision about money is the one thing
// this surface exists to prevent.
func (h *InboundAdminHandler) resolve(
	c *gin.Context,
	act func(ctx context.Context, adminID, creditID uuid.UUID, note string) (*models.InboundCredit, error),
	okMessage string,
) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	creditID, ok := inboundCreditIDParam(c)
	if !ok {
		return
	}
	var req inboundResolveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	credit, err := act(c.Request.Context(), admin.ID, creditID, req.Note)
	if err != nil {
		inboundAdminFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, okMessage, credit)
}

func inboundCreditIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid credit id")
		return uuid.Nil, false
	}
	return id, true
}

// parseInboundBound reads a window bound as either a bare date or full RFC3339.
//
// A bare `to` date is inclusive of the day it names: an operator reconciling
// 2026-09-10 means through that evening, and reading it as midnight would
// silently hide every credit that landed on the day they are looking at.
func parseInboundBound(raw string, endOfDay bool) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts.UTC(), nil
	}
	day, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, err
	}
	if endOfDay {
		return day.UTC().Add(24*time.Hour - time.Nanosecond), nil
	}
	return day.UTC(), nil
}

// inboundAdminFail logs the real error and answers with the sanitized mapping.
// Non-sentinel errors are generic to the client — a GORM error embeds SQL — so
// this log line is the only place the actual failure is visible.
func inboundAdminFail(c *gin.Context, err error) {
	log.Printf("[inbound-admin] %s %s failed: %v", c.Request.Method, c.FullPath(), err)
	switch {
	case errors.Is(err, services.ErrInboundCreditNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, err.Error())
	case errors.Is(err, services.ErrInboundCreditNotHeld):
		// 409, not 400: the request is well-formed, the credit has simply
		// already been resolved — usually by another operator a moment ago.
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrInboundBindRefused):
		// The matcher's refusal is written for the operator (which claim, which
		// amounts) and carries no SQL, so it is shown as is.
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrInboundBadRequest):
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrInboundBinderUnavailable):
		utils.ErrorResponse(c, http.StatusServiceUnavailable, err.Error())
	default:
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to process inbound credit")
	}
}
