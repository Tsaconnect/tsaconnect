package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// billFail logs the REAL error server-side and answers the client with the
// sanitized mapping. Client messages are deliberately generic for non-sentinel
// errors (vendor errors embed URLs, GORM errors embed SQL), which makes this
// log line the only place the actual failure is visible — without it a live
// 400 is undebuggable. Vendor-originated errors are already credential-redacted
// at the client layer before they reach here.
func billFail(c *gin.Context, err error) {
	log.Printf("[bills] %s %s failed: %v", c.Request.Method, c.FullPath(), err)
	utils.ErrorResponse(c, billStatus(err), billMessage(err))
}

// billService is the subset of *services.BillService the handler uses; an
// interface so the handler is unit-testable with a fake.
type billService interface {
	Quote(ctx context.Context, in services.BillQuoteInput) (*services.BillQuoteResult, error)
	Pay(ctx context.Context, userID uuid.UUID, walletAddress string, orderID uuid.UUID) (*models.BillOrder, error)
	Validate(ctx context.Context, category, provider, customerID string) (string, error)
	Providers(ctx context.Context, category string) ([]services.BillProvider, error)
	Plans(ctx context.Context, category, provider string) ([]services.BillPlan, error)
	GetOrder(ctx context.Context, userID, orderID uuid.UUID) (*models.BillOrder, error)
	ListOrders(ctx context.Context, userID uuid.UUID, limit int) ([]models.BillOrder, error)
}

// BillHandler serves /api/bills/* — utility bills (airtime/data/electricity/
// cable/betting/exam e-PINs) paid with spendable MCGP and vended via
// ClubKonnect. Registered only when the bill service is fully wired.
type BillHandler struct {
	bills billService
}

func NewBillHandler(bills billService) *BillHandler {
	return &BillHandler{bills: bills}
}

// GET /api/bills/catalog/:category — operators for a category.
func (h *BillHandler) GetProviders(c *gin.Context) {
	list, err := h.bills.Providers(c.Request.Context(), c.Param("category"))
	if err != nil {
		billFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Providers", list)
}

// GET /api/bills/catalog/:category/:provider/plans — fixed-price products.
func (h *BillHandler) GetPlans(c *gin.Context) {
	plans, err := h.bills.Plans(c.Request.Context(), c.Param("category"), c.Param("provider"))
	if err != nil {
		billFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Plans", plans)
}

type billValidateReq struct {
	Category   string `json:"category"`
	Provider   string `json:"provider"`
	CustomerID string `json:"customerId"`
}

// POST /api/bills/validate — resolve/validate the customer before quoting.
func (h *BillHandler) Validate(c *gin.Context) {
	var req billValidateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	name, err := h.bills.Validate(c.Request.Context(), req.Category, req.Provider, req.CustomerID)
	if err != nil {
		billFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Validated", gin.H{"customerName": name})
}

type billQuoteReq struct {
	Category     string `json:"category"`
	Provider     string `json:"provider"`
	CustomerID   string `json:"customerId"`
	CustomerName string `json:"customerName"`
	PhoneNumber  string `json:"phoneNumber"`
	PlanRef      string `json:"planRef"`
	MeterType    string `json:"meterType"`
	NgnAmount    int64  `json:"ngnAmount"`
}

// POST /api/bills/quote — price a bill in MCGP and stage a quoted order.
func (h *BillHandler) Quote(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req billQuoteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	res, err := h.bills.Quote(c.Request.Context(), services.BillQuoteInput{
		UserID:       user.ID,
		Category:     req.Category,
		Provider:     req.Provider,
		CustomerID:   req.CustomerID,
		CustomerName: req.CustomerName,
		PhoneNumber:  req.PhoneNumber,
		PlanRef:      req.PlanRef,
		MeterType:    req.MeterType,
		NgnAmount:    req.NgnAmount,
	})
	if err != nil {
		billFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Quote", res)
}

// POST /api/bills/orders/:id/pay — debit the user's spendable MCGP
// (operator-signed; the user signs nothing and pays no gas) and vend.
func (h *BillHandler) Pay(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order id")
		return
	}
	order, err := h.bills.Pay(c.Request.Context(), user.ID, user.WalletAddress, orderID)
	if err != nil {
		billFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment accepted", order)
}

// GET /api/bills/orders/:id
func (h *BillHandler) GetOrder(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order id")
		return
	}
	order, err := h.bills.GetOrder(c.Request.Context(), user.ID, orderID)
	if err != nil {
		billFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Order", order)
}

// GET /api/bills/orders
func (h *BillHandler) ListOrders(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	orders, err := h.bills.ListOrders(c.Request.Context(), user.ID, 50)
	if err != nil {
		log.Printf("[bills] GET %s failed: %v", c.FullPath(), err)
		utils.ErrorResponse(c, http.StatusInternalServerError, billMessage(err))
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Orders", orders)
}

// billStatus maps service errors to HTTP status codes, and billMessage decides
// what text a client may see. Only sentinel errors pass their message through;
// anything else is internal detail (vendor transport errors embed request
// URLs, GORM errors embed SQL) and is replaced with a generic line — billFail
// above logs the full error server-side.
func billStatus(err error) int {
	switch {
	case errors.Is(err, services.ErrBillsDisabled),
		errors.Is(err, services.ErrClubkonnectDisabled),
		errors.Is(err, services.ErrBillRateStale):
		return http.StatusServiceUnavailable
	case errors.Is(err, services.ErrBillNotFound),
		errors.Is(err, services.ErrBillCustomerNotFound):
		return http.StatusNotFound
	case errors.Is(err, services.ErrBillPayInsufficient):
		return http.StatusPaymentRequired
	default:
		return http.StatusBadRequest
	}
}

func billMessage(err error) string {
	for _, known := range []error{
		services.ErrBillsDisabled, services.ErrClubkonnectDisabled,
		services.ErrBillRateStale, services.ErrBillNotFound,
		services.ErrBillCustomerNotFound, services.ErrBillPayInsufficient,
		services.ErrBillQuoteExpired, services.ErrBillBadState,
		services.ErrBillBadCategory, services.ErrBillBadRequest,
	} {
		if errors.Is(err, known) {
			return err.Error()
		}
	}
	return "Could not process this bill request. Please try again."
}
