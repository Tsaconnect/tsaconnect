package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// Reconciliation routes on a merchant owner's own account: payments by day,
// period totals, the paid-payments CSV and a webhook sent again.

// payOwnerDateRange reads ?from&to (YYYY-MM-DD days in WAT, to inclusive) as
// [start, end). No dates is no range (zero times). false means the response
// has been written.
func payOwnerDateRange(c *gin.Context) (time.Time, time.Time, bool) {
	start, end, err := services.ParsePayDateRange(c.Query("from"), c.Query("to"))
	if err != nil {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Choose both dates as YYYY-MM-DD, with the end on or after the start", "date_range")
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

// GET /api/pay/merchant/payments/summary?mode&from&to — the period's totals:
// what was paid in it (by the day it was paid), how the payments made in it
// ended, and the refunds confirmed in it. No dates: all time.
func (h *PayHandler) OwnerPaymentSummary(c *gin.Context) {
	_, m, ok := h.payOwnerMerchant(c)
	if !ok {
		return
	}
	mode, ok := payOwnerMode(c)
	if !ok {
		return
	}
	start, end, ok := payOwnerDateRange(c)
	if !ok {
		return
	}
	sum, err := h.payments.Summary(c.Request.Context(), m.ID, mode, start, end)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Summary", sum)
}
